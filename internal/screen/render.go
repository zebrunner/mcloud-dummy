// Package screen renders the synthetic screen of a dummy device: the fake app, the clock, the recent touches.
package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/zebrunner/mcloud-dummy/internal/ui"
)

var (
	regularFont, _ = opentype.Parse(goregular.TTF)
	boldFont, _    = opentype.Parse(gobold.TTF)
)

// Renderer draws the screen of one device and caches the encoded frames.
type Renderer struct {
	app    *ui.App
	accent color.RGBA

	mu     sync.Mutex
	faces  map[faceKey]font.Face
	cache  map[cacheKey]*frame
	frames atomic.Uint64
}

type faceKey struct {
	bold bool
	size int
}

type cacheKey struct {
	w, h, quality int
	png           bool
}

type frame struct {
	version uint64
	second  int64
	data    []byte
}

// New creates the renderer of a device.
func New(app *ui.App) *Renderer {
	accent := color.RGBA{0x2e, 0x7d, 0x32, 0xff}
	if app.Info().Platform == "ios" {
		accent = color.RGBA{0x15, 0x65, 0xc0, 0xff}
	}
	return &Renderer{app: app, accent: accent, faces: map[faceKey]font.Face{}, cache: map[cacheKey]*frame{}}
}

// Frames is the number of rendered frames.
func (r *Renderer) Frames() uint64 { return r.frames.Load() }

func (r *Renderer) face(bold bool, size int) font.Face {
	if size < 6 {
		size = 6
	}
	k := faceKey{bold, size}
	if f, ok := r.faces[k]; ok {
		return f
	}
	ft := regularFont
	if bold {
		ft = boldFont
	}
	f, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic(err)
	}
	r.faces[k] = f
	return f
}

// JPEG returns the screen as a JPEG of the given size, re-rendered only when the content or the clock changed.
func (r *Renderer) JPEG(w, h, quality int) []byte {
	return r.encoded(cacheKey{w: w, h: h, quality: quality})
}

// PNG returns the screen as a PNG of the given size.
func (r *Renderer) PNG(w, h int) []byte {
	return r.encoded(cacheKey{w: w, h: h, png: true})
}

func (r *Renderer) encoded(k cacheKey) []byte {
	if k.w <= 0 || k.h <= 0 {
		k.w, k.h = r.app.Size()
	}
	if k.quality <= 0 || k.quality > 100 {
		k.quality = 80
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	version, second := r.app.Version(), time.Now().Unix()
	if f, ok := r.cache[k]; ok && f.version == version && f.second == second {
		return f.data
	}
	img := r.render(k.w, k.h)
	var buf bytes.Buffer
	if k.png {
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		_ = enc.Encode(&buf, img)
	} else {
		_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: k.quality})
	}
	if len(r.cache) > 16 {
		r.cache = map[cacheKey]*frame{}
	}
	r.cache[k] = &frame{version: version, second: second, data: buf.Bytes()}
	r.frames.Add(1)
	return buf.Bytes()
}

func (r *Renderer) render(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	dw, dh := r.app.Size()
	sx, sy := float64(w)/float64(dw), float64(h)/float64(dh)
	s := min(sx, sy)
	scale := func(rc ui.Rect) image.Rectangle {
		return image.Rect(int(float64(rc.X)*sx), int(float64(rc.Y)*sy), int(float64(rc.X+rc.W)*sx), int(float64(rc.Y+rc.H)*sy))
	}
	bg := color.RGBA{0xf4, 0xf6, 0xf8, 0xff}
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	unit := float64(min(dw, dh)) / 20
	barH := int(unit * 2 * sy)
	draw.Draw(img, image.Rect(0, 0, w, barH), &image.Uniform{r.accent}, image.Point{}, draw.Src)
	info := r.app.Info()
	now := r.app.Now()
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	r.text(img, now.Format("15:04:05"), false, int(unit*sx*0.5), barH*7/10, int(unit*s*1.1), white)
	r.textRight(img, statusText(r.app), false, w-int(unit*sx*0.5), barH*7/10, int(unit*s*0.9), white)

	if r.app.Locked() {
		dark := color.RGBA{0x10, 0x10, 0x18, 0xff}
		draw.Draw(img, image.Rect(0, barH, w, h), &image.Uniform{dark}, image.Point{}, draw.Src)
		r.textCenter(img, "Locked", true, w/2, h/2, int(unit*s*1.6), white)
		r.drawTouches(img, sx, sy, s, unit)
		return img
	}

	root := r.app.Layout()
	text := color.RGBA{0x21, 0x21, 0x21, 0xff}
	muted := color.RGBA{0x61, 0x61, 0x61, 0xff}
	for _, e := range ui.Elements(root) {
		rc := scale(e.Rect)
		size := int(unit * s * 0.9)
		baseline := rc.Min.Y + rc.Dy()/2 + size/3
		switch e.Kind {
		case ui.KindLabel:
			bold := e.ID == "title"
			c := muted
			if bold {
				c, size = text, int(unit*s*1.4)
				baseline = rc.Min.Y + rc.Dy()/2 + size/3
			}
			r.text(img, e.Text, bold, rc.Min.X, baseline, size, c)
		case ui.KindButton:
			fill := r.accent
			if e.ID == "reset_button" {
				fill = color.RGBA{0x75, 0x75, 0x75, 0xff}
			}
			roundRect(img, rc, rc.Dy()/4, fill)
			r.textCenter(img, e.Text, true, rc.Min.X+rc.Dx()/2, baseline, int(unit*s), white)
		case ui.KindInput:
			border := color.RGBA{0x9e, 0x9e, 0x9e, 0xff}
			if e.Focused {
				border = r.accent
			}
			roundRect(img, rc, rc.Dy()/6, border)
			inner := rc.Inset(max(2, int(unit*s*0.12)))
			roundRect(img, inner, inner.Dy()/6, white)
			t, c := e.Text, text
			if t == "" {
				t, c = "Type here", color.RGBA{0xbd, 0xbd, 0xbd, 0xff}
			}
			if e.Focused && now.UnixMilli()/500%2 == 0 {
				t += "|"
			}
			r.text(img, t, false, inner.Min.X+int(unit*s*0.5), baseline, size, c)
		}
	}
	footer := fmt.Sprintf("%s %s - %s - frame %d", now.Format("2006-01-02"), now.Format("MST"), info.Locale, r.frames.Load()+1)
	r.textCenter(img, footer, false, w/2, h-int(unit*sy), int(unit*s*0.8), muted)
	r.drawTouches(img, sx, sy, s, unit)
	return img
}

// statusText is the right part of the status bar: the network and the battery.
func statusText(app *ui.App) string {
	network := map[string]string{"wifi": "WiFi", "cellular": "LTE", "none": "No network"}[app.Network()]
	p := app.Power()
	charging := ""
	if p.Charging {
		charging = "+"
	}
	return fmt.Sprintf("%s  %d%%%s", network, p.Level, charging)
}

func (r *Renderer) drawTouches(img *image.RGBA, sx, sy, s, unit float64) {
	for _, t := range r.app.Touches() {
		age := time.Since(t.At).Seconds()
		alpha := uint8(max(0, 200-int(age*100)))
		c := color.RGBA{0xe5, 0x39, 0x35, alpha}
		if !t.Up {
			c = color.RGBA{0xff, 0x8f, 0x00, alpha}
		}
		circle(img, int(float64(t.X)*sx), int(float64(t.Y)*sy), int(unit*s*0.8), c)
	}
}

func (r *Renderer) measure(s string, bold bool, size int) int {
	return font.MeasureString(r.face(bold, size), s).Round()
}

func (r *Renderer) text(img *image.RGBA, s string, bold bool, x, y, size int, c color.Color) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: r.face(bold, size), Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func (r *Renderer) textCenter(img *image.RGBA, s string, bold bool, cx, y, size int, c color.Color) {
	r.text(img, s, bold, cx-r.measure(s, bold, size)/2, y, size, c)
}

func (r *Renderer) textRight(img *image.RGBA, s string, bold bool, right, y, size int, c color.Color) {
	r.text(img, s, bold, right-r.measure(s, bold, size), y, size, c)
}

func roundRect(img *image.RGBA, rc image.Rectangle, radius int, c color.Color) {
	src := image.NewUniform(c)
	if radius <= 0 {
		draw.Draw(img, rc, src, image.Point{}, draw.Over)
		return
	}
	radius = min(radius, rc.Dx()/2, rc.Dy()/2)
	draw.Draw(img, image.Rect(rc.Min.X+radius, rc.Min.Y, rc.Max.X-radius, rc.Max.Y), src, image.Point{}, draw.Over)
	draw.Draw(img, image.Rect(rc.Min.X, rc.Min.Y+radius, rc.Min.X+radius, rc.Max.Y-radius), src, image.Point{}, draw.Over)
	draw.Draw(img, image.Rect(rc.Max.X-radius, rc.Min.Y+radius, rc.Max.X, rc.Max.Y-radius), src, image.Point{}, draw.Over)
	for _, p := range []image.Point{{rc.Min.X + radius, rc.Min.Y + radius}, {rc.Max.X - radius - 1, rc.Min.Y + radius},
		{rc.Min.X + radius, rc.Max.Y - radius - 1}, {rc.Max.X - radius - 1, rc.Max.Y - radius - 1}} {
		circle(img, p.X, p.Y, radius, c)
	}
}

func circle(img *image.RGBA, cx, cy, radius int, c color.Color) {
	src := image.NewUniform(c)
	for dy := -radius; dy <= radius; dy++ {
		dx := 0
		for dx*dx+dy*dy <= radius*radius {
			dx++
		}
		if dx > 0 {
			draw.Draw(img, image.Rect(cx-dx+1, cy+dy, cx+dx, cy+dy+1), src, image.Point{}, draw.Over)
		}
	}
}
