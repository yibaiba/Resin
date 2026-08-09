package service

import (
	"errors"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
)

type RotateLeaseRequest struct {
	Strategy *string `json:"strategy"`
}

type RotateLeaseResponse struct {
	Status   string         `json:"status"`
	Reason   string         `json:"reason"`
	Strategy string         `json:"strategy"`
	Lease    *LeaseResponse `json:"lease,omitempty"`
}

// ------------------------------------------------------------------
// Leases
// ------------------------------------------------------------------

// LeaseResponse is the API response for a lease.
type LeaseResponse struct {
	PlatformID                string `json:"platform_id"`
	SelectedPlatformID        string `json:"selected_platform_id"`
	Account                   string `json:"account"`
	NodeHash                  string `json:"node_hash"`
	NodeTag                   string `json:"node_tag"`
	EgressIP                  string `json:"egress_ip"`
	Expiry                    string `json:"expiry"`
	LastAccessed              string `json:"last_accessed"`
	LastRotatedAt             string `json:"last_rotated_at,omitempty"`
	RotationRequestedAt       string `json:"rotation_requested_at,omitempty"`
	RotationRequestedStrategy string `json:"rotation_requested_strategy,omitempty"`
	RotationDeferred          bool   `json:"rotation_deferred"`
	RotationDeferredReason    string `json:"rotation_deferred_reason,omitempty"`
}

func leaseToResponse(lease model.Lease, nodeTag string) LeaseResponse {
	response := LeaseResponse{
		PlatformID:                lease.PlatformID,
		SelectedPlatformID:        lease.SelectedPlatformID,
		Account:                   lease.Account,
		NodeHash:                  lease.NodeHash,
		NodeTag:                   nodeTag,
		EgressIP:                  lease.EgressIP,
		Expiry:                    time.Unix(0, lease.ExpiryNs).UTC().Format(time.RFC3339Nano),
		LastAccessed:              time.Unix(0, lease.LastAccessedNs).UTC().Format(time.RFC3339Nano),
		RotationRequestedStrategy: lease.RotationRequestedStrategy,
		RotationDeferred:          lease.RotationDeferred,
		RotationDeferredReason:    lease.RotationDeferredReason,
	}
	if lease.RotationRequestedAtNs > 0 {
		response.RotationRequestedAt = time.Unix(0, lease.RotationRequestedAtNs).UTC().Format(time.RFC3339Nano)
	}
	if lease.LastRotatedAtNs > 0 {
		response.LastRotatedAt = time.Unix(0, lease.LastRotatedAtNs).UTC().Format(time.RFC3339Nano)
	}
	return response
}

func (s *ControlPlaneService) resolveLeaseNodeTag(hash node.Hash) string {
	if s == nil || s.Pool == nil {
		return ""
	}
	return s.Pool.ResolveNodeDisplayTag(hash)
}

func (s *ControlPlaneService) resolveLeaseNodeTagFromHex(hashHex string) string {
	hash, err := node.ParseHex(hashHex)
	if err != nil {
		return ""
	}
	return s.resolveLeaseNodeTag(hash)
}

// ListLeases returns all leases for a platform.
func (s *ControlPlaneService) ListLeases(platformID string) ([]LeaseResponse, error) {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return nil, notFound("platform not found")
	}
	var result []LeaseResponse
	s.Router.RangeLeases(platformID, func(account string, lease routing.Lease) bool {
		result = append(result, leaseToResponse(model.Lease{
			PlatformID:                platformID,
			SelectedPlatformID:        lease.SelectedPlatformID,
			Account:                   account,
			NodeHash:                  lease.NodeHash.Hex(),
			EgressIP:                  lease.EgressIP.String(),
			ExpiryNs:                  lease.ExpiryNs,
			LastAccessedNs:            lease.LastAccessedNs,
			LastRotatedAtNs:           lease.LastRotatedAtNs,
			RotationRequestedAtNs:     lease.RotationRequestedAtNs,
			RotationRequestedStrategy: lease.RotationRequestedStrategy,
			RotationDeferred:          lease.RotationDeferred,
			RotationDeferredReason:    lease.RotationDeferredReason,
		}, s.resolveLeaseNodeTag(lease.NodeHash)))
		return true
	})
	if result == nil {
		result = []LeaseResponse{}
	}
	return result, nil
}

// GetLease returns a single lease.
func (s *ControlPlaneService) GetLease(platformID, account string) (*LeaseResponse, error) {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return nil, notFound("platform not found")
	}
	ml := s.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: account})
	if ml == nil {
		return nil, notFound("lease not found")
	}
	resp := leaseToResponse(*ml, s.resolveLeaseNodeTagFromHex(ml.NodeHash))
	return &resp, nil
}

// InheritLeaseByPlatformName copies a valid parent lease onto newAccount.
func (s *ControlPlaneService) InheritLeaseByPlatformName(platformName, parentAccount, newAccount string) error {
	platformName = strings.TrimSpace(platformName)
	if platformName == "" {
		return invalidArg("platform: must be non-empty")
	}
	parentAccount = strings.TrimSpace(parentAccount)
	if parentAccount == "" {
		return invalidArg("parent_account: must be non-empty")
	}
	newAccount = strings.TrimSpace(newAccount)
	if newAccount == "" {
		return invalidArg("new_account: must be non-empty")
	}
	if parentAccount == newAccount {
		return invalidArg("new_account: must differ from parent_account")
	}

	plat, ok := s.Pool.GetPlatformByName(platformName)
	if !ok || plat == nil {
		return notFound("platform not found")
	}

	parentLease := s.Router.ReadLease(model.LeaseKey{
		PlatformID: plat.ID,
		Account:    parentAccount,
	})
	nowNs := time.Now().UnixNano()
	if parentLease == nil || parentLease.ExpiryNs < nowNs {
		return notFound("parent lease not found")
	}

	next := *parentLease
	next.Account = newAccount
	next.LastRotatedAtNs = nowNs
	next.RotationRequestedAtNs = 0
	next.RotationRequestedStrategy = ""
	next.RotationDeferred = false
	next.RotationDeferredReason = ""
	if err := s.Router.UpsertLease(next); err != nil {
		return internal("inherit lease", err)
	}

	return nil
}

// DeleteLease removes a single lease.
func (s *ControlPlaneService) DeleteLease(platformID, account string) error {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return notFound("platform not found")
	}
	if !s.Router.DeleteLease(platformID, account) {
		return notFound("lease not found")
	}
	return nil
}

func (s *ControlPlaneService) RotateLease(
	platformID string,
	account string,
	req RotateLeaseRequest,
) (*RotateLeaseResponse, error) {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return nil, notFound("platform not found")
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, invalidArg("account: must be non-empty")
	}
	strategy := platform.RotationStrategy("")
	if req.Strategy != nil {
		strategy = platform.ParseRotationStrategy(*req.Strategy)
		if strategy == "" {
			return nil, invalidArg("strategy: must be ANY, SAME_IP, PREFER_NEW_IP, or REQUIRE_NEW_IP")
		}
	}
	result, err := s.Router.RotateLease(platformID, account, strategy)
	if err != nil {
		if errors.Is(err, routing.ErrLeaseNotFound) {
			return nil, notFound("lease not found")
		}
		if errors.Is(err, routing.ErrNoAvailableNodes) {
			return nil, conflict("no eligible rotation candidate")
		}
		return nil, internal("rotate lease", err)
	}
	lease, err := s.GetLease(platformID, account)
	if err != nil {
		return nil, err
	}
	return &RotateLeaseResponse{
		Status: result.RotationStatus, Reason: result.RotationReason,
		Strategy: result.RotationStrategy, Lease: lease,
	}, nil
}

// DeleteAllLeases removes all leases for a platform.
func (s *ControlPlaneService) DeleteAllLeases(platformID string) error {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return notFound("platform not found")
	}
	s.Router.DeleteAllLeases(platformID)
	return nil
}

// IPLoadEntry is the API response for IP load stats.
type IPLoadEntry struct {
	EgressIP   string `json:"egress_ip"`
	LeaseCount int64  `json:"lease_count"`
}

// GetIPLoad returns IP load stats for a platform.
func (s *ControlPlaneService) GetIPLoad(platformID string) ([]IPLoadEntry, error) {
	if _, ok := s.Pool.GetPlatform(platformID); !ok {
		return nil, notFound("platform not found")
	}
	snapshot := s.Router.SnapshotIPLoad(platformID)
	result := make([]IPLoadEntry, 0, len(snapshot))
	for ip, count := range snapshot {
		result = append(result, IPLoadEntry{
			EgressIP:   ip.String(),
			LeaseCount: count,
		})
	}
	return result, nil
}
