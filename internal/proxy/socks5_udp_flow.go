package proxy

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/routing"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

const socks5UDPMaxDatagramSize = 65535

type socks5UDPClientWriter struct {
	packetConn net.PacketConn
	mu         sync.Mutex
}

func (w *socks5UDPClientWriter) writeTo(payload []byte, destination net.Addr) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.packetConn.WriteTo(payload, destination)
}

type socks5UDPFlowCreateResult struct {
	flow     *socks5UDPRelayFlow
	route    routing.RouteResult
	stage    string
	err      error
	proxyErr *ProxyError
}

type socks5UDPFlowRequest struct {
	ctx         context.Context
	association *socks5UDPAssociation
	client      *socks5UDPClientWriter
	target      string
}

type socks5UDPFlowOptions struct {
	association *socks5UDPAssociation
	client      *socks5UDPClientWriter
	target      string
	packetConn  net.PacketConn
	upstream    net.Addr
	route       routing.RouteResult
	stage       string
	err         error
}

type socks5UDPRelayFlow struct {
	association *socks5UDPAssociation
	target      string
	packetConn  net.PacketConn
	upstream    net.Addr
	client      *socks5UDPClientWriter
	route       routing.RouteResult
	health      HealthRecorder
	metricsSink MetricsEventSink
	idleTimeout time.Duration

	writeMu            sync.Mutex
	recoveryGeneration uint64
	idleMu             sync.Mutex
	lastActivity       time.Time
	idleTimer          *time.Timer
	retiring           bool
	closeOnce          sync.Once
	failureOnce        sync.Once
	healthOKOnce       sync.Once
	closed             atomic.Bool
}

func (s *Socks5Inbound) newUDPRelayFlow(request socks5UDPFlowRequest) socks5UDPFlowCreateResult {
	destination := M.ParseSocksaddr(request.target)
	plan, routeErr := resolveOutboundPlan(s.tunnel.decisioner, s.tunnel.router, s.tunnel.pool, s.tunnel.chain, routing.RouteOptions{
		PlatformName:    request.association.platformName,
		Account:         request.association.account,
		Target:          request.target,
		RequiredNetwork: N.NetworkUDP,
	})
	if routeErr != nil {
		return socks5UDPFlowCreateResult{
			stage: "udp_route", proxyErr: routeErr, err: errors.New(routeErr.ResinError),
		}
	}
	if plan.IsDirect() {
		packetConn, upstream, err := s.udpDirectDialer.ListenPacket(request.ctx, request.target)
		return s.finishUDPFlowCreate(socks5UDPFlowOptions{
			association: request.association,
			client:      request.client,
			target:      request.target,
			packetConn:  packetConn,
			upstream:    upstream,
			stage:       "udp_direct_listen",
			err:         err,
		})
	}
	routed := plan.Routed
	defer routed.Release()
	packetConn, err := routed.Outbound.ListenPacket(request.ctx, destination)
	var upstream net.Addr = destination
	if destination.IsIP() {
		upstream = destination.UDPAddr()
	}
	result := s.finishUDPFlowCreate(socks5UDPFlowOptions{
		association: request.association,
		client:      request.client,
		target:      request.target,
		packetConn:  packetConn,
		upstream:    upstream,
		route:       routed.Route,
		stage:       "udp_outbound_listen",
		err:         err,
	})
	if result.flow == nil && result.proxyErr != nil {
		recordUDPOutboundListenFailure(s.tunnel.health, routed.Route, request.association.account, s.tunnel.router)
	}
	return result
}

func (s *Socks5Inbound) finishUDPFlowCreate(
	options socks5UDPFlowOptions,
) socks5UDPFlowCreateResult {
	if options.err != nil {
		if options.packetConn != nil {
			_ = options.packetConn.Close()
		}
		return socks5UDPFlowCreateResult{
			route:    options.route,
			stage:    options.stage,
			err:      options.err,
			proxyErr: classifyUpstreamError(options.err),
		}
	}
	if options.packetConn == nil || options.upstream == nil {
		if options.packetConn != nil {
			_ = options.packetConn.Close()
		}
		err := errors.New("UDP packet flow returned nil connection or destination")
		return socks5UDPFlowCreateResult{
			route: options.route, stage: options.stage, err: err, proxyErr: ErrInternalError,
		}
	}
	flow := &socks5UDPRelayFlow{
		association: options.association,
		target:      options.target,
		packetConn:  options.packetConn,
		upstream:    options.upstream,
		client:      options.client,
		route:       options.route,
		health:      s.tunnel.health,
		metricsSink: s.tunnel.metricsSink,
		idleTimeout: options.association.manager.idleTimeout,
	}
	if flow.metricsSink != nil {
		flow.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
	}
	return socks5UDPFlowCreateResult{flow: flow, route: options.route}
}
