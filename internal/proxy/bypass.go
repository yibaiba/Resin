package proxy

import "github.com/Resinat/Resin/internal/routing"

// TargetBypassMatcher remains an alias for compatibility with proxy callers.
type TargetBypassMatcher = routing.TargetBypassMatcher

func NewTargetBypassMatcher(patterns []string) *TargetBypassMatcher {
	return routing.NewTargetBypassMatcher(patterns)
}
