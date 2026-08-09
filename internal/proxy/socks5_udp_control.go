package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
)

type socks5UDPRegistration struct {
	ctx       context.Context
	conn      net.Conn
	handshake socks5HandshakeResult
	lifecycle *requestLifecycle
}

type socks5UDPControlWait struct {
	ctx             context.Context
	conn            net.Conn
	reader          io.Reader
	associationDone <-chan struct{}
}

type socks5UDPControlWatcher struct {
	ctx             context.Context
	conn            net.Conn
	associationDone <-chan struct{}
	stop            <-chan struct{}
	done            chan<- struct{}
}

func (s *Socks5Inbound) serveUDPAssociate(session socks5Session) {
	lifecycle := newRequestLifecycleFromMetadata(
		s.events,
		session.conn.RemoteAddr().String(),
		"UDP_ASSOCIATE",
		ProxyTypeSocks5Forward,
		true,
	)
	lifecycle.setAccount(session.handshake.account)
	association, replyAddr, err := s.registerUDPAssociation(socks5UDPRegistration{
		ctx:       session.ctx,
		conn:      session.conn,
		handshake: session.handshake,
		lifecycle: lifecycle,
	})
	if err != nil {
		lifecycle.setUpstreamError("socks5_udp_association_register", err)
		lifecycle.setNetOK(false)
		_ = writeSocks5Reply(session.conn, socks5ReplyGeneralFailure, nil)
		lifecycle.finish()
		return
	}
	defer association.Close()

	if err := writeSocks5Reply(session.conn, socks5ReplySucceeded, replyAddr); err != nil {
		association.recordFailure("socks5_udp_associate_response_write", err, ErrUpstreamRequestFailed)
		return
	}
	association.recordSuccess()
	if !association.startIdleTimeout() {
		return
	}
	waitForSocks5UDPControlClose(socks5UDPControlWait{
		ctx: session.ctx, conn: session.conn, reader: session.reader, associationDone: association.done,
	})
}

func (s *Socks5Inbound) registerUDPAssociation(
	registration socks5UDPRegistration,
) (*socks5UDPAssociation, net.Addr, error) {
	requestedSource, err := netip.ParseAddrPort(registration.handshake.request.target)
	if err != nil {
		return nil, nil, fmt.Errorf("parse UDP association source: %w", err)
	}
	clientSource := normalizeAddrPort(addrPortFromNetAddr(registration.conn.RemoteAddr()))
	if !clientSource.IsValid() {
		return nil, nil, errSocks5UDPInvalidAssociationSource
	}
	policy := InboundPolicyFromContext(registration.ctx)
	scope := policy.Socks5UDPAssociationScope
	if scope == "" {
		scope = registration.conn.LocalAddr().String()
	}
	udpPort := policy.Socks5UDPPort
	if udpPort == 0 {
		udpPort = addrPortFromNetAddr(registration.conn.LocalAddr()).Port()
	}
	if udpPort == 0 {
		return nil, nil, errSocks5UDPPortUnavailable
	}
	association, err := s.udpAssociations.register(socks5UDPAssociationOptions{
		endpointID:       scope,
		clientIP:         clientSource.Addr(),
		requestedSource:  requestedSource,
		platformName:     registration.handshake.platformName,
		account:          registration.handshake.account,
		lifecycle:        registration.lifecycle,
		deferIdleTimeout: true,
	})
	if err != nil {
		return nil, nil, err
	}
	return association, unspecifiedUDPAddr(registration.conn.LocalAddr(), udpPort), nil
}

var errSocks5UDPPortUnavailable = errors.New("SOCKS5 UDP endpoint port is unavailable")

func addrPortFromNetAddr(addr net.Addr) netip.AddrPort {
	if addr == nil {
		return netip.AddrPort{}
	}
	if addrPort, err := netip.ParseAddrPort(addr.String()); err == nil {
		return addrPort
	}
	switch typed := addr.(type) {
	case *net.TCPAddr:
		parsed, ok := netip.AddrFromSlice(typed.IP)
		if ok && typed.Port >= 0 && typed.Port <= 65535 {
			return netip.AddrPortFrom(parsed.Unmap(), uint16(typed.Port))
		}
	case *net.UDPAddr:
		parsed, ok := netip.AddrFromSlice(typed.IP)
		if ok && typed.Port >= 0 && typed.Port <= 65535 {
			return netip.AddrPortFrom(parsed.Unmap(), uint16(typed.Port))
		}
	}
	return netip.AddrPort{}
}

func unspecifiedUDPAddr(localAddr net.Addr, port uint16) *net.UDPAddr {
	local := addrPortFromNetAddr(localAddr)
	if local.IsValid() && local.Addr().Is6() {
		return net.UDPAddrFromAddrPort(netip.AddrPortFrom(netip.IPv6Unspecified(), port))
	}
	return net.UDPAddrFromAddrPort(netip.AddrPortFrom(netip.IPv4Unspecified(), port))
}

func waitForSocks5UDPControlClose(wait socks5UDPControlWait) {
	stopWatcher := make(chan struct{})
	watcherDone := make(chan struct{})
	watcher := socks5UDPControlWatcher{
		ctx: wait.ctx, conn: wait.conn, associationDone: wait.associationDone,
		stop: stopWatcher, done: watcherDone,
	}
	go watcher.run()
	_, _ = io.Copy(io.Discard, wait.reader)
	close(stopWatcher)
	<-watcherDone
}

func (w socks5UDPControlWatcher) run() {
	defer close(w.done)
	if w.ctx == nil {
		w.ctx = context.Background()
	}
	select {
	case <-w.stop:
		return
	case <-w.ctx.Done():
	case <-w.associationDone:
	}
	_ = w.conn.Close()
}
