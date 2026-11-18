// Read-only Remote AX protocol informed by Pinky's Apache-2.0 iOS driver.
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	dtx "github.com/danielpaulus/go-ios/ios/dtx_codec"
	"github.com/danielpaulus/go-ios/ios/nskeyedarchiver"
)

const axService = "com.apple.accessibility.axAuditDaemon.remoteAXService"
const axDataSelector = "processDataFromRemoteDevice:"

type axElement struct {
	Text     string `json:"text"`
	Class    string `json:"class"`
	Bounds   string `json:"bounds"`
	Focused  bool   `json:"focused"`
	Enabled  bool   `json:"enabled"`
	Selected bool   `json:"selected"`
}

type axSnapshot struct {
	Elements []axElement `json:"elements"`
	Width    int         `json:"width"`
	Height   int         `json:"height"`
}

type axPushes chan dtx.Message

func (p axPushes) Dispatch(msg dtx.Message) {
	if len(msg.Payload) == 0 {
		return
	}
	if selector, _ := msg.Payload[0].(string); selector == axDataSelector {
		select {
		case p <- msg:
		default:
		}
	}
}

func readAccessibility(ctx context.Context, run runDeviceCommand, id string) (axSnapshot, error) {
	dev, err := readyDevice(ctx, run, id)
	if err != nil {
		return axSnapshot{}, err
	}
	if err := requireUnlocked(ctx, run, id); err != nil {
		return axSnapshot{}, err
	}
	display, err := readPortraitDisplay(ctx, run, id)
	if err != nil {
		return axSnapshot{}, err
	}
	if display.PointScale <= 0 || !finite(display.PointScale) {
		return axSnapshot{}, errors.New("iPhone display does not report a valid accessibility scale")
	}
	entry, err := findUSBDevice(dev)
	if err != nil {
		return axSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	tunnel, err := openDeviceTunnel(ctx, entry)
	if err != nil {
		return axSnapshot{}, err
	}
	defer tunnel.Close()
	stop := context.AfterFunc(ctx, tunnel.Close)
	defer stop()
	entry, announcement, err := tunnel.entry(ctx, entry)
	if err != nil {
		return axSnapshot{}, err
	}
	defer announcement.Close()
	conn, err := dtx.NewTunnelConnection(entry, axService)
	if err != nil {
		return axSnapshot{}, fmt.Errorf("open iPhone accessibility service: %w", err)
	}
	defer conn.Close()
	pushes := make(axPushes, 8)
	conn.MessageDispatcher = pushes
	channel := conn.GlobalChannel()
	channel.RegisterMethodForRemote("hostAPIVersion")
	go func() {
		for {
			msg, err := channel.ReceiveMethodCallWithTimeout(ctx, "hostAPIVersion")
			if err != nil {
				return
			}
			payload, err := nskeyedarchiver.ArchiveBin(uint64(7))
			if err != nil {
				return
			}
			packet, err := dtx.Encode(msg.Identifier, msg.ConversationIndex+1, msg.ChannelCode,
				false, dtx.ResponseWithReturnValueInPayload, payload, dtx.NewPrimitiveDictionary())
			if err != nil || conn.Send(packet) != nil {
				return
			}
		}
	}()
	if err := channel.MethodCallAsync("_notifyOfPublishedCapabilities:", map[string]any{
		"com.apple.private.DTXBlockCompression": uint64(2),
		"com.apple.private.DTXConnection":       uint64(1),
	}); err != nil {
		return axSnapshot{}, err
	}
	if _, err := channel.MethodCallWithContext(ctx, "deviceAPIVersion"); err != nil {
		return axSnapshot{}, fmt.Errorf("negotiate iPhone accessibility service: %w", err)
	}
	if err := channel.MethodCallAsync("clientNeedsAccessibility:", true); err != nil {
		return axSnapshot{}, err
	}
	width, height := display.NativeSize[0], display.NativeSize[1]
	snapshot := axSnapshot{Width: width, Height: height}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return axSnapshot{}, fmt.Errorf("waiting for iPhone accessibility elements: %w", ctx.Err())
		case <-conn.Closed():
			return axSnapshot{}, errors.New("iPhone accessibility connection closed")
		case <-settle:
			return snapshot, requireUnlocked(ctx, run, id)
		case msg := <-pushes:
			tree := decodeAXPush(msg.Auxiliary.GetArguments())
			elements := visibleAXElements(tree, width, height, display.PointScale)
			if len(elements) == 0 {
				continue
			}
			snapshot.Elements = elements
			if settle == nil {
				timer := time.NewTimer(250 * time.Millisecond)
				defer timer.Stop()
				settle = timer.C
			}
		}
	}
}

func visibleAXElements(tree []any, width, height int, scale float64) []axElement {
	var out []axElement
	seen := map[axObject]bool{}
	for _, entry := range tree {
		r, ok := entry.(axRecord)
		if !ok || r.kind != 5 || r.object.id == 0 || seen[r.object] {
			continue
		}
		attrs, _ := r.result.(axDictionary)
		if attrs[32] == false || attrs[26] != true {
			continue
		}
		// Read labels only; never export editable values, secure elements, or identifiers.
		role := axInteger(attrs[61])
		if role == 50 { // XCUIElementTypeSecureTextField
			continue
		}
		text, _ := attrs[33].(string)
		if text == "" {
			text, _ = attrs[42].(string)
		}
		text = strings.Join(strings.Fields(text), " ")
		if text == "" {
			continue
		}
		frame, ok := attrs[21].(axRect)
		if !ok {
			continue
		}
		x1, y1, x2, y2 := frame[0]*scale, frame[1]*scale, (frame[0]+frame[2])*scale, (frame[1]+frame[3])*scale
		if !finite(x1) || !finite(y1) || !finite(x2) || !finite(y2) {
			continue
		}
		left, top := int(math.Floor(max(0, min(float64(width), x1)))), int(math.Floor(max(0, min(float64(height), y1))))
		right, bottom := int(math.Ceil(max(0, min(float64(width), x2)))), int(math.Ceil(max(0, min(float64(height), y2))))
		if left >= right || top >= bottom {
			continue
		}
		class := "iOSAccessibilityElement"
		switch role {
		case 9:
			class = "iOSButton"
		case 22, 49, 52:
			class = "iOSTextField"
		case 48:
			class = "iOSStaticText"
		}
		out = append(out, axElement{
			Text: text, Class: class, Bounds: fmt.Sprintf("[%d,%d][%d,%d]", left, top, right, bottom),
			Focused: attrs[28] == true, Enabled: attrs[27] != false, Selected: attrs[30] == true,
		})
		seen[r.object] = true
		if len(out) == 250 {
			break
		}
	}
	return out
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
