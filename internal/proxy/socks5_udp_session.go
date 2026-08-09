package proxy

import (
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/routing"
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
	lifecycle    *requestLifecycle
	targetSet    bool
	netFailed    bool
	closed       atomic.Bool
}

type socks5UDPFlow interface {
	writePayload(payload []byte) error
	Close() error
}

type socks5UDPFlowRef struct {
	flow socks5UDPFlow
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

func (a *socks5UDPAssociation) startIdleTimeout() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return false
	}
	if a.timer == nil {
		a.lastActivity = time.Now()
		a.timer = time.AfterFunc(a.manager.idleTimeout, a.expireIfIdle)
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
	flow socks5UDPFlow,
) (*socks5UDPFlowRef, bool, error) {
	if flow == nil {
		return nil, false, errors.New("SOCKS5 UDP flow is nil")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed.Load() {
		return nil, false, errSocks5UDPAssociationNotFound
	}
	if current := a.flows[target]; current != nil {
		return current, false, nil
	}
	ref := &socks5UDPFlowRef{flow: flow}
	a.flows[target] = ref
	return ref, true, nil
}

func (a *socks5UDPAssociation) flow(target string) (*socks5UDPFlowRef, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	flow := a.flows[target]
	return flow, flow != nil && !a.closed.Load()
}

func (a *socks5UDPAssociation) removeFlow(target string, flow socks5UDPFlow) error {
	a.mu.Lock()
	current := a.flows[target]
	if current == nil || current.flow != flow {
		a.mu.Unlock()
		return nil
	}
	delete(a.flows, target)
	a.mu.Unlock()
	return flow.Close()
}

func (a *socks5UDPAssociation) recordRoute(target string, route routing.RouteResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lifecycle == nil {
		return
	}
	if !a.targetSet {
		a.lifecycle.setTarget(target, "")
		a.targetSet = true
	}
	if route.PlatformID != "" {
		a.lifecycle.setRouteResult(route)
	}
}

func (a *socks5UDPAssociation) recordTraffic(ingressBytes, egressBytes int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lifecycle == nil {
		return
	}
	a.lifecycle.addIngressBytes(ingressBytes)
	a.lifecycle.addEgressBytes(egressBytes)
	if ingressBytes > 0 {
		a.lifecycle.markFirstByteReceived()
	}
}

func (a *socks5UDPAssociation) recordSuccess() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lifecycle != nil && !a.netFailed {
		a.lifecycle.setNetOK(true)
	}
}

func (a *socks5UDPAssociation) recordFailure(stage string, err error, proxyErr *ProxyError) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.netFailed = true
	if a.lifecycle == nil {
		return
	}
	a.lifecycle.setProxyError(proxyErr)
	a.lifecycle.setUpstreamError(stage, err)
	a.lifecycle.setNetOK(false)
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
	lifecycle := a.lifecycle
	a.lifecycle = nil
	a.mu.Unlock()

	errs := make([]error, 0, len(flows))
	for _, flow := range flows {
		if err := flow.flow.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if lifecycle != nil {
		lifecycle.finish()
	}
	return errors.Join(errs...)
}
