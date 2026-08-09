package platform

import "strings"

type RotationStrategy string

const (
	RotationStrategyAny          RotationStrategy = "ANY"
	RotationStrategySameIP       RotationStrategy = "SAME_IP"
	RotationStrategyPreferNewIP  RotationStrategy = "PREFER_NEW_IP"
	RotationStrategyRequireNewIP RotationStrategy = "REQUIRE_NEW_IP"
)

func (s RotationStrategy) IsValid() bool {
	return s == RotationStrategyAny || s == RotationStrategySameIP ||
		s == RotationStrategyPreferNewIP || s == RotationStrategyRequireNewIP
}

func ParseRotationStrategy(raw string) RotationStrategy {
	strategy := RotationStrategy(strings.ToUpper(strings.TrimSpace(raw)))
	if !strategy.IsValid() {
		return ""
	}
	return strategy
}
