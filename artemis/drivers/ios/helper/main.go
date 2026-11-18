package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type runDeviceCommand func(context.Context, ...string) ([]byte, error)

type request struct {
	ID       json.RawMessage `json:"id"`
	Method   string          `json:"method"`
	Device   string          `json:"device"`
	BundleID string          `json:"bundle_id"`
	X        *int            `json:"x"`
	Y        *int            `json:"y"`
	EndX     *int            `json:"end_x"`
	EndY     *int            `json:"end_y"`
	Duration *int            `json:"duration_ms"`
	Text     *string         `json:"text"`
	Clear    bool            `json:"clear_existing"`
}

type response struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	State   string `json:"state"`
	udid    string
}

type deviceProperties struct {
	Name, OSVersionNumber, DeveloperModeStatus string
	DDIServicesAvailable                       *bool
}

func listDevices(ctx context.Context, run runDeviceCommand) ([]device, error) {
	raw, err := run(ctx, "list", "devices", "--json-output", "-", "--quiet", "--timeout", "15")
	if err != nil {
		return nil, err
	}
	var listing struct {
		Info   struct{ Outcome string }
		Result *struct {
			Devices []struct {
				Identifier           string
				HardwareProperties   struct{ DeviceType, Platform, UDID string }
				ConnectionProperties struct{ TransportType, PairingState string }
				DeviceProperties     deviceProperties
			}
		}
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("decode devicectl device list: %w", err)
	}
	if listing.Info.Outcome != "success" || listing.Result == nil || listing.Result.Devices == nil {
		return nil, errors.New("devicectl did not return a successful device list")
	}
	devices := make([]device, 0)
	for _, row := range listing.Result.Devices {
		hardware, connection, props := row.HardwareProperties, row.ConnectionProperties, row.DeviceProperties
		major, err := strconv.Atoi(strings.SplitN(props.OSVersionNumber, ".", 2)[0])
		if row.Identifier == "" || !strings.EqualFold(hardware.DeviceType, "iPhone") ||
			!strings.EqualFold(hardware.Platform, "iOS") || connection.TransportType != "wired" ||
			err != nil || major < 27 {
			continue
		}
		state := "trust_required"
		if connection.PairingState == "paired" {
			state = developerState(props)
			if state != "ready" {
				// Listing can report cached readiness before CoreDevice opens a tunnel.
				state = liveDeveloperState(ctx, run, row.Identifier)
			}
		}
		devices = append(devices, device{row.Identifier, props.Name, props.OSVersionNumber, state, hardware.UDID})
	}
	return devices, nil
}

func developerState(props deviceProperties) string {
	switch {
	case props.DeveloperModeStatus == "disabled":
		return "developer_mode_required"
	case props.DeveloperModeStatus != "enabled", props.DDIServicesAvailable == nil:
		return "readiness_unknown"
	case !*props.DDIServicesAvailable:
		return "developer_services_unavailable"
	default:
		return "ready"
	}
}

func liveDeveloperState(ctx context.Context, run runDeviceCommand, id string) string {
	raw, err := run(ctx, "device", "info", "details", "--device", id,
		"--json-output", "-", "--quiet", "--timeout", "5")
	var details struct {
		Info   struct{ Outcome string }
		Result *struct{ DeviceProperties deviceProperties }
	}
	if err != nil || json.Unmarshal(raw, &details) != nil ||
		details.Info.Outcome != "success" || details.Result == nil {
		return "readiness_unknown"
	}
	return developerState(details.Result.DeviceProperties)
}

var bundleID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*(\.[A-Za-z0-9][A-Za-z0-9-]*)+$`)

func dispatch(ctx context.Context, req request, run runDeviceCommand) (any, error) {
	switch req.Method {
	case "list":
		return listDevices(ctx, run)
	case "accessibility":
		return readAccessibility(ctx, run, req.Device)
	case "check_input":
		if err := requireReadyDevice(ctx, run, req.Device); err != nil {
			return nil, err
		}
		return true, requireUnlocked(ctx, run, req.Device)
	case "screenshot":
		if err := requireReadyDevice(ctx, run, req.Device); err != nil {
			return nil, err
		}
		return capture(ctx, run, req.Device)
	case "launch":
		if !bundleID.MatchString(req.BundleID) {
			return nil, errors.New("launch requires a valid bundle ID")
		}
		if err := requireReadyDevice(ctx, run, req.Device); err != nil {
			return nil, err
		}
		if _, err := run(ctx, "device", "process", "launch", "--device", req.Device,
			"--quiet", "--timeout", "15", req.BundleID); err != nil {
			return nil, err
		}
		return true, nil
	case "tap":
		if req.X == nil || req.Y == nil || *req.X < 0 || *req.Y < 0 {
			return nil, errors.New("tap requires nonnegative integer pixel coordinates x and y")
		}
		point := pixelPoint{*req.X, *req.Y}
		return true, touch(ctx, run, req.Device, point, point, 60*time.Millisecond)
	case "swipe":
		if req.X == nil || req.Y == nil || req.EndX == nil || req.EndY == nil ||
			min(*req.X, *req.Y, *req.EndX, *req.EndY) < 0 {
			return nil, errors.New("swipe requires nonnegative integer pixel coordinates x, y, end_x and end_y")
		}
		from, to := pixelPoint{*req.X, *req.Y}, pixelPoint{*req.EndX, *req.EndY}
		if from == to {
			return nil, errors.New("swipe requires distinct endpoints")
		}
		ms := 800
		if req.Duration != nil {
			ms = *req.Duration
		}
		if ms < 50 || ms > 3000 {
			return nil, errors.New("swipe duration_ms must be between 50 and 3000")
		}
		return true, touch(ctx, run, req.Device, from, to, time.Duration(ms)*time.Millisecond)
	case "type":
		if req.Text == nil {
			return nil, errors.New("type requires printable ASCII text")
		}
		strokes, err := textStrokes(*req.Text, req.Clear)
		if err != nil {
			return nil, err
		}
		if len(strokes) == 0 {
			return true, nil
		}
		return true, withDeviceInput(ctx, run, req.Device, func(ctx context.Context, entry inputDevice) error {
			return typeKeys(ctx, entry.device, strokes)
		})
	default:
		return nil, fmt.Errorf("unsupported method %q", req.Method)
	}
}

func requireUnlocked(ctx context.Context, run runDeviceCommand, id string) error {
	raw, err := run(ctx, "device", "info", "lockState", "--device", id,
		"--json-output", "-", "--quiet", "--timeout", "5")
	if err != nil {
		return err
	}
	var lock struct {
		Result struct{ PasscodeRequired *bool }
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return fmt.Errorf("read device lock state: %w", err)
	}
	if lock.Result.PasscodeRequired == nil || *lock.Result.PasscodeRequired {
		return errors.New("unlock the iPhone before sending input")
	}
	return nil
}

type screenImage struct {
	PNG    []byte `json:"png_base64"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func requireReadyDevice(ctx context.Context, run runDeviceCommand, id string) error {
	_, err := readyDevice(ctx, run, id)
	return err
}

func readyDevice(ctx context.Context, run runDeviceCommand, id string) (device, error) {
	if id == "" {
		return device{}, errors.New("operation requires an explicit device ID")
	}
	devices, err := listDevices(ctx, run)
	if err != nil {
		return device{}, err
	}
	for _, dev := range devices {
		if dev.ID != id {
			continue
		}
		if dev.State != "ready" {
			return device{}, fmt.Errorf("device %q is not ready: %s", id, dev.State)
		}
		return dev, nil
	}
	return device{}, fmt.Errorf("device %q is not a connected USB iOS 27+ iPhone", id)
}

func capture(ctx context.Context, run runDeviceCommand, id string) (screenImage, error) {
	dir, err := os.MkdirTemp("", "artemis-ios-")
	if err != nil {
		return screenImage{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "screen.png")
	if _, err := run(ctx, "device", "capture", "screenshot", "--device", id,
		"--destination", path, "--timeout", "15"); err != nil {
		return screenImage{}, err
	}
	img, err := os.ReadFile(path)
	if err != nil {
		return screenImage{}, err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		return screenImage{}, fmt.Errorf("decode screenshot dimensions: %w", err)
	}
	return screenImage{PNG: img, Width: cfg.Width, Height: cfg.Height}, nil
}

func serve(in io.Reader, out io.Writer, run runDeviceCommand) error {
	scanner, encoder := bufio.NewScanner(in), json.NewEncoder(out)
	for scanner.Scan() {
		var req request
		var res response
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			res.Error = "invalid request: " + err.Error()
		} else {
			res.ID = req.ID
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			result, err := dispatch(ctx, req, run)
			cancel()
			if err != nil {
				res.Error = err.Error()
			} else {
				res.Result = result
			}
		}
		if err := encoder.Encode(res); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func devicectl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/xcrun", append([]string{"devicectl"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return nil, fmt.Errorf("devicectl: %w: %s", err, detail)
		}
		return nil, fmt.Errorf("devicectl: %w", err)
	}
	return out, nil
}

func main() {
	if err := serve(os.Stdin, os.Stdout, devicectl); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
