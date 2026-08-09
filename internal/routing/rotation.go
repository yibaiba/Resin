package routing

import (
	"errors"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/puzpuzpuz/xsync/v4"
)

var ErrLeaseNotFound = errors.New("lease not found")

type rotationTrigger struct {
	strategy platform.RotationStrategy
	reason   string
	manual   bool
}

func (r *Router) rotationDue(root *platform.Platform, lease Lease, now time.Time) (rotationTrigger, bool) {
	strategy := root.RotationStrategy
	if strategy == "" {
		strategy = platform.RotationStrategyAny
	}
	if requested := platform.ParseRotationStrategy(lease.RotationRequestedStrategy); requested != "" {
		strategy = requested
	}
	if lease.RotationRequestedAtNs > 0 {
		return rotationTrigger{strategy: strategy, reason: "manual", manual: true}, true
	}
	status := r.egressPolicies.Get(lease.EgressIP).EffectiveStatus(now)
	if status == EgressIPBlocked {
		return rotationTrigger{strategy: strategy, reason: "blocked"}, true
	}
	if status == EgressIPDraining {
		return rotationTrigger{strategy: strategy, reason: "draining"}, true
	}
	if root.RotationIntervalNs <= 0 {
		return rotationTrigger{}, false
	}
	base := lease.LastRotatedAtNs
	if base <= 0 {
		base = lease.CreatedAtNs
	}
	due := base <= 0 || now.UnixNano()-base >= root.RotationIntervalNs
	return rotationTrigger{strategy: strategy, reason: "interval"}, due
}

func (r *Router) rotateStickyLeaseAcross(
	request routeRequest,
	current Lease,
	trigger rotationTrigger,
) (Lease, xsync.ComputeOp, RouteResult, error) {
	r.allocationMu.Lock()
	defer r.allocationMu.Unlock()

	next, result, err := r.selectRotationCandidate(request, current, trigger.strategy)
	if err == nil {
		r.decrementLeaseLoads(request.root.ID, request.state, current)
		r.incrementLeaseLoads(request.state, next)
		r.emitLeaseTransition(request, current, next, LeaseReplace)
		result.RotationStatus = "rotated"
		result.RotationReason = trigger.reason
		result.RotationStrategy = string(trigger.strategy)
		return next, xsync.UpdateOp, result, nil
	}
	if !r.canRetainLease(request, current) {
		r.removeLeaseAccounting(request, current, leaseInvalidationRemove)
		return current, xsync.DeleteOp, RouteResult{}, err
	}
	deferred := current
	deferred.LastAccessedNs = request.now.UnixNano()
	deferred.RotationDeferred = true
	deferred.RotationDeferredReason = "no_candidate_for_" + strings.ToLower(string(trigger.strategy))
	if trigger.manual {
		deferred.RotationRequestedAtNs = request.now.UnixNano()
		deferred.RotationRequestedStrategy = string(trigger.strategy)
	}
	r.emitLeaseEvent(LeaseEvent{
		Type: LeaseTouch, PlatformID: request.root.ID,
		SelectedPlatformID: deferred.SelectedPlatformID, Account: request.account,
		NodeHash: deferred.NodeHash, EgressIP: deferred.EgressIP,
	})
	result = r.rotationDeferredResult(request, deferred, trigger)
	return deferred, xsync.UpdateOp, result, nil
}

func (r *Router) selectRotationCandidate(
	request routeRequest,
	current Lease,
	strategy platform.RotationStrategy,
) (Lease, RouteResult, error) {
	passes := []platform.RotationStrategy{strategy}
	if strategy == platform.RotationStrategyPreferNewIP {
		passes = []platform.RotationStrategy{
			platform.RotationStrategyRequireNewIP,
			platform.RotationStrategyAny,
		}
	}
	for _, pass := range passes {
		if next, result, ok := r.selectRotationPass(request, current, pass); ok {
			return next, result, nil
		}
	}
	return Lease{}, RouteResult{}, ErrNoAvailableNodes
}

func (r *Router) selectRotationPass(
	request routeRequest,
	current Lease,
	strategy platform.RotationStrategy,
) (Lease, RouteResult, bool) {
	path := make([]string, 0, len(request.candidates))
	for _, candidate := range request.candidates {
		path = append(path, candidate.ID)
		attempt := r.attemptFor(request, candidate)
		attempt.capacityCredit = current.EgressIP
		attempt.view = r.rotationCandidateView(attempt.view, current, strategy)
		next, result, err := r.createLease(attempt)
		if err != nil {
			continue
		}
		next.CreatedAtNs = current.CreatedAtNs
		next.ExpiryNs = current.ExpiryNs
		next.LastRotatedAtNs = request.now.UnixNano()
		next.RotationRequestedAtNs = 0
		next.RotationRequestedStrategy = ""
		next.RotationDeferred = false
		next.RotationDeferredReason = ""
		result.LeaseCreated = false
		return next, withPlatformContext(request.root, candidate, path, result), true
	}
	return Lease{}, RouteResult{}, false
}

func (r *Router) rotationCandidateView(
	view platform.ReadOnlyView,
	current Lease,
	strategy platform.RotationStrategy,
) platform.ReadOnlyView {
	filtered := newFilteredView(view.Size())
	view.Range(func(hash node.Hash) bool {
		if hash == current.NodeHash {
			return true
		}
		entry, ok := r.pool.GetEntry(hash)
		if !ok {
			return true
		}
		ip := entry.GetEgressIP()
		if strategy == platform.RotationStrategySameIP && ip != current.EgressIP {
			return true
		}
		if strategy == platform.RotationStrategyRequireNewIP && ip == current.EgressIP {
			return true
		}
		filtered.Add(hash)
		return true
	})
	return filtered
}

func (r *Router) canRetainLease(request routeRequest, lease Lease) bool {
	selected := findCandidatePlatform(request.candidates, lease.SelectedPlatformID)
	if selected == nil || lease.IsExpired(request.now) || !r.allowsExistingEgress(lease.EgressIP, request.now) {
		return false
	}
	entry, ok := r.pool.GetEntry(lease.NodeHash)
	return ok && selected.NetworkView(request.network).Contains(lease.NodeHash) &&
		entry.GetEgressIP() == lease.EgressIP
}

func (r *Router) rotationDeferredResult(
	request routeRequest,
	lease Lease,
	trigger rotationTrigger,
) RouteResult {
	selected := findCandidatePlatform(request.candidates, lease.SelectedPlatformID)
	path := fallbackPathTo(request.candidates, lease.SelectedPlatformID)
	result := withPlatformContext(request.root, selected, path, RouteResult{
		NodeHash: lease.NodeHash, EgressIP: lease.EgressIP, LeaseCreated: false,
	})
	result.SelectionReason = "rotation_deferred"
	result.RotationStatus = "deferred"
	result.RotationReason = trigger.reason
	result.RotationStrategy = string(trigger.strategy)
	return result
}

func (r *Router) RotateLease(
	platformID string,
	account string,
	strategy platform.RotationStrategy,
) (RouteResult, error) {
	root, ok := r.pool.GetPlatform(platformID)
	if !ok {
		return RouteResult{}, ErrPlatformNotFound
	}
	if strategy == "" {
		strategy = root.RotationStrategy
	}
	if strategy == "" {
		strategy = platform.RotationStrategyAny
	}
	if !strategy.IsValid() {
		return RouteResult{}, errors.New("invalid rotation strategy")
	}
	candidates, err := r.fallbackPlatforms(root)
	if err != nil {
		return RouteResult{}, err
	}
	request := routeRequest{
		root: root, candidates: candidates, state: r.ensurePlatformState(root.ID),
		network: "tcp", account: account, now: time.Now(),
	}
	return r.rotateLeaseRequest(request, rotationTrigger{
		strategy: strategy, reason: "manual", manual: true,
	})
}

func (r *Router) rotateLeaseRequest(request routeRequest, trigger rotationTrigger) (RouteResult, error) {
	var result RouteResult
	var rotationErr error
	_, _ = request.state.Leases.leases.Compute(request.account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
		if !loaded || current.IsExpired(request.now) {
			rotationErr = ErrLeaseNotFound
			return current, xsync.CancelOp
		}
		next, op, routeResult, err := r.rotateStickyLeaseAcross(request, current, trigger)
		result, rotationErr = routeResult, err
		return next, op
	})
	if rotationErr == nil && r.nodeTagResolver != nil {
		result.NodeTag = r.nodeTagResolver(result.NodeHash)
	}
	return result, rotationErr
}
