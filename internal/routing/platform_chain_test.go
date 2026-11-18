package routing

import (
	"encoding/json"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/testutil"
)

func newIsolatedRoutableEntry(t *testing.T, raw, ip, subID string) (node.Hash, *node.NodeEntry) {
	t.Helper()
	rawOpts := json.RawMessage(raw)
	h := node.HashFromRawOptions(rawOpts)
	e := node.NewNodeEntry(h, rawOpts, time.Now(), 16)
	e.AddSubscriptionID(subID)

	parsedIP, err := netip.ParseAddr(ip)
	if err != nil {
		t.Fatalf("parse ip %q: %v", ip, err)
	}
	e.SetEgressIP(parsedIP)
	e.LatencyTable.Update("cloudflare.com", 100*time.Millisecond, 10*time.Minute)
	waitForDomainLatency(t, e, "cloudflare.com")
	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)
	return h, e
}

func newChainPlatform(id, name, subID string) *platform.Platform {
	plat := platform.NewPlatform(id, name, nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	plat.SubscriptionIDs = []string{subID}
	return plat
}

func TestPlatformChainAttachesOrderedHops(t *testing.T) {
	pool := newRouterTestPool()
	hopA := newChainPlatform("hop-a", "HopA", "sub-a")
	hopB := newChainPlatform("hop-b", "HopB", "sub-b")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hopA.ID, hopB.ID}
	for _, plat := range []*platform.Platform{hopA, hopB, landing} {
		pool.addPlatform(plat)
	}

	hashA, entryA := newIsolatedRoutableEntry(t, `{"chain":"a"}`, "198.51.100.10", "sub-a")
	hashB, entryB := newIsolatedRoutableEntry(t, `{"chain":"b"}`, "198.51.100.11", "sub-b")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"l"}`, "198.51.100.12", "sub-land")
	pool.addEntry(hashA, entryA)
	pool.addEntry(hashB, entryB)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hopA)
	pool.rebuildPlatformView(hopB)
	pool.rebuildPlatformView(landing)

	result, err := newTestRouter(pool, nil).RouteRequest(landing.Name, "", "example.com:443")
	if err != nil {
		t.Fatalf("route chained platform: %v", err)
	}
	if result.NodeHash != hashL || result.SelectedPlatformID != landing.ID {
		t.Fatalf("landing = %+v, want node %s", result, hashL.Hex())
	}
	if len(result.ChainHops) != 2 {
		t.Fatalf("chain hops = %d, want 2: %+v", len(result.ChainHops), result.ChainHops)
	}
	if result.ChainHops[0].NodeHash != hashA || result.ChainHops[0].PlatformID != hopA.ID {
		t.Fatalf("first hop = %+v, want %s on %s", result.ChainHops[0], hashA.Hex(), hopA.ID)
	}
	if result.ChainHops[1].NodeHash != hashB || result.ChainHops[1].PlatformID != hopB.ID {
		t.Fatalf("second hop = %+v, want %s on %s", result.ChainHops[1], hashB.Hex(), hopB.ID)
	}
}

func TestPlatformChainSingleHop(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop", "Hop", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)

	hashH, entryH := newIsolatedRoutableEntry(t, `{"chain":"hop"}`, "198.51.100.20", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land"}`, "198.51.100.21", "sub-land")
	pool.addEntry(hashH, entryH)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	router := newTestRouter(pool, nil)
	result, err := router.RouteRequest(landing.Name, "acct-1", "example.com:443")
	if err != nil {
		t.Fatalf("route A->C: %v", err)
	}
	if result.NodeHash != hashL || len(result.ChainHops) != 1 || result.ChainHops[0].NodeHash != hashH {
		t.Fatalf("A->C result = %+v", result)
	}
	again, err := router.RouteRequest(landing.Name, "acct-1", "example.com:443")
	if err != nil {
		t.Fatalf("reuse A->C: %v", err)
	}
	if again.LeaseCreated || again.NodeHash != hashL || len(again.ChainHops) != 1 || again.ChainHops[0].NodeHash != hashH {
		t.Fatalf("sticky A->C = %+v", again)
	}
}

func TestPlatformChainDoesNotExpandHopChain(t *testing.T) {
	pool := newRouterTestPool()
	inner := newChainPlatform("inner", "Inner", "sub-inner")
	hop := newChainPlatform("hop", "Hop", "sub-hop")
	hop.ChainPlatformIDs = []string{inner.ID}
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	for _, plat := range []*platform.Platform{inner, hop, landing} {
		pool.addPlatform(plat)
	}

	hashInner, entryInner := newIsolatedRoutableEntry(t, `{"chain":"inner"}`, "198.51.100.30", "sub-inner")
	hashH, entryH := newIsolatedRoutableEntry(t, `{"chain":"hop"}`, "198.51.100.31", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land"}`, "198.51.100.32", "sub-land")
	pool.addEntry(hashInner, entryInner)
	pool.addEntry(hashH, entryH)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(inner)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	result, err := newTestRouter(pool, nil).RouteRequest(landing.Name, "", "example.com:443")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(result.ChainHops) != 1 || result.ChainHops[0].NodeHash != hashH {
		t.Fatalf("hop chain was expanded: %+v", result.ChainHops)
	}
}

func TestPlatformChainHopUnavailable(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop-empty", "HopEmpty", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land"}`, "198.51.100.40", "sub-land")
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	_, err := newTestRouter(pool, nil).RouteRequest(landing.Name, "", "example.com:443")
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("empty hop error = %v, want ErrNoAvailableNodes", err)
	}
}

func TestPlatformChainMissingHopPlatform(t *testing.T) {
	pool := newRouterTestPool()
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{"missing-hop"}
	pool.addPlatform(landing)
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land"}`, "198.51.100.41", "sub-land")
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(landing)

	_, err := newTestRouter(pool, nil).RouteRequest(landing.Name, "", "example.com:443")
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("missing hop error = %v, want ErrNoAvailableNodes", err)
	}
}

func TestPlatformChainUsesHopFallback(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop", "Hop", "sub-missing")
	fallback := newChainPlatform("hop-fb", "HopFB", "sub-hop")
	hop.FallbackPlatformIDs = []string{fallback.ID}
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(fallback)
	pool.addPlatform(landing)

	hashH, entryH := newIsolatedRoutableEntry(t, `{"chain":"hop-fb"}`, "198.51.100.50", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land"}`, "198.51.100.51", "sub-land")
	pool.addEntry(hashH, entryH)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(fallback)
	pool.rebuildPlatformView(landing)

	result, err := newTestRouter(pool, nil).RouteRequest(landing.Name, "", "example.com:443")
	if err != nil {
		t.Fatalf("route via hop fallback: %v", err)
	}
	if len(result.ChainHops) != 1 || result.ChainHops[0].NodeHash != hashH || result.ChainHops[0].PlatformID != fallback.ID {
		t.Fatalf("hop fallback = %+v, want node %s on %s", result.ChainHops, hashH.Hex(), fallback.ID)
	}
}

func TestRouteDiagnosticFailsWhenChainHopUnavailable(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop-empty", "HopEmpty", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land-diag"}`, "198.51.100.60", "sub-land")
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	router := newTestRouter(pool, nil)
	diagnostic, err := router.DiagnoseRoute(RouteOptions{
		PlatformName: landing.Name, Target: "example.com:443", RequiredNetwork: "tcp",
	}, 7)
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("diagnostic error = %v, want ErrNoAvailableNodes", err)
	}
	if diagnostic.Outcome != "no_available_nodes" {
		t.Fatalf("outcome = %q, want no_available_nodes", diagnostic.Outcome)
	}
	if diagnostic.Selection == nil || diagnostic.Selection.NodeHash != hashL.Hex() {
		t.Fatalf("landing selection missing: %+v", diagnostic.Selection)
	}
	if len(diagnostic.Selection.Chain) != 1 || diagnostic.Selection.Chain[0].NodeHash != "" {
		t.Fatalf("failed hop should be present without a node: %+v", diagnostic.Selection.Chain)
	}
	if router.states.Size() != 0 {
		t.Fatal("diagnostic mutated hop or landing lease state")
	}
}

func TestRouteDiagnosticAttachesChainHops(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop", "Hop", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)
	hashH, entryH := newIsolatedRoutableEntry(t, `{"chain":"hop-diag"}`, "198.51.100.61", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land-diag-ok"}`, "198.51.100.62", "sub-land")
	pool.addEntry(hashH, entryH)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	diagnostic, err := newTestRouter(pool, nil).DiagnoseRoute(RouteOptions{
		PlatformName: landing.Name, Target: "example.com:443", RequiredNetwork: "tcp",
	}, 9)
	if err != nil {
		t.Fatalf("diagnostic: %v", err)
	}
	if diagnostic.Selection == nil || diagnostic.Selection.NodeHash != hashL.Hex() {
		t.Fatalf("landing = %+v", diagnostic.Selection)
	}
	if len(diagnostic.Selection.Chain) != 1 || diagnostic.Selection.Chain[0].NodeHash != hashH.Hex() {
		t.Fatalf("chain = %+v, want hop %s", diagnostic.Selection.Chain, hashH.Hex())
	}
}

func TestPlatformChainExcludesLandingFromHopPick(t *testing.T) {
	pool := newRouterTestPool()
	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.StickyTTLNs = int64(time.Hour)
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)

	hashVPS, entryVPS := newIsolatedRoutableEntry(t, `{"chain":"vps"}`, "198.51.100.80", "sub-vps")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land-self"}`, "198.51.100.81", "sub-land")
	pool.addEntry(hashVPS, entryVPS)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	router := newTestRouter(pool, nil)
	result, err := router.RouteRequest(landing.Name, "acct-self", "example.com:443")
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if result.NodeHash != hashL || len(result.ChainHops) != 1 || result.ChainHops[0].NodeHash != hashVPS {
		t.Fatalf("result = %+v, want landing %s hop %s", result, hashL.Hex(), hashVPS.Hex())
	}
	again, err := router.RouteRequest(landing.Name, "acct-self", "example.com:443")
	if err != nil {
		t.Fatalf("sticky: %v", err)
	}
	if again.ChainHops[0].NodeHash != hashVPS {
		t.Fatalf("sticky hop reused landing: %+v", again.ChainHops)
	}
}

func TestPlatformChainLandingOnlyHopIsUnavailable(t *testing.T) {
	pool := newRouterTestPool()
	hop := platform.NewPlatform("hop", "Hop", nil, nil)
	hop.StickyTTLNs = int64(time.Hour)
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)

	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land-only-hop"}`, "198.51.100.82", "sub-land")
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	_, err := newTestRouter(pool, nil).RouteRequest(landing.Name, "", "example.com:443")
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("self hop error = %v, want ErrNoAvailableNodes", err)
	}
}

func TestPlatformChainUDPAttachesUDPHops(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop", "Hop", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)
	hashH, entryH := newIsolatedRoutableEntry(t, `{"chain":"hop-udp"}`, "198.51.100.70", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land-udp"}`, "198.51.100.71", "sub-land")
	setRouteEntryNetworks(entryH, "tcp", "udp")
	setRouteEntryNetworks(entryL, "tcp", "udp")
	pool.addEntry(hashH, entryH)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	result, err := newTestRouter(pool, nil).RouteWithOptions(RouteOptions{
		PlatformName:    landing.Name,
		Target:          "example.com:53",
		RequiredNetwork: "udp",
	})
	if err != nil {
		t.Fatalf("udp chain: %v", err)
	}
	if result.NodeHash != hashL || len(result.ChainHops) != 1 || result.ChainHops[0].NodeHash != hashH {
		t.Fatalf("udp chain result = %+v, want landing %s hop %s", result, hashL.Hex(), hashH.Hex())
	}
}

func TestPlatformChainUDPHopNotInUDPView(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop", "Hop", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)
	hashH, entryH := newIsolatedRoutableEntry(t, `{"chain":"hop-tcp"}`, "198.51.100.72", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land-udp-only"}`, "198.51.100.73", "sub-land")
	setRouteEntryNetworks(entryH, "tcp")
	setRouteEntryNetworks(entryL, "tcp", "udp")
	pool.addEntry(hashH, entryH)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	_, err := newTestRouter(pool, nil).RouteWithOptions(RouteOptions{
		PlatformName:    landing.Name,
		Target:          "example.com:53",
		RequiredNetwork: "udp",
	})
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("tcp-only hop error = %v, want ErrNoAvailableNodes", err)
	}
}
