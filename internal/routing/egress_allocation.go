package routing

import (
	"net/netip"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

func (r *Router) newAllocationView(
	attempt routeAttempt,
	enforceCapacity bool,
) platform.ReadOnlyView {
	if !r.egressPolicies.HasRestrictions() {
		return attempt.view
	}
	policySnapshot := r.egressPolicies.snapshot.Load()
	filtered := newFilteredView(attempt.view.Size())
	attempt.view.Range(func(hash node.Hash) bool {
		entry, ok := r.pool.GetEntry(hash)
		if !ok {
			return true
		}
		ip := entry.GetEgressIP().Unmap()
		policy := policyFromSnapshot(policySnapshot, ip)
		if policy.EffectiveStatus(attempt.now) != EgressIPActive {
			return true
		}
		leaseCount := r.globalStats.Get(ip)
		if enforceCapacity && ip == attempt.capacityCredit.Unmap() {
			leaseCount--
		}
		if enforceCapacity && policy.MaxActiveLeases > 0 && leaseCount >= policy.MaxActiveLeases {
			return true
		}
		filtered.Add(hash)
		return true
	})
	return filtered
}

func (r *Router) allowsExistingEgress(ip netip.Addr, now time.Time) bool {
	return r.egressPolicies.Get(ip).EffectiveStatus(now) != EgressIPBlocked
}

func (r *Router) canUpsertLease(next, current Lease, loaded bool, now time.Time) bool {
	policy := r.egressPolicies.Get(next.EgressIP)
	if loaded && current.EgressIP == next.EgressIP {
		return policy.EffectiveStatus(now) != EgressIPBlocked
	}
	if policy.EffectiveStatus(now) != EgressIPActive {
		return false
	}
	return policy.MaxActiveLeases <= 0 || r.globalStats.Get(next.EgressIP) < policy.MaxActiveLeases
}

func (r *Router) incrementLeaseLoads(state *PlatformRoutingState, lease Lease) {
	state.IPLoadStats.Inc(lease.EgressIP)
	r.ensureSelectedStats(lease.SelectedPlatformID).Inc(lease.EgressIP)
	r.globalStats.Inc(lease.EgressIP)
}

func (r *Router) decrementLeaseLoads(
	ownerPlatformID string,
	state *PlatformRoutingState,
	lease Lease,
) {
	selectedPlatformID := lease.SelectedPlatformID
	if selectedPlatformID == "" {
		selectedPlatformID = ownerPlatformID
	}
	state.IPLoadStats.Dec(lease.EgressIP)
	r.ensureSelectedStats(selectedPlatformID).Dec(lease.EgressIP)
	r.globalStats.Dec(lease.EgressIP)
}

// EgressPolicies returns the routing policy store used by this router.
func (r *Router) EgressPolicies() *EgressPolicyStore { return r.egressPolicies }

// ReplaceEgressPolicies atomically publishes persisted policies.
func (r *Router) ReplaceEgressPolicies(policies []model.EgressIPPolicy) error {
	return r.egressPolicies.Replace(policies)
}

// SetEgressPolicy publishes one persisted policy.
func (r *Router) SetEgressPolicy(policy model.EgressIPPolicy) error {
	return r.egressPolicies.Set(policy)
}

// SnapshotGlobalIPLoad returns active sticky lease counts across all owners.
func (r *Router) SnapshotGlobalIPLoad() map[netip.Addr]int64 {
	return r.globalStats.Snapshot()
}
