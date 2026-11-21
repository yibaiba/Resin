package routing

import (
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	N "github.com/sagernet/sing/common/network"
)

type chainedLandingFinder interface {
	HasUniqueLandingChain(hash node.Hash) bool
	MatchingChainedPlatforms(hash node.Hash) []*platform.Platform
}

// HasUniqueLandingChain reports that the landing matches at least one non-empty
// platform chain. Used by OutboundManager as landingChainResolver.
func (r *Router) HasUniqueLandingChain(hash node.Hash) bool {
	finder, ok := r.pool.(chainedLandingFinder)
	return ok && finder.HasUniqueLandingChain(hash)
}

// UniqueLandingChain picks TCP hops for an HTTP/TCP chain probe using the same
// hop algorithm as user traffic (P2C/sticky, Vision filter, failed-hop exclude)
// under ProbeAccount.
func (r *Router) UniqueLandingChain(hash node.Hash) ([]node.Hash, bool) {
	return r.UniqueLandingChainForNetwork(hash, N.NetworkTCP)
}

// UniqueLandingChainForNetwork picks hops from the requested network view.
func (r *Router) UniqueLandingChainForNetwork(hash node.Hash, network string) ([]node.Hash, bool) {
	hops, _, ok := r.uniqueLandingChain(hash, network)
	return hops, ok
}

func (r *Router) uniqueLandingChain(hash node.Hash, network string) ([]node.Hash, string, bool) {
	if r == nil {
		return nil, "", false
	}
	finder, ok := r.pool.(chainedLandingFinder)
	if !ok || !finder.HasUniqueLandingChain(hash) {
		return nil, "", false
	}
	if network == "" {
		network = N.NetworkTCP
	}
	for _, plat := range finder.MatchingChainedPlatforms(hash) {
		hops, hopPlatID, ok := r.pickProbeChainHops(plat.ChainPlatformIDs, network, hash)
		if ok {
			return hops, hopPlatID, true
		}
	}
	return nil, "", false
}

func (r *Router) pickProbeChainHops(chainIDs []string, network string, landing node.Hash) ([]node.Hash, string, bool) {
	if len(chainIDs) == 0 {
		return nil, "", false
	}
	hops := make([]node.Hash, 0, len(chainIDs))
	firstHopPlat := ""
	for _, hopID := range chainIDs {
		hopPlat, ok := r.pool.GetPlatform(hopID)
		if !ok || hopPlat == nil {
			return nil, "", false
		}
		result, err := r.RouteWithOptions(RouteOptions{
			PlatformName:    hopPlat.Name,
			Account:         ProbeAccount,
			RequiredNetwork: network,
			SkipProxyChain:  true,
			ExcludeNodeHash: landing,
		})
		if err != nil || result.NodeHash.IsZero() {
			return nil, "", false
		}
		if firstHopPlat == "" {
			firstHopPlat = result.SelectedPlatformID
			if firstHopPlat == "" {
				firstHopPlat = hopPlat.ID
			}
		}
		hops = append(hops, result.NodeHash)
	}
	return hops, firstHopPlat, true
}
