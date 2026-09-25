package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sessionFile is Claude Code's ~/.claude/sessions/<pid>.json. The format is
// internal and undocumented, so it's only used as a fallback signal: if it
// changes shape, ReadLiveSessions returns nil and hooks alone drive the screen.
type sessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`    // "busy", "waiting" or "idle"
	ProcStart string `json:"procStart"` // /proc/<pid>/stat starttime, guards against PID reuse
}

func ReadLiveSessions(dir string) map[string]LiveSession {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil
	}
	live := map[string]LiveSession{}
	parsed := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f sessionFile
		if json.Unmarshal(b, &f) != nil || f.SessionID == "" || f.PID == 0 {
			continue
		}
		parsed++
		live[f.SessionID] = LiveSession{
			SessionID:   f.SessionID,
			Cwd:         f.Cwd,
			Interactive: f.Kind == "interactive",
			Status:      f.Status,
			Alive:       processAlive(f.PID, f.ProcStart),
		}
	}
	if len(paths) > 0 && parsed == 0 {
		return nil // files exist but none parse: format changed, don't trust it
	}
	return live
}

func processAlive(pid int, procStart string) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	if procStart == "" {
		return true
	}
	// Fields after "(comm)" start at field 3; starttime is field 22.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return true
	}
	fields := strings.Fields(string(b[i+1:]))
	return len(fields) > 19 && fields[19] == procStart
}
