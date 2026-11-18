package probe

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
)

func TestProbeManager_UDPResponseValidationAndHealth(t *testing.T) {
	tests := []struct {
		name        string
		response    func(*dns.Msg) ([]byte, bool)
		wantSuccess bool
	}{
		{name: "valid", wantSuccess: true, response: func(query *dns.Msg) ([]byte, bool) {
			return validDNSResponse(query), false
		}},
		{name: "wrong transaction ID", response: func(query *dns.Msg) ([]byte, bool) {
			response := new(dns.Msg)
			response.SetReply(query)
			response.Id++
			payload, _ := response.Pack()
			return payload, false
		}},
		{name: "query bit", response: func(query *dns.Msg) ([]byte, bool) {
			payload, _ := query.Pack()
			return payload, false
		}},
		{name: "truncated", response: func(query *dns.Msg) ([]byte, bool) {
			response := new(dns.Msg)
			response.SetReply(query)
			response.Truncated = true
			payload, _ := response.Pack()
			return payload, false
		}},
		{name: "missing question", response: func(query *dns.Msg) ([]byte, bool) {
			response := new(dns.Msg)
			response.SetReply(query)
			response.Question = nil
			payload, _ := response.Pack()
			return payload, false
		}},
		{name: "malformed", response: func(*dns.Msg) ([]byte, bool) {
			return []byte{0, 1, 2}, false
		}},
		{name: "wrong source", response: func(query *dns.Msg) ([]byte, bool) {
			return validDNSResponse(query), true
		}},
		{name: "timeout", response: func(*dns.Msg) ([]byte, bool) {
			return nil, false
		}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			server := newUDPProbeResponder(t, testCase.response)
			manager, entry, hash := newUDPProbeTestManager(t, server.target, "tcp", "udp")
			var probeEvents atomic.Int32
			manager.SetOnProbeEvent(func(kind string) {
				if kind == "udp" {
					probeEvents.Add(1)
				}
			})

			manager.probeUDP(hash, entry)
			if probeEvents.Load() != 1 || entry.LastUDPProbeAttempt.Load() == 0 {
				t.Fatalf("probe attempt not recorded: events=%d entry=%+v", probeEvents.Load(), entry)
			}
			if testCase.wantSuccess {
				if entry.LastUDPProbeSuccess.Load() == 0 || entry.UDPHealthState().FailureCount != 0 {
					t.Fatalf("successful health result: success=%d failures=%d",
						entry.LastUDPProbeSuccess.Load(), entry.UDPHealthState().FailureCount)
				}
				return
			}
			if entry.LastUDPProbeSuccess.Load() != 0 || entry.UDPHealthState().FailureCount != 1 {
				t.Fatalf("failed health result: success=%d failures=%d",
					entry.LastUDPProbeSuccess.Load(), entry.UDPHealthState().FailureCount)
			}
		})
	}
}

func TestProbeManager_StopDoesNotPenalizeBlockedUDPTask(t *testing.T) {
	server := newUDPProbeResponder(t, func(*dns.Msg) ([]byte, bool) { return nil, false })
	manager, entry, _ := newUDPProbeTestManager(t, server.target, "tcp", "udp")
	manager.Start()
	manager.TriggerImmediateUDPProbes()
	select {
	case <-server.requests:
	case <-time.After(time.Second):
		t.Fatal("UDP server did not receive worker probe")
	}

	manager.Stop()
	udpHealth := entry.UDPHealthState()
	if udpHealth.FailureCount != 0 || udpHealth.CircuitOpenSince != 0 {
		t.Fatalf("shutdown cancellation changed UDP health: failures=%d circuit=%d",
			udpHealth.FailureCount, udpHealth.CircuitOpenSince)
	}
}

func TestProbeManager_UDPProbeUsesRealSingBoxDirectOutbound(t *testing.T) {
	server := newUDPProbeResponder(t, func(query *dns.Msg) ([]byte, bool) {
		return validDNSResponse(query), false
	})
	manager, entry, hash := newUDPProbeTestManager(t, server.target, "tcp", "udp")
	builder, err := outbound.NewSingboxBuilderWithConfig(outbound.SingboxBuilderConfig{
		DNSUpstreams: []string{"local"},
	})
	if err != nil {
		t.Fatalf("create sing-box builder: %v", err)
	}
	t.Cleanup(func() { _ = builder.Close() })
	realOutbound, err := builder.Build(json.RawMessage(`{"type":"direct","tag":"udp-probe-real"}`))
	if err != nil {
		t.Fatalf("build direct outbound: %v", err)
	}
	t.Cleanup(func() {
		if closer, ok := realOutbound.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})
	entry.Outbound.Store(&realOutbound)

	manager.probeUDP(hash, entry)
	if entry.LastUDPProbeSuccess.Load() == 0 || entry.UDPHealthState().FailureCount != 0 {
		t.Fatalf("real sing-box UDP probe failed: success=%d failures=%d",
			entry.LastUDPProbeSuccess.Load(), entry.UDPHealthState().FailureCount)
	}
}

func TestProbeManager_StopCancelsBlockedUDPProbe(t *testing.T) {
	server := newUDPProbeResponder(t, func(*dns.Msg) ([]byte, bool) { return nil, false })
	manager, _, hash := newUDPProbeTestManager(t, server.target, "tcp", "udp")
	done := make(chan error, 1)
	go func() { done <- manager.performUDPProbe(hash, server.target()) }()
	select {
	case <-server.requests:
	case <-time.After(time.Second):
		t.Fatal("UDP server did not receive probe")
	}

	manager.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked probe error: got %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked UDP probe did not stop after manager cancellation")
	}
}

func TestProbeManager_UDPProbeUsesHotTarget(t *testing.T) {
	first := newUDPProbeResponder(t, func(query *dns.Msg) ([]byte, bool) {
		return validDNSResponse(query), false
	})
	second := newUDPProbeResponder(t, func(query *dns.Msg) ([]byte, bool) {
		return validDNSResponse(query), false
	})
	var target atomic.Value
	target.Store(first.target())
	manager, entry, _ := newUDPProbeTestManager(t, func() string { return target.Load().(string) }, "tcp", "udp")
	manager.Start()
	manager.TriggerImmediateUDPProbes()

	select {
	case <-first.requests:
	case <-time.After(time.Second):
		t.Fatal("initial UDP target did not receive probe")
	}
	target.Store(second.target())
	manager.TriggerImmediateUDPProbes()
	select {
	case <-second.requests:
	case <-time.After(time.Second):
		t.Fatal("hot UDP target did not receive immediate probe")
	}
	if entry.LastUDPProbeSuccess.Load() == 0 {
		t.Fatal("hot target probe did not record success")
	}
}

func TestProbeManager_UDPProbeSucceedsWhenSetDeadlineUnsupported(t *testing.T) {
	server := newUDPProbeResponder(t, func(query *dns.Msg) ([]byte, bool) {
		return validDNSResponse(query), false
	})
	manager, entry, hash := newUDPProbeTestManager(t, server.target, "tcp", "udp")
	var ob adapter.Outbound = &udpProbeTestOutbound{
		NoopOutbound: &testutil.NoopOutbound{},
		networks:     []string{"tcp", "udp"},
		wrap: func(conn net.PacketConn) net.PacketConn {
			return hysteriaLikePacketConn{PacketConn: conn}
		},
	}
	entry.Outbound.Store(&ob)

	manager.probeUDP(hash, entry)
	if entry.LastUDPProbeSuccess.Load() == 0 || entry.UDPHealthState().FailureCount != 0 {
		t.Fatalf("hy2-like UDP probe failed: success=%d failures=%d",
			entry.LastUDPProbeSuccess.Load(), entry.UDPHealthState().FailureCount)
	}
}

func TestProbeManager_UDPProbeTimesOutWithoutDeadlines(t *testing.T) {
	server := newUDPProbeResponder(t, func(*dns.Msg) ([]byte, bool) { return nil, false })
	manager, entry, hash := newUDPProbeTestManager(t, server.target, "tcp", "udp")
	var ob adapter.Outbound = &udpProbeTestOutbound{
		NoopOutbound: &testutil.NoopOutbound{},
		networks:     []string{"tcp", "udp"},
		wrap: func(conn net.PacketConn) net.PacketConn {
			return noDeadlinePacketConn{PacketConn: conn}
		},
	}
	entry.Outbound.Store(&ob)

	done := make(chan struct{})
	go func() {
		manager.probeUDP(hash, entry)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("UDP probe did not time out after unsupported deadlines")
	}
	if entry.LastUDPProbeSuccess.Load() != 0 || entry.UDPHealthState().FailureCount != 1 {
		t.Fatalf("timeout health: success=%d failures=%d",
			entry.LastUDPProbeSuccess.Load(), entry.UDPHealthState().FailureCount)
	}
}

func TestProbeManager_UDPProbeSkipDoesNotRecordHealth(t *testing.T) {
	server := newUDPProbeResponder(t, func(query *dns.Msg) ([]byte, bool) {
		return validDNSResponse(query), false
	})
	manager, entry, hash := newUDPProbeTestManager(t, server.target, "tcp", "udp")
	manager.outbound = func(node.Hash) (adapter.Outbound, error) {
		return nil, outbound.ErrSkipUDPProbe
	}

	manager.probeUDP(hash, entry)
	if entry.LastUDPProbeAttempt.Load() != 0 || entry.LastUDPProbeSuccess.Load() != 0 {
		t.Fatalf("skip recorded probe result: attempt=%d success=%d",
			entry.LastUDPProbeAttempt.Load(), entry.LastUDPProbeSuccess.Load())
	}
	if entry.UDPHealthState().FailureCount != 0 {
		t.Fatalf("skip recorded UDP failure: %+v", entry.UDPHealthState())
	}
	select {
	case <-server.requests:
		t.Fatal("skipped UDP probe still dialed the target")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTriggerImmediateUDPProbes_SkipsSingleStackNodes(t *testing.T) {
	server := newUDPProbeResponder(t, func(query *dns.Msg) ([]byte, bool) {
		return validDNSResponse(query), false
	})
	manager, _, _ := newUDPProbeTestManager(t, server.target, "tcp")
	manager.Start()
	manager.TriggerImmediateUDPProbes()
	select {
	case <-server.requests:
		t.Fatal("TCP-only node unexpectedly received a UDP probe")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSameAddrPort_UnmapsIPv4(t *testing.T) {
	left := netip.MustParseAddrPort("[::ffff:127.0.0.1]:53")
	right := netip.MustParseAddrPort("127.0.0.1:53")
	if !sameAddrPort(left, right) {
		t.Fatal("IPv4-mapped source should match canonical IPv4 target")
	}
}
