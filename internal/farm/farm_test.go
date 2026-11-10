package farm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/zebrunner/mcloud-dummy/internal/config"
)

func TestAgentDevices(t *testing.T) {
	cfg, err := config.Parse([]byte("devices:\n  - platform: android\n    count: 2\n    name: Dummy Pixel/8\n  - platform: ios\n"))
	if err != nil {
		t.Fatal(err)
	}
	devices, err := AgentDevices(cfg.Devices, "mcloud-dummy", 8600, 20)
	if err != nil {
		t.Fatal(err)
	}
	a, i := devices[1], devices[2]
	if a.Name != "Dummy_Pixel_8_002" || a.Address != "mcloud-dummy:15556" || a.Connection != "android-sim" ||
		a.AppiumPort != 8621 || a.MinPort != 8623 || a.MaxPort != 8630 {
		t.Fatalf("%+v", a)
	}
	if i.Connection != "proxy" || i.Address != "mcloud-dummy" || i.WDAPort != 18100 || i.MJPEGPort != 18101 || i.OS != "ios" {
		t.Fatalf("%+v", i)
	}
	if _, err := AgentDevices(cfg.Devices, "h", 8600, 5); err == nil {
		t.Fatal("a step of 5: no error")
	}
	if _, err := AgentDevices(cfg.Devices, "h", 65530, 10); err == nil {
		t.Fatal("ports over 65535: no error")
	}
}

func TestReplaceDevices(t *testing.T) {
	vars := "---\nWDA_FILE: /dev/null\n\n# the devices\ndevices:\n  - id: old\n    os: android\n\n# after\nOTHER: 1\n"
	section, err := MarshalDevices([]AgentDevice{{ID: "new", OS: "android", Name: "n", Connection: "android-sim", Address: "h:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(section), "devices:\n  - id: new\n    os: android\n") {
		t.Fatalf("%s", section)
	}
	out, err := ReplaceDevices([]byte(vars), section)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "old") || !strings.Contains(s, "# the devices\ndevices:\n  - id: new") || !strings.HasSuffix(s, "\n# after\nOTHER: 1\n") {
		t.Fatalf("%s", s)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out, &parsed); err != nil || parsed["OTHER"] != 1 {
		t.Fatalf("%v %v", parsed, err)
	}
	if _, err := ReplaceDevices([]byte("A: 1\n"), section); err == nil {
		t.Fatal("no devices: section, no error")
	}
}

const stfDevices = `{"success":true,"devices":[
 {"serial":"A1","model":"Pixel 8 Pro","manufacturer":"Google","platform":"Android","version":"16","sdk":"36","abi":"arm64-v8a","product":"husky","display":{"width":1344,"height":2992,"density":2.625}},
 {"serial":"A2","model":"Pixel 8 Pro","manufacturer":"Google","platform":"Android","version":"16","sdk":"36","abi":"arm64-v8a","product":"husky","display":{"width":1344,"height":2992,"density":2.625}},
 {"serial":"A3","model":"Galaxy Tab S9","manufacturer":"Samsung","platform":"Android","version":"14","sdk":"34","abi":"arm64-v8a","display":{"width":2560,"height":1600,"density":2}},
 {"serial":"I1","model":"iPhone 15","manufacturer":"Apple","platform":"iOS","version":"17.4","display":{"width":1179,"height":2556,"scale":3}},
 {"serial":"X","model":"","platform":"Android","display":{}}
]}`

func TestClone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.URL.Path != "/api/v1/devices" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(stfDevices))
	}))
	defer srv.Close()
	if _, err := FetchDevices(context.Background(), srv.URL, "wrong", false); err == nil {
		t.Fatal("a wrong token: no error")
	}
	devices, err := FetchDevices(context.Background(), srv.URL, "secret", false)
	if err != nil || len(devices) != 5 {
		t.Fatalf("%d %v", len(devices), err)
	}
	out, err := Clone(devices, CloneOptions{Source: "test", Count: 6, Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	p := cfg.Profiles["pixel-8-pro-16"]
	if p.Density != 420 || p.Width != 1344 || p.SDK != 36 || p.Device != "husky" {
		t.Fatalf("pixel: %+v", p)
	}
	if tab := cfg.Profiles["galaxy-tab-s9-14"]; tab.Type != "tablet" || tab.Width != 1600 || tab.Density != 320 {
		t.Fatalf("tab: %+v", tab)
	}
	if ip := cfg.Profiles["iphone-15-17-4"]; ip.Width != 393 || ip.Height != 852 || ip.Scale != 3 || ip.Platform != "ios" {
		t.Fatalf("iphone: %+v", ip)
	}
	android, ios := 0, 0
	for _, d := range cfg.Devices {
		if d.Platform == "android" {
			android++
		} else {
			ios++
		}
	}
	if android != 6 || ios != 6 {
		t.Fatalf("%d android, %d ios", android, ios)
	}
	only, err := Clone(devices, CloneOptions{Platform: "ios"})
	if err != nil || strings.Contains(string(only), "Pixel") {
		t.Fatalf("%v\n%s", err, only)
	}
}
