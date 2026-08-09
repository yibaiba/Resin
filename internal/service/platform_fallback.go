package service

import (
	"fmt"
	"strings"

	"github.com/Resinat/Resin/internal/model"
)

func (s *ControlPlaneService) validatePlatformFallbackGraph(candidate model.Platform) *ServiceError {
	graph, err := s.Engine.ListPlatformFallbacks()
	if err != nil {
		return internal("list platforms for fallback validation", err)
	}
	graph[candidate.ID] = append([]string(nil), candidate.FallbackPlatformIDs...)
	for id, fallbackIDs := range graph {
		seen := make(map[string]struct{}, len(fallbackIDs))
		for index, fallbackID := range fallbackIDs {
			fallbackID = strings.TrimSpace(fallbackID)
			fallbackIDs[index] = fallbackID
			if fallbackID == "" {
				return invalidArg("fallback_platform_ids must not contain empty values")
			}
			if fallbackID == id {
				return invalidArg("fallback_platform_ids must not reference the same platform")
			}
			if _, duplicate := seen[fallbackID]; duplicate {
				return invalidArg("fallback_platform_ids must not contain duplicates")
			}
			seen[fallbackID] = struct{}{}
			if _, exists := graph[fallbackID]; !exists {
				return invalidArg(fmt.Sprintf("fallback platform %q does not exist", fallbackID))
			}
		}
		graph[id] = fallbackIDs
	}
	if hasFallbackCycle(graph) {
		return invalidArg("fallback_platform_ids introduce a cycle")
	}
	return nil
}

func hasFallbackCycle(platforms map[string][]string) bool {
	const (
		unseen = iota
		visiting
		visited
	)
	states := make(map[string]int, len(platforms))
	var visit func(string) bool
	visit = func(id string) bool {
		if states[id] == visiting {
			return true
		}
		if states[id] == visited {
			return false
		}
		states[id] = visiting
		for _, fallbackID := range platforms[id] {
			if visit(fallbackID) {
				return true
			}
		}
		states[id] = visited
		return false
	}
	for id := range platforms {
		if visit(id) {
			return true
		}
	}
	return false
}
