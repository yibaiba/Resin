package topology

import (
	"errors"
	"fmt"
	"time"

	"github.com/Resinat/Resin/internal/node"
	N "github.com/sagernet/sing/common/network"
)

// HealthSource identifies the origin of TCP health feedback.
type HealthSource string

const (
	HealthSourcePassive      HealthSource = "passive"
	HealthSourceLatencyProbe HealthSource = "latency_probe"
	HealthSourceEgressProbe  HealthSource = "egress_probe"
	HealthSourceConfirmation HealthSource = "confirmation"
	HealthSourceRecovery     HealthSource = "recovery"
)

// TCPValidationRequest is an independent egress validation requested by the pool.
type TCPValidationRequest struct {
	Hash       node.Hash
	Validation node.TCPValidation
	Source     HealthSource
	RequestID  string
}

type TCPValidationRequester func(request TCPValidationRequest) error

var ErrTCPValidationRequesterUnavailable = errors.New("tcp validation requester unavailable")

// SetTCPValidationRequester injects the active validation queue. Call before
// proxy handlers or background producers can record TCP failures.
func (p *GlobalNodePool) SetTCPValidationRequester(requester TCPValidationRequester) {
	p.tcpValidationRequester = requester
}

// RecordResult retains the existing boolean API for active health feedback.
func (p *GlobalNodePool) RecordResult(hash node.Hash, success bool) {
	_ = p.RecordTCPHealthResult(hash, HealthSourceEgressProbe, success, "")
}

// RecordNetworkResult records a transport-specific health result.
func (p *GlobalNodePool) RecordNetworkResult(hash node.Hash, network string, success bool) {
	if N.NetworkName(network) == N.NetworkUDP {
		_ = p.recordUDPResult(hash, success)
		return
	}
	_ = p.RecordTCPHealthResult(hash, HealthSourceEgressProbe, success, "")
}

// RecordTCPHealthResult atomically applies ordinary TCP feedback. A threshold
// failure queues confirmation while the node remains routable.
func (p *GlobalNodePool) RecordTCPHealthResult(
	hash node.Hash,
	source HealthSource,
	success bool,
	requestID string,
) error {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return fmt.Errorf("record TCP health: node %s not found", hash.Hex())
	}

	transition := entry.RecordTCPHealth(success, p.currentMaxConsecutiveFailures())
	p.applyTCPHealthTransition(hash, transition)
	event := ordinaryTCPHealthEvent(transition, success)
	if event != "" {
		outcome := "failure"
		if success {
			outcome = "success"
		}
		writeTCPHealthLog(tcpHealthLog{
			event: event, source: source, hash: hash, requestID: requestID,
			outcome: outcome, state: transition.Current,
		})
	}
	if transition.Validation == nil {
		return nil
	}
	return p.enqueueTCPValidation(entry, TCPValidationRequest{
		Hash:       hash,
		Validation: *transition.Validation,
		Source:     source,
		RequestID:  requestID,
	})
}

// BeginTCPValidation creates and queues one exact startup/recovery validation.
func (p *GlobalNodePool) BeginTCPValidation(
	hash node.Hash,
	kind node.TCPValidationKind,
	source HealthSource,
) error {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return fmt.Errorf("begin TCP validation: node %s not found", hash.Hex())
	}
	transition := entry.BeginTCPValidation(kind, p.currentMaxConsecutiveFailures())
	if transition.Validation == nil {
		return nil
	}
	writeTCPHealthLog(tcpHealthLog{
		event: "validation_requested", source: source, hash: hash,
		state: transition.Current,
	})
	return p.enqueueTCPValidation(entry, TCPValidationRequest{
		Hash:       hash,
		Validation: *transition.Validation,
		Source:     source,
	})
}

// CompleteTCPValidation applies an exact generation and ignores stale results.
func (p *GlobalNodePool) CompleteTCPValidation(
	hash node.Hash,
	validation node.TCPValidation,
	success bool,
) bool {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return false
	}
	transition := entry.CompleteTCPValidation(validation, success, time.Now().UnixNano())
	if !transition.Changed() {
		writeTCPHealthLog(tcpHealthLog{
			event: "validation_stale", source: HealthSource(validation.Kind), hash: hash,
			outcome: "ignored", state: transition.Current,
		})
		return false
	}
	p.applyTCPHealthTransition(hash, transition)
	outcome := "failure"
	if success {
		outcome = "success"
	}
	writeTCPHealthLog(tcpHealthLog{
		event:  validationResultEvent(validation, success),
		source: HealthSource(validation.Kind), hash: hash, outcome: outcome,
		state: transition.Current,
	})
	return true
}

// CancelTCPValidation clears an exact pending generation without changing
// persisted failure or circuit fields.
func (p *GlobalNodePool) CancelTCPValidation(hash node.Hash, validation node.TCPValidation) bool {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return false
	}
	transition := entry.CancelTCPValidation(validation)
	if transition.Changed() {
		writeTCPHealthLog(tcpHealthLog{
			event: "validation_canceled", source: HealthSource(validation.Kind), hash: hash,
			outcome: "neutral", state: transition.Current,
		})
	}
	return transition.Changed()
}

func (p *GlobalNodePool) enqueueTCPValidation(
	entry *node.NodeEntry,
	request TCPValidationRequest,
) error {
	if p.tcpValidationRequester == nil {
		state := entry.CancelTCPValidation(request.Validation).Current
		writeTCPHealthLog(tcpHealthLog{
			event: "validation_enqueue_failed", source: request.Source, hash: request.Hash,
			requestID: request.RequestID, outcome: "error", state: state,
			reason: ErrTCPValidationRequesterUnavailable.Error(),
		})
		return ErrTCPValidationRequesterUnavailable
	}
	if err := p.tcpValidationRequester(request); err != nil {
		state := entry.CancelTCPValidation(request.Validation).Current
		writeTCPHealthLog(tcpHealthLog{
			event: "validation_enqueue_failed", source: request.Source, hash: request.Hash,
			requestID: request.RequestID, outcome: "error", state: state, reason: err.Error(),
		})
		return fmt.Errorf("enqueue %s validation: %w", request.Validation.Kind, err)
	}
	writeTCPHealthLog(tcpHealthLog{
		event: "validation_enqueued", source: request.Source, hash: request.Hash,
		requestID: request.RequestID, outcome: "pending", state: entry.TCPHealth(),
	})
	return nil
}

func (p *GlobalNodePool) applyTCPHealthTransition(hash node.Hash, transition node.TCPHealthTransition) {
	if transition.CircuitChanged() {
		p.notifyAllPlatformsDirty(hash)
	}
	if transition.PersistentChanged() && p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
}

func (p *GlobalNodePool) recordUDPResult(hash node.Hash, success bool) bool {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return false
	}
	transition := entry.RecordUDPHealth(success, p.currentMaxConsecutiveFailures(), time.Now().UnixNano())
	if transition.CircuitChanged() {
		p.notifyAllPlatformsDirty(hash)
	}
	if transition.PersistentChanged() && p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
	return transition.PersistentChanged()
}

// RecordUDPProbeResult records an active UDP probe attempt and its health result.
func (p *GlobalNodePool) RecordUDPProbeResult(hash node.Hash, success bool) {
	entry, ok := p.nodes.Load(hash)
	if !ok {
		return
	}
	now := time.Now().UnixNano()
	entry.LastUDPProbeAttempt.Store(now)
	firstSuccess := false
	if success {
		firstSuccess = entry.LastUDPProbeSuccess.Swap(now) == 0
	}
	healthChanged := p.recordUDPResult(hash, success)
	if firstSuccess {
		p.notifyAllPlatformsDirty(hash)
	}
	if !healthChanged && p.onNodeDynamicChanged != nil {
		p.onNodeDynamicChanged(hash)
	}
}

// RecordPassiveResult records health feedback from user proxy traffic.
func (p *GlobalNodePool) RecordPassiveResult(platformID string, hash node.Hash, success bool) {
	_ = p.RecordPassiveResultWithRequest(platformID, hash, success, "")
}

// RecordPassiveResultWithRequest applies platform-aware TCP feedback and keeps
// the request ID available for diagnostic correlation.
func (p *GlobalNodePool) RecordPassiveResultWithRequest(
	platformID string,
	hash node.Hash,
	success bool,
	requestID string,
) error {
	if !success && p.passiveCircuitBreakerDisabled(platformID) {
		return nil
	}
	return p.RecordTCPHealthResult(hash, HealthSourcePassive, success, requestID)
}

// RecordPassiveNetworkResult records proxy-traffic feedback for one transport.
func (p *GlobalNodePool) RecordPassiveNetworkResult(
	platformID string,
	hash node.Hash,
	network string,
	success bool,
) {
	if !success && p.passiveCircuitBreakerDisabled(platformID) {
		return
	}
	if N.NetworkName(network) == N.NetworkUDP {
		_ = p.recordUDPResult(hash, success)
		return
	}
	_ = p.RecordPassiveResultWithRequest(platformID, hash, success, "")
}

func (p *GlobalNodePool) passiveCircuitBreakerDisabled(platformID string) bool {
	if platformID == "" {
		return false
	}
	p.platMu.RLock()
	defer p.platMu.RUnlock()
	plat, ok := p.platformByID[platformID]
	return ok && plat != nil && plat.PassiveCircuitBreakerDisabled
}

func (p *GlobalNodePool) currentMaxConsecutiveFailures() int {
	return p.maxConsecutiveFailures()
}
