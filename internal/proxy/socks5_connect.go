package proxy

import "net"

func (s *Socks5Inbound) serveConnect(session socks5Session) {
	lifecycle := newRequestLifecycleFromMetadata(
		s.events,
		session.conn.RemoteAddr().String(),
		"",
		ProxyTypeSocks5Forward,
		true,
	)
	lifecycle.setTarget(session.handshake.request.target, "")
	lifecycle.setAccount(session.handshake.account)
	defer lifecycle.finish()

	prepare := prepareConnectTunnel(connectTunnelRequest{
		ctx:          session.ctx,
		deps:         s.tunnel,
		platformName: session.handshake.platformName,
		account:      session.handshake.account,
		target:       session.handshake.request.target,
		requestID:    lifecycle.log.ID,
	})
	lifecycle.setHealthFeedback(prepare.health)
	if prepare.route.PlatformID != "" {
		lifecycle.setRouteResult(prepare.route)
	}
	if prepare.session == nil {
		s.finishFailedConnect(session.conn, lifecycle, prepare)
		return
	}

	if err := writeSocks5Reply(session.conn, socks5ReplySucceeded, prepare.session.upstreamConn.LocalAddr()); err != nil {
		_ = prepare.session.upstreamConn.Close()
		prepare.session.health.record(passiveHealthNeutral)
		lifecycle.setProxyError(ErrUpstreamRequestFailed)
		lifecycle.setUpstreamError("socks5_connect_response_write", err)
		lifecycle.setNetOK(false)
		return
	}

	relay := pumpPreparedTunnel(session.conn, session.reader, prepare.session, tunnelPumpOptions{
		onFirstIngressByte: lifecycle.markFirstByteReceived,
	})
	lifecycle.setTunnelRelay(relay)
	lifecycle.addIngressBytes(relay.ingressBytes)
	lifecycle.addEgressBytes(relay.egressBytes)
	if relay.proxyErr != nil {
		lifecycle.setProxyError(relay.proxyErr)
		lifecycle.setUpstreamError(relay.upstreamStage, relay.upstreamErr)
	}
	lifecycle.setNetOK(relay.netOK)
}

func (s *Socks5Inbound) finishFailedConnect(
	conn net.Conn,
	lifecycle *requestLifecycle,
	prepare tunnelPrepareResult,
) {
	if prepare.proxyErr != nil {
		lifecycle.setProxyError(prepare.proxyErr)
		if prepare.upstreamStage != "" {
			lifecycle.setUpstreamError(prepare.upstreamStage, prepare.upstreamErr)
		}
		lifecycle.setNetOK(false)
		_ = writeSocks5Reply(conn, socks5ReplyGeneralFailure, nil)
		return
	}
	if prepare.canceled {
		lifecycle.setNetOK(true)
	}
}
