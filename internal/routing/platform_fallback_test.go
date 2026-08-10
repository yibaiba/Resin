package routing

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
)

func TestPlatformFallbackSelectsFirstRoutableDepthFirst(t *testing.T) {
	pool := newRouterTestPool()
	root := platform.NewPlatform("root", "Root", nil, nil)
	first := platform.NewPlatform("first", "First", nil, nil)
	nested := platform.NewPlatform("nested", "Nested", nil, nil)
	second := platform.NewPlatform("second", "Second", nil, nil)
	root.FallbackPlatformIDs = []string{first.ID, second.ID}
	first.FallbackPlatformIDs = []string{nested.ID}
	for _, candidate := range []*platform.Platform{root, first, nested, second} {
		candidate.StickyTTLNs = int64(time.Hour)
		pool.addPlatform(candidate)
	}

	hash, entry := newRoutableEntry(t, `{"fallback":"nested"}`, "198.51.100.91")
	pool.addEntry(hash, entry)
	pool.rebuildPlatformView(nested)
	pool.rebuildPlatformView(second)

	router := newTestRouter(pool, nil)
	result, err := router.RouteRequest(root.Name, "", "example.com:443")
	if err != nil {
		t.Fatalf("route with fallback: %v", err)
	}
	if result.SelectedPlatformID != nested.ID || result.NodeHash != hash {
		t.Fatalf("selected fallback = %+v, want platform %q node %s", result, nested.ID, hash.Hex())
	}
	wantPath := []string{root.ID, first.ID, nested.ID}
	if !reflect.DeepEqual(result.FallbackPath, wantPath) {
		t.Fatalf("fallback path = %v, want %v", result.FallbackPath, wantPath)
	}
}

func TestPlatformFallbackStickyLeaseKeepsOwnerAndSelectedIdentity(t *testing.T) {
	pool := newRouterTestPool()
	root := platform.NewPlatform("owner", "Owner", nil, nil)
	selected := platform.NewPlatform("selected", "Selected", nil, nil)
	root.FallbackPlatformIDs = []string{selected.ID}
	root.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(root)
	pool.addPlatform(selected)
	hash, entry := newRoutableEntry(t, `{"fallback":"sticky"}`, "198.51.100.92")
	pool.addEntry(hash, entry)
	pool.rebuildPlatformView(selected)

	router := newTestRouter(pool, nil)
	first, err := router.RouteRequest(root.Name, "account-1", "example.com:443")
	if err != nil {
		t.Fatalf("create fallback lease: %v", err)
	}
	second, err := router.RouteRequest(root.Name, "account-1", "example.com:443")
	if err != nil {
		t.Fatalf("reuse fallback lease: %v", err)
	}
	if first.NodeHash != second.NodeHash || second.LeaseCreated {
		t.Fatalf("lease was not reused: first=%+v second=%+v", first, second)
	}

	persisted := router.ReadLease(model.LeaseKey{PlatformID: root.ID, Account: "account-1"})
	if persisted == nil || persisted.PlatformID != root.ID || persisted.SelectedPlatformID != selected.ID {
		t.Fatalf("persisted lease identities = %+v", persisted)
	}
	ip := netip.MustParseAddr("198.51.100.92")
	if got := router.SnapshotIPLoad(root.ID)[ip]; got != 1 {
		t.Fatalf("owner load = %d, want 1", got)
	}
	if got := router.ensureSelectedStats(selected.ID).Get(ip); got != 1 {
		t.Fatalf("selected load = %d, want 1", got)
	}

	restored := newTestRouter(pool, nil)
	restored.RestoreLeases([]model.Lease{*persisted})
	result, err := restored.RouteRequest(root.Name, "account-1", "example.com:443")
	if err != nil || result.SelectedPlatformID != selected.ID || result.NodeHash != hash {
		t.Fatalf("restored fallback lease: result=%+v err=%v", result, err)
	}
}

func TestPlatformFallbackMissingReferenceIsExplicit(t *testing.T) {
	pool := newRouterTestPool()
	root := platform.NewPlatform("root-missing", "Root-Missing", nil, nil)
	root.FallbackPlatformIDs = []string{"does-not-exist"}
	pool.addPlatform(root)

	_, err := newTestRouter(pool, nil).RouteRequest(root.Name, "", "example.com:443")
	if !errors.Is(err, ErrPlatformNotFound) {
		t.Fatalf("route error = %v, want ErrPlatformNotFound", err)
	}
}

func TestDeleteLeasesForPlatformClearsOwnerSelectedAndGlobalLoads(t *testing.T) {
	pool := newRouterTestPool()
	for _, candidate := range []*platform.Platform{
		platform.NewPlatform("deleted", "Deleted", nil, nil),
		platform.NewPlatform("owner", "Owner", nil, nil),
		platform.NewPlatform("other", "Other", nil, nil),
	} {
		pool.addPlatform(candidate)
	}
	events := make([]LeaseEvent, 0, 2)
	router := newTestRouter(pool, func(event LeaseEvent) { events = append(events, event) })
	ip := netip.MustParseAddr("198.51.100.93")
	hash, _ := newRoutableEntry(t, `{"fallback":"delete"}`, ip.String())
	now := time.Now()
	for _, lease := range []model.Lease{
		{PlatformID: "deleted", SelectedPlatformID: "deleted", Account: "owned", NodeHash: hash.Hex(), EgressIP: ip.String(), ExpiryNs: now.Add(time.Hour).UnixNano()},
		{PlatformID: "owner", SelectedPlatformID: "deleted", Account: "selected", NodeHash: hash.Hex(), EgressIP: ip.String(), ExpiryNs: now.Add(time.Hour).UnixNano()},
		{PlatformID: "other", SelectedPlatformID: "other", Account: "kept", NodeHash: hash.Hex(), EgressIP: ip.String(), ExpiryNs: now.Add(time.Hour).UnixNano()},
	} {
		if err := router.UpsertLease(lease); err != nil {
			t.Fatalf("upsert lease %q: %v", lease.Account, err)
		}
	}

	if deleted := router.DeleteLeasesForPlatform("deleted"); deleted != 2 {
		t.Fatalf("deleted leases = %d, want 2", deleted)
	}
	if router.ReadLease(model.LeaseKey{PlatformID: "deleted", Account: "owned"}) != nil ||
		router.ReadLease(model.LeaseKey{PlatformID: "owner", Account: "selected"}) != nil {
		t.Fatal("owner or selected lease survived Platform deletion")
	}
	if router.ReadLease(model.LeaseKey{PlatformID: "other", Account: "kept"}) == nil {
		t.Fatal("unrelated lease was deleted")
	}
	if got := router.SnapshotGlobalIPLoad()[ip]; got != 1 {
		t.Fatalf("global load = %d, want 1", got)
	}
	if len(events) != 5 || events[3].Type != LeaseRemove || events[4].Type != LeaseRemove {
		t.Fatalf("lease events = %+v, want three creates then two removes", events)
	}
}

func TestRestoreLeasesSkipsMissingOwnerOrSelectedPlatform(t *testing.T) {
	pool := newRouterTestPool()
	pool.addPlatform(platform.NewPlatform("owner", "Owner", nil, nil))
	hash, _ := newRoutableEntry(t, `{"fallback":"orphan"}`, "198.51.100.94")
	now := time.Now()
	router := newTestRouter(pool, nil)
	router.RestoreLeases([]model.Lease{
		{PlatformID: "missing-owner", Account: "owner-gone", NodeHash: hash.Hex(), EgressIP: "198.51.100.94", ExpiryNs: now.Add(time.Hour).UnixNano()},
		{PlatformID: "owner", SelectedPlatformID: "missing-selected", Account: "selected-gone", NodeHash: hash.Hex(), EgressIP: "198.51.100.94", ExpiryNs: now.Add(time.Hour).UnixNano()},
	})
	if loads := router.SnapshotGlobalIPLoad(); len(loads) != 0 {
		t.Fatalf("orphan leases restored global loads: %v", loads)
	}
}

func TestUpsertLeaseRejectsMissingOwnerOrSelectedPlatform(t *testing.T) {
	pool := newRouterTestPool()
	pool.addPlatform(platform.NewPlatform("owner", "Owner", nil, nil))
	hash, _ := newRoutableEntry(t, `{"fallback":"upsert-orphan"}`, "198.51.100.95")
	base := model.Lease{
		PlatformID: "owner", Account: "account", NodeHash: hash.Hex(),
		EgressIP: "198.51.100.95", ExpiryNs: time.Now().Add(time.Hour).UnixNano(),
	}
	router := newTestRouter(pool, nil)

	missingOwner := base
	missingOwner.PlatformID = "missing-owner"
	if err := router.UpsertLease(missingOwner); !errors.Is(err, ErrPlatformNotFound) {
		t.Fatalf("missing owner error = %v, want ErrPlatformNotFound", err)
	}
	missingSelected := base
	missingSelected.SelectedPlatformID = "missing-selected"
	if err := router.UpsertLease(missingSelected); !errors.Is(err, ErrPlatformNotFound) {
		t.Fatalf("missing selected error = %v, want ErrPlatformNotFound", err)
	}
	if loads := router.SnapshotGlobalIPLoad(); len(loads) != 0 {
		t.Fatalf("rejected orphan leases changed global loads: %v", loads)
	}
}

func TestDeleteLeasesForPlatformWaitsForInFlightUpsert(t *testing.T) {
	pool := newRouterTestPool()
	pool.addPlatform(platform.NewPlatform("deleted", "Deleted", nil, nil))
	hash, _ := newRoutableEntry(t, `{"fallback":"in-flight"}`, "198.51.100.96")
	createStarted := make(chan struct{})
	releaseCreate := make(chan struct{})
	router := newTestRouter(pool, func(event LeaseEvent) {
		if event.Type == LeaseCreate {
			close(createStarted)
			<-releaseCreate
		}
	})
	lease := model.Lease{
		PlatformID: "deleted", Account: "account", NodeHash: hash.Hex(),
		EgressIP: "198.51.100.96", ExpiryNs: time.Now().Add(time.Hour).UnixNano(),
	}
	upsertDone := make(chan error, 1)
	go func() { upsertDone <- router.UpsertLease(lease) }()
	<-createStarted

	cleanupDone := make(chan int, 1)
	go func() { cleanupDone <- router.DeleteLeasesForPlatform("deleted") }()
	select {
	case <-cleanupDone:
		t.Fatal("lease cleanup completed before the in-flight upsert")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseCreate)
	if err := <-upsertDone; err != nil {
		t.Fatalf("upsert lease: %v", err)
	}
	if deleted := <-cleanupDone; deleted != 1 {
		t.Fatalf("deleted leases = %d, want 1", deleted)
	}
	if lease := router.ReadLease(model.LeaseKey{PlatformID: "deleted", Account: "account"}); lease != nil {
		t.Fatal("in-flight lease survived platform cleanup")
	}
	if loads := router.SnapshotGlobalIPLoad(); len(loads) != 0 {
		t.Fatalf("platform cleanup left global loads: %v", loads)
	}
}
