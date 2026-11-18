// The CoreDevice keyboard wire format follows Pinky's Apache-2.0
// internal/driver/ios/hid.go (https://github.com/DIYgod/pinky).
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/xpc"
)

const keyboardSurface = uint64(0x100002001)

func textStrokes(text string, clear bool) ([][]byte, error) {
	if len(text) > 128 {
		return nil, errors.New("type accepts at most 128 printable ASCII characters")
	}
	var strokes [][]byte
	if clear {
		strokes = append(strokes, []byte{0xe3, 0x04}, []byte{0x2a}) // Command+A, Delete
	}
	for _, c := range text {
		switch {
		case c >= 'a' && c <= 'z':
			strokes = append(strokes, []byte{byte(c-'a') + 4})
		case c >= 'A' && c <= 'Z':
			strokes = append(strokes, []byte{0xe1, byte(c-'A') + 4})
		case c == ' ':
			strokes = append(strokes, []byte{0x2c})
		default:
			const plain = "1234567890-=[]\\;'`,./"
			const shifted = "!@#$%^&*()_+{}|:\"~<>?"
			usages := [...]byte{30, 31, 32, 33, 34, 35, 36, 37, 38, 39, 45, 46, 47, 48, 49, 51, 52, 53, 54, 55, 56}
			if i := strings.IndexRune(plain, c); i >= 0 {
				strokes = append(strokes, []byte{usages[i]})
			} else if i := strings.IndexRune(shifted, c); i >= 0 {
				strokes = append(strokes, []byte{0xe1, usages[i]})
			} else {
				return nil, errors.New("type supports only printable ASCII (US keyboard layout)")
			}
		}
	}
	return strokes, nil
}

func keyboardMessage(name string, payload map[string]any) map[string]any {
	return map[string]any{
		"featureIdentifier": "com.apple.coredevice.feature.remote.universalhidservice",
		"messageType":       "Request",
		"payload":           map[string]any{name: payload},
	}
}

func keyboardRegistration() map[string]any {
	const product = "Artemis virtual keyboard"
	storage := map[string]any{
		"Manufacturer":     map[string]any{"string": "Artemis"},
		"Product":          map[string]any{"string": product},
		"ProductID":        map[string]any{"int": int64(0x0250)},
		"VendorID":         map[string]any{"int": int64(0x05ac)},
		"PrimaryUsage":     map[string]any{"int": int64(6)},
		"PrimaryUsagePage": map[string]any{"int": int64(1)},
		"DeviceUsagePairs": map[string]any{"array": []any{
			map[string]any{"dictionary": map[string]any{
				"DeviceUsage": map[string]any{"int": int64(6)}, "DeviceUsagePage": map[string]any{"int": int64(1)},
			}},
		}},
		"Transport": map[string]any{"string": "USB"},
		"ReportDescriptor": map[string]any{"data": []byte{
			0x05, 0x01, 0x09, 0x06, 0xa1, 0x01, 0x05, 0x07, 0x19, 0xe0, 0x29, 0xe7,
			0x15, 0x00, 0x25, 0x01, 0x95, 0x08, 0x75, 0x01, 0x81, 0x02, 0x95, 0x01, 0x75,
			0x08, 0x81, 0x01, 0x05, 0x07, 0x19, 0x00, 0x29, 0xff, 0x15, 0x00, 0x26, 0xff,
			0x00, 0x95, 0x06, 0x75, 0x08, 0x81, 0x00, 0x05, 0x08, 0x19, 0x01, 0x29, 0x05,
			0x95, 0x05, 0x75, 0x01, 0x91, 0x02, 0x95, 0x01, 0x75, 0x03, 0x91, 0x01, 0xc0,
		}},
		"UniversalControlVirtualService": map[string]any{"bool": true},
		"_ServiceID":                     map[string]any{"uint": keyboardSurface},
	}
	return keyboardMessage("createService", map[string]any{"_0": map[string]any{
		"DeviceUsagePairs": []any{map[string]any{"DeviceUsage": int64(6), "DeviceUsagePage": int64(1)}},
		"PrimaryUsage":     uint64(6), "PrimaryUsagePage": uint64(1), "Product": product,
		"ProductID": int64(0x0250), "VendorID": int64(0x05ac),
		"_CoreDevice_codablePropertyStorage": storage, "_ServiceID": keyboardSurface,
	}})
}

func typeKeys(ctx context.Context, entry ios.DeviceEntry, strokes [][]byte) error {
	conn, err := ios.ConnectToXpcServiceTunnelIface(entry, "com.apple.coredevice.hid.universalhidservice")
	if err != nil {
		return fmt.Errorf("open device keyboard service: %w", err)
	}
	defer conn.Close()
	if err := conn.Send(keyboardRegistration(), xpc.HeartbeatRequestFlag); err != nil {
		return fmt.Errorf("register virtual keyboard: %w", err)
	}
	reply, err := conn.ReceiveOnServerClientStream()
	if err != nil {
		return fmt.Errorf("register virtual keyboard: %w", err)
	}
	if reply["error"] != nil {
		return errors.New("device rejected virtual keyboard registration")
	}
	surface := keyboardSurface
	switch id := reply["serviceID"].(type) {
	case uint64:
		if id != 0 {
			surface = id
		}
	case int64:
		if id > 0 {
			surface = uint64(id)
		}
	}
	if err := wait(ctx, 300*time.Millisecond); err != nil {
		return err
	}
	epoch := time.Now()
	report := func(keys []byte) error {
		var data [39]byte
		data[0] = 1
		for _, k := range keys {
			data[1+k/8] |= 1 << (k % 8)
		}
		// The iPhone uses 24 MHz ticks and a 48-bit timestamp field.
		var stamp [8]byte
		binary.LittleEndian.PutUint64(stamp[:], uint64(time.Since(epoch).Nanoseconds())/125*3)
		copy(data[31:37], stamp[:6])
		return conn.Send(keyboardMessage("send", map[string]any{"_0": data[:], "_1": surface}))
	}
	for _, stroke := range strokes {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("text input may be incomplete: %w", err)
		}
		if err := report(stroke); err != nil {
			return fmt.Errorf("send key (outcome uncertain): %w", err)
		}
		held := wait(ctx, 15*time.Millisecond)
		if err := report(nil); err != nil {
			return fmt.Errorf("release key (outcome uncertain): %w", err)
		}
		if held != nil {
			return fmt.Errorf("text input may be incomplete: %w", held)
		}
		if err := wait(ctx, 35*time.Millisecond); err != nil {
			return fmt.Errorf("text input may be incomplete: %w", err)
		}
	}
	return nil
}
