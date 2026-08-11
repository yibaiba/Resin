package outbound

import (
	"errors"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/sagernet/sing/common/buf"
	B "github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type waitableExtendedConn struct {
	net.Conn
}

func (c *waitableExtendedConn) ReadBuffer(*buf.Buffer) error {
	return io.EOF
}

func (c *waitableExtendedConn) WriteBuffer(buffer *buf.Buffer) error {
	buffer.Release()
	return nil
}

func (c *waitableExtendedConn) InitializeReadWaiter(N.ReadWaitOptions) bool {
	return false
}

func (c *waitableExtendedConn) WaitReadBuffer() (*buf.Buffer, error) {
	return nil, io.EOF
}

type waitablePacketConn struct {
	net.PacketConn
}

type syscallNetConn struct {
	net.Conn
	err error
}

func (c *syscallNetConn) SyscallConn() (syscall.RawConn, error) {
	return nil, c.err
}

type syscallPacketConn struct {
	net.PacketConn
	err error
}

type upstreamNetConn struct {
	net.Conn
	upstream any
}

func (c *upstreamNetConn) Upstream() any {
	return c.upstream
}

type upstreamPacketConn struct {
	net.PacketConn
	upstream any
}

func (c *upstreamPacketConn) Upstream() any {
	return c.upstream
}

func (c *syscallPacketConn) SyscallConn() (syscall.RawConn, error) {
	return nil, c.err
}

func (c *waitablePacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, io.EOF
}

func (c *waitablePacketConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	return nil
}

func (c *waitablePacketConn) InitializeReadWaiter(N.ReadWaitOptions) bool {
	return false
}

func (c *waitablePacketConn) WaitReadPacket() (*buf.Buffer, M.Socksaddr, error) {
	return nil, M.Socksaddr{}, io.EOF
}

func TestTrackedConnPreservesReadWaiterDiscovery(t *testing.T) {
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()
	underlying := &waitableExtendedConn{Conn: conn}
	tracked := &trackedConn{ExtendedConn: underlying, release: func() {}}

	waiter, ok := B.CreateReadWaiter(tracked)
	if !ok || waiter != underlying {
		t.Fatalf("read waiter discovery: ok=%t waiter=%T", ok, waiter)
	}
}

func TestTrackedPacketConnPreservesReadWaiterDiscovery(t *testing.T) {
	underlying := &waitablePacketConn{PacketConn: &testPacketConn{}}
	tracked := &trackedPacketConn{NetPacketConn: underlying, release: func() {}}

	waiter, ok := B.CreatePacketReadWaiter(tracked)
	if !ok || waiter != underlying {
		t.Fatalf("packet read waiter discovery: ok=%t waiter=%T", ok, waiter)
	}
}

func TestTrackedConnPreservesSyscallConn(t *testing.T) {
	wantErr := errors.New("stream syscall marker")
	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	tracked := newTrackedConn(&syscallNetConn{Conn: conn, err: wantErr}, func() {})
	syscallTracked, ok := tracked.(syscall.Conn)
	if !ok {
		t.Fatal("tracked stream did not expose syscall.Conn")
	}
	if _, err := syscallTracked.SyscallConn(); !errors.Is(err, wantErr) {
		t.Fatalf("SyscallConn error = %v, want %v", err, wantErr)
	}
}

func TestTrackedPacketConnPreservesSyscallConn(t *testing.T) {
	wantErr := errors.New("packet syscall marker")
	underlying := &syscallPacketConn{PacketConn: &testPacketConn{}, err: wantErr}

	tracked := newTrackedPacketConn(underlying, func() {})
	syscallTracked, ok := tracked.(syscall.Conn)
	if !ok {
		t.Fatal("tracked packet connection did not expose syscall.Conn")
	}
	if _, err := syscallTracked.SyscallConn(); !errors.Is(err, wantErr) {
		t.Fatalf("SyscallConn error = %v, want %v", err, wantErr)
	}
}

func TestTrackedConnectionsDoNotInventSyscallConn(t *testing.T) {
	stream, peer := net.Pipe()
	defer stream.Close()
	defer peer.Close()
	if _, ok := newTrackedConn(stream, func() {}).(syscall.Conn); ok {
		t.Fatal("tracked stream exposed unsupported syscall.Conn")
	}
	if _, ok := newTrackedPacketConn(&testPacketConn{}, func() {}).(syscall.Conn); ok {
		t.Fatal("tracked packet connection exposed unsupported syscall.Conn")
	}
}

func TestTrackedConnectionsDoNotExposeWrappedSyscallConn(t *testing.T) {
	stream, peer := net.Pipe()
	defer stream.Close()
	defer peer.Close()
	wrappedStream := &upstreamNetConn{
		Conn:     stream,
		upstream: &syscallNetConn{Conn: stream},
	}
	if _, ok := newTrackedConn(wrappedStream, func() {}).(syscall.Conn); ok {
		t.Fatal("tracked stream exposed syscall.Conn hidden by protocol wrapper")
	}

	packet := &testPacketConn{}
	wrappedPacket := &upstreamPacketConn{
		PacketConn: packet,
		upstream:   &syscallPacketConn{PacketConn: packet},
	}
	if _, ok := newTrackedPacketConn(wrappedPacket, func() {}).(syscall.Conn); ok {
		t.Fatal("tracked packet connection exposed syscall.Conn hidden by protocol wrapper")
	}
}
