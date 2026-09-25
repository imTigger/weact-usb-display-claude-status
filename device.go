package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// WeAct Studio Display FS 0.96" serial protocol (CDC-ACM, firmware V1.0.0.2).
// Every command is [cmd][args, little-endian u16s][0x0A]; reads set bit 0x80
// and the device answers [cmd][payload][0x0A]. Writes are never acknowledged.
const (
	cmdSetOrientation = 0x02
	cmdSetBrightness  = 0x03
	cmdSetBitmap      = 0x05
	cmdFree           = 0x07 // back to the device's own standalone screen
	cmdWhoAmI         = 0x81
	cmdVersion        = 0xC2
	cmdUnconnectBrt   = 0x90 // brightness the standalone screen uses
	cmdEnd            = 0x0A
)

// Device is an open connection to the panel. It is not safe for concurrent use;
// Display owns the only instance.
type Device struct {
	f *os.File
}

func OpenDevice(path string) (*Device, error) {
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	d := &Device{f: f}
	if err := d.control(makeRaw); err != nil {
		f.Close()
		return nil, fmt.Errorf("raw mode: %w", err)
	}
	return d, nil
}

func (d *Device) Close() error { return d.f.Close() }

// control runs fn on the raw fd without f.Fd(), which would switch the file to
// blocking mode and disable deadlines.
func (d *Device) control(fn func(fd int) error) error {
	rc, err := d.f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

func makeRaw(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF
	t.Oflag &^= unix.OPOST // no \n -> \r\n rewriting of pixel data
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CRTSCTS | unix.CBAUD
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | unix.B115200 // baud is ignored by CDC-ACM
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		return err
	}
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}

// send writes b as its own USB transfer: it waits for the kernel to push the
// bytes out before returning, so the next write cannot share a packet with it.
func (d *Device) send(b []byte) error {
	_ = d.f.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := d.f.Write(b); err != nil {
		return err
	}
	return d.control(func(fd int) error { return unix.IoctlSetInt(fd, unix.TCSBRK, 1) }) // tcdrain
}

func (d *Device) SetOrientation(o byte) error {
	return d.send([]byte{cmdSetOrientation, o, cmdEnd})
}

// SetBrightness fades to level (0-255) over fade (device caps it at 5 s).
func (d *Device) SetBrightness(level byte, fade time.Duration) error {
	ms := min(max(fade.Milliseconds(), 0), 5000)
	return d.send([]byte{cmdSetBrightness, level, byte(ms), byte(ms >> 8), cmdEnd})
}

// Blit draws RGB565LE pixels into r. The header has to reach the device as a
// separate transfer from the pixel data: when they share one, the firmware
// silently drops the frame.
func (d *Device) Blit(r image.Rectangle, px []byte) error {
	if len(px) != r.Dx()*r.Dy()*2 {
		return fmt.Errorf("blit %v: got %d bytes, want %d", r, len(px), r.Dx()*r.Dy()*2)
	}
	x0, y0, x1, y1 := r.Min.X, r.Min.Y, r.Max.X-1, r.Max.Y-1
	hdr := []byte{cmdSetBitmap,
		byte(x0), byte(x0 >> 8), byte(y0), byte(y0 >> 8),
		byte(x1), byte(x1 >> 8), byte(y1), byte(y1 >> 8), cmdEnd}
	if err := d.send(hdr); err != nil {
		return err
	}
	return d.send(px)
}

func (d *Device) Free() error { return d.send([]byte{cmdFree, cmdEnd}) }

// QueryString reads a text reply (who-am-i, version).
func (d *Device) QueryString(cmd byte) (string, error) {
	b, err := d.query(cmd, func(buf []byte) bool { return len(buf) > 1 && buf[len(buf)-1] == cmdEnd })
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(b[1 : len(b)-1])), nil
}

// QueryByte reads a one-byte value. The reply is exactly 3 bytes; the value
// itself may be 0x0A, so the terminator can't be used to find the end.
func (d *Device) QueryByte(cmd byte) (byte, error) {
	b, err := d.query(cmd, func(buf []byte) bool { return len(buf) >= 3 })
	if err != nil {
		return 0, err
	}
	return b[1], nil
}

func (d *Device) query(cmd byte, done func([]byte) bool) ([]byte, error) {
	if err := d.control(func(fd int) error { return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }); err != nil {
		return nil, err
	}
	if err := d.send([]byte{cmd, cmdEnd}); err != nil {
		return nil, err
	}
	_ = d.f.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var buf []byte
	chunk := make([]byte, 64)
	for !done(buf) {
		n, err := d.f.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return nil, fmt.Errorf("query %#02x: no reply", cmd)
			}
			return nil, err
		}
	}
	if buf[0] != cmd {
		return nil, fmt.Errorf("query %#02x: unexpected reply % x", cmd, buf)
	}
	return buf, nil
}
