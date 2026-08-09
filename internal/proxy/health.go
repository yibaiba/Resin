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
		f.recorded.Store(uint32(outcome) + 1)
		switch outcome {
		case passiveHealthSuccess:
			recordPassiveResult(f.health, f.route, true, f.requestID)
		case passiveHealthFailure:
			recordPassiveResult(f.health, f.route, false, f.requestID)
		}
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

func recordPassiveResult(health HealthRecorder, route routing.RouteResult, success bool, requestID string) {
	if health == nil {
		return
	}
	if recorder, ok := health.(passiveRequestHealthRecorder); ok {
		if err := recorder.RecordPassiveResultWithRequest(route.PlatformID, route.NodeHash, success, requestID); err != nil {
			diagnosticlog.Print("[node-health]",
				diagnosticlog.F("event", "passive_feedback_error"),
				diagnosticlog.F("source", "passive"),
				diagnosticlog.F("request_id", requestID),
				diagnosticlog.F("node_hash", route.NodeHash.Hex()),
				diagnosticlog.F("error", err.Error()),
			)
		}
		return
	}
	if recorder, ok := health.(passiveHealthRecorder); ok {
		recorder.RecordPassiveResult(route.PlatformID, route.NodeHash, success)
		return
	}
	health.RecordResult(route.NodeHash, success)
}

func recordPassiveUDPResultAsync(health HealthRecorder, route routing.RouteResult, success bool) {
	if health == nil {
		return
	}
	if recorder, ok := health.(passiveNetworkHealthRecorder); ok {
		go recorder.RecordPassiveNetworkResult(route.PlatformID, route.NodeHash, N.NetworkUDP, success)
	}
}
