package service

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

func TestLiveTCPSelectionReadyRejectsIncompleteChain(t *testing.T) {
	if liveTCPSelectionReady(nil) {
		t.Fatal("nil route is not ready")
	}
	if liveTCPSelectionReady(&routing.RouteDiagnostic{Outcome: "selected"}) {
		t.Fatal("missing selection is not ready")
	}
	incomplete := &routing.RouteDiagnostic{
		Outcome: "no_available_nodes",
		Selection: &routing.DiagnosticSelection{
			NodeHash: "land",
			Chain:    []routing.DiagnosticChainHop{{PlatformID: "hop"}},
		},
	}
	if liveTCPSelectionReady(incomplete) {
		t.Fatal("failed hop attach is not ready")
	}
	ready := &routing.RouteDiagnostic{
		Outcome: "selected",
		Selection: &routing.DiagnosticSelection{
			NodeHash: "land",
			Chain:    []routing.DiagnosticChainHop{{PlatformID: "hop", NodeHash: "abc"}},
		},
	}
	if !liveTCPSelectionReady(ready) {
		t.Fatal("complete chain should be ready")
	}
}

func TestDiagnosticSelectionHopsParsesChain(t *testing.T) {
	hop := node.HashFromRawOptions([]byte(`{"hop":1}`))
	hops, err := diagnosticSelectionHops(&routing.DiagnosticSelection{
		Chain: []routing.DiagnosticChainHop{{NodeHash: hop.Hex()}},
	})
	if err != nil || len(hops) != 1 || hops[0] != hop {
		t.Fatalf("hops=%v err=%v", hops, err)
	}
	if hops, err := diagnosticSelectionHops(&routing.DiagnosticSelection{}); err != nil || hops != nil {
		t.Fatalf("empty chain hops=%v err=%v", hops, err)
	}
	if _, err := diagnosticSelectionHops(&routing.DiagnosticSelection{
		Chain: []routing.DiagnosticChainHop{{NodeHash: "not-a-hash"}},
	}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLiveTCPTimeoutDefaults(t *testing.T) {
	unchained := &routing.RouteDiagnostic{
		Selection: &routing.DiagnosticSelection{NodeHash: "land"},
	}
	timeout, err := liveTCPTimeout("", unchained)
	if err != nil || timeout != defaultDiagnosticTCPTimeout {
		t.Fatalf("unchained timeout=%v err=%v, want %s", timeout, err, defaultDiagnosticTCPTimeout)
	}
	chained := &routing.RouteDiagnostic{
		Selection: &routing.DiagnosticSelection{
			NodeHash: "land",
			Chain:    []routing.DiagnosticChainHop{{PlatformID: "hop", NodeHash: "abc"}},
		},
	}
	timeout, err = liveTCPTimeout("", chained)
	if err != nil || timeout != chainedDiagnosticTCPTimeout {
		t.Fatalf("chained timeout=%v err=%v, want %s", timeout, err, chainedDiagnosticTCPTimeout)
	}
	timeout, err = liveTCPTimeout("8s", chained)
	if err != nil || timeout != 8*time.Second {
		t.Fatalf("explicit timeout=%v err=%v, want 8s", timeout, err)
	}
}
