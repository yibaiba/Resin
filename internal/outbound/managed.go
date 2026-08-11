package outbound

import (
	"context"
	"errors"
	"net"
	"sync"
	"syscall"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var ErrOutboundRetired = errors.New("outbound retired")

// managedOutbound defers closing the protocol outbound until active streams
// finish. Retired outbounds reject new connections immediately.
type managedOutbound struct {
	adapter.Outbound

	mu        sync.Mutex
	active    int
	retired   bool
	closeOnce sync.Once
	closeErr  error
	onDrained func(*managedOutbound)
}

func newManagedOutbound(outbound adapter.Outbound, onDrained func(*managedOutbound)) *managedOutbound {
	return &managedOutbound{Outbound: outbound, onDrained: onDrained}
}

func (o *managedOutbound) DialContext(
	ctx context.Context,
	network string,
	destination M.Socksaddr,
) (net.Conn, error) {
	if !o.acquire() {
		return nil, ErrOutboundRetired
	}
	conn, err := o.Outbound.DialContext(ctx, network, destination)
	if err != nil {
		o.release()
		return nil, err
	}
	return newTrackedConn(conn, o.release), nil
}

func (o *managedOutbound) ListenPacket(
	ctx context.Context,
	destination M.Socksaddr,
) (net.PacketConn, error) {
	if !o.acquire() {
		return nil, ErrOutboundRetired
	}
	conn, err := o.Outbound.ListenPacket(ctx, destination)
	if err != nil {
		o.release()
		return nil, err
	}
	return newTrackedPacketConn(conn, o.release), nil
}

func (o *managedOutbound) acquire() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.retired {
		return false
	}
	o.active++
	return true
}

func (o *managedOutbound) release() {
	o.mu.Lock()
	if o.active > 0 {
		o.active--
	}
	shouldClose := o.retired && o.active == 0
	o.mu.Unlock()
	if shouldClose {
		_ = o.closeUnderlying()
	}
}

func (o *managedOutbound) retire() error {
	o.mu.Lock()
	o.retired = true
	shouldClose := o.active == 0
	o.mu.Unlock()
	if !shouldClose {
		return nil
	}
	return o.closeUnderlying()
}

func (o *managedOutbound) Close() error {
	o.mu.Lock()
	o.retired = true
	o.mu.Unlock()
	return o.closeUnderlying()
}

func (o *managedOutbound) closeUnderlying() error {
	o.closeOnce.Do(func() {
		o.closeErr = closeOutbound(o.Outbound)
		if o.onDrained != nil {
			o.onDrained(o)
		}
	})
	return o.closeErr
}

type trackedConn struct {
	N.ExtendedConn
	release   func()
	closeOnce sync.Once
	closeErr  error
}

func newTrackedConn(conn net.Conn, release func()) net.Conn {
	tracked := &trackedConn{ExtendedConn: bufio.NewExtendedConn(conn), release: release}
	syscallConn, ok := conn.(syscall.Conn)
	if !ok {
		return tracked
	}
	return &trackedSyscallConn{trackedConn: tracked, syscallConn: syscallConn}
}

var (
	_ N.ReaderWithUpstream = (*trackedConn)(nil)
	_ N.WriterWithUpstream = (*trackedConn)(nil)
	_ N.WithUpstreamReader = (*trackedConn)(nil)
	_ N.WithUpstreamWriter = (*trackedConn)(nil)
)

func (c *trackedConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.ExtendedConn.Close()
		c.release()
	})
	return c.closeErr
}

func (c *trackedConn) CloseRead() error {
	return N.CloseRead(c.ExtendedConn)
}

func (c *trackedConn) CloseWrite() error {
	return N.CloseWrite(c.ExtendedConn)
}

func (c *trackedConn) Upstream() any {
	return c.ExtendedConn
}

func (c *trackedConn) ReaderReplaceable() bool {
	return true
}

func (c *trackedConn) WriterReplaceable() bool {
	return true
}

func (c *trackedConn) UpstreamReader() any {
	return c.ExtendedConn
}

func (c *trackedConn) UpstreamWriter() any {
	return c.ExtendedConn
}

type trackedSyscallConn struct {
	*trackedConn
	syscallConn syscall.Conn
}

var _ syscall.Conn = (*trackedSyscallConn)(nil)

func (c *trackedSyscallConn) SyscallConn() (syscall.RawConn, error) {
	return c.syscallConn.SyscallConn()
}

type trackedPacketConn struct {
	N.NetPacketConn
	release   func()
	closeOnce sync.Once
	closeErr  error
}

func newTrackedPacketConn(conn net.PacketConn, release func()) net.PacketConn {
	tracked := &trackedPacketConn{NetPacketConn: bufio.NewPacketConn(conn), release: release}
	syscallConn, ok := conn.(syscall.Conn)
	if !ok {
		return tracked
	}
	return &trackedSyscallPacketConn{trackedPacketConn: tracked, syscallConn: syscallConn}
}

var (
	_ N.ReaderWithUpstream = (*trackedPacketConn)(nil)
	_ N.WriterWithUpstream = (*trackedPacketConn)(nil)
	_ N.WithUpstreamReader = (*trackedPacketConn)(nil)
	_ N.WithUpstreamWriter = (*trackedPacketConn)(nil)
)

func (c *trackedPacketConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.NetPacketConn.Close()
		c.release()
	})
	return c.closeErr
}

func (c *trackedPacketConn) Upstream() any {
	return c.NetPacketConn
}

func (c *trackedPacketConn) ReaderReplaceable() bool {
	return true
}

func (c *trackedPacketConn) WriterReplaceable() bool {
	return true
}

func (c *trackedPacketConn) UpstreamReader() any {
	return c.NetPacketConn
}

func (c *trackedPacketConn) UpstreamWriter() any {
	return c.NetPacketConn
}

type trackedSyscallPacketConn struct {
	*trackedPacketConn
	syscallConn syscall.Conn
}

var _ syscall.Conn = (*trackedSyscallPacketConn)(nil)

func (c *trackedSyscallPacketConn) SyscallConn() (syscall.RawConn, error) {
	return c.syscallConn.SyscallConn()
}
