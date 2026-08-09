package proxy

import (
	"errors"
	"io"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type socks5UDPAssociation struct {
	manager      *socks5UDPAssociationManager
	endpointID   string
	clientIP     netip.Addr
	platformName string
	account      string

	mu           sync.Mutex
	source       netip.AddrPort
	lastActivity time.Time
	timer        *time.Timer
	flows        map[string]*socks5UDPFlowRef
	closed       atomic.Bool
}

type socks5UDPFlowRef struct {
	closer io.Closer
}

func (a *socks5UDPAssociation) sourceAddr() netip.AddrPort {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.source
}

func (a *socks5UDPAssociation) touch() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return false
	}
	a.lastActivity = time.Now()
	if a.timer != nil {
		a.timer.Reset(a.manager.idleTimeout)
	}
	return true
}

func (a *socks5UDPAssociation) setSource(source netip.AddrPort) {
	a.mu.Lock()
	a.source = source
	a.mu.Unlock()
}

func (a *socks5UDPAssociation) expireIfIdle() {
	a.mu.Lock()
	if a.closed.Load() {
		a.mu.Unlock()
		return
	}
	remaining := a.manager.idleTimeout - time.Since(a.lastActivity)
	if remaining > 0 {
		a.timer.Reset(remaining)
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	_ = a.Close()
}

func (a *socks5UDPAssociation) installFlow(
	target string,
	closer io.Closer,
) (*socks5UDPFlowRef, bool, error) {
	if closer == nil {
		return nil, false, errors.New("SOCKS5 UDP flow closer is nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return nil, false, errSocks5UDPAssociationNotFound
	}
	if current := a.flows[target]; current != nil {
		return current, false, nil
	}
	flow := &socks5UDPFlowRef{closer: closer}
	a.flows[target] = flow
	return flow, true, nil
}

func (a *socks5UDPAssociation) flow(target string) (*socks5UDPFlowRef, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	flow := a.flows[target]
	return flow, flow != nil && !a.closed.Load()
}

func (a *socks5UDPAssociation) removeFlow(target string, flow *socks5UDPFlowRef) error {
	a.mu.Lock()
	if a.flows[target] != flow {
		a.mu.Unlock()
		return nil
	}
	delete(a.flows, target)
	a.mu.Unlock()
	return flow.closer.Close()
}

func (a *socks5UDPAssociation) Close() error {
	if a == nil || !a.closed.CompareAndSwap(false, true) {
		return nil
	}
	a.manager.remove(a)
	return a.closeResources()
}

func (a *socks5UDPAssociation) closeResources() error {
	a.mu.Lock()
	if a.timer != nil {
		a.timer.Stop()
	}
	flows := a.flows
	a.flows = make(map[string]*socks5UDPFlowRef)
	a.mu.Unlock()

	errs := make([]error, 0, len(flows))
	for _, flow := range flows {
		if err := flow.closer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
