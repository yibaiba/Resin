package service

import (
	"fmt"
	"strings"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
)

func (s *ControlPlaneService) validatePlatformChainGraph(candidate model.Platform) *ServiceError {
	graph, err := s.Engine.ListPlatformChains()
	if err != nil {
		return internal("list platforms for chain validation", err)
	}
	graph[candidate.ID] = append([]string(nil), candidate.ChainPlatformIDs...)
	for id, chainIDs := range graph {
		seen := make(map[string]struct{}, len(chainIDs))
		if len(chainIDs) > platform.MaxChainPlatformIDs {
			return invalidArg(fmt.Sprintf("chain_platform_ids: at most %d hops", platform.MaxChainPlatformIDs))
		}
		for index, chainID := range chainIDs {
			chainID = strings.TrimSpace(chainID)
			chainIDs[index] = chainID
			if chainID == "" {
				return invalidArg("chain_platform_ids must not contain empty values")
			}
			if chainID == id {
				return invalidArg("chain_platform_ids must not reference the same platform")
			}
			if _, duplicate := seen[chainID]; duplicate {
				return invalidArg("chain_platform_ids must not contain duplicates")
			}
			seen[chainID] = struct{}{}
			if _, exists := graph[chainID]; !exists {
				return invalidArg(fmt.Sprintf("chain platform %q does not exist", chainID))
			}
		}
		graph[id] = chainIDs
	}
	if hasFallbackCycle(graph) {
		return invalidArg("chain_platform_ids introduce a cycle")
	}
	return nil
}
