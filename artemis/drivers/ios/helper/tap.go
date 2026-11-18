package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/display"
	"github.com/danielpaulus/go-ios/ios/hid"
	"github.com/google/uuid"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
)

type pixelPoint struct{ x, y int }

type inputDevice struct {
	device        ios.DeviceEntry
	width, height int
}

func touch(ctx context.Context, run runDeviceCommand, id string, from, to pixelPoint, duration time.Duration) error {
	return withDeviceInput(ctx, run, id, func(ctx context.Context, dev inputDevice) error {
		panel := func(p pixelPoint) hid.Point {
			return hid.Point{X: pixelCenter(p.x, dev.width), Y: pixelCenter(p.y, dev.height)}
		}
		input, err := hid.NewSession(dev.device)
		if err != nil {
			return fmt.Errorf("open device touch service: %w", err)
		}
		defer input.Close()
		return performTouch(ctx, input, panel(from), panel(to), duration, from != to)
	}, from, to)
}

func withDeviceInput(ctx context.Context, run runDeviceCommand, id string, action func(context.Context, inputDevice) error, points ...pixelPoint) error {
	dev, err := readyDevice(ctx, run, id)
	if err != nil {
		return err
	}
	if err := requireUnlocked(ctx, run, id); err != nil {
		return err
	}
	d, err := readPortraitDisplay(ctx, run, id)
	if err != nil {
		return err
	}
	width, height := d.NativeSize[0], d.NativeSize[1]
	for _, p := range points {
		if p.x >= width || p.y >= height {
			return fmt.Errorf("touch coordinates are outside the %dx%d display", width, height)
		}
	}
	entry, err := findUSBDevice(dev)
	if err != nil {
		return err
	}
	return withInputStream(ctx, entry, d.DisplayID, func(ctx context.Context, entry ios.DeviceEntry) error {
		return action(ctx, inputDevice{entry, width, height})
	})
}

type portraitDisplay struct {
	DisplayID                                             int
	Primary                                               bool
	NativeSize                                            [2]int
	PointScale                                            float64
	NativeOrientation, CurrentOrientation, BacklightState string
}

func readPortraitDisplay(ctx context.Context, run runDeviceCommand, id string) (portraitDisplay, error) {
	raw, err := run(ctx, "device", "info", "displays", "--device", id,
		"--json-output", "-", "--quiet", "--timeout", "5")
	if err != nil {
		return portraitDisplay{}, err
	}
	var screen struct {
		Result struct {
			Displays []portraitDisplay
		}
	}
	if err := json.Unmarshal(raw, &screen); err != nil {
		return portraitDisplay{}, fmt.Errorf("read device display: %w", err)
	}
	if len(screen.Result.Displays) != 1 {
		return portraitDisplay{}, errors.New("requires exactly one iPhone display")
	}
	d := screen.Result.Displays[0]
	if !d.Primary || d.DisplayID <= 0 || d.NativeOrientation != "rot0" || d.CurrentOrientation != "rot0" {
		return portraitDisplay{}, errors.New("requires the iPhone primary display in upright portrait")
	}
	if d.BacklightState != "activeOn" && d.BacklightState != "activeDimmed" {
		return portraitDisplay{}, errors.New("requires the iPhone display to be awake")
	}
	if d.NativeSize[0] <= 0 || d.NativeSize[1] <= 0 {
		return portraitDisplay{}, errors.New("iPhone display has invalid dimensions")
	}
	return d, nil
}

func findUSBDevice(dev device) (ios.DeviceEntry, error) {
	if dev.udid == "" {
		return ios.DeviceEntry{}, errors.New("CoreDevice did not provide a USB identifier")
	}
	entries, err := ios.ListDevices()
	if err != nil {
		return ios.DeviceEntry{}, fmt.Errorf("list USB devices: %w", err)
	}
	for _, entry := range entries.DeviceList {
		if entry.Properties.SerialNumber == dev.udid && entry.Properties.ConnectionType == "USB" {
			return entry, nil
		}
	}
	return ios.DeviceEntry{}, errors.New("iPhone is no longer available over USB")
}

func pixelCenter(pixel, size int) uint16 {
	return uint16((2*int64(pixel) + 1) * 65536 / (2 * int64(size)))
}

func withInputStream(ctx context.Context, entry ios.DeviceEntry, displayID int, action func(context.Context, ios.DeviceEntry) error) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tunnel, err := openDeviceTunnel(ctx, entry)
	if err != nil {
		return fmt.Errorf("open USB CoreDevice tunnel: %w", err)
	}
	defer tunnel.Close()
	stop := context.AfterFunc(ctx, tunnel.Close)
	defer stop()
	entry, announcement, err := tunnel.entry(ctx, entry)
	if err != nil {
		return fmt.Errorf("discover CoreDevice services: %w", err)
	}
	defer announcement.Close()

	// iOS accepts but discards HID reports without an active media session.
	// Read only enough RTP to confirm startup; no video is decoded or saved.
	addr := tunnelAddress(tunnel.host, 0)
	receiver, err := gonet.DialUDP(tunnel.stack, &addr, nil, ipv6.ProtocolNumber)
	if err != nil {
		return fmt.Errorf("bind device media receiver: %w", err)
	}
	defer receiver.Close()
	service, err := display.New(entry)
	if err != nil {
		return fmt.Errorf("open device display service: %w", err)
	}
	defer service.Close()
	mediaID, err := service.StartVideoStream(ctx, display.VideoStreamRequest{
		ReceiverIP: tunnel.host.String(), ReceiverPort: receiver.LocalAddr().(*net.UDPAddr).Port,
		SenderIP: tunnel.phone.String(), DisplayID: displayID,
	})
	if mediaID != uuid.Nil {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			service.StopMediaStream(cleanup, mediaID)
		}()
	}
	if err != nil {
		return fmt.Errorf("start device display session: %w", err)
	}
	receiver.SetReadDeadline(time.Now().Add(3 * time.Second))
	packet := make([]byte, 65535)
	if _, err := receiver.Read(packet); err != nil {
		return fmt.Errorf("wait for the device display session: %w", err)
	}
	if err := wait(ctx, 250*time.Millisecond); err != nil {
		return err
	}
	return action(ctx, entry)
}

type touchPanel interface {
	TouchDown(hid.Point) error
	TouchUp(hid.Point) error
}

func performTouch(ctx context.Context, input touchPanel, from, to hid.Point, duration time.Duration, moving bool) error {
	if err := input.TouchDown(from); err != nil {
		return fmt.Errorf("send touch (outcome uncertain): %w", err)
	}
	if moving {
		start := time.Now()
		steps := max(1, int(duration/(16*time.Millisecond)))
		for i := 1; i <= steps; i++ {
			if err := wait(ctx, time.Until(start.Add(duration*time.Duration(i)/time.Duration(steps)))); err != nil {
				return fmt.Errorf("swipe outcome uncertain: %w", err)
			}
			x := int(from.X) + (int(to.X)-int(from.X))*i/steps
			y := int(from.Y) + (int(to.Y)-int(from.Y))*i/steps
			if err := input.TouchDown(hid.Point{X: uint16(x), Y: uint16(y)}); err != nil {
				return fmt.Errorf("move touch (outcome uncertain): %w", err)
			}
		}
	} else if err := wait(ctx, duration); err != nil {
		return fmt.Errorf("tap outcome uncertain: %w", err)
	}
	if err := input.TouchUp(to); err != nil {
		return fmt.Errorf("finish touch (outcome uncertain): %w", err)
	}
	return nil
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
