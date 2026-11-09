package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
	"github.com/Cosss7/tina/services/tinad/internal/engine/dsh"
	"github.com/Cosss7/tina/services/tinad/internal/engine/fake"
	"github.com/Cosss7/tina/services/tinad/internal/runtime"
)

func main() {
	configPath := flag.String("config", ".tina/tinad.json", "Local node configuration (includes private enrollment token)")
	flag.Parse()
	file, err := os.Open(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	var config struct {
		runtime.Config
		Dsh dsh.Options `json:"dsh"`
	}
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
	log.Printf("tinad %s connecting to control plane", config.NodeID)
	runErr := node.Run(ctx)
	if err := node.Close(); err != nil {
		log.Printf("tinad cleanup: %v", err)
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Printf("tinad stopped: %v", runErr)
		os.Exit(1)
	}
}
