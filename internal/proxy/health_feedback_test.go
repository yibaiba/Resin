package proxy

import (
	"testing"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

func TestRequestHealthFeedback_SubmitsAtMostOnce(t *testing.T) {
	route := routing.RouteResult{NodeHash: node.HashFromRawOptions([]byte(`{"type":"once"}`))}

	t.Run("success wins", func(t *testing.T) {
		recorder := &mockHealthRecorder{}
		feedback := newRequestHealthFeedback(recorder, route, "request-success")
		feedback.record(passiveHealthSuccess)
		feedback.record(passiveHealthFailure)
		feedback.record(passiveHealthNeutral)
		if recorder.resultCalls.Load() != 1 || recorder.lastSuccess.Load() != 1 {
			t.Fatalf("calls=%d success=%d", recorder.resultCalls.Load(), recorder.lastSuccess.Load())
		}
		if feedback.outcome() != passiveHealthSuccess {
			t.Fatalf("outcome = %d, want success", feedback.outcome())
		}
	})

	t.Run("failure wins", func(t *testing.T) {
		recorder := &mockHealthRecorder{}
		feedback := newRequestHealthFeedback(recorder, route, "request-failure")
		feedback.record(passiveHealthFailure)
		feedback.record(passiveHealthSuccess)
		if recorder.resultCalls.Load() != 1 || recorder.lastSuccess.Load() != 0 {
			t.Fatalf("calls=%d success=%d", recorder.resultCalls.Load(), recorder.lastSuccess.Load())
		}
		if feedback.outcome() != passiveHealthFailure {
			t.Fatalf("outcome = %d, want failure", feedback.outcome())
		}
	})

	t.Run("neutral is terminal", func(t *testing.T) {
		recorder := &mockHealthRecorder{}
		feedback := newRequestHealthFeedback(recorder, route, "request-neutral")
		feedback.record(passiveHealthNeutral)
		feedback.record(passiveHealthFailure)
		if recorder.resultCalls.Load() != 0 {
			t.Fatalf("neutral feedback recorded %d results", recorder.resultCalls.Load())
		}
		if feedback.outcome() != passiveHealthNeutral {
			t.Fatalf("outcome = %d, want neutral", feedback.outcome())
		}
	})

	t.Run("suppressed failure is neutral", func(t *testing.T) {
		recorder := &suppressedPassiveHealthRecorder{}
		feedback := newRequestHealthFeedback(recorder, route, "request-suppressed")
		feedback.record(passiveHealthFailure)
		if recorder.resultCalls.Load() != 0 {
			t.Fatalf("suppressed feedback recorded %d results", recorder.resultCalls.Load())
		}
		if feedback.outcome() != passiveHealthNeutral {
			t.Fatalf("outcome = %d, want neutral", feedback.outcome())
		}
	})
}

func TestRequestHealthFeedback_ChainedDialFailureIsNeutral(t *testing.T) {
	hash := node.HashFromRawOptions([]byte(`{"type":"chain-dial"}`))
	chained := routing.RouteResult{
		NodeHash:  hash,
		ChainHops: []routing.RouteChainHop{{PlatformID: "hop", NodeHash: hash}},
	}
	recorder := &mockHealthRecorder{}
	failover := &mockChainDialFailover{}
	feedback := newRequestHealthFeedback(recorder, chained, "request-chain-dial").
		bindChainFailover("acct-1", failover)
	feedback.recordDialFailure()
	if recorder.resultCalls.Load() != 0 {
		t.Fatalf("chained dial failure recorded %d results", recorder.resultCalls.Load())
	}
	if feedback.outcome() != passiveHealthNeutral {
		t.Fatalf("outcome = %d, want neutral", feedback.outcome())
	}
	if failover.calls != 1 || failover.account != "acct-1" || len(failover.route.ChainHops) != 1 {
		t.Fatalf("failover calls=%d account=%q hops=%d", failover.calls, failover.account, len(failover.route.ChainHops))
	}

	direct := newRequestHealthFeedback(&mockHealthRecorder{}, routing.RouteResult{NodeHash: hash}, "request-direct-dial")
	direct.recordDialFailure()
	if direct.outcome() != passiveHealthFailure {
		t.Fatalf("direct dial outcome = %d, want failure", direct.outcome())
	}
}

type mockChainDialFailover struct {
	calls   int
	account string
	route   routing.RouteResult
}

func (m *mockChainDialFailover) NoteChainDialFailure(account string, route routing.RouteResult) {
	m.calls++
	m.account = account
	m.route = route
}

type suppressedPassiveHealthRecorder struct {
	mockHealthRecorder
}

func (*suppressedPassiveHealthRecorder) RecordPassiveResultWithRequestStatus(
	string,
	node.Hash,
	bool,
	string,
) (bool, error) {
	return false, nil
}
