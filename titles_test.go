package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestTitles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	titles := NewTitles()
	if got := titles.Title(path); got != "" {
		t.Fatalf("no file: got %q", got)
	}

	appendTo(t, path, `{"type":"user","message":"hi"}`+"\n"+
		`{"type":"ai-title","aiTitle":"First guess","sessionId":"s"}`+"\n")
	if got := titles.Title(path); got != "First guess" {
		t.Fatalf("got %q, want First guess", got)
	}

	// Only what's appended since is read; the newest ai-title wins. A line
	// can arrive in two pieces.
	appendTo(t, path, `{"type":"ai-title","aiTi`)
	titles.Title(path)
	appendTo(t, path, `tle":"KECTASK-0883 merge status"}`+"\n")
	if got := titles.Title(path); got != "KECTASK-0883 merge status" {
		t.Fatalf("got %q", got)
	}

	// The same words quoted inside a tool result aren't a title.
	appendTo(t, path, `{"type":"user","toolUseResult":"{\"type\":\"ai-title\",\"aiTitle\":\"not me\"}"}`+"\n")
	if got := titles.Title(path); got != "KECTASK-0883 merge status" {
		t.Fatalf("quoted title taken: got %q", got)
	}

	// A /rename title wins over later automatic ones.
	appendTo(t, path, `{"type":"custom-title","customTitle":"Billing bug","sessionId":"s"}`+"\n"+
		`{"type":"ai-title","aiTitle":"Later guess","sessionId":"s"}`+"\n")
	if got := titles.Title(path); got != "Billing bug" {
		t.Fatalf("got %q, want the custom title", got)
	}

	// Rewritten shorter: start over.
	if err := os.WriteFile(path, []byte(`{"type":"ai-title","aiTitle":"Fresh"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := titles.Title(path); got != "Fresh" {
		t.Fatalf("after rewrite: got %q, want Fresh", got)
	}
}

func TestTitlesReadsOnlyTheTailOfBigFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	head := `{"type":"custom-title","customTitle":"Too far back"}` + "\n"
	filler := `{"type":"assistant","text":"` + strings.Repeat("x", 1000) + `"}` + "\n"
	appendTo(t, path, head+strings.Repeat(filler, titleTail/len(filler)+10)+
		`{"type":"ai-title","aiTitle":"Near the end"}`+"\n")
	if got := NewTitles().Title(path); got != "Near the end" {
		t.Fatalf("got %q, want Near the end", got)
	}
}

func TestTranscriptPaths(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "-home-tiger-app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(proj, "abc-123.jsonl")
	appendTo(t, want, "")
	if got := findTranscript(dir, "abc-123"); got != want {
		t.Errorf("findTranscript = %q, want %q", got, want)
	}
	if got := findTranscript(dir, "*"); got != "" {
		t.Errorf("findTranscript(*) = %q, want nothing", got)
	}
	for path, want := range map[string]bool{
		want:                                true,
		filepath.Join(dir, "../etc/passwd"): false,
		"/etc/passwd":                       false,
		"relative/x.jsonl":                  false,
	} {
		if got := underDir(path, dir); got != want {
			t.Errorf("underDir(%q) = %v, want %v", path, got, want)
		}
	}
}
