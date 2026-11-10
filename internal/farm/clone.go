package farm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// STFDevice is a device of GET /api/v1/devices of STF: the fields a profile is made of.
type STFDevice struct {
	Serial       string `json:"serial"`
	Model        string `json:"model"`
	Manufacturer string `json:"manufacturer"`
	Platform     string `json:"platform"` // Android or iOS
	Version      string `json:"version"`
	SDK          string `json:"sdk"`
	ABI          string `json:"abi"`
	Product      string `json:"product"`
	Display      struct {
		Width   int     `json:"width"`   // pixels
		Height  int     `json:"height"`  // pixels
		Density float64 `json:"density"` // Android: DisplayMetrics.density (dpi / 160)
		Scale   float64 `json:"scale"`   // iOS: pixels per point
	} `json:"display"`
}

// FetchDevices reads the devices of an STF with an access token.
func FetchDevices(ctx context.Context, stfURL, token string, insecure bool) ([]STFDevice, error) {
	url := strings.TrimRight(stfURL, "/") + "/api/v1/devices?fields=serial,model,manufacturer,platform,version,sdk,abi,product,display"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 30 * time.Second}
	if insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // -insecure of a lab STF
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	var body struct {
		Success bool        `json:"success"`
		Devices []STFDevice `json:"devices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return body.Devices, nil
}

// Profile is a profile of the configuration of mcloud-dummy made of a real device.
type Profile struct {
	Platform     string `yaml:"platform"`
	Model        string `yaml:"model"`
	Manufacturer string `yaml:"manufacturer,omitempty"`
	Device       string `yaml:"device,omitempty"`
	Type         string `yaml:"type,omitempty"`
	OSVersion    string `yaml:"osVersion,omitempty"`
	SDK          int    `yaml:"sdk,omitempty"`
	ABI          string `yaml:"abi,omitempty"`
	Width        int    `yaml:"width,omitempty"`
	Height       int    `yaml:"height,omitempty"`
	Density      int    `yaml:"density,omitempty"`
	Scale        int    `yaml:"scale,omitempty"`
}

// ProfileOf makes the profile of a real device: the screen in pixels and dpi (Android) or in points and scale (iOS).
func ProfileOf(d STFDevice) Profile {
	p := Profile{Model: d.Model, Manufacturer: d.Manufacturer, OSVersion: d.Version, ABI: d.ABI}
	w, h := d.Display.Width, d.Display.Height
	if w > h {
		w, h = h, w
	}
	if strings.EqualFold(d.Platform, "ios") {
		p.Platform, p.ABI = "ios", ""
		p.Scale = int(math.Round(d.Display.Scale))
		if p.Scale < 1 {
			p.Scale = 1
		}
		p.Width, p.Height = w/p.Scale, h/p.Scale
		if p.Width >= 744 {
			p.Type = "tablet"
		}
		return p
	}
	p.Platform, p.Device = "android", d.Product
	p.SDK, _ = strconv.Atoi(d.SDK)
	p.Width, p.Height = w, h
	p.Density = int(math.Round(d.Display.Density * 160))
	if p.Density > 0 && w*160/p.Density >= 600 {
		p.Type = "tablet"
	}
	return p
}

// CloneOptions shape the fleet of a clone.
type CloneOptions struct {
	Source   string // the STF of the comment
	Platform string // android, ios or empty for both
	Count    int    // devices per platform; 0 is as many as the real ones
	Seed     int64
}

// Clone makes a configuration of mcloud-dummy with a profile per model of the real devices and an entry per platform
// that picks the profiles as often as the farm has them, with a random battery, charging and network.
func Clone(devices []STFDevice, opt CloneOptions) ([]byte, error) {
	profiles := map[string]Profile{}
	weights := map[string][]string{} // platform -> profile names, one per real device
	for _, d := range devices {
		p := ProfileOf(d)
		if (opt.Platform != "" && opt.Platform != p.Platform) || p.Model == "" || p.Width == 0 {
			continue
		}
		name := profileName(p)
		profiles[name] = p
		weights[p.Platform] = append(weights[p.Platform], name)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no devices with a model and a display")
	}
	type entry struct {
		Platform  string         `yaml:"platform"`
		Count     int            `yaml:"count"`
		Port      int            `yaml:"port,omitempty"`
		WDAPort   int            `yaml:"wdaPort,omitempty"`
		MJPEGPort int            `yaml:"mjpegPort,omitempty"`
		Seed      int64          `yaml:"seed"`
		Vary      map[string]any `yaml:"vary"`
	}
	var entries []entry
	for _, platform := range []string{"android", "ios"} {
		names := weights[platform]
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		count := opt.Count
		if count == 0 {
			count = len(names)
		}
		e := entry{Platform: platform, Count: count, Seed: opt.Seed, Vary: map[string]any{
			"profile": names, "battery": "5-100", "charging": []bool{true, true, false}, "network": []string{"wifi", "wifi", "cellular"},
		}}
		if platform == "android" {
			e.Port = 15555
		} else {
			e.WDAPort, e.MJPEGPort = 18100, 18101
		}
		entries = append(entries, e)
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "---\n# mcloud-dummy clone of %s: %d devices, %d models; the profiles are picked as often as the farm has them.\n",
		opt.Source, len(devices), len(profiles))
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	err := enc.Encode(struct {
		Profiles map[string]Profile `yaml:"profiles"`
		Devices  []entry            `yaml:"devices"`
	}{profiles, entries})
	return buf.Bytes(), err
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// profileName is the model and the OS version: pixel-8-pro-16.
func profileName(p Profile) string {
	return strings.Trim(nonWord.ReplaceAllString(strings.ToLower(p.Model+" "+p.OSVersion), "-"), "-")
}
