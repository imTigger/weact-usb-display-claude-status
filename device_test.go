package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceCurrent(t *testing.T) {
	dir := t.TempDir()
	tty0 := filepath.Join(dir, "ttyACM0")
	link := filepath.Join(dir, "weact-display")
	if err := os.WriteFile(tty0, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ttyACM0", link); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	d := &Device{f: f}
	defer d.Close()

	if !d.Current(link) {
		t.Fatal("freshly opened: want current")
	}
	// Unplugged: udev removes the node and the link.
	os.Remove(link)
	os.Remove(tty0)
	if d.Current(link) {
		t.Fatal("link gone: want not current")
	}
	// Replugged: the panel comes back as a new tty.
	if err := os.WriteFile(filepath.Join(dir, "ttyACM1"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ttyACM1", link); err != nil {
		t.Fatal(err)
	}
	if d.Current(link) {
		t.Fatal("link points at a new tty: want not current")
	}
}
