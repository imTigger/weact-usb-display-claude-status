package main

import (
	"slices"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 25, 21, 0, 0, 0, time.UTC)

// replay applies events one second apart and returns the tracker and the time
// of the last event.
func replay(t *testing.T, events ...HookEvent) (*Tracker, time.Time) {
	t.Helper()
	tr := NewTracker(t0)
	now := t0
	for _, ev := range events {
		now = now.Add(time.Second)
		if ev.SessionID == "" {
			ev.SessionID = "a"
		}
		if ev.Cwd == "" {
			ev.Cwd = "/home/tiger/proj-" + ev.SessionID
		}
		tr.Apply(ev, now)
	}
	return tr, now
}

func ev(name string, kv ...string) HookEvent {
	e := HookEvent{Event: name}
	for i := 0; i+1 < len(kv); i += 2 {
		switch kv[i] {
		case "tool":
			e.ToolName = kv[i+1]
		case "session":
			e.SessionID = kv[i+1]
		case "agent":
			e.AgentID = kv[i+1]
		case "type":
			e.NotificationType = kv[i+1]
		case "source":
			e.Source = kv[i+1]
		case "error":
			e.Error = kv[i+1]
		}
	}
	return e
}

func TestTurn(t *testing.T) {
	steps := []struct {
		ev   HookEvent
		kind Kind
		big  string
	}{
		{ev("SessionStart", "source", "startup"), KindReady, "Ready"},
		{ev("UserPromptSubmit"), KindThinking, "Thinking…"},
		{ev("PreToolUse", "tool", "Bash"), KindTool, "Bash"},
		{ev("PermissionRequest", "tool", "Bash"), KindNeedsYou, "APPROVE?"},
		{ev("Notification", "type", "permission_prompt"), KindNeedsYou, "APPROVE?"},
		{ev("PostToolUse", "tool", "Bash"), KindThinking, "Thinking…"},
		{ev("PreToolUse", "tool", "mcp__context7__query-docs"), KindTool, "query-docs"},
		{ev("PreToolUse", "tool", "AskUserQuestion"), KindNeedsYou, "Question"},
		{ev("PostToolUse", "tool", "AskUserQuestion"), KindThinking, "Thinking…"},
		{ev("Stop"), KindDone, "Done"},
	}
	tr := NewTracker(t0)
	now := t0
	for i, s := range steps {
		now = now.Add(time.Second)
		s.ev.SessionID, s.ev.Cwd = "a", "/home/tiger/qinheng-display"
		tr.Apply(s.ev, now)
		v := tr.View(now)
		if v.Kind != s.kind || v.Big != s.big {
			t.Fatalf("step %d %s: got %v %q, want %v %q", i, s.ev.Event, v.Kind, v.Big, s.kind, s.big)
		}
	}
	if v := tr.View(now); v.Corner != "0:08" || v.Small != "qinheng-display" {
		t.Errorf("done view = %+v, want turn time 0:08 in project qinheng-display", v)
	}
	if v := tr.View(now.Add(doneLinger + time.Second)); v.Kind != KindReady {
		t.Errorf("after %v: got %v, want Ready", doneLinger, v.Kind)
	}
}

func TestSubagentToolsDontOverrideMainTool(t *testing.T) {
	tr, now := replay(t,
		ev("UserPromptSubmit"),
		ev("PreToolUse", "tool", "Agent"),
		ev("SubagentStart", "agent", "x"),
		ev("SubagentStart", "agent", "y"),
		ev("PreToolUse", "tool", "Grep", "agent", "x"),
		ev("PostToolUse", "tool", "Grep", "agent", "x"),
	)
	v := tr.View(now)
	if v.Big != "Agent" || v.Corner != "0:05" || v.Detail != "2 tools · 2 agents" {
		t.Fatalf("got %+v, want Agent at 0:05 with 2 tools · 2 agents", v)
	}
	// A subagent's permission prompt still needs you, and approving clears it.
	tr.Apply(ev("PermissionRequest", "session", "a", "tool", "Bash", "agent", "x"), now)
	if v := tr.View(now); v.Kind != KindNeedsYou || v.Small != "Bash · proj-a" {
		t.Fatalf("subagent permission: got %+v", v)
	}
	tr.Apply(ev("PostToolUse", "session", "a", "tool", "Bash", "agent", "x"), now)
	if v := tr.View(now); v.Kind != KindThinking {
		t.Fatalf("after approval: got %v, want Thinking", v.Kind)
	}
}

func TestMostUrgentSessionWins(t *testing.T) {
	tr, now := replay(t,
		ev("UserPromptSubmit", "session", "a"),
		ev("Stop", "session", "a"),
		ev("UserPromptSubmit", "session", "b"),
		ev("PreToolUse", "session", "b", "tool", "Edit"),
		ev("UserPromptSubmit", "session", "c"),
		ev("PermissionRequest", "session", "c", "tool", "Write"),
	)
	want := []Kind{KindDone, KindTool, KindNeedsYou} // in the order the sessions opened
	if v := tr.View(now); v.Kind != KindNeedsYou || v.Small != "Write · proj-c" || !slices.Equal(v.Dots, want) || v.Focus != 2 {
		t.Fatalf("got %+v, want c's approval, dots %v focused on c", v, want)
	}
	tr.Apply(ev("SessionEnd", "session", "c"), now)
	if v := tr.View(now); v.Kind != KindTool || v.Big != "Edit" || len(v.Dots) != 2 || v.Focus != 1 {
		t.Fatalf("after c ends: got %+v, want b's Edit", v)
	}
	tr.Apply(ev("SessionEnd", "session", "b"), now)
	if v := tr.View(now); v.Dots != nil {
		t.Fatalf("one session left: got dots %v, want none", v.Dots)
	}
}

func TestCompactionRestoresWork(t *testing.T) {
	tr, now := replay(t, ev("UserPromptSubmit"), ev("PreToolUse", "tool", "Read"), ev("PreCompact"))
	if v := tr.View(now); v.Kind != KindCompacting {
		t.Fatalf("got %v, want Compacting", v.Kind)
	}
	tr.Apply(ev("SessionStart", "session", "a", "source", "compact"), now)
	if v := tr.View(now); v.Kind != KindThinking {
		t.Fatalf("after compaction mid-turn: got %v, want Thinking", v.Kind)
	}
	tr, now = replay(t, ev("Stop"), ev("PreCompact"), ev("PostCompact"))
	if v := tr.View(now); v.Kind != KindReady {
		t.Fatalf("after /compact between turns: got %v, want Ready", v.Kind)
	}
}

func TestStopFailure(t *testing.T) {
	tr, now := replay(t, ev("UserPromptSubmit"), ev("StopFailure", "error", "rate_limit"))
	if v := tr.View(now); v.Kind != KindError || v.Detail != "rate limit" || v.Small != "proj-a" {
		t.Fatalf("got %+v", v)
	}
	tr.Apply(ev("UserPromptSubmit", "session", "a"), now)
	if v := tr.View(now); v.Kind != KindThinking {
		t.Fatalf("next prompt: got %v, want Thinking", v.Kind)
	}
}

func TestReconcile(t *testing.T) {
	live := func(status string, alive bool) map[string]LiveSession {
		return map[string]LiveSession{"a": {SessionID: "a", Cwd: "/x/proj-a", Interactive: true, Status: status, Alive: alive}}
	}

	t.Run("interrupt with Esc: no Stop hook, process goes idle", func(t *testing.T) {
		tr, now := replay(t, ev("UserPromptSubmit"), ev("PreToolUse", "tool", "Bash"))
		tr.Reconcile(live(statusIdle, true), now.Add(time.Second))
		if v := tr.View(now); v.Kind != KindTool {
			t.Fatalf("within %v: got %v, want still Tool", interruptLag, v.Kind)
		}
		tr.Reconcile(live(statusIdle, true), now.Add(interruptLag+time.Second))
		if v := tr.View(now); v.Kind != KindReady {
			t.Fatalf("got %v, want Ready", v.Kind)
		}
	})
	t.Run("open permission prompt reports waiting, not idle", func(t *testing.T) {
		tr, now := replay(t, ev("UserPromptSubmit"), ev("PermissionRequest", "tool", "Write"))
		tr.Reconcile(live(statusWaiting, true), now.Add(time.Minute))
		if v := tr.View(now); v.Kind != KindNeedsYou {
			t.Fatalf("got %v, want NeedsYou", v.Kind)
		}
	})
	t.Run("denied prompt: no Stop hook, process goes idle", func(t *testing.T) {
		tr, now := replay(t, ev("UserPromptSubmit"), ev("PermissionRequest", "tool", "Write"))
		tr.Reconcile(live(statusIdle, true), now.Add(interruptLag+time.Second))
		if v := tr.View(now); v.Kind != KindReady {
			t.Fatalf("got %v, want Ready", v.Kind)
		}
	})
	t.Run("busy process keeps a long tool running", func(t *testing.T) {
		tr, now := replay(t, ev("UserPromptSubmit"), ev("PreToolUse", "tool", "Bash"))
		tr.Reconcile(live(statusBusy, true), now.Add(time.Hour))
		if v := tr.View(now); v.Kind != KindTool {
			t.Fatalf("got %v, want Tool", v.Kind)
		}
	})
	t.Run("dead process is dropped", func(t *testing.T) {
		tr, now := replay(t, ev("UserPromptSubmit"))
		tr.Reconcile(live(statusBusy, false), now)
		if v := tr.View(now); v.Kind != KindClock {
			t.Fatalf("got %v, want Clock", v.Kind)
		}
	})
	t.Run("session without hooks mirrors busy/idle", func(t *testing.T) {
		tr := NewTracker(t0)
		tr.Reconcile(live(statusBusy, true), t0)
		if v := tr.View(t0); v.Kind != KindThinking || v.Small != "proj-a" {
			t.Fatalf("got %+v, want Thinking in proj-a", v)
		}
		tr.Reconcile(live(statusWaiting, true), t0.Add(time.Second))
		if v := tr.View(t0); v.Kind != KindNeedsYou {
			t.Fatalf("got %v, want NeedsYou", v.Kind)
		}
		tr.Reconcile(live(statusIdle, true), t0.Add(time.Second))
		if v := tr.View(t0); v.Kind != KindReady {
			t.Fatalf("got %v, want Ready", v.Kind)
		}
		tr.Reconcile(map[string]LiveSession{}, t0.Add(2*time.Second))
		if v := tr.View(t0); v.Kind != KindClock {
			t.Fatalf("after its file is gone: got %v, want Clock", v.Kind)
		}
	})
	t.Run("non-interactive runs are not adopted", func(t *testing.T) {
		tr := NewTracker(t0)
		tr.Reconcile(map[string]LiveSession{"p": {SessionID: "p", Status: statusBusy, Alive: true}}, t0)
		if v := tr.View(t0); v.Kind != KindClock {
			t.Fatalf("got %v, want Clock", v.Kind)
		}
	})
	t.Run("unreadable sessions dir only times out stale sessions", func(t *testing.T) {
		tr, now := replay(t, ev("UserPromptSubmit"))
		tr.Reconcile(nil, now.Add(time.Hour))
		if v := tr.View(now); v.Kind != KindThinking {
			t.Fatalf("got %v, want Thinking", v.Kind)
		}
		tr.Reconcile(nil, now.Add(hookTimeout+time.Second))
		if v := tr.View(now); v.Kind != KindClock {
			t.Fatalf("got %v, want Clock", v.Kind)
		}
	})
}

func TestIdleScreen(t *testing.T) {
	tr := NewTracker(t0)
	if v := tr.View(t0.Add(time.Minute)); v.Kind != KindClock {
		t.Fatalf("got %v, want Clock", v.Kind)
	}
	if v := tr.View(t0.Add(clockFor + time.Second)); v.Kind != KindOff {
		t.Fatalf("got %v, want Off", v.Kind)
	}
}

func TestFormatting(t *testing.T) {
	for in, want := range map[string]string{
		"mcp__plugin_exa_exa__web_search_exa": "web_search_exa",
		"Task":                                "Agent",
		"Bash":                                "Bash",
	} {
		if got := displayTool(in); got != want {
			t.Errorf("displayTool(%q) = %q, want %q", in, got, want)
		}
	}
	for d, want := range map[time.Duration]string{
		9 * time.Second:                           "0:09",
		12*time.Minute + 5*time.Second:            "12:05",
		time.Hour + 2*time.Minute + 3*time.Second: "1:02:03",
	} {
		if got := fmtDuration(d); got != want {
			t.Errorf("fmtDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestFocus(t *testing.T) {
	tr := NewTracker(t0)
	now := t0
	// step applies events and returns what the screen shows afterwards, as the
	// render loop would see it.
	step := func(events ...HookEvent) View {
		t.Helper()
		for _, e := range events {
			now = now.Add(time.Second)
			e.Cwd = "/x/proj-" + e.SessionID
			tr.Apply(e, now)
		}
		return tr.View(now)
	}

	if v := step(ev("UserPromptSubmit", "session", "a"), ev("PreToolUse", "session", "a", "tool", "Bash")); v.Small != "proj-a" {
		t.Fatalf("got %q, want proj-a", v.Small)
	}
	// Two busy sessions: the one on screen stays through the other's tool calls
	// and through its own.
	for _, e := range []HookEvent{
		ev("UserPromptSubmit", "session", "b"),
		ev("PreToolUse", "session", "b", "tool", "Read"),
		ev("PostToolUse", "session", "a", "tool", "Bash"),
		ev("PostToolUse", "session", "b", "tool", "Read"),
		ev("PreToolUse", "session", "b", "tool", "Edit"),
	} {
		if v := step(e); v.Small != "proj-a" {
			t.Fatalf("after %s from %s: showing %q, want proj-a to stay", e.Event, e.SessionID, v.Small)
		}
	}

	// Two prompts open while a is on screen: the one that waited longest first.
	step(ev("UserPromptSubmit", "session", "c"))
	tr.Apply(ev("PermissionRequest", "session", "c", "tool", "Write"), now.Add(time.Second))
	tr.Apply(ev("PermissionRequest", "session", "b", "tool", "Bash"), now.Add(2*time.Second))
	now = now.Add(2 * time.Second)
	v := tr.View(now)
	if v.Kind != KindNeedsYou || v.Small != "proj-c" || v.Corner != "2 waiting" || v.Dots != nil {
		t.Fatalf("got %+v, want c's prompt with 2 waiting and no squares", v)
	}
	// A newer prompt doesn't jump the queue.
	if v := step(ev("PermissionRequest", "session", "a", "tool", "Edit")); v.Small != "proj-c" || v.Corner != "3 waiting" {
		t.Fatalf("got %+v, want c still first with 3 waiting", v)
	}
	if v := step(ev("PostToolUse", "session", "c", "tool", "Write")); v.Small != "proj-b" || v.Corner != "2 waiting" {
		t.Fatalf("after answering c: got %+v, want b's prompt", v)
	}
	if v := step(ev("PostToolUse", "session", "b", "tool", "Bash")); v.Small != "Edit · proj-a" || v.Corner != "" || len(v.Dots) != 3 {
		t.Fatalf("after answering b: got %+v, want a's prompt alone, with squares", v)
	}
	// The session on screen ends: the next most urgent takes over.
	if v := step(ev("SessionEnd", "session", "a")); v.Kind != KindThinking {
		t.Fatalf("after a ends: got %+v, want a working session", v)
	}
}
