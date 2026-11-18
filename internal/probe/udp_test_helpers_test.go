package probe

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
	"github.com/miekg/dns"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type udpProbeTestOutbound struct {
	*testutil.NoopOutbound
	networks []string
	wrap     func(net.PacketConn) net.PacketConn
}

func (o *udpProbeTestOutbound) Network() []string {
	return append([]string(nil), o.networks...)
}

func (o *udpProbeTestOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil || o.wrap == nil {
		return conn, err
	}
	return o.wrap(conn), nil
}

// hysteriaLikePacketConn matches sing-quic hy2: SetDeadline is invalid,
// SetReadDeadline works.
type hysteriaLikePacketConn struct {
	net.PacketConn
}

func (c hysteriaLikePacketConn) SetDeadline(time.Time) error {
	return os.ErrInvalid
}

func (c hysteriaLikePacketConn) SetWriteDeadline(time.Time) error {
	return os.ErrInvalid
}

type noDeadlinePacketConn struct {
	net.PacketConn
}

func (c noDeadlinePacketConn) SetDeadline(time.Time) error {
	return os.ErrInvalid
}

func (c noDeadlinePacketConn) SetReadDeadline(time.Time) error {
	return os.ErrInvalid
}

func (c noDeadlinePacketConn) SetWriteDeadline(time.Time) error {
	return os.ErrInvalid
}

type udpProbeResponder struct {
	conn     *net.UDPConn
	requests chan struct{}
	done     chan struct{}
	mu       sync.RWMutex
	handler  func(*dns.Msg) ([]byte, bool)
}

func newUDPProbeResponder(t *testing.T, handler func(*dns.Msg) ([]byte, bool)) *udpProbeResponder {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	server := &udpProbeResponder{
		conn: conn, requests: make(chan struct{}, 16), done: make(chan struct{}), handler: handler,
	}
	go server.serve()
	t.Cleanup(func() {
		_ = conn.Close()
		<-server.done
	})
	return server
}

func (s *udpProbeResponder) serve() {
	defer close(s.done)
	buffer := make([]byte, dns.MaxMsgSize)
	for {
		n, client, err := s.conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		select {
		case s.requests <- struct{}{}:
		default:
		}
		query := new(dns.Msg)
		if err := query.Unpack(buffer[:n]); err != nil {
			continue
		}
		s.mu.RLock()
		handler := s.handler
		s.mu.RUnlock()
		payload, alternateSource := handler(query)
		if payload == nil {
			continue
		}
		if alternateSource {
			alternate, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err == nil {
				_, _ = alternate.WriteToUDP(payload, client)
				_ = alternate.Close()
			}
			continue
		}
		_, _ = s.conn.WriteToUDP(payload, client)
	}
}

func (s *udpProbeResponder) target() string {
	return s.conn.LocalAddr().String()
}

func validDNSResponse(query *dns.Msg) []byte {
	response := new(dns.Msg)
	response.SetReply(query)
	payload, _ := response.Pack()
	return payload
}

func newUDPProbeTestManager(
	t *testing.T,
	target func() string,
	networks ...string,
) (*ProbeManager, *node.NodeEntry, node.Hash) {
	t.Helper()
	subManager := topology.NewSubscriptionManager()
	sub := subscription.NewSubscription("sub-udp-probe", "UDP Probe", "local", true, false)
	subManager.Register(sub)
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subManager.Lookup,
		MaxLatencyTableEntries: 4,
		MaxConsecutiveFailures: func() int { return 2 },
	})
	raw := json.RawMessage(`{"type":"test-udp-probe"}`)
	hash := node.HashFromRawOptions(raw)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"udp"}})
	pool.AddNodeFromSub(hash, raw, sub.ID)
	entry, _ := pool.GetEntry(hash)
	var outbound adapter.Outbound = &udpProbeTestOutbound{
		NoopOutbound: &testutil.NoopOutbound{}, networks: networks,
	}
	entry.Outbound.Store(&outbound)

	manager := NewProbeManager(ProbeConfig{
		Pool:               pool,
		Concurrency:        1,
		MaxUDPTestInterval: func() time.Duration { return time.Hour },
		UDPProbeTarget:     target,
		Timeout:            func() time.Duration { return 150 * time.Millisecond },
	})
	t.Cleanup(manager.Stop)
	return manager, entry, hash
}
