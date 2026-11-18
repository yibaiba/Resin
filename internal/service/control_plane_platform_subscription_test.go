package service

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/state"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
)

func newPlatformSubscriptionFixture(t *testing.T) (*ControlPlaneService, *subscription.Subscription, *subscription.Subscription, node.Hash, node.Hash) {
	t.Helper()

	dir := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(
		filepath.Join(dir, "state"),
		filepath.Join(dir, "cache"),
	)
	if err != nil {
		t.Fatalf("PersistenceBootstrap: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	subMgr := topology.NewSubscriptionManager()
	exclusive := subscription.NewSubscription("sub-exclusive", "exclusive", "https://example.com/a", true, false)
	shared := subscription.NewSubscription("sub-shared", "shared", "https://example.com/b", true, false)
	subMgr.Register(exclusive)
	subMgr.Register(shared)

	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	seed := func(raw []byte, sub *subscription.Subscription, tag string) node.Hash {
		hash := node.HashFromRawOptions(raw)
		sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{tag}})
		entry := node.NewNodeEntry(hash, raw, time.Now(), 16)
		entry.AddSubscriptionID(sub.ID)
		entry.SetEgressIP(netip.MustParseAddr("1.2.3.4"))
		entry.LatencyTable.LoadEntry("cloudflare.com", node.DomainLatencyStats{
			Ewma:        50 * time.Millisecond,
			LastUpdated: time.Now(),
		})
		ob := testutil.NewNoopOutbound()
		entry.Outbound.Store(&ob)
		pool.LoadNodeFromBootstrap(entry)
		return hash
	}

	exclusiveHash := seed([]byte(`{"type":"ss","server":"1.1.1.1","port":443}`), exclusive, "exclusive-node")
	sharedHash := seed([]byte(`{"type":"ss","server":"2.2.2.2","port":443}`), shared, "shared-node")

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())
	cp := &ControlPlaneService{
		Engine:     engine,
		Pool:       pool,
		SubMgr:     subMgr,
		RuntimeCfg: runtimeCfg,
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL:              30 * time.Minute,
			DefaultPlatformRegexFilters:           []string{},
			DefaultPlatformRegionFilters:          []string{},
			DefaultPlatformReverseProxyMissAction: "TREAT_AS_EMPTY",
			DefaultPlatformAllocationPolicy:       "BALANCED",
		},
	}
	return cp, exclusive, shared, exclusiveHash, sharedHash
}

func TestCreatePlatform_SubscriptionAllowlist(t *testing.T) {
	cp, exclusive, _, exclusiveHash, sharedHash := newPlatformSubscriptionFixture(t)

	name := "exclusive-platform"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:            &name,
		SubscriptionIDs: []string{exclusive.ID},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	if !reflect.DeepEqual(created.SubscriptionIDs, []string{exclusive.ID}) {
		t.Fatalf("subscription_ids = %v, want [%s]", created.SubscriptionIDs, exclusive.ID)
	}

	plat, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatal("platform missing from pool")
	}
	if plat.View().Size() != 1 || !plat.View().Contains(exclusiveHash) {
		t.Fatalf("view size=%d contains exclusive=%v shared=%v", plat.View().Size(), plat.View().Contains(exclusiveHash), plat.View().Contains(sharedHash))
	}
}

func TestCreatePlatform_SubscriptionExclude(t *testing.T) {
	cp, exclusive, _, exclusiveHash, sharedHash := newPlatformSubscriptionFixture(t)

	name := "exclude-home"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:                   &name,
		SubscriptionExcludeIDs: []string{exclusive.ID},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	if !reflect.DeepEqual(created.SubscriptionExcludeIDs, []string{exclusive.ID}) {
		t.Fatalf("subscription_exclude_ids = %v, want [%s]", created.SubscriptionExcludeIDs, exclusive.ID)
	}

	plat, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatal("platform missing from pool")
	}
	if plat.View().Contains(exclusiveHash) {
		t.Fatal("excluded subscription node should not be routable")
	}
	if !plat.View().Contains(sharedHash) {
		t.Fatal("non-excluded subscription node should stay routable")
	}
}

func TestCreatePlatform_RejectsAllowAndExcludeOverlap(t *testing.T) {
	cp, exclusive, _, _, _ := newPlatformSubscriptionFixture(t)
	name := "overlap-platform"
	_, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:                   &name,
		SubscriptionIDs:        []string{exclusive.ID},
		SubscriptionExcludeIDs: []string{exclusive.ID},
	})
	if err == nil {
		t.Fatal("expected overlapping allow/exclude to be rejected")
	}
	if svcErr, ok := err.(*ServiceError); !ok || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestCreatePlatform_RejectsUnknownSubscription(t *testing.T) {
	cp, _, _, _, _ := newPlatformSubscriptionFixture(t)
	name := "bad-sub-platform"
	_, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:            &name,
		SubscriptionIDs: []string{"missing-sub"},
	})
	if err == nil {
		t.Fatal("expected unknown subscription to be rejected")
	}
	if svcErr, ok := err.(*ServiceError); !ok || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}

func TestUpdatePlatform_ClearsSubscriptionAllowlist(t *testing.T) {
	cp, exclusive, _, exclusiveHash, sharedHash := newPlatformSubscriptionFixture(t)
	name := "toggle-sub-platform"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:            &name,
		SubscriptionIDs: []string{exclusive.ID},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}

	patch, err := json.Marshal(map[string]any{"subscription_ids": []string{}})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	updated, err := cp.UpdatePlatform(created.ID, patch)
	if err != nil {
		t.Fatalf("UpdatePlatform: %v", err)
	}
	if len(updated.SubscriptionIDs) != 0 {
		t.Fatalf("cleared subscription_ids = %v, want empty", updated.SubscriptionIDs)
	}

	plat, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatal("platform missing from pool")
	}
	if !plat.View().Contains(exclusiveHash) || !plat.View().Contains(sharedHash) {
		t.Fatalf("cleared allowlist should include both nodes, view size=%d", plat.View().Size())
	}
}

func TestDeleteSubscription_RemovesPlatformSubscriptionIDs(t *testing.T) {
	cp, exclusive, _, exclusiveHash, sharedHash := newPlatformSubscriptionFixture(t)
	name := "cleanup-sub-platform"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:            &name,
		SubscriptionIDs: []string{exclusive.ID},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}

	if err := cp.Engine.UpsertSubscription(model.Subscription{
		ID:                        exclusive.ID,
		Name:                      exclusive.Name(),
		URL:                       exclusive.URL(),
		UpdateIntervalNs:          int64(30 * time.Second),
		Enabled:                   true,
		EphemeralNodeEvictDelayNs: exclusive.EphemeralNodeEvictDelayNs(),
		CreatedAtNs:               time.Now().UnixNano(),
		UpdatedAtNs:               time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("UpsertSubscription: %v", err)
	}

	if err := cp.DeleteSubscription(exclusive.ID); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}

	got, err := cp.GetPlatform(created.ID)
	if err != nil {
		t.Fatalf("GetPlatform: %v", err)
	}
	if len(got.SubscriptionIDs) != 0 {
		t.Fatalf("subscription_ids after delete = %v, want empty", got.SubscriptionIDs)
	}

	plat, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatal("platform missing from pool")
	}
	if len(plat.SubscriptionIDs) != 0 {
		t.Fatalf("runtime subscription_ids = %v, want empty", plat.SubscriptionIDs)
	}
	if plat.View().Contains(exclusiveHash) {
		t.Fatal("deleted subscription node should leave the view")
	}
	if !plat.View().Contains(sharedHash) {
		t.Fatal("empty allowlist should make remaining subscription nodes routable")
	}

	err = cp.DeleteSubscription(exclusive.ID)
	if err == nil {
		t.Fatal("expected second delete to return not found")
	}
	if svcErr, ok := err.(*ServiceError); !ok || svcErr.Code != "NOT_FOUND" {
		t.Fatalf("second delete error = %v, want NOT_FOUND", err)
	}
}

func TestDeleteSubscription_StripsStaleAllowlistWhenSubscriptionAlreadyGone(t *testing.T) {
	cp, exclusive, _, exclusiveHash, sharedHash := newPlatformSubscriptionFixture(t)
	name := "stale-allowlist-platform"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:            &name,
		SubscriptionIDs: []string{exclusive.ID},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}

	exclusive.ManagedNodes().RangeNodes(func(h node.Hash, _ subscription.ManagedNode) bool {
		cp.Pool.RemoveNodeFromSub(h, exclusive.ID)
		return true
	})
	cp.SubMgr.Unregister(exclusive.ID)

	got, err := cp.GetPlatform(created.ID)
	if err != nil {
		t.Fatalf("GetPlatform: %v", err)
	}
	if len(got.SubscriptionIDs) != 1 || got.SubscriptionIDs[0] != exclusive.ID {
		t.Fatalf("stale subscription_ids = %v, want [%s]", got.SubscriptionIDs, exclusive.ID)
	}

	if err := cp.DeleteSubscription(exclusive.ID); err != nil {
		t.Fatalf("DeleteSubscription should strip leftover allowlist IDs: %v", err)
	}

	got, err = cp.GetPlatform(created.ID)
	if err != nil {
		t.Fatalf("GetPlatform after strip: %v", err)
	}
	if len(got.SubscriptionIDs) != 0 {
		t.Fatalf("subscription_ids after stale cleanup = %v, want empty", got.SubscriptionIDs)
	}
	plat, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatal("platform missing from pool")
	}
	if len(plat.SubscriptionIDs) != 0 {
		t.Fatalf("runtime subscription_ids after stale cleanup = %v, want empty", plat.SubscriptionIDs)
	}
	if plat.View().Contains(exclusiveHash) {
		t.Fatal("exclusive node should remain gone")
	}
	if !plat.View().Contains(sharedHash) {
		t.Fatal("stripped allowlist should include remaining subscription nodes")
	}
}

func TestPreviewFilter_SubscriptionIDs(t *testing.T) {
	cp, exclusive, _, exclusiveHash, sharedHash := newPlatformSubscriptionFixture(t)

	nodes, err := cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			SubscriptionIDs: []string{exclusive.ID},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != exclusiveHash.Hex() {
		t.Fatalf("preview nodes = %+v, want only %s", nodes, exclusiveHash.Hex())
	}
	for _, n := range nodes {
		if n.NodeHash == sharedHash.Hex() {
			t.Fatal("shared node should not match exclusive allowlist preview")
		}
	}
}

func TestPreviewFilter_SubscriptionExcludeIDs(t *testing.T) {
	cp, exclusive, _, exclusiveHash, sharedHash := newPlatformSubscriptionFixture(t)

	nodes, err := cp.PreviewFilter(PreviewFilterRequest{
		PlatformSpec: &PlatformSpecFilter{
			SubscriptionExcludeIDs: []string{exclusive.ID},
		},
	})
	if err != nil {
		t.Fatalf("PreviewFilter: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != sharedHash.Hex() {
		t.Fatalf("preview nodes = %+v, want only %s", nodes, sharedHash.Hex())
	}
	for _, n := range nodes {
		if n.NodeHash == exclusiveHash.Hex() {
			t.Fatal("excluded subscription node should not match preview")
		}
	}
}

func TestDeleteSubscription_RemovesPlatformExcludeIDs(t *testing.T) {
	cp, exclusive, _, exclusiveHash, sharedHash := newPlatformSubscriptionFixture(t)
	name := "cleanup-exclude-platform"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:                   &name,
		SubscriptionExcludeIDs: []string{exclusive.ID},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}

	if err := cp.Engine.UpsertSubscription(model.Subscription{
		ID:                        exclusive.ID,
		Name:                      exclusive.Name(),
		URL:                       exclusive.URL(),
		UpdateIntervalNs:          int64(30 * time.Second),
		Enabled:                   true,
		EphemeralNodeEvictDelayNs: exclusive.EphemeralNodeEvictDelayNs(),
		CreatedAtNs:               time.Now().UnixNano(),
		UpdatedAtNs:               time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("UpsertSubscription: %v", err)
	}

	if err := cp.DeleteSubscription(exclusive.ID); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}

	got, err := cp.GetPlatform(created.ID)
	if err != nil {
		t.Fatalf("GetPlatform: %v", err)
	}
	if len(got.SubscriptionExcludeIDs) != 0 {
		t.Fatalf("subscription_exclude_ids after delete = %v, want empty", got.SubscriptionExcludeIDs)
	}

	plat, ok := cp.Pool.GetPlatform(created.ID)
	if !ok {
		t.Fatal("platform missing from pool")
	}
	if len(plat.SubscriptionExcludeIDs) != 0 {
		t.Fatalf("runtime subscription_exclude_ids = %v, want empty", plat.SubscriptionExcludeIDs)
	}
	if plat.View().Contains(exclusiveHash) {
		t.Fatal("deleted subscription node should leave the view")
	}
	if !plat.View().Contains(sharedHash) {
		t.Fatal("cleared exclude list should keep remaining subscription nodes")
	}
}

func TestUpdatePlatform_RejectsAllowAndExcludeOverlap(t *testing.T) {
	cp, exclusive, _, _, _ := newPlatformSubscriptionFixture(t)
	name := "update-overlap-platform"
	created, err := cp.CreatePlatform(CreatePlatformRequest{
		Name:            &name,
		SubscriptionIDs: []string{exclusive.ID},
	})
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}

	patch, err := json.Marshal(map[string]any{"subscription_exclude_ids": []string{exclusive.ID}})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	_, err = cp.UpdatePlatform(created.ID, patch)
	if err == nil {
		t.Fatal("expected overlapping allow/exclude patch to be rejected")
	}
	if svcErr, ok := err.(*ServiceError); !ok || svcErr.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error = %v, want INVALID_ARGUMENT", err)
	}
}
