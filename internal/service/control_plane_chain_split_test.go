package service

import (
	"net/netip"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/state"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/topology"
)

const clashChainYAML = `
proxies:
  - name: hop-jp
    type: ss
    server: 1.2.3.4
    port: 443
    cipher: aes-256-gcm
    password: hop-pass
  - name: land-home
    type: http
    server: 5.6.7.8
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hops
proxy-groups:
  - name: hops
    type: select
    proxies:
      - hop-jp
`

func newChainSplitFixture(t *testing.T) *ControlPlaneService {
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
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})

	envCfg := &config.EnvConfig{
		DefaultPlatformStickyTTL:              30 * time.Minute,
		DefaultPlatformRegexFilters:           []string{},
		DefaultPlatformRegionFilters:          []string{},
		DefaultPlatformReverseProxyMissAction: "TREAT_AS_EMPTY",
		DefaultPlatformAllocationPolicy:       "BALANCED",
	}
	defaultRow := model.Platform{
		ID:                     platform.DefaultPlatformID,
		Name:                   platform.DefaultPlatformName,
		StickyTTLNs:            int64(envCfg.DefaultPlatformStickyTTL),
		RegexFilters:           []string{},
		RegionFilters:          []string{},
		ReverseProxyMissAction: envCfg.DefaultPlatformReverseProxyMissAction,
		AllocationPolicy:       envCfg.DefaultPlatformAllocationPolicy,
		UpdatedAtNs:            time.Now().UnixNano(),
	}
	if err := engine.UpsertPlatform(defaultRow); err != nil {
		t.Fatalf("UpsertPlatform Default: %v", err)
	}
	defaultPlat, err := platform.BuildFromModel(defaultRow)
	if err != nil {
		t.Fatalf("BuildFromModel Default: %v", err)
	}
	pool.RegisterPlatform(defaultPlat)

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	runtimeCfg.Store(config.NewDefaultRuntimeConfig())
	cp := &ControlPlaneService{
		Engine:     engine,
		Pool:       pool,
		SubMgr:     subMgr,
		RuntimeCfg: runtimeCfg,
		EnvCfg:     envCfg,
	}
	cp.Scheduler = topology.NewSubscriptionScheduler(topology.SchedulerConfig{
		SubManager: subMgr,
		Pool:       pool,
	})
	cp.Scheduler.SetOnSubUpdated(cp.EnsureClashChainPlatforms)
	return cp
}

func TestEnsureClashChainPlatforms_CreatesHopAndLanding(t *testing.T) {
	cp := newChainSplitFixture(t)
	name := "联合国公益"
	source := subscription.SourceTypeLocal
	content := clashChainYAML
	created, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name:       &name,
		SourceType: &source,
		Content:    &content,
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if err := cp.RefreshSubscription(created.ID); err != nil {
		t.Fatalf("RefreshSubscription: %v", err)
	}

	sub := cp.SubMgr.Lookup(created.ID)
	if sub == nil {
		t.Fatal("subscription missing from manager")
	}
	hops, landings := subscription.CountChainRoles(sub.ManagedNodes())
	if hops != 1 || landings != 1 {
		t.Fatalf("role counts hop=%d landing=%d, want 1, 1", hops, landings)
	}
	if !sub.DisableInvalidNodeCheck() {
		t.Fatal("expected disable_invalid_node_check enabled")
	}

	plats, err := cp.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms: %v", err)
	}
	var hop, land, def *PlatformResponse
	for i := range plats {
		p := plats[i]
		switch p.Name {
		case "联合国公益-前置":
			hop = &plats[i]
		case "联合国公益-落地":
			land = &plats[i]
		case platform.DefaultPlatformName:
			def = &plats[i]
		}
	}
	if hop == nil || land == nil || def == nil {
		t.Fatalf("missing auto platforms: hop=%v landing=%v default=%v (n=%d)", hop != nil, land != nil, def != nil, len(plats))
	}
	if !reflect.DeepEqual(hop.RegexFilters, []string{subscription.RoleTagHop}) {
		t.Fatalf("hop regex = %v", hop.RegexFilters)
	}
	if !reflect.DeepEqual(hop.SubscriptionIDs, []string{created.ID}) {
		t.Fatalf("hop subscription_ids = %v", hop.SubscriptionIDs)
	}
	if len(hop.ChainPlatformIDs) != 0 {
		t.Fatalf("hop chain = %v, want empty", hop.ChainPlatformIDs)
	}
	if !reflect.DeepEqual(land.RegexFilters, []string{subscription.RoleTagLanding}) {
		t.Fatalf("landing regex = %v", land.RegexFilters)
	}
	if !reflect.DeepEqual(land.SubscriptionIDs, []string{created.ID}) {
		t.Fatalf("landing subscription_ids = %v", land.SubscriptionIDs)
	}
	if !reflect.DeepEqual(land.ChainPlatformIDs, []string{hop.ID}) {
		t.Fatalf("landing chain = %v, want [%s]", land.ChainPlatformIDs, hop.ID)
	}
	if !reflect.DeepEqual(def.SubscriptionExcludeIDs, []string{created.ID}) {
		t.Fatalf("Default exclude = %v, want [%s]", def.SubscriptionExcludeIDs, created.ID)
	}

	hopID, landID := hop.ID, land.ID
	if err := cp.RefreshSubscription(created.ID); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	plats, err = cp.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms after refresh: %v", err)
	}
	if len(plats) != 3 {
		t.Fatalf("platform count after second refresh = %d, want 3", len(plats))
	}
	got := platformsByName(plats)
	if got["联合国公益-前置"].ID != hopID || got["联合国公益-落地"].ID != landID {
		t.Fatal("second refresh recreated auto platforms")
	}
}

func TestEnsureClashChainPlatforms_CreatesRegionLandings(t *testing.T) {
	cp := newChainSplitFixture(t)
	name := "联合国公益"
	source := subscription.SourceTypeLocal
	content := `
proxies:
  - name: hop-jp
    type: ss
    server: 1.2.3.4
    port: 443
    cipher: aes-256-gcm
    password: hop-pass
  - name: 日本-4% 1.2.3.4
    type: http
    server: 5.6.7.8
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hop-jp
  - name: 印度-1% 9.9.9.9
    type: http
    server: 6.6.6.6
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hop-jp
  - name: 印度尼西亚-8% 8.8.8.8
    type: http
    server: 7.7.7.7
    port: 8080
    username: user
    password: land-pass
    dialer-proxy: hop-jp
`
	created, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name:       &name,
		SourceType: &source,
		Content:    &content,
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if err := cp.RefreshSubscription(created.ID); err != nil {
		t.Fatalf("RefreshSubscription: %v", err)
	}
	plats, err := cp.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms: %v", err)
	}
	got := platformsByName(plats)
	hop, ok := got["联合国公益-前置"]
	if !ok {
		t.Fatal("missing hop platform")
	}
	if _, ok := got["联合国公益-落地"]; !ok {
		t.Fatal("missing catch-all landing")
	}
	jp, ok := got["联合国公益-落地-日本"]
	if !ok {
		t.Fatal("missing japan landing")
	}
	in, ok := got["联合国公益-落地-印度"]
	if !ok {
		t.Fatal("missing india landing")
	}
	id, ok := got["联合国公益-落地-印度尼西亚"]
	if !ok {
		t.Fatal("missing indonesia landing")
	}
	if !reflect.DeepEqual(jp.RegexFilters, []string{subscription.RegionFilterPattern("日本")}) {
		t.Fatalf("japan regex = %v", jp.RegexFilters)
	}
	if !reflect.DeepEqual(in.RegexFilters, []string{subscription.RegionFilterPattern("印度")}) {
		t.Fatalf("india regex = %v", in.RegexFilters)
	}
	if !reflect.DeepEqual(id.RegexFilters, []string{subscription.RegionFilterPattern("印度尼西亚")}) {
		t.Fatalf("indonesia regex = %v", id.RegexFilters)
	}
	if !reflect.DeepEqual(jp.ChainPlatformIDs, []string{hop.ID}) ||
		!reflect.DeepEqual(in.ChainPlatformIDs, []string{hop.ID}) {
		t.Fatalf("region landings should chain through hop %s", hop.ID)
	}
	if err := cp.RefreshSubscription(created.ID); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	plats, err = cp.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms after refresh: %v", err)
	}
	if len(plats) != 6 {
		t.Fatalf("platform count after second refresh = %d, want 6", len(plats))
	}
}

func TestEnsureClashChainPlatforms_SkipsNonMixedSubscription(t *testing.T) {
	cp := newChainSplitFixture(t)
	name := "plain"
	source := subscription.SourceTypeLocal
	content := `
proxies:
  - name: only-ss
    type: ss
    server: 1.1.1.1
    port: 443
    cipher: aes-256-gcm
    password: p
`
	created, err := cp.CreateSubscription(CreateSubscriptionRequest{
		Name:       &name,
		SourceType: &source,
		Content:    &content,
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if err := cp.RefreshSubscription(created.ID); err != nil {
		t.Fatalf("RefreshSubscription: %v", err)
	}
	plats, err := cp.ListPlatforms()
	if err != nil {
		t.Fatalf("ListPlatforms: %v", err)
	}
	if len(plats) != 1 || plats[0].Name != platform.DefaultPlatformName {
		t.Fatalf("expected only Default, got %+v", plats)
	}
	sub := cp.SubMgr.Lookup(created.ID)
	if sub.DisableInvalidNodeCheck() {
		t.Fatal("plain subscription should keep disable_invalid_node_check off")
	}
}

func platformsByName(plats []PlatformResponse) map[string]PlatformResponse {
	out := make(map[string]PlatformResponse, len(plats))
	for _, p := range plats {
		out[p.Name] = p
	}
	return out
}
