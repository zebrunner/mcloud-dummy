// Package android emulates an Android device behind a fake adbd: enough of it for the mcloud-android-connector,
// the STF provider (minicap, minitouch, STFService, minirev) and Appium with UiAutomator2.
package android

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/adb"
	"github.com/zebrunner/mcloud-dummy/internal/config"
	"github.com/zebrunner/mcloud-dummy/internal/screen"
	"github.com/zebrunner/mcloud-dummy/internal/ui"
)

// Features of the CNXN banner. Without abb_exec and sendrecv_v2 the host falls back to "exec:cmd package install"
// and to the sync v1 transfers.
const Features = "shell_v2,cmd,stat_v2,ls_v2,fixed_push_mkdir,fixed_push_symlink_timestamp"

// SocketHandler serves a connection to a local socket of the device (localabstract:<name> or tcp:<port>).
type SocketHandler func(s *adb.Stream)

// Faults are the injected failures of a device.
type Faults struct {
	Offline   bool          `json:"offline"`   // the adb port is closed (device unplugged)
	Auth      string        `json:"auth"`      // none, accept or deny
	Latency   time.Duration `json:"latency"`   // added before every opened service
	BootStuck bool          `json:"bootStuck"` // sys.boot_completed never becomes 1
	NoScreen  bool          `json:"noScreen"`  // minicap does not answer
}

// Device is one dummy Android device.
type Device struct {
	Cfg    config.Device
	Log    *slog.Logger
	App    *ui.App
	Screen *screen.Renderer

	server *adb.Server
	fs     *VFS
	pkgs   *Packages
	procs  *Procs
	logs   *logBuffer

	mu        sync.Mutex
	props     map[string]string
	settings  map[string]map[string]string
	sockets   map[string]*socketRegistration
	bootAt    time.Time
	faults    Faults
	rebooting bool
	clipboard string
	ime       string
	started   time.Time
	opened    map[string]int
	commands  []CommandRecord
	done      chan struct{}
}

// New creates a device; Start opens its adb port.
func New(cfg config.Device, log *slog.Logger) *Device {
	app := ui.New(ui.Info{Platform: "android", Name: cfg.Name, Model: cfg.Model, OSVersion: cfg.OSVersion,
		Serial: cfg.Serial, Width: cfg.Width, Height: cfg.Height, Locale: cfg.Locale, TimeZone: cfg.TimeZone})
	ui.ApplyConfig(app, cfg)
	d := &Device{
		Cfg:      cfg,
		Log:      log.With("device", cfg.Serial),
		App:      app,
		Screen:   screen.New(app),
		fs:       newVFS(),
		pkgs:     newPackages(),
		procs:    newProcs(),
		logs:     newLogBuffer(),
		settings: map[string]map[string]string{"global": {}, "secure": {}, "system": {}},
		sockets:  map[string]*socketRegistration{},
		faults:   Faults{Auth: cfg.Auth},
		ime:      "com.android.inputmethod.latin/.LatinIME",
		opened:   map[string]int{},
		done:     make(chan struct{}),
	}
	d.props = d.defaultProps()
	d.settings["global"]["adb_enabled"] = "1"
	d.settings["global"]["airplane_mode_on"] = "0"
	d.settings["secure"]["default_input_method"] = d.ime
	d.settings["secure"]["enabled_input_methods"] = d.ime
	d.settings["system"]["screen_off_timeout"] = "600000"
	d.settings["system"]["accelerometer_rotation"] = "0"
	d.server = &adb.Server{Addr: fmt.Sprintf(":%d", cfg.Port), Dev: d, Log: d.Log}
	d.watchState()
	return d
}

func (d *Device) defaultProps() map[string]string {
	c := d.Cfg
	chars := "default"
	switch c.Type {
	case "tablet":
		chars = "tablet"
	case "tv":
		chars = "tv"
	}
	abilist := c.ABI
	switch c.ABI {
	case "x86_64":
		abilist = "x86_64,x86"
	case "arm64-v8a":
		abilist = "arm64-v8a,armeabi-v7a,armeabi"
	}
	fp := fmt.Sprintf("%s/%s/%s:%s/MCLOUD.%d/1:user/release-keys", c.Brand, c.Device, c.Device, c.OSVersion, c.SDK)
	return map[string]string{
		"ro.serialno":                  c.Serial,
		"ro.boot.serialno":             c.Serial,
		"ro.product.model":             c.Model,
		"ro.product.brand":             c.Brand,
		"ro.product.name":              c.Device,
		"ro.product.device":            c.Device,
		"ro.product.manufacturer":      c.Manufacturer,
		"ro.product.board":             c.Device,
		"ro.product.cpu.abi":           c.ABI,
		"ro.product.cpu.abilist":       abilist,
		"ro.product.cpu.abilist64":     abilist,
		"ro.product.locale":            c.Locale,
		"ro.build.version.release":     c.OSVersion,
		"ro.build.version.sdk":         strconv.Itoa(c.SDK),
		"ro.build.version.preview_sdk": "0",
		"ro.build.version.codename":    "REL",
		"ro.build.id":                  fmt.Sprintf("MCLOUD.%d", c.SDK),
		"ro.build.display.id":          fmt.Sprintf("MCLOUD.%d dummy", c.SDK),
		"ro.build.type":                "user",
		"ro.build.tags":                "release-keys",
		"ro.build.fingerprint":         fp,
		"ro.build.characteristics":     chars,
		"ro.board.platform":            "dummy",
		"ro.hardware":                  "dummy",
		"ro.kernel.qemu":               "0",
		"ro.debuggable":                "0",
		"ro.secure":                    "1",
		"ro.adb.secure":                "1",
		"ro.opengles.version":          "196610",
		"ro.sf.lcd_density":            strconv.Itoa(c.Density),
		"ro.zygote":                    "zygote64_32",
		"persist.sys.locale":           c.Locale,
		"persist.sys.timezone":         c.TimeZone,
		"persist.sys.usb.config":       "adb",
		"gsm.operator.alpha":           "MCloud",
		"gsm.sim.operator.alpha":       "MCloud",
		"gsm.sim.state":                "READY",
		"net.hostname":                 c.Device,
		"service.adb.tcp.port":         strconv.Itoa(c.Port),
		"init.svc.adbd":                "running",
		"init.svc.bootanim":            "stopped",
		"dev.bootcomplete":             "1",
		"debug.stf.product.device":     c.Name,
		"wifi.interface":               "wlan0",
	}
}

// Start opens the adb port and starts the boot.
func (d *Device) Start() error {
	d.mu.Lock()
	d.bootAt = time.Now().Add(d.Cfg.BootTime)
	d.started = time.Now()
	d.mu.Unlock()
	go d.produceLogs()
	go d.App.DrainBattery(d.done, d.Cfg.BatteryRate)
	return d.server.Start()
}

// Stop closes the device.
func (d *Device) Stop() {
	select {
	case <-d.done:
	default:
		close(d.done)
	}
	d.server.Stop()
	d.procs.KillAll()
}

// Banner of CNXN.
func (d *Device) Banner() string {
	c := d.Cfg
	return fmt.Sprintf("device::ro.product.name=%s;ro.product.model=%s;ro.product.device=%s;features=%s",
		c.Device, c.Model, c.Device, Features)
}

// Auth mode of the adb authentication.
func (d *Device) Auth() adb.AuthMode {
	d.mu.Lock()
	defer d.mu.Unlock()
	return adb.AuthMode(d.faults.Auth)
}

// Delay before opening a service.
func (d *Device) Delay() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.faults.Latency
}

// Booted tells whether sys.boot_completed is 1.
func (d *Device) Booted() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.faults.BootStuck && !d.rebooting && !time.Now().Before(d.bootAt)
}

// Prop returns a system property.
func (d *Device) Prop(name string) string {
	if name == "gsm.network.type" {
		return networkType(d.App.Network())
	}
	if name == "sys.boot_completed" {
		if d.Booted() {
			return "1"
		}
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.props[name]
}

// Props returns all the properties.
func (d *Device) Props() map[string]string {
	d.mu.Lock()
	out := make(map[string]string, len(d.props)+1)
	for k, v := range d.props {
		out[k] = v
	}
	d.mu.Unlock()
	if d.Booted() {
		out["sys.boot_completed"] = "1"
	}
	out["gsm.network.type"] = networkType(d.App.Network())
	return out
}

func networkType(network string) string {
	if network == "cellular" {
		return "LTE"
	}
	return "Unknown"
}

// SetProp sets a system property.
func (d *Device) SetProp(name, value string) {
	d.mu.Lock()
	d.props[name] = value
	d.mu.Unlock()
}

// socketRegistration is a handler of a local socket; its address tells the registrations of a name apart.
type socketRegistration struct{ h SocketHandler }

// RegisterSocket listens on a local socket ("localabstract:minicap", "tcp:6790"), it replaces the earlier handler of
// the name; the returned func unregisters it, unless a later handler has replaced it (a new minicap is started before
// the old one exits).
func (d *Device) RegisterSocket(name string, h SocketHandler) func() {
	reg := &socketRegistration{h: h}
	d.mu.Lock()
	d.sockets[name] = reg
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		if d.sockets[name] == reg {
			delete(d.sockets, name)
		}
		d.mu.Unlock()
	}
}

func (d *Device) socket(name string) (SocketHandler, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	reg, ok := d.sockets[name]
	if !ok {
		return nil, false
	}
	return reg.h, true
}

// Open resolves a service of the adb protocol.
func (d *Device) Open(service string) (adb.Handler, bool) {
	d.countService(service)
	if req, ok := adb.ParseShellService(service); ok {
		return func(s *adb.Stream) {
			adb.ServeShell(s, req, d.runShell)
		}, true
	}
	name, arg, _ := strings.Cut(service, ":")
	switch name {
	case "sync":
		return func(s *adb.Stream) { adb.ServeSync(s, d.fs) }, true
	case "localabstract", "localreserved", "localfilesystem", "dev":
		if name == "localabstract" && arg == "minicap" && d.faultNoScreen() {
			return nil, false
		}
		h, ok := d.socket("localabstract:" + arg)
		if !ok {
			return nil, false
		}
		return adb.Handler(h), true
	case "tcp":
		port, _, _ := strings.Cut(arg, ":")
		h, ok := d.socket("tcp:" + port)
		if !ok {
			return nil, false
		}
		return adb.Handler(h), true
	case "reboot":
		return func(s *adb.Stream) {
			d.App.Log("adb", "reboot", "%s", arg)
			go d.Reboot()
		}, true
	case "root":
		return message("adbd cannot run as root in production builds\n"), true
	case "unroot":
		return message("adbd not running as root\n"), true
	case "remount":
		return message("remount is not supported on a dummy device\n"), true
	case "tcpip":
		return message(fmt.Sprintf("restarting in TCP mode port: %s\n", arg)), true
	case "usb":
		return message("restarting in USB mode\n"), true
	case "enable-verity", "disable-verity":
		return message("verity is not supported on a dummy device\n"), true
	case "reverse":
		return d.reverse(arg), true
	case "jdwp", "track-jdwp", "track-app":
		return func(s *adb.Stream) {
			if name != "jdwp" {
				_, _ = s.Write([]byte("0000"))
			}
			<-s.Context().Done()
		}, true
	}
	d.App.Log("adb", "refused", "%s", service)
	return nil, false
}

func message(text string) adb.Handler {
	return func(s *adb.Stream) { _, _ = s.Write([]byte(text)) }
}

func (d *Device) reverse(arg string) adb.Handler {
	return func(s *adb.Stream) {
		switch {
		case arg == "list-forward":
			_, _ = s.Write([]byte("OKAY0000"))
		case arg == "killforward-all", strings.HasPrefix(arg, "killforward:"):
			_, _ = s.Write([]byte("OKAY"))
		default:
			msg := "reverse forwarding is not supported on a dummy device"
			_, _ = fmt.Fprintf(s, "FAIL%04x%s", len(msg), msg)
		}
	}
}

func (d *Device) countService(service string) {
	key, _, _ := strings.Cut(service, ":")
	if req, ok := adb.ParseShellService(service); ok {
		f := strings.Fields(req.Command)
		key = "shell"
		if len(f) > 0 {
			key = "shell " + f[0]
		}
	}
	d.mu.Lock()
	d.opened[key]++
	d.mu.Unlock()
}

func (d *Device) faultNoScreen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.faults.NoScreen
}

// Reboot drops the connections, kills the processes and boots again.
func (d *Device) Reboot() {
	d.mu.Lock()
	if d.rebooting {
		d.mu.Unlock()
		return
	}
	d.rebooting = true
	d.mu.Unlock()
	d.Log.Info("rebooting")
	d.server.Stop()
	d.procs.KillAll()
	time.Sleep(d.Cfg.RebootTime)
	d.mu.Lock()
	d.rebooting = false
	d.bootAt = time.Now().Add(d.Cfg.BootTime)
	offline := d.faults.Offline
	d.mu.Unlock()
	if !offline {
		if err := d.server.Start(); err != nil {
			d.Log.Error("adb port can't be opened after the reboot", "err", err)
		}
	}
	d.App.Log("device", "booted", "after reboot")
}

// SetFaults applies the injected failures.
func (d *Device) SetFaults(f Faults) {
	if f.Auth == "" {
		f.Auth = d.Cfg.Auth
	}
	d.mu.Lock()
	old := d.faults
	d.faults = f
	rebooting := d.rebooting
	d.mu.Unlock()
	d.App.Log("control", "faults", "%+v", f)
	if f.Offline && !old.Offline {
		d.server.Stop()
	}
	if !f.Offline && old.Offline && !rebooting {
		if err := d.server.Start(); err != nil {
			d.Log.Error("adb port can't be opened", "err", err)
		}
	}
	if f.Auth != old.Auth {
		d.server.Disconnect()
	}
}

// Faults returns the injected failures.
func (d *Device) Faults() Faults {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.faults
}

// Disconnect drops the adb connections: the host sees the device offline until it reconnects.
func (d *Device) Disconnect() {
	d.App.Log("control", "disconnect", "")
	d.server.Disconnect()
}

// Crash kills the processes with the name (minicap, minitouch, stf.agent, io.appium.uiautomator2.server...).
func (d *Device) Crash(name string) int {
	n := d.procs.KillByName(name, 9)
	d.App.Log("control", "crash", "%s: %d processes", name, n)
	return n
}

// Status is the state of the device for the control API.
type Status struct {
	Platform    string         `json:"platform"`
	Serial      string         `json:"serial"`
	Name        string         `json:"name"`
	Model       string         `json:"model"`
	OSVersion   string         `json:"osVersion"`
	Address     string         `json:"address"`
	Booted      bool           `json:"booted"`
	Connections int            `json:"connections"`
	Faults      Faults         `json:"faults"`
	Processes   []string       `json:"processes"`
	Sockets     []string       `json:"sockets"`
	Packages    []string       `json:"packages"`
	Services    map[string]int `json:"services"`
	Taps        int            `json:"taps"`
	Input       string         `json:"input"`
	Frames      uint64         `json:"frames"`
	Power       ui.Power       `json:"battery"`
	Network     string         `json:"network"`
	Rotation    int            `json:"rotation"`
	Locale      string         `json:"locale"`
	TimeZone    string         `json:"timeZone"`
	Screen      string         `json:"screen"`
}

// Status of the device.
func (d *Device) Status(host string) Status {
	st := Status{Platform: "android", Serial: d.Cfg.Serial, Name: d.Cfg.Name, Model: d.Cfg.Model,
		OSVersion: d.Cfg.OSVersion, Address: net.JoinHostPort(host, strconv.Itoa(d.Cfg.Port)), Booted: d.Booted(),
		Connections: d.server.Connections(), Faults: d.Faults(), Taps: d.App.Taps(), Input: d.App.Input(),
		Frames: d.Screen.Frames(), Services: map[string]int{}, Power: d.App.Power(), Network: d.App.Network(),
		Rotation: d.App.Rotation(), Locale: d.Cfg.Locale, TimeZone: d.Cfg.TimeZone,
		Screen: fmt.Sprintf("%dx%d@%d", d.Cfg.Width, d.Cfg.Height, d.Cfg.Density)}
	for _, p := range d.procs.List() {
		st.Processes = append(st.Processes, fmt.Sprintf("%d %s", p.PID, p.Name))
	}
	d.mu.Lock()
	for name := range d.sockets {
		st.Sockets = append(st.Sockets, name)
	}
	for k, v := range d.opened {
		st.Services[k] = v
	}
	d.mu.Unlock()
	sort.Strings(st.Sockets)
	for _, p := range d.pkgs.List() {
		if !p.System {
			st.Packages = append(st.Packages, fmt.Sprintf("%s %s", p.Name, p.VersionName))
		}
	}
	return st
}

// httpOnSocket serves an http.Handler on a local socket of the device.
func (d *Device) httpOnSocket(ctx context.Context, name string, h http.Handler) func() {
	ln := newStreamListener()
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	unregister := d.RegisterSocket(name, func(s *adb.Stream) {
		if !ln.push(s) {
			return
		}
		<-s.Context().Done()
	})
	var once sync.Once
	// the end of the process and the caller both stop it
	stop := func() {
		once.Do(func() {
			unregister()
			_ = srv.Close()
		})
	}
	go func() {
		<-ctx.Done()
		stop()
	}()
	return stop
}

// streamListener hands the adb streams of a local socket to an http.Server.
type streamListener struct {
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newStreamListener() *streamListener {
	return &streamListener{ch: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *streamListener) push(c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-l.closed:
		return false
	}
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *streamListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *streamListener) Addr() net.Addr { return streamAddr("device") }

type streamAddr string

func (a streamAddr) Network() string { return "adb" }
func (a streamAddr) String() string  { return string(a) }
