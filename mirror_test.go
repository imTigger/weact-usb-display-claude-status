package main

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPanelName(t *testing.T) {
	for in, want := range map[string]string{
		"/dev/serial/by-id/usb-WeAct_Studio_Display_FS_0.96_Inch_adde166c3c5e-if00": "adde166c3c5e",
		"/dev/weact-display": "weact-display",
	} {
		if got := panelName(in); got != want {
			t.Errorf("panelName(%q) = %q, want %q", in, got, want)
		}
	}
}

// connected returns a Display that looks connected on port and answers flips
// the way its session loop would.
func connected(t *testing.T, port string) *Display {
	d := NewDisplay("/dev/serial/by-id/usb-WeAct_Studio_Display_FS_0.96_Inch_x"+port+"-if00", nil)
	d.setInfo(port, orientLandscape, true)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case reply := <-d.flips:
				reply <- flipReply{res: FlipResult{Port: port, Orientation: orientLandscapeFlipped}}
			}
		}
	}()
	return d
}

func TestMirrorFlip(t *testing.T) {
	m := NewMirror("", nil)
	if _, err := m.Flip(""); err != errNotConnected {
		t.Fatalf("no panels: got %v, want errNotConnected", err)
	}

	m.panels["a"] = connected(t, "1-1")
	if res, err := m.Flip(""); err != nil || res.Port != "1-1" {
		t.Fatalf("one panel: got %+v, %v; want it flipped", res, err)
	}

	m.panels["b"] = connected(t, "1-2")
	if _, err := m.Flip(""); err == nil || !strings.Contains(err.Error(), "1-1, 1-2") {
		t.Fatalf("two panels, no port: got %v, want an error naming both ports", err)
	}
	if res, err := m.Flip("1-2"); err != nil || res.Port != "1-2" {
		t.Fatalf("port 1-2: got %+v, %v; want that panel flipped", res, err)
	}
	if _, err := m.Flip("9-9"); err == nil || !strings.Contains(err.Error(), "no panel on USB port 9-9") {
		t.Fatalf("unknown port: got %v", err)
	}
	if got := m.Panels(); len(got) != 2 || got[0].Port != "1-1" || got[1].Port != "1-2" {
		t.Fatalf("Panels() = %+v, want 1-1 then 1-2", got)
	}
}

func TestMirrorShowsEveryPanel(t *testing.T) {
	dir := t.TempDir()
	for _, serial := range []string{"aaa", "bbb"} {
		p := filepath.Join(dir, "usb-WeAct_Studio_Display_FS_0.96_Inch_"+serial+"-if00")
		if err := os.WriteFile(p, nil, 0o644); err != nil { // not a tty: sessions fail and retry
			t.Fatal(err)
		}
	}
	m := NewMirror(filepath.Join(dir, "usb-WeAct_Studio_Display_FS_0.96_Inch_*-if00"), NewOrientations(filepath.Join(dir, "o.json"), orientLandscape))
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); m.wg.Wait() }()

	f := Frame{Img: image.NewRGBA(image.Rect(0, 0, 1, 1))}
	m.Show(f) // before any panel is found: kept for panels found later
	m.scan(ctx)
	m.scan(ctx) // a second scan doesn't start a panel twice

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.panels) != 2 {
		t.Fatalf("got %d panels, want 2", len(m.panels))
	}
	for p, d := range m.panels {
		select {
		case got := <-d.frames:
			if got.Img != f.Img {
				t.Errorf("%s: got another frame", p)
			}
		default:
			t.Errorf("%s: no frame queued", p)
		}
	}
}
