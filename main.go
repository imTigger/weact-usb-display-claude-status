// Command claude-display mirrors Claude Code session status on a WeAct Studio
// Display FS 0.96" USB screen. Claude Code posts hook events to it over HTTP;
// it is the only process that writes to the panel.
//
//	claude-display [flags]         run the daemon
//	claude-display [flags] flip    turn the panel 180° and remember it for its USB port
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	home, _ := os.UserHomeDir()
	configDir, _ := os.UserConfigDir()
	var (
		devicePath  = flag.String("device", "/dev/weact-display", "serial device of the panel")
		listen      = flag.String("listen", "127.0.0.1:47800", "address Claude Code hooks post to")
		orientFile  = flag.String("config", filepath.Join(configDir, "claude-display", "orientation.json"), "orientation saved per USB port")
		orientation = flag.Int("orientation", orientLandscapeFlipped, "orientation for a USB port with nothing saved: 2 landscape, 3 landscape rotated 180°")
		sessionsDir = flag.String("sessions", filepath.Join(home, ".claude", "sessions"), "Claude Code sessions directory")
		samplesDir  = flag.String("samples", "", "write a PNG of every screen to this directory and exit")
		scale       = flag.Int("scale", 1, "with -samples: enlarge each pixel to a scale×scale block")
	)
	flag.Parse()
	log.SetFlags(0) // journald adds timestamps

	if flag.Arg(0) == "flip" {
		if err := flip(*listen); err != nil {
			fmt.Fprintln(os.Stderr, "claude-display flip:", err)
			os.Exit(1)
		}
		return
	}
	if !validOrientation(*orientation) {
		log.Fatalf("-orientation %d: want 2 or 3", *orientation)
	}
	if *samplesDir != "" {
		if err := writeSamples(*samplesDir, *scale); err != nil {
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
	display := NewDisplay(*devicePath, NewOrientations(*orientFile, byte(*orientation)))
	srv := &http.Server{Handler: NewServer(tracker, display), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("listening on http://%s/event", ln.Addr())

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

// samples are the screens rendered by -samples, also used for the README.
var samples = []struct {
	name string
	view View
}{
	{"thinking", View{Kind: KindThinking, Big: "Thinking…", Small: "my-app", Corner: "2:14", Detail: "12 tools · 2 agents", Spinner: true, Dots: []Kind{KindReady, KindThinking, KindDone}, Focus: 1}},
	{"tool", View{Kind: KindTool, Big: "Bash", Small: "my-app", Corner: "0:47", Detail: "3 tools", Spinner: true}},
	{"tool-long", View{Kind: KindTool, Big: displayTool("mcp__plugin_exa_exa__web_search_exa"), Small: "payments-dashboard-frontend", Corner: "1:02:03", Detail: "148 tools · 5 agents", Spinner: true, Dots: []Kind{KindTool, KindTool, KindNeedsYou, KindReady, KindDone}, Focus: 0}},
	{"compacting", View{Kind: KindCompacting, Big: "Compacting", Small: "my-app", Corner: "8:40", Detail: "31 tools", Spinner: true}},
	{"approve", View{Kind: KindNeedsYou, Big: "APPROVE?", Small: "Bash · my-app", Dots: []Kind{KindNeedsYou, KindTool}, Focus: 0}},
	{"approve-2", View{Kind: KindNeedsYou, Big: "APPROVE?", Small: "api-server", Corner: "2 waiting"}},
	{"question", View{Kind: KindNeedsYou, Big: "Question", Small: "my-app", Corner: "×2"}},
	{"done", View{Kind: KindDone, Big: "Done", Small: "my-app", Corner: "4:12", Detail: "23 tools", Dots: []Kind{KindReady, KindTool, KindDone}, Focus: 2}},
	{"error", View{Kind: KindError, Big: "Error", Small: "my-app", Detail: "rate limit"}},
	{"ready", View{Kind: KindReady, Big: "Ready", Small: "my-app", Dots: []Kind{KindReady, KindReady}, Focus: 1}},
	{"clock", View{Kind: KindClock, Big: "21:47", Small: "no sessions"}},
}

// writeSamples renders every sample screen to dir, each pixel blown up to a
// scale×scale block so they stay crisp in a README.
func writeSamples(dir string, scale int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	now := time.UnixMilli(4 * 120) // a fixed moment, so the spinner shows ✻ and files don't churn
	for _, s := range samples {
		f, err := os.Create(filepath.Join(dir, s.name+".png"))
		if err != nil {
			return err
		}
		err = png.Encode(f, scaleUp(Render(s.view, now).Img, scale))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

func scaleUp(src *image.RGBA, k int) *image.RGBA {
	if k <= 1 {
		return src
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
	for y := range dst.Rect.Dy() {
		for x := range dst.Rect.Dx() {
			dst.SetRGBA(x, y, src.RGBAAt(b.Min.X+x/k, b.Min.Y+y/k))
		}
	}
	return dst
}

// flip asks the running daemon to turn the panel 180°.
func flip(listen string) error {
	resp, err := http.Post("http://"+listen+"/flip", "application/json", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("is the claude-display service running? %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return errors.New(strings.TrimSpace(string(body)))
	}
	var res FlipResult
	if err := json.Unmarshal(body, &res); err != nil {
		return err
	}
	if res.SaveError != "" {
		fmt.Printf("flipped to orientation %d, but couldn't save it for USB port %s: %s\n", res.Orientation, res.Port, res.SaveError)
		return nil
	}
	fmt.Printf("flipped to orientation %d, saved for USB port %s\n", res.Orientation, res.Port)
	return nil
}
