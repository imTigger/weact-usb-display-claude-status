package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	titleTail   = 2 << 20 // on first sight of a transcript, read only its last 2 MB
	maxFragment = 4 << 20 // longest unfinished line kept between reads
)

// Titles follows session titles in Claude Code transcripts, which can run to
// tens of MB. Claude Code keeps appending {"type":"ai-title","aiTitle":…} as
// a conversation goes, and {"type":"custom-title","customTitle":…} after a
// /rename, which wins. Each file is read once from its tail, then only the
// bytes added since. Not safe for concurrent use.
type Titles struct {
	files map[string]*titleFile
}

type titleFile struct {
	offset   int64
	fragment []byte // unfinished last line
	skip     bool   // fragment is the middle of a line: drop up to the next newline
	custom   string
	ai       string
}

func NewTitles() *Titles { return &Titles{files: map[string]*titleFile{}} }

// Title returns the session title in the transcript at path, or "".
func (t *Titles) Title(path string) string {
	f := t.files[path]
	file, err := os.Open(path)
	if err != nil {
		if f != nil {
			return f.title()
		}
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	if f == nil || info.Size() < f.offset { // new, or rewritten shorter
		f = &titleFile{}
		if info.Size() > titleTail {
			f.offset, f.skip = info.Size()-titleTail, true
		}
		t.files[path] = f
	}
	if info.Size() > f.offset {
		if _, err := file.Seek(f.offset, io.SeekStart); err == nil {
			added, _ := io.ReadAll(io.LimitReader(file, info.Size()-f.offset))
			f.offset += int64(len(added))
			f.scan(added)
		}
	}
	return f.title()
}

// Forget drops files no longer asked about.
func (t *Titles) Forget(keep map[string]bool) {
	for p := range t.files {
		if !keep[p] {
			delete(t.files, p)
		}
	}
}

func (f *titleFile) title() string {
	if f.custom != "" {
		return f.custom
	}
	return f.ai
}

func (f *titleFile) scan(added []byte) {
	data := append(f.fragment, added...)
	lines := bytes.Split(data, []byte{'\n'})
	f.fragment = append([]byte(nil), lines[len(lines)-1]...)
	for _, line := range lines[:len(lines)-1] {
		if f.skip {
			f.skip = false
			continue
		}
		// Title entries are short top-level objects; the same words quoted
		// inside a tool result are escaped and fail to parse as one.
		if !bytes.Contains(line, []byte(`-title"`)) {
			continue
		}
		var e struct {
			Type        string `json:"type"`
			AITitle     string `json:"aiTitle"`
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch {
		case e.Type == "custom-title" && e.CustomTitle != "":
			f.custom = e.CustomTitle
		case e.Type == "ai-title" && e.AITitle != "":
			f.ai = e.AITitle
		}
	}
	if len(f.fragment) > maxFragment { // a huge tool result mid-write: no title in it
		f.fragment, f.skip = nil, true
	}
}

// findTranscript locates a session's transcript for sessions that never sent
// a hook (and so never said where it is).
func findTranscript(projectsDir, sessionID string) string {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\*?[`) {
		return ""
	}
	m, _ := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

// underDir reports whether path is inside dir, so a transcript path posted by
// a hook can't point the daemon at arbitrary files.
func underDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && filepath.IsAbs(path)
}
