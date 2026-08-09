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
