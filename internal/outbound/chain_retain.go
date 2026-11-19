package outbound

import (
	"context"
	"net"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

func nopChainRelease() {}

func (m *OutboundManager) holdChainLocked(key string) func() {
	chain, ok := m.chains[key]
	if !ok {
		return nopChainRelease
	}
	chain.refs++
	var once sync.Once
	return func() {
		once.Do(func() { m.releaseChainRef(key) })
	}
}

func (m *OutboundManager) retainChain(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	chain, ok := m.chains[key]
	if !ok {
		return false
	}
	chain.refs++
	return true
}

func (m *OutboundManager) releaseChainRef(key string) {
	m.mu.Lock()
	chain, ok := m.chains[key]
	if !ok {
		m.mu.Unlock()
		return
	}
	if chain.refs > 0 {
		chain.refs--
	}
	stale := m.evictExcessChainsLocked("")
	m.mu.Unlock()
	m.finishChainReleases(stale)
}

func (m *OutboundManager) wrapCachedChain(key string, inner adapter.Outbound) adapter.Outbound {
	return &retainedChainOutbound{Outbound: inner, mgr: m, key: key}
}

func bindChainLease(ob adapter.Outbound, release func()) adapter.Outbound {
	if ob == nil || release == nil {
		return ob
	}
	return &chainLeaseOutbound{Outbound: ob, release: release}
}

// retainedChainOutbound pins a cached chain for each live Dial/ListenPacket.
type retainedChainOutbound struct {
	adapter.Outbound
	mgr *OutboundManager
	key string
}

func (o *retainedChainOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if !o.mgr.retainChain(o.key) {
		return nil, ErrOutboundNotReady
	}
	conn, err := o.Outbound.DialContext(ctx, network, destination)
	if err != nil {
		o.mgr.releaseChainRef(o.key)
		return nil, err
	}
	return &retainedConn{Conn: conn, release: func() { o.mgr.releaseChainRef(o.key) }}, nil
}

func (o *retainedChainOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if !o.mgr.retainChain(o.key) {
		return nil, ErrOutboundNotReady
	}
	conn, err := o.Outbound.ListenPacket(ctx, destination)
	if err != nil {
		o.mgr.releaseChainRef(o.key)
		return nil, err
	}
	return &retainedPacketConn{PacketConn: conn, release: func() { o.mgr.releaseChainRef(o.key) }}, nil
}

type retainedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *retainedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

type retainedPacketConn struct {
	net.PacketConn
	once    sync.Once
	release func()
}

func (c *retainedPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(c.release)
	return err
}

// chainLeaseOutbound drops the Ensure hold after the first Dial/ListenPacket
// so one-shot probe callers can keep the (Outbound, error) signature.
type chainLeaseOutbound struct {
	adapter.Outbound
	once    sync.Once
	release func()
}

func (o *chainLeaseOutbound) drop() {
	if o.release == nil {
		return
	}
	o.once.Do(o.release)
}

func (o *chainLeaseOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := o.Outbound.DialContext(ctx, network, destination)
	o.drop()
	return conn, err
}

func (o *chainLeaseOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	conn, err := o.Outbound.ListenPacket(ctx, destination)
	o.drop()
	return conn, err
}

func (o *retainedChainOutbound) Close() error {
	return nil
}

func (o *chainLeaseOutbound) Close() error {
	o.drop()
	return nil
}
