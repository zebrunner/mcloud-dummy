// Package control is the HTTP API and the dashboard of mcloud-dummy: the state of the devices, their screens,
// the injected failures and the input.
package control

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zebrunner/mcloud-dummy/internal/android"
	"github.com/zebrunner/mcloud-dummy/internal/ios"
	"github.com/zebrunner/mcloud-dummy/internal/screen"
	"github.com/zebrunner/mcloud-dummy/internal/ui"
)

// Server of the control API.
type Server struct {
	Host    string
	Android []*android.Device
	IOS     []*ios.Device
}

type device struct {
	serial   string
	platform string
	app      *ui.App
	screen   *screen.Renderer
	and      *android.Device
	ios      *ios.Device
}

func (s *Server) devices() []device {
	var out []device
	for _, d := range s.Android {
		out = append(out, device{serial: d.Cfg.Serial, platform: "android", app: d.App, screen: d.Screen, and: d})
	}
	for _, d := range s.IOS {
		out = append(out, device{serial: d.Cfg.Serial, platform: "ios", app: d.App, screen: d.Screen, ios: d})
	}
	return out
}

func (s *Server) device(serial string) (device, bool) {
	for _, d := range s.devices() {
		if d.serial == serial {
			return d, true
		}
	}
	return device{}, false
}

func (s *Server) status(d device) any {
	if d.and != nil {
		return d.and.Status(s.Host)
	}
	return d.ios.Status(s.Host)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// Handler returns the routes of the control API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /{$}", s.dashboard)
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		var list []any
		for _, d := range s.devices() {
			list = append(list, s.status(d))
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /api/devices/{serial}", s.with(func(w http.ResponseWriter, r *http.Request, d device) {
		writeJSON(w, http.StatusOK, map[string]any{"status": s.status(d), "events": d.app.Events()})
	}))
	mux.HandleFunc("GET /api/devices/{serial}/events", s.with(func(w http.ResponseWriter, r *http.Request, d device) {
		writeJSON(w, http.StatusOK, d.app.Events())
	}))
	mux.HandleFunc("GET /api/devices/{serial}/screen", s.with(s.screen))
	mux.HandleFunc("GET /api/devices/{serial}/commands", s.with(func(w http.ResponseWriter, r *http.Request, d device) {
		if d.and == nil {
			writeJSON(w, http.StatusOK, d.ios.Status(s.Host).Requests)
			return
		}
		writeJSON(w, http.StatusOK, d.and.Commands())
	}))
	mux.HandleFunc("POST /api/devices/{serial}/faults", s.with(s.faults))
	mux.HandleFunc("POST /api/devices/{serial}/reboot", s.with(func(w http.ResponseWriter, r *http.Request, d device) {
		if d.and != nil {
			go d.and.Reboot()
		} else {
			go d.ios.Reboot()
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"rebooting": d.serial})
	}))
	mux.HandleFunc("POST /api/devices/{serial}/disconnect", s.with(func(w http.ResponseWriter, r *http.Request, d device) {
		if d.and != nil {
			d.and.Disconnect()
		} else {
			d.ios.DropSessions()
		}
		writeJSON(w, http.StatusOK, map[string]any{"disconnected": d.serial})
	}))
	mux.HandleFunc("POST /api/devices/{serial}/crash", s.with(func(w http.ResponseWriter, r *http.Request, d device) {
		if d.and == nil {
			d.ios.DropSessions()
			writeJSON(w, http.StatusOK, map[string]any{"sessions": "dropped"})
			return
		}
		name := r.URL.Query().Get("process")
		if name == "" {
			http.Error(w, "process is required, e.g. ?process=minicap", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"killed": d.and.Crash(name)})
	}))
	mux.HandleFunc("POST /api/devices/{serial}/input", s.with(s.input))
	mux.HandleFunc("POST /api/devices/{serial}/state", s.with(func(w http.ResponseWriter, r *http.Request, d device) {
		req, err := decodeState(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req.apply(d.app)
		writeJSON(w, http.StatusOK, s.status(d))
	}))
	mux.HandleFunc("POST /api/state", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeState(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		platform := r.URL.Query().Get("platform")
		n := 0
		for _, d := range s.devices() {
			if platform == "" || platform == d.platform {
				req.apply(d.app)
				n++
			}
		}
		writeJSON(w, http.StatusOK, map[string]int{"devices": n})
	})
	return mux
}

func (s *Server) with(h func(http.ResponseWriter, *http.Request, device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.device(r.PathValue("serial"))
		if !ok {
			http.Error(w, "unknown device", http.StatusNotFound)
			return
		}
		h(w, r, d)
	}
}

func (s *Server) screen(w http.ResponseWriter, r *http.Request, d device) {
	width, height := d.app.Size()
	if v, err := strconv.Atoi(r.URL.Query().Get("width")); err == nil && v > 0 {
		height = height * v / width
		width = v
	}
	w.Header().Set("Cache-Control", "no-cache")
	if r.URL.Query().Get("format") == "png" {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(d.screen.PNG(width, height))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(d.screen.JPEG(width, height, 70))
}

// faultsRequest accepts the durations as Go durations ("500ms") or as milliseconds.
type faultsRequest map[string]any

func duration(v any) time.Duration {
	switch x := v.(type) {
	case float64:
		return time.Duration(x) * time.Millisecond
	case string:
		d, _ := time.ParseDuration(x)
		return d
	}
	return 0
}

func boolean(v any) bool {
	b, _ := v.(bool)
	return b
}

func (s *Server) faults(w http.ResponseWriter, r *http.Request, d device) {
	var req faultsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if d.and != nil {
		f := d.and.Faults()
		for k, v := range req {
			switch k {
			case "offline":
				f.Offline = boolean(v)
			case "auth":
				f.Auth, _ = v.(string)
			case "latency":
				f.Latency = duration(v)
			case "bootStuck":
				f.BootStuck = boolean(v)
			case "noScreen":
				f.NoScreen = boolean(v)
			default:
				http.Error(w, "unknown fault "+k, http.StatusBadRequest)
				return
			}
		}
		if f.Auth != "" && f.Auth != "none" && f.Auth != "accept" && f.Auth != "deny" {
			http.Error(w, "auth must be none, accept or deny", http.StatusBadRequest)
			return
		}
		d.and.SetFaults(f)
		writeJSON(w, http.StatusOK, d.and.Faults())
		return
	}
	f := d.ios.Faults()
	for k, v := range req {
		switch k {
		case "down":
			f.Down = boolean(v)
		case "noMjpeg":
			f.NoMJPEG = boolean(v)
		case "latency":
			f.Latency = duration(v)
		case "singleSession":
			f.SingleSession = boolean(v)
		case "notReady":
			f.NotReady = boolean(v)
		default:
			http.Error(w, "unknown fault "+k, http.StatusBadRequest)
			return
		}
	}
	d.ios.SetFaults(f)
	writeJSON(w, http.StatusOK, d.ios.Faults())
}

func (s *Server) input(w http.ResponseWriter, r *http.Request, d device) {
	var req struct {
		Tap    *[2]int `json:"tap"`
		Swipe  *[4]int `json:"swipe"`
		Text   string  `json:"text"`
		Key    string  `json:"key"`
		Rotate *int    `json:"rotate"`
		Lock   *bool   `json:"lock"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hit := ""
	if req.Tap != nil {
		hit = d.app.Tap("control", req.Tap[0], req.Tap[1])
	}
	if req.Swipe != nil {
		d.app.Swipe("control", req.Swipe[0], req.Swipe[1], req.Swipe[2], req.Swipe[3])
	}
	if req.Text != "" {
		d.app.Type("control", req.Text)
	}
	if req.Key != "" {
		d.app.Key("control", req.Key)
	}
	if req.Rotate != nil {
		d.app.SetRotation("control", *req.Rotate)
	}
	if req.Lock != nil {
		d.app.SetLocked("control", *req.Lock)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hit": hit, "taps": d.app.Taps(), "input": d.app.Input()})
}

// stateRequest changes the state of a device; the fields that are not set stay.
type stateRequest struct {
	Battery  *int    `json:"battery"`
	Charging *bool   `json:"charging"`
	Network  *string `json:"network"`
	Rotation *int    `json:"rotation"`
}

func decodeState(r *http.Request) (stateRequest, error) {
	var req stateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, err
	}
	if req.Battery != nil && (*req.Battery < 0 || *req.Battery > 100) {
		return req, fmt.Errorf("battery must be 0-100")
	}
	if req.Network != nil && *req.Network != "wifi" && *req.Network != "cellular" && *req.Network != "none" {
		return req, fmt.Errorf("network must be wifi, cellular or none")
	}
	if req.Rotation != nil && (*req.Rotation%90 != 0 || *req.Rotation < 0 || *req.Rotation > 270) {
		return req, fmt.Errorf("rotation must be 0, 90, 180 or 270")
	}
	return req, nil
}

func (req stateRequest) apply(app *ui.App) {
	if req.Battery != nil || req.Charging != nil {
		p := app.Power()
		if req.Battery != nil {
			p.Level = *req.Battery
		}
		if req.Charging != nil {
			p.Charging = *req.Charging
		}
		app.SetPower("control", p)
	}
	if req.Network != nil {
		app.SetNetwork("control", *req.Network)
	}
	if req.Rotation != nil {
		app.SetRotation("control", *req.Rotation)
	}
}

var page = template.Must(template.New("dashboard").Funcs(template.FuncMap{"lower": strings.ToLower}).Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>mcloud-dummy</title>
<style>
body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;margin:16px;background:#fafafa;color:#212121}
.grid{display:flex;flex-wrap:wrap;gap:16px}
.card{background:#fff;border-radius:8px;box-shadow:0 1px 3px rgba(0,0,0,.2);padding:12px;width:240px}
.card img{width:216px;border-radius:6px;border:1px solid #ddd;cursor:crosshair}
.card h3{margin:4px 0;font-size:14px}.meta{font-size:11px;color:#616161;word-break:break-all}
button{font-size:11px;margin:2px}
</style></head><body>
<h2>mcloud-dummy - {{len .}} devices</h2>
<div class="grid">
{{range .}}<div class="card" data-serial="{{.Serial}}" data-platform="{{.Platform}}">
<h3>{{.Name}}</h3>
<div class="meta">{{.Platform}} - {{.Serial}}<br>{{.Address}}</div>
<img src="/api/devices/{{.Serial}}/screen?width=216" data-w="{{.W}}" data-h="{{.H}}" onclick="tap(event,this)">
<div>
{{if eq .Platform "android"}}
<button onclick="fault(this,{offline:true})">offline</button><button onclick="fault(this,{offline:false})">online</button>
<button onclick="fault(this,{auth:'deny'})">unauthorized</button><button onclick="fault(this,{auth:'none'})">authorized</button>
<button onclick="post(this,'disconnect')">disconnect</button><button onclick="post(this,'crash?process=minicap')">kill minicap</button>
{{else}}
<button onclick="fault(this,{down:true})">WDA down</button><button onclick="fault(this,{down:false})">WDA up</button>
<button onclick="fault(this,{noMjpeg:true})">no MJPEG</button><button onclick="fault(this,{noMjpeg:false})">MJPEG</button>
<button onclick="post(this,'disconnect')">drop sessions</button>
{{end}}
<button onclick="post(this,'reboot')">reboot</button><br>
<button onclick="state(this,{battery:5,charging:false})">battery 5%</button><button onclick="state(this,{battery:100,charging:true})">battery full</button>
<button onclick="state(this,{network:'wifi'})">wifi</button><button onclick="state(this,{network:'cellular'})">cellular</button><button onclick="state(this,{network:'none'})">no network</button>
<button onclick="state(this,{rotation:0})">portrait</button><button onclick="state(this,{rotation:90})">landscape</button>
</div></div>{{end}}
</div>
<script>
function serial(el){return el.closest('.card').dataset.serial}
function post(el,path,body){return fetch('/api/devices/'+encodeURIComponent(serial(el))+'/'+path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body||{})})}
function fault(el,f){post(el,'faults',f)}
function state(el,st){post(el,'state',st)}
function tap(e,img){
  var r=img.getBoundingClientRect();
  post(img,'input',{tap:[Math.round((e.clientX-r.left)/r.width*img.dataset.w),Math.round((e.clientY-r.top)/r.height*img.dataset.h)]})
}
setInterval(function(){document.querySelectorAll('.card img').forEach(function(img){
  img.src='/api/devices/'+encodeURIComponent(serial(img))+'/screen?width=216&t='+Date.now()})},1000)
</script></body></html>`))

type cardInfo struct {
	Serial, Name, Platform, Address string
	W, H                            int
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	var cards []cardInfo
	for _, d := range s.Android {
		cards = append(cards, cardInfo{Serial: d.Cfg.Serial, Name: d.Cfg.Name, Platform: "android",
			Address: fmt.Sprintf("adb connect %s:%d", s.Host, d.Cfg.Port), W: d.Cfg.Width, H: d.Cfg.Height})
	}
	for _, d := range s.IOS {
		cards = append(cards, cardInfo{Serial: d.Cfg.Serial, Name: d.Cfg.Name, Platform: "ios",
			Address: fmt.Sprintf("WDA %s:%d - MJPEG %d", s.Host, d.Cfg.WDAPort, d.Cfg.MJPEGPort), W: d.Cfg.Width, H: d.Cfg.Height})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(w, cards)
}
