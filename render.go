package main

import (
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	screenW = 160
	screenH = 80
	pad     = 6
)

// Kind is what the screen is showing; it decides colours, layout and backlight.
type Kind int

const (
	KindOff Kind = iota
	KindClock
	KindReady
	KindThinking
	KindTool
	KindCompacting
	KindNeedsYou
	KindDone
	KindError
)

// View is everything the renderer needs for one screen.
//
// Working, Done and Error use the status layout, modelled on Claude Code's
// spinner line: "✻ Thinking…  2:14" over the project and a detail line.
// Screens that must be noticed or read from afar use one big word instead.
type View struct {
	Kind    Kind
	Big     string // headline
	Small   string // project, or "tool · project"
	Corner  string // turn time or badges
	Detail  string // status layout only: turn activity or error
	Spinner bool
	Dots    []Kind // every open session in a stable order, when there are several
	Focus   int    // index in Dots of the session on screen
}

type style struct {
	bg, fg     color.RGBA
	brightness byte
	breathe    bool
}

var (
	white  = color.RGBA{255, 255, 255, 255}
	black  = color.RGBA{0, 0, 0, 255}
	orange = color.RGBA{217, 119, 87, 255}
)

func (k Kind) style() style {
	switch k {
	case KindClock:
		return style{bg: black, fg: color.RGBA{110, 120, 135, 255}, brightness: 25}
	case KindReady:
		return style{bg: color.RGBA{24, 28, 36, 255}, fg: color.RGBA{150, 160, 175, 255}, brightness: 60}
	case KindThinking, KindTool:
		return style{bg: orange, fg: white, brightness: 200}
	case KindCompacting:
		return style{bg: color.RGBA{124, 92, 200, 255}, fg: white, brightness: 200}
	case KindNeedsYou:
		return style{bg: color.RGBA{255, 176, 0, 255}, fg: black, breathe: true}
	case KindDone:
		return style{bg: color.RGBA{46, 160, 67, 255}, fg: white, brightness: 200}
	case KindError:
		return style{bg: color.RGBA{200, 50, 50, 255}, fg: white, brightness: 220}
	default: // KindOff
		return style{bg: black, fg: black}
	}
}

func (k Kind) statusLayout() bool {
	switch k {
	case KindThinking, KindTool, KindCompacting, KindDone, KindError:
		return true
	}
	return false
}

var (
	//go:embed fonts/DejaVuSansCondensed-Bold.ttf
	boldTTF []byte
	//go:embed fonts/DejaVuSansCondensed.ttf
	regularTTF []byte
	//go:embed fonts/DejaVuSans-Bold.ttf
	symbolTTF []byte // the DejaVu cut that has the ✢✳✶✻✽ dingbats

	boldFont    = mustParse(boldTTF)
	regularFont = mustParse(regularTTF)
	symbolFont  = mustParse(symbolTTF)
)

// Claude Code's own spinner glyphs, played forwards then backwards.
var spinnerFrames = []rune("·✢✳✶✻✽✻✶✳✢")

func mustParse(ttf []byte) *opentype.Font {
	f, err := opentype.Parse(ttf)
	if err != nil {
		panic(err)
	}
	return f
}

type faceKey struct {
	f    *opentype.Font
	size int
}

// faces caches font faces. Render is only ever called from one goroutine.
var faces = map[faceKey]font.Face{}

func face(f *opentype.Font, size int) font.Face {
	k := faceKey{f, size}
	if fc, ok := faces[k]; ok {
		return fc
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	faces[k] = fc
	return fc
}

// Render draws v. Spinner frames are picked from now, so calling it again a
// moment later animates.
func Render(v View, now time.Time) Frame {
	st := v.Kind.style()
	img := image.NewRGBA(image.Rect(0, 0, screenW, screenH))
	fill(img, img.Bounds(), st.bg)
	switch {
	case v.Kind == KindOff:
	case v.Kind.statusLayout():
		renderStatus(img, v, st.fg, now)
	default:
		renderBig(img, v, st.fg)
	}
	return Frame{Img: img, Brightness: st.brightness, Breathe: st.breathe}
}

const (
	iconBox   = 20
	dotSize   = 8
	dotGap    = 3
	baseline1 = 24 // status layout rows
	baseline2 = 48
	baseline3 = 72
)

func renderStatus(img *image.RGBA, v View, fg color.RGBA, now time.Time) {
	icon := "✔"
	switch {
	case v.Spinner:
		icon = string(spinnerFrames[now.UnixMilli()/120%int64(len(spinnerFrames))])
	case v.Kind == KindError:
		icon = "✖"
	}
	sf := face(symbolFont, 17)
	drawAt(img, sf, pad+(iconBox-textWidth(sf, icon))/2, baseline1-2, icon, fg)
	left := pad + iconBox + 2
	hf, head := fit(boldFont, v.Big, 22, 14, screenW-pad-left) // the headline gets the whole row
	drawAt(img, hf, left, baseline1, head, fg)

	right := screenW - pad
	if v.Corner != "" {
		cf := face(regularFont, 17)
		cw := textWidth(cf, v.Corner)
		// Digits have no descenders, so on the project's baseline they look
		// raised next to "qinheng-display"; drop them to line up by eye.
		drawAt(img, cf, right-cw, baseline2+2, v.Corner, fg)
		right -= cw + pad
	}
	pf, project := fit(regularFont, v.Small, 17, 13, right-pad)
	drawAt(img, pf, pad, baseline2, project, fg)

	right = screenW - pad
	if len(v.Dots) > 0 {
		right = drawDots(img, v, fg, right, baseline3-dotSize-1) - pad
	}
	df, detail := fit(regularFont, v.Detail, 15, 12, right-pad)
	drawAt(img, df, pad, baseline3, detail, fg)
}

func renderBig(img *image.RGBA, v View, fg color.RGBA) {
	const bigMax, bigMin = 34, 16
	bf, big := fit(boldFont, v.Big, bigMax, bigMin, screenW-2*pad)
	drawAt(img, bf, pad, 4+face(boldFont, bigMax).Metrics().Ascent.Ceil(), big, fg)

	const small = screenH - 7 // baseline of the bottom line
	right := screenW - pad
	if len(v.Dots) > 0 {
		right = drawDots(img, v, fg, right, small-dotSize-1) - pad
	}
	if v.Corner != "" {
		cf := face(regularFont, 13) // smaller than the project, which says where to go
		cw := textWidth(cf, v.Corner)
		drawAt(img, cf, right-cw, small, v.Corner, fg)
		right -= cw + 4
	}
	sf, text := fit(regularFont, v.Small, 16, 12, right-pad)
	drawAt(img, sf, pad, small, text, fg)
}

// drawDots draws one square per session, filled with that session's colour,
// ringed so it shows on any background, with the one on screen underlined.
// It returns the left edge of the row.
func drawDots(img *image.RGBA, v View, fg color.RGBA, right, top int) int {
	left := right - len(v.Dots)*(dotSize+dotGap) + dotGap
	for i, k := range v.Dots {
		x := left + i*(dotSize+dotGap)
		r := image.Rect(x, top, x+dotSize, top+dotSize)
		fill(img, r, fg)
		fill(img, r.Inset(1), k.style().bg)
		if i == v.Focus {
			fill(img, image.Rect(x, r.Max.Y+2, r.Max.X, r.Max.Y+4), fg)
		}
	}
	return left
}

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
}

// fit picks the largest size in [minSize, size] that shows s within maxW, and
// truncates s with an ellipsis if even minSize is too wide.
func fit(f *opentype.Font, s string, size, minSize, maxW int) (font.Face, string) {
	if s == "" {
		return face(f, size), s
	}
	for ; size >= minSize; size-- {
		if textWidth(face(f, size), s) <= maxW {
			return face(f, size), s
		}
	}
	fc := face(f, minSize)
	r := []rune(s)
	for len(r) > 0 && textWidth(fc, string(r)+"…") > maxW {
		r = r[:len(r)-1]
	}
	return fc, string(r) + "…"
}

func textWidth(fc font.Face, s string) int { return font.MeasureString(fc, s).Ceil() }

// drawAt draws s with its baseline at y.
func drawAt(img *image.RGBA, fc font.Face, x, y int, s string, c color.RGBA) {
	d := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: fc, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

var kindNames = [...]string{"off", "clock", "ready", "thinking", "tool", "compacting", "needs-you", "done", "error"}

func (k Kind) MarshalText() ([]byte, error) { return []byte(kindNames[k]), nil }
