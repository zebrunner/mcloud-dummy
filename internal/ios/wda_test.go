package ios

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/config"
	"github.com/zebrunner/mcloud-dummy/internal/ui"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startDevice(t *testing.T) *Device {
	t.Helper()
	wda, mjpeg := freePort(t), freePort(t)
	cfg, err := config.Parse([]byte(fmt.Sprintf("devices:\n  - platform: ios\n    wdaPort: %d\n    mjpegPort: %d\n    bootTime: 1ms\n", wda, mjpeg)))
	if err != nil {
		t.Fatal(err)
	}
	d := New(cfg.Devices[0], "127.0.0.1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	time.Sleep(5 * time.Millisecond)
	return d
}

func call(t *testing.T, d *Device, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", d.Cfg.WDAPort, path), r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestStatusAndSession(t *testing.T) {
	d := startDevice(t)
	code, st := call(t, d, "GET", "/status", nil)
	v := st["value"].(map[string]any)
	if code != 200 || v["ready"] != true || v["os"].(map[string]any)["version"] != "17.4" {
		t.Fatalf("%d %v", code, st)
	}
	_, s := call(t, d, "POST", "/session", map[string]any{"capabilities": map[string]any{"alwaysMatch": map[string]any{}}})
	sid := s["value"].(map[string]any)["sessionId"].(string)
	if _, st := call(t, d, "GET", "/status", nil); st["sessionId"] != sid {
		t.Fatalf("status reports the session for STF: %v", st)
	}
	_, el := call(t, d, "POST", "/session/"+sid+"/element", map[string]any{"using": "accessibility id", "value": "tap_button"})
	id := el["value"].(map[string]any)["ELEMENT"].(string)
	if code, _ := call(t, d, "POST", "/session/"+sid+"/element/"+id+"/click", map[string]any{}); code != 200 || d.App.Taps() != 1 {
		t.Fatalf("click: %d, taps %d", code, d.App.Taps())
	}
	code, e := call(t, d, "POST", "/session/"+sid+"/element", map[string]any{"using": "accessibility id", "value": "nope"})
	if code != 404 || e["value"].(map[string]any)["error"] != "no such element" {
		t.Fatalf("missing element: %d %v", code, e)
	}
	call(t, d, "DELETE", "/session/"+sid, nil)
	if code, _ := call(t, d, "GET", "/session/"+sid+"/window/size", nil); code != 404 {
		t.Fatalf("a deleted session: %d", code)
	}
}

func TestActionsTapInPoints(t *testing.T) {
	d := startDevice(t)
	b := d.App.Find("tap_button")
	_, s := call(t, d, "POST", "/session", map[string]any{"capabilities": map[string]any{}})
	sid := s["value"].(map[string]any)["sessionId"].(string)
	call(t, d, "POST", "/session/"+sid+"/actions", map[string]any{"actions": []any{map[string]any{
		"type": "pointer", "id": "finger1", "parameters": map[string]any{"pointerType": "touch"},
		"actions": []any{
			map[string]any{"type": "pointerMove", "duration": 0, "x": b.Rect.X + 5, "y": b.Rect.Y + 5},
			map[string]any{"type": "pointerDown", "button": 0},
			map[string]any{"type": "pointerUp", "button": 0},
		}}}})
	if d.App.Taps() != 1 {
		t.Fatalf("taps %d", d.App.Taps())
	}
}

// readMJPEGFrame reads the next JPEG of the MJPEG stream.
func readMJPEGFrame(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "Content-Length: ") {
			fmt.Sscanf(line, "Content-Length: %d", &length)
		}
		if line == "\r\n" && length > 0 {
			break
		}
	}
	jpg := make([]byte, length)
	if _, err := io.ReadFull(r, jpg); err != nil || !bytes.HasPrefix(jpg, []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("frame: %v %x", err, jpg[:4])
	}
	return jpg
}

func openMJPEG(t *testing.T, d *Device) *bufio.Reader {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", d.Cfg.MJPEGPort))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	status, _ := r.ReadString('\n')
	if !strings.HasPrefix(status, "HTTP/1.0 200") {
		t.Fatalf("status %q", status)
	}
	return r
}

// STF connects a plain socket to the MJPEG port and sends nothing: the stream must start at once.
func TestMJPEGStartsWithoutRequest(t *testing.T) {
	d := startDevice(t)
	readMJPEGFrame(t, openMJPEG(t, d))
}

// WebDriverAgent streams the framebuffer, of the natural orientation, unless mjpegFixOrientation: the STF UI turns
// the screen of a landscape device itself; landscape frames would be stretched into the portrait screen.
func TestMJPEGFramesOfTheNaturalOrientation(t *testing.T) {
	d := startDevice(t)
	d.App.SetRotation("test", 90)
	size := func(jpg []byte) (int, int) {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(jpg))
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Width, cfg.Height
	}
	r := openMJPEG(t, d)
	if w, h := size(readMJPEGFrame(t, r)); w != 390*3 || h != 844*3 {
		t.Fatalf("landscape without mjpegFixOrientation: %dx%d", w, h)
	}
	_, s := call(t, d, "POST", "/session", map[string]any{"capabilities": map[string]any{}})
	sid := s["value"].(map[string]any)["sessionId"].(string)
	call(t, d, "POST", "/session/"+sid+"/appium/settings", map[string]any{"settings": map[string]any{"mjpegFixOrientation": true}})
	d.App.Tick()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if w, h := size(readMJPEGFrame(t, r)); w == 844*3 && h == 390*3 {
			return
		}
	}
	t.Fatal("mjpegFixOrientation: the frames stay of the natural orientation")
}

func TestFaultDown(t *testing.T) {
	d := startDevice(t)
	d.SetFaults(Faults{Down: true})
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/status", d.Cfg.WDAPort)); err == nil {
		t.Fatal("WDA must be unreachable")
	}
	d.SetFaults(Faults{})
	if code, _ := call(t, d, "GET", "/status", nil); code != 200 {
		t.Fatalf("WDA is back: %d", code)
	}
}

func TestBatteryAndDeviceInfo(t *testing.T) {
	d := startDevice(t)
	d.App.SetPower("test", ui.Power{Level: 30, Charging: false})
	_, out := call(t, d, "GET", "/wda/batteryInfo", nil)
	v := out["value"].(map[string]any)
	if v["level"] != 0.3 || v["state"] != 1.0 {
		t.Fatalf("%v", v)
	}
	_, out = call(t, d, "GET", "/wda/device/info", nil)
	v = out["value"].(map[string]any)
	if v["currentLocale"] != "en_US" || v["timeZone"] != "UTC" {
		t.Fatalf("%v", v)
	}
}
