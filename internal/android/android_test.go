package android

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/config"
	"github.com/zebrunner/mcloud-dummy/internal/ui"
)

func testDevice(t *testing.T) *Device {
	t.Helper()
	cfg, err := config.Parse([]byte("devices:\n  - platform: android\n    serial: test-\n    bootTime: 1ms\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := New(cfg.Devices[0], slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.bootAt = time.Now()
	return d
}

func run(t *testing.T, d *Device, script string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code := d.runShell(ctx, script, strings.NewReader(""), &out, &out)
	return out.String(), code
}

func TestGetprop(t *testing.T) {
	d := testDevice(t)
	if out, _ := run(t, d, "getprop ro.build.version.sdk"); out != "34\n" {
		t.Fatalf("sdk: %q", out)
	}
	out, _ := run(t, d, "getprop")
	if !strings.Contains(out, "[ro.product.cpu.abi]: [x86_64]\n") || !strings.Contains(out, "[sys.boot_completed]: [1]\n") {
		t.Fatalf("getprop: %q", out)
	}
	// adbkit waitBootComplete: an endless loop, the client closes the stream once it reads 1
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var loop bytes.Buffer
	code := d.runShell(ctx, "while getprop sys.boot_completed 2>/dev/null; do sleep 1; done", nil, &loop, &loop)
	if !strings.HasPrefix(loop.String(), "1\n") || code == 0 {
		t.Fatalf("boot loop: %q %d", loop.String(), code)
	}
}

func TestBootGatesDumpsysDisplay(t *testing.T) {
	d := testDevice(t)
	d.SetFaults(Faults{BootStuck: true})
	if out, _ := run(t, d, "dumpsys display 2>/dev/null | grep -A 20 DisplayDeviceInfo"); out != "" {
		t.Fatalf("a device that is not booted has no display yet: %q", out)
	}
	d.SetFaults(Faults{})
	if out, _ := run(t, d, "dumpsys display | grep -A 20 DisplayDeviceInfo"); !strings.Contains(out, "720 x 1280") {
		t.Fatalf("display: %q", out)
	}
}

func TestShellBasics(t *testing.T) {
	d := testDevice(t)
	out, code := run(t, d, `echo hi > /data/local/tmp/a.txt; echo more >> /data/local/tmp/a.txt; cat /data/local/tmp/a.txt; ls /nonexistent; echo "rc=$?"`)
	if out != "hi\nmore\nls: /nonexistent: No such file or directory\nrc=1\n" || code != 0 {
		t.Fatalf("got %q %d", out, code)
	}
	if out, code := run(t, d, "missingcmd"); code != 127 || !strings.Contains(out, "inaccessible or not found") {
		t.Fatalf("unknown command: %q %d", out, code)
	}
	if out, _ := run(t, d, `if [ "$(id -u)" = 0 ]; then echo root; else echo shell; fi`); out != "shell\n" {
		t.Fatalf("id: %q", out)
	}
	if out, _ := run(t, d, "ps --help | grep -c '^-A'"); out != "1\n" {
		t.Fatalf("ps --help: %q", out)
	}
}

func TestExecutableResourceAndProcesses(t *testing.T) {
	d := testDevice(t)
	_ = d.fs.WriteFile("/data/local/tmp/minicap", 0o100755, []byte("\x7fELF"), time.Now())
	if out, _ := run(t, d, "[ -x /data/local/tmp/minicap ] && echo exec"); out != "exec\n" {
		t.Fatalf("-x: %q", out)
	}
	out, _ := run(t, d, "LD_LIBRARY_PATH=/data/local/tmp exec /data/local/tmp/minicap -i")
	var info map[string]any
	if err := json.Unmarshal([]byte(out), &info); err != nil || info["width"].(float64) != 720 {
		t.Fatalf("minicap -i: %q %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var mc bytes.Buffer
	done := make(chan int)
	go func() {
		done <- d.runShell(ctx, "LD_LIBRARY_PATH=/data/local/tmp exec /data/local/tmp/minicap -S -Q 80 -P 720x1280@360x640/0", nil, &mc, &mc)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(d.procs.Find("minicap")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := d.socket("localabstract:minicap"); !ok {
		t.Fatal("minicap does not listen")
	}
	ps, _ := run(t, d, "ps 2>/dev/null | grep minicap")
	if !strings.Contains(ps, "shell") || !strings.HasSuffix(strings.TrimSpace(ps), "minicap") {
		t.Fatalf("ps: %q", ps)
	}
	pid := d.procs.Find("minicap")[0].PID
	if _, code := run(t, d, "kill -15 "+itoa(pid)); code != 0 {
		t.Fatal("kill failed")
	}
	select {
	case code := <-done:
		if code != 143 || !strings.HasPrefix(mc.String(), "PID: ") {
			t.Fatalf("minicap ended with %d: %q", code, mc.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("minicap did not end after kill")
	}
	cancel()
	if _, ok := d.socket("localabstract:minicap"); ok {
		t.Fatal("the socket stays after the process ended")
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestSTFServiceVersionAndTelephony(t *testing.T) {
	d := testDevice(t)
	d.pkgs.pkgs[stfPackage] = &Package{Name: stfPackage, VersionName: "2.5.8", Path: "/data/app/jp.co.cyberagent.stf-1/base.apk", Granted: map[string]bool{}}
	out, _ := run(t, d, "pm path jp.co.cyberagent.stf")
	if out != "package:/data/app/jp.co.cyberagent.stf-1/base.apk\n" {
		t.Fatalf("pm path: %q", out)
	}
	out, _ = run(t, d, "export CLASSPATH='/data/app/jp.co.cyberagent.stf-1/base.apk'; exec app_process /system/bin 'jp.co.cyberagent.stf.Agent' --version 2>/dev/null")
	if out != "2.5.8" {
		t.Fatalf("--version: %q", out)
	}
	out, _ = run(t, d, `export CLASSPATH='/data/app/jp.co.cyberagent.stf-1/base.apk'; if [ "$(id -u)" = 0 ] && command -v su >/dev/null 2>&1; then set -- su shell; else set --; fi; exec "$@" app_process /system/bin 'jp.co.cyberagent.stf.Agent' --telephony`)
	if !strings.Contains(out, "imei=35") || !strings.Contains(out, "phoneNumber=+1555") {
		t.Fatalf("--telephony: %q", out)
	}
	out, _ = run(t, d, "am start-foreground-service --user 0 -a 'jp.co.cyberagent.stf.ACTION_START' -n 'jp.co.cyberagent.stf/.Service'")
	if strings.Contains(out, "Error") {
		t.Fatalf("service: %q", out)
	}
	if _, ok := d.socket("localabstract:stfservice"); !ok {
		t.Fatal("STFService does not listen")
	}
}

func TestAppiumChecks(t *testing.T) {
	d := testDevice(t)
	d.pkgs.pkgs["io.appium.settings"] = &Package{Name: "io.appium.settings", VersionName: "5.12.0", VersionCode: 1, Launcher: ".Settings", Path: "/data/app/io.appium.settings-1/base.apk", Granted: map[string]bool{}}
	out, _ := run(t, d, "dumpsys package io.appium.settings")
	if !strings.Contains(out, "Package [io.appium.settings] (") || !strings.Contains(out, "versionName=5.12.0") {
		t.Fatalf("dumpsys package: %q", out)
	}
	if out, _ := run(t, d, "dumpsys activity services io.appium.settings"); strings.Contains(out, "isForeground=true") {
		t.Fatal("not started yet")
	}
	run(t, d, "am start -n io.appium.settings/.Settings -a android.intent.action.MAIN -c android.intent.category.LAUNCHER")
	if out, _ := run(t, d, "dumpsys activity services io.appium.settings"); !strings.Contains(out, "isForeground=true") {
		t.Fatalf("services: %q", out)
	}
	out, _ = run(t, d, "dumpsys window windows")
	if !strings.Contains(out, "mCurrentFocus=Window{1a2b3c u0 io.appium.settings/.MainActivity}") {
		t.Fatalf("focus: %q", out)
	}
	if out, _ := run(t, d, "am force-stop io.appium.settings; pidof io.appium.settings"); out != "" {
		t.Fatalf("force-stop: %q", out)
	}
}

func TestInputReachesTheApp(t *testing.T) {
	d := testDevice(t)
	button := d.App.Find("tap_button")
	run(t, d, "input tap "+itoa(button.Rect.X+5)+" "+itoa(button.Rect.Y+5))
	run(t, d, "input text hello%sworld")
	if d.App.Taps() != 1 || d.App.Input() != "hello world" {
		t.Fatalf("taps %d input %q", d.App.Taps(), d.App.Input())
	}
}

func TestProtobufEnvelope(t *testing.T) {
	msg := encodeEnvelope(7, true, msgGetDisplay, pbMsg(pbVarint(1, 0)))
	env := decodeEnvelope(msg)
	if !env.hasID || env.id != 7 || env.typ != msgGetDisplay {
		t.Fatalf("%+v", env)
	}
	ev := decodeEnvelope(encodeEnvelope(0, false, msgEventRotation, pbMsg(pbVarint(1, 90))))
	if ev.hasID || decodeFields(ev.message).uint(1) != 90 {
		t.Fatalf("event: %+v", ev)
	}
}

func TestStateBatteryAndNetwork(t *testing.T) {
	cfg, err := config.Parse([]byte("devices:\n  - platform: android\n    battery: 42\n    charging: false\n    network: cellular\n    locale: de-DE\n    timeZone: Europe/Berlin\n    rotation: 90\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := New(cfg.Devices[0], slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.bootAt = time.Now()
	out, _ := run(t, d, "dumpsys battery; dumpsys connectivity; getprop gsm.network.type; getprop persist.sys.locale; getprop persist.sys.timezone")
	for _, want := range []string{"USB powered: false", "status: 3", "level: 42", "MOBILE[LTE] CONNECTED", "\nLTE\n", "de-DE", "Europe/Berlin"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if d.App.Rotation() != 90 {
		t.Errorf("rotation %d", d.App.Rotation())
	}
	events, unsubscribe := d.events().subscribe()
	defer unsubscribe()
	run(t, d, "dumpsys battery set level 7; dumpsys battery set ac 1; svc wifi enable")
	if p := d.App.Power(); p.Level != 7 || !p.Charging || d.App.Network() != "wifi" {
		t.Fatalf("%+v %s", p, d.App.Network())
	}
	got := map[int]bool{}
	deadline := time.After(2 * time.Second)
	for !got[msgEventBattery] || !got[msgEventConnectivity] {
		select {
		case m := <-events:
			got[decodeEnvelope(m).typ] = true
		case <-deadline:
			t.Fatalf("events %v", got)
		}
	}
	run(t, d, "dumpsys battery reset")
	if p := d.App.Power(); p.Level != 42 || p.Charging {
		t.Fatalf("reset: %+v", p)
	}
}

func TestBatteryStatus(t *testing.T) {
	d := testDevice(t)
	d.App.SetPower("test", ui.Power{Level: 50, Charging: false})
	if p := d.App.Power(); p.Status() != 3 {
		t.Fatalf("status %d", p.Status())
	}
	d.App.SetPower("test", ui.Power{Level: 100, Charging: true})
	if p := d.App.Power(); p.Status() != 5 {
		t.Fatalf("full: %d", p.Status())
	}
}

// STF asks minicap for the size of the box of the browser; minicap streams the largest size of the aspect ratio of
// the display within it (Projection.hpp of the native minicap, Main.kt of the apk), or the screen is stretched and
// the touches of the STF UI miss the elements shown.
func TestMinicapKeepsTheAspectRatio(t *testing.T) {
	for _, tc := range []struct {
		proj     string
		apk      bool
		vw, vh   int
		fw, fh   int
		describe string
	}{
		{"720x1280@705x824/0", true, 464, 824, 464, 824, "a phone in a wide box"},
		{"1200x1920@705x824/0", true, 515, 824, 515, 824, "a tablet"},
		{"1200x1920@900x700/0", false, 438, 700, 438, 700, "a box wider than high"},
		{"720x1280@360x1000/0", false, 360, 640, 360, 640, "a high box"},
		{"720x1280@705x824/90", true, 464, 824, 824, 464, "the frames of a landscape screen"},
		{"720x1280@2000x3000/0", false, 720, 1280, 720, 1280, "the native minicap caps the size at the real one"},
		{"720x1280@2000x3000/0", true, 1688, 3000, 1688, 3000, "the apk does not"},
	} {
		p := parseProjection(tc.proj, 720, 1280).fit(tc.apk)
		fw, fh := p.frameSize()
		if p.vw != tc.vw || p.vh != tc.vh || fw != tc.fw || fh != tc.fh {
			t.Errorf("%s (%s apk=%v): virtual %dx%d frames %dx%d, want %dx%d frames %dx%d",
				tc.describe, tc.proj, tc.apk, p.vw, p.vh, fw, fh, tc.vw, tc.vh, tc.fw, tc.fh)
		}
	}
}

func TestMinicapBanner(t *testing.T) {
	d := testDevice(t)
	d.App.SetRotation("test", 90)
	p := parseProjection("720x1280@705x824/90", 720, 1280)
	u32 := func(b []byte, at int) int { return int(b[at]) | int(b[at+1])<<8 | int(b[at+2])<<16 | int(b[at+3])<<24 }
	native := minicapBanner(d, 7, p.fit(false), false)
	apk := minicapBanner(d, 7, p.fit(true), true)
	for _, tc := range []struct {
		name           string
		b              []byte
		rw, rh, vw, vh int
	}{
		{"native: the natural orientation", native, 720, 1280, 464, 824},
		{"apk: turned by the rotation", apk, 1280, 720, 824, 464},
	} {
		b := tc.b
		if len(b) != 24 || u32(b, 2) != 7 || u32(b, 6) != tc.rw || u32(b, 10) != tc.rh || u32(b, 14) != tc.vw ||
			u32(b, 18) != tc.vh || b[22] != 1 || b[23] != 2 {
			t.Errorf("%s: %v", tc.name, b)
		}
	}
}

func jpegSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	return cfg.Width, cfg.Height
}

func TestMinicapScreenshotSize(t *testing.T) {
	d := testDevice(t)
	_ = d.fs.WriteFile("/data/local/tmp/minicap", 0o100755, []byte("\x7fELF"), time.Now())
	_ = d.fs.WriteFile("/data/local/tmp/minicap.apk", 0o100644, []byte("PK"), time.Now())
	out, _ := run(t, d, "/data/local/tmp/minicap -s -P 720x1280@705x824/0")
	if w, h := jpegSize(t, []byte(out)); w != 464 || h != 824 {
		t.Fatalf("native: %dx%d", w, h)
	}
	d.App.SetRotation("test", 90)
	out, _ = run(t, d, "CLASSPATH=/data/local/tmp/minicap.apk app_process /system/bin io.devicefarmer.minicap.Main -s -P 720x1280@705x824/90")
	if w, h := jpegSize(t, []byte(out)); w != 824 || h != 464 {
		t.Fatalf("apk, landscape: %dx%d", w, h)
	}
}

// STF sends the touches of minitouch in the natural orientation, from 0 to the max of its banner (the size - 1).
func TestTouchOfTheNaturalOrientation(t *testing.T) {
	d := testDevice(t) // 720x1280
	for _, tc := range []struct {
		rotation, x, y, wantX, wantY int
	}{
		{0, 0, 0, 0, 0},
		{0, 719, 1279, 719, 1279},
		{0, 800, -5, 719, 0},
		{90, 0, 0, 0, 719},
		{90, 719, 1279, 1279, 0},
		{180, 0, 0, 719, 1279},
		{270, 0, 0, 1279, 0},
		{270, 719, 1279, 0, 719},
	} {
		d.App.SetRotation("test", tc.rotation)
		w, h := d.App.Size()
		x, y := naturalToDisplay(d, tc.x, tc.y)
		if x != tc.wantX || y != tc.wantY || x < 0 || y < 0 || x >= w || y >= h {
			t.Errorf("rotation %d: %d,%d -> %d,%d, want %d,%d within %dx%d", tc.rotation, tc.x, tc.y, x, y, tc.wantX, tc.wantY, w, h)
		}
	}
}
