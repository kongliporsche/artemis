package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/danielpaulus/go-ios/ios/nskeyedarchiver"
	"howett.net/plist"
)

const readyListing = `{"info":{"outcome":"success"},"result":{"devices":[
	{"identifier":"usb","hardwareProperties":{"deviceType":"iPhone","platform":"iOS"},"connectionProperties":{"transportType":"wired","pairingState":"paired"},"deviceProperties":{"name":"Test phone","osVersionNumber":"27.0.1","developerModeStatus":"enabled","ddiServicesAvailable":true}}
]}}`

func TestAccessibilityDecodesVisibleLabelWithoutEditableValue(t *testing.T) {
	u := func(i int) plist.UID { return plist.UID(i) }
	objects := []any{
		"$null",
		map[string]any{"$class": u(2), "resultData": u(3)},
		map[string]any{"$classname": "AXPTranslatorResponse"},
		map[string]any{"$class": u(4), "NS.keys": []any{u(5)}, "NS.objects": []any{u(6)}},
		map[string]any{"$classname": "NSDictionary"},
		"treeDump",
		map[string]any{"$class": u(7), "NS.objects": []any{u(8)}},
		map[string]any{"$classname": "NSArray"},
		map[string]any{"$class": u(2), "associatedRequestType": 5, "associatedTranslationObject": u(9), "resultData": u(11)},
		map[string]any{"$class": u(10), "pid": 7, "objectID": 3},
		map[string]any{"$classname": "AXPTranslationObject"},
		map[string]any{"$class": u(4), "NS.keys": []any{u(12), u(13), u(14), u(15), u(16), u(17)},
			"NS.objects": []any{u(18), u(19), u(19), u(20), u(21), u(22)}},
		21, 26, 28, 33, 53, 61,
		map[string]any{"$class": u(23), "NS.rectval": u(24)},
		true, "Search", "editable text", 49,
		map[string]any{"$classname": "NSValue"},
		"{{10, 20}, {100, 40}}",
	}
	inner, err := plist.Marshal(map[string]any{
		"$archiver": "NSKeyedArchiver", "$objects": objects, "$top": map[string]any{"root": u(1)},
	}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	outer, err := nskeyedarchiver.ArchiveBin(inner)
	if err != nil {
		t.Fatal(err)
	}
	result := visibleAXElements(decodeAXPush([]any{outer}), 1179, 2556, 3)
	if len(result) != 1 || result[0].Text != "Search" || result[0].Class != "iOSTextField" ||
		result[0].Bounds != "[30,60][330,180]" || !result[0].Focused {
		t.Fatalf("unexpected accessibility result: %+v", result)
	}
}

func TestAccessibilityOnLockedDeviceDoesNotOpenService(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args[:3], " ") {
		case "list devices --json-output":
			return []byte(readyListing), nil
		case "device info lockState":
			return []byte(`{"result":{"passcodeRequired":true}}`), nil
		default:
			t.Fatalf("unexpected command on locked device: %q", args)
			return nil, nil
		}
	}
	var output bytes.Buffer
	if err := serve(strings.NewReader("{\"id\":1,\"method\":\"accessibility\",\"device\":\"usb\"}\n"), &output, run); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "unlock") {
		t.Fatalf("expected lock rejection: %s", output.String())
	}
}

func TestTapRequiresExplicitPixelCoordinates(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		t.Fatalf("invalid tap reached device: %q", args)
		return nil, nil
	}
	var output bytes.Buffer
	if err := serve(strings.NewReader(`{"id":1,"method":"tap","device":"usb","x":0}`+"\n"), &output, run); err != nil {
		t.Fatal(err)
	}
	var got struct{ Error string }
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || !strings.Contains(got.Error, "pixel coordinates") {
		t.Fatalf("expected a coordinate validation error: %s (%v)", output.String(), err)
	}
}

func TestTypeRejectsUnsupportedTextBeforeTouchingDevice(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		t.Fatalf("invalid text reached device: %q", args)
		return nil, nil
	}
	for _, text := range []string{`"hello\nworld"`, `"\u4f60\u597d"`, `null`} {
		var output bytes.Buffer
		input := `{"id":1,"method":"type","device":"usb","text":` + text + "}\n"
		if err := serve(strings.NewReader(input), &output, run); err != nil {
			t.Fatal(err)
		}
		var got struct{ Error string }
		if err := json.Unmarshal(output.Bytes(), &got); err != nil || !strings.Contains(got.Error, "printable ASCII") {
			t.Fatalf("expected a text validation error: %s (%v)", output.String(), err)
		}
	}
}

func TestTapDoesNotSendInputToLockedDevice(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args[:3], " ") {
		case "list devices --json-output":
			return []byte(readyListing), nil
		case "device info lockState":
			return []byte(`{"result":{"passcodeRequired":true}}`), nil
		default:
			t.Fatalf("unexpected command on locked device: %q", args)
			return nil, nil
		}
	}
	var output bytes.Buffer
	if err := serve(strings.NewReader(`{"id":1,"method":"tap","device":"usb","x":0,"y":0}`+"\n"), &output, run); err != nil {
		t.Fatal(err)
	}
	var got struct{ Error string }
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || !strings.Contains(got.Error, "unlock") {
		t.Fatalf("expected a lock error: %s (%v)", output.String(), err)
	}
}

func TestCheckInputRequiresKnownUnlockedState(t *testing.T) {
	for _, tt := range []struct {
		name, lock string
		unlocked   bool
	}{
		{"unlocked", `{"result":{"passcodeRequired":false}}`, true},
		{"locked", `{"result":{"passcodeRequired":true}}`, false},
		{"unknown", `{"result":{}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			run := func(_ context.Context, args ...string) ([]byte, error) {
				switch strings.Join(args[:3], " ") {
				case "list devices --json-output":
					return []byte(readyListing), nil
				case "device info lockState":
					return []byte(tt.lock), nil
				default:
					t.Fatalf("read-only check reached %q", args)
					return nil, nil
				}
			}
			var output bytes.Buffer
			input := strings.NewReader("{\"id\":1,\"method\":\"check_input\",\"device\":\"usb\"}\n")
			if err := serve(input, &output, run); err != nil {
				t.Fatal(err)
			}
			var got struct {
				Result bool
				Error  string
			}
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Result != tt.unlocked || (!tt.unlocked && !strings.Contains(got.Error, "unlock")) {
				t.Fatalf("unexpected input status: %s", output.String())
			}
		})
	}
}

func TestSwipeRejectsInvalidRequestsBeforeTouchingDevice(t *testing.T) {
	for _, tt := range []struct{ fields, message string }{
		{`"x":10,"y":20,"end_x":30,"duration_ms":800`, "pixel coordinates"},
		{`"x":10,"y":20,"end_x":30,"end_y":40,"duration_ms":9000`, "duration_ms"},
		{`"x":10,"y":20,"end_x":10,"end_y":20`, "distinct"},
	} {
		run := func(_ context.Context, args ...string) ([]byte, error) {
			t.Fatalf("invalid swipe reached device: %q", args)
			return nil, nil
		}
		var output bytes.Buffer
		input := `{"id":1,"method":"swipe","device":"usb",` + tt.fields + "}\n"
		if err := serve(strings.NewReader(input), &output, run); err != nil {
			t.Fatal(err)
		}
		var got struct{ Error string }
		if err := json.Unmarshal(output.Bytes(), &got); err != nil || !strings.Contains(got.Error, tt.message) {
			t.Fatalf("expected %q error: %s (%v)", tt.message, output.String(), err)
		}
	}
}

func TestSwipeRejectsEndpointOutsideLiveDisplay(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch strings.Join(args[:3], " ") {
		case "list devices --json-output":
			return []byte(readyListing), nil
		case "device info lockState":
			return []byte(`{"result":{"passcodeRequired":false}}`), nil
		case "device info displays":
			return []byte(`{"result":{"displays":[{"displayId":1,"primary":true,"nativeSize":[1179,2556],"nativeOrientation":"rot0","currentOrientation":"rot0","backlightState":"activeOn"}]}}`), nil
		default:
			t.Fatalf("unexpected device command: %q", args)
			return nil, nil
		}
	}
	var output bytes.Buffer
	input := `{"id":1,"method":"swipe","device":"usb","x":10,"y":20,"end_x":1179,"end_y":40}` + "\n"
	if err := serve(strings.NewReader(input), &output, run); err != nil {
		t.Fatal(err)
	}
	var got struct{ Error string }
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || !strings.Contains(got.Error, "outside the 1179x2556 display") {
		t.Fatalf("expected a display bounds error: %s (%v)", output.String(), err)
	}
}

func TestListRefreshesUninitializedDeveloperServices(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(strings.Replace(readyListing, `"developerModeStatus":"enabled","ddiServicesAvailable":true`, `"ddiServicesAvailable":false`, 1)), nil
		}
		if strings.Join(args[:5], " ") != "device info details --device usb" {
			t.Fatalf("unexpected device command: %q", args)
		}
		return []byte(`{"info":{"outcome":"success"},"result":{"deviceProperties":{"developerModeStatus":"enabled","ddiServicesAvailable":true}}}`), nil
	}
	var output bytes.Buffer
	if err := serve(strings.NewReader("{\"id\":1,\"method\":\"list\"}\n"), &output, run); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Result []struct{ State string }
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil || len(got.Result) != 1 || got.Result[0].State != "ready" {
		t.Fatalf("expected ready device from live details: %s (%v)", output.String(), err)
	}
}

func TestLaunchAcceptsBundleIDButNotExtraArguments(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(readyListing), nil
		}
		if strings.Join(args, " ") != "device process launch --device usb --quiet --timeout 15 com.example.TestApp" {
			t.Fatalf("unexpected launch command: %q", args)
		}
		return nil, nil
	}
	var output bytes.Buffer
	input := strings.NewReader(
		`{"id":1,"method":"launch","device":"usb","bundle_id":"com.example.TestApp"}` + "\n" +
			`{"id":2,"method":"launch","device":"usb","bundle_id":"--terminate-existing"}` + "\n",
	)
	if err := serve(input, &output, run); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var valid struct {
		Error  string
		Result bool
	}
	if err := decoder.Decode(&valid); err != nil || valid.Error != "" || !valid.Result {
		t.Fatalf("launch failed: %+v (%v)", valid, err)
	}
	var invalid struct{ Error string }
	if err := decoder.Decode(&invalid); err != nil || !strings.Contains(invalid.Error, "bundle ID") {
		t.Fatalf("invalid bundle ID not rejected: %+v (%v)", invalid, err)
	}
}

func TestScreenshotReturnsPNGAndPixelDimensions(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(readyListing), nil
		}
		if len(args) != 9 || strings.Join(args[:6], " ") != "device capture screenshot --device usb --destination" {
			return nil, errors.New("unexpected capture command")
		}
		file, err := os.Create(args[6])
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return nil, png.Encode(file, image.NewRGBA(image.Rect(0, 0, 3, 5)))
	}
	var output bytes.Buffer
	if err := serve(strings.NewReader("{\"id\":2,\"method\":\"screenshot\",\"device\":\"usb\"}\n"), &output, run); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Error  string
		Result struct {
			PNG           []byte `json:"png_base64"`
			Width, Height int
		}
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != "" || got.Result.Width != 3 || got.Result.Height != 5 {
		t.Fatalf("unexpected response: %s", output.String())
	}
	if _, err := png.Decode(bytes.NewReader(got.Result.PNG)); err != nil {
		t.Fatal(err)
	}
}

func TestListReturnsOnlyWiredIOS27IPhones(t *testing.T) {
	run := func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") != "list devices --json-output - --quiet --timeout 15" {
			return nil, errors.New("unexpected device command")
		}
		return []byte(`{"info":{"outcome":"success"},"result":{"devices":[
			{"identifier":"usb","hardwareProperties":{"deviceType":"iPhone","platform":"iOS"},"connectionProperties":{"transportType":"wired","pairingState":"paired"},"deviceProperties":{"name":"Test phone","osVersionNumber":"27.0.1","developerModeStatus":"enabled","ddiServicesAvailable":true}},
			{"identifier":"wifi","hardwareProperties":{"deviceType":"iPhone","platform":"iOS"},"connectionProperties":{"transportType":"localNetwork"},"deviceProperties":{"osVersionNumber":"27.0.1"}},
			{"identifier":"old","hardwareProperties":{"deviceType":"iPhone","platform":"iOS"},"connectionProperties":{"transportType":"wired"},"deviceProperties":{"osVersionNumber":"26.5"}},
			{"identifier":"tablet","hardwareProperties":{"deviceType":"iPad","platform":"iPadOS"},"connectionProperties":{"transportType":"wired"},"deviceProperties":{"osVersionNumber":"27.0"}}
		]}}`), nil
	}
	var output bytes.Buffer
	if err := serve(strings.NewReader("{\"id\":1,\"method\":\"list\"}\n"), &output, run); err != nil {
		t.Fatal(err)
	}
	var got struct {
		ID     int
		Error  string
		Result []struct{ ID, Name, State string }
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 || got.Error != "" || len(got.Result) != 1 ||
		got.Result[0].ID != "usb" || got.Result[0].Name != "Test phone" || got.Result[0].State != "ready" {
		t.Fatalf("unexpected response: %s", output.String())
	}
}
