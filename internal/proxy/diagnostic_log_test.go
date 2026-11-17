package proxy

import (
	"strings"
	"testing"

	"github.com/Resinat/Resin/internal/routing"
	"github.com/google/uuid"
)

func TestProxyFailureLineIncludesCorrelationAndTunnelDetails(t *testing.T) {
	feedback := newRequestHealthFeedback(&mockHealthRecorder{}, testRoute(), "request-123")
	feedback.record(passiveHealthSuccess)
	lifecycle := &requestLifecycle{
		finished: RequestFinishedEvent{IsConnect: true},
		log: RequestLogEntry{
			ID:                  "request-123",
			ProxyType:           ProxyTypeForward,
			ClientIP:            "192.0.2.1",
			Account:             "private-account",
			TargetHost:          "example.com:443",
			TargetURL:           "https://user:pass@example.com/private?token=secret",
			PlatformName:        "platform",
			NodeHash:            "abc123",
			NodeTag:             "tag\nforged=value",
			DurationNs:          20,
			FirstByteDurationNs: 10,
			UpstreamStage:       "connect_upstream_to_client_copy",
			UpstreamErrKind:     "connection_reset",
			UpstreamErrno:       "ECONNRESET",
			UpstreamErrMsg:      "reset\npassword=hunter2",
		},
		healthFeedback: feedback,
		tunnelRelay: &tunnelRelayResult{
			firstEnded: tunnelDirectionEgress,
			ingress: tunnelDirectionResult{
				bytes: 7, endOrder: 2, operation: tunnelIORead,
				errorKind: "connection_reset", errno: "ECONNRESET", errorMessage: "token=abc",
			},
			egress: tunnelDirectionResult{
				bytes: 11, endOrder: 1, operation: tunnelIORead, errorKind: "eof", errorMessage: "EOF",
			},
		},
	}

	line := proxyFailureLine(lifecycle)
	for _, expected := range []string{
		`[proxy-connect]`, `request_id="request-123"`, `health_attribution="success"`,
		`first_ended="client_to_upstream"`, `upstream_to_client_end_order="2"`,
		`client_to_upstream_bytes="11"`, `errno="ECONNRESET"`,
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("line missing %q: %s", expected, line)
		}
	}
	for _, forbidden := range []string{"192.0.2.1", "private-account", "TargetURL", "hunter2", "token=abc", "\n"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("line contains forbidden value %q: %s", forbidden, line)
		}
	}
}

func TestProxyFailureLineIncludesNoAvailableNodes(t *testing.T) {
	lifecycle := &requestLifecycle{
		finished: RequestFinishedEvent{IsConnect: true},
		log: RequestLogEntry{
			ID:         "request-empty-node",
			ProxyType:  ProxyTypeSocks5Forward,
			TargetHost: "example.com:443",
		},
	}
	lifecycle.setRouteFailure(ErrNoAvailableNodes)

	line := proxyFailureLine(lifecycle)
	for _, expected := range []string{
		`[proxy-connect]`,
		`resin_error="NO_AVAILABLE_NODES"`,
		`error="NO_AVAILABLE_NODES"`,
		`stage="route"`,
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("line missing %q: %s", expected, line)
		}
	}
}

func TestRequestLifecycleGeneratesStableUUID(t *testing.T) {
	emitter := newMockEventEmitter()
	lifecycle := newRequestLifecycleFromMetadata(emitter, "192.0.2.1:1234", "GET", ProxyTypeForward, false)
	if lifecycle.log.ID == "" {
		t.Fatal("request ID is empty")
	}
	if _, err := uuid.Parse(lifecycle.log.ID); err != nil {
		t.Fatalf("request ID = %q: %v", lifecycle.log.ID, err)
	}
	lifecycle.setNetOK(true)
	lifecycle.finish()

	entry := <-emitter.logCh
	if entry.ID != lifecycle.log.ID {
		t.Fatalf("emitted ID = %q, lifecycle ID = %q", entry.ID, lifecycle.log.ID)
	}
}

func testRoute() routing.RouteResult {
	return routing.RouteResult{}
}
