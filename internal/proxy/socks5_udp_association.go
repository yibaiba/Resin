package proxy

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"
)

var (
	errSocks5UDPAssociationManagerClosed = errors.New("SOCKS5 UDP association manager is closed")
	errSocks5UDPAssociationExists        = errors.New("SOCKS5 UDP association already exists")
	errSocks5UDPPendingExists            = errors.New("pending SOCKS5 UDP association already exists")
	errSocks5UDPAssociationNotFound      = errors.New("SOCKS5 UDP association not found")
	errSocks5UDPInvalidAssociationSource = errors.New("invalid SOCKS5 UDP association source")
)

type socks5UDPAssociationOptions struct {
	endpointID      string
	clientIP        netip.Addr
	requestedSource netip.AddrPort
	platformName    string
	account         string
}

type socks5UDPSourceKey struct {
	endpointID string
	source     netip.AddrPort
}

type socks5UDPPendingKey struct {
	endpointID string
	clientIP   netip.Addr
}

type socks5UDPAssociationManager struct {
	mu          sync.Mutex
	idleTimeout time.Duration
	bySource    map[socks5UDPSourceKey]*socks5UDPAssociation
	pending     map[socks5UDPPendingKey]*socks5UDPAssociation
	closed      bool
}

func newSocks5UDPAssociationManager(idleTimeout time.Duration) *socks5UDPAssociationManager {
	if idleTimeout <= 0 {
		idleTimeout = defaultSocks5UDPIdleTimeout
	}
	return &socks5UDPAssociationManager{
		idleTimeout: idleTimeout,
		bySource:    make(map[socks5UDPSourceKey]*socks5UDPAssociation),
		pending:     make(map[socks5UDPPendingKey]*socks5UDPAssociation),
	}
}

func (m *socks5UDPAssociationManager) register(
	options socks5UDPAssociationOptions,
) (*socks5UDPAssociation, error) {
	clientIP, source, pending, err := validateSocks5UDPAssociationOptions(options)
	if err != nil {
		return nil, err
	}
	association := &socks5UDPAssociation{
		manager:      m,
		endpointID:   options.endpointID,
		clientIP:     clientIP,
		platformName: options.platformName,
		account:      options.account,
		source:       source,
		lastActivity: time.Now(),
		flows:        make(map[string]*socks5UDPFlowRef),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errSocks5UDPAssociationManagerClosed
	}
	if pending {
		key := socks5UDPPendingKey{endpointID: options.endpointID, clientIP: clientIP}
		if _, exists := m.pending[key]; exists {
			return nil, errSocks5UDPPendingExists
		}
		m.pending[key] = association
	} else {
		key := socks5UDPSourceKey{endpointID: options.endpointID, source: source}
		if _, exists := m.bySource[key]; exists {
			return nil, errSocks5UDPAssociationExists
		}
		m.bySource[key] = association
	}
	association.timer = time.AfterFunc(m.idleTimeout, association.expireIfIdle)
	return association, nil
}

func (m *socks5UDPAssociationManager) match(
	endpointID string,
	source netip.AddrPort,
) (*socks5UDPAssociation, error) {
	source = normalizeAddrPort(source)
	if endpointID == "" || !source.IsValid() || source.Port() == 0 {
		return nil, errSocks5UDPInvalidAssociationSource
	}

	m.mu.Lock()
	key := socks5UDPSourceKey{endpointID: endpointID, source: source}
	association := m.bySource[key]
	if association == nil {
		pendingKey := socks5UDPPendingKey{endpointID: endpointID, clientIP: source.Addr()}
		association = m.pending[pendingKey]
		if association != nil {
			delete(m.pending, pendingKey)
			m.bySource[key] = association
			association.setSource(source)
		}
	}
	m.mu.Unlock()

	if association == nil || !association.touch() {
		return nil, errSocks5UDPAssociationNotFound
	}
	return association, nil
}

func (m *socks5UDPAssociationManager) closeEndpoint(endpointID string) error {
	if endpointID == "" {
		return nil
	}
	return m.closeMatching(func(association *socks5UDPAssociation) bool {
		return association.endpointID == endpointID
	})
}

func (m *socks5UDPAssociationManager) close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	associations := m.detachMatchingLocked(func(*socks5UDPAssociation) bool { return true })
	m.mu.Unlock()
	return closeSocks5UDPAssociations(associations)
}

func (m *socks5UDPAssociationManager) closeMatching(
	matches func(*socks5UDPAssociation) bool,
) error {
	m.mu.Lock()
	associations := m.detachMatchingLocked(matches)
	m.mu.Unlock()
	return closeSocks5UDPAssociations(associations)
}

func (m *socks5UDPAssociationManager) detachMatchingLocked(
	matches func(*socks5UDPAssociation) bool,
) []*socks5UDPAssociation {
	unique := make(map[*socks5UDPAssociation]struct{})
	for key, association := range m.bySource {
		if matches(association) {
			delete(m.bySource, key)
			unique[association] = struct{}{}
		}
	}
	for key, association := range m.pending {
		if matches(association) {
			delete(m.pending, key)
			unique[association] = struct{}{}
		}
	}
	associations := make([]*socks5UDPAssociation, 0, len(unique))
	for association := range unique {
		associations = append(associations, association)
	}
	return associations
}

func (m *socks5UDPAssociationManager) remove(association *socks5UDPAssociation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, current := range m.bySource {
		if current == association {
			delete(m.bySource, key)
		}
	}
	for key, current := range m.pending {
		if current == association {
			delete(m.pending, key)
		}
	}
}

func validateSocks5UDPAssociationOptions(
	options socks5UDPAssociationOptions,
) (netip.Addr, netip.AddrPort, bool, error) {
	clientIP := normalizeAddr(options.clientIP)
	requested := normalizeAddrPort(options.requestedSource)
	if options.endpointID == "" || !clientIP.IsValid() || !requested.IsValid() {
		return netip.Addr{}, netip.AddrPort{}, false, errSocks5UDPInvalidAssociationSource
	}
	requestedIP := requested.Addr()
	if requested.Port() == 0 {
		if !requestedIP.IsUnspecified() {
			return netip.Addr{}, netip.AddrPort{}, false, fmt.Errorf(
				"%w: zero port requires unspecified address",
				errSocks5UDPInvalidAssociationSource,
			)
		}
		return clientIP, netip.AddrPort{}, true, nil
	}
	if !requestedIP.IsUnspecified() && requestedIP != clientIP {
		return netip.Addr{}, netip.AddrPort{}, false, fmt.Errorf(
			"%w: requested IP does not match TCP client",
			errSocks5UDPInvalidAssociationSource,
		)
	}
	return clientIP, netip.AddrPortFrom(clientIP, requested.Port()), false, nil
}

func normalizeAddrPort(addrPort netip.AddrPort) netip.AddrPort {
	if !addrPort.IsValid() {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(normalizeAddr(addrPort.Addr()), addrPort.Port())
}

func normalizeAddr(addr netip.Addr) netip.Addr {
	if !addr.IsValid() {
		return netip.Addr{}
	}
	return addr.Unmap()
}

func closeSocks5UDPAssociations(associations []*socks5UDPAssociation) error {
	errs := make([]error, 0, len(associations))
	for _, association := range associations {
		if association.closed.CompareAndSwap(false, true) {
			if err := association.closeResources(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
