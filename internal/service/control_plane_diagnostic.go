package service

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/diagnosticlog"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

const (
	DiagnosticModeDryRun        = "DRY_RUN"
	DiagnosticModeLiveTCP       = "LIVE_TCP"
	defaultDiagnosticTCPTimeout = 5 * time.Second
)

type DiagnosticTCPProber interface {
	ProbeDirect(ctx context.Context, target string) error
	ProbeRouted(ctx context.Context, hash node.Hash, target string) error
}

type RouteDiagnosticRequest struct {
	Mode            string  `json:"mode"`
	PlatformName    string  `json:"platform_name"`
	Account         string  `json:"account"`
	Target          string  `json:"target"`
	RequiredNetwork string  `json:"required_network"`
	Seed            *uint64 `json:"seed"`
	Timeout         string  `json:"timeout"`
}

type RouteDiagnosticResponse struct {
	Mode          string                   `json:"mode"`
	Decision      routing.Decision         `json:"decision"`
	DecisionTrace routing.DecisionTrace    `json:"decision_trace"`
	Route         *routing.RouteDiagnostic `json:"route,omitempty"`
	LiveTCP       *LiveTCPDiagnostic       `json:"live_tcp,omitempty"`
}

type LiveTCPDiagnostic struct {
	Attempted  bool   `json:"attempted"`
	Success    bool   `json:"success"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func (s *ControlPlaneService) DiagnoseRoute(
	ctx context.Context,
	req RouteDiagnosticRequest,
) (*RouteDiagnosticResponse, error) {
	mode := strings.ToUpper(strings.TrimSpace(req.Mode))
	if mode != DiagnosticModeDryRun && mode != DiagnosticModeLiveTCP {
		return nil, invalidArg("mode: must be DRY_RUN or LIVE_TCP")
	}
	decisionEngine, err := s.routingDecisionEngine()
	if err != nil {
		return nil, internal("initialize routing diagnostics", err)
	}
	requiredNetwork := strings.ToLower(strings.TrimSpace(req.RequiredNetwork))
	input := routing.DecisionInput{
		PlatformName: req.PlatformName, Target: req.Target,
		RequiredNetwork: requiredNetwork,
	}
	decision, decisionTrace, err := decisionEngine.Diagnose(input)
	if err != nil {
		return nil, invalidArg(err.Error())
	}
	response := &RouteDiagnosticResponse{
		Mode: mode, Decision: decision, DecisionTrace: decisionTrace,
	}
	if decision.Action == routing.DecisionRouted {
		seed := diagnosticSeed(req.Seed)
		route, routeErr := s.Router.DiagnoseRoute(routing.RouteOptions{
			PlatformName: decision.PlatformName, Account: req.Account,
			Target: req.Target, RequiredNetwork: requiredNetwork,
		}, seed)
		response.Route = &route
		if routeErr != nil && !errors.Is(routeErr, routing.ErrNoAvailableNodes) {
			return nil, internal("diagnose routed selection", routeErr)
		}
	}
	if mode == DiagnosticModeLiveTCP {
		live, svcErr := s.runLiveTCPDiagnostic(ctx, req, response)
		if svcErr != nil {
			return nil, svcErr
		}
		response.LiveTCP = live
	}
	return response, nil
}

func (s *ControlPlaneService) routingDecisionEngine() (*routing.DecisionEngine, error) {
	s.routingRuleMu.Lock()
	defer s.routingRuleMu.Unlock()
	return s.ensureRoutingDecisionEngineLocked()
}

func diagnosticSeed(seed *uint64) uint64 {
	if seed != nil {
		return *seed
	}
	return uint64(time.Now().UnixNano())
}

func (s *ControlPlaneService) runLiveTCPDiagnostic(
	ctx context.Context,
	req RouteDiagnosticRequest,
	response *RouteDiagnosticResponse,
) (*LiveTCPDiagnostic, *ServiceError) {
	if strings.ToLower(strings.TrimSpace(req.RequiredNetwork)) == "udp" {
		return nil, invalidArg("LIVE_TCP requires required_network tcp")
	}
	if response.Decision.Action == routing.DecisionReject {
		return &LiveTCPDiagnostic{Reason: "route_rejected"}, nil
	}
	if s.DiagnosticTCPProber == nil {
		return nil, internal("LIVE_TCP prober is not configured", nil)
	}
	target, svcErr := diagnosticTCPAuthority(response.Decision.Target)
	if svcErr != nil {
		return nil, svcErr
	}
	timeout, svcErr := parseDiagnosticTimeout(req.Timeout)
	if svcErr != nil {
		return nil, svcErr
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	err := s.probeDiagnosticTarget(probeCtx, response, target)
	result := &LiveTCPDiagnostic{
		Attempted: true, Success: err == nil,
		DurationMs: time.Since(started).Milliseconds(),
	}
	if err != nil {
		result.Error = diagnosticlog.Sanitize(err.Error())
	}
	return result, nil
}

func diagnosticTCPAuthority(target routing.Target) (string, *ServiceError) {
	if target.Port == 0 {
		return "", invalidArg("target: LIVE_TCP requires an explicit or scheme-derived port")
	}
	return net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))), nil
}

func parseDiagnosticTimeout(raw string) (time.Duration, *ServiceError) {
	if strings.TrimSpace(raw) == "" {
		return defaultDiagnosticTCPTimeout, nil
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || timeout <= 0 {
		return 0, invalidArg("timeout: must be a positive duration")
	}
	return timeout, nil
}

func (s *ControlPlaneService) probeDiagnosticTarget(
	ctx context.Context,
	response *RouteDiagnosticResponse,
	target string,
) error {
	if response.Decision.Action == routing.DecisionDirect {
		return s.DiagnosticTCPProber.ProbeDirect(ctx, target)
	}
	if response.Route == nil || response.Route.Selection == nil {
		return errors.New("no routed candidate selected")
	}
	hash, err := node.ParseHex(response.Route.Selection.NodeHash)
	if err != nil {
		return err
	}
	return s.DiagnosticTCPProber.ProbeRouted(ctx, hash, target)
}
