package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
)

func TestResinAppShutdownStepOrder(t *testing.T) {
	steps := (&resinApp{}).shutdownSteps()
	got := make([]string, 0, len(steps))
	for _, step := range steps {
		got = append(got, step.name)
	}
	want := []string{
		"endpoints",
		"lease cleaner",
		"ephemeral cleaner",
		"subscription scheduler",
		"probe manager",
		"GeoIP service",
		"outbound transport pool",
		"outbound manager",
		"sing-box services",
		"request log service",
		"request log repo",
		"metrics manager",
		"metrics DB",
		"cache flush worker",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown order:\n got: %v\nwant: %v", got, want)
	}
}

func TestRunShutdownStepsContinuesAndAggregatesErrors(t *testing.T) {
	firstErr := errors.New("first close")
	lastErr := errors.New("last close")
	var order []string
	steps := []shutdownStep{
		{name: "first", run: recordingShutdownStep(&order, "first", firstErr)},
		{name: "middle", run: recordingShutdownStep(&order, "middle", nil)},
		{name: "last", run: recordingShutdownStep(&order, "last", lastErr)},
	}

	err := runShutdownSteps(context.Background(), steps)
	if !errors.Is(err, firstErr) || !errors.Is(err, lastErr) {
		t.Fatalf("runShutdownSteps() error: got %v, want both errors", err)
	}
	if want := []string{"first", "middle", "last"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("execution order: got %v, want %v", order, want)
	}
}

func TestResinAppShutdownIsNilSafe(t *testing.T) {
	if err := (&resinApp{}).shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown() error: %v", err)
	}
}

func TestCleanupFailedInitializationClosesPublishedOutbounds(t *testing.T) {
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		GeoLookup:              func(netip.Addr) string { return "" },
		MaxConsecutiveFailures: func() int { return 3 },
	})
	raw := json.RawMessage(`{"type":"startup-partial"}`)
	entry := node.NewNodeEntry(node.HashFromRawOptions(raw), raw, time.Now(), 0)
	pool.LoadNodeFromBootstrap(entry)
	manager := outbound.NewOutboundManager(pool, &testutil.StubOutboundBuilder{})
	manager.EnsureNodeOutbound(entry.Hash)
	if !entry.HasOutbound() {
		t.Fatal("setup: expected a published outbound")
	}

	initErr := errors.New("later initialization failed")
	app := &resinApp{topoRuntime: &topologyRuntime{outboundMgr: manager}}
	err := cleanupFailedInitialization(app, initErr)
	if !errors.Is(err, initErr) {
		t.Fatalf("cleanup error: got %v, want initialization error", err)
	}
	if entry.HasOutbound() {
		t.Fatal("expected partial initialization cleanup to clear outbound")
	}
	manager.EnsureNodeOutbound(entry.Hash)
	if entry.HasOutbound() {
		t.Fatal("expected cleaned-up manager to reject later builds")
	}
}

func recordingShutdownStep(order *[]string, name string, err error) func(context.Context) error {
	return func(context.Context) error {
		*order = append(*order, name)
		return err
	}
}
