package service

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/state"
)

type EgressIPResponse struct {
	EgressIP         string   `json:"egress_ip"`
	Status           string   `json:"status"`
	ConfiguredStatus string   `json:"configured_status"`
	NodeCount        int      `json:"node_count"`
	PlatformIDs      []string `json:"platform_ids"`
	ActiveLeaseCount int64    `json:"active_lease_count"`
	MaxActiveLeases  int64    `json:"max_active_leases"`
	ResumeAt         string   `json:"resume_at,omitempty"`
	UpdatedAt        string   `json:"updated_at,omitempty"`
}

type UpdateEgressIPPolicyRequest struct {
	Status          *string `json:"status"`
	MaxActiveLeases *int64  `json:"max_active_leases"`
	ResumeAt        *string `json:"resume_at"`
}

type BatchDrainEgressIPsRequest struct {
	EgressIPs []string `json:"egress_ips"`
	ResumeAt  *string  `json:"resume_at"`
}

type BatchEgressIPResult struct {
	EgressIP string `json:"egress_ip"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
}

type egressInventoryItem struct {
	nodeCount   int
	platformIDs map[string]struct{}
}

func (s *ControlPlaneService) ListEgressIPs() ([]EgressIPResponse, error) {
	policies, err := s.Engine.ListEgressIPPolicies()
	if err != nil {
		return nil, internal("list egress IP policies", err)
	}
	inventory := s.collectEgressInventory()
	for _, policy := range policies {
		if _, exists := inventory[policy.EgressIP]; !exists {
			inventory[policy.EgressIP] = &egressInventoryItem{platformIDs: map[string]struct{}{}}
		}
	}
	loads := s.Router.SnapshotGlobalIPLoad()
	responses := make([]EgressIPResponse, 0, len(inventory))
	for ip, item := range inventory {
		policy := findEgressPolicy(policies, ip)
		response, err := buildEgressIPResponse(policy, item, loads)
		if err != nil {
			return nil, internal("decode egress IP policy", err)
		}
		responses = append(responses, response)
	}
	slices.SortFunc(responses, func(a, b EgressIPResponse) int {
		return strings.Compare(a.EgressIP, b.EgressIP)
	})
	return responses, nil
}

func (s *ControlPlaneService) collectEgressInventory() map[string]*egressInventoryItem {
	inventory := make(map[string]*egressInventoryItem)
	s.Pool.Range(func(_ node.Hash, entry *node.NodeEntry) bool {
		ip := entry.GetEgressIP().Unmap()
		if ip.IsValid() {
			item := ensureEgressInventoryItem(inventory, ip.String())
			item.nodeCount++
		}
		return true
	})
	s.Pool.RangePlatforms(func(plat *platform.Platform) bool {
		plat.View().Range(func(hash node.Hash) bool {
			entry, ok := s.Pool.GetEntry(hash)
			if !ok {
				return true
			}
			ip := entry.GetEgressIP().Unmap()
			if ip.IsValid() {
				ensureEgressInventoryItem(inventory, ip.String()).platformIDs[plat.ID] = struct{}{}
			}
			return true
		})
		return true
	})
	return inventory
}

func ensureEgressInventoryItem(items map[string]*egressInventoryItem, ip string) *egressInventoryItem {
	item, ok := items[ip]
	if ok {
		return item
	}
	item = &egressInventoryItem{platformIDs: make(map[string]struct{})}
	items[ip] = item
	return item
}

func findEgressPolicy(policies []model.EgressIPPolicy, ip string) model.EgressIPPolicy {
	for _, policy := range policies {
		if policy.EgressIP == ip {
			return policy
		}
	}
	return model.EgressIPPolicy{EgressIP: ip, Status: string(routing.EgressIPActive)}
}

func buildEgressIPResponse(
	modelPolicy model.EgressIPPolicy,
	item *egressInventoryItem,
	loads map[netip.Addr]int64,
) (EgressIPResponse, error) {
	store := routing.NewEgressPolicyStore()
	if err := store.Replace([]model.EgressIPPolicy{modelPolicy}); err != nil {
		return EgressIPResponse{}, err
	}
	ip, err := netip.ParseAddr(modelPolicy.EgressIP)
	if err != nil {
		return EgressIPResponse{}, err
	}
	ip = ip.Unmap()
	policy := store.Get(ip)
	platformIDs := make([]string, 0, len(item.platformIDs))
	for id := range item.platformIDs {
		platformIDs = append(platformIDs, id)
	}
	slices.Sort(platformIDs)
	response := EgressIPResponse{
		EgressIP: ip.String(), Status: string(policy.EffectiveStatus(time.Now())),
		ConfiguredStatus: string(policy.Status), NodeCount: item.nodeCount,
		PlatformIDs: platformIDs, ActiveLeaseCount: loads[ip],
		MaxActiveLeases: policy.MaxActiveLeases,
	}
	if modelPolicy.ResumeAtNs > 0 {
		response.ResumeAt = time.Unix(0, modelPolicy.ResumeAtNs).UTC().Format(time.RFC3339Nano)
	}
	if modelPolicy.UpdatedAtNs > 0 {
		response.UpdatedAt = time.Unix(0, modelPolicy.UpdatedAtNs).UTC().Format(time.RFC3339Nano)
	}
	return response, nil
}

func (s *ControlPlaneService) UpdateEgressIPPolicy(
	rawIP string,
	req UpdateEgressIPPolicyRequest,
) (*EgressIPResponse, error) {
	ip, svcErr := normalizeEgressIP(rawIP)
	if svcErr != nil {
		return nil, svcErr
	}
	policy, err := s.loadEgressIPPolicy(ip)
	if err != nil {
		return nil, err
	}
	if svcErr := applyEgressPolicyPatch(&policy, req); svcErr != nil {
		return nil, svcErr
	}
	policy.UpdatedAtNs = time.Now().UnixNano()
	if err := routing.ValidateEgressPolicy(policy); err != nil {
		return nil, invalidArg(err.Error())
	}
	if err := s.Engine.UpsertEgressIPPolicy(policy); err != nil {
		return nil, internal("persist egress IP policy", err)
	}
	if err := s.Router.SetEgressPolicy(policy); err != nil {
		return nil, internal("publish egress IP policy", err)
	}
	return s.getEgressIPResponse(policy)
}

func normalizeEgressIP(raw string) (string, *ServiceError) {
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "", invalidArg("egress_ip: must be a valid IP address")
	}
	return ip.Unmap().String(), nil
}

func (s *ControlPlaneService) loadEgressIPPolicy(ip string) (model.EgressIPPolicy, error) {
	policy, err := s.Engine.GetEgressIPPolicy(ip)
	if err == nil {
		return *policy, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return model.EgressIPPolicy{}, internal("get egress IP policy", err)
	}
	return model.EgressIPPolicy{EgressIP: ip, Status: string(routing.EgressIPActive)}, nil
}

func applyEgressPolicyPatch(
	policy *model.EgressIPPolicy,
	req UpdateEgressIPPolicyRequest,
) *ServiceError {
	if req.Status != nil {
		status := routing.EgressIPStatus(strings.ToUpper(strings.TrimSpace(*req.Status)))
		if !status.IsValid() {
			return invalidArg("status: must be ACTIVE, DRAINING, or BLOCKED")
		}
		policy.Status = string(status)
	}
	if req.MaxActiveLeases != nil {
		if *req.MaxActiveLeases < 0 {
			return invalidArg("max_active_leases: must be >= 0")
		}
		policy.MaxActiveLeases = *req.MaxActiveLeases
	}
	if req.ResumeAt != nil {
		resumeAt, err := parseOptionalRFC3339(*req.ResumeAt)
		if err != nil {
			return invalidArg("resume_at: must be empty or RFC3339")
		}
		policy.ResumeAtNs = resumeAt
	}
	if policy.Status == string(routing.EgressIPActive) {
		policy.ResumeAtNs = 0
	}
	return nil
}

func parseOptionalRFC3339(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return 0, err
	}
	return parsed.UnixNano(), nil
}

func (s *ControlPlaneService) getEgressIPResponse(policy model.EgressIPPolicy) (*EgressIPResponse, error) {
	items := s.collectEgressInventory()
	item := ensureEgressInventoryItem(items, policy.EgressIP)
	response, err := buildEgressIPResponse(policy, item, s.Router.SnapshotGlobalIPLoad())
	if err != nil {
		return nil, internal("decode egress IP policy", err)
	}
	return &response, nil
}

func (s *ControlPlaneService) BatchDrainEgressIPs(
	req BatchDrainEgressIPsRequest,
) []BatchEgressIPResult {
	results := make([]BatchEgressIPResult, 0, len(req.EgressIPs))
	status := string(routing.EgressIPDraining)
	for _, rawIP := range req.EgressIPs {
		_, err := s.UpdateEgressIPPolicy(rawIP, UpdateEgressIPPolicyRequest{
			Status: &status, ResumeAt: req.ResumeAt,
		})
		result := BatchEgressIPResult{EgressIP: rawIP, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	return results
}
