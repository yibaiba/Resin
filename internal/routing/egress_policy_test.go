package routing

import (
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

func setupEgressPolicyRoute(t *testing.T) (*Router, *platform.Platform, netip.Addr) {
	t.Helper()
	pool := newRouterTestPool()
	plat := platform.NewPlatform("egress-policy", "Egress-Policy", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)
	ip := netip.MustParseAddr("198.51.100.101")
	hash, entry := newRoutableEntry(t, `{"egress":"policy"}`, ip.String())
	pool.addEntry(hash, entry)
	pool.rebuildPlatformView(plat)
	return newTestRouter(pool, nil), plat, ip
}

func setTestEgressPolicy(t *testing.T, router *Router, ip netip.Addr, status EgressIPStatus, max int64, resume time.Time) {
	t.Helper()
	resumeAtNs := int64(0)
	if !resume.IsZero() {
		resumeAtNs = resume.UnixNano()
	}
	err := router.SetEgressPolicy(model.EgressIPPolicy{
		EgressIP: ip.String(), Status: string(status),
		MaxActiveLeases: max, ResumeAtNs: resumeAtNs,
	})
	if err != nil {
		t.Fatalf("set egress policy: %v", err)
	}
}

func TestEgressPolicyDrainingKeepsExistingLeaseButRejectsNewAllocation(t *testing.T) {
	router, plat, ip := setupEgressPolicyRoute(t)
	first, err := router.RouteRequest(plat.Name, "existing", "example.com:443")
	if err != nil {
		t.Fatalf("create existing lease: %v", err)
	}
	setTestEgressPolicy(t, router, ip, EgressIPDraining, 0, time.Time{})

	reused, err := router.RouteRequest(plat.Name, "existing", "example.com:443")
	if err != nil || reused.NodeHash != first.NodeHash {
		t.Fatalf("reuse draining lease: result=%+v err=%v", reused, err)
	}
	if _, err := router.RouteRequest(plat.Name, "new", "example.com:443"); !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("new draining allocation error = %v, want ErrNoAvailableNodes", err)
	}
	if _, err := router.RouteRequest(plat.Name, "", "example.com:443"); !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("random draining allocation error = %v, want ErrNoAvailableNodes", err)
	}
}

func TestEgressPolicyBlockedInvalidatesExistingLease(t *testing.T) {
	router, plat, ip := setupEgressPolicyRoute(t)
	if _, err := router.RouteRequest(plat.Name, "account", "example.com:443"); err != nil {
		t.Fatalf("create lease: %v", err)
	}
	setTestEgressPolicy(t, router, ip, EgressIPBlocked, 0, time.Time{})

	if _, err := router.RouteRequest(plat.Name, "account", "example.com:443"); !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("blocked lease route error = %v, want ErrNoAvailableNodes", err)
	}
	if lease := router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: "account"}); lease != nil {
		t.Fatalf("blocked lease was retained: %+v", lease)
	}
	if got := router.SnapshotGlobalIPLoad()[ip]; got != 0 {
		t.Fatalf("global load after block = %d, want 0", got)
	}
}

func TestEgressPolicyCapacityReservationIsGlobalAndAtomic(t *testing.T) {
	router, plat, ip := setupEgressPolicyRoute(t)
	const capacity = int64(3)
	setTestEgressPolicy(t, router, ip, EgressIPActive, capacity, time.Time{})

	start := make(chan struct{})
	var successes atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(account string) {
			defer wg.Done()
			<-start
			if _, err := router.RouteRequest(plat.Name, account, "example.com:443"); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrNoAvailableNodes) {
				t.Errorf("route %s: %v", account, err)
			}
		}(string(rune('a' + i)))
	}
	close(start)
	wg.Wait()
	if got := successes.Load(); got != capacity {
		t.Fatalf("successful leases = %d, want %d", got, capacity)
	}
	if got := router.SnapshotGlobalIPLoad()[ip]; got != capacity {
		t.Fatalf("global load = %d, want %d", got, capacity)
	}
	if _, err := router.RouteRequest(plat.Name, "", "example.com:443"); err != nil {
		t.Fatalf("non-sticky route should ignore lease capacity: %v", err)
	}
}

func TestEgressPolicyRestoreRebuildsCapacityAndResumeActivates(t *testing.T) {
	router, plat, ip := setupEgressPolicyRoute(t)
	now := time.Now()
	router.RestoreLeases([]model.Lease{
		{PlatformID: plat.ID, Account: "one", NodeHash: nodeHashForTest(t, router, plat), EgressIP: ip.String(), ExpiryNs: now.Add(time.Hour).UnixNano()},
		{PlatformID: plat.ID, Account: "two", NodeHash: nodeHashForTest(t, router, plat), EgressIP: ip.String(), ExpiryNs: now.Add(time.Hour).UnixNano()},
	})
	setTestEgressPolicy(t, router, ip, EgressIPActive, 2, time.Time{})
	if _, err := router.RouteRequest(plat.Name, "three", "example.com:443"); !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("route beyond restored capacity = %v, want ErrNoAvailableNodes", err)
	}

	setTestEgressPolicy(t, router, ip, EgressIPBlocked, 0, now.Add(-time.Second))
	if _, err := router.RouteRequest(plat.Name, "resumed", "example.com:443"); err != nil {
		t.Fatalf("route after automatic resume: %v", err)
	}
}

func nodeHashForTest(t *testing.T, router *Router, plat *platform.Platform) string {
	t.Helper()
	var result string
	plat.View().Range(func(hash node.Hash) bool {
		result = hash.Hex()
		return false
	})
	if result == "" {
		t.Fatal("test platform has no node")
	}
	return result
}
