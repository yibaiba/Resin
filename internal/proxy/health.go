package proxy

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Resinat/Resin/internal/diagnosticlog"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	N "github.com/sagernet/sing/common/network"
)

// HealthRecorder abstracts passive health feedback reporting.
// topology.GlobalNodePool satisfies this interface.
type HealthRecorder interface {
	RecordResult(hash node.Hash, success bool)
	RecordLatency(hash node.Hash, rawTarget string, latency *time.Duration)
}

type passiveHealthRecorder interface {
	RecordPassiveResult(platformID string, hash node.Hash, success bool)
}

type passiveRequestHealthRecorder interface {
	RecordPassiveResultWithRequest(platformID string, hash node.Hash, success bool, requestID string) error
}

type passiveRequestHealthStatusRecorder interface {
	RecordPassiveResultWithRequestStatus(
		platformID string,
		hash node.Hash,
		success bool,
		requestID string,
	) (bool, error)
}

type passiveNetworkHealthRecorder interface {
	RecordPassiveNetworkResult(platformID string, hash node.Hash, network string, success bool)
}

type passiveHealthOutcome uint8

const (
	passiveHealthNeutral passiveHealthOutcome = iota
	passiveHealthSuccess
	passiveHealthFailure
)

type requestHealthFeedback struct {
	once      sync.Once
	recorded  atomic.Uint32
	health    HealthRecorder
	route     routing.RouteResult
	requestID string
}

func newRequestHealthFeedback(
	health HealthRecorder,
	route routing.RouteResult,
	requestID string,
) *requestHealthFeedback {
	return &requestHealthFeedback{health: health, route: route, requestID: requestID}
}

func (f *requestHealthFeedback) record(outcome passiveHealthOutcome) {
	if f == nil {
		return
	}
	f.once.Do(func() {
		actual := outcome
		switch outcome {
		case passiveHealthSuccess:
			if !recordPassiveResult(f.health, f.route, true, f.requestID) {
				actual = passiveHealthNeutral
			}
		case passiveHealthFailure:
			if !recordPassiveResult(f.health, f.route, false, f.requestID) {
				actual = passiveHealthNeutral
			}
		}
		f.recorded.Store(uint32(actual) + 1)
	})
}

func (f *requestHealthFeedback) outcome() passiveHealthOutcome {
	if f == nil {
		return passiveHealthNeutral
	}
	recorded := f.recorded.Load()
	if recorded == 0 {
		return passiveHealthNeutral
	}
	return passiveHealthOutcome(recorded - 1)
}

func recordPassiveResult(health HealthRecorder, route routing.RouteResult, success bool, requestID string) bool {
	if health == nil {
		return false
	}
	if recorder, ok := health.(passiveRequestHealthStatusRecorder); ok {
		accepted, err := recorder.RecordPassiveResultWithRequestStatus(
			route.PlatformID, route.NodeHash, success, requestID,
		)
		if err != nil {
			logPassiveFeedbackError(route.NodeHash, requestID, err)
		}
		return accepted
	}
	if recorder, ok := health.(passiveRequestHealthRecorder); ok {
		if err := recorder.RecordPassiveResultWithRequest(route.PlatformID, route.NodeHash, success, requestID); err != nil {
			logPassiveFeedbackError(route.NodeHash, requestID, err)
		}
		return true
	}
	if recorder, ok := health.(passiveHealthRecorder); ok {
		recorder.RecordPassiveResult(route.PlatformID, route.NodeHash, success)
		return true
	}
	health.RecordResult(route.NodeHash, success)
	return true
}

func logPassiveFeedbackError(hash node.Hash, requestID string, err error) {
	diagnosticlog.Print("[node-health]",
		diagnosticlog.F("event", "passive_feedback_error"),
		diagnosticlog.F("source", "passive"),
		diagnosticlog.F("request_id", requestID),
		diagnosticlog.F("node_hash", hash.Hex()),
		diagnosticlog.F("error", err.Error()),
	)
}

func recordPassiveUDPResultAsync(health HealthRecorder, route routing.RouteResult, success bool) {
	if health == nil {
		return
	}
	if recorder, ok := health.(passiveNetworkHealthRecorder); ok {
		go recorder.RecordPassiveNetworkResult(route.PlatformID, route.NodeHash, N.NetworkUDP, success)
	}
}
