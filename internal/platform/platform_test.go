package platform

import (
	"net/netip"
	"regexp"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/sagernet/sing-box/adapter"
)

type networkLimitedOutbound struct {
	*testutil.NoopOutbound
	networks []string
}

func (o *networkLimitedOutbound) Network() []string {
	return append([]string(nil), o.networks...)
}

func setEntryNetworks(entry *node.NodeEntry, networks ...string) {
	var outbound adapter.Outbound = &networkLimitedOutbound{
		NoopOutbound: &testutil.NoopOutbound{},
		networks:     networks,
	}
	entry.Outbound.Store(&outbound)
	for _, network := range networks {
		if network == "udp" {
			entry.LastUDPProbeSuccess.Store(time.Now().UnixNano())
		}
	}
}

// makeFullyRoutableEntry creates a NodeEntry that passes all filter conditions.
func makeFullyRoutableEntry(hash node.Hash, subIDs ...string) *node.NodeEntry {
	e := node.NewNodeEntry(hash, nil, time.Now(), 16)
	for _, id := range subIDs {
		e.AddSubscriptionID(id)
	}
	// Set all conditions to pass.
	e.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)
	e.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
	return e
}

func alwaysLookup(subID string, hash node.Hash) (string, bool, []string, bool) {
	return "TestSub", true, []string{"us-node", "fast"}, true
}

func usGeoLookup(addr netip.Addr) string { return "us" }

func TestPlatform_EvaluateNode_AllPass(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, nil) // no filters
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 1 {
		t.Fatalf("expected 1 routable node, got %d", p.View().Size())
	}
}

func TestPlatform_MaintainsTCPAndUDPNetworkViews(t *testing.T) {
	p := NewPlatform("p-network", "Network", nil, nil)
	tcpHash := makeHash(`{"network":"tcp"}`)
	dualHash := makeHash(`{"network":"dual"}`)
	udpHash := makeHash(`{"network":"udp"}`)
	entries := map[node.Hash]*node.NodeEntry{
		tcpHash:  makeFullyRoutableEntry(tcpHash, "sub1"),
		dualHash: makeFullyRoutableEntry(dualHash, "sub1"),
		udpHash:  makeFullyRoutableEntry(udpHash, "sub1"),
	}
	setEntryNetworks(entries[tcpHash], "tcp")
	setEntryNetworks(entries[dualHash], "tcp", "udp")
	setEntryNetworks(entries[udpHash], "udp")

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		for hash, entry := range entries {
			fn(hash, entry)
		}
	}, alwaysLookup, usGeoLookup)

	if !p.View().Contains(tcpHash) || !p.View().Contains(dualHash) || p.View().Contains(udpHash) {
		t.Fatalf("unexpected TCP view contents: size=%d", p.View().Size())
	}
	udpView := p.NetworkView("udp")
	if udpView.Size() != 1 || !udpView.Contains(dualHash) {
		t.Fatalf("unexpected UDP view contents: size=%d", udpView.Size())
	}
}

func TestPlatform_UDPViewRequiresVerifiedHealthyUDP(t *testing.T) {
	p := NewPlatform("p-udp-health", "UDP Health", nil, nil)
	h := makeHash(`{"network":"dual-unknown"}`)
	entry := makeFullyRoutableEntry(h, "sub1")
	setEntryNetworks(entry, "tcp", "udp")
	entry.LastUDPProbeSuccess.Store(0)

	rebuild := func() {
		p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
			fn(h, entry)
		}, alwaysLookup, usGeoLookup)
	}
	rebuild()
	if !p.View().Contains(h) || p.NetworkView("udp").Contains(h) {
		t.Fatal("unknown UDP health should affect only the UDP view")
	}

	entry.LastUDPProbeSuccess.Store(time.Now().UnixNano())
	rebuild()
	if !p.NetworkView("udp").Contains(h) {
		t.Fatal("first successful UDP probe should add the node to the UDP view")
	}

	entry.RestoreUDPHealth(0, time.Now().UnixNano())
	rebuild()
	if !p.View().Contains(h) || p.NetworkView("udp").Contains(h) {
		t.Fatal("UDP circuit should remove only the UDP view membership")
	}
}

func TestPlatform_EvaluateNode_CircuitOpen(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, nil)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")
	entry.RestoreTCPHealth(0, time.Now().UnixNano()) // circuit open

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 0 {
		t.Fatal("circuit-broken node should not be routable")
	}
}

func TestPlatform_EvaluateNode_NoLatency(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, nil)
	h := makeHash(`{"type":"ss"}`)
	// Create entry without latency table (maxLatencyTableEntries=0).
	entry := node.NewNodeEntry(h, nil, time.Now(), 0)
	entry.AddSubscriptionID("sub1")
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 0 {
		t.Fatal("node without latency should not be routable")
	}
}

func TestPlatform_EvaluateNode_NoOutbound(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, nil)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")
	entry.Outbound.Store(nil) // no outbound

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 0 {
		t.Fatal("node without outbound should not be routable")
	}
}

func TestPlatform_EvaluateNode_NoEgressIP(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, nil) // no region filters
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")
	entry.SetEgressIP(netip.Addr{}) // egress unknown

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 0 {
		t.Fatal("node without egress IP should not be routable")
	}
}

func TestPlatform_EvaluateNode_RegexFilter(t *testing.T) {
	regexes := []*regexp.Regexp{regexp.MustCompile("us")}
	p := NewPlatform("p1", "Test", regexes, nil)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	// Lookup returns "TestSub/us-node" which matches "us".
	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 1 {
		t.Fatal("node matching regex should be routable")
	}

	// Now with a "jp" filter — should NOT match.
	p2 := NewPlatform("p2", "Test", []*regexp.Regexp{regexp.MustCompile("^jp")}, nil)
	p2.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p2.View().Size() != 0 {
		t.Fatal("node not matching regex should not be routable")
	}
}

func TestPlatform_EvaluateNode_RegionFilter(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, []string{"us"})
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 1 {
		t.Fatal("node in allowed region should be routable")
	}

	// Region filter "jp" — node has US egress, should fail.
	p2 := NewPlatform("p2", "Test", nil, []string{"jp"})
	p2.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p2.View().Size() != 0 {
		t.Fatal("node not in allowed region should not be routable")
	}
}

func TestPlatform_EvaluateNode_RegionFilter_NoEgressIP(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, []string{"us"})
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")
	// Don't set egress IP — clear it.
	entry.SetEgressIP(netip.Addr{})

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 0 {
		t.Fatal("node without egress IP should not be routable")
	}
}

func TestPlatform_EvaluateNode_RegionFilter_PrefersStoredRegion(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, []string{"jp"})
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")
	entry.SetEgressRegion("jp")

	geoCalled := false
	geoLookup := func(netip.Addr) string {
		geoCalled = true
		return "us"
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, geoLookup)

	if p.View().Size() != 1 {
		t.Fatal("stored region should be used before GeoIP fallback")
	}
	if geoCalled {
		t.Fatal("GeoIP lookup should be skipped when stored region exists")
	}
}

func TestPlatform_EvaluateNode_RegionFilter_ExcludeOnlyUnknownRegion(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, []string{"!hk"})
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	geoLookup := func(netip.Addr) string { return "" }
	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h, entry)
	}, alwaysLookup, geoLookup)

	if p.View().Size() != 0 {
		t.Fatal("node with unknown region should not be routable when region filters are configured")
	}
}

func TestMatchRegionFilter(t *testing.T) {
	tests := []struct {
		name    string
		filters []string
		region  string
		want    bool
	}{
		{
			name:    "include only match",
			filters: []string{"hk", "us"},
			region:  "hk",
			want:    true,
		},
		{
			name:    "include only miss",
			filters: []string{"hk", "us"},
			region:  "jp",
			want:    false,
		},
		{
			name:    "exclude only",
			filters: []string{"!hk"},
			region:  "us",
			want:    true,
		},
		{
			name:    "exclude only blocked",
			filters: []string{"!hk"},
			region:  "hk",
			want:    false,
		},
		{
			name:    "exclude only unknown region",
			filters: []string{"!hk"},
			region:  "",
			want:    false,
		},
		{
			name:    "mixed include and exclude allows expected",
			filters: []string{"hk", "!us"},
			region:  "hk",
			want:    true,
		},
		{
			name:    "mixed include and exclude blocks excluded",
			filters: []string{"hk", "!us"},
			region:  "us",
			want:    false,
		},
		{
			name:    "mixed include and same exclude blocks",
			filters: []string{"hk", "!hk"},
			region:  "hk",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchRegionFilter(tt.region, tt.filters); got != tt.want {
				t.Fatalf("MatchRegionFilter(%q, %v) = %v, want %v", tt.region, tt.filters, got, tt.want)
			}
		})
	}
}

func TestPlatform_NotifyDirty_AddRemove(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, nil)
	h := makeHash(`{"type":"ss"}`)
	entry := makeFullyRoutableEntry(h, "sub1")

	entryStore := map[node.Hash]*node.NodeEntry{h: entry}
	getEntry := func(hash node.Hash) (*node.NodeEntry, bool) {
		e, ok := entryStore[hash]
		return e, ok
	}

	// Initially empty — add via NotifyDirty.
	p.NotifyDirty(h, getEntry, alwaysLookup, usGeoLookup)
	if p.View().Size() != 1 {
		t.Fatal("NotifyDirty should add passing node")
	}

	// Circuit-break → NotifyDirty removes.
	entry.RestoreTCPHealth(0, time.Now().UnixNano())
	p.NotifyDirty(h, getEntry, alwaysLookup, usGeoLookup)
	if p.View().Size() != 0 {
		t.Fatal("NotifyDirty should remove circuit-broken node")
	}

	// Recover → NotifyDirty re-adds.
	entry.RestoreTCPHealth(0, 0)
	p.NotifyDirty(h, getEntry, alwaysLookup, usGeoLookup)
	if p.View().Size() != 1 {
		t.Fatal("NotifyDirty should re-add recovered node")
	}

	// Delete from pool → NotifyDirty removes.
	delete(entryStore, h)
	p.NotifyDirty(h, getEntry, alwaysLookup, usGeoLookup)
	if p.View().Size() != 0 {
		t.Fatal("NotifyDirty should remove deleted node")
	}
}

func TestPlatform_EvaluateNode_SubscriptionAllowlist(t *testing.T) {
	p := NewPlatform("p-sub", "SubOnly", nil, nil)
	p.SubscriptionIDs = []string{"sub-exclusive"}

	keepHash := makeHash(`{"type":"ss","n":"keep"}`)
	dropHash := makeHash(`{"type":"ss","n":"drop"}`)
	keep := makeFullyRoutableEntry(keepHash, "sub-exclusive")
	drop := makeFullyRoutableEntry(dropHash, "sub-other")

	lookup := func(subID string, hash node.Hash) (string, bool, []string, bool) {
		return subID, true, []string{"node"}, true
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(keepHash, keep)
		fn(dropHash, drop)
	}, lookup, usGeoLookup)

	if p.View().Size() != 1 {
		t.Fatalf("allowlist view size = %d, want 1", p.View().Size())
	}
	if !p.View().Contains(keepHash) {
		t.Fatal("exclusive subscription node should remain routable")
	}
	if p.View().Contains(dropHash) {
		t.Fatal("node from another subscription should be excluded")
	}
}

func TestPlatform_EvaluateNode_SubscriptionExclude(t *testing.T) {
	p := NewPlatform("p-ex", "CatchAllMinusHome", nil, nil)
	p.SubscriptionExcludeIDs = []string{"home-sub"}

	keepHash := makeHash(`{"type":"ss","n":"vps"}`)
	dropHash := makeHash(`{"type":"ss","n":"home"}`)
	keep := makeFullyRoutableEntry(keepHash, "vps-sub")
	drop := makeFullyRoutableEntry(dropHash, "home-sub")

	lookup := func(subID string, hash node.Hash) (string, bool, []string, bool) {
		return subID, true, []string{"node"}, true
	}

	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(keepHash, keep)
		fn(dropHash, drop)
	}, lookup, usGeoLookup)

	if !p.View().Contains(keepHash) {
		t.Fatal("non-excluded subscription node should remain routable")
	}
	if p.View().Contains(dropHash) {
		t.Fatal("excluded subscription node should leave the view")
	}
}

func flagLookupFor(ids ...string) node.DisableInvalidNodeCheckFunc {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return func(subID string) bool {
		_, ok := set[subID]
		return ok
	}
}

func TestPlatform_EvaluateNode_DisableInvalidNodeCheck(t *testing.T) {
	h := makeHash(`{"type":"ss","role":"home"}`)
	unhealthy := func() *node.NodeEntry {
		entry := makeFullyRoutableEntry(h, "sub1")
		entry.RestoreTCPHealth(3, time.Now().UnixNano())
		entry.SetEgressIP(netip.Addr{})
		return entry
	}
	rebuild := func(p *Platform, entry *node.NodeEntry, flags node.DisableInvalidNodeCheckFunc) {
		p.FullRebuildWithFlags(func(fn func(node.Hash, *node.NodeEntry) bool) {
			fn(h, entry)
		}, alwaysLookup, usGeoLookup, flags)
	}

	t.Run("chained with flag keeps circuit-open node", func(t *testing.T) {
		p := NewPlatform("landing", "Landing", nil, nil)
		p.ChainPlatformIDs = []string{"hop"}
		rebuild(p, unhealthy(), flagLookupFor("sub1"))
		if p.View().Size() != 1 {
			t.Fatal("chained landing with flag should keep the node")
		}
	})

	t.Run("unchained still requires health", func(t *testing.T) {
		p := NewPlatform("direct", "Direct", nil, nil)
		rebuild(p, unhealthy(), flagLookupFor("sub1"))
		if p.View().Size() != 0 {
			t.Fatal("unchained platform must still require health")
		}
	})

	t.Run("chained without flag still requires health", func(t *testing.T) {
		p := NewPlatform("landing", "Landing", nil, nil)
		p.ChainPlatformIDs = []string{"hop"}
		rebuild(p, unhealthy(), flagLookupFor())
		if p.View().Size() != 0 {
			t.Fatal("chained landing without flag must still require health")
		}
	})

	t.Run("still requires outbound", func(t *testing.T) {
		p := NewPlatform("landing", "Landing", nil, nil)
		p.ChainPlatformIDs = []string{"hop"}
		entry := unhealthy()
		entry.Outbound.Store(nil)
		rebuild(p, entry, flagLookupFor("sub1"))
		if p.View().Size() != 0 {
			t.Fatal("skip-health still requires outbound")
		}
	})

	t.Run("unknown egress skips region", func(t *testing.T) {
		p := NewPlatform("landing", "Landing", nil, []string{"us"})
		p.ChainPlatformIDs = []string{"hop"}
		rebuild(p, unhealthy(), flagLookupFor("sub1"))
		if p.View().Size() != 1 {
			t.Fatal("unknown egress should skip region while skip-health")
		}
	})

	t.Run("known egress still applies region", func(t *testing.T) {
		p := NewPlatform("landing", "Landing", nil, []string{"us"})
		p.ChainPlatformIDs = []string{"hop"}
		entry := unhealthy()
		entry.SetEgressIP(netip.MustParseAddr("8.8.8.8"))
		jpLookup := func(netip.Addr) string { return "jp" }
		p.FullRebuildWithFlags(func(fn func(node.Hash, *node.NodeEntry) bool) {
			fn(h, entry)
		}, alwaysLookup, jpLookup, flagLookupFor("sub1"))
		if p.View().Size() != 0 {
			t.Fatal("known jp egress should fail us region filter")
		}
	})
}

func TestPlatform_FullRebuild_ClearsOld(t *testing.T) {
	p := NewPlatform("p1", "Test", nil, nil)
	h1 := makeHash(`{"type":"ss","n":1}`)
	h2 := makeHash(`{"type":"ss","n":2}`)
	e1 := makeFullyRoutableEntry(h1, "sub1")
	e2 := makeFullyRoutableEntry(h2, "sub1")

	// First rebuild with 2 nodes.
	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h1, e1)
		fn(h2, e2)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 2 {
		t.Fatalf("expected 2, got %d", p.View().Size())
	}

	// Second rebuild with only 1 node — old entries cleared.
	p.FullRebuild(func(fn func(node.Hash, *node.NodeEntry) bool) {
		fn(h1, e1)
	}, alwaysLookup, usGeoLookup)

	if p.View().Size() != 1 {
		t.Fatalf("expected 1 after rebuild, got %d", p.View().Size())
	}
	if p.View().Contains(h2) {
		t.Fatal("h2 should have been removed by rebuild")
	}
}

func TestPlatform_MatchesLandingFilters_IgnoresLiveHealth(t *testing.T) {
	p := NewPlatform("landing", "Landing", nil, nil)
	p.SubscriptionIDs = []string{"land-sub"}
	h := makeHash(`{"type":"ss","role":"landing"}`)
	entry := makeFullyRoutableEntry(h, "land-sub")
	entry.RestoreTCPHealth(3, time.Now().UnixNano())
	entry.SetEgressIP(netip.Addr{})

	if p.evaluateNode(entry, alwaysLookup, usGeoLookup, nil) {
		t.Fatal("circuit-open landing without egress must stay out of the routable view")
	}
	if !p.MatchesLandingFilters(entry, alwaysLookup, usGeoLookup) {
		t.Fatal("unique-chain membership should ignore live health and missing egress")
	}

	p.RegionFilters = []string{"us"}
	entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
	if !p.MatchesLandingFilters(entry, alwaysLookup, usGeoLookup) {
		t.Fatal("known us egress should match us region filter")
	}
	entry.SetEgressIP(netip.MustParseAddr("8.8.8.8"))
	jpLookup := func(netip.Addr) string { return "jp" }
	if p.MatchesLandingFilters(entry, alwaysLookup, jpLookup) {
		t.Fatal("jp egress should not match us region filter")
	}
}

func TestPlatform_HasRestrictiveLandingFilters(t *testing.T) {
	catchAll := NewPlatform("default", "Default", nil, nil)
	if catchAll.HasRestrictiveLandingFilters() {
		t.Fatal("empty Default-style platform should not compete with a unique chain")
	}
	allowlist := NewPlatform("land", "Land", nil, nil)
	allowlist.SubscriptionIDs = []string{"land-sub"}
	if !allowlist.HasRestrictiveLandingFilters() {
		t.Fatal("subscription allowlist is a restrictive landing filter")
	}
	denylist := NewPlatform("ex", "Ex", nil, nil)
	denylist.SubscriptionExcludeIDs = []string{"home-sub"}
	if !denylist.HasRestrictiveLandingFilters() {
		t.Fatal("subscription exclude list is a restrictive landing filter")
	}
}
