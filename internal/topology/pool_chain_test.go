package topology

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/sagernet/sing-box/adapter"
)

func addHealthyChainNode(t *testing.T, pool *GlobalNodePool, subMgr *SubscriptionManager, subID, raw, ip string) node.Hash {
	t.Helper()
	return addHealthyChainNodeWithTags(t, pool, subMgr, subID, raw, ip, []string{subID})
}

func mustTagFilter(t *testing.T, rules ...string) node.TagFilter {
	t.Helper()
	filter, err := platform.CompileRegexFilters(rules)
	if err != nil {
		t.Fatalf("compile regex filters %v: %v", rules, err)
	}
	return filter
}

func addHealthyChainNodeWithTags(
	t *testing.T,
	pool *GlobalNodePool,
	subMgr *SubscriptionManager,
	subID, raw, ip string,
	tags []string,
) node.Hash {
	t.Helper()
	sub := subscription.NewSubscription(subID, subID, "url", true, false)
	subMgr.Register(sub)
	rawOpts := json.RawMessage(raw)
	hash := node.HashFromRawOptions(rawOpts)
	managed := subscription.NewManagedNodes()
	managed.StoreNode(hash, subscription.ManagedNode{Tags: append([]string(nil), tags...)})
	sub.SwapManagedNodes(managed)
	pool.AddNodeFromSub(hash, rawOpts, subID)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing after add", hash.Hex())
	}
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        100 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	entry.SetEgressIP(netip.MustParseAddr(ip))
	pool.RecordResult(hash, true)
	return hash
}

func TestUniqueLandingChain_DefaultCatchAllDoesNotCompete(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	def := platform.NewPlatform(platform.DefaultPlatformID, platform.DefaultPlatformName, nil, nil)
	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(def)
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	hopHash := addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop"}`, "198.51.100.1")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(def)
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != hopHash {
		t.Fatalf("unique chain = %v ok=%v, want [%s]", hops, ok, hopHash.Hex())
	}
	if _, ok := pool.UniqueLandingChain(hopHash); ok {
		t.Fatal("hop node must probe direct")
	}
}

func TestUniqueLandingChain_CompetingAllowlistStillChains(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	direct := platform.NewPlatform("direct", "Direct", nil, nil)
	direct.SubscriptionIDs = []string{"land-sub"}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)
	pool.RegisterPlatform(direct)

	hopHash := addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop"}`, "198.51.100.1")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)
	pool.RebuildPlatform(direct)

	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != hopHash {
		t.Fatalf("competing unchained allowlist should still chain, hops=%v ok=%v", hops, ok)
	}
}

func TestUniqueLandingChain_PrefersRegionPlatformOverCatchAll(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hopBroad := platform.NewPlatform("hop-broad", "HopBroad", nil, nil)
	hopBroad.SubscriptionIDs = []string{"hop-broad"}
	hopRole := platform.NewPlatform("hop-role", "HopRole", nil, nil)
	hopRole.SubscriptionIDs = []string{"hop-role"}
	hopRegion := platform.NewPlatform("hop-region", "HopRegion", nil, nil)
	hopRegion.SubscriptionIDs = []string{"hop-region"}

	landBroad := platform.NewPlatform("aaa-broad", "LandBroad", nil, nil)
	landBroad.SubscriptionIDs = []string{"land-sub"}
	landBroad.ChainPlatformIDs = []string{hopBroad.ID}
	landBroad.RegexFilters = mustTagFilter(t, "%")

	landRole := platform.NewPlatform("aab-role", "LandRole", nil, nil)
	landRole.SubscriptionIDs = []string{"land-sub"}
	landRole.ChainPlatformIDs = []string{hopRole.ID}
	landRole.RegexFilters = mustTagFilter(t, subscription.RoleTagLanding)

	landRegion := platform.NewPlatform("zzz-region", "LandRegion", nil, nil)
	landRegion.SubscriptionIDs = []string{"land-sub"}
	landRegion.ChainPlatformIDs = []string{hopRegion.ID}
	landRegion.RegexFilters = mustTagFilter(t, subscription.RegionFilterPattern("日本"))

	for _, plat := range []*platform.Platform{hopBroad, hopRole, hopRegion, landBroad, landRole, landRegion} {
		pool.RegisterPlatform(plat)
	}

	_ = addHealthyChainNode(t, pool, subMgr, "hop-broad", `{"role":"hop-broad"}`, "198.51.100.1")
	_ = addHealthyChainNode(t, pool, subMgr, "hop-role", `{"role":"hop-role"}`, "198.51.100.2")
	regionHop := addHealthyChainNode(t, pool, subMgr, "hop-region", `{"role":"hop-region"}`, "198.51.100.3")
	landHash := addHealthyChainNodeWithTags(
		t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.4",
		[]string{"日本-4% 198.51.100.4", subscription.RoleTagLanding, subscription.RegionTag("日本")},
	)
	for _, plat := range []*platform.Platform{hopBroad, hopRole, hopRegion, landBroad, landRole, landRegion} {
		pool.RebuildPlatform(plat)
	}

	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != regionHop {
		t.Fatalf("region chain should win over catch-all, hops=%v ok=%v want %s", hops, ok, regionHop.Hex())
	}
}

func TestUniqueLandingChain_TwoDifferentChainsPicksStable(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hopA := platform.NewPlatform("hop-a", "HopA", nil, nil)
	hopA.SubscriptionIDs = []string{"hop-a"}
	hopB := platform.NewPlatform("hop-b", "HopB", nil, nil)
	hopB.SubscriptionIDs = []string{"hop-b"}
	landA := platform.NewPlatform("land-a", "LandA", nil, nil)
	landA.SubscriptionIDs = []string{"land-sub"}
	landA.ChainPlatformIDs = []string{hopA.ID}
	landB := platform.NewPlatform("land-b", "LandB", nil, nil)
	landB.SubscriptionIDs = []string{"land-sub"}
	landB.ChainPlatformIDs = []string{hopB.ID}
	for _, plat := range []*platform.Platform{hopA, hopB, landA, landB} {
		pool.RegisterPlatform(plat)
	}

	hopAHash := addHealthyChainNode(t, pool, subMgr, "hop-a", `{"role":"a"}`, "198.51.100.1")
	_ = addHealthyChainNode(t, pool, subMgr, "hop-b", `{"role":"b"}`, "198.51.100.2")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.3")
	for _, plat := range []*platform.Platform{hopA, hopB, landA, landB} {
		pool.RebuildPlatform(plat)
	}

	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != hopAHash {
		t.Fatalf("two chains should pick land-a hop, hops=%v ok=%v", hops, ok)
	}
}

func TestUniqueLandingChain_PicksStableHopAndFallback(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"missing-sub"}
	fallback := platform.NewPlatform("hop-fb", "HopFB", nil, nil)
	fallback.SubscriptionIDs = []string{"hop-sub"}
	hop.FallbackPlatformIDs = []string{fallback.ID}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(fallback)
	pool.RegisterPlatform(landing)

	hopHash := addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop"}`, "198.51.100.1")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(fallback)
	pool.RebuildPlatform(landing)

	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != hopHash {
		t.Fatalf("fallback hop = %v ok=%v, want [%s]", hops, ok, hopHash.Hex())
	}
}

func TestUniqueLandingChain_EmptyHopViewIsNotUnique(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	if hops, ok := pool.UniqueLandingChain(landHash); ok {
		t.Fatalf("empty hop view should not chain-probe, hops=%v", hops)
	}
	if !pool.HasUniqueLandingChain(landHash) {
		t.Fatal("empty hop view still belongs to a unique chain")
	}
	if hops, ok := pool.UniqueLandingChainForNetwork(landHash, "udp"); ok {
		t.Fatalf("empty hop UDP chain = %v", hops)
	}
}

func TestUniqueLandingChain_ExcludesLandingFromHopPick(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	vpsHash := addHealthyChainNode(t, pool, subMgr, "vps-sub", `{"role":"vps"}`, "198.51.100.1")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != vpsHash {
		t.Fatalf("chain hops = %v ok=%v, want VPS %s", hops, ok, vpsHash.Hex())
	}
}

func TestUniqueLandingChain_SkipsWhenOnlyHopIsLanding(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land-only"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	if hops, ok := pool.UniqueLandingChain(landHash); ok {
		t.Fatalf("self hop should be skipped, hops=%v", hops)
	}
	if !pool.HasUniqueLandingChain(landHash) {
		t.Fatal("self-only hop still requires a chain probe")
	}
}

type networkLimitedOutbound struct {
	*testutil.NoopOutbound
	networks []string
}

func (o *networkLimitedOutbound) Network() []string {
	return append([]string(nil), o.networks...)
}

func TestUniqueLandingChain_PrefersUDPCapableHop(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	tcpHash := addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop-tcp"}`, "198.51.100.1")
	udpHash := addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop-udp"}`, "198.51.100.3")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")

	tcpEntry, _ := pool.GetEntry(tcpHash)
	var tcpOut adapter.Outbound = &networkLimitedOutbound{
		NoopOutbound: &testutil.NoopOutbound{},
		networks:     []string{"tcp"},
	}
	tcpEntry.Outbound.Store(&tcpOut)

	udpEntry, _ := pool.GetEntry(udpHash)
	udpEntry.LastUDPProbeSuccess.Store(time.Now().UnixNano())

	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != udpHash {
		t.Fatalf("unique chain = %v ok=%v, want UDP hop %s", hops, ok, udpHash.Hex())
	}
	udpHops, ok := pool.UniqueLandingChainForNetwork(landHash, "udp")
	if !ok || len(udpHops) != 1 || udpHops[0] != udpHash {
		t.Fatalf("udp chain = %v ok=%v, want UDP hop %s", udpHops, ok, udpHash.Hex())
	}
}

func TestUniqueLandingChain_PrefersNonVisionHop(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	visionHash := addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop-vision","flow":"xtls-rprx-vision"}`, "198.51.100.1")
	plainHash := addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop-ws","transport":{"type":"ws"}}`, "198.51.100.3")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	hopSub, ok := subMgr.Get("hop-sub")
	if !ok {
		t.Fatal("hop-sub missing")
	}
	managed := subscription.NewManagedNodes()
	managed.StoreNode(visionHash, subscription.ManagedNode{Tags: []string{"hop-sub"}})
	managed.StoreNode(plainHash, subscription.ManagedNode{Tags: []string{"hop-sub"}})
	hopSub.SwapManagedNodes(managed)

	plainEntry, _ := pool.GetEntry(plainHash)
	var tcpOut adapter.Outbound = &networkLimitedOutbound{
		NoopOutbound: &testutil.NoopOutbound{},
		networks:     []string{"tcp"},
	}
	plainEntry.Outbound.Store(&tcpOut)
	pool.RecordUDPProbeResult(visionHash, true)

	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != plainHash {
		t.Fatalf("http chain = %v ok=%v, want non-vision hop %s (vision=%s)", hops, ok, plainHash.Hex(), visionHash.Hex())
	}
	udpHops, ok := pool.UniqueLandingChainForNetwork(landHash, "udp")
	if !ok || len(udpHops) != 1 || udpHops[0] != visionHash {
		t.Fatalf("udp chain = %v ok=%v, want vision UDP hop %s", udpHops, ok, visionHash.Hex())
	}
}

func TestUniqueLandingChainForNetwork_UDPRequiresUDPHop(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	tcpHash := addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop-tcp-only"}`, "198.51.100.1")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	tcpEntry, _ := pool.GetEntry(tcpHash)
	var tcpOut adapter.Outbound = &networkLimitedOutbound{
		NoopOutbound: &testutil.NoopOutbound{},
		networks:     []string{"tcp"},
	}
	tcpEntry.Outbound.Store(&tcpOut)
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	if !pool.HasUniqueLandingChain(landHash) {
		t.Fatal("tcp-only hop platform is still a unique chain")
	}
	hops, ok := pool.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != tcpHash {
		t.Fatalf("http chain = %v ok=%v, want TCP hop %s", hops, ok, tcpHash.Hex())
	}
	if udpHops, ok := pool.UniqueLandingChainForNetwork(landHash, "udp"); ok {
		t.Fatalf("udp chain should be unavailable, hops=%v", udpHops)
	}
}

func TestPool_DisableInvalidNodeCheck_ChainedLandingAllowsUnhealthy(t *testing.T) {
	subMgr := NewSubscriptionManager()
	pool := newTestPool(subMgr)

	def := platform.NewPlatform(platform.DefaultPlatformID, platform.DefaultPlatformName, nil, nil)
	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(def)
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	_ = addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop"}`, "198.51.100.1")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	land, ok := pool.GetEntry(landHash)
	if !ok {
		t.Fatal("landing node missing")
	}
	land.RestoreTCPHealth(3, time.Now().UnixNano())
	land.SetEgressIP(netip.Addr{})

	pool.RebuildAllPlatforms()
	if landing.View().Contains(landHash) {
		t.Fatal("unhealthy landing must stay out while the flag is off")
	}

	sub := subMgr.Lookup("land-sub")
	if sub == nil {
		t.Fatal("land-sub missing")
	}
	sub.SetDisableInvalidNodeCheck(true)
	pool.RebuildAllPlatforms()
	if !landing.View().Contains(landHash) {
		t.Fatal("chained landing should keep the node when disable_invalid_node_check is on")
	}
	if def.View().Contains(landHash) {
		t.Fatal("unchained Default must still require health")
	}
}
