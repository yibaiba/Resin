package main

import (
	"context"
	"net"

	"github.com/Resinat/Resin/internal/proxy"
)

type endpointSocksGate struct {
	current      func() endpointRuntimeSnapshot
	next         inboundConnHandler
	scopeHandler endpointPacketHandler
}

func (g *endpointSocksGate) ServeConnContext(ctx context.Context, conn net.Conn) {
	snapshot := endpointRuntimeSnapshot{}
	if g != nil && g.current != nil {
		snapshot = g.current()
	}
	endpoint := snapshot.endpoint
	associationScope := snapshot.associationScope
	if !endpoint.AllowProxy || !endpoint.AllowSOCKS5 || g == nil || g.next == nil ||
		g.scopeHandler == nil || associationScope == "" {
		rejectEndpointSOCKS5(conn)
		return
	}
	releaseScope, err := g.scopeHandler.RetainUDPAssociationScope(associationScope)
	if err != nil {
		rejectEndpointSOCKS5(conn)
		return
	}
	defer releaseScope()
	ctx = proxy.ContextWithInboundPolicy(ctx, proxy.InboundPolicy{
		RequireProxyAuthInfo:      endpoint.RequireProxyAuthInfo,
		Socks5UDPAssociationScope: associationScope,
		Socks5UDPPort:             uint16(endpoint.Port),
	})
	g.next.ServeConnContext(ctx, conn)
}

func rejectEndpointSOCKS5(conn net.Conn) {
	if conn == nil {
		return
	}
	_, _ = conn.Write([]byte{0x05, 0xFF})
	_ = conn.Close()
}
