package service

import (
	"encoding/json"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/state"
	"github.com/Resinat/Resin/internal/topology"
)

const concurrentMutationIterations = 20

func TestConcurrentPlatformFallbackUpdatesRemainAcyclic(t *testing.T) {
	cp := newRoutingControlTestService(t)
	a := createTestPlatform(t, cp, "fallback-a")
	b := createTestPlatform(t, cp, "fallback-b")

	for iteration := 0; iteration < concurrentMutationIterations; iteration++ {
		clearPlatformFallbacks(t, cp, a.ID)
		clearPlatformFallbacks(t, cp, b.ID)
		errors := concurrentFallbackUpdates(cp, a.ID, b.ID)
		if successes := countNilErrors(errors); successes != 1 {
			t.Fatalf("iteration %d successes = %d, want 1; errors=%v", iteration, successes, errors)
		}
		graph, err := cp.Engine.ListPlatformFallbacks()
		if err != nil {
			t.Fatalf("list fallback graph: %v", err)
		}
		if hasFallbackCycle(graph) {
			t.Fatalf("iteration %d persisted a fallback cycle: %v", iteration, graph)
		}
	}
}

func TestConcurrentEgressPolicyPatchesRetainFieldsAndSnapshotOrder(t *testing.T) {
	cp := newRoutingControlTestService(t)
	ip := "198.51.100.201"
	active := string(routing.EgressIPActive)
	zero := int64(0)
	draining := string(routing.EgressIPDraining)
	limit := int64(7)

	for iteration := 0; iteration < concurrentMutationIterations; iteration++ {
		if _, err := cp.UpdateEgressIPPolicy(ip, UpdateEgressIPPolicyRequest{
			Status: &active, MaxActiveLeases: &zero,
		}); err != nil {
			t.Fatalf("reset policy: %v", err)
		}
		errors := concurrentEgressUpdates(cp, ip, draining, limit)
		for _, err := range errors {
			if err != nil {
				t.Fatalf("iteration %d concurrent patch: %v", iteration, err)
			}
		}
		assertEgressPolicyConsistent(t, cp, ip, draining, limit)
	}
}

func TestDeletePlatformReleasesOwnerAndSelectedLeaseCapacity(t *testing.T) {
	cp := newRoutingControlTestService(t)
	deleted := createTestPlatform(t, cp, "delete-with-leases")
	owner := createTestPlatform(t, cp, "fallback-owner")
	other := createTestPlatform(t, cp, "capacity-reuser")
	setPlatformFallbacks(t, cp, owner.ID, []string{deleted.ID})

	ip := "198.51.100.202"
	hash := node.HashFromRawOptions([]byte(`{"delete":"capacity"}`)).Hex()
	expires := time.Now().Add(time.Hour).UnixNano()
	for _, lease := range []model.Lease{
		{PlatformID: deleted.ID, SelectedPlatformID: deleted.ID, Account: "owned", NodeHash: hash, EgressIP: ip, ExpiryNs: expires},
		{PlatformID: owner.ID, SelectedPlatformID: deleted.ID, Account: "selected", NodeHash: hash, EgressIP: ip, ExpiryNs: expires},
	} {
		if err := cp.Router.UpsertLease(lease); err != nil {
			t.Fatalf("seed lease %q: %v", lease.Account, err)
		}
	}
	limit := int64(2)
	if _, err := cp.UpdateEgressIPPolicy(ip, UpdateEgressIPPolicyRequest{MaxActiveLeases: &limit}); err != nil {
		t.Fatalf("set capacity: %v", err)
	}
	blocked := model.Lease{PlatformID: other.ID, Account: "waiting", NodeHash: hash, EgressIP: ip, ExpiryNs: expires}
	if err := cp.Router.UpsertLease(blocked); !errors.Is(err, routing.ErrNoAvailableNodes) {
		t.Fatalf("capacity precondition error = %v, want ErrNoAvailableNodes", err)
	}

	setPlatformFallbacks(t, cp, owner.ID, []string{})
	if err := cp.DeletePlatform(deleted.ID); err != nil {
		t.Fatalf("delete platform: %v", err)
	}
	if got := cp.Router.SnapshotGlobalIPLoad()[netip.MustParseAddr(ip)]; got != 0 {
		t.Fatalf("global load after delete = %d, want 0", got)
	}
	if err := cp.Router.UpsertLease(blocked); err != nil {
		t.Fatalf("reuse capacity after delete: %v", err)
	}
}

func newRoutingControlTestService(t *testing.T) *ControlPlaneService {
	t.Helper()
	dir := t.TempDir()
	engine, closer, err := state.PersistenceBootstrap(filepath.Join(dir, "state"), filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatalf("bootstrap persistence: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16, MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow: func() time.Duration { return 10 * time.Minute },
	})
	router := routing.NewRouter(routing.RouterConfig{
		Pool: pool, Authorities: func() []string { return []string{"cloudflare.com"} },
		P2CWindow: func() time.Duration { return 10 * time.Minute },
	})
	return &ControlPlaneService{
		Engine: engine, Pool: pool, Router: router,
		EnvCfg: &config.EnvConfig{
			DefaultPlatformStickyTTL: time.Hour, DefaultPlatformRegexFilters: []string{},
			DefaultPlatformRegionFilters: []string{}, DefaultPlatformReverseProxyMissAction: "TREAT_AS_EMPTY",
			DefaultPlatformAllocationPolicy: "BALANCED",
		},
	}
}

func createTestPlatform(t *testing.T, cp *ControlPlaneService, name string) *PlatformResponse {
	t.Helper()
	created, err := cp.CreatePlatform(CreatePlatformRequest{Name: &name})
	if err != nil {
		t.Fatalf("create platform %q: %v", name, err)
	}
	return created
}

func clearPlatformFallbacks(t *testing.T, cp *ControlPlaneService, id string) {
	t.Helper()
	setPlatformFallbacks(t, cp, id, []string{})
}

func setPlatformFallbacks(t *testing.T, cp *ControlPlaneService, id string, ids []string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"fallback_platform_ids": ids})
	if err != nil {
		t.Fatalf("marshal fallback patch: %v", err)
	}
	if _, err := cp.UpdatePlatform(id, payload); err != nil {
		t.Fatalf("set fallback %s -> %v: %v", id, ids, err)
	}
}

func concurrentFallbackUpdates(cp *ControlPlaneService, a, b string) []error {
	start := make(chan struct{})
	errors := make([]error, 2)
	var wg sync.WaitGroup
	for index, edge := range [][2]string{{a, b}, {b, a}} {
		wg.Add(1)
		go func(index int, edge [2]string) {
			defer wg.Done()
			<-start
			payload, _ := json.Marshal(map[string]any{"fallback_platform_ids": []string{edge[1]}})
			_, errors[index] = cp.UpdatePlatform(edge[0], payload)
		}(index, edge)
	}
	close(start)
	wg.Wait()
	return errors
}

func concurrentEgressUpdates(cp *ControlPlaneService, ip, status string, limit int64) []error {
	start := make(chan struct{})
	errors := make([]error, 2)
	var wg sync.WaitGroup
	requests := []UpdateEgressIPPolicyRequest{{Status: &status}, {MaxActiveLeases: &limit}}
	for index, request := range requests {
		wg.Add(1)
		go func(index int, request UpdateEgressIPPolicyRequest) {
			defer wg.Done()
			<-start
			_, errors[index] = cp.UpdateEgressIPPolicy(ip, request)
		}(index, request)
	}
	close(start)
	wg.Wait()
	return errors
}

func countNilErrors(errors []error) int {
	count := 0
	for _, err := range errors {
		if err == nil {
			count++
		}
	}
	return count
}

func assertEgressPolicyConsistent(t *testing.T, cp *ControlPlaneService, ip, status string, limit int64) {
	t.Helper()
	persisted, err := cp.Engine.GetEgressIPPolicy(ip)
	if err != nil {
		t.Fatalf("get persisted policy: %v", err)
	}
	runtime := cp.Router.EgressPolicies().Get(netip.MustParseAddr(ip))
	if persisted.Status != status || persisted.MaxActiveLeases != limit {
		t.Fatalf("persisted policy = %+v, want status=%s limit=%d", persisted, status, limit)
	}
	if string(runtime.Status) != persisted.Status || runtime.MaxActiveLeases != persisted.MaxActiveLeases {
		t.Fatalf("runtime policy = %+v, persisted = %+v", runtime, persisted)
	}
}
