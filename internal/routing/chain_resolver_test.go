package routing_test

import (
	"encoding/json"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
	N "github.com/sagernet/sing/common/network"
)

func addChainResolverNode(t *testing.T, pool *topology.GlobalNodePool, subMgr *topology.SubscriptionManager, subID, raw, ip string) node.Hash {
	t.Helper()
	sub, ok := subMgr.Get(subID)
	if !ok {
		sub = subscription.NewSubscription(subID, subID, "url", true, false)
		subMgr.Register(sub)
	}
	rawOpts := json.RawMessage(raw)
	hash := node.HashFromRawOptions(rawOpts)
	next := subscription.NewManagedNodes()
	if existing := sub.ManagedNodes(); existing != nil {
		existing.RangeNodes(func(h node.Hash, n subscription.ManagedNode) bool {
			next.StoreNode(h, n)
			return true
		})
	}
	next.StoreNode(hash, subscription.ManagedNode{Tags: []string{subID}})
	sub.SwapManagedNodes(next)
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

func hopLeaseAccounts(router *routing.Router, platformID string) map[string]routing.Lease {
	out := make(map[string]routing.Lease)
	router.RangeLeases(platformID, func(account string, lease routing.Lease) bool {
		out[account] = lease
		return true
	})
	return out
}

func TestRouterUniqueLandingChainUsesProbeAccount(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.StickyTTLNs = int64(time.Hour)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	hopHash := addChainResolverNode(t, pool, subMgr, "hop-sub", `{"role":"hop"}`, "198.51.100.1")
	landHash := addChainResolverNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"cloudflare.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})
	hops, ok := router.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != hopHash {
		t.Fatalf("probe hops = %v ok=%v, want [%s]", hops, ok, hopHash.Hex())
	}
	leases := hopLeaseAccounts(router, hop.ID)
	if _, exists := leases[routing.ProbeAccount]; !exists {
		t.Fatal("probe hop pick should create __resin_probe__ lease")
	}
	if _, exists := leases["acct-user"]; exists {
		t.Fatal("probe hop pick occupied a user account")
	}

	user, err := router.RouteRequest(landing.Name, "acct-user", "example.com:443")
	if err != nil {
		t.Fatalf("user route: %v", err)
	}
	if len(user.ChainHops) != 1 || user.ChainHops[0].NodeHash != hopHash {
		t.Fatalf("user hops = %+v, want %s", user.ChainHops, hopHash.Hex())
	}
	leases = hopLeaseAccounts(router, hop.ID)
	if _, exists := leases["acct-user"]; !exists {
		t.Fatal("user hop sticky missing")
	}
}

func TestRouterUniqueLandingChainUDPRequiresUDPHop(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	tcpHash := addChainResolverNode(t, pool, subMgr, "hop-sub", `{"role":"hop-tcp"}`, "198.51.100.1")
	landHash := addChainResolverNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return nil },
		P2CWindow:   func() time.Duration { return time.Minute },
	})
	hops, ok := router.UniqueLandingChain(landHash)
	if !ok || len(hops) != 1 || hops[0] != tcpHash {
		t.Fatalf("tcp probe hops = %v ok=%v, want %s", hops, ok, tcpHash.Hex())
	}
	if udpHops, ok := router.UniqueLandingChainForNetwork(landHash, N.NetworkUDP); ok {
		t.Fatalf("udp probe hops = %v, want skip", udpHops)
	}
}

func TestRouterNoteProbeChainFailureRotatesProbeHop(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.StickyTTLNs = int64(time.Hour)
	hop.SubscriptionIDs = []string{"hop-sub"}
	landing := platform.NewPlatform("landing", "Landing", nil, nil)
	landing.SubscriptionIDs = []string{"land-sub"}
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.RegisterPlatform(hop)
	pool.RegisterPlatform(landing)

	hashH1 := addChainResolverNode(t, pool, subMgr, "hop-sub", `{"role":"hop-a"}`, "198.51.100.1")
	hashH2 := addChainResolverNode(t, pool, subMgr, "hop-sub", `{"role":"hop-b"}`, "198.51.100.3")
	landHash := addChainResolverNode(t, pool, subMgr, "land-sub", `{"role":"land"}`, "198.51.100.2")
	pool.RebuildPlatform(hop)
	pool.RebuildPlatform(landing)

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"cloudflare.com"} },
		P2CWindow:   func() time.Duration { return 10 * time.Minute },
	})
	first, ok := router.UniqueLandingChain(landHash)
	if !ok || len(first) != 1 {
		t.Fatalf("first probe hops = %v ok=%v", first, ok)
	}
	failed := first[0]
	router.NoteProbeChainFailure(landHash)
	second, ok := router.UniqueLandingChain(landHash)
	if !ok || len(second) != 1 {
		t.Fatalf("second probe hops = %v ok=%v", second, ok)
	}
	if second[0] == failed {
		t.Fatalf("probe reused failed hop %s (candidates %s %s)", failed.Hex(), hashH1.Hex(), hashH2.Hex())
	}
}
