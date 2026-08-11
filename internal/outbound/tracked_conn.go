package outbound

import (
	"net"
	"sync"
	"syscall"

	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

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
