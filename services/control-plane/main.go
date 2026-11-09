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
	"path/filepath"
	"strings"
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
	importH0 := flag.String("import-h0", "", "Import a stopped H0 SQLite database and exit")
	oldStopped := flag.Bool("h0-stopped", false, "Confirm that the H0 execution service was stopped before import")
	backupDirectory := flag.String("backup-dir", "", "H0 snapshot directory (defaults beside the destination database)")
	enrollConfig := flag.String("enroll-config", "", "Enroll node identity from a private tinad JSON configuration and exit")
	flag.Parse()
	projects, err := project.Open(*dbPath)
	if err != nil {
		log.Fatalf("Open project store %q: %v", *dbPath, err)
	}
	defer projects.Close()
	if *importH0 != "" {
		directory := *backupDirectory
		if directory == "" {
			directory = filepath.Join(filepath.Dir(*dbPath), "migration-backups")
		}
		result, err := projects.ImportH0(context.Background(), *importH0, directory, *oldStopped)
		if err != nil {
			log.Fatalf("Import H0: %v", err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *enrollConfig != "" {
		data, err := os.ReadFile(*enrollConfig)
		if err != nil {
			log.Fatal(err)
		}
		var config struct {
			NodeID string `json:"nodeId"`
			Name   string `json:"name"`
			Token  string `json:"token"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			log.Fatal(err)
		}
		if err := projects.EnrollNode(context.Background(), config.NodeID, config.Name, strings.TrimSpace(config.Token)); err != nil {
			log.Fatal(err)
		}
		log.Printf("Enrolled node %s", config.NodeID)
		return
	}
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
