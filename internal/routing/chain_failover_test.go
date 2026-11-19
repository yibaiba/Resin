package routing

import (
	"errors"
	"testing"
	"time"
)

func TestChainDialFailureRotatesHopSticky(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop", "Hop", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)

	hashH1, entryH1 := newIsolatedRoutableEntry(t, `{"chain":"hop-1"}`, "198.51.100.20", "sub-hop")
	hashH2, entryH2 := newIsolatedRoutableEntry(t, `{"chain":"hop-2"}`, "198.51.100.21", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land"}`, "198.51.100.22", "sub-land")
	pool.addEntry(hashH1, entryH1)
	pool.addEntry(hashH2, entryH2)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	router := newTestRouter(pool, nil)
	first, err := router.RouteRequest(landing.Name, "acct-1", "example.com:443")
	if err != nil || len(first.ChainHops) != 1 {
		t.Fatalf("first route = %+v err=%v", first, err)
	}
	failed := first.ChainHops[0].NodeHash
	router.NoteChainDialFailure("acct-1", first)

	second, err := router.RouteRequest(landing.Name, "acct-1", "example.com:443")
	if err != nil || len(second.ChainHops) != 1 {
		t.Fatalf("second route = %+v err=%v", second, err)
	}
	if second.ChainHops[0].NodeHash == failed {
		t.Fatalf("failed hop %s was reused", failed.Hex())
	}
}

func TestChainDialFailureExcludesHopForEmptyAccount(t *testing.T) {
	pool := newRouterTestPool()
	hop := newChainPlatform("hop", "Hop", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)

	hashH1, entryH1 := newIsolatedRoutableEntry(t, `{"chain":"hop-empty-1"}`, "198.51.100.30", "sub-hop")
	hashH2, entryH2 := newIsolatedRoutableEntry(t, `{"chain":"hop-empty-2"}`, "198.51.100.31", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land-empty"}`, "198.51.100.32", "sub-land")
	pool.addEntry(hashH1, entryH1)
	pool.addEntry(hashH2, entryH2)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	router := newTestRouter(pool, nil)
	first, err := router.RouteRequest(landing.Name, "", "example.com:443")
	if err != nil || len(first.ChainHops) != 1 {
		t.Fatalf("first route = %+v err=%v", first, err)
	}
	failed := first.ChainHops[0].NodeHash
	router.NoteChainDialFailure("", first)

	for i := 0; i < 20; i++ {
		next, err := router.RouteRequest(landing.Name, "", "example.com:443")
		if err != nil || len(next.ChainHops) != 1 {
			t.Fatalf("retry %d = %+v err=%v", i, next, err)
		}
		if next.ChainHops[0].NodeHash == failed {
			t.Fatalf("empty-account retry reused failed hop %s", failed.Hex())
		}
	}
}

func TestChainDialFailureSingleHopUnavailableUntilTTL(t *testing.T) {
	oldTTL := chainHopFailExcludeTTL
	chainHopFailExcludeTTL = 40 * time.Millisecond
	defer func() { chainHopFailExcludeTTL = oldTTL }()

	pool := newRouterTestPool()
	hop := newChainPlatform("hop", "Hop", "sub-hop")
	landing := newChainPlatform("landing", "Landing", "sub-land")
	landing.ChainPlatformIDs = []string{hop.ID}
	pool.addPlatform(hop)
	pool.addPlatform(landing)

	hashH, entryH := newIsolatedRoutableEntry(t, `{"chain":"hop-only"}`, "198.51.100.40", "sub-hop")
	hashL, entryL := newIsolatedRoutableEntry(t, `{"chain":"land-only"}`, "198.51.100.41", "sub-land")
	pool.addEntry(hashH, entryH)
	pool.addEntry(hashL, entryL)
	pool.rebuildPlatformView(hop)
	pool.rebuildPlatformView(landing)

	router := newTestRouter(pool, nil)
	first, err := router.RouteRequest(landing.Name, "acct-ttl", "example.com:443")
	if err != nil {
		t.Fatalf("first route: %v", err)
	}
	router.NoteChainDialFailure("acct-ttl", first)
	if _, err := router.RouteRequest(landing.Name, "acct-ttl", "example.com:443"); !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("excluded hop error = %v, want ErrNoAvailableNodes", err)
	}
	time.Sleep(60 * time.Millisecond)
	again, err := router.RouteRequest(landing.Name, "acct-ttl", "example.com:443")
	if err != nil {
		t.Fatalf("after ttl: %v", err)
	}
	if len(again.ChainHops) != 1 || again.ChainHops[0].NodeHash != hashH {
		t.Fatalf("after ttl hops = %+v, want %s", again.ChainHops, hashH.Hex())
	}
}
