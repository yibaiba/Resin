package routing

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
	N "github.com/sagernet/sing/common/network"
)

type DecisionAction string

const (
	DecisionRouted DecisionAction = "ROUTED"
	DecisionDirect DecisionAction = "DIRECT"
	DecisionReject DecisionAction = "REJECT"
)

const (
	RuleHostAny    = "ANY"
	RuleHostExact  = "EXACT"
	RuleHostSuffix = "DOMAIN_SUFFIX"
	RuleHostGlob   = "WILDCARD"
	RuleHostCIDR   = "CIDR"

	RuleNetworkAny     = "ANY"
	RuleActionPlatform = "PLATFORM"
)

type DecisionInput struct {
	PlatformName    string
	Target          string
	RequiredNetwork string
}

type Decision struct {
	Action               DecisionAction `json:"action"`
	PlatformID           string         `json:"platform_id,omitempty"`
	PlatformName         string         `json:"platform_name,omitempty"`
	MatchedRuleID        string         `json:"matched_rule_id,omitempty"`
	MatchedRuleName      string         `json:"matched_rule_name,omitempty"`
	Reason               string         `json:"reason"`
	ExplicitPlatformKept bool           `json:"explicit_platform_kept"`
	Target               Target         `json:"target"`
}

type DecisionPlatformResolver interface {
	GetPlatform(id string) (*platform.Platform, bool)
}

type compiledDecisionRule struct {
	model.RoutingRule
	prefix netip.Prefix
	host   string
}

type decisionSnapshot struct {
	rules []compiledDecisionRule
}

// DecisionEngine owns immutable target-routing rules and environment bypasses.
type DecisionEngine struct {
	bypass   *TargetBypassMatcher
	resolver DecisionPlatformResolver
	snapshot atomic.Pointer[decisionSnapshot]
}

func NewDecisionEngine(bypass []string, resolver DecisionPlatformResolver) *DecisionEngine {
	engine := &DecisionEngine{bypass: NewTargetBypassMatcher(bypass), resolver: resolver}
	engine.snapshot.Store(&decisionSnapshot{})
	return engine
}

func (e *DecisionEngine) ReplaceRules(rules []model.RoutingRule) error {
	compiled, err := compileDecisionRules(rules, e.resolver)
	if err != nil {
		return err
	}
	e.snapshot.Store(&decisionSnapshot{rules: compiled})
	return nil
}

func (e *DecisionEngine) Decide(input DecisionInput) (Decision, error) {
	target, err := ParseTarget(input.Target)
	if err != nil {
		return Decision{}, err
	}
	network, err := normalizeDecisionNetwork(input.RequiredNetwork)
	if err != nil {
		return Decision{}, err
	}
	if e.bypass != nil && e.bypass.ShouldBypass(input.Target) {
		return Decision{Action: DecisionDirect, Reason: "environment_bypass", Target: target}, nil
	}

	explicit := strings.TrimSpace(input.PlatformName) != ""
	for _, rule := range e.snapshot.Load().rules {
		if !rule.Enabled || (explicit && !rule.OverrideExplicitPlatform) {
			continue
		}
		if !rule.matches(target, network) {
			continue
		}
		return e.ruleDecision(rule, target)
	}
	return Decision{
		Action:               DecisionRouted,
		PlatformName:         input.PlatformName,
		Reason:               "requested_or_default_platform",
		ExplicitPlatformKept: explicit,
		Target:               target,
	}, nil
}

func (e *DecisionEngine) ruleDecision(rule compiledDecisionRule, target Target) (Decision, error) {
	decision := Decision{
		Action:          DecisionRouted,
		MatchedRuleID:   rule.ID,
		MatchedRuleName: rule.Name,
		Reason:          "routing_rule",
		Target:          target,
	}
	if rule.Action != RuleActionPlatform {
		decision.Action = DecisionAction(rule.Action)
		return decision, nil
	}
	selected, ok := e.resolver.GetPlatform(rule.PlatformID)
	if !ok {
		return Decision{}, fmt.Errorf("routing rule %s references missing platform", rule.ID)
	}
	decision.PlatformID = selected.ID
	decision.PlatformName = selected.Name
	return decision, nil
}

func compileDecisionRules(
	rules []model.RoutingRule,
	resolver DecisionPlatformResolver,
) ([]compiledDecisionRule, error) {
	compiled := make([]compiledDecisionRule, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		if _, exists := seen[rule.ID]; exists {
			return nil, fmt.Errorf("duplicate routing rule id %q", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		next, err := compileDecisionRule(rule, resolver)
		if err != nil {
			return nil, fmt.Errorf("routing rule %s: %w", rule.ID, err)
		}
		compiled = append(compiled, next)
	}
	slices.SortStableFunc(compiled, func(a, b compiledDecisionRule) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(a.ID, b.ID)
	})
	return compiled, nil
}

func compileDecisionRule(rule model.RoutingRule, resolver DecisionPlatformResolver) (compiledDecisionRule, error) {
	rule.ID = strings.TrimSpace(rule.ID)
	rule.Name = strings.TrimSpace(rule.Name)
	rule.HostType = strings.ToUpper(strings.TrimSpace(rule.HostType))
	rule.Network = strings.ToUpper(strings.TrimSpace(rule.Network))
	rule.Action = strings.ToUpper(strings.TrimSpace(rule.Action))
	rule.PlatformID = strings.TrimSpace(rule.PlatformID)
	if rule.ID == "" || rule.Name == "" {
		return compiledDecisionRule{}, fmt.Errorf("id and name are required")
	}
	if err := validateRulePorts(rule.Ports); err != nil {
		return compiledDecisionRule{}, err
	}
	network := strings.ToUpper(rule.Network)
	if network != RuleNetworkAny && network != "TCP" && network != "UDP" {
		return compiledDecisionRule{}, fmt.Errorf("network must be ANY, tcp, or udp")
	}
	if network == RuleNetworkAny {
		rule.Network = RuleNetworkAny
	} else {
		rule.Network = strings.ToLower(network)
	}
	if rule.Action != RuleActionPlatform && rule.Action != string(DecisionDirect) && rule.Action != string(DecisionReject) {
		return compiledDecisionRule{}, fmt.Errorf("action must be PLATFORM, DIRECT, or REJECT")
	}
	if rule.Action == RuleActionPlatform {
		if rule.PlatformID == "" {
			return compiledDecisionRule{}, fmt.Errorf("platform_id is required for ROUTED")
		}
		if _, ok := resolver.GetPlatform(rule.PlatformID); !ok {
			return compiledDecisionRule{}, fmt.Errorf("platform_id does not exist")
		}
	} else if rule.PlatformID != "" {
		return compiledDecisionRule{}, fmt.Errorf("platform_id is only valid for ROUTED")
	}
	return compileRuleHost(rule)
}

func compileRuleHost(rule model.RoutingRule) (compiledDecisionRule, error) {
	compiled := compiledDecisionRule{RoutingRule: rule}
	value := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(rule.HostValue)), ".")
	switch rule.HostType {
	case RuleHostAny:
		if value != "" {
			return compiledDecisionRule{}, fmt.Errorf("host_value must be empty for ANY")
		}
	case RuleHostExact:
		if value == "" || strings.ContainsAny(value, "*?/:") {
			return compiledDecisionRule{}, fmt.Errorf("invalid exact host")
		}
		compiled.host = value
	case RuleHostSuffix:
		value = strings.TrimPrefix(value, ".")
		if value == "" || strings.ContainsAny(value, "*?/:") {
			return compiledDecisionRule{}, fmt.Errorf("invalid domain suffix")
		}
		compiled.host = value
	case RuleHostGlob:
		if !strings.HasPrefix(value, "*.") || strings.ContainsAny(strings.TrimPrefix(value, "*."), "*?/:") {
			return compiledDecisionRule{}, fmt.Errorf("wildcard must use the form *.example.com")
		}
		compiled.host = strings.TrimPrefix(value, "*.")
	case RuleHostCIDR:
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return compiledDecisionRule{}, fmt.Errorf("invalid CIDR")
		}
		compiled.prefix = prefix.Masked()
	default:
		return compiledDecisionRule{}, fmt.Errorf("unsupported host_type %q", rule.HostType)
	}
	compiled.HostValue = value
	return compiled, nil
}

func validateRulePorts(ports []model.RoutingPortRange) error {
	for index, item := range ports {
		if item.Start == 0 || item.End == 0 || item.Start > item.End {
			return fmt.Errorf("ports[%d] must be an inclusive range between 1 and 65535", index)
		}
	}
	return nil
}

func normalizeDecisionNetwork(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return N.NetworkTCP, nil
	}
	network := strings.ToLower(strings.TrimSpace(raw))
	if network != N.NetworkTCP && network != N.NetworkUDP {
		return "", fmt.Errorf("unsupported required network %q", raw)
	}
	return network, nil
}

func (r compiledDecisionRule) matches(target Target, network string) bool {
	if r.Network != RuleNetworkAny && r.Network != network {
		return false
	}
	if len(r.Ports) > 0 && !matchesRulePort(r.Ports, target.Port) {
		return false
	}
	switch r.HostType {
	case RuleHostAny:
		return true
	case RuleHostExact:
		return target.Host == r.host
	case RuleHostSuffix:
		return target.Host == r.host || strings.HasSuffix(target.Host, "."+r.host)
	case RuleHostGlob:
		return target.Host != r.host && strings.HasSuffix(target.Host, "."+r.host)
	case RuleHostCIDR:
		return target.Addr.IsValid() && r.prefix.Contains(target.Addr)
	default:
		return false
	}
}

func matchesRulePort(ranges []model.RoutingPortRange, port uint16) bool {
	if port == 0 {
		return false
	}
	for _, item := range ranges {
		if port >= item.Start && port <= item.End {
			return true
		}
	}
	return false
}
