package proxy

import (
	"log"
	"sync"
	"time"

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
		switch outcome {
		case passiveHealthSuccess:
			recordPassiveResult(f.health, f.route, true, f.requestID)
		case passiveHealthFailure:
			recordPassiveResult(f.health, f.route, false, f.requestID)
		}
	})
}

func recordPassiveResult(health HealthRecorder, route routing.RouteResult, success bool, requestID string) {
	if health == nil {
		return
	}
	if recorder, ok := health.(passiveRequestHealthRecorder); ok {
		if err := recorder.RecordPassiveResultWithRequest(route.PlatformID, route.NodeHash, success, requestID); err != nil {
			log.Printf("[node-health] event=passive_feedback_error source=passive request_id=%q node_hash=%s error=%q", requestID, route.NodeHash.Hex(), err)
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
