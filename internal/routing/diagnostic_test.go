package routing

import (
	"reflect"
	"testing"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/platform"
)

func TestDecisionDiagnosticExplainsRuleEvaluation(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("diagnostic-platform", "Diagnostic", nil, nil)
	pool.addPlatform(plat)
	engine := NewDecisionEngine(nil, pool)
	err := engine.ReplaceRules([]model.RoutingRule{
		{ID: "disabled", Name: "Disabled", Enabled: false, Priority: 0, HostType: RuleHostAny, Network: RuleNetworkAny, Action: string(DecisionDirect)},
		{ID: "port", Name: "Port", Enabled: true, Priority: 1, HostType: RuleHostAny, Ports: []model.RoutingPortRange{{Start: 80, End: 80}}, Network: RuleNetworkAny, Action: string(DecisionReject)},
		{ID: "match", Name: "Match", Enabled: true, Priority: 2, HostType: RuleHostSuffix, HostValue: "example.com", Network: "tcp", Action: RuleActionPlatform, PlatformID: plat.ID},
	})
	if err != nil {
		t.Fatalf("ReplaceRules: %v", err)
	}
	decision, trace, err := engine.Diagnose(DecisionInput{Target: "api.example.com:443", RequiredNetwork: "tcp"})
	if err != nil || decision.PlatformID != plat.ID {
		t.Fatalf("diagnose decision: decision=%+v err=%v", decision, err)
	}
	wantReasons := []string{"disabled", "port_mismatch", "matched"}
	if len(trace.Rules) != len(wantReasons) {
		t.Fatalf("rule trace = %+v", trace.Rules)
	}
	for index, reason := range wantReasons {
		if trace.Rules[index].Reason != reason {
			t.Fatalf("rule %d reason = %q, want %q", index, trace.Rules[index].Reason, reason)
		}
	}
}

func TestRouteDiagnosticIsSeededAndDoesNotMutateState(t *testing.T) {
	fixture := newRotationFixture(t, true, true)
	beforeLease := fixture.router.ReadLease(model.LeaseKey{
		PlatformID: fixture.platform.ID, Account: "account",
	})
	beforeLoads := fixture.router.SnapshotGlobalIPLoad()
	beforeStates := fixture.router.states.Size()
	options := RouteOptions{
		PlatformName: fixture.platform.Name, Account: "new-account",
		Target: "example.com:443", RequiredNetwork: "tcp",
	}

	first, err := fixture.router.DiagnoseRoute(options, 42)
	if err != nil {
		t.Fatalf("first diagnostic: %v", err)
	}
	second, err := fixture.router.DiagnoseRoute(options, 42)
	if err != nil {
		t.Fatalf("second diagnostic: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same seed was not reproducible:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Selection == nil || first.Selection.NodeHash == "" || len(first.P2C) == 0 {
		t.Fatalf("diagnostic selection is incomplete: %+v", first)
	}
	if got := fixture.router.ReadLease(model.LeaseKey{PlatformID: fixture.platform.ID, Account: "new-account"}); got != nil {
		t.Fatalf("diagnostic created a lease: %+v", got)
	}
	afterLease := fixture.router.ReadLease(model.LeaseKey{PlatformID: fixture.platform.ID, Account: "account"})
	if !reflect.DeepEqual(beforeLease, afterLease) || !reflect.DeepEqual(beforeLoads, fixture.router.SnapshotGlobalIPLoad()) {
		t.Fatalf("diagnostic mutated lease state: before=%+v after=%+v", beforeLease, afterLease)
	}
	if fixture.router.states.Size() != beforeStates {
		t.Fatalf("diagnostic mutated platform state map")
	}
}

func TestRouteDiagnosticDoesNotMarkDeferredRotation(t *testing.T) {
	fixture := newRotationFixture(t, false, false)
	fixture.platform.RotationIntervalNs = 1
	fixture.platform.RotationStrategy = platform.RotationStrategyRequireNewIP
	before := fixture.router.ReadLease(model.LeaseKey{PlatformID: fixture.platform.ID, Account: "account"})
	diagnostic, err := fixture.router.DiagnoseRoute(RouteOptions{
		PlatformName: fixture.platform.Name, Account: "account", Target: "example.com:443",
	}, 9)
	if err != nil || diagnostic.Outcome != "rotation_deferred" {
		t.Fatalf("rotation diagnostic: result=%+v err=%v", diagnostic, err)
	}
	after := fixture.router.ReadLease(model.LeaseKey{PlatformID: fixture.platform.ID, Account: "account"})
	if !reflect.DeepEqual(before, after) || after.RotationDeferred {
		t.Fatalf("diagnostic persisted deferred metadata: before=%+v after=%+v", before, after)
	}
}
