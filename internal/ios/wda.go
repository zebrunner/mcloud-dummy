// Package ios emulates an iOS device as a fake WebDriverAgent with its MJPEG stream: enough of it for the
// mcloud-ios-connector (proxy connection), STF ios-device and Appium with XCUITest (webDriverAgentUrl).
package ios

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/config"
	"github.com/zebrunner/mcloud-dummy/internal/screen"
	"github.com/zebrunner/mcloud-dummy/internal/ui"
	wd "github.com/zebrunner/mcloud-dummy/internal/webdriver"
)

// AppBundle is the bundle id of the fake app.
const AppBundle = "com.zebrunner.dummy"

// WDABundle is the bundle id WebDriverAgent reports.
const WDABundle = "com.facebook.WebDriverAgentRunner.xctrunner"

// Faults are the injected failures of a device.
type Faults struct {
	Down          bool          `json:"down"`          // WDA port closed
	NoMJPEG       bool          `json:"noMjpeg"`       // MJPEG port closed
	Latency       time.Duration `json:"latency"`       // added before every WDA response
	SingleSession bool          `json:"singleSession"` // a new session invalidates the previous one, like the real WDA
	NotReady      bool          `json:"notReady"`      // every WDA request answers 500 unknown error
}

type session struct {
	id      string
	caps    map[string]any
	created time.Time
}

// Device is one dummy iOS device.
type Device struct {
	Cfg    config.Device
	Log    *slog.Logger
	App    *ui.App
	Screen *screen.Renderer
	Host   string

	mu         sync.Mutex
	wda        *httpListener
	mjpeg      *tcpListener
	sessions   map[string]*session
	last       string
	settings   map[string]any
	faults     Faults
	pasteboard []byte
	started    time.Time
	requests   map[string]int
	streams    int
	mjpegGen   int            // incremented when the MJPEG port is closed: the open streams end
	apps       map[string]int // bundle id -> state (1 not running, 4 foreground)
	done       chan struct{}
}

// New creates a device; Start opens its ports.
func New(cfg config.Device, host string, log *slog.Logger) *Device {
	app := ui.New(ui.Info{Platform: "ios", Name: cfg.Name, Model: cfg.Model, OSVersion: cfg.OSVersion,
		Serial: cfg.Serial, Width: cfg.Width, Height: cfg.Height, Locale: cfg.Locale, TimeZone: cfg.TimeZone})
	ui.ApplyConfig(app, cfg)
	d := &Device{Cfg: cfg, Log: log.With("device", cfg.Serial), App: app, Screen: screen.New(app), Host: host,
		sessions: map[string]*session{}, requests: map[string]int{},
		apps: map[string]int{AppBundle: 4, "com.apple.springboard": 4, "com.apple.mobilesafari": 1, "com.apple.Preferences": 1},
		done: make(chan struct{})}
	d.settings = defaultSettings()
	d.wda = &httpListener{addr: fmt.Sprintf(":%d", cfg.WDAPort), handler: http.HandlerFunc(d.serveWDA)}
	d.mjpeg = &tcpListener{addr: fmt.Sprintf(":%d", cfg.MJPEGPort), handle: d.serveMJPEG}
	return d
}

func defaultSettings() map[string]any {
	return map[string]any{
		"mjpegServerScreenshotQuality": 25, "mjpegServerFramerate": 10, "mjpegScalingFactor": 100,
		"mjpegFixOrientation": false, "screenshotQuality": 1, "elementResponseAttributes": "type,label",
		"shouldUseCompactResponses": true, "waitForIdleTimeout": 10, "animationCoolOffTimeout": 2,
		"snapshotMaxDepth": 50, "useFirstMatch": false, "boundElementsByIndex": false, "reduceMotion": false,
		"defaultActiveApplication": "auto", "activeAppDetectionPoint": "64.00,64.00", "includeNonModalElements": false,
		"acceptAlertButtonSelector": "", "dismissAlertButtonSelector": "", "keyboardAutocorrection": false,
		"keyboardPrediction": false, "customSnapshotTimeout": 15, "dismissAlertButtonSelector2": "",
		"autoClickAlertSelector": "", "maxTypingFrequency": 60, "respectSystemAlerts": false,
		"useClearTextShortcut": true, "limitXpathContextScope": true,
	}
}

// Start opens the WDA and MJPEG ports after the boot time.
func (d *Device) Start() error {
	d.mu.Lock()
	d.started = time.Now()
	d.mu.Unlock()
	go d.App.DrainBattery(d.done, d.Cfg.BatteryRate)
	if err := d.wda.start(); err != nil {
		return err
	}
	if err := d.mjpeg.start(); err != nil {
		d.wda.stop()
		return err
	}
	return nil
}

// Stop closes the ports.
func (d *Device) Stop() {
	select {
	case <-d.done:
	default:
		close(d.done)
	}
	d.wda.stop()
	d.stopMJPEG()
}

func (d *Device) stopMJPEG() {
	d.mjpeg.stop()
	d.mu.Lock()
	d.mjpegGen++
	d.mu.Unlock()
}

func (d *Device) booted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Since(d.started) >= d.Cfg.BootTime
}

// SetFaults applies the injected failures.
func (d *Device) SetFaults(f Faults) {
	d.mu.Lock()
	old := d.faults
	d.faults = f
	d.mu.Unlock()
	d.App.Log("control", "faults", "%+v", f)
	if f.Down != old.Down {
		if f.Down {
			d.wda.stop()
		} else if err := d.wda.start(); err != nil {
			d.Log.Error("WDA port can't be opened", "err", err)
		}
	}
	if f.NoMJPEG != old.NoMJPEG {
		if f.NoMJPEG {
			d.stopMJPEG()
		} else if err := d.mjpeg.start(); err != nil {
			d.Log.Error("MJPEG port can't be opened", "err", err)
		}
	}
}

// Faults returns the injected failures.
func (d *Device) Faults() Faults {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.faults
}

// Reboot closes the ports for the reboot time and drops the sessions.
func (d *Device) Reboot() {
	d.App.Log("control", "reboot", "")
	d.Stop()
	d.mu.Lock()
	d.sessions = map[string]*session{}
	d.last = ""
	d.mu.Unlock()
	time.Sleep(d.Cfg.RebootTime)
	f := d.Faults()
	d.mu.Lock()
	d.started = time.Now()
	d.mu.Unlock()
	if !f.Down {
		_ = d.wda.start()
	}
	if !f.NoMJPEG {
		_ = d.mjpeg.start()
	}
}

// DropSessions invalidates the WDA sessions (a WDA restart).
func (d *Device) DropSessions() {
	d.mu.Lock()
	d.sessions = map[string]*session{}
	d.last = ""
	d.mu.Unlock()
	d.App.Log("control", "sessions", "dropped")
}

// Status is the state of the device for the control API.
type Status struct {
	Platform  string         `json:"platform"`
	Serial    string         `json:"serial"`
	Name      string         `json:"name"`
	Model     string         `json:"model"`
	OSVersion string         `json:"osVersion"`
	WDA       string         `json:"wda"`
	MJPEG     string         `json:"mjpeg"`
	Booted    bool           `json:"booted"`
	Faults    Faults         `json:"faults"`
	Sessions  []string       `json:"sessions"`
	Streams   int            `json:"streams"`
	Requests  map[string]int `json:"requests"`
	Taps      int            `json:"taps"`
	Input     string         `json:"input"`
	Frames    uint64         `json:"frames"`
	Power     ui.Power       `json:"battery"`
	Network   string         `json:"network"`
	Rotation  int            `json:"rotation"`
	Locale    string         `json:"locale"`
	TimeZone  string         `json:"timeZone"`
	Screen    string         `json:"screen"`
}

// Status of the device.
func (d *Device) Status(host string) Status {
	st := Status{Platform: "ios", Serial: d.Cfg.Serial, Name: d.Cfg.Name, Model: d.Cfg.Model, OSVersion: d.Cfg.OSVersion,
		WDA: fmt.Sprintf("http://%s:%d", host, d.Cfg.WDAPort), MJPEG: fmt.Sprintf("http://%s:%d", host, d.Cfg.MJPEGPort),
		Booted: d.booted(), Faults: d.Faults(), Taps: d.App.Taps(), Input: d.App.Input(), Frames: d.Screen.Frames(),
		Requests: map[string]int{}, Power: d.App.Power(), Network: d.App.Network(), Rotation: d.App.Rotation(),
		Locale: d.Cfg.Locale, TimeZone: d.Cfg.TimeZone, Screen: fmt.Sprintf("%dx%d@%d", d.Cfg.Width, d.Cfg.Height, d.Cfg.Scale)}
	d.mu.Lock()
	for id := range d.sessions {
		st.Sessions = append(st.Sessions, id)
	}
	for k, v := range d.requests {
		st.Requests[k] = v
	}
	st.Streams = d.streams
	d.mu.Unlock()
	sort.Strings(st.Sessions)
	return st
}

func (d *Device) idiom() int {
	switch d.Cfg.Type {
	case "tablet":
		return 1
	case "tv":
		return 2
	}
	return 0
}

func (d *Device) osName() string {
	if d.Cfg.Type == "tv" {
		return "tvOS"
	}
	return "iOS"
}

func (d *Device) statusValue() map[string]any {
	device := "iphone"
	switch d.Cfg.Type {
	case "tablet":
		device = "ipad"
	case "tv":
		device = "apple tv"
	}
	return map[string]any{
		"ready": true, "message": "WebDriverAgent is ready to accept commands", "state": "success",
		"os":     map[string]any{"name": d.osName(), "version": d.Cfg.OSVersion, "sdkVersion": d.Cfg.OSVersion, "testmanagerdVersion": 28},
		"ios":    map[string]any{"ip": d.Host},
		"build":  map[string]any{"time": "Jan  1 2026 00:00:00", "productBundleIdentifier": WDABundle, "version": "11.4.3", "upgradedAt": "0"},
		"device": device,
	}
}

func (d *Device) deviceInfo() map[string]any {
	return map[string]any{
		"currentLocale": strings.ReplaceAll(d.Cfg.Locale, "-", "_"), "timeZone": d.Cfg.TimeZone, "name": d.Cfg.Name, "model": d.Cfg.Model,
		"uuid": d.Cfg.Serial, "userInterfaceIdiom": d.idiom(), "userInterfaceStyle": "light", "isSimulator": false,
		"thermalState": 0, "productType": d.Cfg.Device, "systemName": d.osName(), "systemVersion": d.Cfg.OSVersion,
	}
}

func (d *Device) screenInfo() map[string]any {
	w, h := d.App.Size()
	return map[string]any{"screenSize": map[string]any{"width": w, "height": h},
		"statusBarSize": map[string]any{"width": w, "height": 47}, "scale": d.Cfg.Scale}
}

func (d *Device) newSession(caps map[string]any) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.faults.SingleSession {
		d.sessions = map[string]*session{}
	}
	s := &session{id: newUUID(), caps: caps, created: time.Now()}
	d.sessions[s.id] = s
	d.last = s.id
	return s
}

func (d *Device) session(id string) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessions[id]
}

func (d *Device) sessionCaps(s *session) map[string]any {
	caps := map[string]any{
		"device": strings.ToLower(d.Cfg.Model), "browserName": d.App.ForegroundApp(), "sdkVersion": d.Cfg.OSVersion,
		"CFBundleIdentifier": d.App.ForegroundApp(), "platformName": d.osName(), "platformVersion": d.Cfg.OSVersion,
		"udid": d.Cfg.Serial, "deviceName": d.Cfg.Name,
	}
	for k, v := range s.caps {
		if _, ok := caps[k]; !ok {
			caps[k] = v
		}
	}
	return caps
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%X-%X-%X-%X-%X", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (d *Device) countRequest(method, cmd string) {
	key := method + " /" + cmd
	if i := strings.Index(cmd, "element/"); i >= 0 {
		rest := cmd[i+len("element/"):]
		if j := strings.Index(rest, "/"); j >= 0 {
			key = method + " /" + cmd[:i] + "element/:id/" + rest[j+1:]
		} else {
			key = method + " /" + cmd[:i] + "element/:id"
		}
	}
	d.mu.Lock()
	d.requests[key]++
	d.mu.Unlock()
}

func (d *Device) serveWDA(w http.ResponseWriter, r *http.Request) {
	f := d.Faults()
	if f.Latency > 0 {
		time.Sleep(f.Latency)
	}
	sid, cmd := wd.Route(r.URL.Path)
	d.countRequest(r.Method, cmd)
	if !d.booted() || f.NotReady {
		wd.Error(w, "", "unknown error", "WebDriverAgent is not ready")
		return
	}
	if sid == "" {
		switch {
		case cmd == "status":
			body := map[string]any{"value": d.statusValue()}
			d.mu.Lock()
			if d.last != "" {
				body["sessionId"] = d.last
			}
			d.mu.Unlock()
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			writeJSON(w, body)
			return
		case cmd == "health":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("I-AM-ALIVE"))
			return
		case cmd == "session" && r.Method == http.MethodPost:
			body := wd.Body(r)
			caps := map[string]any{}
			if c := wd.Obj(body, "capabilities"); c != nil {
				for k, v := range wd.Obj(c, "alwaysMatch") {
					caps[k] = v
				}
				if fm, ok := c["firstMatch"].([]any); ok && len(fm) > 0 {
					if m, ok := fm[0].(map[string]any); ok {
						for k, v := range m {
							caps[k] = v
						}
					}
				}
			}
			if bundle, ok := caps["bundleId"].(string); ok && bundle != "" {
				d.launch("wda", bundle)
			}
			s := d.newSession(caps)
			d.App.Log("wda", "session", "created %s", s.id)
			wd.Reply(w, s.id, map[string]any{"sessionId": s.id, "capabilities": d.sessionCaps(s)})
			return
		}
		d.command(w, r, "", cmd)
		return
	}
	s := d.session(sid)
	if s == nil {
		wd.Error(w, sid, "invalid session id", fmt.Sprintf("Session does not exist: %s", sid))
		return
	}
	if cmd == "" {
		switch r.Method {
		case http.MethodGet:
			wd.Reply(w, sid, map[string]any{"sessionId": sid, "capabilities": d.sessionCaps(s)})
		case http.MethodDelete:
			d.mu.Lock()
			delete(d.sessions, sid)
			if d.last == sid {
				d.last = ""
			}
			d.mu.Unlock()
			d.App.Log("wda", "session", "deleted %s", sid)
			wd.Reply(w, "", nil)
		default:
			wd.Error(w, sid, "unknown method", "")
		}
		return
	}
	d.command(w, r, sid, cmd)
}

func writeJSON(w http.ResponseWriter, body any) {
	data, _ := json.Marshal(body)
	_, _ = w.Write(data)
}

func (d *Device) launch(source, bundle string) {
	d.mu.Lock()
	for b, st := range d.apps {
		if st == 4 && b != "com.apple.springboard" {
			d.apps[b] = 3
		}
	}
	d.apps[bundle] = 4
	d.mu.Unlock()
	d.App.SetForegroundApp(source, bundle)
}

func (d *Device) element(id string) *ui.Element {
	for _, e := range ui.Elements(d.App.Layout()) {
		if ui.ElementUUID(e.ID) == id {
			return e
		}
	}
	return nil
}

func (d *Device) origin(id string) (int, int, bool) {
	e := d.element(id)
	if e == nil {
		return 0, 0, false
	}
	return e.Rect.X + e.Rect.W/2, e.Rect.Y + e.Rect.H/2, true
}

func (d *Device) find(w http.ResponseWriter, sid string, body map[string]any, many bool) {
	using, value := wd.Str(body, "using"), wd.Str(body, "value")
	found, err := ui.FindIOS(d.App.Layout(), "MCloud Dummy", using, value)
	if err != nil {
		code := "unknown error"
		if errors.Is(err, ui.ErrInvalidSelector) {
			code = "invalid selector"
		}
		wd.Error(w, sid, code, err.Error())
		return
	}
	if many {
		list := make([]any, 0, len(found))
		for _, e := range found {
			list = append(list, wd.ElementRef(ui.ElementUUID(e.ID)))
		}
		wd.Reply(w, sid, list)
		return
	}
	if len(found) == 0 {
		wd.Error(w, sid, "no such element", fmt.Sprintf("unable to find an element using '%s', value '%s'", using, value))
		return
	}
	wd.Reply(w, sid, wd.ElementRef(ui.ElementUUID(found[0].ID)))
}

var pressButtons = map[string]string{"home": "HOME", "volumeup": "VOLUME_UP", "volumedown": "VOLUME_DOWN",
	"menu": "MENU", "select": "SELECT", "up": "UP", "down": "DOWN", "left": "LEFT", "right": "RIGHT", "playpause": "PLAY_PAUSE"}

func (d *Device) command(w http.ResponseWriter, r *http.Request, sid, cmd string) {
	app := d.App
	parts := strings.Split(cmd, "/")
	body := map[string]any{}
	if r.Method == http.MethodPost {
		body = wd.Body(r)
	}
	width, height := app.Size()
	switch {
	case cmd == "appium/settings":
		d.mu.Lock()
		if r.Method == http.MethodPost {
			for k, v := range wd.Obj(body, "settings") {
				d.settings[k] = v
			}
		}
		settings := map[string]any{}
		for k, v := range d.settings {
			settings[k] = v
		}
		d.mu.Unlock()
		wd.Reply(w, sid, settings)
	case cmd == "wda/device/info":
		wd.Reply(w, sid, d.deviceInfo())
	case cmd == "wda/screen":
		wd.Reply(w, sid, d.screenInfo())
	case cmd == "window/size" || (len(parts) == 3 && parts[0] == "window" && parts[2] == "size"):
		wd.Reply(w, sid, map[string]any{"width": width, "height": height})
	case cmd == "window/rect":
		wd.Reply(w, sid, map[string]any{"x": 0, "y": 0, "width": width, "height": height})
	case cmd == "wda/batteryInfo":
		power := d.App.Power()
		state := 1 // UIDeviceBatteryStateUnplugged
		switch power.Status() {
		case 2:
			state = 2
		case 5:
			state = 3
		}
		wd.Reply(w, sid, map[string]any{"level": float64(power.Level) / 100, "state": state})
	case cmd == "wda/healthcheck":
		wd.Reply(w, sid, nil)
	case cmd == "orientation":
		if r.Method == http.MethodPost {
			if strings.HasPrefix(strings.ToUpper(wd.Str(body, "orientation")), "LANDSCAPE") {
				app.SetRotation("wda", 90)
			} else {
				app.SetRotation("wda", 0)
			}
			wd.Reply(w, sid, nil)
			return
		}
		o := "PORTRAIT"
		if app.Rotation()%180 != 0 {
			o = "LANDSCAPE"
		}
		wd.Reply(w, sid, o)
	case cmd == "rotation":
		if r.Method == http.MethodPost {
			z, _ := wd.Num(body, "z")
			app.SetRotation("wda", int(z))
			wd.Reply(w, sid, nil)
			return
		}
		wd.Reply(w, sid, map[string]any{"x": 0, "y": 0, "z": app.Rotation()})
	case cmd == "screenshot":
		pw, ph := width*d.Cfg.Scale, height*d.Cfg.Scale
		wd.Reply(w, sid, base64.StdEncoding.EncodeToString(d.Screen.PNG(pw, ph)))
	case cmd == "source":
		switch r.URL.Query().Get("format") {
		case "json":
			wd.Reply(w, sid, jsonTree(app.Layout()))
		case "description":
			wd.Reply(w, sid, describe(app.Layout()))
		default:
			wd.Reply(w, sid, ui.IOSXML(app.Layout(), "MCloud Dummy"))
		}
	case cmd == "element" || cmd == "elements":
		d.find(w, sid, body, cmd == "elements")
	case cmd == "element/active":
		if e := app.Find("input"); e != nil && e.Focused {
			wd.Reply(w, sid, wd.ElementRef(ui.ElementUUID(e.ID)))
			return
		}
		wd.Error(w, sid, "no such element", "No element is focused")
	case cmd == "actions":
		if r.Method == http.MethodPost {
			for _, g := range wd.Gestures(body, d.origin) {
				if g.Kind == "swipe" {
					app.Swipe("wda", g.X1, g.Y1, g.X2, g.Y2)
				} else {
					app.Tap("wda", g.X1, g.Y1)
				}
			}
			if keys := wd.Keys(body); keys != "" {
				app.Type("wda", keys)
			}
		}
		wd.Reply(w, sid, nil)
	case cmd == "wda/tap" || cmd == "wda/tap/0" || cmd == "wda/doubleTap" || cmd == "wda/touchAndHold":
		if id := wd.ElementID(body); id != "" {
			if e := d.element(id); e != nil {
				app.Click("wda", e.ID)
				wd.Reply(w, sid, nil)
				return
			}
		}
		x, _ := wd.Num(body, "x")
		y, _ := wd.Num(body, "y")
		app.Tap("wda", int(x), int(y))
		wd.Reply(w, sid, nil)
	case cmd == "wda/dragfromtoforduration" || cmd == "wda/pressAndDragWithVelocity":
		x1, _ := wd.Num(body, "fromX")
		y1, _ := wd.Num(body, "fromY")
		x2, _ := wd.Num(body, "toX")
		y2, _ := wd.Num(body, "toY")
		app.Swipe("wda", int(x1), int(y1), int(x2), int(y2))
		wd.Reply(w, sid, nil)
	case cmd == "wda/pressButton":
		name := strings.ToLower(wd.Str(body, "name"))
		key, ok := pressButtons[name]
		if !ok {
			wd.Error(w, sid, "invalid argument", fmt.Sprintf("The button '%s' is unknown", name))
			return
		}
		if key == "HOME" {
			d.launch("wda", "com.apple.springboard")
		}
		app.Key("wda", key)
		wd.Reply(w, sid, nil)
	case cmd == "wda/homescreen":
		d.launch("wda", "com.apple.springboard")
		app.Key("wda", "HOME")
		wd.Reply(w, sid, nil)
	case cmd == "wda/keys" || cmd == "keys":
		app.Type("wda", wd.Text(body))
		wd.Reply(w, sid, nil)
	case cmd == "wda/lock":
		app.SetLocked("wda", true)
		wd.Reply(w, sid, nil)
	case cmd == "wda/unlock":
		app.SetLocked("wda", false)
		wd.Reply(w, sid, nil)
	case cmd == "wda/locked":
		wd.Reply(w, sid, app.Locked())
	case cmd == "wda/apps/launch" || cmd == "wda/apps/activate" || cmd == "wda/apps/launchUnattached":
		d.launch("wda", wd.Str(body, "bundleId"))
		wd.Reply(w, sid, nil)
	case cmd == "wda/apps/terminate":
		bundle := wd.Str(body, "bundleId")
		d.mu.Lock()
		running := d.apps[bundle] > 1
		d.apps[bundle] = 1
		d.mu.Unlock()
		if app.ForegroundApp() == bundle {
			d.launch("wda", "com.apple.springboard")
		}
		wd.Reply(w, sid, running)
	case cmd == "wda/apps/state":
		d.mu.Lock()
		state, ok := d.apps[wd.Str(body, "bundleId")]
		d.mu.Unlock()
		if !ok {
			state = 1
		}
		wd.Reply(w, sid, state)
	case cmd == "wda/activeAppInfo":
		bundle := app.ForegroundApp()
		wd.Reply(w, sid, map[string]any{"pid": 1234, "bundleId": bundle, "name": "", "processArguments": map[string]any{"env": map[string]any{}, "args": []any{}}})
	case cmd == "wda/apps/list":
		wd.Reply(w, sid, []any{map[string]any{"pid": 1234, "bundleId": app.ForegroundApp()}})
	case cmd == "wda/getPasteboard":
		d.mu.Lock()
		data := d.pasteboard
		d.mu.Unlock()
		wd.Reply(w, sid, base64.StdEncoding.EncodeToString(data))
	case cmd == "wda/setPasteboard":
		data, _ := base64.StdEncoding.DecodeString(wd.Str(body, "content"))
		d.mu.Lock()
		d.pasteboard = data
		d.mu.Unlock()
		wd.Reply(w, sid, nil)
	case cmd == "wda/device/appearance":
		wd.Reply(w, sid, nil)
	case cmd == "wda/device/location":
		wd.Reply(w, sid, map[string]any{"authorizationStatus": 3, "latitude": 52.2297, "longitude": 21.0122, "altitude": 100})
	case cmd == "timeouts" || cmd == "url" || cmd == "wda/resetAppAuth" || cmd == "wda/siri/activate" ||
		cmd == "wda/deactivateApp" || cmd == "wda/keyboard/dismiss" || cmd == "wda/performIoHidEvent":
		wd.Reply(w, sid, nil)
	case cmd == "alert/text" || cmd == "alert/accept" || cmd == "alert/dismiss" || cmd == "wda/alert/buttons":
		wd.Error(w, sid, "no such alert", "An attempt was made to operate on a modal dialog when one was not open")
	case len(parts) >= 2 && parts[0] == "element":
		d.elementCommand(w, r, sid, parts[1], strings.Join(parts[2:], "/"), body)
	case len(parts) >= 3 && parts[0] == "wda" && parts[1] == "element":
		e := d.element(parts[2])
		if e == nil {
			wd.Error(w, sid, "stale element reference", "The previously found element is not present in the current view anymore")
			return
		}
		sub := strings.Join(parts[3:], "/")
		switch sub {
		case "swipe":
			cx, cy := e.Rect.X+e.Rect.W/2, e.Rect.Y+e.Rect.H/2
			delta := map[string][2]int{"up": {0, -200}, "down": {0, 200}, "left": {-200, 0}, "right": {200, 0}}[wd.Str(body, "direction")]
			app.Swipe("wda", cx, cy, cx+delta[0], cy+delta[1])
		default:
			app.Click("wda", e.ID)
		}
		wd.Reply(w, sid, nil)
	default:
		app.Log("wda", "unknown", "%s /%s", r.Method, cmd)
		wd.Error(w, sid, "unknown command", fmt.Sprintf("Unhandled endpoint: /%s", cmd))
	}
}

func (d *Device) elementCommand(w http.ResponseWriter, r *http.Request, sid, id, sub string, body map[string]any) {
	app := d.App
	e := d.element(id)
	if e == nil {
		wd.Error(w, sid, "stale element reference", "The previously found element is not present in the current view anymore")
		return
	}
	switch {
	case sub == "element" || sub == "elements":
		d.find(w, sid, body, sub == "elements")
	case sub == "click":
		app.Click("wda", e.ID)
		wd.Reply(w, sid, nil)
	case sub == "clear":
		app.SetText("wda", e.ID, "")
		wd.Reply(w, sid, nil)
	case sub == "value":
		app.Click("wda", e.ID)
		app.Type("wda", wd.Text(body))
		wd.Reply(w, sid, nil)
	case sub == "text":
		v, _ := ui.IOSAttr(e, "label")
		if e.Kind == ui.KindInput {
			v = e.Text
		}
		wd.Reply(w, sid, v)
	case sub == "name":
		wd.Reply(w, sid, ui.IOSType(e))
	case strings.HasPrefix(sub, "attribute/"):
		name := strings.TrimPrefix(sub, "attribute/")
		if name == "rect" {
			wd.Reply(w, sid, fmt.Sprintf(`{"x":%d,"y":%d,"width":%d,"height":%d}`, e.Rect.X, e.Rect.Y, e.Rect.W, e.Rect.H))
			return
		}
		v, ok := ui.IOSAttr(e, name)
		if !ok {
			wd.Reply(w, sid, nil)
			return
		}
		wd.Reply(w, sid, v)
	case sub == "rect":
		wd.Reply(w, sid, map[string]any{"x": e.Rect.X, "y": e.Rect.Y, "width": e.Rect.W, "height": e.Rect.H})
	case sub == "displayed" || sub == "enabled" || sub == "wda/accessible":
		wd.Reply(w, sid, true)
	case sub == "selected":
		wd.Reply(w, sid, e.Focused)
	case sub == "screenshot":
		width, height := app.Size()
		wd.Reply(w, sid, base64.StdEncoding.EncodeToString(d.Screen.PNG(width*d.Cfg.Scale, height*d.Cfg.Scale)))
	default:
		app.Log("wda", "unknown", "%s /element/%s/%s", r.Method, id, sub)
		wd.Error(w, sid, "unknown command", "Unhandled endpoint: /element/"+id+"/"+sub)
	}
}

func jsonTree(e *ui.Element) map[string]any {
	node := map[string]any{"type": ui.IOSType(e), "name": e.ID, "label": e.Text, "value": e.Text, "isEnabled": "1",
		"rect": map[string]any{"x": e.Rect.X, "y": e.Rect.Y, "width": e.Rect.W, "height": e.Rect.H}}
	if len(e.Children) > 0 {
		var children []any
		for _, c := range e.Children {
			children = append(children, jsonTree(c))
		}
		node["children"] = children
	}
	return node
}

func describe(root *ui.Element) string {
	var b strings.Builder
	for _, e := range ui.Elements(root) {
		fmt.Fprintf(&b, "%s, {{%d, %d}, {%d, %d}}, identifier: '%s', label: '%s'\n", strings.TrimPrefix(ui.IOSType(e), "XCUIElementType"),
			e.Rect.X, e.Rect.Y, e.Rect.W, e.Rect.H, e.ID, e.Text)
	}
	return b.String()
}

// serveMJPEG writes the screen as WebDriverAgent's MJPEG server does: a raw TCP server that starts the HTTP/1.0
// multipart/x-mixed-replace response at once, without waiting for a request (STF reads the plain socket).
func (d *Device) serveMJPEG(conn net.Conn) {
	defer conn.Close()
	d.mu.Lock()
	d.streams++
	gen := d.mjpegGen
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.streams--
		d.mu.Unlock()
	}()
	d.App.Log("mjpeg", "connect", "%s", conn.RemoteAddr())
	buf := bufio.NewWriter(conn)
	_, _ = buf.WriteString("HTTP/1.0 200 OK\r\nServer: WDA MJPEG Server\r\nConnection: close\r\nMax-Age: 0\r\nExpires: 0\r\n" +
		"Cache-Control: no-cache, private\r\nPragma: no-cache\r\nContent-Type: multipart/x-mixed-replace; boundary=--BoundaryString\r\n\r\n")
	if buf.Flush() != nil {
		return
	}
	closed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}()
	var lastVersion uint64 = ^uint64(0)
	var lastSecond int64
	for {
		d.mu.Lock()
		fps, _ := wd.Num(d.settings, "mjpegServerFramerate")
		scale, _ := wd.Num(d.settings, "mjpegScalingFactor")
		quality, _ := wd.Num(d.settings, "mjpegServerScreenshotQuality")
		fixOrientation, _ := d.settings["mjpegFixOrientation"].(bool)
		stopped := gen != d.mjpegGen
		d.mu.Unlock()
		if stopped {
			return
		}
		if fps <= 0 || fps > 60 {
			fps = 10
		}
		if limit := float64(d.Cfg.FPS); limit > 0 && fps > limit {
			fps = limit
		}
		if scale <= 0 || scale > 100 {
			scale = 100
		}
		if quality <= 0 {
			quality = 25
		}
		version, second := d.App.Version(), time.Now().Unix()
		if version != lastVersion || second != lastSecond {
			factor := scale / 100 * float64(d.Cfg.MJPEGScale) / 100
			var jpg []byte
			if fixOrientation {
				w, h := d.App.Size()
				jpg = d.Screen.JPEG(int(float64(w*d.Cfg.Scale)*factor), int(float64(h*d.Cfg.Scale)*factor), int(quality))
			} else {
				// the frames of the framebuffer, as the STF UI expects them: it turns the landscape screen itself
				w, h := d.Cfg.Width, d.Cfg.Height
				jpg = d.Screen.NaturalJPEG(int(float64(w*d.Cfg.Scale)*factor), int(float64(h*d.Cfg.Scale)*factor), int(quality))
			}
			_, err := fmt.Fprintf(buf, "--BoundaryString\r\nContent-type: image/jpg\r\nContent-Length: %d\r\n\r\n", len(jpg))
			if err == nil {
				_, err = buf.Write(jpg)
			}
			if err == nil {
				_, err = buf.WriteString("\r\n\r\n")
			}
			if err == nil {
				err = buf.Flush()
			}
			if err != nil {
				return
			}
			lastVersion, lastSecond = version, second
		}
		select {
		case <-closed:
			return
		case <-time.After(time.Duration(float64(time.Second) / fps)):
		}
	}
}

// tcpListener is a raw TCP server that can be stopped and started again; stop closes the open connections too.
type tcpListener struct {
	addr   string
	handle func(net.Conn)
	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
}

func (l *tcpListener) start() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", l.addr)
	if err != nil {
		return err
	}
	l.ln = ln
	if l.conns == nil {
		l.conns = map[net.Conn]struct{}{}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			l.mu.Lock()
			l.conns[c] = struct{}{}
			l.mu.Unlock()
			go func() {
				l.handle(c)
				l.mu.Lock()
				delete(l.conns, c)
				l.mu.Unlock()
			}()
		}
	}()
	return nil
}

func (l *tcpListener) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln != nil {
		_ = l.ln.Close()
		l.ln = nil
	}
	for c := range l.conns {
		_ = c.Close()
	}
}

// httpListener is an http.Server that can be stopped and started again.
type httpListener struct {
	addr    string
	handler http.Handler
	mu      sync.Mutex
	srv     *http.Server
}

func (l *httpListener) start() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", l.addr)
	if err != nil {
		return err
	}
	l.srv = &http.Server{Handler: l.handler, ReadHeaderTimeout: 30 * time.Second}
	go func(srv *http.Server) { _ = srv.Serve(ln) }(l.srv)
	return nil
}

func (l *httpListener) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.srv != nil {
		_ = l.srv.Close()
		l.srv = nil
	}
}
