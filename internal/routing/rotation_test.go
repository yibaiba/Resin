package routing

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

type rotationFixture struct {
	router      *Router
	platform    *platform.Platform
	hashOne     node.Hash
	hashSameIP  node.Hash
	hashNewIP   node.Hash
	ipOne       netip.Addr
	ipTwo       netip.Addr
	createdAtNs int64
	expiryNs    int64
}

func newRotationFixture(t *testing.T, includeSameIP, includeNewIP bool) rotationFixture {
	t.Helper()
	pool := newRouterTestPool()
	plat := platform.NewPlatform("rotation", "Rotation", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	plat.RotationStrategy = platform.RotationStrategyAny
	pool.addPlatform(plat)
	ipOne := netip.MustParseAddr("198.51.100.111")
	ipTwo := netip.MustParseAddr("198.51.100.112")
	hashOne, entryOne := newRoutableEntry(t, `{"rotation":"one"}`, ipOne.String())
	pool.addEntry(hashOne, entryOne)
	var hashSameIP, hashNewIP node.Hash
	if includeSameIP {
		var entry *node.NodeEntry
		hashSameIP, entry = newRoutableEntry(t, `{"rotation":"same"}`, ipOne.String())
		pool.addEntry(hashSameIP, entry)
	}
	if includeNewIP {
		var entry *node.NodeEntry
		hashNewIP, entry = newRoutableEntry(t, `{"rotation":"new"}`, ipTwo.String())
		pool.addEntry(hashNewIP, entry)
	}
	pool.rebuildPlatformView(plat)
	now := time.Now()
	fixture := rotationFixture{
		router: newTestRouter(pool, nil), platform: plat,
		hashOne: hashOne, hashSameIP: hashSameIP, hashNewIP: hashNewIP,
		ipOne: ipOne, ipTwo: ipTwo,
		createdAtNs: now.Add(-10 * time.Minute).UnixNano(),
		expiryNs:    now.Add(50 * time.Minute).UnixNano(),
	}
	fixture.seedLease(t, "account", now.Add(-5*time.Minute))
	return fixture
}

func (f rotationFixture) seedLease(t *testing.T, account string, lastRotated time.Time) {
	t.Helper()
	err := f.router.UpsertLease(model.Lease{
		PlatformID: f.platform.ID, Account: account, NodeHash: f.hashOne.Hex(),
		EgressIP: f.ipOne.String(), CreatedAtNs: f.createdAtNs, ExpiryNs: f.expiryNs,
		LastAccessedNs: time.Now().UnixNano(), LastRotatedAtNs: lastRotated.UnixNano(),
	})
	if err != nil {
		t.Fatalf("seed lease: %v", err)
	}
}

func TestManualRotationStrategyMatrix(t *testing.T) {
	tests := []struct {
		strategy platform.RotationStrategy
	}{
		{strategy: platform.RotationStrategyAny},
		{strategy: platform.RotationStrategySameIP},
		{strategy: platform.RotationStrategyRequireNewIP},
		{strategy: platform.RotationStrategyPreferNewIP},
	}
	for _, test := range tests {
		t.Run(string(test.strategy), func(t *testing.T) {
			fixture := newRotationFixture(t, true, true)
			wantHash := fixture.hashSameIP
			if test.strategy != platform.RotationStrategySameIP && test.strategy != platform.RotationStrategyAny {
				wantHash = fixture.hashNewIP
			}
			result, err := fixture.router.RotateLease(fixture.platform.ID, "account", test.strategy)
			if err != nil {
				t.Fatalf("RotateLease: %v", err)
			}
			if test.strategy == platform.RotationStrategyAny {
				if result.NodeHash == fixture.hashOne || result.RotationStatus != "rotated" {
					t.Fatalf("ANY rotation did not move lease: %+v", result)
				}
				return
			}
			if result.NodeHash != wantHash || result.RotationStatus != "rotated" {
				t.Fatalf("rotation result = %+v, want node %s", result, wantHash.Hex())
			}
			lease := fixture.router.ReadLease(model.LeaseKey{PlatformID: fixture.platform.ID, Account: "account"})
			if lease.CreatedAtNs != fixture.createdAtNs || lease.ExpiryNs != fixture.expiryNs {
				t.Fatalf("rotation changed lease lifetime: %+v", lease)
			}
			if lease.RotationDeferred || lease.LastRotatedAtNs <= 0 {
				t.Fatalf("rotation metadata = %+v", lease)
			}
		})
	}
}

func TestRotationCanMoveLeaseIntoFallbackPlatform(t *testing.T) {
	pool := newRouterTestPool()
	root := platform.NewPlatform("rotation-root", "Rotation-Root", nil, nil)
	fallback := platform.NewPlatform("rotation-fallback", "Rotation-Fallback", nil, nil)
	root.StickyTTLNs = int64(time.Hour)
	root.FallbackPlatformIDs = []string{fallback.ID}
	pool.addPlatform(root)
	pool.addPlatform(fallback)

	oldHash, oldEntry := newRoutableEntry(t, `{"rotation":"root"}`, "198.51.100.131")
	pool.addEntry(oldHash, oldEntry)
	pool.rebuildPlatformView(root)
	newHash, newEntry := newRoutableEntry(t, `{"rotation":"fallback"}`, "198.51.100.132")
	pool.addEntry(newHash, newEntry)
	pool.rebuildPlatformView(fallback)

	router := newTestRouter(pool, nil)
	now := time.Now()
	if err := router.UpsertLease(model.Lease{
		PlatformID: root.ID, Account: "account", NodeHash: oldHash.Hex(),
		EgressIP: "198.51.100.131", CreatedAtNs: now.Add(-time.Minute).UnixNano(),
		ExpiryNs: now.Add(time.Hour).UnixNano(), LastAccessedNs: now.UnixNano(),
	}); err != nil {
		t.Fatalf("seed root lease: %v", err)
	}
	result, err := router.RotateLease(root.ID, "account", platform.RotationStrategyRequireNewIP)
	if err != nil || result.NodeHash != newHash || result.SelectedPlatformID != fallback.ID {
		t.Fatalf("fallback rotation: result=%+v err=%v", result, err)
	}
	lease := router.ReadLease(model.LeaseKey{PlatformID: root.ID, Account: "account"})
	if lease == nil || lease.PlatformID != root.ID || lease.SelectedPlatformID != fallback.ID {
		t.Fatalf("fallback rotation lease = %+v", lease)
	}
}

func TestStrictRotationDefersWhileOldLeaseRemainsUsable(t *testing.T) {
	fixture := newRotationFixture(t, false, false)
	result, err := fixture.router.RotateLease(
		fixture.platform.ID,
		"account",
		platform.RotationStrategyRequireNewIP,
	)
	if err != nil || result.RotationStatus != "deferred" || result.NodeHash != fixture.hashOne {
		t.Fatalf("deferred rotation: result=%+v err=%v", result, err)
	}
	lease := fixture.router.ReadLease(model.LeaseKey{PlatformID: fixture.platform.ID, Account: "account"})
	if lease == nil || !lease.RotationDeferred || lease.RotationRequestedAtNs == 0 ||
		lease.RotationRequestedStrategy != string(platform.RotationStrategyRequireNewIP) {
		t.Fatalf("deferred lease metadata = %+v", lease)
	}
}

func TestTimedAndDrainingRotation(t *testing.T) {
	t.Run("interval", func(t *testing.T) {
		fixture := newRotationFixture(t, false, true)
		fixture.platform.RotationIntervalNs = int64(time.Minute)
		fixture.platform.RotationStrategy = platform.RotationStrategyRequireNewIP
		result, err := fixture.router.RouteRequest(fixture.platform.Name, "account", "example.com:443")
		if err != nil || result.NodeHash != fixture.hashNewIP || result.RotationReason != "interval" {
			t.Fatalf("timed rotation: result=%+v err=%v", result, err)
		}
	})

	t.Run("draining", func(t *testing.T) {
		fixture := newRotationFixture(t, false, true)
		fixture.platform.RotationStrategy = platform.RotationStrategyRequireNewIP
		setTestEgressPolicy(t, fixture.router, fixture.ipOne, EgressIPDraining, 0, time.Time{})
		result, err := fixture.router.RouteRequest(fixture.platform.Name, "account", "example.com:443")
		if err != nil || result.NodeHash != fixture.hashNewIP || result.RotationReason != "draining" {
			t.Fatalf("draining rotation: result=%+v err=%v", result, err)
		}
	})
}

func TestBlockedLeaseCannotBeDeferred(t *testing.T) {
	fixture := newRotationFixture(t, false, false)
	fixture.platform.RotationStrategy = platform.RotationStrategyRequireNewIP
	setTestEgressPolicy(t, fixture.router, fixture.ipOne, EgressIPBlocked, 0, time.Time{})
	_, err := fixture.router.RouteRequest(fixture.platform.Name, "account", "example.com:443")
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("blocked rotation error = %v, want ErrNoAvailableNodes", err)
	}
	if lease := fixture.router.ReadLease(model.LeaseKey{PlatformID: fixture.platform.ID, Account: "account"}); lease != nil {
		t.Fatalf("blocked lease was deferred: %+v", lease)
	}
}

func TestRotateLeasesByEgressIPReturnsPerLeaseResults(t *testing.T) {
	fixture := newRotationFixture(t, false, true)
	fixture.seedLease(t, "second-account", time.Now())
	results := fixture.router.RotateLeasesByEgressIP(
		fixture.ipOne,
		platform.RotationStrategyRequireNewIP,
	)
	if len(results) != 2 {
		t.Fatalf("batch rotation results = %+v, want 2", results)
	}
	for _, result := range results {
		if result.Status != "rotated" || result.Error != "" {
			t.Fatalf("batch rotation result = %+v", result)
		}
		lease := fixture.router.ReadLease(model.LeaseKey{
			PlatformID: fixture.platform.ID, Account: result.Account,
		})
		if lease == nil || lease.EgressIP != fixture.ipTwo.String() {
			t.Fatalf("batch-rotated lease = %+v", lease)
		}
	}
}
