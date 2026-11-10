// Package config reads the description of the dummy devices.
package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // the time zones of the devices in an image without zoneinfo

	"gopkg.in/yaml.v3"
)

// Config of mcloud-dummy.
type Config struct {
	// Control is the listen address of the control API and the dashboard.
	Control string `yaml:"control"`
	// Host is the address the dummies advertise (e.g. the WDA URL of /status); the listeners bind all interfaces.
	Host string `yaml:"host"`
	// Profiles are named partial devices (a model of a real device): an entry takes one with `profile` or varies them
	// with `vary: {profile: [...]}`.
	Profiles map[string]Device `yaml:"profiles"`
	// Devices to emulate; an entry with count > 1 is expanded into numbered devices with consecutive ports.
	Devices []Device `yaml:"devices"`
}

// Device is one dummy device, or an entry of devices that is expanded into Count of them.
type Device struct {
	Platform     string `yaml:"platform"` // android or ios
	Count        int    `yaml:"count"`
	Serial       string `yaml:"serial"` // Android ro.serialno / iOS UDID
	Name         string `yaml:"name"`
	Model        string `yaml:"model"`
	Manufacturer string `yaml:"manufacturer"`
	Brand        string `yaml:"brand"`
	Device       string `yaml:"device"` // ro.product.device / iOS product type
	Type         string `yaml:"type"`   // phone, tablet or tv
	OSVersion    string `yaml:"osVersion"`
	SDK          int    `yaml:"sdk"`
	ABI          string `yaml:"abi"`
	Width        int    `yaml:"width"`  // pixels (Android) or points (iOS)
	Height       int    `yaml:"height"` // pixels (Android) or points (iOS)
	Density      int    `yaml:"density"`
	Scale        int    `yaml:"scale"` // iOS screen scale

	// The state of the device at the start; the control API changes it at runtime.
	Battery     *int    `yaml:"battery"`     // percent, 100 by default
	Charging    *bool   `yaml:"charging"`    // plugged, true by default
	BatteryRate float64 `yaml:"batteryRate"` // percent per minute: discharges unplugged, charges plugged; 0 keeps it
	Rotation    int     `yaml:"rotation"`    // 0, 90, 180 or 270
	Locale      string  `yaml:"locale"`      // en-US
	TimeZone    string  `yaml:"timeZone"`    // UTC
	Network     string  `yaml:"network"`     // wifi, cellular or none

	// Android: the adb port (adb connect <host>:<port>).
	Port int `yaml:"port"`
	// iOS: the WebDriverAgent and MJPEG ports.
	WDAPort   int `yaml:"wdaPort"`
	MJPEGPort int `yaml:"mjpegPort"`

	// Auth of adb: none (default, like an emulator), accept or deny (stays unauthorized).
	Auth string `yaml:"auth"`
	// BootTime is how long sys.boot_completed stays 0 after a start or a reboot.
	BootTime time.Duration `yaml:"bootTime"`
	// RebootTime is how long the device is gone during a reboot.
	RebootTime time.Duration `yaml:"rebootTime"`
	// FPS is the maximum frame rate of the screen streams.
	FPS int `yaml:"fps"`
	// MJPEGScale (iOS) is the resolution of the MJPEG stream in percent of the screen pixels, applied with the
	// mjpegScalingFactor setting of WDA: 100 is the real size, a smaller one saves CPU for many devices.
	MJPEGScale int `yaml:"mjpegScale"`

	// Profile is the name of a profile of Config.Profiles the entry starts from.
	Profile string `yaml:"profile"`
	// Vary gives every device of the entry its own value of a field: a list of values ([a, b]), a range of
	// numbers ("5-100") or a single value; `profile` varies the profiles, `screen` the size ("1080x2400@420").
	Vary map[string]any `yaml:"vary"`
	// Pick is how the values of Vary are taken: random (by Seed, the same for the same configuration) or cycle.
	Pick string `yaml:"pick"`
	// Seed of the random picks.
	Seed int64 `yaml:"seed"`
}

// Load reads a YAML configuration and applies the defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses a YAML configuration, expands its entries and applies the defaults.
func Parse(data []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if c.Control == "" {
		c.Control = ":8800"
	}
	if c.Host == "" {
		c.Host, _ = os.Hostname()
	}
	var out []Device
	for i, d := range c.Devices {
		expanded, err := expand(d, c.Profiles)
		if err != nil {
			return nil, fmt.Errorf("devices[%d]: %w", i, err)
		}
		out = append(out, expanded...)
	}
	seen := map[string]bool{}
	ports := map[int]string{}
	for _, d := range out {
		if seen[d.Serial] {
			return nil, fmt.Errorf("duplicate serial %q", d.Serial)
		}
		seen[d.Serial] = true
		for _, p := range []int{d.Port, d.WDAPort, d.MJPEGPort} {
			if p == 0 {
				continue
			}
			if other, ok := ports[p]; ok {
				return nil, fmt.Errorf("port %d of %q is already used by %q", p, d.Serial, other)
			}
			ports[p] = d.Serial
		}
	}
	c.Devices = out
	return &c, nil
}

// expand makes the devices of an entry: the profile, then the fields of the entry, then the values of Vary, then
// the numbering of the serials, the names and the ports, then the defaults.
func expand(entry Device, profiles map[string]Device) ([]Device, error) {
	if entry.Platform != "android" && entry.Platform != "ios" {
		return nil, fmt.Errorf("platform must be android or ios, got %q", entry.Platform)
	}
	if entry.Pick != "" && entry.Pick != "random" && entry.Pick != "cycle" {
		return nil, fmt.Errorf("pick must be random or cycle, got %q", entry.Pick)
	}
	count := max(entry.Count, 1)
	keys := make([]string, 0, len(entry.Vary))
	for k := range entry.Vary {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Device, 0, count)
	for i := 0; i < count; i++ {
		picks := map[string]any{}
		for _, k := range keys {
			v, err := pick(entry, k, i, count)
			if err != nil {
				return nil, err
			}
			picks[k] = v
		}
		profileName := entry.Profile
		if p, ok := picks["profile"]; ok {
			profileName = fmt.Sprint(p)
		}
		var n Device
		if profileName != "" {
			p, ok := profiles[profileName]
			if !ok {
				return nil, fmt.Errorf("unknown profile %q", profileName)
			}
			if p.Platform != "" && p.Platform != entry.Platform {
				return nil, fmt.Errorf("profile %q is %s, the entry is %s", profileName, p.Platform, entry.Platform)
			}
			n = p
		}
		overlay(&n, entry)
		n.Platform, n.Count, n.Vary, n.Profile = entry.Platform, 1, nil, profileName
		for _, k := range keys {
			if k == "profile" {
				continue
			}
			if err := setField(&n, k, picks[k]); err != nil {
				return nil, fmt.Errorf("vary %s: %w", k, err)
			}
		}
		name := n.Name
		if n.Platform == "android" {
			androidDefaults(&n)
		} else {
			iosDefaults(&n)
		}
		if err := commonDefaults(&n); err != nil {
			return nil, err
		}
		if name == "" {
			name = n.Name
		}
		n.Name = name
		if count > 1 || strings.HasSuffix(n.Serial, "-") {
			number(&n, i, count > 1)
		}
		out = append(out, n)
	}
	return out, nil
}

// number makes the serial, the name and the ports of the i-th device of an entry.
func number(n *Device, i int, rename bool) {
	if n.Platform == "ios" {
		n.Serial = iosUDID(n.Serial, i+1)
	} else {
		n.Serial = fmt.Sprintf("%s%03d", n.Serial, i+1)
	}
	if rename {
		n.Name = fmt.Sprintf("%s %03d", n.Name, i+1)
	}
	if n.Port != 0 {
		n.Port += i
	}
	if n.WDAPort != 0 {
		n.WDAPort += 2 * i
		n.MJPEGPort += 2 * i
	}
}

var localeRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

func commonDefaults(d *Device) error {
	if d.Auth == "" {
		d.Auth = "none"
	}
	if d.Auth != "none" && d.Auth != "accept" && d.Auth != "deny" {
		return fmt.Errorf("auth must be none, accept or deny, got %q", d.Auth)
	}
	if d.RebootTime == 0 {
		d.RebootTime = 10 * time.Second
	}
	if d.FPS == 0 {
		d.FPS = 10
	}
	if d.MJPEGScale <= 0 || d.MJPEGScale > 100 {
		d.MJPEGScale = 100
	}
	if d.Battery == nil {
		d.Battery = ptr(100)
	}
	if *d.Battery < 0 || *d.Battery > 100 {
		return fmt.Errorf("battery must be 0-100, got %d", *d.Battery)
	}
	if d.Charging == nil {
		d.Charging = ptr(true)
	}
	if d.BatteryRate < 0 {
		return fmt.Errorf("batteryRate must not be negative, got %v", d.BatteryRate)
	}
	if d.Rotation%90 != 0 || d.Rotation < 0 || d.Rotation > 270 {
		return fmt.Errorf("rotation must be 0, 90, 180 or 270, got %d", d.Rotation)
	}
	if d.Locale == "" {
		d.Locale = "en-US"
	}
	if !localeRe.MatchString(d.Locale) {
		return fmt.Errorf("locale must be a language and a region like en-US, got %q", d.Locale)
	}
	if d.TimeZone == "" {
		d.TimeZone = "UTC"
	}
	if _, err := time.LoadLocation(d.TimeZone); err != nil {
		return fmt.Errorf("timeZone %q: %w", d.TimeZone, err)
	}
	if d.Network == "" {
		d.Network = "wifi"
	}
	if d.Network != "wifi" && d.Network != "cellular" && d.Network != "none" {
		return fmt.Errorf("network must be wifi, cellular or none, got %q", d.Network)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// iosUDID makes a numbered UDID of the form of a real device (25 characters: 8 hex, a dash, 16 hex).
func iosUDID(prefix string, n int) string {
	if len(prefix) == 25 && prefix[8] == '-' {
		return fmt.Sprintf("%s%04X", prefix[:21], n)
	}
	return fmt.Sprintf("%s%03d", prefix, n)
}

func androidDefaults(d *Device) {
	set := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	set(&d.Serial, "dummy-android-")
	set(&d.Model, "Dummy Pixel")
	set(&d.Manufacturer, "Zebrunner")
	set(&d.Brand, strings.ToLower(strings.ReplaceAll(d.Manufacturer, " ", "")))
	set(&d.Device, "dummy")
	set(&d.Type, "phone")
	set(&d.OSVersion, "14")
	set(&d.ABI, "x86_64")
	set(&d.Name, d.Model)
	if d.SDK == 0 {
		d.SDK = androidSDK(d.OSVersion)
	}
	if d.Width == 0 || d.Height == 0 {
		d.Width, d.Height = 720, 1280
		if d.Type == "tablet" {
			d.Width, d.Height = 1200, 1920
		}
		if d.Type == "tv" {
			d.Width, d.Height = 1920, 1080
		}
	}
	if d.Density == 0 {
		d.Density = 320
	}
	if d.Port == 0 {
		d.Port = 15555
	}
	if d.BootTime == 0 {
		d.BootTime = 3 * time.Second
	}
}

func androidSDK(release string) int {
	major := release
	if i := strings.IndexByte(release, '.'); i >= 0 {
		major = release[:i]
	}
	sdk := map[string]int{"8": 26, "9": 28, "10": 29, "11": 30, "12": 31, "13": 33, "14": 34, "15": 35, "16": 36}[major]
	if release == "8.1" || release == "8.1.0" {
		sdk = 27
	}
	if release == "12L" {
		sdk = 32
	}
	if sdk == 0 {
		sdk = 34
	}
	return sdk
}

func iosDefaults(d *Device) {
	set := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	set(&d.Type, "phone")
	set(&d.Serial, "00008110-000A1B2C3D4E0000")
	set(&d.OSVersion, "17.4")
	switch d.Type {
	case "tablet":
		set(&d.Model, "iPad")
		set(&d.Device, "iPad13,18")
	case "tv":
		set(&d.Model, "Apple TV")
		set(&d.Device, "AppleTV14,1")
	default:
		set(&d.Model, "iPhone")
		set(&d.Device, "iPhone15,2")
	}
	set(&d.Name, "Dummy "+d.Model)
	set(&d.Manufacturer, "Apple")
	if d.Width == 0 || d.Height == 0 {
		d.Width, d.Height = 390, 844
		if d.Type == "tablet" {
			d.Width, d.Height = 820, 1180
		}
		if d.Type == "tv" {
			d.Width, d.Height = 1920, 1080
		}
	}
	if d.Scale == 0 {
		d.Scale = 3
		if d.Type != "phone" {
			d.Scale = 2
		}
		if d.Type == "tv" {
			d.Scale = 1
		}
	}
	if d.WDAPort == 0 {
		d.WDAPort = 18100
	}
	if d.MJPEGPort == 0 {
		d.MJPEGPort = d.WDAPort + 1
	}
	if d.BootTime == 0 {
		d.BootTime = time.Second
	}
}
