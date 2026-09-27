package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Landscape orientations; the layouts are all 160×80.
const (
	orientLandscape        = 2
	orientLandscapeFlipped = 3
)

func validOrientation(o int) bool { return o == orientLandscape || o == orientLandscapeFlipped }

func flipped(o byte) byte {
	if o == orientLandscape {
		return orientLandscapeFlipped
	}
	return orientLandscape
}

// Orientations remembers which way up the panel goes on each USB port, so it
// can move between the laptop's left and right sides without being flipped by
// hand each time. The file is plain JSON, fine to edit by hand:
//
//	{"default": 2, "ports": {"1-1": 3, "1-2": 2}}
type Orientations struct {
	path     string
	fallback byte // for ports with nothing saved, before anything was ever saved
	mu       sync.Mutex
}

type orientationFile struct {
	Default int            `json:"default,omitempty"` // last orientation chosen; used for new ports
	Ports   map[string]int `json:"ports"`
}

func NewOrientations(path string, fallback byte) *Orientations {
	return &Orientations{path: path, fallback: fallback}
}

// load reads the file fresh each time, so hand edits apply on the next
// reconnect without a restart.
func (o *Orientations) load() orientationFile {
	f := orientationFile{}
	if b, err := os.ReadFile(o.path); err == nil {
		if err := json.Unmarshal(b, &f); err != nil {
			log.Printf("orientation: ignoring %s: %v", o.path, err)
			f = orientationFile{}
		}
	}
	if f.Ports == nil {
		f.Ports = map[string]int{}
	}
	return f
}

// For returns the orientation for port: its own, else the last one chosen,
// else the fallback.
func (o *Orientations) For(port string) byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	f := o.load()
	for _, v := range []int{f.Ports[port], f.Default} {
		if validOrientation(v) {
			return byte(v)
		}
	}
	return o.fallback
}

// Set records orientation for port and makes it the default for new ports.
func (o *Orientations) Set(port string, orientation byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	f := o.load()
	if port != "" {
		f.Ports[port] = int(orientation)
	}
	f.Default = int(orientation)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.path), 0o755); err != nil {
		return err
	}
	tmp := o.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, o.path)
}

// usbPort names the physical USB port the serial device at devPath hangs off:
// "1-2" for a port on the laptop, "1-3.2" behind a hub or dock.
func usbPort(devPath, sysClassTTY string) (string, error) {
	tty, err := filepath.EvalSymlinks(devPath) // /dev/serial/by-id/usb-WeAct_…-if00 -> /dev/ttyACM1
	if err != nil {
		return "", err
	}
	// .../usb1/1-2/1-2:1.0: the USB interface, whose parent is the device on the port.
	iface, err := filepath.EvalSymlinks(filepath.Join(sysClassTTY, filepath.Base(tty), "device"))
	if err != nil {
		return "", err
	}
	return filepath.Base(filepath.Dir(iface)), nil
}
