package proxy

import (
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/sagernet/sing-box/adapter"
)

type routedOutbound struct {
	Route    routing.RouteResult
	Outbound adapter.Outbound
}

type RouteDecisioner interface {
	Decide(routing.DecisionInput) (routing.Decision, error)
}

type outboundPlan struct {
	Decision routing.Decision
	Routed   routedOutbound
}

func (p outboundPlan) IsDirect() bool {
	return p.Decision.Action == routing.DecisionDirect
}

type compatibilityDecisioner struct {
	bypass *routing.TargetBypassMatcher
}

func newCompatibilityDecisioner(patterns []string) RouteDecisioner {
	return compatibilityDecisioner{bypass: routing.NewTargetBypassMatcher(patterns)}
}

func (d compatibilityDecisioner) Decide(input routing.DecisionInput) (routing.Decision, error) {
	target, err := routing.ParseTarget(input.Target)
	if err != nil {
		return routing.Decision{}, err
	}
	if d.bypass != nil && d.bypass.ShouldBypass(input.Target) {
		return routing.Decision{Action: routing.DecisionDirect, Reason: "environment_bypass", Target: target}, nil
	}
	return routing.Decision{
		Action: routing.DecisionRouted, PlatformName: input.PlatformName,
		Reason: "requested_or_default_platform", Target: target,
	}, nil
}

func resolveOutboundPlan(
	decisioner RouteDecisioner,
	router *routing.Router,
	pool outbound.PoolAccessor,
	options routing.RouteOptions,
) (outboundPlan, *ProxyError) {
	decision, err := decisioner.Decide(routing.DecisionInput{
		PlatformName: options.PlatformName, Target: options.Target,
		RequiredNetwork: options.RequiredNetwork,
	})
	if err != nil {
		return outboundPlan{}, ErrInvalidHost
	}
	switch decision.Action {
	case routing.DecisionDirect:
		return outboundPlan{Decision: decision}, nil
	case routing.DecisionReject:
		return outboundPlan{Decision: decision}, ErrRouteRejected
	case routing.DecisionRouted:
		options.PlatformName = decision.PlatformName
		routed, routeErr := resolveRoutedOutboundWithOptions(router, pool, options)
		if routeErr != nil {
			return outboundPlan{Decision: decision}, routeErr
		}
		return outboundPlan{Decision: decision, Routed: routed}, nil
	default:
		return outboundPlan{}, ErrInternalError
	}
}

func resolveRoutedOutbound(
	router *routing.Router,
	pool outbound.PoolAccessor,
	platformName string,
	account string,
	target string,
) (routedOutbound, *ProxyError) {
	return resolveRoutedOutboundWithOptions(router, pool, routing.RouteOptions{
		PlatformName: platformName,
		Account:      account,
		Target:       target,
	})
}

func resolveRoutedOutboundWithOptions(
	router *routing.Router,
	pool outbound.PoolAccessor,
	options routing.RouteOptions,
) (routedOutbound, *ProxyError) {
	result, err := router.RouteWithOptions(options)
	if err != nil {
		return routedOutbound{}, mapRouteError(err)
	}

	entry, ok := pool.GetEntry(result.NodeHash)
	if !ok {
		return routedOutbound{}, ErrNoAvailableNodes
	}
	obPtr := entry.Outbound.Load()
	if obPtr == nil {
		return routedOutbound{}, ErrNoAvailableNodes
	}

	return routedOutbound{
		Route:    result,
		Outbound: *obPtr,
	}, nil
}
