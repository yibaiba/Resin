package platform

import (
	"fmt"
	"strings"
)

// NormalizeSubscriptionIDs trims subscription allowlist IDs, rejects blanks
// and duplicates, and returns a stable copy. An empty input means "all
// subscriptions" and is stored as an empty slice.
func NormalizeSubscriptionIDs(ids []string) ([]string, error) {
	return normalizeSubscriptionIDList(ids, "subscription_ids")
}

// NormalizeSubscriptionExcludeIDs trims excluded subscription IDs.
func NormalizeSubscriptionExcludeIDs(ids []string) ([]string, error) {
	return normalizeSubscriptionIDList(ids, "subscription_exclude_ids")
}

func normalizeSubscriptionIDList(ids []string, field string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for i, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, fmt.Errorf("%s[%d]: must not be empty", field, i)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%s must not contain duplicates", field)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// ValidateSubscriptionFilterOverlap rejects IDs that appear in both the
// allowlist and the exclude list.
func ValidateSubscriptionFilterOverlap(allowIDs, excludeIDs []string) error {
	if len(allowIDs) == 0 || len(excludeIDs) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(allowIDs))
	for _, id := range allowIDs {
		allow[id] = struct{}{}
	}
	for _, id := range excludeIDs {
		if _, ok := allow[id]; ok {
			return fmt.Errorf("subscription %q cannot be both allowed and excluded", id)
		}
	}
	return nil
}
