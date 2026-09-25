package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOrientations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-display", "orientation.json")
	o := NewOrientations(path, orientLandscapeFlipped)

	if got := o.For("1-1"); got != orientLandscapeFlipped {
		t.Fatalf("nothing saved: got %d, want the fallback 3", got)
	}
	// Flipped on the right-hand port: remembered there, and the new default.
	if err := o.Set("1-2", orientLandscape); err != nil {
		t.Fatal(err)
	}
	if got := o.For("1-2"); got != orientLandscape {
		t.Fatalf("1-2: got %d, want 2", got)
	}
	if got := o.For("1-3.2"); got != orientLandscape {
		t.Fatalf("new port: got %d, want the last one chosen (2)", got)
	}
	// Back on the left: its own setting wins over the default.
	if err := o.Set("1-1", orientLandscapeFlipped); err != nil {
		t.Fatal(err)
	}
	if err := o.Set("1-2", orientLandscape); err != nil {
		t.Fatal(err)
	}
	if got := o.For("1-1"); got != orientLandscapeFlipped {
		t.Fatalf("1-1: got %d, want 3", got)
	}

	// Hand edits apply on the next lookup; junk is ignored, not fatal.
	if err := os.WriteFile(path, []byte(`{"default": 3, "ports": {"1-2": 3, "9-9": 7}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := o.For("1-2"); got != orientLandscapeFlipped {
		t.Fatalf("after hand edit: got %d, want 3", got)
	}
	if got := o.For("9-9"); got != orientLandscapeFlipped {
		t.Fatalf("invalid saved value: got %d, want the default 3", got)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := o.For("1-2"); got != orientLandscapeFlipped {
		t.Fatalf("broken file: got %d, want the fallback 3", got)
	}
}

func TestUSBPort(t *testing.T) {
	// A fake /dev and /sys shaped like the real ones:
	//   /dev/weact-display -> ttyACM1
	//   /sys/class/tty/ttyACM1/device -> /sys/devices/.../usb1/1-2/1-2:1.0
	root := t.TempDir()
	dev := filepath.Join(root, "dev")
	iface := filepath.Join(root, "sys/devices/pci0000:00/usb1/1-2/1-2:1.0")
	class := filepath.Join(root, "sys/class/tty")
	for _, d := range []string{dev, iface, filepath.Join(class, "ttyACM1")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dev, "ttyACM1"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ttyACM1", filepath.Join(dev, "weact-display")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(iface, filepath.Join(class, "ttyACM1", "device")); err != nil {
		t.Fatal(err)
	}

	got, err := usbPort(filepath.Join(dev, "weact-display"), class)
	if err != nil || got != "1-2" {
		t.Fatalf("got %q, %v; want 1-2", got, err)
	}
}
