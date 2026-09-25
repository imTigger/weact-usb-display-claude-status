// Command claude-display mirrors Claude Code session status on a WeAct Studio
// Display FS 0.96" USB screen. Claude Code posts hook events to it over HTTP;
// it is the only process that writes to the panel.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	home, _ := os.UserHomeDir()
	var (
		devicePath  = flag.String("device", "/dev/weact-display", "serial device of the panel")
		listen      = flag.String("listen", "127.0.0.1:47800", "address Claude Code hooks post to")
		orientation = flag.Int("orientation", 3, "panel orientation: 2 landscape, 3 landscape rotated 180°")
		sessionsDir = flag.String("sessions", filepath.Join(home, ".claude", "sessions"), "Claude Code sessions directory")
		samples     = flag.String("samples", "", "write a PNG of every screen to this directory and exit")
	)
	flag.Parse()
	log.SetFlags(0) // journald adds timestamps

	if *samples != "" {
		if err := writeSamples(*samples); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tracker := NewTracker(time.Now())
	tracker.Reconcile(ReadLiveSessions(*sessionsDir), time.Now())

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Handler: NewServer(tracker), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("listening on http://%s/event", ln.Addr())

	display := NewDisplay(*devicePath, byte(*orientation))
	displayDone := make(chan struct{})
	go func() {
		display.Run(ctx)
		close(displayDone)
	}()

	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				tracker.Reconcile(ReadLiveSessions(*sessionsDir), time.Now())
			}
		}
	}()

	renderLoop(ctx, tracker, display)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	select { // let the panel go back to its own screen
	case <-displayDone:
	case <-time.After(3 * time.Second):
	}
}

// renderLoop redraws on every state change, and on a timer for the spinner
// and the seconds counter. The display only sends pixels that changed.
func renderLoop(ctx context.Context, t *Tracker, d *Display) {
	for {
		now := time.Now()
		v := t.View(now)
		d.Show(Render(v, now))
		step := time.Second
		if v.Spinner {
			step = 120 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-t.Changed():
		case <-time.After(time.Until(now.Truncate(step).Add(step))):
		}
	}
}

func writeSamples(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dots := []Kind{KindReady, KindTool, KindDone}
	views := map[string]View{
		"clock":      {Kind: KindClock, Big: "21:47", Small: "no sessions"},
		"ready":      {Kind: KindReady, Big: "Ready", Small: "qinheng-display", Dots: []Kind{KindReady, KindReady}, Focus: 1},
		"thinking":   {Kind: KindThinking, Big: "Thinking…", Small: "qinheng-display", Corner: "2:14", Detail: "12 tools · 2 agents", Spinner: true, Dots: dots, Focus: 1},
		"tool":       {Kind: KindTool, Big: "Bash", Small: "qinheng-display", Corner: "12:05", Detail: "3 tools", Spinner: true},
		"tool-long":  {Kind: KindTool, Big: displayTool("mcp__plugin_exa_exa__web_search_exa"), Small: "supplier-portal-frontend", Corner: "1:02:03", Detail: "148 tools · 5 agents", Spinner: true, Dots: []Kind{KindTool, KindTool, KindNeedsYou, KindReady, KindDone}, Focus: 0},
		"compacting": {Kind: KindCompacting, Big: "Compacting", Small: "qinheng-display", Corner: "8:40", Detail: "31 tools", Spinner: true},
		"approve":    {Kind: KindNeedsYou, Big: "APPROVE?", Small: "Bash · qinheng-display", Dots: []Kind{KindNeedsYou, KindTool}, Focus: 0},
		"approve-2":  {Kind: KindNeedsYou, Big: "APPROVE?", Small: "qinheng-display", Corner: "2 waiting"},
		"question":   {Kind: KindNeedsYou, Big: "Question", Small: "qinheng-display", Corner: "×2"},
		"done":       {Kind: KindDone, Big: "Done", Small: "qinheng-display", Corner: "4:12", Detail: "23 tools", Dots: dots, Focus: 2},
		"error":      {Kind: KindError, Big: "Error", Small: "qinheng-display", Detail: "rate limit"},
	}
	now := time.Now()
	for name, v := range views {
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			return err
		}
		err = png.Encode(f, Render(v, now).Img)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
