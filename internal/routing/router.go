package routing

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strings"
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
}

func NewRouter(cfg RouterConfig) *Router {
	return &Router{
		pool:            cfg.Pool,
		states:          xsync.NewMap[string, *PlatformRoutingState](),
		authorities:     cfg.Authorities,
		p2cWindow:       cfg.P2CWindow,
		onLeaseEvent:    cfg.OnLeaseEvent,
		nodeTagResolver: cfg.NodeTagResolver,
	}
}

type RouteResult struct {
	PlatformID   string
	PlatformName string
	NodeHash     node.Hash
	EgressIP     netip.Addr
	NodeTag      string // display tag: "<Subscription>/<Tag>" (DESIGN.md §601)
	LeaseCreated bool
}

// RouteOptions describes one routing request. RequiredNetwork defaults to TCP.
type RouteOptions struct {
	PlatformName    string
	Account         string
	Target          string
	RequiredNetwork string
}

type routeAttempt struct {
	platform     *platform.Platform
	state        *PlatformRoutingState
	view         platform.ReadOnlyView
	account      string
	targetDomain string
	now          time.Time
}

type previousLeaseState struct {
	lease        Lease
	loaded       bool
	invalidation leaseInvalidationReason
}

type networkLeaseReplacement struct {
	previous Lease
	next     Lease
	result   RouteResult
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
	plat, err := r.resolvePlatform(options.PlatformName)
	if err != nil {
		return RouteResult{}, err
	}

	attempt := routeAttempt{
		platform:     plat,
		state:        r.ensurePlatformState(plat.ID),
		view:         plat.NetworkView(requiredNetwork),
		account:      options.Account,
		targetDomain: netutil.ExtractDomain(options.Target),
		now:          time.Now(),
	}
	var result RouteResult
	if options.Account == "" {
		result, err = r.routeRandom(attempt)
	} else {
		result, err = r.routeSticky(attempt)
	}
	if err != nil {
		return RouteResult{}, err
	}
	result = withPlatformContext(plat, result)
	if r.nodeTagResolver != nil {
		result.NodeTag = r.nodeTagResolver(result.NodeHash)
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

func withPlatformContext(plat *platform.Platform, res RouteResult) RouteResult {
	res.PlatformID = plat.ID
	res.PlatformName = plat.Name
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

func (r *Router) routeRandom(attempt routeAttempt) (RouteResult, error) {
	h, entry, err := r.selectLiveRandomRoute(attempt)
	if err != nil {
		return RouteResult{}, err
	}
	return RouteResult{
		NodeHash:     h,
		EgressIP:     entry.GetEgressIP(),
		LeaseCreated: false,
	}, nil
}

func (r *Router) routeSticky(attempt routeAttempt) (RouteResult, error) {
	var result RouteResult
	var routeErr error

	_, _ = attempt.state.Leases.leases.Compute(attempt.account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
		newLease, op, routeResult, err := r.decideStickyLease(attempt, current, loaded)
		if err != nil {
			routeErr = err
			return newLease, op
		}
		result = routeResult
		return newLease, op
	})

	return result, routeErr
}

func (r *Router) decideStickyLease(
	attempt routeAttempt,
	current Lease,
	loaded bool,
) (Lease, xsync.ComputeOp, RouteResult, error) {
	hadPreviousLease := loaded
	invalidation := leaseInvalidationNone

	if loaded && current.IsExpired(attempt.now) {
		invalidation = leaseInvalidationExpire
		loaded = false
	}

	if loaded {
		if newLease, hitResult, ok := r.tryLeaseHit(attempt, current); ok {
			return newLease, xsync.UpdateOp, hitResult, nil
		}
		if newLease, rotatedResult, ok := r.tryLeaseSameIPRotation(attempt, current); ok {
			return newLease, xsync.UpdateOp, rotatedResult, nil
		}
		invalidation = r.leaseInvalidation(attempt, current)
	}

	return r.createOrAbortStickyLease(attempt, previousLeaseState{
		lease:        current,
		loaded:       hadPreviousLease,
		invalidation: invalidation,
	})
}

func (r *Router) createOrAbortStickyLease(
	attempt routeAttempt,
	previous previousLeaseState,
) (Lease, xsync.ComputeOp, RouteResult, error) {
	newLease, createdResult, err := r.createLease(attempt)
	if err != nil {
		if previous.invalidation == leaseInvalidationNetwork {
			return previous.lease, xsync.CancelOp, RouteResult{}, err
		}
		r.cleanupPreviousLease(
			attempt.state,
			previous.lease,
			previous.loaded,
			previous.invalidation,
			attempt.platform.ID,
			attempt.account,
		)
		lease, op := abortLeaseCreate(previous.lease, previous.loaded)
		return lease, op, RouteResult{}, err
	}

	if previous.invalidation == leaseInvalidationNetwork {
		return r.replaceNetworkLease(attempt, networkLeaseReplacement{
			previous: previous.lease,
			next:     newLease,
			result:   createdResult,
		})
	}
	r.cleanupPreviousLease(
		attempt.state,
		previous.lease,
		previous.loaded,
		previous.invalidation,
		attempt.platform.ID,
		attempt.account,
	)
	attempt.state.IPLoadStats.Inc(newLease.EgressIP)
	r.emitLeaseEvent(LeaseEvent{
		Type:       LeaseCreate,
		PlatformID: attempt.platform.ID,
		Account:    attempt.account,
		NodeHash:   newLease.NodeHash,
		EgressIP:   newLease.EgressIP,
	})
	return newLease, xsync.UpdateOp, createdResult, nil
}

func (r *Router) tryLeaseHit(
	attempt routeAttempt,
	current Lease,
) (Lease, RouteResult, bool) {
	entry, ok := r.pool.GetEntry(current.NodeHash)
	if !ok || !attempt.view.Contains(current.NodeHash) || entry.GetEgressIP() != current.EgressIP {
		return Lease{}, RouteResult{}, false
	}

	newLease := current
	newLease.LastAccessedNs = attempt.now.UnixNano()
	r.emitLeaseEvent(LeaseEvent{
		Type:       LeaseTouch,
		PlatformID: attempt.platform.ID,
		Account:    attempt.account,
		NodeHash:   current.NodeHash,
		EgressIP:   current.EgressIP,
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
		Type:       LeaseReplace,
		PlatformID: attempt.platform.ID,
		Account:    attempt.account,
		NodeHash:   bestHash,
		EgressIP:   current.EgressIP,
	})
	return newLease, RouteResult{
		NodeHash:     bestHash,
		EgressIP:     current.EgressIP,
		LeaseCreated: false,
	}, true
}

func (r *Router) createLease(attempt routeAttempt) (Lease, RouteResult, error) {
	h, entry, err := r.selectLiveRandomRoute(attempt)
	if err != nil {
		return Lease{}, RouteResult{}, err
	}
	ttl := attempt.platform.StickyTTLNs
	if ttl <= 0 {
		ttl = int64(24 * time.Hour) // Default safeguard
	}
	nowNs := attempt.now.UnixNano()

	lease := Lease{
		NodeHash:       h,
		EgressIP:       entry.GetEgressIP(),
		CreatedAtNs:    nowNs,
		ExpiryNs:       attempt.now.Add(time.Duration(ttl)).UnixNano(),
		LastAccessedNs: nowNs,
	}
	return lease, RouteResult{
		NodeHash:     lease.NodeHash,
		EgressIP:     lease.EgressIP,
		LeaseCreated: true,
	}, nil
}

func (r *Router) leaseInvalidation(attempt routeAttempt, current Lease) leaseInvalidationReason {
	entry, ok := r.pool.GetEntry(current.NodeHash)
	if ok && attempt.platform.View().Contains(current.NodeHash) && entry.GetEgressIP() == current.EgressIP {
		return leaseInvalidationNetwork
	}
	return leaseInvalidationRemove
}

func (r *Router) replaceNetworkLease(
	attempt routeAttempt,
	replacement networkLeaseReplacement,
) (Lease, xsync.ComputeOp, RouteResult, error) {
	attempt.state.IPLoadStats.Dec(replacement.previous.EgressIP)
	attempt.state.IPLoadStats.Inc(replacement.next.EgressIP)
	r.emitLeaseEvent(LeaseEvent{
		Type:        LeaseReplace,
		PlatformID:  attempt.platform.ID,
		Account:     attempt.account,
		NodeHash:    replacement.next.NodeHash,
		EgressIP:    replacement.next.EgressIP,
		CreatedAtNs: replacement.previous.CreatedAtNs,
	})
	replacement.result.LeaseCreated = false
	return replacement.next, xsync.UpdateOp, replacement.result, nil
}

func (r *Router) cleanupPreviousLease(
	state *PlatformRoutingState,
	lease Lease,
	hadPreviousLease bool,
	invalidation leaseInvalidationReason,
	platformID string,
	account string,
) {
	if !hadPreviousLease {
		return
	}
	state.Leases.stats.Dec(lease.EgressIP)
	switch invalidation {
	case leaseInvalidationExpire:
		r.emitLeaseEvent(LeaseEvent{
			Type:        LeaseExpire,
			PlatformID:  platformID,
			Account:     account,
			NodeHash:    lease.NodeHash,
			EgressIP:    lease.EgressIP,
			CreatedAtNs: lease.CreatedAtNs,
		})
	case leaseInvalidationRemove:
		r.emitLeaseEvent(LeaseEvent{
			Type:        LeaseRemove,
			PlatformID:  platformID,
			Account:     account,
			NodeHash:    lease.NodeHash,
			EgressIP:    lease.EgressIP,
			CreatedAtNs: lease.CreatedAtNs,
		})
	}
}

func abortLeaseCreate(current Lease, hadPreviousLease bool) (Lease, xsync.ComputeOp) {
	if hadPreviousLease {
		return current, xsync.DeleteOp
	}
	return current, xsync.CancelOp
}

func (r *Router) emitLeaseEvent(event LeaseEvent) {
	if r.onLeaseEvent != nil {
		r.onLeaseEvent(event)
	}
}

func (r *Router) selectLiveRandomRoute(
	attempt routeAttempt,
) (node.Hash, *node.NodeEntry, error) {
	var lastMissing node.Hash
	for i := 0; i < livePickAttempts; i++ {
		h, err := randomRoute(randomRouteOptions{
			platform:     attempt.platform,
			view:         attempt.view,
			stats:        attempt.state.IPLoadStats,
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
		PlatformID:     key.PlatformID,
		Account:        key.Account,
		NodeHash:       lease.NodeHash.Hex(),
		EgressIP:       lease.EgressIP.String(),
		CreatedAtNs:    lease.CreatedAtNs,
		ExpiryNs:       lease.ExpiryNs,
		LastAccessedNs: lease.LastAccessedNs,
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
		NodeHash:       h,
		EgressIP:       ip,
		CreatedAtNs:    ml.CreatedAtNs,
		ExpiryNs:       ml.ExpiryNs,
		LastAccessedNs: ml.LastAccessedNs,
	}

	eventType := LeaseCreate
	_, _ = state.Leases.leases.Compute(account, func(current Lease, loaded bool) (Lease, xsync.ComputeOp) {
		if loaded {
			state.Leases.stats.Dec(current.EgressIP)
			eventType = LeaseReplace
		}
		state.Leases.stats.Inc(lease.EgressIP)
		return lease, xsync.UpdateOp
	})

	r.emitLeaseEvent(LeaseEvent{
		Type:       eventType,
		PlatformID: platformID,
		Account:    account,
		NodeHash:   lease.NodeHash,
		EgressIP:   lease.EgressIP,
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
			NodeHash:       h,
			EgressIP:       ip,
			CreatedAtNs:    ml.CreatedAtNs,
			ExpiryNs:       ml.ExpiryNs,
			LastAccessedNs: ml.LastAccessedNs,
		}
		// Directly insert into table and stats
		state.Leases.CreateLease(ml.Account, l)
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
	lease, deleted := state.Leases.DeleteLease(account)
	if !deleted {
		return false
	}
	r.emitLeaseEvent(LeaseEvent{
		Type:        LeaseRemove,
		PlatformID:  platformID,
		Account:     account,
		NodeHash:    lease.NodeHash,
		EgressIP:    lease.EgressIP,
		CreatedAtNs: lease.CreatedAtNs,
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
		removed, deleted := state.Leases.DeleteLease(account)
		if deleted {
			r.emitLeaseEvent(LeaseEvent{
				Type:        LeaseRemove,
				PlatformID:  platformID,
				Account:     account,
				NodeHash:    removed.NodeHash,
				EgressIP:    removed.EgressIP,
				CreatedAtNs: removed.CreatedAtNs,
			})
			count++
		}
		return true
	})
	return count
}
