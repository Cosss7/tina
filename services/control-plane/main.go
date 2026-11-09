package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"time"
)

const version = "0.1.0"

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:7801", "Listen address")
	flag.Parse()

	hub := NewHub()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			hub.Sweep()
		}
	}()

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("Embedded web assets missing: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "version": version})
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, hub.Snapshot())
	})
	mux.HandleFunc("GET /ws/node", hub.ServeNodeWS)
	mux.HandleFunc("GET /api/board", hub.board)
	mux.HandleFunc("POST /api/cards", hub.createCard)
	mux.HandleFunc("POST /api/plans", hub.requestPlan)
	mux.HandleFunc("POST /api/plans/{id}/decide", hub.decidePlan)
	mux.HandleFunc("POST /api/tasks/{id}/dispatch", hub.dispatchTask)
	mux.HandleFunc("POST /api/tasks/{id}/review", hub.reviewTask)
	mux.Handle("GET /", http.FileServerFS(static))

	log.Printf("Control plane listening on http://%s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("Control plane stopped: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("Failed to write response: %v", err)
	}
}
