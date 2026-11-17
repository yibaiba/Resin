package platform

import (
	"net/netip"
	"regexp"
	"sync"

	"github.com/Resinat/Resin/internal/node"
	N "github.com/sagernet/sing/common/network"
)

// DefaultPlatformID is the well-known UUID of the built-in Default platform.
const DefaultPlatformID = "00000000-0000-0000-0000-000000000000"

// DefaultPlatformName is the built-in platform name.
const DefaultPlatformName = "Default"

// MaxChainPlatformIDs is the safety cap on a platform's ordered hop list.
const MaxChainPlatformIDs = 8

// GeoLookupFunc resolves an IP address to a lowercase ISO country code.
type GeoLookupFunc func(netip.Addr) string

// PoolRangeFunc iterates all nodes in the global pool.
type PoolRangeFunc func(fn func(node.Hash, *node.NodeEntry) bool)

// GetEntryFunc retrieves a node entry from the global pool by hash.
type GetEntryFunc func(node.Hash) (*node.NodeEntry, bool)

// Platform represents a routing platform with its filtered routable view.
type Platform struct {
	ID   string
	Name string

	// Filter configuration.
	RegexFilters    node.TagFilter
	RegionFilters   []string // lowercase ISO codes, supports negation "!xx"
	SubscriptionIDs []string // empty means all subscriptions; otherwise allowlist

	// Other config fields.
	StickyTTLNs                      int64
	ReverseProxyMissAction           string
	ReverseProxyEmptyAccountBehavior string
	ReverseProxyFixedAccountHeader   string
	ReverseProxyFixedAccountHeaders  []string
	AllocationPolicy                 AllocationPolicy
	PassiveCircuitBreakerDisabled    bool
	FallbackPlatformIDs              []string
	ChainPlatformIDs                 []string
	RotationIntervalNs               int64
	RotationStrategy                 RotationStrategy

	// Routable view & its lock.
	// viewMu serializes both FullRebuild and NotifyDirty.
	tcpView *RoutableView
	udpView *RoutableView
	viewMu  sync.Mutex
}

// NewPlatform creates a Platform with an empty routable view.
// The regex slice is treated as MUST rules for compatibility with internal callers.
func NewPlatform(id, name string, regexFilters []*regexp.Regexp, regionFilters []string) *Platform {
	return NewPlatformWithTagFilter(id, name, node.TagFilter{Must: regexFilters}, regionFilters)
}

// NewPlatformWithTagFilter creates a Platform with compiled line-oriented tag rules.
func NewPlatformWithTagFilter(id, name string, regexFilters node.TagFilter, regionFilters []string) *Platform {
	return &Platform{
		ID:               id,
		Name:             name,
		RegexFilters:     regexFilters,
		RegionFilters:    regionFilters,
		RotationStrategy: RotationStrategyAny,
		tcpView:          NewRoutableView(),
		udpView:          NewRoutableView(),
	}
}

// View returns the platform's routable view as a read-only interface.
// External callers cannot Add/Remove/Clear — only FullRebuild and NotifyDirty can mutate.
func (p *Platform) View() ReadOnlyView {
	return p.tcpView
}

// NetworkView returns the routable view for a required network. UDP-only
// nodes are intentionally excluded: the UDP view requires both TCP health
// probe capability and UDP transport capability.
func (p *Platform) NetworkView(required string) ReadOnlyView {
	if N.NetworkName(required) == N.NetworkUDP {
		return p.udpView
	}
	return p.tcpView
}

// FullRebuild clears the routable view and re-evaluates all nodes from the pool.
// Acquires viewMu — any concurrent NotifyDirty calls block until rebuild completes.
func (p *Platform) FullRebuild(
	poolRange PoolRangeFunc,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
) {
	p.viewMu.Lock()
	defer p.viewMu.Unlock()

	p.tcpView.Clear()
	p.udpView.Clear()
	poolRange(func(h node.Hash, entry *node.NodeEntry) bool {
		if p.evaluateNode(entry, subLookup, geoLookup) {
			p.updateNetworkViews(h, entry)
		}
		return true
	})
}

// NotifyDirty re-evaluates a single node and adds/removes it from the view.
// Acquires viewMu — serialized with FullRebuild.
func (p *Platform) NotifyDirty(
	h node.Hash,
	getEntry GetEntryFunc,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
) {
	p.viewMu.Lock()
	defer p.viewMu.Unlock()

	entry, ok := getEntry(h)
	if !ok {
		// Node was deleted from pool.
		p.removeFromNetworkViews(h)
		return
	}

	if p.evaluateNode(entry, subLookup, geoLookup) {
		p.updateNetworkViews(h, entry)
	} else {
		p.removeFromNetworkViews(h)
	}
}

func (p *Platform) updateNetworkViews(h node.Hash, entry *node.NodeEntry) {
	if !entry.SupportsNetwork(N.NetworkTCP) {
		p.removeFromNetworkViews(h)
		return
	}
	p.tcpView.Add(h)
	if entry.IsUDPRouteHealthy() {
		p.udpView.Add(h)
		return
	}
	p.udpView.Remove(h)
}

func (p *Platform) removeFromNetworkViews(h node.Hash) {
	p.tcpView.Remove(h)
	p.udpView.Remove(h)
}

// evaluateNode checks all filter conditions for platform routability.
func (p *Platform) evaluateNode(
	entry *node.NodeEntry,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
) bool {
	// 0. Disabled nodes are never routable.
	if entry.IsDisabledBySubscriptions(subLookup) {
		return false
	}

	// 1. Healthy for routing (outbound ready + circuit not open).
	if !entry.IsHealthy() {
		return false
	}

	if !entry.MatchesSubscriptionAllowlist(p.SubscriptionIDs, subLookup) {
		return false
	}

	// 3. Tag regex match.
	if !entry.MatchTagFilter(p.RegexFilters, subLookup) {
		return false
	}

	// 4. Egress IP must be known.
	egressIP := entry.GetEgressIP()
	if !egressIP.IsValid() {
		return false
	}

	// 5. Region filter (when configured).
	if len(p.RegionFilters) > 0 {
		region := entry.GetRegion(geoLookup)
		if !MatchRegionFilter(region, p.RegionFilters) {
			return false
		}
	}

	// 6. Has at least one latency record.
	if !entry.HasLatency() {
		return false
	}

	return true
}

// MatchesLandingFilters reports whether the node could belong to this
// platform as a landing node, ignoring live health, egress IP, and latency.
// Region filters apply only after egress is known so a chained landing can
// still be probed through hops.
func (p *Platform) MatchesLandingFilters(
	entry *node.NodeEntry,
	subLookup node.SubLookupFunc,
	geoLookup GeoLookupFunc,
) bool {
	if entry == nil {
		return false
	}
	if entry.IsDisabledBySubscriptions(subLookup) {
		return false
	}
	if !entry.MatchesSubscriptionAllowlist(p.SubscriptionIDs, subLookup) {
		return false
	}
	if !entry.MatchTagFilter(p.RegexFilters, subLookup) {
		return false
	}
	if len(p.RegionFilters) == 0 {
		return true
	}
	egressIP := entry.GetEgressIP()
	if !egressIP.IsValid() {
		return true
	}
	return MatchRegionFilter(entry.GetRegion(geoLookup), p.RegionFilters)
}

// HasRestrictiveLandingFilters reports whether this platform uses an explicit
// membership filter. Catch-all platforms such as Default do not compete with
// a unique landing chain.
func (p *Platform) HasRestrictiveLandingFilters() bool {
	if p == nil {
		return false
	}
	return len(p.SubscriptionIDs) > 0 || len(p.RegionFilters) > 0 || !p.RegexFilters.Empty()
}

// MatchRegionFilter applies include/exclude region filters.
// Positive entries (xx) build an include set; negative entries (!xx) build an exclude set.
// Unknown regions never match when region filters are configured.
// Final result is: region known AND (include empty OR region in include) AND (region not in exclude).
func MatchRegionFilter(region string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	if region == "" {
		return false
	}

	included := false
	hasInclude := false

	for _, filter := range filters {
		if len(filter) > 0 && filter[0] == '!' {
			if region == filter[1:] {
				return false
			}
			continue
		}
		hasInclude = true
		if region == filter {
			included = true
		}
	}

	if hasInclude && !included {
		return false
	}
	return true
}
