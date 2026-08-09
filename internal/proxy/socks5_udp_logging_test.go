package proxy

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

func TestSocks5UDPAssociationLogRetainsSingleTargetRoute(t *testing.T) {
	association, emitter := newLoggedUDPAssociation(t)
	route := udpLoggingTestRoute("platform-a", 1)
	association.recordRoute("example.com:53", route)
	if err := association.Close(); err != nil {
		t.Fatalf("close association: %v", err)
	}

	finished, entry := readUDPAssociationEvents(t, emitter)
	if entry.TargetHost != "example.com:53" || entry.PlatformID != route.PlatformID {
		t.Fatalf("single-target log metadata: %+v", entry)
	}
	if entry.NodeHash != route.NodeHash.Hex() || finished.PlatformID != route.PlatformID {
		t.Fatalf("single-target route metadata: finished=%+v log=%+v", finished, entry)
	}
}

func TestSocks5UDPAssociationLogClearsMetadataForMultipleTargets(t *testing.T) {
	association, emitter := newLoggedUDPAssociation(t)
	association.recordRoute("192.0.2.1:53", routing.RouteResult{})
	association.recordRoute("example.net:53", udpLoggingTestRoute("platform-b", 2))
	association.recordRoute("192.0.2.1:53", udpLoggingTestRoute("platform-c", 3))
	association.recordFailure(
		"udp_upstream_read",
		errors.New("second target failed"),
		ErrUpstreamRequestFailed,
	)
	if err := association.Close(); err != nil {
		t.Fatalf("close association: %v", err)
	}

	finished, entry := readUDPAssociationEvents(t, emitter)
	if entry.TargetHost != "" || entry.TargetURL != "" || finished.PlatformID != "" {
		t.Fatalf("multi-target identity was not cleared: finished=%+v log=%+v", finished, entry)
	}
	if entry.PlatformID != "" || entry.PlatformName != "" || entry.NodeHash != "" ||
		entry.NodeTag != "" || entry.EgressIP != "" {
		t.Fatalf("multi-target route was not cleared: %+v", entry)
	}
	if entry.UpstreamStage != "udp_upstream_read" || entry.UpstreamErrMsg == "" {
		t.Fatalf("association failure summary was lost: %+v", entry)
	}
}

func newLoggedUDPAssociation(t *testing.T) (*socks5UDPAssociation, *mockEventEmitter) {
	t.Helper()
	manager := newSocks5UDPAssociationManager(time.Minute)
	t.Cleanup(func() { _ = manager.close() })
	emitter := newMockEventEmitter()
	lifecycle := newRequestLifecycleFromMetadata(
		emitter, "198.51.100.40:48000", "UDP_ASSOCIATE", ProxyTypeSocks5Forward, true,
	)
	association, err := manager.register(socks5UDPAssociationOptions{
		endpointID:      "logging-scope",
		clientIP:        netip.MustParseAddr("198.51.100.40"),
		requestedSource: netip.MustParseAddrPort("0.0.0.0:48000"),
		lifecycle:       lifecycle,
	})
	if err != nil {
		t.Fatalf("register logged association: %v", err)
	}
	return association, emitter
}

func udpLoggingTestRoute(platformID string, hashByte byte) routing.RouteResult {
	return routing.RouteResult{
		PlatformID:   platformID,
		PlatformName: platformID + "-name",
		NodeHash:     node.Hash{hashByte},
		NodeTag:      platformID + "-tag",
		EgressIP:     netip.MustParseAddr("192.0.2.10"),
	}
}

func readUDPAssociationEvents(
	t *testing.T,
	emitter *mockEventEmitter,
) (RequestFinishedEvent, RequestLogEntry) {
	t.Helper()
	var finished RequestFinishedEvent
	var entry RequestLogEntry
	select {
	case finished = <-emitter.finishedCh:
	case <-time.After(time.Second):
		t.Fatal("missing UDP association finished event")
	}
	select {
	case entry = <-emitter.logCh:
	case <-time.After(time.Second):
		t.Fatal("missing UDP association log event")
	}
	return finished, entry
}
