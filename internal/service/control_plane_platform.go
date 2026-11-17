package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/state"
)

// ------------------------------------------------------------------
// Platform
// ------------------------------------------------------------------

// PlatformResponse is the API response model for a platform.
type PlatformResponse struct {
	ID                               string   `json:"id"`
	Name                             string   `json:"name"`
	StickyTTL                        string   `json:"sticky_ttl"`
	RegexFilters                     []string `json:"regex_filters"`
	RegionFilters                    []string `json:"region_filters"`
	SubscriptionIDs                  []string `json:"subscription_ids"`
	RoutableNodeCount                int      `json:"routable_node_count"`
	ReverseProxyMissAction           string   `json:"reverse_proxy_miss_action"`
	ReverseProxyEmptyAccountBehavior string   `json:"reverse_proxy_empty_account_behavior"`
	ReverseProxyFixedAccountHeader   string   `json:"reverse_proxy_fixed_account_header"`
	AllocationPolicy                 string   `json:"allocation_policy"`
	PassiveCircuitBreakerDisabled    bool     `json:"passive_circuit_breaker_disabled"`
	FallbackPlatformIDs              []string `json:"fallback_platform_ids"`
	ChainPlatformIDs                 []string `json:"chain_platform_ids"`
	RotationInterval                 string   `json:"rotation_interval"`
	RotationStrategy                 string   `json:"rotation_strategy"`
	UpdatedAt                        string   `json:"updated_at"`
}

func platformToResponse(p model.Platform) PlatformResponse {
	behavior := normalizePlatformEmptyAccountBehavior(p.ReverseProxyEmptyAccountBehavior)
	fixedHeader := normalizeHeaderFieldName(p.ReverseProxyFixedAccountHeader)
	return PlatformResponse{
		ID:                               p.ID,
		Name:                             p.Name,
		StickyTTL:                        time.Duration(p.StickyTTLNs).String(),
		RegexFilters:                     append([]string{}, p.RegexFilters...),
		RegionFilters:                    append([]string{}, p.RegionFilters...),
		SubscriptionIDs:                  append([]string{}, p.SubscriptionIDs...),
		RoutableNodeCount:                0,
		ReverseProxyMissAction:           p.ReverseProxyMissAction,
		ReverseProxyEmptyAccountBehavior: behavior,
		ReverseProxyFixedAccountHeader:   fixedHeader,
		AllocationPolicy:                 p.AllocationPolicy,
		PassiveCircuitBreakerDisabled:    p.PassiveCircuitBreakerDisabled,
		FallbackPlatformIDs:              append([]string(nil), p.FallbackPlatformIDs...),
		ChainPlatformIDs:                 append([]string(nil), p.ChainPlatformIDs...),
		RotationInterval:                 time.Duration(p.RotationIntervalNs).String(),
		RotationStrategy:                 normalizeRotationStrategy(p.RotationStrategy),
		UpdatedAt:                        time.Unix(0, p.UpdatedAtNs).UTC().Format(time.RFC3339Nano),
	}
}

func (s *ControlPlaneService) withRoutableNodeCount(resp PlatformResponse) PlatformResponse {
	if s == nil || s.Pool == nil {
		return resp
	}
	plat, ok := s.Pool.GetPlatform(resp.ID)
	if !ok || plat == nil {
		return resp
	}
	resp.RoutableNodeCount = plat.View().Size()
	return resp
}

type platformConfig struct {
	Name                             string
	StickyTTLNs                      int64
	RegexFilters                     []string
	RegionFilters                    []string
	SubscriptionIDs                  []string
	ReverseProxyMissAction           string
	ReverseProxyEmptyAccountBehavior string
	ReverseProxyFixedAccountHeader   string
	AllocationPolicy                 string
	PassiveCircuitBreakerDisabled    bool
	FallbackPlatformIDs              []string
	ChainPlatformIDs                 []string
	RotationIntervalNs               int64
	RotationStrategy                 string
}

func normalizePlatformMissAction(raw string) string {
	normalized := platform.NormalizeReverseProxyMissAction(raw)
	if normalized == "" {
		return ""
	}
	return string(normalized)
}

func normalizePlatformEmptyAccountBehavior(raw string) string {
	if platform.ReverseProxyEmptyAccountBehavior(raw).IsValid() {
		return raw
	}
	return string(platform.ReverseProxyEmptyAccountBehaviorRandom)
}

func (s *ControlPlaneService) defaultPlatformConfig(name string) platformConfig {
	return platformConfig{
		Name:                   name,
		StickyTTLNs:            int64(s.EnvCfg.DefaultPlatformStickyTTL),
		RegexFilters:           append([]string(nil), s.EnvCfg.DefaultPlatformRegexFilters...),
		RegionFilters:          append([]string(nil), s.EnvCfg.DefaultPlatformRegionFilters...),
		SubscriptionIDs:        []string{},
		ReverseProxyMissAction: s.EnvCfg.DefaultPlatformReverseProxyMissAction,
		ReverseProxyEmptyAccountBehavior: normalizePlatformEmptyAccountBehavior(
			s.EnvCfg.DefaultPlatformReverseProxyEmptyAccountBehavior,
		),
		ReverseProxyFixedAccountHeader: normalizeHeaderFieldName(
			s.EnvCfg.DefaultPlatformReverseProxyFixedAccountHeader,
		),
		AllocationPolicy: s.EnvCfg.DefaultPlatformAllocationPolicy,
		RotationStrategy: string(platform.RotationStrategyAny),
	}
}

func platformConfigFromModel(mp model.Platform) platformConfig {
	return platformConfig{
		Name:                             mp.Name,
		StickyTTLNs:                      mp.StickyTTLNs,
		RegexFilters:                     append([]string(nil), mp.RegexFilters...),
		RegionFilters:                    append([]string(nil), mp.RegionFilters...),
		SubscriptionIDs:                  append([]string{}, mp.SubscriptionIDs...),
		ReverseProxyMissAction:           mp.ReverseProxyMissAction,
		ReverseProxyEmptyAccountBehavior: normalizePlatformEmptyAccountBehavior(mp.ReverseProxyEmptyAccountBehavior),
		ReverseProxyFixedAccountHeader:   normalizeHeaderFieldName(mp.ReverseProxyFixedAccountHeader),
		AllocationPolicy:                 mp.AllocationPolicy,
		PassiveCircuitBreakerDisabled:    mp.PassiveCircuitBreakerDisabled,
		FallbackPlatformIDs:              append([]string(nil), mp.FallbackPlatformIDs...),
		ChainPlatformIDs:                 append([]string(nil), mp.ChainPlatformIDs...),
		RotationIntervalNs:               mp.RotationIntervalNs,
		RotationStrategy:                 normalizeRotationStrategy(mp.RotationStrategy),
	}
}

func (cfg platformConfig) toModel(id string, updatedAtNs int64) model.Platform {
	return model.Platform{
		ID:                               id,
		Name:                             cfg.Name,
		StickyTTLNs:                      cfg.StickyTTLNs,
		RegexFilters:                     append([]string(nil), cfg.RegexFilters...),
		RegionFilters:                    append([]string(nil), cfg.RegionFilters...),
		SubscriptionIDs:                  append([]string{}, cfg.SubscriptionIDs...),
		ReverseProxyMissAction:           cfg.ReverseProxyMissAction,
		ReverseProxyEmptyAccountBehavior: cfg.ReverseProxyEmptyAccountBehavior,
		ReverseProxyFixedAccountHeader:   cfg.ReverseProxyFixedAccountHeader,
		AllocationPolicy:                 cfg.AllocationPolicy,
		PassiveCircuitBreakerDisabled:    cfg.PassiveCircuitBreakerDisabled,
		FallbackPlatformIDs:              append([]string(nil), cfg.FallbackPlatformIDs...),
		ChainPlatformIDs:                 append([]string(nil), cfg.ChainPlatformIDs...),
		RotationIntervalNs:               cfg.RotationIntervalNs,
		RotationStrategy:                 cfg.RotationStrategy,
		UpdatedAtNs:                      updatedAtNs,
	}
}

func (cfg platformConfig) toRuntime(id string) (*platform.Platform, error) {
	compiledRegexFilters, err := platform.CompileRegexFilters(cfg.RegexFilters)
	if err != nil {
		return nil, err
	}
	configured := platform.NewConfiguredPlatform(
		id,
		cfg.Name,
		compiledRegexFilters,
		cfg.RegionFilters,
		cfg.StickyTTLNs,
		cfg.ReverseProxyMissAction,
		cfg.ReverseProxyEmptyAccountBehavior,
		cfg.ReverseProxyFixedAccountHeader,
		cfg.AllocationPolicy,
		cfg.PassiveCircuitBreakerDisabled,
	)
	configured.FallbackPlatformIDs = append([]string(nil), cfg.FallbackPlatformIDs...)
	configured.ChainPlatformIDs = append([]string(nil), cfg.ChainPlatformIDs...)
	configured.SubscriptionIDs = append([]string{}, cfg.SubscriptionIDs...)
	configured.RotationIntervalNs = cfg.RotationIntervalNs
	configured.RotationStrategy = platform.ParseRotationStrategy(cfg.RotationStrategy)
	return configured, nil
}

func normalizeRotationStrategy(raw string) string {
	strategy := platform.ParseRotationStrategy(raw)
	if strategy == "" {
		strategy = platform.RotationStrategyAny
	}
	return string(strategy)
}

func validatePlatformMissAction(raw string) *ServiceError {
	if normalizePlatformMissAction(raw) != "" {
		return nil
	}
	return invalidArg(fmt.Sprintf(
		"reverse_proxy_miss_action: must be %s or %s",
		platform.ReverseProxyMissActionTreatAsEmpty,
		platform.ReverseProxyMissActionReject,
	))
}

func validatePlatformEmptyAccountBehavior(raw string) *ServiceError {
	if platform.ReverseProxyEmptyAccountBehavior(raw).IsValid() {
		return nil
	}
	return invalidArg(fmt.Sprintf(
		"reverse_proxy_empty_account_behavior: must be %s, %s, or %s",
		platform.ReverseProxyEmptyAccountBehaviorRandom,
		platform.ReverseProxyEmptyAccountBehaviorFixedHeader,
		platform.ReverseProxyEmptyAccountBehaviorAccountHeaderRule,
	))
}

func normalizeHeaderFieldName(raw string) string {
	normalized, _, err := platform.NormalizeFixedAccountHeaders(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return normalized
}

func validatePlatformEmptyAccountConfig(cfg *platformConfig) *ServiceError {
	if cfg == nil {
		return invalidArg("platform config is required")
	}
	if err := validatePlatformEmptyAccountBehavior(cfg.ReverseProxyEmptyAccountBehavior); err != nil {
		return err
	}
	normalizedFixedHeaders, fixedHeaders, err := platform.NormalizeFixedAccountHeaders(cfg.ReverseProxyFixedAccountHeader)
	if err != nil {
		return invalidArg("reverse_proxy_fixed_account_header: " + err.Error())
	}
	cfg.ReverseProxyFixedAccountHeader = normalizedFixedHeaders
	if cfg.ReverseProxyEmptyAccountBehavior == string(platform.ReverseProxyEmptyAccountBehaviorFixedHeader) &&
		len(fixedHeaders) == 0 {
		return invalidArg(
			"reverse_proxy_fixed_account_header: required when reverse_proxy_empty_account_behavior is FIXED_HEADER",
		)
	}
	return nil
}

func validatePlatformAllocationPolicy(raw string) *ServiceError {
	if platform.AllocationPolicy(raw).IsValid() {
		return nil
	}
	return invalidArg(fmt.Sprintf(
		"allocation_policy: must be %s, %s, or %s",
		platform.AllocationPolicyBalanced,
		platform.AllocationPolicyPreferLowLatency,
		platform.AllocationPolicyPreferIdleIP,
	))
}

func setPlatformStickyTTL(cfg *platformConfig, d time.Duration) *ServiceError {
	if d <= 0 {
		return invalidArg("sticky_ttl: must be > 0")
	}
	cfg.StickyTTLNs = int64(d)
	return nil
}

func setPlatformRotationInterval(cfg *platformConfig, interval time.Duration) *ServiceError {
	if interval < 0 {
		return invalidArg("rotation_interval: must be >= 0")
	}
	cfg.RotationIntervalNs = int64(interval)
	return nil
}

func setPlatformRotationStrategy(cfg *platformConfig, raw string) *ServiceError {
	strategy := platform.ParseRotationStrategy(raw)
	if strategy == "" {
		return invalidArg("rotation_strategy: must be ANY, SAME_IP, PREFER_NEW_IP, or REQUIRE_NEW_IP")
	}
	cfg.RotationStrategy = string(strategy)
	return nil
}

func setPlatformMissAction(cfg *platformConfig, missAction string) *ServiceError {
	if err := validatePlatformMissAction(missAction); err != nil {
		return err
	}
	cfg.ReverseProxyMissAction = normalizePlatformMissAction(missAction)
	return nil
}

func setPlatformEmptyAccountBehavior(cfg *platformConfig, behavior string) *ServiceError {
	if err := validatePlatformEmptyAccountBehavior(behavior); err != nil {
		return err
	}
	cfg.ReverseProxyEmptyAccountBehavior = behavior
	return nil
}

func (s *ControlPlaneService) normalizeAndValidateSubscriptionIDs(ids []string) ([]string, *ServiceError) {
	normalized, err := platform.NormalizeSubscriptionIDs(ids)
	if err != nil {
		return nil, invalidArg(err.Error())
	}
	if s == nil {
		return normalized, nil
	}
	for _, id := range normalized {
		if s.SubMgr == nil || s.SubMgr.Lookup(id) == nil {
			return nil, invalidArg(fmt.Sprintf("subscription_ids: subscription %q does not exist", id))
		}
	}
	return normalized, nil
}

func setPlatformAllocationPolicy(cfg *platformConfig, policy string) *ServiceError {
	if err := validatePlatformAllocationPolicy(policy); err != nil {
		return err
	}
	cfg.AllocationPolicy = policy
	return nil
}

func validatePlatformConfig(cfg *platformConfig, validateRegionFilters bool) *ServiceError {
	if validateRegionFilters {
		if err := platform.ValidateRegionFilters(cfg.RegionFilters); err != nil {
			return invalidArg(err.Error())
		}
	}
	if err := validatePlatformEmptyAccountConfig(cfg); err != nil {
		return err
	}
	if cfg.RotationIntervalNs > 0 && cfg.RotationIntervalNs >= cfg.StickyTTLNs {
		return invalidArg("rotation_interval: must be less than sticky_ttl")
	}
	if err := setPlatformRotationStrategy(cfg, cfg.RotationStrategy); err != nil {
		return err
	}
	return nil
}

func (s *ControlPlaneService) compileAndUpsertPlatform(id string, cfg platformConfig) (model.Platform, *platform.Platform, *ServiceError) {
	if err := platform.ValidatePlatformName(cfg.Name); err != nil {
		return model.Platform{}, nil, invalidArg("name: " + err.Error())
	}

	for index := range cfg.FallbackPlatformIDs {
		cfg.FallbackPlatformIDs[index] = strings.TrimSpace(cfg.FallbackPlatformIDs[index])
	}
	for index := range cfg.ChainPlatformIDs {
		cfg.ChainPlatformIDs[index] = strings.TrimSpace(cfg.ChainPlatformIDs[index])
	}
	plat, err := cfg.toRuntime(id)
	if err != nil {
		return model.Platform{}, nil, invalidArg(err.Error())
	}
	mp := cfg.toModel(id, time.Now().UnixNano())
	if err := s.validatePlatformFallbackGraph(mp); err != nil {
		return model.Platform{}, nil, err
	}
	if err := s.validatePlatformChainGraph(mp); err != nil {
		return model.Platform{}, nil, err
	}
	if err := s.Engine.UpsertPlatform(mp); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return model.Platform{}, nil, conflict("platform name already exists")
		}
		if strings.HasPrefix(err.Error(), "platform name: ") {
			return model.Platform{}, nil, invalidArg("name: " + strings.TrimPrefix(err.Error(), "platform name: "))
		}
		return model.Platform{}, nil, internal("persist platform", err)
	}
	return mp, plat, nil
}

// ListPlatforms returns all platforms from the database.
func (s *ControlPlaneService) ListPlatforms() ([]PlatformResponse, error) {
	platforms, err := s.Engine.ListPlatforms()
	if err != nil {
		return nil, internal("list platforms", err)
	}
	resp := make([]PlatformResponse, len(platforms))
	for i, p := range platforms {
		resp[i] = s.withRoutableNodeCount(platformToResponse(p))
	}
	return resp, nil
}

func (s *ControlPlaneService) getPlatformModel(id string) (*model.Platform, error) {
	p, err := s.Engine.GetPlatform(id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, notFound("platform not found")
		}
		return nil, internal("get platform", err)
	}
	return p, nil
}

// GetPlatform returns a single platform by ID.
func (s *ControlPlaneService) GetPlatform(id string) (*PlatformResponse, error) {
	mp, err := s.getPlatformModel(id)
	if err != nil {
		return nil, err
	}
	r := s.withRoutableNodeCount(platformToResponse(*mp))
	return &r, nil
}

// CreatePlatformRequest holds create platform parameters.
type CreatePlatformRequest struct {
	Name                             *string  `json:"name"`
	StickyTTL                        *string  `json:"sticky_ttl"`
	RegexFilters                     []string `json:"regex_filters"`
	RegionFilters                    []string `json:"region_filters"`
	SubscriptionIDs                  []string `json:"subscription_ids"`
	ReverseProxyMissAction           *string  `json:"reverse_proxy_miss_action"`
	ReverseProxyEmptyAccountBehavior *string  `json:"reverse_proxy_empty_account_behavior"`
	ReverseProxyFixedAccountHeader   *string  `json:"reverse_proxy_fixed_account_header"`
	AllocationPolicy                 *string  `json:"allocation_policy"`
	PassiveCircuitBreakerDisabled    *bool    `json:"passive_circuit_breaker_disabled"`
	FallbackPlatformIDs              []string `json:"fallback_platform_ids"`
	ChainPlatformIDs                 []string `json:"chain_platform_ids"`
	RotationInterval                 *string  `json:"rotation_interval"`
	RotationStrategy                 *string  `json:"rotation_strategy"`
}

// CreatePlatform creates a new platform.
func (s *ControlPlaneService) CreatePlatform(req CreatePlatformRequest) (*PlatformResponse, error) {
	s.platformMu.Lock()
	defer s.platformMu.Unlock()

	// Validate name.
	if req.Name == nil {
		return nil, invalidArg("name is required")
	}
	name := platform.NormalizePlatformName(*req.Name)
	if name == "" {
		return nil, invalidArg("name is required")
	}
	if err := platform.ValidatePlatformName(name); err != nil {
		return nil, invalidArg("name: " + err.Error())
	}
	if name == platform.DefaultPlatformName {
		return nil, conflict("cannot use reserved name 'Default'")
	}

	// Apply defaults from env and overlay request fields.
	cfg := s.defaultPlatformConfig(name)
	if req.StickyTTL != nil {
		d, err := time.ParseDuration(*req.StickyTTL)
		if err != nil {
			return nil, invalidArg("sticky_ttl: " + err.Error())
		}
		if err := setPlatformStickyTTL(&cfg, d); err != nil {
			return nil, err
		}
	}
	if req.RegexFilters != nil {
		cfg.RegexFilters = req.RegexFilters
	}
	if req.RegionFilters != nil {
		cfg.RegionFilters = req.RegionFilters
	}
	if req.SubscriptionIDs != nil {
		cfg.SubscriptionIDs = req.SubscriptionIDs
	}
	if req.ReverseProxyMissAction != nil {
		if err := setPlatformMissAction(&cfg, *req.ReverseProxyMissAction); err != nil {
			return nil, err
		}
	}
	if req.ReverseProxyEmptyAccountBehavior != nil {
		if err := setPlatformEmptyAccountBehavior(&cfg, *req.ReverseProxyEmptyAccountBehavior); err != nil {
			return nil, err
		}
	}
	if req.ReverseProxyFixedAccountHeader != nil {
		cfg.ReverseProxyFixedAccountHeader = *req.ReverseProxyFixedAccountHeader
	}
	if req.AllocationPolicy != nil {
		if err := setPlatformAllocationPolicy(&cfg, *req.AllocationPolicy); err != nil {
			return nil, err
		}
	}
	if req.PassiveCircuitBreakerDisabled != nil {
		cfg.PassiveCircuitBreakerDisabled = *req.PassiveCircuitBreakerDisabled
	}
	if req.FallbackPlatformIDs != nil {
		cfg.FallbackPlatformIDs = append([]string(nil), req.FallbackPlatformIDs...)
	}
	if req.ChainPlatformIDs != nil {
		cfg.ChainPlatformIDs = append([]string(nil), req.ChainPlatformIDs...)
	}
	if req.RotationInterval != nil {
		interval, err := time.ParseDuration(*req.RotationInterval)
		if err != nil {
			return nil, invalidArg("rotation_interval: " + err.Error())
		}
		if svcErr := setPlatformRotationInterval(&cfg, interval); svcErr != nil {
			return nil, svcErr
		}
	}
	if req.RotationStrategy != nil {
		if svcErr := setPlatformRotationStrategy(&cfg, *req.RotationStrategy); svcErr != nil {
			return nil, svcErr
		}
	}
	if err := validatePlatformConfig(&cfg, true); err != nil {
		return nil, err
	}
	if ids, err := s.normalizeAndValidateSubscriptionIDs(cfg.SubscriptionIDs); err != nil {
		return nil, err
	} else {
		cfg.SubscriptionIDs = ids
	}

	id := uuid.New().String()
	mp, plat, svcErr := s.compileAndUpsertPlatform(id, cfg)
	if svcErr != nil {
		return nil, svcErr
	}

	// Register in topology pool.
	// Build the routable view before publish so concurrent readers don't observe
	// a newly created platform with an empty view.
	s.Pool.RebuildPlatform(plat)
	s.Pool.RegisterPlatform(plat)

	r := s.withRoutableNodeCount(platformToResponse(mp))
	return &r, nil
}

// UpdatePlatform applies a constrained partial patch to a platform.
// This is not RFC 7396 JSON Merge Patch: patch must be a non-empty object and
// null values are rejected.
func (s *ControlPlaneService) UpdatePlatform(id string, patchJSON json.RawMessage) (*PlatformResponse, error) {
	patch, verr := parseMergePatch(patchJSON)
	if verr != nil {
		return nil, verr
	}
	if err := patch.validateFields(platformPatchAllowedFields, func(key string) string {
		return fmt.Sprintf("field %q is read-only or unknown", key)
	}); err != nil {
		return nil, err
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()

	// Load current.
	current, err := s.getPlatformModel(id)
	if err != nil {
		return nil, err
	}
	if current.ID == platform.DefaultPlatformID {
		if nameVal, ok := patch["name"]; ok {
			nameStr, _ := nameVal.(string)
			if nameStr != platform.DefaultPlatformName {
				return nil, conflict("cannot rename Default platform")
			}
		}
	}

	cfg := platformConfigFromModel(*current)

	// Apply patch to current config.
	if nameStr, ok, err := patch.optionalNonEmptyString("name"); err != nil {
		return nil, err
	} else if ok {
		cfg.Name = platform.NormalizePlatformName(nameStr)
		if err := platform.ValidatePlatformName(cfg.Name); err != nil {
			return nil, invalidArg("name: " + err.Error())
		}
		if cfg.Name == platform.DefaultPlatformName && current.ID != platform.DefaultPlatformID {
			return nil, conflict("cannot use reserved name 'Default'")
		}
	}

	if d, ok, err := patch.optionalDurationString("sticky_ttl"); err != nil {
		return nil, err
	} else if ok {
		if err := setPlatformStickyTTL(&cfg, d); err != nil {
			return nil, err
		}
	}

	if filters, ok, err := patch.optionalStringSlice("regex_filters"); err != nil {
		return nil, err
	} else if ok {
		cfg.RegexFilters = filters
	}

	regionFiltersPatched := false
	if filters, ok, err := patch.optionalStringSlice("region_filters"); err != nil {
		return nil, err
	} else if ok {
		regionFiltersPatched = true
		cfg.RegionFilters = filters
	}

	if ids, ok, err := patch.optionalStringSlice("subscription_ids"); err != nil {
		return nil, err
	} else if ok {
		cfg.SubscriptionIDs = ids
	}

	if ma, ok, err := patch.optionalString("reverse_proxy_miss_action"); err != nil {
		return nil, err
	} else if ok {
		if err := setPlatformMissAction(&cfg, ma); err != nil {
			return nil, err
		}
	}
	if behavior, ok, err := patch.optionalString("reverse_proxy_empty_account_behavior"); err != nil {
		return nil, err
	} else if ok {
		if err := setPlatformEmptyAccountBehavior(&cfg, behavior); err != nil {
			return nil, err
		}
	}
	if fixedHeader, ok, err := patch.optionalString("reverse_proxy_fixed_account_header"); err != nil {
		return nil, err
	} else if ok {
		cfg.ReverseProxyFixedAccountHeader = fixedHeader
	}

	if ap, ok, err := patch.optionalString("allocation_policy"); err != nil {
		return nil, err
	} else if ok {
		if err := setPlatformAllocationPolicy(&cfg, ap); err != nil {
			return nil, err
		}
	}
	if disabled, ok, err := patch.optionalBool("passive_circuit_breaker_disabled"); err != nil {
		return nil, err
	} else if ok {
		cfg.PassiveCircuitBreakerDisabled = disabled
	}
	if fallbackIDs, ok, err := patch.optionalStringSlice("fallback_platform_ids"); err != nil {
		return nil, err
	} else if ok {
		cfg.FallbackPlatformIDs = fallbackIDs
	}
	if chainIDs, ok, err := patch.optionalStringSlice("chain_platform_ids"); err != nil {
		return nil, err
	} else if ok {
		cfg.ChainPlatformIDs = chainIDs
	}
	if interval, ok, err := patch.optionalDurationString("rotation_interval"); err != nil {
		return nil, err
	} else if ok {
		if svcErr := setPlatformRotationInterval(&cfg, interval); svcErr != nil {
			return nil, svcErr
		}
	}
	if strategy, ok, err := patch.optionalString("rotation_strategy"); err != nil {
		return nil, err
	} else if ok {
		if svcErr := setPlatformRotationStrategy(&cfg, strategy); svcErr != nil {
			return nil, svcErr
		}
	}
	if err := validatePlatformConfig(&cfg, regionFiltersPatched); err != nil {
		return nil, err
	}
	if ids, err := s.normalizeAndValidateSubscriptionIDs(cfg.SubscriptionIDs); err != nil {
		return nil, err
	} else {
		cfg.SubscriptionIDs = ids
	}

	mp, plat, svcErr := s.compileAndUpsertPlatform(id, cfg)
	if svcErr != nil {
		return nil, svcErr
	}

	// Replace in topology pool.
	if err := s.Pool.ReplacePlatform(plat); err != nil {
		return nil, internal("replace platform in pool", err)
	}

	r := s.withRoutableNodeCount(platformToResponse(mp))
	return &r, nil
}

// DeletePlatform deletes a platform.
func (s *ControlPlaneService) DeletePlatform(id string) error {
	if id == platform.DefaultPlatformID {
		return conflict("cannot delete Default platform")
	}
	s.platformMu.Lock()
	defer s.platformMu.Unlock()

	referenced, err := s.Engine.RoutingRuleReferencesPlatform(id)
	if err != nil {
		return internal("check routing rule references", err)
	}
	if referenced {
		return conflict("platform is referenced by a routing rule")
	}
	referenced, err = s.Engine.PlatformFallbackReferences(id)
	if err != nil {
		return internal("check platform fallback references", err)
	}
	if referenced {
		return conflict("platform is referenced as a fallback")
	}
	referenced, err = s.Engine.PlatformChainReferences(id)
	if err != nil {
		return internal("check platform chain references", err)
	}
	if referenced {
		return conflict("platform is referenced as a chain hop")
	}

	if err := s.Engine.DeletePlatform(id); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return notFound("platform not found")
		}
		return internal("delete platform", err)
	}
	s.Pool.UnregisterPlatform(id)
	if s.Router != nil {
		s.Router.DeleteLeasesForPlatform(id)
	}
	return nil
}

// ResetPlatformToDefault resets a platform to env defaults.
func (s *ControlPlaneService) ResetPlatformToDefault(id string) (*PlatformResponse, error) {
	s.platformMu.Lock()
	defer s.platformMu.Unlock()

	name, err := s.Engine.GetPlatformName(id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, notFound("platform not found")
		}
		return nil, internal("get platform", err)
	}

	cfg := s.defaultPlatformConfig(name)
	mp, plat, svcErr := s.compileAndUpsertPlatform(id, cfg)
	if svcErr != nil {
		return nil, svcErr
	}

	if err := s.Pool.ReplacePlatform(plat); err != nil {
		return nil, internal("replace platform in pool", err)
	}

	r := s.withRoutableNodeCount(platformToResponse(mp))
	return &r, nil
}

// RebuildPlatformView triggers a full rebuild of the platform's routable view.
func (s *ControlPlaneService) RebuildPlatformView(id string) error {
	plat, ok := s.Pool.GetPlatform(id)
	if !ok {
		return notFound("platform not found")
	}
	s.Pool.RebuildPlatform(plat)
	return nil
}

// PreviewFilterRequest holds preview filter parameters.
type PreviewFilterRequest struct {
	PlatformID   *string             `json:"platform_id"`
	PlatformSpec *PlatformSpecFilter `json:"platform_spec"`
}

type PlatformSpecFilter struct {
	RegexFilters    []string `json:"regex_filters"`
	RegionFilters   []string `json:"region_filters"`
	SubscriptionIDs []string `json:"subscription_ids"`
}

// NodeSummary is the API response for a node.
type NodeSummary struct {
	NodeHash                         string         `json:"node_hash"`
	CreatedAt                        string         `json:"created_at"`
	Enabled                          bool           `json:"enabled"`
	DisplayTag                       string         `json:"display_tag,omitempty"`
	HasOutbound                      bool           `json:"has_outbound"`
	LastError                        string         `json:"last_error,omitempty"`
	CircuitOpenSince                 *string        `json:"circuit_open_since"`
	FailureCount                     int            `json:"failure_count"`
	SupportsUDP                      bool           `json:"supports_udp"`
	UDPHealth                        node.UDPHealth `json:"udp_health"`
	UDPFailureCount                  int            `json:"udp_failure_count"`
	UDPCircuitOpenSince              *string        `json:"udp_circuit_open_since"`
	EgressIP                         string         `json:"egress_ip,omitempty"`
	Region                           string         `json:"region,omitempty"`
	LastEgressUpdate                 string         `json:"last_egress_update,omitempty"`
	LastLatencyProbeAttempt          string         `json:"last_latency_probe_attempt,omitempty"`
	LastAuthorityLatencyProbeAttempt string         `json:"last_authority_latency_probe_attempt,omitempty"`
	ReferenceLatencyMs               *float64       `json:"reference_latency_ms,omitempty"`
	LastEgressUpdateAttempt          string         `json:"last_egress_update_attempt,omitempty"`
	LastUDPProbeAttempt              string         `json:"last_udp_probe_attempt,omitempty"`
	LastUDPProbeSuccess              string         `json:"last_udp_probe_success,omitempty"`
	Tags                             []NodeTag      `json:"tags"`
}

// IsHealthyAndEnabled follows the node-summary health rule used by API/UI
// aggregates: enabled, outbound-ready, and not circuit-open.
func (n NodeSummary) IsHealthyAndEnabled() bool {
	return n.Enabled && n.HasOutbound && n.CircuitOpenSince == nil
}

type NodeTag struct {
	SubscriptionID          string `json:"subscription_id"`
	SubscriptionName        string `json:"subscription_name"`
	Tag                     string `json:"tag"`
	SubscriptionCreatedAtNs int64  `json:"-"`
}

func (s *ControlPlaneService) nodeEntryToSummary(h node.Hash, entry *node.NodeEntry) NodeSummary {
	tcpHealth := entry.TCPHealth()
	udpHealth := entry.UDPHealthState()
	ns := NodeSummary{
		NodeHash:        h.Hex(),
		CreatedAt:       entry.CreatedAt.UTC().Format(time.RFC3339Nano),
		Enabled:         true,
		HasOutbound:     entry.HasOutbound(),
		LastError:       entry.GetLastError(),
		FailureCount:    int(tcpHealth.FailureCount),
		SupportsUDP:     entry.SupportsNetwork("udp"),
		UDPHealth:       entry.UDPHealth(),
		UDPFailureCount: int(udpHealth.FailureCount),
	}

	if s != nil && s.Pool != nil {
		ns.Enabled = !s.Pool.IsNodeDisabled(h)
		ns.DisplayTag = s.Pool.ResolveNodeDisplayTag(h)
	}

	if cos := tcpHealth.CircuitOpenSince; cos > 0 {
		t := time.Unix(0, cos).UTC().Format(time.RFC3339Nano)
		ns.CircuitOpenSince = &t
	}
	if cos := udpHealth.CircuitOpenSince; cos > 0 {
		t := time.Unix(0, cos).UTC().Format(time.RFC3339Nano)
		ns.UDPCircuitOpenSince = &t
	}

	egressIP := entry.GetEgressIP()
	if egressIP.IsValid() {
		ns.EgressIP = egressIP.String()
		ns.Region = entry.GetRegion(nil)
		if s.GeoIP != nil {
			ns.Region = entry.GetRegion(s.GeoIP.Lookup)
		}
	}

	if leu := entry.LastEgressUpdate.Load(); leu > 0 {
		ns.LastEgressUpdate = time.Unix(0, leu).UTC().Format(time.RFC3339Nano)
	}
	if lastAny := entry.LastLatencyProbeAttempt.Load(); lastAny > 0 {
		ns.LastLatencyProbeAttempt = time.Unix(0, lastAny).UTC().Format(time.RFC3339Nano)
	}
	if lastAuthority := entry.LastAuthorityLatencyProbeAttempt.Load(); lastAuthority > 0 {
		ns.LastAuthorityLatencyProbeAttempt = time.Unix(0, lastAuthority).UTC().Format(time.RFC3339Nano)
	}
	if s != nil && s.RuntimeCfg != nil {
		if cfg := s.RuntimeCfg.Load(); cfg != nil {
			if avgMs, ok := node.AverageEWMAForDomainsMs(entry, cfg.LatencyAuthorities); ok {
				ns.ReferenceLatencyMs = &avgMs
			}
		}
	}
	if lastEgressAttempt := entry.LastEgressUpdateAttempt.Load(); lastEgressAttempt > 0 {
		ns.LastEgressUpdateAttempt = time.Unix(0, lastEgressAttempt).UTC().Format(time.RFC3339Nano)
	}
	if lastUDPAttempt := entry.LastUDPProbeAttempt.Load(); lastUDPAttempt > 0 {
		ns.LastUDPProbeAttempt = time.Unix(0, lastUDPAttempt).UTC().Format(time.RFC3339Nano)
	}
	if lastUDPSuccess := entry.LastUDPProbeSuccess.Load(); lastUDPSuccess > 0 {
		ns.LastUDPProbeSuccess = time.Unix(0, lastUDPSuccess).UTC().Format(time.RFC3339Nano)
	}

	// Build tags.
	subIDs := entry.SubscriptionIDs()
	for _, subID := range subIDs {
		sub := s.SubMgr.Lookup(subID)
		if sub == nil {
			continue
		}
		managed, ok := sub.ManagedNodes().LoadNode(h)
		if !ok {
			continue
		}
		tags := managed.Tags
		for _, tag := range tags {
			ns.Tags = append(ns.Tags, NodeTag{
				SubscriptionID:          subID,
				SubscriptionName:        sub.Name(),
				Tag:                     sub.Name() + "/" + tag,
				SubscriptionCreatedAtNs: sub.CreatedAtNs,
			})
		}
	}
	if ns.Tags == nil {
		ns.Tags = []NodeTag{}
	}
	return ns
}

// PreviewFilter returns nodes matching the given filter spec.
func (s *ControlPlaneService) PreviewFilter(req PreviewFilterRequest) ([]NodeSummary, error) {
	hasPlatformID := req.PlatformID != nil && *req.PlatformID != ""
	hasPlatformSpec := req.PlatformSpec != nil

	if hasPlatformID == hasPlatformSpec {
		return nil, invalidArg("exactly one of platform_id or platform_spec is required")
	}

	var regexFilters node.TagFilter
	var regionFilters []string
	var subscriptionIDs []string

	if hasPlatformID {
		plat, ok := s.Pool.GetPlatform(*req.PlatformID)
		if !ok {
			return nil, notFound("platform not found")
		}
		regexFilters = plat.RegexFilters
		regionFilters = plat.RegionFilters
		subscriptionIDs = append([]string{}, plat.SubscriptionIDs...)
	} else {
		compiled, err := platform.CompileRegexFilters(req.PlatformSpec.RegexFilters)
		if err != nil {
			return nil, invalidArg(err.Error())
		}
		regexFilters = compiled
		regionFilters = req.PlatformSpec.RegionFilters
		if err := platform.ValidateRegionFilters(regionFilters); err != nil {
			return nil, invalidArg(err.Error())
		}
		ids, svcErr := s.normalizeAndValidateSubscriptionIDs(req.PlatformSpec.SubscriptionIDs)
		if svcErr != nil {
			return nil, svcErr
		}
		subscriptionIDs = ids
	}

	var subLookup node.SubLookupFunc
	if s.Pool != nil {
		subLookup = s.Pool.MakeSubLookup()
	}
	var result []NodeSummary
	s.Pool.Range(func(h node.Hash, entry *node.NodeEntry) bool {
		if !entry.MatchesSubscriptionAllowlist(subscriptionIDs, subLookup) {
			return true
		}
		if !entry.MatchTagFilter(regexFilters, subLookup) {
			return true
		}
		if len(regionFilters) > 0 {
			region := entry.GetRegion(nil)
			if s.GeoIP != nil {
				region = entry.GetRegion(s.GeoIP.Lookup)
			}
			if !platform.MatchRegionFilter(region, regionFilters) {
				return true
			}
		}
		result = append(result, s.nodeEntryToSummary(h, entry))
		return true
	})
	return result, nil
}
