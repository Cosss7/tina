package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
	"github.com/Cosss7/tina/services/tinad/internal/engine/dsh"
	"github.com/Cosss7/tina/services/tinad/internal/engine/fake"
	"github.com/Cosss7/tina/services/tinad/internal/runtime"
)

type fileConfig struct {
	runtime.Config
	Dsh          dsh.Options `json:"dsh"`
	AutoProjects bool        `json:"autoProjects,omitempty"`
	Stopping     bool        `json:"stopping,omitempty"`
}

func main() {
	configPath := flag.String("config", ".tina/tinad.json", "Local node configuration (includes private enrollment token)")
	flag.Parse()
	file, err := os.Open(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	var config fileConfig
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&config)
	file.Close()
	if err != nil {
		log.Fatalf("Read tinad configuration: %v", err)
	}
	registry := &engine.Registry{}
	fakeDriver := fake.NewScripted()
	if err := registry.Register(fakeDriver); err != nil {
		log.Fatal(err)
	}
	engines := []p.EngineRegistration{{ID: "fake", Version: "scripted-v1", Capabilities: fakeDriver.Definition().Capabilities}}
	if len(config.Dsh.Command) > 0 {
		driver := &dsh.Driver{Options: config.Dsh}
		if err := registry.Register(driver); err != nil {
			log.Fatal(err)
		}
		engines = append(engines, p.EngineRegistration{ID: "dsh", Version: "stdio-v1", Capabilities: driver.Definition().Capabilities})
	}
	node, err := runtime.New(config.Config, registry, engines)
	if err != nil {
		log.Fatalf("Initialize tinad: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		var seen string
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			data, err := os.ReadFile(*configPath)
			if err != nil || string(data) == seen {
				continue
			}
			seen = string(data)
			var next fileConfig
			if err := json.Unmarshal(data, &next); err != nil {
				log.Printf("Read updated authorization: %v", err)
				continue
			}
			if next.Stopping {
				stop()
				return
			}
			if next.NodeID != config.NodeID || next.Name != config.Name || next.URL != config.URL || next.Token != config.Token || next.Database != config.Database || next.MaxConcurrent != config.MaxConcurrent || !reflect.DeepEqual(next.Dsh, config.Dsh) {
				log.Printf("Node identity, connection or engine launch changes require a restart")
				continue
			}
			if err := node.UpdateAuthorization(next.Roots, next.Agents); err != nil {
				log.Printf("Update local authorization: %v", err)
			}
		}
	}()
	log.Printf("tinad %s connecting to control plane", config.NodeID)
	linkCtx, cancelLink := context.WithCancel(context.Background())
	linkDone := make(chan error, 1)
	go func() { linkDone <- node.Run(linkCtx) }()
	var runErr error
	linkStopped := false
	select {
	case <-ctx.Done():
	case runErr = <-linkDone:
		linkStopped = true
	}
	if err := node.Quiesce(); err != nil {
		log.Printf("Stop local execution: %v", err)
	}
	if !linkStopped {
		flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := node.Flush(flushCtx); err != nil {
			log.Printf("Unacknowledged facts retained for reconnect: %v", err)
		}
		cancel()
	}
	cancelLink()
	if !linkStopped {
		runErr = <-linkDone
	}
	if err := node.Close(); err != nil {
		log.Printf("tinad cleanup: %v", err)
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Printf("tinad stopped: %v", runErr)
		os.Exit(1)
	}
}
