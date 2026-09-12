package platform

import (
	"fmt"
	"strings"
)

// NormalizeSubscriptionIDs trims subscription allowlist IDs, rejects blanks
// and duplicates, and returns a stable copy. An empty input means "all
// subscriptions" and is stored as an empty slice.
func NormalizeSubscriptionIDs(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for i, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, fmt.Errorf("subscription_ids[%d]: must not be empty", i)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("subscription_ids must not contain duplicates")
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}
