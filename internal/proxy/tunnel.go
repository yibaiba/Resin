package proxy

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
	M "github.com/sagernet/sing/common/metadata"
)

type tunnelDeps struct {
	router      *routing.Router
	pool        outbound.PoolAccessor
	chain       chainOutboundResolver
	decisioner  RouteDecisioner
	health      HealthRecorder
	metricsSink MetricsEventSink
}

type preparedTunnel struct {
	upstreamConn net.Conn
	health       *requestHealthFeedback
}

type connectTunnelRequest struct {
	ctx          context.Context
	deps         tunnelDeps
	platformName string
	account      string
	target       string
	requestID    string
}

type tunnelPrepareResult struct {
	route         routing.RouteResult
	session       *preparedTunnel
	health        *requestHealthFeedback
	proxyErr      *ProxyError
	upstreamStage string
	upstreamErr   error
	canceled      bool
}

func prepareConnectTunnel(request connectTunnelRequest) tunnelPrepareResult {
	plan, routeErr := resolveOutboundPlan(
		request.deps.decisioner,
		request.deps.router,
		request.deps.pool,
		request.deps.chain,
		routing.RouteOptions{
			PlatformName: request.platformName,
			Account:      request.account,
			Target:       request.target,
		},
	)
	if routeErr != nil {
		return tunnelPrepareResult{
			proxyErr:      routeErr,
			upstreamStage: "route",
			upstreamErr:   errors.New(routeErr.ResinError),
		}
	}
	if plan.IsDirect() {
		return prepareDirectConnectTunnel(request.ctx, request.deps, request.target)
	}
	routed := plan.Routed
	health := newRequestHealthFeedback(request.deps.health, routed.Route, request.requestID)

	domain := netutil.ExtractDomain(request.target)
	nodeHashRaw := routed.Route.NodeHash
	if request.deps.health != nil {
		request.deps.health.RecordLatency(nodeHashRaw, domain, nil)
	}

	rawConn, err := routed.Outbound.DialContext(request.ctx, "tcp", M.ParseSocksaddr(request.target))
	if err != nil {
		proxyErr := classifyConnectError(err)
		if proxyErr == nil {
			health.record(passiveHealthNeutral)
			return tunnelPrepareResult{
				route:         routed.Route,
				health:        health,
				upstreamStage: "connect_dial",
				upstreamErr:   err,
				canceled:      true,
			}
		}
		health.recordDialFailure()
		return tunnelPrepareResult{
			route:         routed.Route,
			health:        health,
			proxyErr:      proxyErr,
			upstreamStage: "connect_dial",
			upstreamErr:   err,
		}
	}

	var upstreamBase net.Conn = rawConn
	if request.deps.metricsSink != nil {
		request.deps.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
		upstreamBase = newCountingConn(rawConn, request.deps.metricsSink)
	}

	upstreamConn := newTLSLatencyConn(upstreamBase, func(latency time.Duration) {
		if request.deps.health != nil {
			request.deps.health.RecordLatency(nodeHashRaw, domain, &latency)
		}
	})

	return tunnelPrepareResult{
		route:  routed.Route,
		health: health,
		session: &preparedTunnel{
			upstreamConn: upstreamConn,
			health:       health,
		},
	}
}

func prepareDirectConnectTunnel(ctx context.Context, deps tunnelDeps, target string) tunnelPrepareResult {
	var dialer net.Dialer
	rawConn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		proxyErr := classifyConnectError(err)
		if proxyErr == nil {
			return tunnelPrepareResult{
				upstreamStage: "connect_direct_dial",
				upstreamErr:   err,
				canceled:      true,
			}
		}
		return tunnelPrepareResult{
			proxyErr:      proxyErr,
			upstreamStage: "connect_direct_dial",
			upstreamErr:   err,
		}
	}

	var upstreamConn net.Conn = rawConn
	if deps.metricsSink != nil {
		deps.metricsSink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
		upstreamConn = newCountingConn(rawConn, deps.metricsSink)
	}
	return tunnelPrepareResult{
		session: &preparedTunnel{
			upstreamConn: upstreamConn,
		},
	}
}
