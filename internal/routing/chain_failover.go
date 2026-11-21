package routing

import (
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	N "github.com/sagernet/sing/common/network"
)

const maxFailedHopsPerAccount = 16

var chainHopFailExcludeTTL = 30 * time.Second

// NoteChainDialFailure records that a chained Dial/Listen failed. The landing
// stays health-neutral; the first hop is excluded for a short TTL so the next
// request can pick another hop. An existing hop sticky lease is rotated when
// account is non-empty. Chain failures are not written to hop or landing
// circuits: hop health stays on the hop platform's own probes.
func (r *Router) NoteChainDialFailure(account string, route RouteResult) {
	if r == nil || len(route.ChainHops) == 0 {
		return
	}
	hop := route.ChainHops[0]
	if hop.NodeHash.IsZero() {
		return
	}
	r.rememberFailedHop(account, hop.NodeHash)
	if account == "" || hop.PlatformID == "" {
		return
	}
	r.rotateHopLease(hop.PlatformID, account, route.Network)
}

func (r *Router) rotateHopLease(platformID, account, network string) {
	r.leaseLifecycleMu.RLock()
	defer r.leaseLifecycleMu.RUnlock()

	root, ok := r.pool.GetPlatform(platformID)
	if !ok {
		return
	}
	candidates, err := r.fallbackPlatforms(root)
	if err != nil {
		return
	}
	if network == "" {
		network = N.NetworkTCP
	}
	request := routeRequest{
		root: root, candidates: candidates, state: r.ensurePlatformState(root.ID),
		network: network, account: account, now: time.Now(),
		hopPick: true,
	}
	_, _ = r.rotateLeaseRequest(request, rotationTrigger{
		strategy: platform.RotationStrategyAny, reason: "chain_dial_failure", manual: true,
	})
}

// NoteProbeChainFailure rotates the TCP probe hop that UniqueLandingChain
// actually used (not necessarily MatchingChainedPlatforms()[0]).
func (r *Router) NoteProbeChainFailure(landing node.Hash) {
	r.NoteProbeChainFailureForNetwork(landing, N.NetworkTCP)
}

// NoteProbeChainFailureForNetwork excludes the hop UniqueLandingChain would
// pick on this network, then rotates that hop platform's probe sticky.
func (r *Router) NoteProbeChainFailureForNetwork(landing node.Hash, network string) {
	if r == nil || landing.IsZero() {
		return
	}
	if network == "" {
		network = N.NetworkTCP
	}
	hops, hopPlatID, ok := r.uniqueLandingChain(landing, network)
	if !ok || hopPlatID == "" || len(hops) == 0 || hops[0].IsZero() {
		return
	}
	r.NoteChainDialFailure(ProbeAccount, RouteResult{
		Network: network,
		ChainHops: []RouteChainHop{{
			PlatformID: hopPlatID,
			NodeHash:   hops[0],
		}},
	})
}

func (r *Router) rememberFailedHop(account string, hash node.Hash) {
	if r == nil || hash.IsZero() {
		return
	}
	now := time.Now()
	r.hopFailMu.Lock()
	defer r.hopFailMu.Unlock()
	if r.hopFails == nil {
		r.hopFails = make(map[string]map[node.Hash]time.Time)
	}
	byHash := r.hopFails[account]
	if byHash == nil {
		byHash = make(map[node.Hash]time.Time)
		r.hopFails[account] = byHash
	}
	r.pruneFailedHopsLocked(byHash, now)
	byHash[hash] = now.Add(chainHopFailExcludeTTL)
	for len(byHash) > maxFailedHopsPerAccount {
		var oldest node.Hash
		var oldestUntil time.Time
		for h, until := range byHash {
			if oldest.IsZero() || until.Before(oldestUntil) {
				oldest = h
				oldestUntil = until
			}
		}
		delete(byHash, oldest)
	}
}

func (r *Router) failedHopHashes(account string) []node.Hash {
	if r == nil {
		return nil
	}
	now := time.Now()
	r.hopFailMu.Lock()
	defer r.hopFailMu.Unlock()
	byHash := r.hopFails[account]
	if len(byHash) == 0 {
		return nil
	}
	r.pruneFailedHopsLocked(byHash, now)
	if len(byHash) == 0 {
		delete(r.hopFails, account)
		return nil
	}
	out := make([]node.Hash, 0, len(byHash))
	for h := range byHash {
		out = append(out, h)
	}
	return out
}

func (r *Router) pruneFailedHopsLocked(byHash map[node.Hash]time.Time, now time.Time) {
	for h, until := range byHash {
		if now.After(until) {
			delete(byHash, h)
		}
	}
}
