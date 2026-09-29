package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Phase is where one Claude Code session is in its turn.
type Phase int

const (
	PhaseReady Phase = iota
	PhaseDone
	PhaseThinking
	PhaseTool
	PhaseCompacting
	PhaseError
	PhaseNeedsYou
)

func (p Phase) working() bool {
	return p == PhaseThinking || p == PhaseTool || p == PhaseCompacting
}

// priority orders sessions for the single screen: whatever needs you wins.
func (p Phase) priority() int {
	switch p {
	case PhaseNeedsYou:
		return 5
	case PhaseError:
		return 4
	case PhaseThinking, PhaseTool, PhaseCompacting:
		return 3
	case PhaseDone:
		return 2
	default:
		return 1
	}
}

const (
	doneLinger   = 5 * time.Minute  // green "Done" fades to "Ready" after this
	clockFor     = 10 * time.Minute // with no sessions, show a dim clock, then go dark
	interruptLag = 3 * time.Second  // quiet time before trusting an "idle" status over our own state
	orphanAfter  = 2 * time.Minute  // hooked session with no sessions file and no events
	hookTimeout  = 12 * time.Hour   // last resort when ~/.claude/sessions is unreadable
)

// HookEvent holds the fields of a Claude Code hook payload that matter here.
// Everything else (tool_input, prompts, file contents) is never decoded.
type HookEvent struct {
	Event            string `json:"hook_event_name"`
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	AgentID          string `json:"agent_id"` // set when a subagent fired the hook
	ToolName         string `json:"tool_name"`
	NotificationType string `json:"notification_type"`
	Error            string `json:"error"`
	Source           string `json:"source"`
	TranscriptPath   string `json:"transcript_path"`
}

type Session struct {
	ID         string
	Project    string
	Phase      Phase
	Tool       string
	Ask        string // big word while waiting on you
	Err        string
	TurnStart  time.Time
	TurnTime   time.Duration // length of the last finished turn
	Tools      int           // tool calls this turn, subagents' included
	Started    time.Time
	Title      string // "KECTASK-0883 merge status": tells apart sessions in one directory
	Transcript string
	Since      time.Time // when Phase was entered
	LastEvent  time.Time
	Subagents  int
	hooked     bool  // got hook events; otherwise only mirrored from ~/.claude/sessions
	prev       Phase // restored after compaction
}

func (s *Session) set(p Phase, now time.Time) {
	if s.Phase != p {
		s.Phase, s.Since = p, now
	}
}

func (s *Session) ask(label string, now time.Time) {
	s.Ask = label
	s.set(PhaseNeedsYou, now)
}

// Tracker is the state of every running Claude Code session.
type Tracker struct {
	mu         sync.Mutex
	sessions   map[string]*Session
	emptySince time.Time
	focusID    string // session on screen
	changed    chan struct{}
}

func NewTracker(now time.Time) *Tracker {
	return &Tracker{sessions: map[string]*Session{}, emptySince: now, changed: make(chan struct{}, 1)}
}

// Changed fires after any state change.
func (t *Tracker) Changed() <-chan struct{} { return t.changed }

func (t *Tracker) notify() {
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

func (t *Tracker) session(id, cwd string, now time.Time) *Session {
	s := t.sessions[id]
	if s == nil {
		s = &Session{ID: id, Started: now, Since: now, LastEvent: now}
		t.sessions[id] = s
	}
	if cwd != "" && s.Project == "" {
		s.Project = filepath.Base(cwd)
	}
	return s
}

func (t *Tracker) Apply(ev HookEvent, now time.Time) {
	if ev.SessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.notify()

	if ev.Event == "SessionEnd" {
		t.remove(ev.SessionID, now)
		return
	}
	s := t.session(ev.SessionID, ev.Cwd, now)
	s.LastEvent, s.hooked = now, true
	if ev.TranscriptPath != "" && s.Transcript == "" {
		s.Transcript = ev.TranscriptPath
	}
	fromSubagent := ev.AgentID != ""

	switch ev.Event {
	case "SessionStart": // only via a command hook: Claude Code skips HTTP hooks for it
		if ev.Source == "compact" {
			s.set(afterCompact(s.prev), now)
		} else {
			s.set(PhaseReady, now)
		}
	case "UserPromptSubmit":
		s.TurnStart, s.Tools = now, 0
		s.Err = ""
		s.set(PhaseThinking, now)
	case "PreToolUse":
		s.Tools++
		switch ev.ToolName {
		case "AskUserQuestion":
			s.ask("Question", now)
		case "ExitPlanMode":
			s.ask("Plan ready", now)
		default:
			// A subagent's tools would flicker over the main agent's "Agent".
			if !fromSubagent {
				s.Tool = displayTool(ev.ToolName)
				s.set(PhaseTool, now)
			}
		}
	case "PermissionRequest":
		s.Tool = displayTool(ev.ToolName)
		s.ask("APPROVE?", now)
	case "Notification":
		switch ev.NotificationType {
		case "permission_prompt":
			if s.Phase != PhaseNeedsYou {
				s.ask("APPROVE?", now)
			}
		case "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input":
			s.ask("INPUT?", now)
		case "idle_prompt": // Claude has been waiting on you for a while
			if s.Phase.working() {
				s.set(PhaseReady, now)
			}
		}
	case "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		// After you answer a prompt, the tool finishing is the first sign of it.
		if !fromSubagent || (s.Phase == PhaseNeedsYou && s.Ask == "APPROVE?") {
			s.set(PhaseThinking, now)
		}
	case "PreCompact":
		if s.Phase != PhaseCompacting {
			s.prev = s.Phase
		}
		s.set(PhaseCompacting, now)
	case "PostCompact":
		s.set(afterCompact(s.prev), now)
	case "SubagentStart":
		s.Subagents++
	case "SubagentStop":
		s.Subagents = max(s.Subagents-1, 0)
	case "Stop":
		if !s.TurnStart.IsZero() {
			s.TurnTime = now.Sub(s.TurnStart)
		}
		s.set(PhaseDone, now)
	case "StopFailure":
		s.Err = errorLabel(ev.Error)
		s.set(PhaseError, now)
	}
}

func afterCompact(prev Phase) Phase {
	if prev.working() {
		return PhaseThinking
	}
	return PhaseReady // manual /compact between turns
}

func (t *Tracker) remove(id string, now time.Time) {
	delete(t.sessions, id)
	if len(t.sessions) == 0 {
		t.emptySince = now
	}
}

// Session status values in ~/.claude/sessions, as observed on v2.1.282.
const (
	statusBusy    = "busy"
	statusWaiting = "waiting" // a permission prompt or question is open
	statusIdle    = "idle"
)

// LiveSession is one ~/.claude/sessions/<pid>.json entry for a running process.
type LiveSession struct {
	SessionID   string
	Cwd         string
	Interactive bool // a terminal session, not a `claude -p` or SDK run
	Status      string
	Alive       bool // process still running (and not a reused PID)
}

// Reconcile corrects what hooks alone can't see: Esc interrupts and denied
// prompts (no Stop hook fires), and sessions whose process died without a
// SessionEnd. live is nil when ~/.claude/sessions couldn't be read.
func (t *Tracker) Reconcile(live map[string]LiveSession, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	changed := false
	for id, s := range t.sessions {
		ls, known := live[id]
		switch {
		case live == nil:
			if now.Sub(s.LastEvent) > hookTimeout {
				t.remove(id, now)
				changed = true
			}
		case known && !ls.Alive, !known && (!s.hooked || now.Sub(s.LastEvent) > orphanAfter):
			t.remove(id, now)
			changed = true
		case known && ls.Status == statusIdle && now.Sub(s.LastEvent) > interruptLag &&
			(s.Phase.working() || s.Phase == PhaseNeedsYou):
			s.set(PhaseReady, now)
			s.Subagents = 0
			changed = true
		case known && !s.hooked: // no hooks (started before the plugin)
			changed = mirror(s, ls.Status, now) || changed
		}
	}
	for id, ls := range live {
		if _, ok := t.sessions[id]; !ok && ls.Alive && ls.Interactive {
			mirror(t.session(id, ls.Cwd, now), ls.Status, now)
			changed = true
		}
	}
	if changed {
		t.notify()
	}
}

// mirror sets a session without hooks from its status file alone.
func mirror(s *Session, status string, now time.Time) bool {
	old := s.Phase
	switch status {
	case statusBusy:
		s.set(PhaseThinking, now)
	case statusWaiting:
		s.ask("Waiting", now)
	case statusIdle:
		s.set(PhaseReady, now)
	}
	return s.Phase != old
}

// View picks the session to show and describes the screen for it.
func (t *Tracker) View(now time.Time) View {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.sessions) == 0 {
		if now.Sub(t.emptySince) > clockFor {
			return View{Kind: KindOff}
		}
		return View{Kind: KindClock, Big: now.Format("15:04"), Small: "no sessions"}
	}

	list := make([]*Session, 0, len(t.sessions))
	for _, s := range t.sessions {
		if s.Phase == PhaseDone && now.Sub(s.Since) > doneLinger {
			s.set(PhaseReady, now)
		}
		list = append(list, s)
	}
	// Dots keep the order sessions opened in, so each one stays in its place.
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Started.Equal(list[j].Started) {
			return list[i].Started.Before(list[j].Started)
		}
		return list[i].ID < list[j].ID
	})
	focus := t.pickFocus(list)
	s := list[focus]
	t.focusID = s.ID
	v := View{Small: s.Project, Title: s.Title, Focus: focus}
	waiting := 0
	if len(list) > 1 {
		for _, o := range list {
			v.Dots = append(v.Dots, o.Phase.kind())
			if o.Phase == PhaseNeedsYou {
				waiting++
			}
		}
	}
	elapsed := ""
	if !s.TurnStart.IsZero() {
		elapsed = fmtDuration(now.Sub(s.TurnStart))
	}

	switch s.Phase {
	case PhaseThinking:
		v.Kind, v.Big, v.Corner, v.Detail, v.Spinner = KindThinking, "Thinking…", elapsed, s.activity(), true
	case PhaseTool:
		v.Kind, v.Big, v.Corner, v.Detail, v.Spinner = KindTool, s.Tool, elapsed, s.activity(), true
	case PhaseCompacting:
		v.Kind, v.Big, v.Corner, v.Detail, v.Spinner = KindCompacting, "Compacting", elapsed, s.activity(), true
	case PhaseDone:
		v.Kind, v.Big, v.Corner, v.Detail = KindDone, "Done", fmtDuration(s.TurnTime), s.activity()
	case PhaseError:
		v.Kind, v.Big, v.Detail = KindError, "Error", s.Err
	case PhaseNeedsYou:
		v.Kind, v.Big, v.Corner = KindNeedsYou, s.Ask, s.agents()
		if s.Ask == "APPROVE?" && s.Tool != "" {
			v.Small = s.Tool + " · " + s.Project
		}
		if waiting > 1 {
			// Amber squares barely show on the amber screen; say it instead,
			// and keep the room for the project, which says where to go.
			v.Small, v.Corner, v.Dots = s.Project, fmt.Sprintf("%d waiting", waiting), nil
		}
	default:
		v.Kind, v.Big, v.Corner = KindReady, "Ready", s.agents()
	}
	return v
}

// pickFocus returns the index in list of the session to show. The session
// already on screen stays while nothing is more urgent than it, so busy
// sessions don't take turns at every tool call. Otherwise, of the most urgent
// sessions: the prompt that has waited longest, or the latest to change.
func (t *Tracker) pickFocus(list []*Session) int {
	top := 0
	for _, s := range list {
		top = max(top, s.Phase.priority())
	}
	best := -1
	for i, s := range list {
		if s.Phase.priority() != top {
			continue
		}
		if s.ID == t.focusID {
			return i
		}
		if best == -1 {
			best = i
			continue
		}
		if s.Phase == PhaseNeedsYou && s.Since.Before(list[best].Since) ||
			s.Phase != PhaseNeedsYou && s.Since.After(list[best].Since) {
			best = i
		}
	}
	return best
}

// activity summarises the turn so far: "12 tools · 2 agents".
func (s *Session) activity() string {
	var parts []string
	if s.Tools > 0 {
		parts = append(parts, plural(s.Tools, "tool"))
	}
	if s.Subagents > 0 {
		parts = append(parts, plural(s.Subagents, "agent"))
	}
	return strings.Join(parts, " · ")
}

// agents is the running-subagents badge for the big-word screens.
func (s *Session) agents() string {
	if s.Subagents == 0 {
		return ""
	}
	return fmt.Sprintf("×%d", s.Subagents)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func (p Phase) kind() Kind {
	switch p {
	case PhaseThinking:
		return KindThinking
	case PhaseTool:
		return KindTool
	case PhaseCompacting:
		return KindCompacting
	case PhaseNeedsYou:
		return KindNeedsYou
	case PhaseDone:
		return KindDone
	case PhaseError:
		return KindError
	default:
		return KindReady
	}
}

// Transcripts lists every session's transcript path, "" where not yet known.
func (t *Tracker) Transcripts() map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]string, len(t.sessions))
	for id, s := range t.sessions {
		out[id] = s.Transcript
	}
	return out
}

// SetTitle records a session's transcript path and the title read from it.
func (t *Tracker) SetTitle(id, transcript, title string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[id]
	if s == nil {
		return
	}
	s.Transcript = transcript
	if s.Title != title {
		s.Title = title
		t.notify()
	}
}

// Snapshot is the /state debug view.
func (t *Tracker) Snapshot() []Session {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Session, 0, len(t.sessions))
	for _, s := range t.sessions {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// displayTool shortens tool names for the screen: mcp__server__tool -> tool.
func displayTool(name string) string {
	if parts := strings.SplitN(name, "__", 3); len(parts) == 3 && parts[0] == "mcp" {
		return parts[2]
	}
	if name == "Task" {
		return "Agent"
	}
	if name == "" {
		return "Tool"
	}
	return name
}

func errorLabel(e string) string {
	if e == "" {
		return "unknown"
	}
	return strings.ReplaceAll(e, "_", " ")
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

var phaseNames = [...]string{"ready", "done", "thinking", "tool", "compacting", "error", "needs-you"}

func (p Phase) MarshalText() ([]byte, error) { return []byte(phaseNames[p]), nil }
