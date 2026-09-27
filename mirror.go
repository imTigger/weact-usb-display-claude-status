package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultPanels matches every connected 0.96" panel by udev's per-serial
// names, so several can be plugged in at once.
const defaultPanels = "/dev/serial/by-id/usb-WeAct_Studio_Display_FS_0.96_Inch_*-if00"

// Mirror drives every connected panel with the same frames, each in the
// orientation saved for its USB port.
type Mirror struct {
	pattern string
	orients *Orientations
	wg      sync.WaitGroup

	mu     sync.Mutex
	panels map[string]*Display // by device path
	latest Frame
}

func NewMirror(pattern string, orients *Orientations) *Mirror {
	return &Mirror{pattern: pattern, orients: orients, panels: map[string]*Display{}}
}

// Show sends f to every panel, and to panels plugged in later.
func (m *Mirror) Show(f Frame) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latest = f
	for _, d := range m.panels {
		d.Show(f)
	}
}

// Run looks for new panels every 2 s until ctx is cancelled, then waits for
// every panel to be handed back to its own screen.
func (m *Mirror) Run(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		m.scan(ctx)
		select {
		case <-ctx.Done():
			m.wg.Wait()
			return
		case <-tick.C:
		}
	}
}

func (m *Mirror) scan(ctx context.Context) {
	paths, _ := filepath.Glob(m.pattern)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range paths {
		if _, ok := m.panels[p]; ok {
			continue
		}
		d := NewDisplay(p, m.orients)
		if m.latest.Img != nil {
			d.Show(m.latest)
		}
		m.panels[p] = d
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			d.Run(ctx) // returns once the panel is unplugged
			m.mu.Lock()
			delete(m.panels, p)
			m.mu.Unlock()
		}()
	}
}

// Panels lists the connected panels by USB port.
func (m *Mirror) Panels() []PanelInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []PanelInfo
	for _, d := range m.panels {
		if info := d.Info(); info.Connected {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// Flip turns one panel 180°: the one on port, or the only one connected.
func (m *Mirror) Flip(port string) (FlipResult, error) {
	m.mu.Lock()
	var match []*Display
	for _, d := range m.panels {
		if info := d.Info(); info.Connected && (port == "" || info.Port == port) {
			match = append(match, d)
		}
	}
	m.mu.Unlock()

	if len(match) == 1 {
		return match[0].Flip()
	}
	var ports []string
	for _, p := range m.Panels() {
		ports = append(ports, p.Port)
	}
	switch {
	case len(ports) == 0:
		return FlipResult{}, errNotConnected
	case len(match) == 0:
		return FlipResult{}, fmt.Errorf("no panel on USB port %s; panels are on %s", port, strings.Join(ports, ", "))
	default:
		return FlipResult{}, fmt.Errorf("%d panels are connected, on USB ports %s: say which, e.g. claude-display flip %s",
			len(ports), strings.Join(ports, ", "), ports[0])
	}
}
