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
	sub := subscription.NewSubscription(subID, subID, "url", true, false)
	subMgr.Register(sub)
	rawOpts := json.RawMessage(raw)
	hash := node.HashFromRawOptions(rawOpts)
	managed := subscription.NewManagedNodes()
	managed.StoreNode(hash, subscription.ManagedNode{Tags: []string{subID}})
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

func TestUniqueLandingChain_CompetingAllowlistProbesDirect(t *testing.T) {
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

	_ = addHealthyChainNode(t, pool, subMgr, "hop-sub", `{"role":"hop"}`, "198.51.100.1")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)
	pool.RebuildPlatform(direct)

	if hops, ok := pool.UniqueLandingChain(landHash); ok {
		t.Fatalf("competing unchained allowlist should probe direct, hops=%v", hops)
	}
}

func TestUniqueLandingChain_TwoDifferentChainsProbeDirect(t *testing.T) {
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

	_ = addHealthyChainNode(t, pool, subMgr, "hop-a", `{"role":"a"}`, "198.51.100.1")
	_ = addHealthyChainNode(t, pool, subMgr, "hop-b", `{"role":"b"}`, "198.51.100.2")
	landHash := addHealthyChainNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.3")
	for _, plat := range []*platform.Platform{hopA, hopB, landA, landB} {
		pool.RebuildPlatform(plat)
	}

	if hops, ok := pool.UniqueLandingChain(landHash); ok {
		t.Fatalf("two chains should probe direct, hops=%v", hops)
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
}
