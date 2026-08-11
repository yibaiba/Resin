package outbound

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

type observedConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *observedConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

type delayedConnectionOutbound struct {
	closableOnly
	dialStarted   chan struct{}
	dialRelease   chan struct{}
	packetStarted chan struct{}
	packetRelease chan struct{}
	stream        *observedConn
	streamPeer    net.Conn
	packet        *testPacketConn
}

func newDelayedConnectionOutbound() *delayedConnectionOutbound {
	return &delayedConnectionOutbound{
		dialStarted:   make(chan struct{}),
		dialRelease:   make(chan struct{}),
		packetStarted: make(chan struct{}),
		packetRelease: make(chan struct{}),
	}
}

func (o *delayedConnectionOutbound) DialContext(
	context.Context,
	string,
	M.Socksaddr,
) (net.Conn, error) {
	close(o.dialStarted)
	<-o.dialRelease
	stream, peer := net.Pipe()
	o.stream = &observedConn{Conn: stream}
	o.streamPeer = peer
	return o.stream, nil
}

func (o *delayedConnectionOutbound) ListenPacket(
	context.Context,
	M.Socksaddr,
) (net.PacketConn, error) {
	close(o.packetStarted)
	<-o.packetRelease
	o.packet = &testPacketConn{}
	return o.packet, nil
}

type unclosableConnectionOutbound struct {
	stream     *observedConn
	streamPeer net.Conn
	packet     *testPacketConn
}

func (o *unclosableConnectionOutbound) Type() string           { return "unclosable" }
func (o *unclosableConnectionOutbound) Tag() string            { return "unclosable" }
func (o *unclosableConnectionOutbound) Network() []string      { return []string{"tcp", "udp"} }
func (o *unclosableConnectionOutbound) Dependencies() []string { return nil }

func (o *unclosableConnectionOutbound) DialContext(
	context.Context,
	string,
	M.Socksaddr,
) (net.Conn, error) {
	stream, peer := net.Pipe()
	o.stream = &observedConn{Conn: stream}
	o.streamPeer = peer
	return o.stream, nil
}

func (o *unclosableConnectionOutbound) ListenPacket(
	context.Context,
	M.Socksaddr,
) (net.PacketConn, error) {
	o.packet = &testPacketConn{}
	return o.packet, nil
}

func TestManagedOutboundCloseRejectsLateDialResult(t *testing.T) {
	underlying := newDelayedConnectionOutbound()
	managed := newManagedOutbound(underlying, nil)
	result := make(chan error, 1)
	go func() {
		conn, err := managed.DialContext(context.Background(), "tcp", M.Socksaddr{})
		if conn != nil {
			_ = conn.Close()
		}
		result <- err
	}()

	<-underlying.dialStarted
	if err := managed.Close(); err != nil {
		t.Fatalf("close managed outbound: %v", err)
	}
	close(underlying.dialRelease)
	if err := <-result; !errors.Is(err, ErrOutboundRetired) {
		t.Fatalf("late dial error = %v, want %v", err, ErrOutboundRetired)
	}
	defer underlying.streamPeer.Close()
	if !underlying.stream.closed.Load() {
		t.Fatal("late stream connection was not closed")
	}
}

func TestManagedOutboundCloseRejectsLatePacketResult(t *testing.T) {
	underlying := newDelayedConnectionOutbound()
	managed := newManagedOutbound(underlying, nil)
	result := make(chan error, 1)
	go func() {
		conn, err := managed.ListenPacket(context.Background(), M.Socksaddr{})
		if conn != nil {
			_ = conn.Close()
		}
		result <- err
	}()

	<-underlying.packetStarted
	if err := managed.Close(); err != nil {
		t.Fatalf("close managed outbound: %v", err)
	}
	close(underlying.packetRelease)
	if err := <-result; !errors.Is(err, ErrOutboundRetired) {
		t.Fatalf("late packet error = %v, want %v", err, ErrOutboundRetired)
	}
	if !underlying.packet.closed.Load() {
		t.Fatal("late packet connection was not closed")
	}
}

func TestManagedOutboundCloseForcesTrackedConnectionsClosed(t *testing.T) {
	underlying := &unclosableConnectionOutbound{}
	managed := newManagedOutbound(underlying, nil)
	stream, err := managed.DialContext(context.Background(), "tcp", M.Socksaddr{})
	if err != nil {
		t.Fatalf("dial tracked stream: %v", err)
	}
	defer underlying.streamPeer.Close()
	packet, err := managed.ListenPacket(context.Background(), M.Socksaddr{})
	if err != nil {
		t.Fatalf("listen tracked packet: %v", err)
	}

	if err := managed.Close(); err != nil {
		t.Fatalf("close managed outbound: %v", err)
	}
	if !underlying.stream.closed.Load() || !underlying.packet.closed.Load() {
		t.Fatalf("tracked connections remained open: stream=%t packet=%t",
			underlying.stream.closed.Load(), underlying.packet.closed.Load())
	}
	managed.mu.Lock()
	active, tracked := managed.active, len(managed.connections)
	managed.mu.Unlock()
	if active != 0 || tracked != 0 {
		t.Fatalf("managed connection state not drained: active=%d tracked=%d", active, tracked)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("repeat stream close: %v", err)
	}
	if err := packet.Close(); err != nil {
		t.Fatalf("repeat packet close: %v", err)
	}
}
