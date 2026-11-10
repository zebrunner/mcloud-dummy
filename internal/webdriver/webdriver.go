// Package webdriver has the W3C WebDriver helpers shared by the fake UiAutomator2 server and the fake WebDriverAgent.
package webdriver

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
)

// Reply writes {"value": value} (and the sessionId when given), the response of a successful command.
func Reply(w http.ResponseWriter, sessionID string, value any) {
	body := map[string]any{"value": value}
	if sessionID != "" {
		body["sessionId"] = sessionID
	}
	write(w, http.StatusOK, body)
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// W3C errors and their HTTP statuses.
var errorStatus = map[string]int{
	"invalid argument":         http.StatusBadRequest,
	"invalid selector":         http.StatusBadRequest,
	"invalid session id":       http.StatusNotFound,
	"no such alert":            http.StatusNotFound,
	"no such element":          http.StatusNotFound,
	"stale element reference":  http.StatusNotFound,
	"unknown command":          http.StatusNotFound,
	"unknown method":           http.StatusMethodNotAllowed,
	"element not interactable": http.StatusBadRequest,
	"session not created":      http.StatusInternalServerError,
	"unknown error":            http.StatusInternalServerError,
	"unsupported operation":    http.StatusInternalServerError,
}

// Error writes a W3C error.
func Error(w http.ResponseWriter, sessionID, code, message string) {
	status, ok := errorStatus[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	body := map[string]any{"value": map[string]any{"error": code, "message": message, "stacktrace": ""}}
	if sessionID != "" {
		body["sessionId"] = sessionID
	}
	write(w, status, body)
}

// Body decodes the JSON body of a request; an empty body is an empty object.
func Body(r *http.Request) map[string]any {
	out := map[string]any{}
	data, err := io.ReadAll(io.LimitReader(r.Body, 64*1024*1024))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// Num reads a number of a decoded JSON object.
func Num(m map[string]any, key string) (float64, bool) {
	switch v := m[key].(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

// Str reads a string of a decoded JSON object.
func Str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// Obj reads an object of a decoded JSON object.
func Obj(m map[string]any, key string) map[string]any {
	o, _ := m[key].(map[string]any)
	return o
}

// Text is the text of the value command: {"text": "..."} or {"value": ["a", "b"]}.
func Text(m map[string]any) string {
	if t, ok := m["text"].(string); ok {
		return t
	}
	switch v := m["value"].(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, x := range v {
			if s, ok := x.(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}

// Gesture is what a W3C actions sequence of a pointer did.
type Gesture struct {
	Kind           string // tap, swipe, longpress
	X1, Y1, X2, Y2 int
	DurationMs     int
}

// Keys collects the typed characters of the key input sources.
func Keys(body map[string]any) string {
	var b strings.Builder
	actions, _ := body["actions"].([]any)
	for _, a := range actions {
		src, _ := a.(map[string]any)
		if Str(src, "type") != "key" {
			continue
		}
		steps, _ := src["actions"].([]any)
		for _, s := range steps {
			step, _ := s.(map[string]any)
			if Str(step, "type") == "keyDown" {
				b.WriteString(Str(step, "value"))
			}
		}
	}
	return b.String()
}

// Gestures interprets the pointer input sources of a W3C actions request. origin resolves an element origin
// ({"element-6066-...": id}) to its center.
func Gestures(body map[string]any, origin func(id string) (int, int, bool)) []Gesture {
	var out []Gesture
	actions, _ := body["actions"].([]any)
	for _, a := range actions {
		src, _ := a.(map[string]any)
		if Str(src, "type") != "pointer" {
			continue
		}
		steps, _ := src["actions"].([]any)
		var x, y float64
		var downX, downY float64
		down := false
		moved := false
		duration := 0
		for _, s := range steps {
			step, _ := s.(map[string]any)
			switch Str(step, "type") {
			case "pointerMove":
				nx, _ := Num(step, "x")
				ny, _ := Num(step, "y")
				switch o := step["origin"].(type) {
				case map[string]any:
					if cx, cy, ok := origin(ElementID(o)); ok {
						nx += float64(cx)
						ny += float64(cy)
					}
				case string:
					if o == "pointer" {
						nx += x
						ny += y
					}
				}
				if down && (math.Abs(nx-downX) > 10 || math.Abs(ny-downY) > 10) {
					moved = true
				}
				if dur, ok := Num(step, "duration"); ok && down {
					duration += int(dur)
				}
				x, y = nx, ny
			case "pointerDown":
				down = true
				moved = false
				duration = 0
				downX, downY = x, y
			case "pause":
				if dur, ok := Num(step, "duration"); ok && down {
					duration += int(dur)
				}
			case "pointerUp":
				if !down {
					continue
				}
				g := Gesture{Kind: "tap", X1: int(downX), Y1: int(downY), X2: int(x), Y2: int(y), DurationMs: duration}
				if moved {
					g.Kind = "swipe"
				} else if duration >= 500 {
					g.Kind = "longpress"
				}
				out = append(out, g)
				down = false
			}
		}
	}
	return out
}

// ElementRef is the element reference object of a found element.
func ElementRef(id string) map[string]any {
	return map[string]any{"ELEMENT": id, "element-6066-11e4-a52e-4f735466cecf": id}
}

// ElementID reads an element reference from a body ({"element": id} or a reference object).
func ElementID(m map[string]any) string {
	for _, k := range []string{"element-6066-11e4-a52e-4f735466cecf", "ELEMENT", "element", "elementId"} {
		switch v := m[k].(type) {
		case string:
			return v
		case map[string]any:
			if id := ElementID(v); id != "" {
				return id
			}
		}
	}
	return ""
}

// Route splits a WebDriver path into the session id and the command path: "/session/<id>/element" gives
// ("<id>", "element"); a path without a session gives ("", path). A /wd/hub prefix is ignored.
func Route(p string) (session, cmd string) {
	p = strings.TrimPrefix(p, "/wd/hub")
	p = strings.Trim(p, "/")
	if rest, ok := strings.CutPrefix(p, "session/"); ok {
		id, cmd, _ := strings.Cut(rest, "/")
		return id, cmd
	}
	return "", p
}
