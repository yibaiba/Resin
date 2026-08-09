package routing

import (
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

var ErrNoAvailableNodes = errors.New("no available nodes")

var randomRouteRNGPool = sync.Pool{
	New: func() any {
		return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	},
}

type randomRouteOptions struct {
	platform     *platform.Platform
	view         platform.ReadOnlyView
	stats        *IPLoadStats
	pool         PoolAccessor
	targetDomain string
	authorities  []string
	p2cWindow    time.Duration
}

type P2CTrace struct {
	FirstNodeHash  string  `json:"first_node_hash,omitempty"`
	SecondNodeHash string  `json:"second_node_hash,omitempty"`
	FirstScore     float64 `json:"first_score"`
	SecondScore    float64 `json:"second_score"`
	SelectedHash   string  `json:"selected_hash,omitempty"`
}

// randomRoute selects a routable node using P2C with latency/load scoring.
// It intentionally trusts Platform.View as the routable source of truth and
// does not do extra pool scans/availability validation on the hot path.
// Post-pick race handling (node removed right after selection) is handled by
// the caller in RouteWithOptions.
func randomRoute(options randomRouteOptions) (node.Hash, error) {
	rng := randomRouteRNGPool.Get().(*rand.Rand)
	defer randomRouteRNGPool.Put(rng)
	hash, _, err := randomRouteWithRNG(options, rng)
	return hash, err
}

func randomRouteWithRNG(options randomRouteOptions, rng *rand.Rand) (node.Hash, P2CTrace, error) {
	h1, h2, pairSize, err := pickP2CCandidates(options.view, rng)
	if err != nil {
		return node.Zero, P2CTrace{}, err
	}
	trace := P2CTrace{FirstNodeHash: h1.Hex(), SelectedHash: h1.Hex()}
	if pairSize == 1 {
		return h1, trace, nil
	}

	lat1, lat2 := compareLatencies(
		h1,
		h2,
		options.pool,
		options.targetDomain,
		options.authorities,
		options.p2cWindow,
	)

	s1 := calculateScore(h1, lat1, options.platform, options.stats, options.pool)
	s2 := calculateScore(h2, lat2, options.platform, options.stats, options.pool)
	trace.SecondNodeHash = h2.Hex()
	trace.FirstScore = s1
	trace.SecondScore = s2
	selected := h2 // favor h2 on tie
	if s1 < s2 {
		selected = h1
	}
	trace.SelectedHash = selected.Hex()
	return selected, trace, nil
}

func pickP2CCandidates(view platform.ReadOnlyView, rng *rand.Rand) (node.Hash, node.Hash, int, error) {
	size := view.Size()
	if size == 0 {
		return node.Zero, node.Zero, 0, ErrNoAvailableNodes
	}
	h1, ok := view.RandomPick(rng)
	if !ok {
		return node.Zero, node.Zero, 0, ErrNoAvailableNodes
	}
	if size == 1 {
		return h1, node.Zero, 1, nil
	}
	h2, ok := view.RandomPick(rng)
	if !ok {
		return h1, node.Zero, 1, nil
	}
	for i := 0; h2 == h1 && i < 3; i++ {
		candidate, candidateOK := view.RandomPick(rng)
		if !candidateOK {
			break
		}
		h2 = candidate
	}
	if h2 == h1 {
		return h1, node.Zero, 1, nil
	}
	return h1, h2, 2, nil
}

// compareLatencies determines the latency values for h1 and h2.
// Implements the 3-level comparison logic:
// 1. Target domain present in both and recent.
// 2. Common authority domains present in both and recent.
// 3. Fallback to 0 (empty) for both.
func compareLatencies(
	h1, h2 node.Hash,
	pool PoolAccessor,
	target string,
	authorities []string,
	window time.Duration,
) (time.Duration, time.Duration) {
	e1, ok1 := pool.GetEntry(h1)
	e2, ok2 := pool.GetEntry(h2)
	if !ok1 || !ok2 || e1.LatencyTable == nil || e2.LatencyTable == nil {
		return 0, 0
	}

	now := time.Now()

	// 1. Target domain check.
	// target can be empty if extracted domain is invalid/empty, handle gracefully.
	lat1, ok1 := lookupRecentDomainLatency(e1, target, now, window)
	lat2, ok2 := lookupRecentDomainLatency(e2, target, now, window)
	if ok1 && ok2 {
		return lat1, lat2
	}

	// 2. Authority intersection check.
	lat1, lat2, ok := averageComparableAuthorityLatencies(e1, e2, authorities, now, window)
	if ok {
		return lat1, lat2
	}

	// 3. Fallback.
	return 0, 0
}

func isRecent(t time.Time, now time.Time, window time.Duration) bool {
	return now.Sub(t) <= window
}

// calculateScore computes the score for a node based on platform allocation policy.
// Lower is better.
func calculateScore(
	h node.Hash,
	latency time.Duration,
	plat *platform.Platform,
	stats *IPLoadStats,
	pool PoolAccessor,
) float64 {
	entry, _ := pool.GetEntry(h)
	// If entry is nil (race), treat as high load/latency?
	// But we hold ref via pool, only deletion removes it.
	// Assuming existence since we just picked it from view.

	// Lease count from stats.
	var leaseCount int64
	if entry != nil {
		ip := entry.GetEgressIP()
		if ip.IsValid() {
			leaseCount = stats.Get(ip)
		}
	}

	// If latency is 0 (empty/incompatible), score = LeaseCount strictly.
	if latency <= 0 {
		return float64(leaseCount)
	}

	// Policy-based scoring.
	switch plat.AllocationPolicy {
	case platform.AllocationPolicyPreferLowLatency:
		return float64(latency)
	case platform.AllocationPolicyPreferIdleIP:
		return float64(leaseCount)
	case platform.AllocationPolicyBalanced:
		fallthrough
	default:
		// (LeaseCount + 1) * Latency
		return float64(leaseCount+1) * float64(latency)
	}
}
