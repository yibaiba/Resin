package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
)

func (s *Socks5Inbound) serveUDPAssociate(session socks5Session) {
	lifecycle := newRequestLifecycleFromMetadata(
		s.events,
		session.conn.RemoteAddr().String(),
		"UDP_ASSOCIATE",
		ProxyTypeSocks5Forward,
		true,
	)
	lifecycle.setAccount(session.handshake.account)
	association, replyAddr, err := s.registerUDPAssociation(
		session.ctx,
		session.conn,
		session.handshake,
	)
	if err != nil {
		lifecycle.setUpstreamError("socks5_udp_association_register", err)
		lifecycle.setNetOK(false)
		_ = writeSocks5Reply(session.conn, socks5ReplyGeneralFailure, nil)
		lifecycle.finish()
		return
	}
	association.setLifecycle(lifecycle)
	defer association.Close()

	if err := writeSocks5Reply(session.conn, socks5ReplySucceeded, replyAddr); err != nil {
		association.recordFailure("socks5_udp_associate_response_write", err, ErrUpstreamRequestFailed)
		return
	}
	association.recordSuccess()
	waitForSocks5UDPControlClose(session.ctx, session.conn, session.reader)
}

func (s *Socks5Inbound) registerUDPAssociation(
	ctx context.Context,
	conn net.Conn,
	handshake socks5HandshakeResult,
) (*socks5UDPAssociation, net.Addr, error) {
	requestedSource, err := netip.ParseAddrPort(handshake.request.target)
	if err != nil {
		return nil, nil, fmt.Errorf("parse UDP association source: %w", err)
	}
	clientSource := normalizeAddrPort(addrPortFromNetAddr(conn.RemoteAddr()))
	if !clientSource.IsValid() {
		return nil, nil, errSocks5UDPInvalidAssociationSource
	}
	policy := InboundPolicyFromContext(ctx)
	scope := policy.Socks5UDPAssociationScope
	if scope == "" {
		scope = conn.LocalAddr().String()
	}
	udpPort := policy.Socks5UDPPort
	if udpPort == 0 {
		udpPort = addrPortFromNetAddr(conn.LocalAddr()).Port()
	}
	if udpPort == 0 {
		return nil, nil, errSocks5UDPPortUnavailable
	}
	association, err := s.udpAssociations.register(socks5UDPAssociationOptions{
		endpointID:      scope,
		clientIP:        clientSource.Addr(),
		requestedSource: requestedSource,
		platformName:    handshake.platformName,
		account:         handshake.account,
	})
	if err != nil {
		return nil, nil, err
	}
	return association, unspecifiedUDPAddr(conn.LocalAddr(), udpPort), nil
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

func waitForSocks5UDPControlClose(ctx context.Context, conn net.Conn, reader io.Reader) {
	controlPhase := startSocks5HandshakePhase(ctx, conn)
	defer controlPhase.Stop()
	_, _ = io.Copy(io.Discard, reader)
}
