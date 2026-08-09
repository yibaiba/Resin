package service

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/testutil"
)

func TestNodeEntryToSummary_UDPDiagnostics(t *testing.T) {
	hash := node.HashFromRawOptions([]byte(`{"type":"direct"}`))
	entry := node.NewNodeEntry(hash, nil, time.Now(), 0)
	outbound := testutil.NewNoopOutbound()
	entry.Outbound.Store(&outbound)

	service := &ControlPlaneService{}
	unknown := service.nodeEntryToSummary(hash, entry)
	if !unknown.SupportsUDP || unknown.UDPHealth != node.UDPHealthUnknown {
		t.Fatalf("unknown summary: %+v", unknown)
	}
	if unknown.UDPFailureCount != 0 || unknown.UDPCircuitOpenSince != nil {
		t.Fatalf("unknown health fields: %+v", unknown)
	}

	attempt := time.Now().Add(-time.Second).UnixNano()
	success := time.Now().UnixNano()
	entry.LastUDPProbeAttempt.Store(attempt)
	entry.LastUDPProbeSuccess.Store(success)
	healthy := service.nodeEntryToSummary(hash, entry)
	if healthy.UDPHealth != node.UDPHealthHealthy ||
		healthy.LastUDPProbeAttempt == "" || healthy.LastUDPProbeSuccess == "" {
		t.Fatalf("healthy summary: %+v", healthy)
	}

	entry.RestoreUDPHealth(3, time.Now().UnixNano())
	open := service.nodeEntryToSummary(hash, entry)
	if open.UDPHealth != node.UDPHealthCircuitOpen || open.UDPFailureCount != 3 ||
		open.UDPCircuitOpenSince == nil {
		t.Fatalf("circuit-open summary: %+v", open)
	}
}
