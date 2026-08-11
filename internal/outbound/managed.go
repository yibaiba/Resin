package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

var ErrOutboundRetired = errors.New("outbound retired")

// managedOutbound lets retired connections drain, while Close force-closes
// every registered connection. Both states reject new connections.
type managedOutbound struct {
	adapter.Outbound

	mu          sync.Mutex
	active      int
	retired     bool
	forceClosed bool
	connections map[io.Closer]struct{}
	forceOnce   sync.Once
	forceErr    error
	closeOnce   sync.Once
	closeErr    error
	onDrained   func(*managedOutbound)
}

func newManagedOutbound(outbound adapter.Outbound, onDrained func(*managedOutbound)) *managedOutbound {
	return &managedOutbound{
		Outbound:    outbound,
		connections: make(map[io.Closer]struct{}),
		onDrained:   onDrained,
	}
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
	return o.trackConn(conn)
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
	return o.trackPacketConn(conn)
}

func (o *managedOutbound) trackConn(conn net.Conn) (net.Conn, error) {
	var tracked net.Conn
	tracked = newTrackedConn(conn, func() { o.releaseConnection(tracked) })
	if o.registerConnection(tracked) {
		return tracked, nil
	}
	return nil, errors.Join(ErrOutboundRetired, tracked.Close())
}

func (o *managedOutbound) trackPacketConn(conn net.PacketConn) (net.PacketConn, error) {
	var tracked net.PacketConn
	tracked = newTrackedPacketConn(conn, func() { o.releaseConnection(tracked) })
	if o.registerConnection(tracked) {
		return tracked, nil
	}
	return nil, errors.Join(ErrOutboundRetired, tracked.Close())
}

func (o *managedOutbound) registerConnection(conn io.Closer) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.forceClosed {
		return false
	}
	o.connections[conn] = struct{}{}
	return true
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
	o.releaseConnection(nil)
}

func (o *managedOutbound) releaseConnection(conn io.Closer) {
	o.mu.Lock()
	if conn != nil {
		delete(o.connections, conn)
	}
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
	o.forceOnce.Do(func() {
		connections := o.beginForceClose()
		errs := make([]error, 0, len(connections)+1)
		for _, connection := range connections {
			errs = append(errs, connection.Close())
		}
		errs = append(errs, o.closeUnderlying())
		o.forceErr = errors.Join(errs...)
	})
	return o.forceErr
}

func (o *managedOutbound) beginForceClose() []io.Closer {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.retired = true
	o.forceClosed = true
	connections := make([]io.Closer, 0, len(o.connections))
	for connection := range o.connections {
		connections = append(connections, connection)
	}
	return connections
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
