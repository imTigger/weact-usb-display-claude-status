package main

import (
	"encoding/json"
	"log"
	"mime"
	"net"
	"net/http"
	"time"
)

// NewServer takes Claude Code HTTP hooks on POST /event, turns the panel on
// POST /flip, and serves a debug dump on GET /state.
//
// Every /event answer is 200 with an empty body, which hooks treat as "no
// decision": this server must never approve or deny a PermissionRequest.
func NewServer(t *Tracker, d *Display) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /event", func(w http.ResponseWriter, r *http.Request) {
		if !isJSON(w, r) {
			return
		}
		var ev HookEvent
		// Payloads can carry whole files (Write's tool_input); only a few
		// small fields are kept.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&ev); err != nil {
			log.Printf("event: bad payload: %v", err)
		} else {
			t.Apply(ev, time.Now())
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /flip", func(w http.ResponseWriter, r *http.Request) {
		if !isJSON(w, r) {
			return
		}
		res, err := d.Flip()
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"view":     t.View(time.Now()),
			"sessions": t.Snapshot(),
		})
	})
	return loopbackOnly(mux)
}

// isJSON rejects anything but application/json. Browsers can't send that
// cross-origin without a preflight, which we never answer, so web pages can't
// post here.
func isJSON(w http.ResponseWriter, r *http.Request) bool {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/json" {
		http.Error(w, "want application/json", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// loopbackOnly rejects requests whose Host isn't a loopback address, which
// stops DNS-rebinding pages from reading /state.
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
