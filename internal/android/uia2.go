package android

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/ui"
	wd "github.com/zebrunner/mcloud-dummy/internal/webdriver"
)

const (
	uia2ServerPackage = "io.appium.uiautomator2.server"
	uia2Port          = 6790
	// uia2Version is the version of the real server the fake follows (tests/contract/uia2-routes-9.10.5.txt): its
	// routes, its responses and its errors, checked by the contract tests of tests/contract.
	uia2Version     = "9.10.5"
	uia2VersionCode = 251
)

// runUIA2 is "am instrument -w ... io.appium.uiautomator2.server.test/...": the fake UiAutomator2 server listens on
// tcp:6790 of the device (Appium forwards its systemPort to it) until the instrumentation is stopped.
func (d *Device) runUIA2(c *Cmd) int {
	proc, done := d.procs.Start(c.ctx, "u0_a130", uia2ServerPackage, strings.Join(c.Args, " "))
	defer done()
	srv := &uia2Server{d: d, proc: proc, settings: uia2DefaultSettings()}
	stop := d.httpOnSocket(proc.Context(), "tcp:"+strconv.Itoa(uia2Port), srv)
	defer stop()
	d.App.Log("uia2", "start", "server on tcp:%d", uia2Port)
	code := waitDone(c, proc)
	d.App.Log("uia2", "stop", "exit %d", code)
	c.printf("INSTRUMENTATION_STATUS: class=io.appium.uiautomator2.server.test.AppiumUiAutomator2Server\nINSTRUMENTATION_STATUS_CODE: 1\nINSTRUMENTATION_RESULT: shortMsg=Process crashed.\nINSTRUMENTATION_CODE: 0\n")
	return 0
}

// uia2DefaultSettings are the settings of a new session of the real server.
func uia2DefaultSettings() map[string]any {
	return map[string]any{
		"includeExtraRenderingInfo": false, "allowInvisibleElements": false, "elementResponseAttributes": "name,text",
		"snapshotMaxDepth": 70, "includeA11yActionsInPageSource": false, "mjpegBilinearFiltering": false,
		"waitForSelectorTimeout": 10000, "serverPort": uia2Port, "ignoreUnimportantViews": false,
		"simpleBoundsCalculation": false, "enableNotificationListener": true, "limitXPathContextScope": true,
		"includeExtrasInPageSource": false, "normalizeTagNames": false, "trackScrollEvents": true,
		"scrollAcknowledgmentTimeout": 200, "enableTopmostWindowFromActivePackage": false, "enableMultiWindows": false,
		"useResourcesForOrientationDetection": false, "currentDisplayId": 0, "shouldUseCompactResponses": true,
		"wakeLockTimeout": 86400000, "shutdownOnPowerDisconnect": true, "mjpegServerPort": 7810,
		"mjpegScalingFactor": 50, "alwaysTraversableViewClasses": "", "disableIdLocatorAutocompletion": false,
		"enforceXPath1": false, "actionAcknowledgmentTimeout": 3000, "mjpegServerScreenshotQuality": 50,
		"keyInjectionDelay": 0, "waitForIdleTimeout": 10000, "mjpegServerFramerate": 10,
	}
}

type uia2Session struct {
	id   string
	caps any
}

// uia2Server is the fake UiAutomator2 server: one session at a time, as the real one.
type uia2Server struct {
	d        *Device
	proc     *Proc
	mu       sync.Mutex
	session  *uia2Session
	settings map[string]any
}

func newSessionID() string {
	b := make([]byte, 16)
	now := time.Now().UnixNano()
	for i := range b {
		b[i] = byte(now >> (i % 8 * 8))
		now = now*6364136223846793005 + 1442695040888963407
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// uia2Handler serves a route; uia2Params are the parts of its path and the JSON body of a POST.
type uia2Handler func(s *uia2Server, w http.ResponseWriter, r *http.Request, p uia2Params)

type uia2Params struct {
	sid, id, id2, name string
	body               map[string]any
}

// uia2Route is a route of the real server: the method and the path with :sessionId, :id, :id2, :name, :windowHandle.
type uia2Route struct {
	method  string
	path    string
	pattern *regexp.Regexp
	handle  uia2Handler
}

var (
	uia2Routes  []uia2Route
	routeParams = regexp.MustCompile(`:(sessionId|id2|id|name|windowHandle)`)
)

func route(method, path string, h uia2Handler) {
	expr := "^" + routeParams.ReplaceAllStringFunc(regexp.QuoteMeta(path), func(m string) string {
		return "(?P<" + strings.TrimPrefix(m, ":") + ">[^/]+)"
	}) + "$"
	uia2Routes = append(uia2Routes, uia2Route{method: method, path: path, pattern: regexp.MustCompile(expr), handle: h})
}

// The routes of AppiumServlet.java of the real server, in its order (the first match wins).
func init() {
	s := "/session/:sessionId"
	e := s + "/element/:id"
	route("DELETE", s, (*uia2Server).deleteSession)
	route("POST", "/session", (*uia2Server).newSession)
	route("POST", s+"/element", (*uia2Server).findElement)
	route("POST", s+"/elements", (*uia2Server).findElements)
	route("POST", e+"/click", (*uia2Server).click)
	route("POST", e+"/clear", (*uia2Server).clear)
	route("POST", s+"/orientation", (*uia2Server).setOrientation)
	route("POST", s+"/rotation", (*uia2Server).setRotation)
	route("POST", s+"/back", (*uia2Server).back)
	route("POST", e+"/value", (*uia2Server).sendKeys)
	route("POST", s+"/keys", (*uia2Server).sendKeys)
	route("POST", s+"/appium/device/open_notifications", (*uia2Server).null)
	route("POST", s+"/appium/device/press_keycode", (*uia2Server).pressKeyCode)
	route("POST", s+"/appium/device/long_press_keycode", (*uia2Server).pressKeyCode)
	route("POST", s+"/appium/settings", (*uia2Server).updateSettings)
	route("POST", s+"/network_connection", (*uia2Server).networkConnection)
	route("POST", s+"/appium/device/get_clipboard", (*uia2Server).getClipboard)
	route("POST", s+"/appium/device/set_clipboard", (*uia2Server).setClipboard)
	route("POST", s+"/alert/accept", (*uia2Server).noAlert)
	route("POST", s+"/alert/dismiss", (*uia2Server).noAlert)
	route("POST", s+"/actions", (*uia2Server).actions)
	route("POST", s+"/appium/reset_ax_cache", (*uia2Server).null)
	route("POST", s+"/appium/list_windows", (*uia2Server).listWindows)
	route("POST", s+"/appium/list_displays", (*uia2Server).listDisplays)
	route("POST", s+"/gestures/scroll_to", (*uia2Server).scrollTo)
	route("POST", s+"/appium/element/:id/scroll_to/:id2", (*uia2Server).scrollToElement)
	route("POST", s+"/appium/schedule_action", (*uia2Server).null)
	route("POST", s+"/appium/action_history", (*uia2Server).actionHistory)
	route("POST", s+"/appium/unschedule_action", (*uia2Server).actionHistory)
	route("GET", "/status", (*uia2Server).status)
	route("GET", "/appium/device/apps", (*uia2Server).packages)
	route("GET", "/sessions", (*uia2Server).sessions)
	route("GET", s+"/appium/device/apps", (*uia2Server).packages)
	route("GET", s, (*uia2Server).sessionDetails)
	route("GET", s+"/screenshot", (*uia2Server).screenshot)
	route("GET", s+"/orientation", (*uia2Server).orientation)
	route("GET", s+"/rotation", (*uia2Server).rotation)
	route("GET", e+"/text", (*uia2Server).text)
	route("GET", e+"/attribute/:name", (*uia2Server).attribute)
	route("GET", e+"/rect", (*uia2Server).rect)
	route("GET", e+"/size", (*uia2Server).size)
	route("GET", e+"/name", (*uia2Server).name)
	route("GET", s+"/element/active", (*uia2Server).activeElement)
	route("GET", e+"/screenshot", (*uia2Server).elementScreenshot)
	route("GET", s+"/screenshot/:id", (*uia2Server).elementScreenshot)
	route("GET", e+"/location", (*uia2Server).location)
	route("GET", s+"/window/:windowHandle/size", (*uia2Server).windowSize)
	route("GET", s+"/source", (*uia2Server).source)
	route("GET", s+"/appium/device/system_bars", (*uia2Server).systemBars)
	route("GET", s+"/appium/device/battery_info", (*uia2Server).batteryInfo)
	route("GET", s+"/appium/settings", (*uia2Server).getSettings)
	route("GET", s+"/appium/device/pixel_ratio", (*uia2Server).pixelRatio)
	route("GET", s+"/appium/element/:id/first_visible", (*uia2Server).firstVisible)
	route("GET", s+"/alert/text", (*uia2Server).noAlert)
	route("GET", s+"/appium/device/info", (*uia2Server).deviceInfo)
	route("GET", s+"/appium/device/display_density", (*uia2Server).displayDensity)
}

var uia2ErrorStatus = map[string]int{
	"invalid argument": 400, "invalid selector": 400, "invalid element state": 400, "invalid session id": 404,
	"no such alert": 404, "no such element": 404, "stale element reference": 404, "unknown command": 404,
	"unknown method": 405,
}

// uia2Error writes an error as the real server: the session id of the request, the W3C code and its HTTP status.
func uia2Error(w http.ResponseWriter, sid any, code, message string) {
	status, ok := uia2ErrorStatus[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	writeUIA2(w, status, sid, map[string]any{"error": code, "message": message,
		"stacktrace": "io.appium.uiautomator2 (the dummy has no stacktrace)"})
}

func writeUIA2(w http.ResponseWriter, status int, sid any, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"sessionId": sid, "value": value})
}

func (s *uia2Server) reply(w http.ResponseWriter, p uia2Params, value any) {
	writeUIA2(w, http.StatusOK, p.sid, value)
}

func (s *uia2Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/wd/hub"), "/")
	for _, rt := range uia2Routes {
		m := rt.pattern.FindStringSubmatch(path)
		if m == nil || rt.method != r.Method {
			continue
		}
		var p uia2Params
		for i, name := range rt.pattern.SubexpNames() {
			switch name {
			case "sessionId":
				p.sid = m[i]
			case "id":
				p.id = m[i]
			case "id2":
				p.id2 = m[i]
			case "name":
				p.name = m[i]
			}
		}
		if r.Method == http.MethodPost {
			p.body = wd.Body(r)
		}
		if p.sid != "" {
			s.mu.Lock()
			known := s.session != nil && s.session.id == p.sid
			s.mu.Unlock()
			if !known {
				uia2Error(w, p.sid, "invalid session id", fmt.Sprintf("The session identified by %s is not known", p.sid))
				return
			}
		}
		rt.handle(s, w, r, p)
		return
	}
	s.d.App.Log("uia2", "unknown", "%s %s", r.Method, path)
	uia2Error(w, nil, "unknown command", "The requested resource could not be found, or a request was received using an HTTP method that is not supported by the mapped resource")
}

func (s *uia2Server) status(w http.ResponseWriter, _ *http.Request, _ uia2Params) {
	writeUIA2(w, http.StatusOK, "None", map[string]any{"ready": true, "message": "UiAutomator2 Server is ready to accept commands",
		"build": map[string]any{"version": uia2Version, "versionCode": uia2VersionCode}})
}

func (s *uia2Server) newSession(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	caps := p.body["capabilities"]
	if caps == nil {
		caps = map[string]any{}
	}
	sess := &uia2Session{id: newSessionID(), caps: caps}
	s.mu.Lock()
	s.session = sess
	s.settings = uia2DefaultSettings()
	s.mu.Unlock()
	s.d.App.Log("uia2", "session", "created %s", sess.id)
	writeUIA2(w, http.StatusOK, sess.id, map[string]any{"sessionId": sess.id, "capabilities": caps})
}

func (s *uia2Server) sessions(w http.ResponseWriter, _ *http.Request, _ uia2Params) {
	s.mu.Lock()
	list := []any{}
	if s.session != nil {
		list = append(list, map[string]any{"id": s.session.id, "capabilities": map[string]any{}, "sessionId": s.session.id})
	}
	s.mu.Unlock()
	writeUIA2(w, http.StatusOK, "None", list)
}

func (s *uia2Server) sessionDetails(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.reply(w, p, map[string]any{"lastScrollData": nil})
}

func (s *uia2Server) deleteSession(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.mu.Lock()
	s.session = nil
	s.mu.Unlock()
	s.d.App.Log("uia2", "session", "deleted %s", p.sid)
	s.reply(w, p, nil)
	// the server quits after its session, the driver waits for its process to end
	go func() {
		time.Sleep(200 * time.Millisecond)
		s.d.procs.Kill(s.proc.PID, 15)
	}()
}

func (s *uia2Server) null(w http.ResponseWriter, _ *http.Request, p uia2Params) { s.reply(w, p, nil) }

func (s *uia2Server) noAlert(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	uia2Error(w, p.sid, "no such alert", "No alert is present on the screen")
}

func (s *uia2Server) getSettings(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.mu.Lock()
	out := map[string]any{}
	for k, v := range s.settings {
		out[k] = v
	}
	s.mu.Unlock()
	s.reply(w, p, out)
}

func (s *uia2Server) updateSettings(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.mu.Lock()
	for k, v := range wd.Obj(p.body, "settings") {
		s.settings[k] = v
	}
	s.mu.Unlock()
	s.reply(w, p, nil)
}

// element returns the element of an id, or writes the error of the real server.
func (s *uia2Server) element(w http.ResponseWriter, p uia2Params, id string) *ui.Element {
	for _, e := range ui.Elements(s.d.App.Layout()) {
		if ui.ElementUUID(e.ID) == id {
			return e
		}
	}
	uia2Error(w, p.sid, "no such element", fmt.Sprintf("The element identified by '%s' is not present in the cache or has expired. Try to find it again", id))
	return nil
}

// find runs a search of the driver: strategy and selector, the element of context for a search inside it.
func (s *uia2Server) find(w http.ResponseWriter, p uia2Params) ([]*ui.Element, bool) {
	for _, field := range []string{"strategy", "selector"} {
		if _, ok := p.body[field].(string); !ok {
			uia2Error(w, p.sid, "invalid argument", fmt.Sprintf("java.lang.IllegalArgumentException: FindElementModel: The mandatory field '%s' is not present in JSON", field))
			return nil, false
		}
	}
	strategy, selector := wd.Str(p.body, "strategy"), wd.Str(p.body, "selector")
	switch strategy {
	case "id", "xpath", "accessibility id", "class name", "-android uiautomator":
	default:
		uia2Error(w, p.sid, "invalid selector", fmt.Sprintf("Selector strategy '%s' is not supported. Only the following selector strategies are supported: [id, xpath, accessibility id, class name, -android uiautomator, -android datamatcher, -android viewmatcher, -android viewtag]", strategy))
		return nil, false
	}
	root := s.d.App.Layout()
	if ctxID := wd.Str(p.body, "context"); ctxID != "" {
		ctx := s.element(w, p, ctxID)
		if ctx == nil {
			return nil, false
		}
		// the descendants of the context element
		root = &ui.Element{ID: ctx.ID, Kind: ctx.Kind, Text: ctx.Text, Rect: ctx.Rect, Children: ctx.Children}
		if strategy == "xpath" {
			selector = "/*" + strings.TrimPrefix(selector, ".")
		}
	}
	found, err := ui.FindAndroid(root, s.d.App.Rotation(), strategy, selector)
	if err != nil {
		code := "unknown error"
		if errors.Is(err, ui.ErrInvalidSelector) {
			code = "invalid selector"
		}
		uia2Error(w, p.sid, code, err.Error())
		return nil, false
	}
	if wd.Str(p.body, "context") != "" {
		out := found[:0]
		for _, f := range found {
			if f.ID != root.ID {
				out = append(out, f)
			}
		}
		found = out
	}
	return found, true
}

func (s *uia2Server) findElement(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	found, ok := s.find(w, p)
	if !ok {
		return
	}
	if len(found) == 0 {
		uia2Error(w, p.sid, "no such element", "An element could not be located on the page using the given search parameters")
		return
	}
	s.reply(w, p, wd.ElementRef(ui.ElementUUID(found[0].ID)))
}

func (s *uia2Server) findElements(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	found, ok := s.find(w, p)
	if !ok {
		return
	}
	list := make([]any, 0, len(found))
	for _, e := range found {
		list = append(list, wd.ElementRef(ui.ElementUUID(e.ID)))
	}
	s.reply(w, p, list)
}

func (s *uia2Server) activeElement(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	if e := s.d.App.Find("input"); e != nil && e.Focused {
		s.reply(w, p, wd.ElementRef(ui.ElementUUID(e.ID)))
		return
	}
	uia2Error(w, p.sid, "no such element", "An element could not be located on the page using the given search parameters")
}

func (s *uia2Server) click(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	if e := s.element(w, p, p.id); e != nil {
		s.d.App.Click("uia2", e.ID)
		s.reply(w, p, nil)
	}
}

func (s *uia2Server) clear(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	if e := s.element(w, p, p.id); e != nil {
		s.d.App.SetText("uia2", e.ID, "")
		s.reply(w, p, nil)
	}
}

// sendKeys is the value of an element (or of the focused one): {"text": "...", "replace": false}.
func (s *uia2Server) sendKeys(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	text := wd.Text(p.body)
	if p.id == "" {
		p.id = wd.ElementID(p.body)
	}
	if p.id != "" {
		e := s.element(w, p, p.id)
		if e == nil {
			return
		}
		if replace, _ := p.body["replace"].(bool); replace {
			s.d.App.SetText("uia2", e.ID, text)
			s.reply(w, p, nil)
			return
		}
		s.d.App.Click("uia2", e.ID)
	}
	s.d.App.Type("uia2", text)
	s.reply(w, p, nil)
}

func (s *uia2Server) back(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.d.pressKey("uia2", 4)
	s.reply(w, p, nil)
}

func (s *uia2Server) pressKeyCode(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	code, ok := wd.Num(p.body, "keycode")
	if !ok {
		uia2Error(w, p.sid, "invalid argument", "java.lang.IllegalArgumentException: KeyCodeModel: The mandatory field 'keycode' is not present in JSON")
		return
	}
	s.d.pressKey("uia2", int(code))
	s.reply(w, p, nil)
}

func (s *uia2Server) orientation(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	o := "PORTRAIT"
	if s.d.App.Rotation()%180 != 0 {
		o = "LANDSCAPE"
	}
	s.reply(w, p, o)
}

func (s *uia2Server) setOrientation(w http.ResponseWriter, r *http.Request, p uia2Params) {
	switch strings.ToUpper(wd.Str(p.body, "orientation")) {
	case "LANDSCAPE":
		s.d.App.SetRotation("uia2", 90)
	case "PORTRAIT":
		s.d.App.SetRotation("uia2", 0)
	default:
		uia2Error(w, p.sid, "invalid argument", "java.lang.IllegalArgumentException: OrientationModel: The mandatory field 'orientation' is not present in JSON")
		return
	}
	s.orientation(w, r, p)
}

func (s *uia2Server) rotation(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.reply(w, p, map[string]any{"x": 0, "y": 0, "z": s.d.App.Rotation()})
}

func (s *uia2Server) setRotation(w http.ResponseWriter, r *http.Request, p uia2Params) {
	z, ok := wd.Num(p.body, "z")
	if !ok {
		uia2Error(w, p.sid, "invalid argument", "java.lang.IllegalArgumentException: RotationModel: The mandatory field 'z' is not present in JSON")
		return
	}
	s.d.App.SetRotation("uia2", int(z))
	s.rotation(w, r, p)
}

func (s *uia2Server) networkConnection(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	t, _ := wd.Num(p.body, "type")
	s.reply(w, p, int(t))
}

func (s *uia2Server) getClipboard(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.d.mu.Lock()
	text := s.d.clipboard
	s.d.mu.Unlock()
	if text == "" {
		s.reply(w, p, "")
		return
	}
	s.reply(w, p, base64.StdEncoding.EncodeToString([]byte(text)))
}

func (s *uia2Server) setClipboard(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	text, err := base64.StdEncoding.DecodeString(wd.Str(p.body, "content"))
	if err != nil {
		uia2Error(w, p.sid, "invalid argument", "The content of the clipboard must be base64 encoded")
		return
	}
	s.d.mu.Lock()
	s.d.clipboard = string(text)
	s.d.mu.Unlock()
	s.reply(w, p, nil)
}

func (s *uia2Server) origin(id string) (int, int, bool) {
	for _, e := range ui.Elements(s.d.App.Layout()) {
		if ui.ElementUUID(e.ID) == id {
			return e.Rect.X + e.Rect.W/2, e.Rect.Y + e.Rect.H/2, true
		}
	}
	return 0, 0, false
}

func (s *uia2Server) actions(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	for _, g := range wd.Gestures(p.body, s.origin) {
		if g.Kind == "swipe" {
			s.d.App.Swipe("uia2", g.X1, g.Y1, g.X2, g.Y2)
		} else {
			s.d.App.Tap("uia2", g.X1, g.Y1)
		}
	}
	if keys := wd.Keys(p.body); keys != "" {
		s.d.App.Type("uia2", keys)
	}
	s.reply(w, p, nil)
}

func (s *uia2Server) listWindows(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	width, height := s.d.App.Size()
	window := func(id, layer, typ int, pkg string, h int, active bool) map[string]any {
		return map[string]any{"displayId": 0, "isAccessibilityFocused": false, "isActive": active, "isFocused": active,
			"isInPictureInPictureMode": false, "layer": layer, "packageName": pkg, "physicalDisplayId": "4619827535830457088",
			"rect": map[string]any{"x": 0, "y": 0, "width": width, "height": h}, "screenshot": nil, "title": nil,
			"type": typ, "virtualDisplayId": nil, "windowId": id}
	}
	s.reply(w, p, []any{window(19, 1, 3, "com.android.systemui", 48, false),
		window(25, 0, 1, s.d.App.ForegroundApp(), height, true)})
}

func (s *uia2Server) listDisplays(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	c := s.d.Cfg
	density := float64(c.Density) / 160
	width, height := s.d.App.Size()
	s.reply(w, p, []any{map[string]any{"id": 0, "isDefault": true, "name": "Built-in Screen",
		"physicalId": "4619827535830457088", "virtualId": nil, "metrics": map[string]any{"density": density,
			"densityDpi": c.Density, "heightPixels": height, "scaledDensity": density, "widthPixels": width,
			"xdpi": float64(c.Density), "ydpi": float64(c.Density)}}})
}

func (s *uia2Server) scrollTo(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	params := wd.Obj(p.body, "params")
	found, _ := ui.FindAndroid(s.d.App.Layout(), 0, wd.Str(params, "strategy"), wd.Str(params, "selector"))
	if len(found) == 0 {
		uia2Error(w, p.sid, "no such element", "Could not scroll element into view")
		return
	}
	s.reply(w, p, nil)
}

func (s *uia2Server) scrollToElement(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	if s.element(w, p, p.id) != nil && s.element(w, p, p.id2) != nil {
		s.reply(w, p, nil)
	}
}

func (s *uia2Server) actionHistory(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	uia2Error(w, p.sid, "invalid argument", fmt.Sprintf("The action '%s' is not scheduled", wd.Str(p.body, "name")))
}

func (s *uia2Server) packages(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	list := []any{}
	for _, pkg := range s.d.pkgs.List() {
		list = append(list, map[string]any{"appPackage": pkg.Name, "appActivity": pkg.Launcher})
	}
	s.reply(w, p, list)
}

func (s *uia2Server) screenshot(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	width, height := s.d.App.Size()
	s.reply(w, p, base64.StdEncoding.EncodeToString(s.d.Screen.PNG(width, height)))
}

func (s *uia2Server) elementScreenshot(w http.ResponseWriter, r *http.Request, p uia2Params) {
	if s.element(w, p, p.id) != nil {
		s.screenshot(w, r, p)
	}
}

func (s *uia2Server) source(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.reply(w, p, ui.AndroidXML(s.d.App.Layout(), s.d.App.Rotation()))
}

func (s *uia2Server) text(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	if e := s.element(w, p, p.id); e != nil {
		s.reply(w, p, e.Text)
	}
}

// name is the content description, null without one.
func (s *uia2Server) name(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	e := s.element(w, p, p.id)
	if e == nil {
		return
	}
	if desc, _, null := ui.UIA2Attr(e, "content-desc"); !null {
		s.reply(w, p, desc)
		return
	}
	s.reply(w, p, nil)
}

func (s *uia2Server) attribute(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	e := s.element(w, p, p.id)
	if e == nil {
		return
	}
	v, known, null := ui.UIA2Attr(e, p.name)
	switch {
	case !known:
		uia2Error(w, p.sid, "unknown command", fmt.Sprintf("'%s' attribute is unknown for the element. Only the following attributes are supported: %s", p.name, ui.UIA2AttrNames()))
	case null:
		s.reply(w, p, nil)
	default:
		s.reply(w, p, v)
	}
}

func (s *uia2Server) rect(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	if e := s.element(w, p, p.id); e != nil {
		s.reply(w, p, map[string]any{"x": e.Rect.X, "y": e.Rect.Y, "width": e.Rect.W, "height": e.Rect.H})
	}
}

func (s *uia2Server) size(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	if e := s.element(w, p, p.id); e != nil {
		s.reply(w, p, map[string]any{"width": e.Rect.W, "height": e.Rect.H})
	}
}

func (s *uia2Server) location(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	if e := s.element(w, p, p.id); e != nil {
		s.reply(w, p, map[string]any{"x": e.Rect.X, "y": e.Rect.Y})
	}
}

func (s *uia2Server) firstVisible(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	e := s.element(w, p, p.id)
	if e == nil {
		return
	}
	if len(e.Children) == 0 {
		uia2Error(w, p.sid, "no such element", "Could not get first visible element inside of the given element")
		return
	}
	s.reply(w, p, wd.ElementRef(ui.ElementUUID(e.Children[0].ID)))
}

func (s *uia2Server) windowSize(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	width, height := s.d.App.Size()
	s.reply(w, p, map[string]any{"width": width, "height": height})
}

func (s *uia2Server) systemBars(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.reply(w, p, map[string]any{"statusBar": 48})
}

func (s *uia2Server) batteryInfo(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	power := s.d.App.Power()
	s.reply(w, p, map[string]any{"level": float64(power.Level) / 100, "status": power.Status()})
}

func (s *uia2Server) pixelRatio(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.reply(w, p, float64(s.d.Cfg.Density)/160)
}

func (s *uia2Server) displayDensity(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	s.reply(w, p, s.d.Cfg.Density)
}

func (s *uia2Server) deviceInfo(w http.ResponseWriter, _ *http.Request, p uia2Params) {
	c := s.d.Cfg
	s.reply(w, p, map[string]any{
		"androidId": fmt.Sprintf("%016x", serialNumber(c.Serial)), "manufacturer": c.Manufacturer, "model": c.Model,
		"brand": c.Brand, "apiVersion": strconv.Itoa(c.SDK), "platformVersion": c.OSVersion, "carrierName": "MCloud",
		"realDisplaySize": fmt.Sprintf("%dx%d", c.Width, c.Height), "displayDensity": c.Density,
		"networks": uia2Networks(s.d.App.Network()),
		"locale":   strings.ReplaceAll(c.Locale, "-", "_"), "timeZone": c.TimeZone, "bluetooth": map[string]any{"state": "OFF"},
	})
}

// uia2Networks is the networks of the device info: the active network or none.
func uia2Networks(network string) []any {
	typ, typeName, subtype, subtypeName, transport, extra := 1, "WIFI", 0, "", "TRANSPORT_WIFI", "\"MCloud\""
	switch network {
	case "cellular":
		typ, typeName, subtype, subtypeName, transport, extra = 0, "MOBILE", 13, "LTE", "TRANSPORT_CELLULAR", "internet"
	case "none":
		return []any{}
	}
	return []any{map[string]any{"type": typ, "typeName": typeName, "subtype": subtype, "subtypeName": subtypeName,
		"isConnected": true, "detailedState": "CONNECTED", "state": "CONNECTED", "extraInfo": extra,
		"isAvailable": true, "isRoaming": false, "isFailover": false,
		"capabilities": map[string]any{"transportTypes": transport,
			"networkCapabilities":       "NET_CAPABILITY_NOT_METERED,NET_CAPABILITY_INTERNET,NET_CAPABILITY_VALIDATED",
			"linkUpstreamBandwidthKbps": 30000, "linkDownBandwidthKbps": 30000, "signalStrength": -50, "SSID": nil}}}
}
