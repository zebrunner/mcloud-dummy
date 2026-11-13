package config

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestExpand(t *testing.T) {
	c, err := Parse([]byte(`
devices:
  - platform: android
    count: 3
    port: 20000
  - platform: ios
    count: 2
    serial: 00008110-000A1B2C3D4E0000
    wdaPort: 21000
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) != 5 {
		t.Fatalf("%d devices", len(c.Devices))
	}
	a := c.Devices[2]
	if a.Serial != "dummy-android-003" || a.Port != 20002 || a.SDK != 34 || a.Width != 720 {
		t.Fatalf("%+v", a)
	}
	i := c.Devices[4]
	if i.Serial != "00008110-000A1B2C3D4E0002" || i.WDAPort != 21002 || i.MJPEGPort != 21003 || len(i.Serial) != 25 {
		t.Fatalf("%+v", i)
	}
}

func TestPortConflict(t *testing.T) {
	_, err := Parse([]byte(`
devices:
  - platform: android
    serial: a
    port: 20000
  - platform: android
    serial: b
    port: 20000
`))
	if err == nil {
		t.Fatal("expected a port conflict")
	}
}

func TestUnknownField(t *testing.T) {
	if _, err := Parse([]byte("devices:\n  - platform: android\n    colour: red\n")); err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

const fleet = `
profiles:
  pixel:
    platform: android
    model: Pixel 8 Pro
    manufacturer: Google
    osVersion: "16"
    width: 1344
    height: 2992
    density: 480
  galaxy:
    model: Galaxy S24
    manufacturer: Samsung
    osVersion: "14"
    width: 1080
    height: 2340
    density: 420
devices:
  - platform: android
    count: 20
    port: 20000
    seed: 7
    vary:
      profile: [pixel, galaxy]
      battery: "5-100"
      charging: [true, false]
      network: [wifi, cellular, none]
      rotation: [0, 90]
      locale: [en-US, de-DE]
      timeZone: [UTC, Europe/Berlin]
`

func TestVaryRandom(t *testing.T) {
	c, err := Parse([]byte(fleet))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Devices) != 20 {
		t.Fatalf("%d devices", len(c.Devices))
	}
	seen := map[string]map[string]bool{}
	note := func(k, v string) {
		if seen[k] == nil {
			seen[k] = map[string]bool{}
		}
		seen[k][v] = true
	}
	for i, d := range c.Devices {
		if d.Port != 20000+i || d.Name != d.Model+" "+threeDigits(i+1) {
			t.Fatalf("numbering %d: %+v", i, d)
		}
		if *d.Battery < 5 || *d.Battery > 100 {
			t.Fatalf("battery %d", *d.Battery)
		}
		if d.Model == "Pixel 8 Pro" && (d.Width != 1344 || d.Density != 480 || d.SDK != 36) {
			t.Fatalf("profile pixel: %+v", d)
		}
		if d.Model == "Galaxy S24" && (d.Width != 1080 || d.SDK != 34) {
			t.Fatalf("profile galaxy: %+v", d)
		}
		note("model", d.Model)
		note("network", d.Network)
		note("rotation", fmt.Sprint(d.Rotation))
		note("charging", fmt.Sprint(*d.Charging))
		note("tz", d.TimeZone)
		note("locale", d.Locale)
		note("battery", fmt.Sprint(*d.Battery))
	}
	for k, n := range map[string]int{"model": 2, "network": 3, "rotation": 2, "charging": 2, "tz": 2, "locale": 2, "battery": 5} {
		if len(seen[k]) < n {
			t.Errorf("%s: only %v", k, seen[k])
		}
	}
	again, _ := Parse([]byte(fleet))
	a, _ := yaml.Marshal(c.Devices)
	b, _ := yaml.Marshal(again.Devices)
	if string(a) != string(b) {
		t.Fatal("the same seed gives other devices")
	}
	other, _ := Parse([]byte(strings.Replace(fleet, "seed: 7", "seed: 8", 1)))
	same := 0
	for i := range c.Devices {
		if *c.Devices[i].Battery == *other.Devices[i].Battery {
			same++
		}
	}
	if same == len(c.Devices) {
		t.Fatal("another seed gives the same batteries")
	}
}

func threeDigits(n int) string { return fmt.Sprintf("%03d", n) }

func TestVaryCycleAndScreen(t *testing.T) {
	c, err := Parse([]byte(`
devices:
  - platform: android
    count: 4
    pick: cycle
    vary:
      screen: [720x1280@320, 1080x2400@420]
      battery: "10-40"
  - platform: ios
    count: 2
    pick: cycle
    vary:
      screen: [390x844@3, 1024x1366@2]
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ w, h, density, battery int }{{720, 1280, 320, 10}, {1080, 2400, 420, 20}, {720, 1280, 320, 30}, {1080, 2400, 420, 40}}
	for i, w := range want {
		d := c.Devices[i]
		if d.Width != w.w || d.Height != w.h || d.Density != w.density || *d.Battery != w.battery {
			t.Fatalf("%d: %dx%d@%d %d%%", i, d.Width, d.Height, d.Density, *d.Battery)
		}
	}
	if d := c.Devices[5]; d.Width != 1024 || d.Scale != 2 {
		t.Fatalf("ios: %+v", d)
	}
}

func TestVaryErrors(t *testing.T) {
	for _, bad := range []string{
		"vary: {port: [1, 2]}",
		"vary: {battery: [101]}",
		"vary: {network: [5g]}",
		"vary: {rotation: [45]}",
		"vary: {screen: [big]}",
		"vary: {nope: [1]}",
		"vary: {timeZone: [Mars/Base]}",
		"vary: {profile: [missing]}",
		"pick: sometimes",
		"locale: english",
	} {
		_, err := Parse([]byte("devices:\n  - platform: android\n    count: 2\n    " + bad + "\n"))
		if err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
	_, err := Parse([]byte("profiles:\n  x: {platform: ios}\ndevices:\n  - {platform: android, profile: x}\n"))
	if err == nil {
		t.Error("an iOS profile on Android: no error")
	}
}

func TestStateDefaults(t *testing.T) {
	c, err := Parse([]byte("devices:\n  - platform: android\n  - platform: ios\n    battery: 0\n    charging: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	a, i := c.Devices[0], c.Devices[1]
	if *a.Battery != 100 || !*a.Charging || a.Network != "wifi" || a.Locale != "en-US" || a.TimeZone != "UTC" {
		t.Fatalf("%+v", a)
	}
	if *i.Battery != 0 || *i.Charging {
		t.Fatalf("battery 0 unplugged: %d %v", *i.Battery, *i.Charging)
	}
}

func TestInvalidDevices(t *testing.T) {
	for _, tc := range []struct{ yaml, err string }{
		{"  - platform: android\n    port: 70000\n", "port 70000"},
		{"  - platform: android\n    port: -1\n", "port -1"},
		{"  - platform: android\n    count: 2\n    port: 65535\n", "port 65536"},
		{"  - platform: ios\n    wdaPort: 65535\n", "mjpegPort 65536"},
		{"  - platform: ios\n    wdaPort: 99999\n    mjpegPort: 8101\n", "wdaPort 99999"},
		{"  - platform: android\n    type: watch\n", "type must be phone, tablet or tv"},
		{"  - platform: ios\n    type: car\n", "type must be phone, tablet or tv"},
	} {
		_, err := Parse([]byte("devices:\n" + tc.yaml))
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q: %v, want %q", tc.yaml, err, tc.err)
		}
	}
}

func TestCheckControl(t *testing.T) {
	c, err := Parse([]byte("devices:\n  - platform: android\n    port: 15555\n  - platform: ios\n    wdaPort: 18100\n"))
	if err != nil {
		t.Fatal(err)
	}
	for control, want := range map[string]string{
		":8800":           "",
		"127.0.0.1:8800":  "",
		"8800":            "must be [host]:port",
		":0":              "the port must be 1-65535",
		":70000":          "the port must be 1-65535",
		":http":           "the port must be 1-65535",
		":15555":          "the port 15555 is the port of",
		"0.0.0.0:18101":   "the port 18101 is the mjpegPort of",
		"localhost:18100": "the port 18100 is the wdaPort of",
	} {
		c.Control = control
		err := c.CheckControl()
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("control %q: %v, want %q", control, err, want)
		}
	}
}
