package routing_test

import (
	"testing"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/routing"
)

func TestParseTarget_NormalizesSupportedForms(t *testing.T) {
	tests := []struct {
		raw  string
		host string
		port uint16
	}{
		{raw: "https://Example.COM/path", host: "example.com", port: 443},
		{raw: "http://example.com:8080/a", host: "example.com", port: 8080},
		{raw: "example.com:8443", host: "example.com", port: 8443},
		{raw: "[2001:db8::1]:443", host: "2001:db8::1", port: 443},
		{raw: "example.com.", host: "example.com", port: 0},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			target, err := routing.ParseTarget(test.raw)
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			if target.Host != test.host || target.Port != test.port {
				t.Fatalf("target = %q:%d, want %q:%d", target.Host, target.Port, test.host, test.port)
			}
		})
	}
}

func TestDecisionEngine_PrecedenceAndMatching(t *testing.T) {
	pool, _ := setupPool(t)
	engine := routing.NewDecisionEngine([]string{"localhost"}, pool)
	rules := []model.RoutingRule{
		{
			ID: "direct", Name: "direct corp", Enabled: true, Priority: 0,
			HostType: routing.RuleHostSuffix, HostValue: "example.com",
			Ports:   []model.RoutingPortRange{{Start: 443, End: 443}},
			Network: routing.RuleNetworkAny, Action: string(routing.DecisionDirect),
		},
		{
			ID: "platform", Name: "platform override", Enabled: true, Priority: 1,
			HostType: routing.RuleHostGlob, HostValue: "*.example.com",
			Network: "tcp", Action: routing.RuleActionPlatform, PlatformID: platID,
			OverrideExplicitPlatform: true,
		},
	}
	if err := engine.ReplaceRules(rules); err != nil {
		t.Fatalf("ReplaceRules: %v", err)
	}

	bypass, err := engine.Decide(routing.DecisionInput{Target: "localhost:80"})
	if err != nil || bypass.Action != routing.DecisionDirect || bypass.Reason != "environment_bypass" {
		t.Fatalf("bypass decision = %+v, err=%v", bypass, err)
	}
	direct, err := engine.Decide(routing.DecisionInput{
		PlatformName: platName, Target: "api.example.com:443", RequiredNetwork: "tcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct.Action != routing.DecisionRouted || direct.PlatformID != platID || direct.MatchedRuleID != "platform" {
		t.Fatalf("explicit platform should skip non-override rule then match override: %+v", direct)
	}
	nonExplicit, err := engine.Decide(routing.DecisionInput{Target: "api.example.com:443"})
	if err != nil || nonExplicit.Action != routing.DecisionDirect || nonExplicit.MatchedRuleID != "direct" {
		t.Fatalf("first applicable rule decision = %+v, err=%v", nonExplicit, err)
	}
}

func TestDecisionEngine_CIDRRequiresLiteralIPAndUnknownPortSkipsPortRule(t *testing.T) {
	pool, _ := setupPool(t)
	engine := routing.NewDecisionEngine(nil, pool)
	if err := engine.ReplaceRules([]model.RoutingRule{{
		ID: "cidr", Name: "cidr", Enabled: true, HostType: routing.RuleHostCIDR,
		HostValue: "10.0.0.0/8", Ports: []model.RoutingPortRange{{Start: 80, End: 90}},
		Network: routing.RuleNetworkAny, Action: string(routing.DecisionReject),
	}}); err != nil {
		t.Fatal(err)
	}
	matched, _ := engine.Decide(routing.DecisionInput{Target: "10.1.2.3:85"})
	if matched.Action != routing.DecisionReject {
		t.Fatalf("literal IP should match: %+v", matched)
	}
	for _, target := range []string{"service.internal:85", "10.1.2.3"} {
		decision, err := engine.Decide(routing.DecisionInput{Target: target})
		if err != nil || decision.Action != routing.DecisionRouted {
			t.Fatalf("target %q should not match: %+v err=%v", target, decision, err)
		}
	}
}
