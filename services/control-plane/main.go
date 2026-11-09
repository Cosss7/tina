package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Cosss7/tina/services/control-plane/internal/project"
)

const version = "0.1.0"

//go:embed web
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:7801", "Listen address")
	defaultDB := os.Getenv("TINA_CONTROL_DB")
	if defaultDB == "" {
		defaultDB = ".tina/control-plane.db"
	}
	dbPath := flag.String("db", defaultDB, "Control-plane database path (:memory: for tests)")
	seedDemo := flag.Bool("seed-demo", true, "Initialize demo projects when the project store is empty")
	flag.Parse()
	projects, err := project.Open(*dbPath)
	if err != nil {
		log.Fatalf("Open project store %q: %v", *dbPath, err)
	}
	defer projects.Close()
	if *seedDemo {
		if err := projects.SeedDemo(context.Background()); err != nil {
			log.Fatalf("Initialize projects: %v", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := NewHub()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				hub.Sweep()
			case <-ctx.Done():
				return
			}
		}
	}()

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("Embedded web assets missing: %v", err)
	}

	server := &http.Server{Addr: *addr, Handler: newHandler(projects, hub, static), ReadHeaderTimeout: 5 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Shutdown: %v", err)
		}
	}()
	log.Printf("Control plane listening on http://%s; project database: %s", *addr, *dbPath)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Control plane stopped: %v", err)
	}
	<-shutdownDone
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("Failed to write response: %v", err)
	}
}
