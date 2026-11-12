// Package ui is the fake application shown by a dummy device: a few elements laid out on the screen, which react to
// the taps and the typing coming from STF (minitouch, WDA actions) and from Appium (UiAutomator2, XCUITest).
package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Kind of an element.
type Kind int

const (
	KindContainer Kind = iota
	KindLabel
	KindButton
	KindInput
)

// Rect in device coordinates (pixels on Android, points on iOS).
type Rect struct{ X, Y, W, H int }

func (r Rect) Contains(x, y int) bool { return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H }

// Element of the fake app.
type Element struct {
	ID       string // stable: resource-id suffix on Android, accessibility id on iOS
	Kind     Kind
	Text     string
	Rect     Rect
	Focused  bool
	Children []*Element
}

// Touch is a recent touch shown on the screen.
type Touch struct {
	X, Y int
	At   time.Time
	Up   bool
}

// Event is something that happened on the device, kept for the control API.
type Event struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
}

// Info is the static description of the device shown by the app.
type Info struct {
	Platform  string // android or ios
	Name      string
	Model     string
	OSVersion string
	Serial    string
	Width     int // device coordinates of the natural orientation
	Height    int
	Locale    string // en-US
	TimeZone  string // UTC
}

// Power is the battery of the device.
type Power struct {
	Level    int  `json:"level"` // percent
	Charging bool `json:"charging"`
}

// Status is the BatteryManager status of Android: 2 charging, 3 discharging, 5 full.
func (p Power) Status() int {
	switch {
	case p.Charging && p.Level >= 100:
		return 5
	case p.Charging:
		return 2
	}
	return 3
}

// App is the state of the fake application of one device.
type App struct {
	info Info

	mu       sync.Mutex
	taps     int
	input    string
	focused  bool
	lastKey  string
	lastTap  string
	touches  []Touch
	events   []Event
	version  uint64
	rotation int
	locked   bool
	appState string // foreground app id
	listener []func()
	power    Power
	charge   float64 // the fraction of a percent of the battery simulation
	network  string  // wifi, cellular or none
}

const maxEvents = 200

// New creates the app of a device.
func New(info Info) *App {
	return &App{info: info, appState: "com.zebrunner.dummy", power: Power{Level: 100, Charging: true}, network: "wifi"}
}

// Power is the battery state.
func (a *App) Power() Power {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.power
}

// SetPower sets the battery level (clamped to 0-100) and whether it charges.
func (a *App) SetPower(source string, p Power) {
	p.Level = max(0, min(100, p.Level))
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.power == p {
		return
	}
	a.power = p
	a.charge = 0
	a.logLocked(source, "battery", fmt.Sprintf("%d%% charging=%v", p.Level, p.Charging))
	a.changed()
}

// Network is the connection of the device: wifi, cellular or none.
func (a *App) Network() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.network
}

// SetNetwork changes the connection of the device.
func (a *App) SetNetwork(source, network string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.network == network {
		return
	}
	a.network = network
	a.logLocked(source, "network", network)
	a.changed()
}

// drainStep is how often the battery simulation updates the level.
var drainStep = 5 * time.Second

// DrainBattery runs the battery: rate percent per minute, down while unplugged, up while charging, until done is closed.
func (a *App) DrainBattery(done <-chan struct{}, rate float64) {
	if rate <= 0 {
		return
	}
	step := drainStep
	t := time.NewTicker(step)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		a.mu.Lock()
		delta := rate * step.Minutes()
		if !a.power.Charging {
			delta = -delta
		}
		a.charge += delta
		whole := int(a.charge)
		if whole != 0 {
			a.charge -= float64(whole)
			level := max(0, min(100, a.power.Level+whole))
			if level != a.power.Level {
				a.power.Level = level
				a.changed()
			}
		}
		a.mu.Unlock()
	}
}

// Now is the time of the device in its time zone.
func (a *App) Now() time.Time {
	if loc, err := time.LoadLocation(a.info.TimeZone); err == nil {
		return time.Now().In(loc)
	}
	return time.Now()
}

// Info of the device.
func (a *App) Info() Info { return a.info }

// OnChange registers a callback called after every change of the screen.
func (a *App) OnChange(f func()) {
	a.mu.Lock()
	a.listener = append(a.listener, f)
	a.mu.Unlock()
}

func (a *App) changed() {
	a.version++
	ls := append([]func(){}, a.listener...)
	go func() {
		for _, f := range ls {
			f()
		}
	}()
}

// Version changes with every change of the screen content.
func (a *App) Version() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.version
}

// Log records an event.
func (a *App) Log(source, kind, format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.logLocked(source, kind, fmt.Sprintf(format, args...))
}

func (a *App) logLocked(source, kind, detail string) {
	a.events = append(a.events, Event{At: time.Now(), Source: source, Kind: kind, Detail: detail})
	if len(a.events) > maxEvents {
		a.events = a.events[len(a.events)-maxEvents:]
	}
}

// Events returns the recent events, the newest last.
func (a *App) Events() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Event(nil), a.events...)
}

// Size of the screen in device coordinates for the current rotation.
func (a *App) Size() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sizeLocked()
}

func (a *App) sizeLocked() (int, int) {
	if a.rotation%180 != 0 {
		return a.info.Height, a.info.Width
	}
	return a.info.Width, a.info.Height
}

// Rotation in degrees (0, 90, 180, 270).
func (a *App) Rotation() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rotation
}

// SetRotation changes the orientation of the screen.
func (a *App) SetRotation(source string, deg int) {
	deg = ((deg % 360) + 360) % 360
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rotation == deg {
		return
	}
	a.rotation = deg
	a.logLocked(source, "rotate", fmt.Sprintf("%d", deg))
	a.changed()
}

// Locked tells whether the screen is locked.
func (a *App) Locked() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.locked
}

// SetLocked locks or unlocks the screen.
func (a *App) SetLocked(source string, locked bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locked == locked {
		return
	}
	a.locked = locked
	a.logLocked(source, "lock", fmt.Sprintf("%v", locked))
	a.changed()
}

// ForegroundApp is the id of the app in the foreground.
func (a *App) ForegroundApp() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.appState
}

// SetForegroundApp records a launched app.
func (a *App) SetForegroundApp(source, id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.appState = id
	a.logLocked(source, "app", id)
	a.changed()
}

// Layout returns the element tree for the current rotation, in device coordinates.
func (a *App) Layout() *Element {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.layoutLocked()
}

// LayoutUnit is the unit of the layout and of the screen of a w x h screen: a twentieth of the width, but the app
// (about 30 units high) and the footer of the screen keep within 32 units of the height, for a wide screen too (a
// tablet, landscape).
func LayoutUnit(w, h int) int {
	return max(1, min(min(w, h)/20, h/32))
}

func (a *App) layoutLocked() *Element {
	w, h := a.sizeLocked()
	u := LayoutUnit(w, h)
	margin := u
	rowH := u * 2
	y := margin * 3
	row := func(id string, kind Kind, text string, height int) *Element {
		e := &Element{ID: id, Kind: kind, Text: text, Rect: Rect{X: margin, Y: y, W: w - 2*margin, H: height}}
		y += height + u/2
		return e
	}
	osName := "Android"
	if a.info.Platform == "ios" {
		osName = "iOS"
	}
	title := row("title", KindLabel, "MCloud Dummy", rowH)
	device := row("device", KindLabel, fmt.Sprintf("%s - %s %s", a.info.Model, osName, a.info.OSVersion), rowH)
	serial := row("serial", KindLabel, a.info.Serial, rowH)
	y += u
	button := row("tap_button", KindButton, "Tap me", rowH*2)
	counter := row("counter", KindLabel, fmt.Sprintf("Taps: %d", a.taps), rowH)
	input := row("input", KindInput, a.input, rowH*2)
	input.Focused = a.focused
	reset := row("reset_button", KindButton, "Reset", rowH*2)
	last := row("last_event", KindLabel, a.lastEventLocked(), rowH)
	root := &Element{ID: "root", Kind: KindContainer, Rect: Rect{W: w, H: h},
		Children: []*Element{title, device, serial, button, counter, input, reset, last}}
	return root
}

func (a *App) lastEventLocked() string {
	switch {
	case a.lastTap != "" && a.lastKey != "":
		return a.lastTap + " - key " + a.lastKey
	case a.lastTap != "":
		return a.lastTap
	case a.lastKey != "":
		return "key " + a.lastKey
	}
	return "Waiting for input"
}

// Find returns the element with the id in the current layout.
func (a *App) Find(id string) *Element {
	return findIn(a.Layout(), id)
}

func findIn(e *Element, id string) *Element {
	if e.ID == id {
		return e
	}
	for _, c := range e.Children {
		if f := findIn(c, id); f != nil {
			return f
		}
	}
	return nil
}

// Elements lists the elements of the layout depth first, the root included.
func Elements(root *Element) []*Element {
	var out []*Element
	var walk func(*Element)
	walk = func(e *Element) {
		out = append(out, e)
		for _, c := range e.Children {
			walk(c)
		}
	}
	walk(root)
	return out
}

// Touches returns the touches of the last seconds.
func (a *App) Touches() []Touch {
	a.mu.Lock()
	defer a.mu.Unlock()
	cut := time.Now().Add(-2 * time.Second)
	keep := a.touches[:0]
	for _, t := range a.touches {
		if t.At.After(cut) {
			keep = append(keep, t)
		}
	}
	a.touches = keep
	return append([]Touch(nil), keep...)
}

// TouchDown shows a touch point (a finger down or moving).
func (a *App) TouchDown(x, y int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.touches = append(a.touches, Touch{X: x, Y: y, At: time.Now()})
	if len(a.touches) > 64 {
		a.touches = a.touches[len(a.touches)-64:]
	}
	a.changed()
}

// Tap at device coordinates: activates the element under the point.
func (a *App) Tap(source string, x, y int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.touches = append(a.touches, Touch{X: x, Y: y, At: time.Now(), Up: true})
	hit := ""
	for _, e := range Elements(a.layoutLocked()) {
		if e.Kind != KindContainer && e.Rect.Contains(x, y) {
			hit = e.ID
		}
	}
	a.activateLocked(hit)
	a.lastTap = fmt.Sprintf("tap %d,%d", x, y)
	if hit != "" {
		a.lastTap += " " + hit
	}
	a.logLocked(source, "tap", fmt.Sprintf("%d,%d %s", x, y, hit))
	a.changed()
	return hit
}

// Swipe from one point to another.
func (a *App) Swipe(source string, x1, y1, x2, y2 int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.touches = append(a.touches, Touch{X: x1, Y: y1, At: now}, Touch{X: x2, Y: y2, At: now, Up: true})
	a.lastTap = fmt.Sprintf("swipe %d,%d->%d,%d", x1, y1, x2, y2)
	a.logLocked(source, "swipe", fmt.Sprintf("%d,%d %d,%d", x1, y1, x2, y2))
	a.changed()
}

// Click activates an element by id (Appium click).
func (a *App) Click(source, id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := findIn(a.layoutLocked(), id)
	if e == nil {
		return false
	}
	cx, cy := e.Rect.X+e.Rect.W/2, e.Rect.Y+e.Rect.H/2
	a.touches = append(a.touches, Touch{X: cx, Y: cy, At: time.Now(), Up: true})
	a.activateLocked(id)
	a.lastTap = "click " + id
	a.logLocked(source, "click", id)
	a.changed()
	return true
}

func (a *App) activateLocked(id string) {
	switch id {
	case "tap_button":
		a.taps++
		a.focused = false
	case "reset_button":
		a.taps = 0
		a.input = ""
		a.focused = false
	case "input":
		a.focused = true
	default:
		a.focused = false
	}
}

// Type text into the input field.
func (a *App) Type(source, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.input += text
	a.focused = true
	a.logLocked(source, "type", text)
	a.changed()
}

// SetText replaces the text of the input field.
func (a *App) SetText(source, id, text string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id != "input" {
		return false
	}
	a.input = text
	a.logLocked(source, "set_text", text)
	a.changed()
	return true
}

// Key handles a key press (a key name such as HOME, BACK, DEL, ENTER, or a character).
func (a *App) Key(source, key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastKey = key
	switch strings.ToUpper(key) {
	case "DEL", "BACKSPACE", "DELETE", "\b":
		if r := []rune(a.input); len(r) > 0 {
			a.input = string(r[:len(r)-1])
		}
	case "HOME":
		a.focused = false
		a.appState = "com.zebrunner.dummy"
	default:
		if len([]rune(key)) == 1 && a.focused {
			a.input += key
		}
	}
	a.logLocked(source, "key", key)
	a.changed()
}

// Taps is the counter of the button.
func (a *App) Taps() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.taps
}

// Input is the text of the input field.
func (a *App) Input() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.input
}

// Tick marks the screen changed (the clock); called by the screen once per second while it is watched.
func (a *App) Tick() {
	a.mu.Lock()
	a.version++
	a.mu.Unlock()
}
