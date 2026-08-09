package proxy

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type scriptedUDPOutbound struct {
	adapter.Outbound
	mu          sync.Mutex
	packetConns []net.PacketConn
	calls       atomic.Int32
}

func (o *scriptedUDPOutbound) Network() []string {
	return []string{"tcp", "udp"}
}

func (o *scriptedUDPOutbound) ListenPacket(
	context.Context,
	M.Socksaddr,
) (net.PacketConn, error) {
	o.calls.Add(1)
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.packetConns) == 0 {
		return nil, errors.New("scripted UDP connection exhausted")
	}
	packetConn := o.packetConns[0]
	o.packetConns = o.packetConns[1:]
	return packetConn, nil
}

type controlledPacketConn struct {
	writeErr       error
	readErr        error
	readAfterWrite chan struct{}
	closed         chan struct{}
	writeOnce      sync.Once
	closeOnce      sync.Once
}

type blockingWritePacketConn struct {
	writeStarted chan struct{}
	closed       chan struct{}
	writeOnce    sync.Once
	closeOnce    sync.Once
	closes       atomic.Int32
}

func newBlockingWritePacketConn() *blockingWritePacketConn {
	return &blockingWritePacketConn{
		writeStarted: make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

func (c *blockingWritePacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	<-c.closed
	return 0, nil, net.ErrClosed
}

func (c *blockingWritePacketConn) WriteTo([]byte, net.Addr) (int, error) {
	c.writeOnce.Do(func() { close(c.writeStarted) })
	<-c.closed
	return 0, net.ErrClosed
}

func (c *blockingWritePacketConn) Close() error {
	c.closeOnce.Do(func() {
		c.closes.Add(1)
		close(c.closed)
	})
	return nil
}

func (c *blockingWritePacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
}

func (c *blockingWritePacketConn) SetDeadline(time.Time) error      { return nil }
func (c *blockingWritePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *blockingWritePacketConn) SetWriteDeadline(time.Time) error { return nil }

func newControlledPacketConn(writeErr, readErr error) *controlledPacketConn {
	return &controlledPacketConn{
		writeErr:       writeErr,
		readErr:        readErr,
		readAfterWrite: make(chan struct{}),
		closed:         make(chan struct{}),
	}
}

func (c *controlledPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	if c.readErr != nil {
		select {
		case <-c.readAfterWrite:
			return 0, nil, c.readErr
		case <-c.closed:
			return 0, nil, net.ErrClosed
		}
	}
	<-c.closed
	return 0, nil, net.ErrClosed
}

func (c *controlledPacketConn) WriteTo(payload []byte, _ net.Addr) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	c.writeOnce.Do(func() { close(c.readAfterWrite) })
	return len(payload), nil
}

func (c *controlledPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *controlledPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
}

func (c *controlledPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *controlledPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *controlledPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestSocks5UDPFlowTransportFailureStageAndRebuild(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		stage    string
		writeErr error
		readErr  error
	}{
		{
			name: "upstream write", stage: "udp_upstream_write",
			writeErr: errors.New("scripted UDP write failure"),
		},
		{
			name: "upstream read", stage: "udp_upstream_read",
			readErr: errors.New("scripted UDP read failure"),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			first := newControlledPacketConn(testCase.writeErr, testCase.readErr)
			second := newControlledPacketConn(nil, nil)
			outbound := &scriptedUDPOutbound{packetConns: []net.PacketConn{first, second}}
			routeEnv := newProxyE2EEnv(t)
			replaceProxyE2EOutbound(t, routeEnv, outbound)
			health := &mockPassiveHealthRecorder{done: make(chan struct{}, 2)}
			emitter := newMockEventEmitter()
			inbound := NewSocks5Inbound(Socks5InboundConfig{
				Router: routeEnv.router, Pool: routeEnv.pool, Health: health, Events: emitter,
			})
			t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
			clientConn := listenLocalUDP(t)
			defer clientConn.Close()
			association := registerRelayTestAssociation(
				t, inbound, "failure-stage-scope", clientConn, "plat", emitter,
			)
			target := "127.0.0.1:53"
			packet, err := encodeSocks5UDPDatagram(target, []byte("request"))
			if err != nil {
				t.Fatalf("encode UDP datagram: %v", err)
			}
			relayPacket := socks5UDPInboundPacket{
				ctx: context.Background(), associationScope: "failure-stage-scope",
				client: &socks5UDPClientWriter{packetConn: clientConn},
				source: clientConn.LocalAddr(), packet: packet,
			}

			inbound.handleUDPDatagram(relayPacket)
			select {
			case <-health.done:
			case <-time.After(time.Second):
				t.Fatal("UDP transport failure was not recorded")
			}
			if _, exists := association.flow(target); exists {
				t.Fatal("failed UDP flow remained installed")
			}
			if got := outbound.calls.Load(); got != 1 {
				t.Fatalf("same-packet flow creation calls: got %d, want 1", got)
			}

			inbound.handleUDPDatagram(relayPacket)
			if got := outbound.calls.Load(); got != 2 {
				t.Fatalf("next-packet flow creation calls: got %d, want 2", got)
			}
			if _, exists := association.flow(target); !exists {
				t.Fatal("next UDP packet did not rebuild the failed flow")
			}
			if err := association.Close(); err != nil {
				t.Fatalf("close association: %v", err)
			}
			select {
			case entry := <-emitter.logCh:
				if entry.UpstreamStage != testCase.stage {
					t.Fatalf("failure stage: got %q, want %q", entry.UpstreamStage, testCase.stage)
				}
			case <-time.After(time.Second):
				t.Fatal("UDP failure request log was not emitted")
			}
			if got := health.passiveCalls.Load(); got != 1 {
				t.Fatalf("passive failure calls: got %d, want 1", got)
			}
		})
	}
}

func TestSocks5UDPAssociationScopeCloseUnblocksUpstreamWrite(t *testing.T) {
	packetConn := newBlockingWritePacketConn()
	outbound := &scriptedUDPOutbound{packetConns: []net.PacketConn{packetConn}}
	routeEnv := newProxyE2EEnv(t)
	replaceProxyE2EOutbound(t, routeEnv, outbound)
	health := &mockPassiveHealthRecorder{done: make(chan struct{}, 1)}
	emitter := newMockEventEmitter()
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		Router: routeEnv.router, Pool: routeEnv.pool, Health: health, Events: emitter,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	clientConn := listenLocalUDP(t)
	defer clientConn.Close()
	const scope = "blocked-write-scope"
	registerRelayTestAssociation(t, inbound, scope, clientConn, "plat", emitter)
	target := "127.0.0.1:53"
	packet, err := encodeSocks5UDPDatagram(target, []byte("request"))
	if err != nil {
		t.Fatalf("encode UDP datagram: %v", err)
	}
	handleDone := make(chan struct{})
	go func() {
		defer close(handleDone)
		inbound.handleUDPDatagram(socks5UDPInboundPacket{
			ctx: context.Background(), associationScope: scope,
			client: &socks5UDPClientWriter{packetConn: clientConn},
			source: clientConn.LocalAddr(), packet: packet,
		})
	}()

	select {
	case <-packetConn.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream UDP write did not start")
	}
	if err := inbound.CloseUDPAssociationScope(scope); err != nil {
		t.Fatalf("close association scope: %v", err)
	}
	select {
	case <-handleDone:
	case <-time.After(time.Second):
		t.Fatal("association scope close did not unblock upstream UDP write")
	}
	if got := packetConn.closes.Load(); got != 1 {
		t.Fatalf("upstream packet connection close count: got %d, want 1", got)
	}
	if got := health.passiveCalls.Load(); got != 0 {
		t.Fatalf("shutdown-induced UDP failures: got %d, want 0", got)
	}
	select {
	case entry := <-emitter.logCh:
		if entry.UpstreamStage != "" {
			t.Fatalf("shutdown-induced upstream stage: got %q", entry.UpstreamStage)
		}
	case <-time.After(time.Second):
		t.Fatal("association scope close did not finish request lifecycle")
	}
}
