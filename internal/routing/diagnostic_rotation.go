package routing

import (
	"errors"
	"math/rand/v2"

	"github.com/Resinat/Resin/internal/platform"
)

func (r *Router) diagnoseRotation(
	request routeRequest,
	current Lease,
	trigger rotationTrigger,
	rng *rand.Rand,
) (*DiagnosticSelection, []DiagnosticP2C, string, error) {
	passes := []platform.RotationStrategy{trigger.strategy}
	if trigger.strategy == platform.RotationStrategyPreferNewIP {
		passes = []platform.RotationStrategy{
			platform.RotationStrategyRequireNewIP,
			platform.RotationStrategyAny,
		}
	}
	allTraces := make([]DiagnosticP2C, 0)
	for _, pass := range passes {
		selection, traces, err := r.diagnoseRotationPass(request, current, pass, rng)
		allTraces = append(allTraces, traces...)
		if err == nil {
			selection.Reason = "rotation"
			selection.RotationStatus = "rotated"
			selection.RotationReason = trigger.reason
			selection.RotationStrategy = string(trigger.strategy)
			return selection, allTraces, "rotation", nil
		}
		if !errors.Is(err, ErrNoAvailableNodes) {
			return nil, allTraces, "error", err
		}
	}
	if r.canRetainLease(request, current) {
		selected := findCandidatePlatform(request.candidates, current.SelectedPlatformID)
		selection := diagnosticSelection(selected, current.NodeHash, current.EgressIP, "rotation_deferred")
		selection.RotationStatus = "deferred"
		selection.RotationReason = trigger.reason
		selection.RotationStrategy = string(trigger.strategy)
		return selection, allTraces, "rotation_deferred", nil
	}
	return nil, allTraces, "no_available_nodes", ErrNoAvailableNodes
}

func (r *Router) diagnoseRotationPass(
	request routeRequest,
	current Lease,
	strategy platform.RotationStrategy,
	rng *rand.Rand,
) (*DiagnosticSelection, []DiagnosticP2C, error) {
	traces := make([]DiagnosticP2C, 0)
	for _, candidate := range request.candidates {
		attempt := r.diagnosticAttempt(request, candidate)
		attempt.capacityCredit = current.EgressIP
		attempt.view = r.rotationCandidateView(attempt.view, current, strategy)
		view := r.newAllocationView(attempt, true)
		hash, trace, err := randomRouteWithRNG(randomRouteOptions{
			platform: candidate, view: view, stats: attempt.allocationStats,
			pool: r.pool, targetDomain: attempt.targetDomain,
			authorities: r.authorities(), p2cWindow: r.p2cWindow(),
		}, rng)
		if errors.Is(err, ErrNoAvailableNodes) {
			continue
		}
		if err != nil {
			return nil, traces, err
		}
		traces = append(traces, DiagnosticP2C{PlatformID: candidate.ID, Trace: trace})
		entry, ok := r.pool.GetEntry(hash)
		if !ok {
			continue
		}
		return diagnosticSelection(candidate, hash, entry.GetEgressIP(), "rotation"), traces, nil
	}
	return nil, traces, ErrNoAvailableNodes
}
