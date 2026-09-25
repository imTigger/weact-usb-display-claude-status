package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

// Frame is one complete screen state for the panel.
type Frame struct {
	Img        *image.RGBA // never mutated after Show
	Brightness byte
	Breathe    bool // pulse the backlight to draw attention
}

const (
	fadeTime      = 400 * time.Millisecond
	breathePeriod = time.Second
	breatheHigh   = 255
	breatheLow    = 40
)

// Display owns the device: it reconnects whenever the panel goes away, sends
// only the pixels that changed, and always shows the most recent frame.
type Display struct {
	path      string
	orients   *Orientations
	frames    chan Frame
	flips     chan chan flipReply
	connected atomic.Bool
}

func NewDisplay(path string, orients *Orientations) *Display {
	return &Display{path: path, orients: orients, frames: make(chan Frame, 1), flips: make(chan chan flipReply)}
}

// FlipResult reports a 180° turn: the port the panel is on and the
// orientation now saved for it.
type FlipResult struct {
	Port        string `json:"port"`
	Orientation byte   `json:"orientation"`
	SaveError   string `json:"save_error,omitempty"` // turned, but won't be remembered
}

type flipReply struct {
	res FlipResult
	err error
}

var errNotConnected = errors.New("display not connected")

// Flip turns the panel 180° and remembers that for the USB port it's on. It
// returns an error only when the panel wasn't turned.
func (d *Display) Flip() (FlipResult, error) {
	if !d.connected.Load() {
		return FlipResult{}, errNotConnected
	}
	reply := make(chan flipReply, 1)
	select {
	case d.flips <- reply:
	case <-time.After(3 * time.Second):
		return FlipResult{}, errNotConnected
	}
	select {
	case r := <-reply:
		return r.res, r.err
	case <-time.After(5 * time.Second):
		return FlipResult{}, errors.New("display did not answer")
	}
}

// Show queues f, replacing any frame that hasn't been sent yet.
func (d *Display) Show(f Frame) {
	for {
		select {
		case d.frames <- f:
			return
		default:
		}
		select {
		case <-d.frames:
		default:
		}
	}
}

// Run drives the panel until ctx is cancelled, then hands it back to its
// standalone screen.
func (d *Display) Run(ctx context.Context) {
	var latest Frame
	lastErr := ""
	for {
		connected, err := d.session(ctx, &latest)
		if ctx.Err() != nil {
			return
		}
		if connected {
			lastErr = ""
		}
		if msg := err.Error(); msg != lastErr { // don't repeat "no such file" every retry
			log.Printf("display: %v", err)
			lastErr = msg
		}
		// Also outlasts the device's 500 ms timeout for a half-received bitmap.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *Display) session(ctx context.Context, latest *Frame) (connected bool, err error) {
	dev, err := OpenDevice(d.path)
	if err != nil {
		return false, err
	}
	defer dev.Close()

	who, err := dev.QueryString(cmdWhoAmI)
	if err != nil {
		// For a second after FREE (a previous instance shutting down) the
		// device ignores everything.
		time.Sleep(1100 * time.Millisecond)
		if who, err = dev.QueryString(cmdWhoAmI); err != nil {
			return false, err
		}
	}
	if !strings.Contains(who, "0.96") {
		return false, fmt.Errorf("unsupported display %q", who)
	}
	version, _ := dev.QueryString(cmdVersion)
	standalone, err := dev.QueryByte(cmdUnconnectBrt)
	if err != nil {
		standalone = 25 // factory default
	}
	port, err := usbPort(d.path, "/sys/class/tty")
	if err != nil {
		log.Printf("display: can't tell which USB port it's on: %v", err)
	}
	orient := d.orients.For(port)
	if err := dev.SetOrientation(orient); err != nil {
		return false, err
	}
	log.Printf("display: connected to %s %s on %s, USB port %s, orientation %d", who, version, d.path, port, orient)
	d.connected.Store(true)
	defer d.connected.Store(false)

	p := painter{dev: dev, brightness: -1}
	if latest.Img != nil {
		if err := p.paint(*latest); err != nil {
			return true, err
		}
	}
	tick := time.NewTicker(breathePeriod)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// Leave the panel as we found it: its own screen at its own brightness.
			_ = dev.SetBrightness(standalone, 0)
			_ = dev.Free()
			return true, nil
		case f := <-d.frames:
			*latest = f
			if err := p.paint(f); err != nil {
				return true, err
			}
		case <-tick.C:
			if err := p.breathe(); err != nil {
				return true, err
			}
		case reply := <-d.flips:
			o := flipped(orient)
			if err := dev.SetOrientation(o); err != nil {
				reply <- flipReply{err: err}
				return true, err
			}
			orient = o
			res := FlipResult{Port: port, Orientation: o}
			if err := d.orients.Set(port, o); err != nil {
				log.Printf("orientation: can't save: %v", err)
				res.SaveError = err.Error()
			}
			reply <- flipReply{res: res}
			p.shown = nil // the old picture is upside down now: repaint all of it
			if latest.Img != nil {
				if err := p.paint(*latest); err != nil {
					return true, err
				}
			}
		}
	}
}

type painter struct {
	dev        *Device
	shown      *image.RGBA // what the panel shows now; nil forces a full repaint
	brightness int         // last level sent; -1 when unknown
	breathing  bool
	high       bool
}

func (p *painter) paint(f Frame) error {
	if r := dirtyRect(p.shown, f.Img); !r.Empty() {
		p.shown = nil
		if err := p.dev.Blit(r, rgb565le(f.Img, r)); err != nil {
			return err
		}
		p.shown = f.Img
	}
	wasBreathing := p.breathing
	p.breathing = f.Breathe
	if f.Breathe {
		if !wasBreathing { // start the pulse bright, right away
			p.high = false
			return p.breathe()
		}
		return nil
	}
	if p.brightness != int(f.Brightness) {
		if err := p.dev.SetBrightness(f.Brightness, fadeTime); err != nil {
			return err
		}
		p.brightness = int(f.Brightness)
	}
	return nil
}

func (p *painter) breathe() error {
	if !p.breathing {
		return nil
	}
	p.high = !p.high
	level := byte(breatheLow)
	if p.high {
		level = breatheHigh
	}
	if err := p.dev.SetBrightness(level, breathePeriod*9/10); err != nil {
		return err
	}
	p.brightness = int(level)
	return nil
}

// dirtyRect is the bounding box of the pixels that differ between a and b.
func dirtyRect(a, b *image.RGBA) image.Rectangle {
	if a == nil || a.Rect != b.Rect {
		return b.Rect
	}
	var r image.Rectangle
	for y := b.Rect.Min.Y; y < b.Rect.Max.Y; y++ {
		for x := b.Rect.Min.X; x < b.Rect.Max.X; x++ {
			i, j := a.PixOffset(x, y), b.PixOffset(x, y)
			if a.Pix[i] != b.Pix[j] || a.Pix[i+1] != b.Pix[j+1] || a.Pix[i+2] != b.Pix[j+2] {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

func rgb565le(img *image.RGBA, r image.Rectangle) []byte {
	out := make([]byte, 0, r.Dx()*r.Dy()*2)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := img.Pix[img.PixOffset(x, y):]
			v := uint16(p[0]>>3)<<11 | uint16(p[1]>>2)<<5 | uint16(p[2]>>3)
			out = append(out, byte(v), byte(v>>8))
		}
	}
	return out
}
