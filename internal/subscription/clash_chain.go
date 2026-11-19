package subscription

import (
	"regexp"
	"sort"
	"strings"

	"github.com/Resinat/Resin/internal/node"
)

const (
	// RoleTagHop marks a Clash node that other nodes reach through dialer-proxy.
	RoleTagHop = "resin-role:hop"
	// RoleTagLanding marks a Clash node that sets dialer-proxy (home-broadband exit).
	RoleTagLanding = "resin-role:landing"
	// RoleTagRegionPrefix marks a landing node's advertised country, e.g. resin-region:日本.
	RoleTagRegionPrefix = "resin-region:"
)

var landingRegionPattern = regexp.MustCompile(`^(.+?)-(\d+)%`)

// RegionTag builds the extra managed-node tag for a landing country label.
func RegionTag(region string) string {
	return RoleTagRegionPrefix + strings.TrimSpace(region)
}

// ParseRegionTag returns the country label stored in a resin-region tag.
func ParseRegionTag(tag string) (string, bool) {
	if !strings.HasPrefix(tag, RoleTagRegionPrefix) {
		return "", false
	}
	region := strings.TrimSpace(strings.TrimPrefix(tag, RoleTagRegionPrefix))
	if region == "" {
		return "", false
	}
	return region, true
}

// LandingRegionLabel extracts the country prefix from names like "日本-4% 1.2.3.4".
func LandingRegionLabel(tag string) string {
	m := landingRegionPattern.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// RegionFilterPattern is the platform regex that matches one resin-region tag.
func RegionFilterPattern(region string) string {
	return regexp.QuoteMeta(RegionTag(region)) + "$"
}

func applyClashChainRoleTags(nodes []ParsedNode, proxies []map[string]any, groups []map[string]any) {
	if len(nodes) == 0 || len(proxies) == 0 {
		return
	}
	hop, landing := classifyClashChainRoles(proxies, groups)
	hopN, landN := 0, 0
	for _, n := range nodes {
		if _, ok := hop[n.Tag]; ok {
			hopN++
		}
		if _, ok := landing[n.Tag]; ok {
			landN++
		}
	}
	if hopN == 0 || landN == 0 {
		return
	}
	for i := range nodes {
		switch {
		case hasMapKey(landing, nodes[i].Tag):
			extras := []string{RoleTagLanding}
			if region := LandingRegionLabel(nodes[i].Tag); region != "" {
				extras = append(extras, RegionTag(region))
			}
			nodes[i].ExtraTags = extras
		case hasMapKey(hop, nodes[i].Tag):
			nodes[i].ExtraTags = []string{RoleTagHop}
		}
	}
}

func classifyClashChainRoles(proxies []map[string]any, groups []map[string]any) (hop, landing map[string]struct{}) {
	hop = make(map[string]struct{})
	landing = make(map[string]struct{})
	groupMembers := make(map[string][]string, len(groups))
	for _, group := range groups {
		name := strings.TrimSpace(getString(group, "name"))
		if name == "" {
			continue
		}
		groupMembers[name] = getStringList(group, "proxies")
	}

	proxyNames := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		name := strings.TrimSpace(firstNonEmpty(getString(proxy, "name"), getString(proxy, "tag")))
		if name == "" {
			continue
		}
		proxyNames = append(proxyNames, name)
		detour := strings.TrimSpace(getString(proxy, "dialer-proxy", "dialer_proxy"))
		if detour == "" {
			continue
		}
		landing[name] = struct{}{}
		if members, ok := groupMembers[detour]; ok {
			for _, member := range members {
				member = strings.TrimSpace(member)
				if member != "" {
					hop[member] = struct{}{}
				}
			}
			continue
		}
		hop[detour] = struct{}{}
	}
	for name := range landing {
		delete(hop, name)
	}

	hopAmongProxies := 0
	for _, name := range proxyNames {
		if _, ok := hop[name]; ok {
			hopAmongProxies++
		}
	}
	if len(landing) > 0 && hopAmongProxies == 0 {
		for _, name := range proxyNames {
			if _, ok := landing[name]; ok {
				continue
			}
			hop[name] = struct{}{}
		}
	}
	return hop, landing
}

func hasMapKey(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

// CountChainRoles returns how many non-evicted managed nodes carry hop or landing role tags.
func CountChainRoles(mn *ManagedNodes) (hops, landings int) {
	if mn == nil {
		return 0, 0
	}
	mn.RangeNodes(func(_ node.Hash, n ManagedNode) bool {
		if n.Evicted {
			return true
		}
		for _, tag := range n.Tags {
			switch tag {
			case RoleTagHop:
				hops++
			case RoleTagLanding:
				landings++
			}
		}
		return true
	})
	return hops, landings
}

// ChainRegionLabels returns sorted unique landing country labels from role tags.
func ChainRegionLabels(mn *ManagedNodes) []string {
	if mn == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	mn.RangeNodes(func(_ node.Hash, n ManagedNode) bool {
		if n.Evicted {
			return true
		}
		for _, tag := range n.Tags {
			region, ok := ParseRegionTag(tag)
			if !ok {
				continue
			}
			if _, dup := seen[region]; dup {
				continue
			}
			seen[region] = struct{}{}
			out = append(out, region)
		}
		return true
	})
	sort.Strings(out)
	return out
}
