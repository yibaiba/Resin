package service

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/state"
	"github.com/google/uuid"
)

type RoutingRuleRequest struct {
	Name                     string                   `json:"name"`
	Enabled                  *bool                    `json:"enabled"`
	HostType                 string                   `json:"host_type"`
	HostValue                string                   `json:"host_value"`
	Ports                    []model.RoutingPortRange `json:"ports"`
	Network                  string                   `json:"network"`
	Action                   string                   `json:"action"`
	PlatformID               string                   `json:"platform_id"`
	OverrideExplicitPlatform bool                     `json:"override_explicit_platform"`
}

type UpdateRoutingRuleRequest struct {
	Name                     *string                   `json:"name"`
	Enabled                  *bool                     `json:"enabled"`
	HostType                 *string                   `json:"host_type"`
	HostValue                *string                   `json:"host_value"`
	Ports                    *[]model.RoutingPortRange `json:"ports"`
	Network                  *string                   `json:"network"`
	Action                   *string                   `json:"action"`
	PlatformID               *string                   `json:"platform_id"`
	OverrideExplicitPlatform *bool                     `json:"override_explicit_platform"`
}

type RoutingRuleResponse struct {
	ID                       string                   `json:"id"`
	Name                     string                   `json:"name"`
	Enabled                  bool                     `json:"enabled"`
	Priority                 int                      `json:"priority"`
	HostType                 string                   `json:"host_type"`
	HostValue                string                   `json:"host_value"`
	Ports                    []model.RoutingPortRange `json:"ports"`
	Network                  string                   `json:"network"`
	Action                   string                   `json:"action"`
	PlatformID               string                   `json:"platform_id,omitempty"`
	OverrideExplicitPlatform bool                     `json:"override_explicit_platform"`
	CreatedAt                string                   `json:"created_at"`
	UpdatedAt                string                   `json:"updated_at"`
}

func routingRuleResponse(rule model.RoutingRule) RoutingRuleResponse {
	ports := append([]model.RoutingPortRange(nil), rule.Ports...)
	if ports == nil {
		ports = []model.RoutingPortRange{}
	}
	return RoutingRuleResponse{
		ID: rule.ID, Name: rule.Name, Enabled: rule.Enabled, Priority: rule.Priority,
		HostType: rule.HostType, HostValue: rule.HostValue, Ports: ports,
		Network: rule.Network, Action: rule.Action, PlatformID: rule.PlatformID,
		OverrideExplicitPlatform: rule.OverrideExplicitPlatform,
		CreatedAt:                time.Unix(0, rule.CreatedAtNs).UTC().Format(time.RFC3339Nano),
		UpdatedAt:                time.Unix(0, rule.UpdatedAtNs).UTC().Format(time.RFC3339Nano),
	}
}

// InitializeRoutingDecisions loads persisted rules into the immutable runtime snapshot.
func (s *ControlPlaneService) InitializeRoutingDecisions() error {
	s.routingRuleMu.Lock()
	defer s.routingRuleMu.Unlock()
	rules, err := s.Engine.ListRoutingRules()
	if err != nil {
		return fmt.Errorf("list routing rules: %w", err)
	}
	engine, err := s.ensureRoutingDecisionEngineLocked()
	if err != nil {
		return err
	}
	return engine.ReplaceRules(rules)
}

func (s *ControlPlaneService) ensureRoutingDecisionEngineLocked() (*routing.DecisionEngine, error) {
	if s.RoutingDecisions != nil {
		return s.RoutingDecisions, nil
	}
	if s.Pool == nil {
		return nil, fmt.Errorf("routing decision engine requires node pool")
	}
	var bypass []string
	if s.EnvCfg != nil {
		bypass = s.EnvCfg.ProxyBypassRules
	}
	s.RoutingDecisions = routing.NewDecisionEngine(bypass, s.Pool)
	return s.RoutingDecisions, nil
}

func (s *ControlPlaneService) ListRoutingRules() ([]RoutingRuleResponse, error) {
	rules, err := s.Engine.ListRoutingRules()
	if err != nil {
		return nil, internal("list routing rules", err)
	}
	responses := make([]RoutingRuleResponse, len(rules))
	for index, rule := range rules {
		responses[index] = routingRuleResponse(rule)
	}
	return responses, nil
}

func (s *ControlPlaneService) GetRoutingRule(id string) (*RoutingRuleResponse, error) {
	rules, err := s.Engine.ListRoutingRules()
	if err != nil {
		return nil, internal("list routing rules", err)
	}
	for _, rule := range rules {
		if rule.ID == id {
			response := routingRuleResponse(rule)
			return &response, nil
		}
	}
	return nil, notFound("routing rule not found")
}

func (s *ControlPlaneService) CreateRoutingRule(req RoutingRuleRequest) (*RoutingRuleResponse, error) {
	s.routingRuleMu.Lock()
	defer s.routingRuleMu.Unlock()
	rules, err := s.Engine.ListRoutingRules()
	if err != nil {
		return nil, internal("list routing rules", err)
	}
	now := time.Now().UnixNano()
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule := model.RoutingRule{
		ID: uuid.NewString(), Name: req.Name, Enabled: enabled, Priority: len(rules),
		HostType: req.HostType, HostValue: req.HostValue, Ports: req.Ports,
		Network: req.Network, Action: req.Action, PlatformID: req.PlatformID,
		OverrideExplicitPlatform: req.OverrideExplicitPlatform,
		CreatedAtNs:              now, UpdatedAtNs: now,
	}
	rules = append(rules, normalizeRoutingRule(rule))
	if err := s.persistRoutingRulesLocked(rules); err != nil {
		return nil, err
	}
	response := routingRuleResponse(rules[len(rules)-1])
	return &response, nil
}

func normalizeRoutingRule(rule model.RoutingRule) model.RoutingRule {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.HostType = strings.ToUpper(strings.TrimSpace(rule.HostType))
	rule.HostValue = strings.TrimSpace(rule.HostValue)
	rule.Network = strings.ToUpper(strings.TrimSpace(rule.Network))
	if rule.Network == "" {
		rule.Network = routing.RuleNetworkAny
	}
	rule.Action = strings.ToUpper(strings.TrimSpace(rule.Action))
	rule.PlatformID = strings.TrimSpace(rule.PlatformID)
	if rule.Ports == nil {
		rule.Ports = []model.RoutingPortRange{}
	}
	return rule
}

func (s *ControlPlaneService) UpdateRoutingRule(
	id string,
	req UpdateRoutingRuleRequest,
) (*RoutingRuleResponse, error) {
	s.routingRuleMu.Lock()
	defer s.routingRuleMu.Unlock()
	rules, err := s.Engine.ListRoutingRules()
	if err != nil {
		return nil, internal("list routing rules", err)
	}
	index := slices.IndexFunc(rules, func(rule model.RoutingRule) bool { return rule.ID == id })
	if index < 0 {
		return nil, notFound("routing rule not found")
	}
	if !applyRoutingRulePatch(&rules[index], req) {
		return nil, invalidArg("empty patch")
	}
	rules[index] = normalizeRoutingRule(rules[index])
	rules[index].UpdatedAtNs = time.Now().UnixNano()
	if err := s.persistRoutingRulesLocked(rules); err != nil {
		return nil, err
	}
	response := routingRuleResponse(rules[index])
	return &response, nil
}

func applyRoutingRulePatch(rule *model.RoutingRule, req UpdateRoutingRuleRequest) bool {
	changed := false
	set := func(ok bool, apply func()) {
		if ok {
			changed = true
			apply()
		}
	}
	set(req.Name != nil, func() { rule.Name = *req.Name })
	set(req.Enabled != nil, func() { rule.Enabled = *req.Enabled })
	set(req.HostType != nil, func() { rule.HostType = *req.HostType })
	set(req.HostValue != nil, func() { rule.HostValue = *req.HostValue })
	set(req.Ports != nil, func() { rule.Ports = append([]model.RoutingPortRange(nil), (*req.Ports)...) })
	set(req.Network != nil, func() { rule.Network = *req.Network })
	set(req.Action != nil, func() { rule.Action = *req.Action })
	set(req.PlatformID != nil, func() { rule.PlatformID = *req.PlatformID })
	set(req.OverrideExplicitPlatform != nil, func() { rule.OverrideExplicitPlatform = *req.OverrideExplicitPlatform })
	return changed
}

func (s *ControlPlaneService) DeleteRoutingRule(id string) error {
	s.routingRuleMu.Lock()
	defer s.routingRuleMu.Unlock()
	rules, err := s.Engine.ListRoutingRules()
	if err != nil {
		return internal("list routing rules", err)
	}
	index := slices.IndexFunc(rules, func(rule model.RoutingRule) bool { return rule.ID == id })
	if index < 0 {
		return notFound("routing rule not found")
	}
	rules = append(rules[:index], rules[index+1:]...)
	resequenceRoutingRules(rules)
	return s.persistRoutingRulesLocked(rules)
}

func (s *ControlPlaneService) ReorderRoutingRules(ids []string) error {
	s.routingRuleMu.Lock()
	defer s.routingRuleMu.Unlock()
	rules, err := s.Engine.ListRoutingRules()
	if err != nil {
		return internal("list routing rules", err)
	}
	if len(ids) != len(rules) {
		return invalidArg("rule_ids must contain every routing rule exactly once")
	}
	byID := make(map[string]model.RoutingRule, len(rules))
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	ordered := make([]model.RoutingRule, 0, len(rules))
	for _, id := range ids {
		rule, ok := byID[id]
		if !ok {
			return invalidArg("rule_ids contains an unknown or duplicate id")
		}
		delete(byID, id)
		ordered = append(ordered, rule)
	}
	resequenceRoutingRules(ordered)
	return s.persistRoutingRulesLocked(ordered)
}

func resequenceRoutingRules(rules []model.RoutingRule) {
	now := time.Now().UnixNano()
	for index := range rules {
		if rules[index].Priority != index {
			rules[index].Priority = index
			rules[index].UpdatedAtNs = now
		}
	}
}

func (s *ControlPlaneService) persistRoutingRulesLocked(rules []model.RoutingRule) error {
	engine, err := s.ensureRoutingDecisionEngineLocked()
	if err != nil {
		return internal("initialize routing decisions", err)
	}
	validator := routing.NewDecisionEngine(nil, s.Pool)
	if err := validator.ReplaceRules(rules); err != nil {
		return invalidArg(err.Error())
	}
	if err := s.Engine.ReplaceRoutingRules(rules); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return conflict("routing rule conflicts with existing data")
		}
		return internal("persist routing rules", err)
	}
	if err := engine.ReplaceRules(rules); err != nil {
		return internal("publish routing rules", err)
	}
	return nil
}

func (s *ControlPlaneService) PreviewRoutingDecision(input routing.DecisionInput) (routing.Decision, error) {
	s.routingRuleMu.Lock()
	defer s.routingRuleMu.Unlock()
	engine, err := s.ensureRoutingDecisionEngineLocked()
	if err != nil {
		return routing.Decision{}, internal("initialize routing decisions", err)
	}
	decision, err := engine.Decide(input)
	if err != nil {
		return routing.Decision{}, invalidArg(err.Error())
	}
	return decision, nil
}
