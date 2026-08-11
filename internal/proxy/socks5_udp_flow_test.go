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
	closes         atomic.Int32
}

type blockingWritePacketConn struct {
	writeStarted chan struct{}
	closed       chan struct{}
	writeOnce    sync.Once
	closeOnce    sync.Once
	closes       atomic.Int32
}

type idleFlowWaitOptions struct {
	inbound      *Socks5Inbound
	association  *socks5UDPAssociation
	scope        string
	client       *socks5UDPClientWriter
	clientSource net.Addr
	target       string
	activePacket []byte
}

type idleFlowTestEnv struct {
	inbound     *Socks5Inbound
	association *socks5UDPAssociation
	clientConn  *net.UDPConn
	client      *socks5UDPClientWriter
	outbound    *scriptedUDPOutbound
	health      *mockPassiveHealthRecorder
	metrics     *countingConnTestSink
	connections [3]*controlledPacketConn
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
	c.closeOnce.Do(func() {
		c.closes.Add(1)
		close(c.closed)
	})
	return nil
}

func (c *controlledPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}
}

func (c *controlledPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *controlledPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *controlledPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestSocks5UDPFlowIdleTimeoutRetiresAndRecreatesTarget(t *testing.T) {
	const idleTimeout = 120 * time.Millisecond
	env := newIdleFlowTestEnv(t, idleTimeout)
	const scope = "flow-idle-scope"
	firstTarget := "127.0.0.1:5301"
	activeTarget := "127.0.0.1:5302"
	firstPacket := mustEncodeUDPFlowTestPacket(t, firstTarget)
	activePacket := mustEncodeUDPFlowTestPacket(t, activeTarget)

	env.relay(scope, firstPacket)
	env.relay(scope, activePacket)
	waitForIdleFlowRemoval(t, idleFlowWaitOptions{
		inbound: env.inbound, association: env.association, scope: scope, client: env.client,
		clientSource: env.clientConn.LocalAddr(), target: firstTarget, activePacket: activePacket,
	})
	env.assertOnlyInactiveFlowRetired(t, activeTarget)

	env.relay(scope, firstPacket)
	if got := env.outbound.calls.Load(); got != 3 {
		t.Fatalf("flow creation calls after recreation: got %d, want 3", got)
	}
	if _, exists := env.association.flow(firstTarget); !exists {
		t.Fatal("next datagram did not recreate retired target flow")
	}
	env.closeAndAssertLifecycles(t)
}

func newIdleFlowTestEnv(t *testing.T, idleTimeout time.Duration) *idleFlowTestEnv {
	t.Helper()
	connections := [3]*controlledPacketConn{
		newControlledPacketConn(nil, nil),
		newControlledPacketConn(nil, nil),
		newControlledPacketConn(nil, nil),
	}
	outbound := &scriptedUDPOutbound{packetConns: []net.PacketConn{
		connections[0], connections[1], connections[2],
	}}
	routeEnv := newProxyE2EEnv(t)
	replaceProxyE2EOutbound(t, routeEnv, outbound)
	health := &mockPassiveHealthRecorder{done: make(chan struct{}, 1)}
	metrics := newCountingConnTestSink()
	inbound := NewSocks5Inbound(Socks5InboundConfig{
		Router: routeEnv.router, Pool: routeEnv.pool, Health: health,
		MetricsSink: metrics, UDPIdleTimeout: idleTimeout,
	})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	clientConn := listenLocalUDP(t)
	t.Cleanup(func() { _ = clientConn.Close() })
	association := registerRelayTestAssociation(
		t, inbound, "flow-idle-scope", clientConn, "plat", nil,
	)
	return &idleFlowTestEnv{
		inbound: inbound, association: association, clientConn: clientConn,
		client: &socks5UDPClientWriter{packetConn: clientConn}, outbound: outbound,
		health: health, metrics: metrics, connections: connections,
	}
}

func (e *idleFlowTestEnv) relay(scope string, packet []byte) {
	e.inbound.handleUDPDatagram(socks5UDPInboundPacket{
		ctx: context.Background(), associationScope: scope,
		client: e.client, source: e.clientConn.LocalAddr(), packet: packet,
	})
}

func (e *idleFlowTestEnv) assertOnlyInactiveFlowRetired(t *testing.T, activeTarget string) {
	t.Helper()
	if e.association.closed.Load() {
		t.Fatal("active target did not keep UDP association alive")
	}
	if _, exists := e.association.flow(activeTarget); !exists {
		t.Fatal("active target flow expired with inactive target")
	}
	if got := e.connections[0].closes.Load(); got != 1 {
		t.Fatalf("inactive target close count: got %d, want 1", got)
	}
	if got := e.health.passiveCalls.Load(); got != 0 {
		t.Fatalf("idle retirement passive failures: got %d, want 0", got)
	}
}

func (e *idleFlowTestEnv) closeAndAssertLifecycles(t *testing.T) {
	t.Helper()
	if err := e.association.Close(); err != nil {
		t.Fatalf("close association: %v", err)
	}
	for index, connection := range e.connections {
		if got := connection.closes.Load(); got != 1 {
			t.Fatalf("flow %d close count: got %d, want 1", index, got)
		}
	}
	assertThreeUDPFlowLifecycles(t, e.metrics.connOps)
}

func TestSocks5UDPFlowResponseRefreshesIdleTimeout(t *testing.T) {
	const idleTimeout = 250 * time.Millisecond
	inbound := NewSocks5Inbound(Socks5InboundConfig{UDPIdleTimeout: idleTimeout})
	t.Cleanup(func() { _ = inbound.CloseUDPAssociations() })
	clientConn := listenLocalUDP(t)
	defer clientConn.Close()
	association := registerRelayTestAssociation(
		t, inbound, "response-refresh-scope", clientConn, "", nil,
	)
	packetConn := newControlledPacketConn(nil, nil)
	clientWriter := newControlledPacketConn(nil, nil)
	flow := &socks5UDPRelayFlow{
		association: association,
		target:      "127.0.0.1:5303",
		packetConn:  packetConn,
		upstream:    &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5303},
		client:      &socks5UDPClientWriter{packetConn: clientWriter},
		idleTimeout: idleTimeout,
	}
	if _, installed, err := association.installFlow(flow.target, flow); err != nil || !installed {
		t.Fatalf("install response flow: installed=%v err=%v", installed, err)
	}
	flow.start()
	time.Sleep(140 * time.Millisecond)
	if err := flow.relayResponse(
		[]byte("response"),
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5303},
		flow.snapshotRecoveryGeneration(),
	); err != nil {
		t.Fatalf("relay response: %v", err)
	}
	time.Sleep(140 * time.Millisecond)
	if _, exists := association.flow(flow.target); !exists {
		t.Fatal("response activity did not refresh flow idle timeout")
	}
}

func mustEncodeUDPFlowTestPacket(t *testing.T, target string) []byte {
	t.Helper()
	packet, err := encodeSocks5UDPDatagram(target, []byte("request"))
	if err != nil {
		t.Fatalf("encode UDP datagram: %v", err)
	}
	return packet
}

func waitForIdleFlowRemoval(
	t *testing.T,
	options idleFlowWaitOptions,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, exists := options.association.flow(options.target); !exists {
			return
		}
		time.Sleep(20 * time.Millisecond)
		options.inbound.handleUDPDatagram(socks5UDPInboundPacket{
			ctx: context.Background(), associationScope: options.scope,
			client: options.client, source: options.clientSource, packet: options.activePacket,
		})
	}
	t.Fatal("inactive UDP flow did not expire")
}

func assertThreeUDPFlowLifecycles(t *testing.T, operations <-chan ConnectionOp) {
	t.Helper()
	openCount := 0
	closeCount := 0
	for index := 0; index < 6; index++ {
		select {
		case operation := <-operations:
			if operation == ConnectionOpen {
				openCount++
			} else if operation == ConnectionClose {
				closeCount++
			}
		case <-time.After(time.Second):
			t.Fatal("missing UDP flow lifecycle metric")
		}
	}
	if openCount != 3 || closeCount != 3 {
		t.Fatalf("UDP flow lifecycle metrics: open=%d close=%d, want 3 each", openCount, closeCount)
	}
}

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
