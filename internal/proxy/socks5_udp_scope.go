package proxy

import "sync"

type socks5UDPAssociationScope struct {
	open        bool
	retainCount int
	closing     bool
}

func (m *socks5UDPAssociationManager) openEndpoint(endpointID string) error {
	if endpointID == "" {
		return errSocks5UDPInvalidAssociationSource
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errSocks5UDPAssociationManagerClosed
	}
	if scope := m.scopes[endpointID]; scope != nil {
		if scope.open {
			return nil
		}
		return errSocks5UDPAssociationScopeClosed
	}
	m.scopes[endpointID] = &socks5UDPAssociationScope{open: true}
	return nil
}

func (m *socks5UDPAssociationManager) retainEndpoint(
	endpointID string,
) (func(), error) {
	if endpointID == "" {
		return nil, errSocks5UDPInvalidAssociationSource
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errSocks5UDPAssociationManagerClosed
	}
	scope := m.scopes[endpointID]
	if scope == nil || !scope.open {
		return nil, errSocks5UDPAssociationScopeClosed
	}
	scope.retainCount++
	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() { m.releaseEndpoint(endpointID, scope) })
	}, nil
}

func (m *socks5UDPAssociationManager) releaseEndpoint(
	endpointID string,
	scope *socks5UDPAssociationScope,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scopes[endpointID] != scope || scope.retainCount == 0 {
		return
	}
	scope.retainCount--
	m.deleteReleasedScopeLocked(endpointID, scope)
}

func (m *socks5UDPAssociationManager) deleteReleasedScopeLocked(
	endpointID string,
	scope *socks5UDPAssociationScope,
) {
	if scope != nil && !scope.open && !scope.closing && scope.retainCount == 0 {
		delete(m.scopes, endpointID)
	}
}
