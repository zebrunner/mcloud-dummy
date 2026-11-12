package android

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/adb"
	"github.com/zebrunner/mcloud-dummy/internal/ui"
)

// projection is the -P argument of minicap: <real w>x<real h>@<virtual w>x<virtual h>/<rotation>.
type projection struct {
	rw, rh, vw, vh, rot int
}

func parseProjection(s string, defW, defH int) projection {
	p := projection{rw: defW, rh: defH, vw: defW, vh: defH}
	real, rest, _ := strings.Cut(s, "@")
	fmt.Sscanf(real, "%dx%d", &p.rw, &p.rh)
	virt, rot, _ := strings.Cut(rest, "/")
	if virt != "" {
		fmt.Sscanf(virt, "%dx%d", &p.vw, &p.vh)
	} else {
		p.vw, p.vh = p.rw, p.rh
	}
	p.rot, _ = strconv.Atoi(rot)
	return p
}

// fit adjusts the virtual size as minicap does: the STF UI asks for the size of its box, minicap streams the largest
// size of the aspect ratio of the real display within it. The native binary first caps it at the real size
// (Projection.hpp forceMaximumSize and forceAspectRatio), the apk only keeps the aspect ratio (Main.kt, in float).
func (p projection) fit(apk bool) projection {
	if p.rw <= 0 || p.rh <= 0 || p.vw <= 0 || p.vh <= 0 {
		return p
	}
	if apk {
		aspect := float32(p.rw) / float32(p.rh)
		if float32(p.vh) > float32(p.vw)/aspect {
			p.vh = int(math.Round(float64(float32(p.vw) / aspect)))
		} else {
			p.vw = int(math.Round(float64(float32(p.vh) * aspect)))
		}
	} else {
		p.vw, p.vh = min(p.vw, p.rw), min(p.vh, p.rh)
		aspect := float64(p.rw) / float64(p.rh)
		if p.vh > int(float64(p.vw)/aspect) {
			p.vh = int(math.Round(float64(p.vw) / aspect))
		} else {
			p.vw = int(math.Round(float64(p.vh) * aspect))
		}
	}
	p.vw, p.vh = max(p.vw, 1), max(p.vh, 1)
	return p
}

// frameSize is the size of the JPEG frames: the virtual size turned by the rotation.
func (p projection) frameSize() (int, int) {
	if p.rot%180 != 0 {
		return p.vh, p.vw
	}
	return p.vw, p.vh
}

// cmdMinicap emulates the native minicap binary: -i prints the display info, -s writes a JPEG, -S serves
// localabstract:minicap.
func cmdMinicap(c *Cmd) int {
	return runMinicap(c, c.Args[1:], false)
}

// runMinicap runs minicap, the native binary or the apk (io.devicefarmer.minicap.Main of app_process).
func runMinicap(c *Cmd, args []string, apk bool) int {
	d := c.d
	var proj string
	quality, fps := 80, d.Cfg.FPS
	server, shot, info := false, false, false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-P":
			if i+1 < len(args) {
				i++
				proj = args[i]
			}
		case "-Q":
			if i+1 < len(args) {
				i++
				quality, _ = strconv.Atoi(args[i])
			}
		case "-r":
			if i+1 < len(args) {
				i++
				if v, err := strconv.ParseFloat(args[i], 64); err == nil && v > 0 {
					fps = int(math.Ceil(v))
				}
			}
		case "-S":
			server = true
		case "-s":
			shot = true
		case "-i":
			info = true
		case "-h":
			c.printf("Usage: minicap [-h] [-n <name>] [-P <value>] [-Q <value>] [-r <value>] [-s] [-S] [-i] [-t]\n")
			return 0
		}
	}
	p := parseProjection(proj, d.Cfg.Width, d.Cfg.Height).fit(apk)
	if info {
		out, _ := json.Marshal(map[string]any{"id": 0, "width": d.Cfg.Width, "height": d.Cfg.Height,
			"xdpi": float64(d.Cfg.Density), "ydpi": float64(d.Cfg.Density), "size": 5.5,
			"density": float64(d.Cfg.Density) / 160, "fps": 60.0, "secure": false, "rotation": d.App.Rotation()})
		c.printf("%s\n", out)
		return 0
	}
	w, h := p.frameSize()
	if shot {
		_, _ = c.Stdout.Write(d.Screen.JPEG(w, h, quality))
		return 0
	}
	if !server {
		c.errorf("ERROR: nothing to do, use -S or -s\n")
		return 1
	}
	proc, done := d.procs.Start(c.ctx, "shell", "minicap", strings.Join(c.Args, " "))
	defer done()
	c.printf("PID: %d\n", proc.PID)
	if apk {
		c.printf("INFO: %dx%d@%dx%d/%d\n", p.rw, p.rh, p.vw, p.vh, p.rot)
	} else {
		c.printf("INFO: Using projection %dx%d@%dx%d/%d\n", p.rw, p.rh, p.vw, p.vh, p.rot)
		c.printf("INFO: (jni/minicap/minicap_dummy.cpp:1) Creating dummy display\n")
		c.printf("INFO: (jni/minicap/JpgEncoder.cpp:64) Allocating %d bytes for JPG encoder\n", w*h*4)
	}
	unregister := d.RegisterSocket("localabstract:minicap", func(s *adb.Stream) {
		serveMinicap(d, s, proc, minicapBanner(d, proc.PID, p, apk), w, h, quality, fps)
	})
	defer unregister()
	d.App.Log("minicap", "start", "%dx%d@%dx%d/%d", p.rw, p.rh, p.vw, p.vh, p.rot)
	return waitDone(c, proc)
}

// minicapBanner is the 24 bytes minicap sends first: the native binary has the real size and the fitted virtual size
// of the natural orientation (minicap.cpp), the apk the current size of the display and the frame size, both turned
// by the rotation (BaseProvider.kt getScreenSize, getTargetSize).
func minicapBanner(d *Device, pid int, p projection, apk bool) []byte {
	rw, rh, vw, vh := p.rw, p.rh, p.vw, p.vh
	if apk {
		rw, rh = d.App.Size()
		vw, vh = p.frameSize()
	}
	banner := make([]byte, 24)
	banner[0] = 1
	banner[1] = 24
	binary.LittleEndian.PutUint32(banner[2:], uint32(pid))
	binary.LittleEndian.PutUint32(banner[6:], uint32(rw))
	binary.LittleEndian.PutUint32(banner[10:], uint32(rh))
	binary.LittleEndian.PutUint32(banner[14:], uint32(vw))
	binary.LittleEndian.PutUint32(banner[18:], uint32(vh))
	banner[22] = byte(p.rot / 90)
	// QUIRK_ALWAYS_UPRIGHT: minicap of a virtual display (the apk, the native one of the recent Android versions)
	// streams the frames turned upright by the rotation; without it the STF UI turns the landscape frames once more
	banner[23] = 2
	return banner
}

func serveMinicap(d *Device, s *adb.Stream, proc *Proc, banner []byte, w, h, quality, fps int) {
	if _, err := s.Write(banner); err != nil {
		return
	}
	go func() { _, _ = io.Copy(io.Discard, s) }()
	if fps <= 0 {
		fps = 10
	}
	interval := time.Second / time.Duration(fps)
	tick := time.NewTicker(interval)
	defer tick.Stop()
	var lastVersion uint64 = math.MaxUint64
	var lastSecond int64
	for {
		version, second := d.App.Version(), time.Now().Unix()
		if version != lastVersion || second != lastSecond {
			jpg := d.Screen.JPEG(w, h, quality)
			frame := make([]byte, 4+len(jpg))
			binary.LittleEndian.PutUint32(frame, uint32(len(jpg)))
			copy(frame[4:], jpg)
			if _, err := s.Write(frame); err != nil {
				return
			}
			lastVersion, lastSecond = version, second
		}
		select {
		case <-tick.C:
		case <-s.Context().Done():
			return
		case <-proc.Done():
			return
		}
	}
}

// cmdMinitouch emulates minitouch: it serves localabstract:minitouch.
func cmdMinitouch(c *Cmd) int {
	d := c.d
	proc, done := d.procs.Start(c.ctx, "shell", "minitouch", strings.Join(c.Args, " "))
	defer done()
	c.errorf("Type B touch device dummy-touchscreen (%dx%d with 10 contacts) detected on /dev/input/event1 (score 22)\n",
		d.Cfg.Width, d.Cfg.Height)
	unregister := d.RegisterSocket("localabstract:minitouch", func(s *adb.Stream) {
		serveMinitouch(d, s, proc)
	})
	defer unregister()
	return waitDone(c, proc)
}

type contact struct {
	x, y, sx, sy int
	down, moved  bool
	pendingDown  bool
	pendingUp    bool
}

// naturalToDisplay maps a touch of the natural orientation (0 to the max of the minitouch banner, the size - 1) to
// the pixels of the rotated display, clamped to the panel as the touch screen does.
func naturalToDisplay(d *Device, x, y int) (int, int) {
	maxX, maxY := d.Cfg.Width-1, d.Cfg.Height-1
	x, y = max(0, min(x, maxX)), max(0, min(y, maxY))
	switch d.App.Rotation() {
	case 90:
		return y, maxX - x
	case 180:
		return maxX - x, maxY - y
	case 270:
		return maxY - y, x
	}
	return x, y
}

func serveMinitouch(d *Device, s *adb.Stream, proc *Proc) {
	if _, err := fmt.Fprintf(s, "v 1\n^ 10 %d %d 255\n$ %d\n", d.Cfg.Width-1, d.Cfg.Height-1, proc.PID); err != nil {
		return
	}
	go func() {
		<-proc.Done()
		s.Close()
	}()
	contacts := map[int]*contact{}
	sc := bufio.NewScanner(s)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		num := func(i int) int {
			if i < len(f) {
				v, _ := strconv.Atoi(f[i])
				return v
			}
			return 0
		}
		switch f[0] {
		case "d":
			id := num(1)
			x, y := naturalToDisplay(d, num(2), num(3))
			contacts[id] = &contact{x: x, y: y, sx: x, sy: y, down: true, pendingDown: true}
		case "m":
			if ct, ok := contacts[num(1)]; ok {
				ct.x, ct.y = naturalToDisplay(d, num(2), num(3))
				if abs(ct.x-ct.sx) > 10 || abs(ct.y-ct.sy) > 10 {
					ct.moved = true
				}
				ct.pendingDown = true
			}
		case "u":
			if ct, ok := contacts[num(1)]; ok {
				ct.pendingUp = true
			}
		case "c":
			for id, ct := range contacts {
				if ct.pendingDown && !ct.pendingUp {
					d.App.TouchDown(ct.x, ct.y)
					ct.pendingDown = false
				}
				if ct.pendingUp {
					if ct.moved {
						d.App.Swipe("minitouch", ct.sx, ct.sy, ct.x, ct.y)
					} else {
						d.App.Tap("minitouch", ct.sx, ct.sy)
					}
					delete(contacts, id)
				}
			}
		case "r":
			contacts = map[int]*contact{}
		case "w":
			time.Sleep(time.Duration(num(1)) * time.Millisecond)
		}
	}
}

// cmdMinirev emulates minirev: it accepts the reverse forwarding connections of STF and drops their data.
func cmdMinirev(c *Cmd) int {
	d := c.d
	proc, done := d.procs.Start(c.ctx, "shell", "minirev", strings.Join(c.Args, " "))
	defer done()
	unregister := d.RegisterSocket("localabstract:minirev", func(s *adb.Stream) {
		go func() {
			<-proc.Done()
			s.Close()
		}()
		_, _ = io.Copy(io.Discard, s)
	})
	defer unregister()
	return waitDone(c, proc)
}

const (
	stfPackage     = "jp.co.cyberagent.stf"
	stfAgentClass  = "jp.co.cyberagent.stf.Agent"
	stfAgentProc   = "stf.agent"
	stfServiceProc = "jp.co.cyberagent.stf"
	minicapClass   = "io.devicefarmer.minicap.Main"
)

// cmdAppProcess runs the Java mains STF starts with app_process: the STFService agent and minicap of the apk.
func cmdAppProcess(c *Cmd) int {
	d := c.d
	args := c.Args[1:]
	var class string
	var rest []string
	for i, a := range args {
		if strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") {
			continue
		}
		class, rest = a, args[i+1:]
		break
	}
	classpath := c.Getenv("CLASSPATH")
	switch class {
	case minicapClass:
		if !d.fs.Exists(classpath) {
			c.errorf("Error: Could not find class '%s'\n", class)
			return 1
		}
		return runMinicap(c, rest, true)
	case stfAgentClass:
		pkg := d.pkgs.ByPath(classpath)
		if pkg == nil || pkg.Name != stfPackage {
			c.errorf("Error: Could not find class '%s'\n", class)
			return 1
		}
		if len(rest) > 0 && rest[0] == "--version" {
			c.printf("%s", pkg.VersionName)
			return 0
		}
		if len(rest) > 0 && rest[0] == "--telephony" {
			c.printf("imei=35%013d\nimsi=00101%010d\niccid=8900100%012d\nphoneNumber=+1555%07d\n",
				serialNumber(d.Cfg.Serial), serialNumber(d.Cfg.Serial), serialNumber(d.Cfg.Serial), serialNumber(d.Cfg.Serial)%10000000)
			return 0
		}
		return d.runSTFAgent(c)
	}
	c.errorf("Error: Could not find class '%s'\n", class)
	return 1
}

func serialNumber(s string) int64 {
	var n int64 = 7
	for _, ch := range s {
		n = (n*31 + int64(ch)) % 1_000_000_000_000
	}
	return n
}

// STFService wire: message types of jp.co.cyberagent.stf.proto.MessageType.
const (
	msgDoIdentify          = 1
	msgDoKeyEvent          = 2
	msgDoType              = 3
	msgDoWake              = 4
	msgGetBrowsers         = 5
	msgGetClipboard        = 6
	msgGetProperties       = 7
	msgGetVersion          = 8
	msgSetClipboard        = 9
	msgSetKeyguardState    = 10
	msgSetWakeLock         = 11
	msgSetRotation         = 12
	msgEventAirplaneMode   = 13
	msgEventBattery        = 14
	msgEventConnectivity   = 15
	msgEventPhoneState     = 16
	msgEventRotation       = 17
	msgEventBrowserPackage = 18
	msgGetDisplay          = 19
	msgDoRemoveAccount     = 20
	msgSetRingerMode       = 21
	msgSetWifiEnabled      = 22
	msgGetWifiStatus       = 23
	msgDoAddAccountMenu    = 24
	msgGetSdStatus         = 25
	msgGetAccounts         = 26
	msgGetRingerMode       = 27
	msgSetMasterMute       = 28
	msgGetBluetoothStatus  = 29
	msgSetBluetoothEnabled = 30
	msgGetRootStatus       = 31
	msgDoCleanBluetooth    = 32
)

// stfEvents fans the STFService events out to the connected service sockets.
type stfEvents struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

var stfEventHub sync.Map // *Device -> *stfEvents

func (d *Device) events() *stfEvents {
	v, _ := stfEventHub.LoadOrStore(d, &stfEvents{subs: map[chan []byte]struct{}{}})
	return v.(*stfEvents)
}

func (e *stfEvents) publish(msg []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (e *stfEvents) subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 32)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.subs, ch)
		e.mu.Unlock()
	}
}

// watchState publishes the STFService events when the rotation, the battery or the network change.
func (d *Device) watchState() {
	var mu sync.Mutex
	rotation, power, network := d.App.Rotation(), d.App.Power(), d.App.Network()
	d.App.OnChange(func() {
		mu.Lock()
		defer mu.Unlock()
		if r := d.App.Rotation(); r != rotation {
			rotation = r
			d.events().publish(rotationEvent(r))
		}
		if p := d.App.Power(); p != power {
			power = p
			d.events().publish(batteryEvent(p))
		}
		if n := d.App.Network(); n != network {
			network = n
			d.events().publish(connectivityEvent(n))
		}
	})
}

func rotationEvent(rotation int) []byte {
	return encodeEnvelope(0, false, msgEventRotation, pbMsg(pbVarint(1, uint64(rotation))))
}

// batteryEvent is the BatteryEvent of STFService: status charging/discharging/full, source usb or empty.
func batteryEvent(p ui.Power) []byte {
	status, source := "discharging", ""
	switch p.Status() {
	case 2:
		status, source = "charging", "usb"
	case 5:
		status, source = "full", "usb"
	}
	return encodeEnvelope(0, false, msgEventBattery, pbMsg(pbString(1, status), pbString(2, "good"), pbString(3, source),
		pbVarint(4, uint64(p.Level)), pbVarint(5, 100), pbDouble(6, 25.0), pbDouble(7, 4.2)))
}

// connectivityEvent is the ConnectivityEvent of STFService: connected, type WIFI or MOBILE, subtype.
func connectivityEvent(network string) []byte {
	typ, subtype := "WIFI", ""
	switch network {
	case "cellular":
		typ, subtype = "MOBILE", "LTE"
	case "none":
		return encodeEnvelope(0, false, msgEventConnectivity, pbMsg(pbBool(1, false)))
	}
	return encodeEnvelope(0, false, msgEventConnectivity, pbMsg(pbBool(1, true), pbString(2, typ), pbString(3, subtype),
		pbBool(4, false), pbBool(5, false)))
}

// setWifi turns wifi on (wifi) or off (cellular).
func setWifi(app *ui.App, source string, on bool) {
	switch {
	case on:
		app.SetNetwork(source, "wifi")
	case app.Network() == "wifi":
		app.SetNetwork(source, "cellular")
	}
}

// runSTFAgent is the agent of STFService (app_process): it serves localabstract:stfagent.
func (d *Device) runSTFAgent(c *Cmd) int {
	proc, done := d.procs.Start(c.ctx, "shell", stfAgentProc, strings.Join(c.Args, " "))
	defer done()
	unregister := d.RegisterSocket("localabstract:stfagent", func(s *adb.Stream) {
		go func() {
			<-proc.Done()
			s.Close()
		}()
		r := bufio.NewReader(s)
		for {
			msg, err := readDelimited(r)
			if err != nil {
				return
			}
			env := decodeEnvelope(msg)
			d.handleAgent(env)
		}
	})
	defer unregister()
	d.App.Log("stfservice", "agent", "started")
	return waitDone(c, proc)
}

func (d *Device) handleAgent(env envelope) {
	fields := decodeFields(env.message)
	switch env.typ {
	case msgDoKeyEvent:
		event := fields.uint(1)
		code := int(fields.uint(2))
		if event != 1 { // DOWN or PRESS; UP is ignored
			d.pressKey("stf", code)
		}
	case msgDoType:
		d.App.Type("stf", fields.str(1))
	case msgDoWake:
		d.App.SetLocked("stf", false)
	case msgSetRotation:
		rot := int(int32(fields.uint(1)))
		if rot > 0 && rot < 4 {
			rot *= 90
		}
		d.App.SetRotation("stf", rot)
	}
}

// startSTFService starts the STFService service (am start-foreground-service): it serves localabstract:stfservice.
func (d *Device) startSTFService() {
	if len(d.procs.Find(stfServiceProc)) > 0 {
		return
	}
	proc, done := d.procs.Start(backgroundCtx(), "u0_a120", stfServiceProc, stfServiceProc)
	unregister := d.RegisterSocket("localabstract:stfservice", func(s *adb.Stream) {
		d.serveSTFService(s, proc)
	})
	go func() {
		<-proc.Done()
		unregister()
		done()
	}()
	d.App.Log("stfservice", "service", "started")
}

func (d *Device) serveSTFService(s *adb.Stream, proc *Proc) {
	var wmu sync.Mutex
	send := func(msg []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := s.Write(appendDelimited(nil, msg))
		return err
	}
	events, unsubscribe := d.events().subscribe()
	defer unsubscribe()
	go func() {
		for {
			select {
			case msg := <-events:
				if send(msg) != nil {
					return
				}
			case <-proc.Done():
				s.Close()
				return
			case <-s.Context().Done():
				return
			}
		}
	}()
	browser := pbMsg(pbString(1, "Chrome"), pbString(2, "com.android.chrome/com.google.android.apps.chrome.Main"),
		pbBool(3, true), pbBool(4, true))
	initial := [][]byte{
		rotationEvent(d.App.Rotation()),
		batteryEvent(d.App.Power()),
		connectivityEvent(d.App.Network()),
		encodeEnvelope(0, false, msgEventPhoneState, pbMsg(pbString(1, "in_service"), pbBool(2, false), pbString(3, "MCloud"))),
		encodeEnvelope(0, false, msgEventAirplaneMode, pbMsg(pbBool(1, false))),
		encodeEnvelope(0, false, msgEventBrowserPackage, pbMsg(pbBool(1, true), pbBytes(2, browser))),
	}
	for _, m := range initial {
		if send(m) != nil {
			return
		}
	}
	r := bufio.NewReader(s)
	for {
		msg, err := readDelimited(r)
		if err != nil {
			return
		}
		env := decodeEnvelope(msg)
		resp := d.serviceResponse(env, browser)
		if send(encodeEnvelope(env.id, true, env.typ, resp)) != nil {
			return
		}
	}
}

func (d *Device) serviceResponse(env envelope, browser []byte) []byte {
	ok := pbBool(1, true)
	fields := decodeFields(env.message)
	switch env.typ {
	case msgGetVersion:
		version := "2.5.8"
		if pkg := d.pkgs.Get(stfPackage); pkg != nil && pkg.VersionName != "" {
			version = pkg.VersionName
		}
		return pbMsg(ok, pbString(2, version))
	case msgGetDisplay:
		density := float32(d.Cfg.Density)
		return pbMsg(ok, pbVarint(2, uint64(d.Cfg.Width)), pbVarint(3, uint64(d.Cfg.Height)), pbFloat(4, density),
			pbFloat(5, density), pbFloat(6, 60), pbFloat(7, density/160), pbVarint(8, uint64(d.App.Rotation())), pbBool(9, false))
	case msgGetProperties:
		values := map[string]string{
			"imei": fmt.Sprintf("35%013d", serialNumber(d.Cfg.Serial)), "imsi": fmt.Sprintf("00101%010d", serialNumber(d.Cfg.Serial)),
			"phoneNumber": fmt.Sprintf("+1555%07d", serialNumber(d.Cfg.Serial)%10000000), "iccid": fmt.Sprintf("8900100%012d", serialNumber(d.Cfg.Serial)),
			"operator": "MCloud", "network": "LTE",
		}
		parts := [][]byte{ok}
		for _, name := range fields.strs(1) {
			if v, found := values[name]; found {
				parts = append(parts, pbBytes(2, pbMsg(pbString(1, name), pbString(2, v))))
			}
		}
		return pbMsg(parts...)
	case msgGetClipboard:
		d.mu.Lock()
		text := d.clipboard
		d.mu.Unlock()
		return pbMsg(ok, pbVarint(2, 1), pbString(3, text))
	case msgSetClipboard:
		d.mu.Lock()
		d.clipboard = fields.str(2)
		d.mu.Unlock()
		return ok
	case msgGetBrowsers:
		return pbMsg(ok, pbBool(2, true), pbBytes(3, browser))
	case msgGetAccounts:
		return ok
	case msgGetRingerMode:
		return pbMsg(ok, pbVarint(2, 2))
	case msgGetWifiStatus:
		return pbMsg(ok, pbBool(2, d.App.Network() == "wifi"))
	case msgGetBluetoothStatus, msgGetRootStatus:
		return pbMsg(ok, pbBool(2, false))
	case msgGetSdStatus:
		return pbMsg(ok, pbBool(2, true))
	case msgSetKeyguardState:
		if fields.uint(1) == 0 {
			d.App.SetLocked("stf", false)
		}
		return ok
	case msgDoIdentify:
		d.App.Log("stf", "identify", "%s", fields.str(1))
		return ok
	case msgSetWifiEnabled:
		setWifi(d.App, "stf", fields.uint(1) != 0)
		return ok
	case msgSetWakeLock, msgSetRingerMode, msgSetBluetoothEnabled, msgDoCleanBluetooth,
		msgSetMasterMute, msgDoRemoveAccount, msgDoAddAccountMenu:
		return ok
	}
	return pbBool(1, false)
}
