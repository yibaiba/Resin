package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStartBackgroundBatch2QueuesRecoveryBeforeCleaner(t *testing.T) {
	var order []string
	hooks := recordingBackgroundBatch2Hooks(&order, nil)
	if err := startBackgroundBatch2(hooks); err != nil {
		t.Fatalf("startBackgroundBatch2: %v", err)
	}
	want := []string{"probe", "recovery", "udp", "requestlog", "lease", "ephemeral"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("startup order = %v, want %v", order, want)
	}
}

func TestStartBackgroundBatch2StopsAfterRecoveryError(t *testing.T) {
	var order []string
	recoveryErr := errors.New("validation queue unavailable")
	err := startBackgroundBatch2(recordingBackgroundBatch2Hooks(&order, recoveryErr))
	if err == nil || !strings.Contains(err.Error(), recoveryErr.Error()) {
		t.Fatalf("error = %v, want recovery error", err)
	}
	want := []string{"probe", "recovery"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("startup order = %v, want %v", order, want)
	}
}

func recordingBackgroundBatch2Hooks(order *[]string, recoveryErr error) backgroundBatch2Hooks {
	record := func(name string) func() {
		return func() { *order = append(*order, name) }
	}
	return backgroundBatch2Hooks{
		startProbe: record("probe"),
		enqueueTCPRecovery: func() error {
			*order = append(*order, "recovery")
			return recoveryErr
		},
		triggerUDP:            record("udp"),
		startRequestLog:       record("requestlog"),
		startLeaseCleaner:     record("lease"),
		startEphemeralCleaner: record("ephemeral"),
	}
}
