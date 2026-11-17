package routing

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

const diagnosticSeedSalt = uint64(0x9e3779b97f4a7c15)

type RouteDiagnostic struct {
	Seed       uint64                `json:"seed"`
	Root       DiagnosticPlatform    `json:"root"`
	Fallback   []DiagnosticPlatform  `json:"fallback"`
	Lease      *model.Lease          `json:"lease,omitempty"`
	Candidates []DiagnosticCandidate `json:"candidates"`
	P2C        []DiagnosticP2C       `json:"p2c"`
	Selection  *DiagnosticSelection  `json:"selection,omitempty"`
	Outcome    string                `json:"outcome"`
}

type DiagnosticPlatform struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type DiagnosticCandidate struct {
	PlatformID       string   `json:"platform_id"`
	NodeHash         string   `json:"node_hash"`
	EgressIP         string   `json:"egress_ip"`
	Status           string   `json:"status"`
	ActiveLeaseCount int64    `json:"active_lease_count"`
	MaxActiveLeases  int64    `json:"max_active_leases"`
	Eligible         bool     `json:"eligible"`
	Exclusions       []string `json:"exclusions"`
}

type DiagnosticP2C struct {
	PlatformID string   `json:"platform_id"`
	Trace      P2CTrace `json:"trace"`
}

type DiagnosticChainHop struct {
	PlatformID   string `json:"platform_id"`
	PlatformName string `json:"platform_name"`
	NodeHash     string `json:"node_hash,omitempty"`
}

type DiagnosticSelection struct {
	PlatformID       string               `json:"platform_id"`
	PlatformName     string               `json:"platform_name"`
	NodeHash         string               `json:"node_hash"`
	EgressIP         string               `json:"egress_ip"`
	Reason           string               `json:"reason"`
	Chain            []DiagnosticChainHop `json:"chain,omitempty"`
	RotationStatus   string               `json:"rotation_status,omitempty"`
	RotationReason   string               `json:"rotation_reason,omitempty"`
	RotationStrategy string               `json:"rotation_strategy,omitempty"`
}

func (r *Router) DiagnoseRoute(options RouteOptions, seed uint64) (RouteDiagnostic, error) {
	network, err := normalizeRequiredNetwork(options.RequiredNetwork)
	if err != nil {
		return RouteDiagnostic{}, err
	}
	root, err := r.resolvePlatform(options.PlatformName)
	if err != nil {
		return RouteDiagnostic{}, err
	}
	candidates, err := r.fallbackPlatforms(root)
	if err != nil {
		return RouteDiagnostic{}, err
	}
	diagnostic := RouteDiagnostic{
		Seed: seed, Root: diagnosticPlatform(root),
		Fallback: make([]DiagnosticPlatform, 0, len(candidates)),
		P2C:      make([]DiagnosticP2C, 0),
		Outcome:  "no_available_nodes",
	}
	for _, candidate := range candidates {
		diagnostic.Fallback = append(diagnostic.Fallback, diagnosticPlatform(candidate))
	}
	request, existing := r.diagnosticRequest(root, candidates, options, network)
	if existing != nil {
		diagnostic.Lease = leaseModel(root.ID, options.Account, *existing)
	}
	diagnostic.Candidates = r.diagnosticCandidates(request, existing)
	rng := rand.New(rand.NewPCG(seed, seed^diagnosticSeedSalt))
	selection, traces, outcome, err := r.diagnoseRouteSelection(request, existing, rng)
	diagnostic.P2C = append(diagnostic.P2C, traces...)
	if selection != nil {
		chain, chainErr := r.diagnosticAttachChain(selection, options, network, rng)
		selection.Chain = chain
		if chainErr != nil {
			diagnostic.Selection = selection
			diagnostic.Outcome = "no_available_nodes"
			return diagnostic, chainErr
		}
	}
	diagnostic.Selection = selection
	diagnostic.Outcome = outcome
	return diagnostic, err
}

func (r *Router) diagnosticAttachChain(
	selection *DiagnosticSelection,
	options RouteOptions,
	network string,
	rng *rand.Rand,
) ([]DiagnosticChainHop, error) {
	if selection == nil {
		return nil, nil
	}
	selected, ok := r.pool.GetPlatform(selection.PlatformID)
	if !ok || len(selected.ChainPlatformIDs) == 0 {
		return nil, nil
	}
	out := make([]DiagnosticChainHop, 0, len(selected.ChainPlatformIDs))
	for _, hopID := range selected.ChainPlatformIDs {
		hopPlat, ok := r.pool.GetPlatform(hopID)
		if !ok {
			out = append(out, DiagnosticChainHop{PlatformID: hopID})
			return out, fmt.Errorf("%w: chain hop platform %s", ErrNoAvailableNodes, hopID)
		}
		candidates, err := r.fallbackPlatforms(hopPlat)
		if err != nil {
			out = append(out, DiagnosticChainHop{PlatformID: hopPlat.ID, PlatformName: hopPlat.Name})
			return out, fmt.Errorf("%w: chain hop platform %s", ErrNoAvailableNodes, hopID)
		}
		hopOptions := RouteOptions{
			PlatformName:    hopPlat.Name,
			Account:         options.Account,
			Target:          options.Target,
			RequiredNetwork: network,
			SkipProxyChain:  true,
		}
		request, existing := r.diagnosticRequest(hopPlat, candidates, hopOptions, network)
		hopSelection, _, _, hopErr := r.diagnoseRouteSelection(request, existing, rng)
		if hopErr != nil || hopSelection == nil {
			out = append(out, DiagnosticChainHop{PlatformID: hopPlat.ID, PlatformName: hopPlat.Name})
			if hopErr == nil {
				hopErr = ErrNoAvailableNodes
			}
			return out, hopErr
		}
		out = append(out, DiagnosticChainHop{
			PlatformID:   hopSelection.PlatformID,
			PlatformName: hopSelection.PlatformName,
			NodeHash:     hopSelection.NodeHash,
		})
	}
	return out, nil
}

func diagnosticPlatform(candidate *platform.Platform) DiagnosticPlatform {
	return DiagnosticPlatform{ID: candidate.ID, Name: candidate.Name}
}

func (r *Router) diagnosticRequest(
	root *platform.Platform,
	candidates []*platform.Platform,
	options RouteOptions,
	network string,
) (routeRequest, *Lease) {
	state, ok := r.states.Load(root.ID)
	if !ok {
		state = NewPlatformRoutingState()
	}
	request := routeRequest{
		root: root, candidates: candidates, state: state, network: network,
		account: options.Account, targetDomain: netutil.ExtractDomain(options.Target), now: time.Now(),
	}
	if options.Account == "" {
		return request, nil
	}
	lease, loaded := state.Leases.GetLease(options.Account)
	if !loaded {
		return request, nil
	}
	if lease.SelectedPlatformID == "" {
		lease.SelectedPlatformID = root.ID
	}
	return request, &lease
}

func leaseModel(platformID, account string, lease Lease) *model.Lease {
	return &model.Lease{
		PlatformID: platformID, SelectedPlatformID: lease.SelectedPlatformID,
		Account: account, NodeHash: lease.NodeHash.Hex(), EgressIP: lease.EgressIP.String(),
		CreatedAtNs: lease.CreatedAtNs, ExpiryNs: lease.ExpiryNs,
		LastAccessedNs: lease.LastAccessedNs, LastRotatedAtNs: lease.LastRotatedAtNs,
		RotationRequestedAtNs:     lease.RotationRequestedAtNs,
		RotationRequestedStrategy: lease.RotationRequestedStrategy,
		RotationDeferred:          lease.RotationDeferred,
		RotationDeferredReason:    lease.RotationDeferredReason,
	}
}

func (r *Router) diagnosticAttempt(request routeRequest, selected *platform.Platform) routeAttempt {
	stats, ok := r.selectedStats.Load(selected.ID)
	if !ok {
		stats = NewIPLoadStats()
	}
	return routeAttempt{
		ownerPlatform: request.root, platform: selected, state: request.state,
		allocationStats: stats, view: selected.NetworkView(request.network),
		account: request.account, targetDomain: request.targetDomain, now: request.now,
	}
}

func (r *Router) diagnosticCandidates(request routeRequest, previous *Lease) []DiagnosticCandidate {
	result := make([]DiagnosticCandidate, 0)
	policySnapshot := r.egressPolicies.snapshot.Load()
	for _, candidate := range request.candidates {
		attempt := r.diagnosticAttempt(request, candidate)
		attempt.view.Range(func(hash node.Hash) bool {
			trace := r.diagnosticCandidate(
				policySnapshot,
				attempt,
				hash,
				request.account != "",
				previous,
			)
			result = append(result, trace)
			return true
		})
	}
	return result
}

func (r *Router) diagnosticCandidate(
	policySnapshot *egressPolicySnapshot,
	attempt routeAttempt,
	hash node.Hash,
	enforceCapacity bool,
	previous *Lease,
) DiagnosticCandidate {
	trace := DiagnosticCandidate{
		PlatformID: attempt.platform.ID,
		NodeHash:   hash.Hex(),
		Eligible:   true,
		Exclusions: make([]string, 0),
	}
	entry, ok := r.pool.GetEntry(hash)
	if !ok {
		trace.Eligible = false
		trace.Exclusions = []string{"node_missing"}
		return trace
	}
	ip := entry.GetEgressIP().Unmap()
	policy := policyFromSnapshot(policySnapshot, ip)
	trace.EgressIP = ip.String()
	trace.Status = string(policy.EffectiveStatus(attempt.now))
	trace.ActiveLeaseCount = r.globalStats.Get(ip)
	trace.MaxActiveLeases = policy.MaxActiveLeases
	if policy.EffectiveStatus(attempt.now) != EgressIPActive {
		trace.Exclusions = append(trace.Exclusions, "egress_"+strings.ToLower(trace.Status))
	}
	effectiveLoad := trace.ActiveLeaseCount
	if previous != nil && previous.EgressIP == ip {
		effectiveLoad--
	}
	if enforceCapacity && policy.MaxActiveLeases > 0 && effectiveLoad >= policy.MaxActiveLeases {
		trace.Exclusions = append(trace.Exclusions, "capacity_reached")
	}
	trace.Eligible = len(trace.Exclusions) == 0
	return trace
}

func (r *Router) diagnoseRouteSelection(
	request routeRequest,
	existing *Lease,
	rng *rand.Rand,
) (*DiagnosticSelection, []DiagnosticP2C, string, error) {
	if existing == nil || existing.IsExpired(request.now) {
		return r.diagnoseNewSelection(request, existing, rng)
	}
	if trigger, due := r.rotationDue(request.root, *existing, request.now); due {
		return r.diagnoseRotation(request, *existing, trigger, rng)
	}
	if selection := r.diagnoseLeaseHit(request, *existing); selection != nil {
		return selection, nil, "existing_lease", nil
	}
	if selection := r.diagnoseSameIPReplacement(request, *existing); selection != nil {
		return selection, nil, "same_ip_replacement", nil
	}
	return r.diagnoseNewSelection(request, existing, rng)
}

func (r *Router) diagnoseLeaseHit(request routeRequest, lease Lease) *DiagnosticSelection {
	selected := findCandidatePlatform(request.candidates, lease.SelectedPlatformID)
	if selected == nil || !r.canRetainLease(request, lease) {
		return nil
	}
	return diagnosticSelection(selected, lease.NodeHash, lease.EgressIP, "existing_lease")
}

func (r *Router) diagnoseSameIPReplacement(request routeRequest, lease Lease) *DiagnosticSelection {
	selected := findCandidatePlatform(request.candidates, lease.SelectedPlatformID)
	if selected == nil || !r.allowsExistingEgress(lease.EgressIP, request.now) {
		return nil
	}
	attempt := r.diagnosticAttempt(request, selected)
	hash, ok := chooseSameIPRotationCandidate(sameIPRotationOptions{
		view: attempt.view, pool: r.pool, targetIP: lease.EgressIP,
		targetDomain: attempt.targetDomain, authorities: r.authorities(),
		window: r.p2cWindow(),
	})
	if !ok {
		return nil
	}
	return diagnosticSelection(selected, hash, lease.EgressIP, "same_ip_replacement")
}

func (r *Router) diagnoseNewSelection(
	request routeRequest,
	previous *Lease,
	rng *rand.Rand,
) (*DiagnosticSelection, []DiagnosticP2C, string, error) {
	traces := make([]DiagnosticP2C, 0, len(request.candidates))
	for _, candidate := range request.candidates {
		attempt := r.diagnosticAttempt(request, candidate)
		if previous != nil {
			attempt.capacityCredit = previous.EgressIP
		}
		view := r.newAllocationView(attempt, request.account != "")
		hash, trace, err := randomRouteWithRNG(randomRouteOptions{
			platform: candidate, view: view, stats: attempt.allocationStats,
			pool: r.pool, targetDomain: attempt.targetDomain,
			authorities: r.authorities(), p2cWindow: r.p2cWindow(),
		}, rng)
		if errors.Is(err, ErrNoAvailableNodes) {
			continue
		}
		if err != nil {
			return nil, traces, "error", err
		}
		traces = append(traces, DiagnosticP2C{PlatformID: candidate.ID, Trace: trace})
		entry, ok := r.pool.GetEntry(hash)
		if !ok {
			continue
		}
		return diagnosticSelection(candidate, hash, entry.GetEgressIP(), "new_allocation"), traces, "selected", nil
	}
	return nil, traces, "no_available_nodes", ErrNoAvailableNodes
}

func diagnosticSelection(
	selected *platform.Platform,
	hash node.Hash,
	ip netip.Addr,
	reason string,
) *DiagnosticSelection {
	return &DiagnosticSelection{
		PlatformID: selected.ID, PlatformName: selected.Name,
		NodeHash: hash.Hex(), EgressIP: ip.String(), Reason: reason,
	}
}
