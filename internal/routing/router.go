package routing

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/puzpuzpuz/xsync/v4"
	N "github.com/sagernet/sing/common/network"
)

var (
	ErrPlatformNotFound   = errors.New("platform not found")
	ErrUnsupportedNetwork = errors.New("unsupported required network")
)

type PoolAccessor interface {
	GetEntry(hash node.Hash) (*node.NodeEntry, bool)
	GetPlatform(id string) (*platform.Platform, bool)
	GetPlatformByName(name string) (*platform.Platform, bool)
	RangePlatforms(fn func(*platform.Platform) bool)
}

// Router handles route selection and lease management.
type Router struct {
	pool            PoolAccessor
	states          *xsync.Map[string, *PlatformRoutingState]
	selectedStats   *xsync.Map[string, *IPLoadStats]
	globalStats     *IPLoadStats
	egressPolicies  *EgressPolicyStore
	allocationMu    sync.Mutex
	authorities     func() []string
	p2cWindow       func() time.Duration
	onLeaseEvent    LeaseEventFunc
	nodeTagResolver func(node.Hash) string
}

type RouterConfig struct {
	Pool        PoolAccessor
	Authorities func() []string
	P2CWindow   func() time.Duration
	// OnLeaseEvent is called synchronously; handlers must stay lightweight.
	OnLeaseEvent LeaseEventFunc
	// NodeTagResolver resolves a node hash to its display tag ("<Sub>/<Tag>").
	// If nil, NodeTag will be empty.
	NodeTagResolver func(node.Hash) string
	EgressPolicies  *EgressPolicyStore
}

func NewRouter(cfg RouterConfig) *Router {
	policies := cfg.EgressPolicies
	if policies == nil {
		policies = NewEgressPolicyStore()
	}
	return &Router{
		pool:            cfg.Pool,
		states:          xsync.NewMap[string, *PlatformRoutingState](),
		selectedStats:   xsync.NewMap[string, *IPLoadStats](),
		globalStats:     NewIPLoadStats(),
		egressPolicies:  policies,
		authorities:     cfg.Authorities,
		p2cWindow:       cfg.P2CWindow,
		onLeaseEvent:    cfg.OnLeaseEvent,
		nodeTagResolver: cfg.NodeTagResolver,
	}
}

type RouteResult struct {
	PlatformID           string
	PlatformName         string
	SelectedPlatformID   string
	SelectedPlatformName string
	FallbackPath         []string
	SelectionReason      string
	NodeHash             node.Hash
	EgressIP             netip.Addr
	NodeTag              string // display tag: "<Subscription>/<Tag>" (DESIGN.md §601)
	LeaseCreated         bool
	RotationStatus       string
	RotationReason       string
	RotationStrategy     string
}

// RouteOptions describes one routing request. RequiredNetwork defaults to TCP.
type RouteOptions struct {
	PlatformName    string
	Account         string
	Target          string
	RequiredNetwork string
}

type routeAttempt struct {
	ownerPlatform   *platform.Platform
	platform        *platform.Platform
	state           *PlatformRoutingState
	allocationStats *IPLoadStats
	view            platform.ReadOnlyView
	account         string
	targetDomain    string
	now             time.Time
	capacityCredit  netip.Addr
}

const livePickAttempts = 2 // first pick + one retry

type leaseInvalidationReason int

const (
	leaseInvalidationNone leaseInvalidationReason = iota
	leaseInvalidationExpire
	leaseInvalidationRemove
	leaseInvalidationNetwork
)

func (r *Router) RouteRequest(platName, account, target string) (RouteResult, error) {
	return r.RouteWithOptions(RouteOptions{
		PlatformName:    platName,
		Account:         account,
		Target:          target,
		RequiredNetwork: N.NetworkTCP,
	})
}

// RouteWithOptions routes a request through a network-compatible platform view.
func (r *Router) RouteWithOptions(options RouteOptions) (RouteResult, error) {
	requiredNetwork, err := normalizeRequiredNetwork(options.RequiredNetwork)
	if err != nil {
		return RouteResult{}, err
	}
	root, err := r.resolvePlatform(options.PlatformName)
	if err != nil {
		return RouteResult{}, err
	}
	candidates, err := r.fallbackPlatforms(root)
	if err != nil {
		return RouteResult{}, err
	}
	request := routeRequest{
		root: root, candidates: candidates, state: r.ensurePlatformState(root.ID),
		network: requiredNetwork, account: options.Account,
		targetDomain: netutil.ExtractDomain(options.Target), now: time.Now(),
	}
	var result RouteResult
	if options.Account == "" {
		result, err = r.routeRandomAcross(request)
	} else {
		result, err = r.routeStickyAcross(request)
	}
	if err != nil {
		return RouteResult{}, err
	}
	if r.nodeTagResolver != nil {
		result.NodeTag = r.nodeTagResolver(result.NodeHash)
	}
	return result, nil
}

type routeRequest struct {
	root         *platform.Platform
	candidates   []*platform.Platform
	state        *PlatformRoutingState
	network      string
	account      string
	targetDomain string
	now          time.Time
}

func (r *Router) attemptFor(request routeRequest, selected *platform.Platform) routeAttempt {
	return routeAttempt{
		ownerPlatform: request.root, platform: selected, state: request.state,
		allocationStats: r.ensureSelectedStats(selected.ID),
		view:            selected.NetworkView(request.network), account: request.account,
		targetDomain: request.targetDomain, now: request.now,
	}
}

func (r *Router) fallbackPlatforms(root *platform.Platform) ([]*platform.Platform, error) {
	result := make([]*platform.Platform, 0, 1+len(root.FallbackPlatformIDs))
	visited := make(map[string]bool)
	var visit func(*platform.Platform) error
	visit = func(current *platform.Platform) error {
		if visited[current.ID] {
			return nil
		}
		visited[current.ID] = true
		result = append(result, current)
		for _, fallbackID := range current.FallbackPlatformIDs {
			fallback, ok := r.pool.GetPlatform(fallbackID)
			if !ok {
				return fmt.Errorf("%w: fallback platform %s", ErrPlatformNotFound, fallbackID)
			}
			if err := visit(fallback); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeRequiredNetwork(required string) (string, error) {
	if required == "" {
		return N.NetworkTCP, nil
	}
	network := N.NetworkName(required)
	if network != N.NetworkTCP && network != N.NetworkUDP {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedNetwork, required)
	}
	return network, nil
}

func withPlatformContext(root, selected *platform.Platform, path []string, res RouteResult) RouteResult {
	res.PlatformID = root.ID
	res.PlatformName = root.Name
	res.SelectedPlatformID = selected.ID
	res.SelectedPlatformName = selected.Name
	res.FallbackPath = append([]string(nil), path...)
	if selected.ID == root.ID {
		res.SelectionReason = "requested_platform"
	} else {
		res.SelectionReason = "platform_fallback"
	}
	return res
}

func (r *Router) resolvePlatform(platName string) (*platform.Platform, error) {
	if platName == "" {
		if p, ok := r.pool.GetPlatform(platform.DefaultPlatformID); ok {
			return p, nil
		}
		return nil, ErrPlatformNotFound
	}
	p, ok := r.pool.GetPlatformByName(platName)
	if !ok {
		return nil, ErrPlatformNotFound
	}
	return p, nil
}

func (r *Router) ensurePlatformState(platformID string) *PlatformRoutingState {
	state, _ := r.states.LoadOrCompute(platformID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})
	return state
}

func (r *Router) ensureSelectedStats(platformID string) *IPLoadStats {
	stats, _ := r.selectedStats.LoadOrCompute(platformID, func() (*IPLoadStats, bool) {
		return NewIPLoadStats(), false
	})
	return stats
}

func (r *Router) routeRandomAcross(request routeRequest) (RouteResult, error) {
	path := make([]string, 0, len(request.candidates))
	for _, candidate := range request.candidates {
		path = append(path, candidate.ID)
		result, err := r.routeRandom(r.attemptFor(request, candidate))
		if errors.Is(err, ErrNoAvailableNodes) {
			continue
		}
		if err != nil {
			return RouteResult{}, err
		}
		return withPlatformContext(request.root, candidate, path, result), nil
	}
	return RouteResult{}, ErrNoAvailableNodes
}

func (r *Router) routeRandom(attempt routeAttempt) (RouteResult, error) {
	h, entry, err := r.selectLiveRandomRoute(attempt, false)
	if err != nil {
		return RouteResult{}, err
	}
	return RouteResult{
		NodeHash:     h,
		EgressIP:     entry.GetEgressIP(),
		LeaseCreated: false,
	}, nil
}

func (r *Router) routeStickyAcross(request routeRequest) (RouteResult, error) {
	var result RouteResult
	var routeErr error

	_, _ = request.state.Leases.leases.Compute(request.account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
		newLease, op, routeResult, err := r.decideStickyLeaseAcross(request, current, loaded)
		if err != nil {
			routeErr = err
			return newLease, op
		}
		result = routeResult
		return newLease, op
	})

	return result, routeErr
}

func (r *Router) decideStickyLeaseAcross(
	request routeRequest,
	current Lease,
	loaded bool,
) (Lease, xsync.ComputeOp, RouteResult, error) {
	invalidation := leaseInvalidationNone
	if loaded && current.SelectedPlatformID == "" {
		current.SelectedPlatformID = request.root.ID
	}
	if loaded && current.IsExpired(request.now) {
		invalidation = leaseInvalidationExpire
	} else if loaded {
		if trigger, due := r.rotationDue(request.root, current, request.now); due {
			return r.rotateStickyLeaseAcross(request, current, trigger)
		}
		if selected := findCandidatePlatform(request.candidates, current.SelectedPlatformID); selected != nil {
			attempt := r.attemptFor(request, selected)
			if next, hit, ok := r.tryLeaseHit(attempt, current); ok {
				path := fallbackPathTo(request.candidates, selected.ID)
				return next, xsync.UpdateOp, withPlatformContext(request.root, selected, path, hit), nil
			}
			if next, rotated, ok := r.tryLeaseSameIPRotation(attempt, current); ok {
				path := fallbackPathTo(request.candidates, selected.ID)
				return next, xsync.UpdateOp, withPlatformContext(request.root, selected, path, rotated), nil
			}
			invalidation = r.leaseInvalidation(attempt, current)
		} else {
			invalidation = leaseInvalidationRemove
		}
	}

	return r.createStickyLeaseAcross(request, current, loaded, invalidation)
}

func (r *Router) createStickyLeaseAcross(
	request routeRequest,
	previous Lease,
	hadPrevious bool,
	invalidation leaseInvalidationReason,
) (Lease, xsync.ComputeOp, RouteResult, error) {
	r.allocationMu.Lock()
	defer r.allocationMu.Unlock()

	path := make([]string, 0, len(request.candidates))
	for _, candidate := range request.candidates {
		path = append(path, candidate.ID)
		attempt := r.attemptFor(request, candidate)
		if hadPrevious {
			attempt.capacityCredit = previous.EgressIP
		}
		next, result, err := r.createLease(attempt)
		if errors.Is(err, ErrNoAvailableNodes) {
			continue
		}
		if err != nil {
			return r.abortStickyCreate(request, previous, hadPrevious, invalidation, err)
		}
		r.commitLeaseTransition(request, previous, hadPrevious, invalidation, next)
		if hadPrevious && invalidation == leaseInvalidationNetwork {
			result.LeaseCreated = false
		}
		return next, xsync.UpdateOp, withPlatformContext(request.root, candidate, path, result), nil
	}
	return r.abortStickyCreate(request, previous, hadPrevious, invalidation, ErrNoAvailableNodes)
}

func (r *Router) abortStickyCreate(
	request routeRequest,
	previous Lease,
	hadPrevious bool,
	invalidation leaseInvalidationReason,
	err error,
) (Lease, xsync.ComputeOp, RouteResult, error) {
	if hadPrevious && invalidation == leaseInvalidationNetwork {
		return previous, xsync.CancelOp, RouteResult{}, err
	}
	if hadPrevious {
		r.removeLeaseAccounting(request, previous, invalidation)
		return previous, xsync.DeleteOp, RouteResult{}, err
	}
	return previous, xsync.CancelOp, RouteResult{}, err
}

func findCandidatePlatform(candidates []*platform.Platform, id string) *platform.Platform {
	for _, candidate := range candidates {
		if candidate.ID == id {
			return candidate
		}
	}
	return nil
}

func fallbackPathTo(candidates []*platform.Platform, selectedID string) []string {
	path := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		path = append(path, candidate.ID)
		if candidate.ID == selectedID {
			break
		}
	}
	return path
}

func (r *Router) tryLeaseHit(
	attempt routeAttempt,
	current Lease,
) (Lease, RouteResult, bool) {
	entry, ok := r.pool.GetEntry(current.NodeHash)
	if !ok || !attempt.view.Contains(current.NodeHash) || entry.GetEgressIP() != current.EgressIP {
		return Lease{}, RouteResult{}, false
	}
	if !r.allowsExistingEgress(current.EgressIP, attempt.now) {
		return Lease{}, RouteResult{}, false
	}

	newLease := current
	newLease.LastAccessedNs = attempt.now.UnixNano()
	r.emitLeaseEvent(LeaseEvent{
		Type:               LeaseTouch,
		PlatformID:         attempt.ownerPlatform.ID,
		SelectedPlatformID: current.SelectedPlatformID,
		Account:            attempt.account,
		NodeHash:           current.NodeHash,
		EgressIP:           current.EgressIP,
	})
	return newLease, RouteResult{
		NodeHash:     current.NodeHash,
		EgressIP:     current.EgressIP,
		LeaseCreated: false,
	}, true
}

func (r *Router) tryLeaseSameIPRotation(
	attempt routeAttempt,
	current Lease,
) (Lease, RouteResult, bool) {
	if !r.allowsExistingEgress(current.EgressIP, attempt.now) {
		return Lease{}, RouteResult{}, false
	}
	bestHash, ok := chooseSameIPRotationCandidate(sameIPRotationOptions{
		view:         attempt.view,
		pool:         r.pool,
		targetIP:     current.EgressIP,
		targetDomain: attempt.targetDomain,
		authorities:  r.authorities(),
		window:       r.p2cWindow(),
	})
	if !ok {
		return Lease{}, RouteResult{}, false
	}

	newLease := current
	newLease.NodeHash = bestHash
	newLease.LastAccessedNs = attempt.now.UnixNano()
	r.emitLeaseEvent(LeaseEvent{
		Type:               LeaseReplace,
		PlatformID:         attempt.ownerPlatform.ID,
		SelectedPlatformID: current.SelectedPlatformID,
		Account:            attempt.account,
		NodeHash:           bestHash,
		EgressIP:           current.EgressIP,
	})
	return newLease, RouteResult{
		NodeHash:     bestHash,
		EgressIP:     current.EgressIP,
		LeaseCreated: false,
	}, true
}

func (r *Router) createLease(attempt routeAttempt) (Lease, RouteResult, error) {
	h, entry, err := r.selectLiveRandomRoute(attempt, true)
	if err != nil {
		return Lease{}, RouteResult{}, err
	}
	ttl := attempt.ownerPlatform.StickyTTLNs
	if ttl <= 0 {
		ttl = int64(24 * time.Hour) // Default safeguard
	}
	nowNs := attempt.now.UnixNano()

	lease := Lease{
		NodeHash:           h,
		EgressIP:           entry.GetEgressIP(),
		SelectedPlatformID: attempt.platform.ID,
		CreatedAtNs:        nowNs,
		ExpiryNs:           attempt.now.Add(time.Duration(ttl)).UnixNano(),
		LastAccessedNs:     nowNs,
		LastRotatedAtNs:    nowNs,
	}
	return lease, RouteResult{
		NodeHash:     lease.NodeHash,
		EgressIP:     lease.EgressIP,
		LeaseCreated: true,
	}, nil
}

func (r *Router) leaseInvalidation(attempt routeAttempt, current Lease) leaseInvalidationReason {
	if !r.allowsExistingEgress(current.EgressIP, attempt.now) {
		return leaseInvalidationRemove
	}
	entry, ok := r.pool.GetEntry(current.NodeHash)
	if ok && attempt.platform.View().Contains(current.NodeHash) && entry.GetEgressIP() == current.EgressIP {
		return leaseInvalidationNetwork
	}
	return leaseInvalidationRemove
}

func (r *Router) commitLeaseTransition(
	request routeRequest,
	previous Lease,
	hadPrevious bool,
	invalidation leaseInvalidationReason,
	next Lease,
) {
	if hadPrevious && invalidation == leaseInvalidationNetwork {
		r.decrementLeaseLoads(request.root.ID, request.state, previous)
		r.incrementLeaseLoads(request.state, next)
		r.emitLeaseTransition(request, previous, next, LeaseReplace)
		return
	}
	if hadPrevious {
		r.removeLeaseAccounting(request, previous, invalidation)
	}
	r.incrementLeaseLoads(request.state, next)
	r.emitLeaseTransition(request, previous, next, LeaseCreate)
}

func (r *Router) emitLeaseTransition(
	request routeRequest,
	previous Lease,
	next Lease,
	eventType LeaseEventType,
) {
	r.emitLeaseEvent(LeaseEvent{
		Type: eventType, PlatformID: request.root.ID, SelectedPlatformID: next.SelectedPlatformID,
		Account: request.account, NodeHash: next.NodeHash, EgressIP: next.EgressIP,
		CreatedAtNs: previous.CreatedAtNs,
	})
}

func (r *Router) removeLeaseAccounting(
	request routeRequest,
	lease Lease,
	invalidation leaseInvalidationReason,
) {
	r.decrementLeaseLoads(request.root.ID, request.state, lease)
	eventType := LeaseRemove
	if invalidation == leaseInvalidationExpire {
		eventType = LeaseExpire
	}
	r.emitLeaseEvent(LeaseEvent{
		Type: eventType, PlatformID: request.root.ID, SelectedPlatformID: lease.SelectedPlatformID,
		Account: request.account, NodeHash: lease.NodeHash, EgressIP: lease.EgressIP,
		CreatedAtNs: lease.CreatedAtNs,
	})
}

func (r *Router) emitLeaseEvent(event LeaseEvent) {
	if r.onLeaseEvent != nil {
		r.onLeaseEvent(event)
	}
}

func (r *Router) selectLiveRandomRoute(
	attempt routeAttempt,
	enforceCapacity bool,
) (node.Hash, *node.NodeEntry, error) {
	view := r.newAllocationView(attempt, enforceCapacity)
	var lastMissing node.Hash
	for i := 0; i < livePickAttempts; i++ {
		h, err := randomRoute(randomRouteOptions{
			platform:     attempt.platform,
			view:         view,
			stats:        attempt.allocationStats,
			pool:         r.pool,
			targetDomain: attempt.targetDomain,
			authorities:  r.authorities(),
			p2cWindow:    r.p2cWindow(),
		})
		if err != nil {
			return node.Zero, nil, err
		}
		entry, ok := r.pool.GetEntry(h)
		if ok {
			return h, entry, nil
		}
		lastMissing = h
	}
	if lastMissing != node.Zero {
		return node.Zero, nil, fmt.Errorf("%w: selected node %s no longer in pool", ErrNoAvailableNodes, lastMissing.Hex())
	}
	return node.Zero, nil, ErrNoAvailableNodes
}

type sameIPRotationOptions struct {
	view         platform.ReadOnlyView
	pool         PoolAccessor
	targetIP     netip.Addr
	targetDomain string
	authorities  []string
	window       time.Duration
}

func chooseSameIPRotationCandidate(options sameIPRotationOptions) (node.Hash, bool) {
	bestKnownHash := node.Zero
	bestKnownLatency := time.Duration(math.MaxInt64)
	fallbackHash := node.Zero

	options.view.Range(func(h node.Hash) bool {
		entry, ok := options.pool.GetEntry(h)
		if !ok || entry.GetEgressIP() != options.targetIP {
			return true
		}
		if fallbackHash == node.Zero {
			fallbackHash = h
		}

		latency, hasLatency := sameIPCandidateLatency(
			entry,
			options.targetDomain,
			options.authorities,
			options.window,
		)
		if hasLatency && latency < bestKnownLatency {
			bestKnownLatency = latency
			bestKnownHash = h
		}
		return true
	})

	if bestKnownHash != node.Zero {
		return bestKnownHash, true
	}
	if fallbackHash != node.Zero {
		return fallbackHash, true
	}
	return node.Zero, false
}

func sameIPCandidateLatency(
	entry *node.NodeEntry,
	targetDomain string,
	authorities []string,
	window time.Duration,
) (time.Duration, bool) {
	now := time.Now()
	if latency, ok := lookupRecentDomainLatency(entry, targetDomain, now, window); ok {
		return latency, true
	}

	if latency, ok := averageRecentAuthorityLatency(entry, authorities, now, window); ok {
		return latency, true
	}
	return 0, false
}

// ReadLease implements weak persistence read.
func (r *Router) ReadLease(key model.LeaseKey) *model.Lease {
	state, ok := r.states.Load(key.PlatformID)
	if !ok {
		return nil
	}
	lease, ok := state.Leases.GetLease(key.Account)
	if !ok {
		return nil
	}
	return &model.Lease{
		PlatformID:                key.PlatformID,
		SelectedPlatformID:        lease.SelectedPlatformID,
		Account:                   key.Account,
		NodeHash:                  lease.NodeHash.Hex(),
		EgressIP:                  lease.EgressIP.String(),
		CreatedAtNs:               lease.CreatedAtNs,
		ExpiryNs:                  lease.ExpiryNs,
		LastAccessedNs:            lease.LastAccessedNs,
		LastRotatedAtNs:           lease.LastRotatedAtNs,
		RotationRequestedAtNs:     lease.RotationRequestedAtNs,
		RotationRequestedStrategy: lease.RotationRequestedStrategy,
		RotationDeferred:          lease.RotationDeferred,
		RotationDeferredReason:    lease.RotationDeferredReason,
	}
}

// UpsertLease writes or replaces a lease for (platform_id, account).
// It updates per-IP lease counters and emits LeaseCreate/LeaseReplace events.
func (r *Router) UpsertLease(ml model.Lease) error {
	platformID := strings.TrimSpace(ml.PlatformID)
	if platformID == "" {
		return errors.New("platform_id is required")
	}
	account := strings.TrimSpace(ml.Account)
	if account == "" {
		return errors.New("account is required")
	}

	h, err := node.ParseHex(ml.NodeHash)
	if err != nil {
		return fmt.Errorf("parse node_hash: %w", err)
	}
	ip, err := netip.ParseAddr(ml.EgressIP)
	if err != nil {
		return fmt.Errorf("parse egress_ip: %w", err)
	}

	state := r.ensurePlatformState(platformID)
	lease := Lease{
		NodeHash:                  h,
		EgressIP:                  ip,
		SelectedPlatformID:        strings.TrimSpace(ml.SelectedPlatformID),
		CreatedAtNs:               ml.CreatedAtNs,
		ExpiryNs:                  ml.ExpiryNs,
		LastAccessedNs:            ml.LastAccessedNs,
		LastRotatedAtNs:           ml.LastRotatedAtNs,
		RotationRequestedAtNs:     ml.RotationRequestedAtNs,
		RotationRequestedStrategy: ml.RotationRequestedStrategy,
		RotationDeferred:          ml.RotationDeferred,
		RotationDeferredReason:    ml.RotationDeferredReason,
	}
	if lease.SelectedPlatformID == "" {
		lease.SelectedPlatformID = platformID
	}

	eventType := LeaseCreate
	var allocationErr error
	_, _ = state.Leases.leases.Compute(account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
		r.allocationMu.Lock()
		defer r.allocationMu.Unlock()
		if !r.canUpsertLease(lease, current, loaded, time.Now()) {
			allocationErr = ErrNoAvailableNodes
			return current, xsync.CancelOp
		}
		if loaded {
			r.decrementLeaseLoads(platformID, state, current)
			eventType = LeaseReplace
		}
		r.incrementLeaseLoads(state, lease)
		return lease, xsync.UpdateOp
	})
	if allocationErr != nil {
		return allocationErr
	}

	r.emitLeaseEvent(LeaseEvent{
		Type:               eventType,
		PlatformID:         platformID,
		SelectedPlatformID: lease.SelectedPlatformID,
		Account:            account,
		NodeHash:           lease.NodeHash,
		EgressIP:           lease.EgressIP,
	})
	return nil
}

// SnapshotIPLoad returns a best-effort point-in-time IP load snapshot for a platform.
// If the platform has no routing state yet, it returns an empty snapshot.
func (r *Router) SnapshotIPLoad(platformID string) map[netip.Addr]int64 {
	state, ok := r.states.Load(platformID)
	if !ok {
		return map[netip.Addr]int64{}
	}
	return state.IPLoadStats.Snapshot()
}

// RestoreLeases restores leases from persistence during bootstrap.
func (r *Router) RestoreLeases(leases []model.Lease) {
	for _, ml := range leases {
		h, err := node.ParseHex(ml.NodeHash)
		if err != nil {
			continue
		}
		ip, err := netip.ParseAddr(ml.EgressIP)
		if err != nil {
			continue
		}

		state, _ := r.states.LoadOrCompute(ml.PlatformID, func() (*PlatformRoutingState, bool) {
			return NewPlatformRoutingState(), false
		})

		l := Lease{
			NodeHash:                  h,
			EgressIP:                  ip,
			SelectedPlatformID:        strings.TrimSpace(ml.SelectedPlatformID),
			CreatedAtNs:               ml.CreatedAtNs,
			ExpiryNs:                  ml.ExpiryNs,
			LastAccessedNs:            ml.LastAccessedNs,
			LastRotatedAtNs:           ml.LastRotatedAtNs,
			RotationRequestedAtNs:     ml.RotationRequestedAtNs,
			RotationRequestedStrategy: ml.RotationRequestedStrategy,
			RotationDeferred:          ml.RotationDeferred,
			RotationDeferredReason:    ml.RotationDeferredReason,
		}
		if l.SelectedPlatformID == "" {
			l.SelectedPlatformID = ml.PlatformID
		}
		_, _ = state.Leases.leases.Compute(ml.Account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
			r.allocationMu.Lock()
			defer r.allocationMu.Unlock()
			if loaded {
				r.decrementLeaseLoads(ml.PlatformID, state, current)
			}
			r.incrementLeaseLoads(state, l)
			return l, xsync.UpdateOp
		})
	}
}

// RangeLeases iterates over all leases for a platform.
// Returns false if the platform has no routing state.
func (r *Router) RangeLeases(platformID string, fn func(account string, lease Lease) bool) bool {
	state, ok := r.states.Load(platformID)
	if !ok {
		return false
	}
	state.Leases.Range(fn)
	return true
}

// DeleteLease removes a single lease by platform and account.
// Returns true if a lease was deleted. Emits a LeaseRemove event.
func (r *Router) DeleteLease(platformID, account string) bool {
	state, ok := r.states.Load(platformID)
	if !ok {
		return false
	}
	var lease Lease
	deleted := false
	_, _ = state.Leases.leases.Compute(account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
		if !loaded {
			return current, xsync.CancelOp
		}
		r.allocationMu.Lock()
		defer r.allocationMu.Unlock()
		lease = current
		deleted = true
		r.decrementLeaseLoads(platformID, state, current)
		return current, xsync.DeleteOp
	})
	if !deleted {
		return false
	}
	selectedPlatformID := lease.SelectedPlatformID
	if selectedPlatformID == "" {
		selectedPlatformID = platformID
	}
	r.emitLeaseEvent(LeaseEvent{
		Type:               LeaseRemove,
		PlatformID:         platformID,
		SelectedPlatformID: selectedPlatformID,
		Account:            account,
		NodeHash:           lease.NodeHash,
		EgressIP:           lease.EgressIP,
		CreatedAtNs:        lease.CreatedAtNs,
	})
	return true
}

// DeleteAllLeases removes all leases for a platform.
// Returns the number of leases deleted. Emits a LeaseRemove event for each.
func (r *Router) DeleteAllLeases(platformID string) int {
	state, ok := r.states.Load(platformID)
	if !ok {
		return 0
	}
	count := 0
	state.Leases.Range(func(account string, _ Lease) bool {
		var removed Lease
		deleted := false
		_, _ = state.Leases.leases.Compute(account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
			if !loaded {
				return current, xsync.CancelOp
			}
			r.allocationMu.Lock()
			defer r.allocationMu.Unlock()
			removed = current
			deleted = true
			r.decrementLeaseLoads(platformID, state, current)
			return current, xsync.DeleteOp
		})
		if deleted {
			selectedPlatformID := removed.SelectedPlatformID
			if selectedPlatformID == "" {
				selectedPlatformID = platformID
			}
			r.emitLeaseEvent(LeaseEvent{
				Type:               LeaseRemove,
				PlatformID:         platformID,
				SelectedPlatformID: selectedPlatformID,
				Account:            account,
				NodeHash:           removed.NodeHash,
				EgressIP:           removed.EgressIP,
				CreatedAtNs:        removed.CreatedAtNs,
			})
			count++
		}
		return true
	})
	return count
}
