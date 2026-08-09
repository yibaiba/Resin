package state

import (
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
)

func TestStateRepo_ReplaceRoutingRulesIsOrderedAndReferencesPlatforms(t *testing.T) {
	repo := newTestStateRepo(t)
	platformID := "11111111-1111-1111-1111-111111111111"
	if err := repo.UpsertPlatform(model.Platform{
		ID: platformID, Name: "rule-target", StickyTTLNs: int64(time.Hour),
		RegexFilters: []string{}, RegionFilters: []string{},
		ReverseProxyMissAction:           string(platform.ReverseProxyMissActionTreatAsEmpty),
		ReverseProxyEmptyAccountBehavior: string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		AllocationPolicy:                 string(platform.AllocationPolicyBalanced), UpdatedAtNs: 1,
	}); err != nil {
		t.Fatal(err)
	}
	rules := []model.RoutingRule{
		{ID: "b", Name: "second", Enabled: true, Priority: 1, HostType: "ANY", Network: "ANY", Action: "DIRECT", CreatedAtNs: 1, UpdatedAtNs: 1},
		{ID: "a", Name: "first", Enabled: true, Priority: 0, HostType: "ANY", Network: "ANY", Action: "PLATFORM", PlatformID: platformID, CreatedAtNs: 1, UpdatedAtNs: 1},
	}
	if err := repo.ReplaceRoutingRules(rules); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.ListRoutingRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].ID != "a" || loaded[1].ID != "b" {
		t.Fatalf("unexpected rule order: %+v", loaded)
	}
	referenced, err := repo.RoutingRuleReferencesPlatform(platformID)
	if err != nil || !referenced {
		t.Fatalf("reference = %v, err=%v", referenced, err)
	}
	if err := repo.DeletePlatform(platformID); err == nil {
		t.Fatal("foreign key should reject deleting a referenced platform")
	}
}
