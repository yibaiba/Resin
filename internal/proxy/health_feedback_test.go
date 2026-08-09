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
	})

	t.Run("failure wins", func(t *testing.T) {
		recorder := &mockHealthRecorder{}
		feedback := newRequestHealthFeedback(recorder, route, "request-failure")
		feedback.record(passiveHealthFailure)
		feedback.record(passiveHealthSuccess)
		if recorder.resultCalls.Load() != 1 || recorder.lastSuccess.Load() != 0 {
			t.Fatalf("calls=%d success=%d", recorder.resultCalls.Load(), recorder.lastSuccess.Load())
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
	})
}
