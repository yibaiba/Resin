package proxy

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"

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

	writeMu      sync.Mutex
	closeOnce    sync.Once
	failureOnce  sync.Once
	healthOKOnce sync.Once
	closed       atomic.Bool
}

func (s *Socks5Inbound) newUDPRelayFlow(request socks5UDPFlowRequest) socks5UDPFlowCreateResult {
	destination := M.ParseSocksaddr(request.target)
	if s.tunnel.bypass != nil && s.tunnel.bypass.ShouldBypass(request.target) {
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
	routed, routeErr := resolveRoutedOutboundWithOptions(s.tunnel.router, s.tunnel.pool, routing.RouteOptions{
		PlatformName:    request.association.platformName,
		Account:         request.association.account,
		Target:          request.target,
		RequiredNetwork: N.NetworkUDP,
	})
	if routeErr != nil {
		return socks5UDPFlowCreateResult{
			stage: "udp_route", proxyErr: routeErr, err: errors.New(routeErr.Message),
		}
	}
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
	if result.flow == nil && s.tunnel.health != nil {
		recordPassiveResultAsync(s.tunnel.health, routed.Route, false)
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
	}
	if flow.metricsSink != nil {
		flow.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
	}
	return socks5UDPFlowCreateResult{flow: flow, route: options.route}
}

func (f *socks5UDPRelayFlow) start() {
	go f.readResponses()
}

func (f *socks5UDPRelayFlow) writePayload(payload []byte) error {
	if !f.association.touch() {
		return errSocks5UDPAssociationNotFound
	}
	f.writeMu.Lock()
	n, err := f.packetConn.WriteTo(payload, f.upstream)
	f.writeMu.Unlock()
	if n > 0 {
		f.association.recordTraffic(0, int64(n))
		if f.metricsSink != nil {
			f.metricsSink.OnTrafficDelta(0, int64(n))
		}
	}
	if err == nil && n != len(payload) {
		err = io.ErrShortWrite
	}
	if err != nil {
		f.fail("udp_upstream_write", err, true)
		return err
	}
	f.association.recordSuccess()
	return nil
}

func (f *socks5UDPRelayFlow) readResponses() {
	buffer := make([]byte, socks5UDPMaxDatagramSize)
	for {
		n, source, err := f.packetConn.ReadFrom(buffer)
		if err != nil {
			if !f.closed.Load() {
				f.fail("udp_upstream_read", err, true)
			}
			return
		}
		if err := f.relayResponse(buffer[:n], source); err != nil {
			f.fail("udp_client_write", err, false)
			return
		}
	}
}

func (f *socks5UDPRelayFlow) relayResponse(payload []byte, source net.Addr) error {
	if !f.association.touch() {
		return errSocks5UDPAssociationNotFound
	}
	target := f.target
	if sourceAddress := M.SocksaddrFromNet(source); sourceAddress.IsValid() {
		target = sourceAddress.String()
	}
	packet, err := encodeSocks5UDPDatagram(target, payload)
	if err != nil {
		return err
	}
	clientSource := f.association.sourceAddr()
	if !clientSource.IsValid() {
		return errSocks5UDPInvalidAssociationSource
	}
	if _, err := f.client.writeTo(packet, net.UDPAddrFromAddrPort(clientSource)); err != nil {
		return err
	}
	f.association.recordTraffic(int64(len(payload)), 0)
	if f.metricsSink != nil {
		f.metricsSink.OnTrafficDelta(int64(len(payload)), 0)
	}
	f.healthOKOnce.Do(func() {
		if f.health != nil && f.route.PlatformID != "" {
			recordPassiveResultAsync(f.health, f.route, true)
		}
	})
	return nil
}

func (f *socks5UDPRelayFlow) fail(stage string, err error, nodeFailure bool) {
	f.failureOnce.Do(func() {
		log.Printf("SOCKS5 UDP flow failed stage=%s target=%q error=%v", stage, f.target, err)
		f.association.recordFailure(stage, err, classifyUpstreamError(err))
		if nodeFailure && f.health != nil && f.route.PlatformID != "" {
			recordPassiveResultAsync(f.health, f.route, false)
		}
		_ = f.association.removeFlow(f.target, f)
	})
}

func (f *socks5UDPRelayFlow) Close() error {
	var closeErr error
	f.closeOnce.Do(func() {
		f.closed.Store(true)
		closeErr = f.packetConn.Close()
		if f.metricsSink != nil {
			f.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionClose)
		}
	})
	return closeErr
}
