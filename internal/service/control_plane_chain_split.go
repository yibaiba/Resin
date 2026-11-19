package service

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/state"
	"github.com/Resinat/Resin/internal/subscription"
)

const (
	autoHopPlatformSuffix     = "-前置"
	autoLandingPlatformSuffix = "-落地"
)

// EnsureClashChainPlatforms creates hop and landing Platforms when a Clash
// subscription has both resin-role:hop and resin-role:landing nodes. Existing
// auto Platforms are left unchanged. Traffic still uses platform chain.
func (s *ControlPlaneService) EnsureClashChainPlatforms(sub *subscription.Subscription) {
	if s == nil || sub == nil || s.Engine == nil || s.Pool == nil || s.EnvCfg == nil {
		return
	}
	if strings.TrimSpace(sub.GetLastError()) != "" {
		return
	}
	hops, landings := subscription.CountChainRoles(sub.ManagedNodes())
	if hops == 0 || landings == 0 {
		return
	}

	s.platformMu.Lock()
	defer s.platformMu.Unlock()

	plats, err := s.Engine.ListPlatforms()
	if err != nil {
		log.Printf("[control-plane] auto chain split: list platforms: %v", err)
		return
	}

	hopPlat := findAutoChainPlatform(plats, sub.ID, subscription.RoleTagHop, false)
	landPlat := findAutoChainPlatform(plats, sub.ID, subscription.RoleTagLanding, true)

	created := false
	if hopPlat == nil {
		name, nameErr := unusedAutoPlatformName(plats, autoPlatformName(sub.Name(), autoHopPlatformSuffix), sub.ID)
		if nameErr != nil {
			log.Printf("[control-plane] auto chain split hop name: %v", nameErr)
			return
		}
		createdPlat, createErr := s.createAutoChainPlatformLocked(name, []string{subscription.RoleTagHop}, []string{sub.ID}, nil)
		if createErr != nil {
			log.Printf("[control-plane] auto chain split create hop %s: %v", name, createErr)
			return
		}
		hopPlat = createdPlat
		created = true
		plats = append(plats, *hopPlat)
	}

	if landPlat == nil {
		name, nameErr := unusedAutoPlatformName(plats, autoPlatformName(sub.Name(), autoLandingPlatformSuffix), sub.ID)
		if nameErr != nil {
			log.Printf("[control-plane] auto chain split landing name: %v", nameErr)
			return
		}
		createdPlat, createErr := s.createAutoChainPlatformLocked(
			name,
			[]string{subscription.RoleTagLanding},
			[]string{sub.ID},
			[]string{hopPlat.ID},
		)
		if createErr != nil {
			log.Printf("[control-plane] auto chain split create landing %s: %v", name, createErr)
			return
		}
		landPlat = createdPlat
		created = true
		plats = append(plats, *landPlat)
	}

	regionCreated := s.ensureRegionLandingPlatformsLocked(sub, hopPlat, plats)
	if !created && !regionCreated {
		return
	}

	if created {
		if err := s.excludeSubscriptionFromDefaultLocked(sub.ID); err != nil {
			log.Printf("[control-plane] auto chain split exclude Default: %v", err)
		}
		if err := s.enableDisableInvalidNodeCheckLocked(sub); err != nil {
			log.Printf("[control-plane] auto chain split disable invalid-node check: %v", err)
		}
		log.Printf(
			"[control-plane] auto-created chain platforms for %s (%s): hop=%s landing=%s",
			sub.Name(),
			sub.ID,
			hopPlat.Name,
			landPlat.Name,
		)
	}
}

const maxAutoRegionLandingPlatforms = 128

func (s *ControlPlaneService) ensureRegionLandingPlatformsLocked(
	sub *subscription.Subscription,
	hopPlat *model.Platform,
	plats []model.Platform,
) bool {
	if hopPlat == nil {
		return false
	}
	regions := subscription.ChainRegionLabels(sub.ManagedNodes())
	if len(regions) == 0 {
		return false
	}
	if len(regions) > maxAutoRegionLandingPlatforms {
		log.Printf(
			"[control-plane] auto chain split skip region platforms for %s: %d regions > %d",
			sub.Name(),
			len(regions),
			maxAutoRegionLandingPlatforms,
		)
		return false
	}
	createdN := 0
	for _, region := range regions {
		rx := subscription.RegionFilterPattern(region)
		if findAutoChainPlatform(plats, sub.ID, rx, true) != nil {
			continue
		}
		name, nameErr := unusedAutoPlatformName(
			plats,
			autoPlatformName(sub.Name(), autoLandingPlatformSuffix+"-"+platform.SanitizeNameFragment(region)),
			sub.ID,
		)
		if nameErr != nil {
			log.Printf("[control-plane] auto chain split region name %s: %v", region, nameErr)
			continue
		}
		createdPlat, createErr := s.createAutoChainPlatformLocked(
			name,
			[]string{rx},
			[]string{sub.ID},
			[]string{hopPlat.ID},
		)
		if createErr != nil {
			log.Printf("[control-plane] auto chain split create region %s: %v", name, createErr)
			continue
		}
		plats = append(plats, *createdPlat)
		createdN++
	}
	if createdN > 0 {
		log.Printf(
			"[control-plane] auto-created %d region landing platforms for %s (%s)",
			createdN,
			sub.Name(),
			sub.ID,
		)
	}
	return createdN > 0
}

func (s *ControlPlaneService) createAutoChainPlatformLocked(
	name string,
	regexFilters []string,
	subscriptionIDs []string,
	chainIDs []string,
) (*model.Platform, error) {
	cfg := s.defaultPlatformConfig(name)
	cfg.RegexFilters = append([]string(nil), regexFilters...)
	cfg.SubscriptionIDs = append([]string(nil), subscriptionIDs...)
	cfg.ChainPlatformIDs = append([]string(nil), chainIDs...)
	if err := validatePlatformConfig(&cfg, true); err != nil {
		return nil, err
	}
	allowIDs, excludeIDs, filterErr := s.normalizeSubscriptionFilters(cfg.SubscriptionIDs, cfg.SubscriptionExcludeIDs)
	if filterErr != nil {
		return nil, filterErr
	}
	cfg.SubscriptionIDs = allowIDs
	cfg.SubscriptionExcludeIDs = excludeIDs

	id := uuid.New().String()
	mp, plat, svcErr := s.compileAndUpsertPlatform(id, cfg)
	if svcErr != nil {
		return nil, svcErr
	}
	s.Pool.RebuildPlatform(plat)
	s.Pool.RegisterPlatform(plat)
	return &mp, nil
}

func (s *ControlPlaneService) excludeSubscriptionFromDefaultLocked(subID string) error {
	mp, err := s.Engine.GetPlatform(platform.DefaultPlatformID)
	if errors.Is(err, state.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, id := range mp.SubscriptionExcludeIDs {
		if id == subID {
			return nil
		}
	}
	for _, id := range mp.SubscriptionIDs {
		if id == subID {
			return nil
		}
	}
	cfg := platformConfigFromModel(*mp)
	cfg.SubscriptionExcludeIDs = append(append([]string(nil), cfg.SubscriptionExcludeIDs...), subID)
	if err := validatePlatformConfig(&cfg, true); err != nil {
		return err
	}
	allowIDs, excludeIDs, filterErr := s.normalizeSubscriptionFilters(cfg.SubscriptionIDs, cfg.SubscriptionExcludeIDs)
	if filterErr != nil {
		return filterErr
	}
	cfg.SubscriptionIDs = allowIDs
	cfg.SubscriptionExcludeIDs = excludeIDs
	updated, plat, svcErr := s.compileAndUpsertPlatform(mp.ID, cfg)
	if svcErr != nil {
		return svcErr
	}
	if _, ok := s.Pool.GetPlatform(updated.ID); ok {
		return s.Pool.ReplacePlatform(plat)
	}
	s.Pool.RebuildPlatform(plat)
	s.Pool.RegisterPlatform(plat)
	return nil
}

func (s *ControlPlaneService) enableDisableInvalidNodeCheckLocked(sub *subscription.Subscription) error {
	if sub.DisableInvalidNodeCheck() {
		return nil
	}
	sub.SetDisableInvalidNodeCheck(true)
	sub.UpdatedAtNs = time.Now().UnixNano()
	if err := s.Engine.UpsertSubscription(subscriptionSnapshot(sub)); err != nil {
		sub.SetDisableInvalidNodeCheck(false)
		return err
	}
	s.Pool.RebuildAllPlatforms()
	return nil
}

func subscriptionSnapshot(sub *subscription.Subscription) model.Subscription {
	return model.Subscription{
		ID:                        sub.ID,
		Name:                      sub.Name(),
		SourceType:                sub.SourceType(),
		URL:                       sub.URL(),
		Content:                   sub.Content(),
		UpdateIntervalNs:          sub.UpdateIntervalNs(),
		Enabled:                   sub.Enabled(),
		Ephemeral:                 sub.Ephemeral(),
		IncrementalAliveNodes:     sub.IncrementalAliveNodes(),
		DisableInvalidNodeCheck:   sub.DisableInvalidNodeCheck(),
		EphemeralNodeEvictDelayNs: sub.EphemeralNodeEvictDelayNs(),
		CreatedAtNs:               sub.CreatedAtNs,
		UpdatedAtNs:               sub.UpdatedAtNs,
	}
}

func findAutoChainPlatform(plats []model.Platform, subID, roleTag string, requireChain bool) *model.Platform {
	for i := range plats {
		p := &plats[i]
		if p.ID == platform.DefaultPlatformID {
			continue
		}
		if len(p.SubscriptionIDs) != 1 || p.SubscriptionIDs[0] != subID {
			continue
		}
		if !sliceContainsString(p.RegexFilters, roleTag) {
			continue
		}
		hasChain := len(p.ChainPlatformIDs) > 0
		if requireChain != hasChain {
			continue
		}
		return p
	}
	return nil
}

func autoPlatformName(subName, suffix string) string {
	name := platform.SanitizeNameFragment(subName) + suffix
	if err := platform.ValidatePlatformName(name); err != nil {
		return "sub" + suffix
	}
	return name
}

func unusedAutoPlatformName(plats []model.Platform, desired, subID string) (string, error) {
	taken := make(map[string]struct{}, len(plats))
	for _, p := range plats {
		taken[p.Name] = struct{}{}
	}
	if _, ok := taken[desired]; !ok {
		if err := platform.ValidatePlatformName(desired); err != nil {
			return "", err
		}
		return desired, nil
	}
	alt := desired + "-" + shortPlatformSuffix(subID)
	if _, ok := taken[alt]; !ok && platform.ValidatePlatformName(alt) == nil {
		return alt, nil
	}
	return "", fmt.Errorf("platform name %q is taken", desired)
}

func shortPlatformSuffix(subID string) string {
	compact := strings.ReplaceAll(subID, "-", "")
	if len(compact) >= 8 {
		return compact[:8]
	}
	if compact != "" {
		return compact
	}
	return "auto"
}

func sliceContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
