package probe

import (
	"errors"
	"fmt"
	"log"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/topology"
)

var (
	ErrManagerStopped   = errors.New("probe manager stopped")
	ErrFetcherMissing   = errors.New("probe fetcher not configured")
	ErrValidationQueue  = errors.New("validation queue rejected task")
	ErrValidationTarget = errors.New("validation target unavailable")
)

// EnqueueTCPValidation adds one generation-scoped high-priority validation.
func (m *ProbeManager) EnqueueTCPValidation(request topology.TCPValidationRequest) error {
	if m == nil || m.pool == nil {
		return fmt.Errorf("%w: node pool missing", ErrValidationTarget)
	}
	select {
	case <-m.stopCh:
		return ErrManagerStopped
	default:
	}
	if m.fetcher == nil {
		return ErrFetcherMissing
	}
	entry, ok := m.pool.GetEntry(request.Hash)
	if !ok {
		return fmt.Errorf("%w: node %s not found", ErrValidationTarget, request.Hash.Hex())
	}
	if entry.Outbound.Load() == nil {
		return fmt.Errorf("%w: node %s outbound not ready", ErrValidationTarget, request.Hash.Hex())
	}

	kind, err := taskKindForValidation(request.Validation)
	if err != nil {
		return err
	}
	key := probeTaskKey{
		hash:       request.Hash,
		kind:       kind,
		generation: request.Validation.Generation,
	}
	if !m.enqueueProbeKey(key, probePriorityHigh) {
		return fmt.Errorf("%w: node %s generation %d", ErrValidationQueue, request.Hash.Hex(), request.Validation.Generation)
	}
	return nil
}

// EnqueueTCPRecoveryValidations rebuilds process-local pending work from the
// persisted failure count and circuit state.
func (m *ProbeManager) EnqueueTCPRecoveryValidations() error {
	if m == nil || m.pool == nil {
		return fmt.Errorf("%w: node pool missing", ErrValidationTarget)
	}
	var scanErrors []error
	m.pool.RangeNodes(func(hash node.Hash, entry *node.NodeEntry) bool {
		state := entry.TCPHealth()
		var err error
		switch {
		case state.CircuitOpenSince != 0:
			err = m.pool.BeginTCPValidation(hash, node.TCPValidationRecovery, topology.HealthSourceRecovery)
		case state.FailureCount > 0:
			err = m.pool.BeginTCPValidation(hash, node.TCPValidationConfirmation, topology.HealthSourceConfirmation)
		}
		if err != nil {
			scanErrors = append(scanErrors, fmt.Errorf("node %s: %w", hash.Hex(), err))
		}
		return true
	})
	return errors.Join(scanErrors...)
}

func (m *ProbeManager) executeTCPValidation(task probeTask) {
	validation, ok := validationFromTask(task)
	if !ok {
		return
	}
	entry, exists := m.pool.GetEntry(task.key.hash)
	if !exists {
		logValidationDrop(task.key.hash, validation, "node removed")
		return
	}
	if !validationMatches(entry.TCPHealth(), validation) {
		logValidationDrop(task.key.hash, validation, "stale generation")
		return
	}
	if entry.Outbound.Load() == nil || m.fetcher == nil {
		m.pool.CancelTCPValidation(task.key.hash, validation)
		logValidationDrop(task.key.hash, validation, "validation target unavailable")
		return
	}

	if m.onProbeEvent != nil {
		m.onProbeEvent(string(validation.Kind))
	}
	body, latency, fetchErr := m.fetcher(task.key.hash, egressTraceURL)
	if fetchErr != nil {
		m.pool.UpdateNodeEgressIP(task.key.hash, nil, nil)
		m.pool.CompleteTCPValidation(task.key.hash, validation, false)
		return
	}
	if latency > 0 {
		m.pool.RecordLatency(task.key.hash, egressTraceDomain, &latency)
	}
	updateEgressFromValidation(m.pool, task.key.hash, body)
	m.pool.CompleteTCPValidation(task.key.hash, validation, true)
}

func updateEgressFromValidation(pool *topology.GlobalNodePool, hash node.Hash, body []byte) {
	ip, loc, err := ParseCloudflareTrace(body)
	if err != nil {
		pool.UpdateNodeEgressIP(hash, nil, nil)
		return
	}
	pool.UpdateNodeEgressIP(hash, &ip, loc)
}

func (m *ProbeManager) cancelDroppedValidation(task probeTask, reason string) {
	validation, ok := validationFromTask(task)
	if !ok || m == nil || m.pool == nil {
		return
	}
	m.pool.CancelTCPValidation(task.key.hash, validation)
	logValidationDrop(task.key.hash, validation, reason)
}

func validationFromTask(task probeTask) (node.TCPValidation, bool) {
	validation := node.TCPValidation{Generation: task.key.generation}
	switch task.key.kind {
	case probeTaskKindConfirmation:
		validation.Kind = node.TCPValidationConfirmation
	case probeTaskKindRecovery:
		validation.Kind = node.TCPValidationRecovery
	default:
		return node.TCPValidation{}, false
	}
	return validation, validation.Generation != 0
}

func taskKindForValidation(validation node.TCPValidation) (probeTaskKind, error) {
	if validation.Generation == 0 {
		return 0, fmt.Errorf("invalid validation generation 0")
	}
	switch validation.Kind {
	case node.TCPValidationConfirmation:
		return probeTaskKindConfirmation, nil
	case node.TCPValidationRecovery:
		return probeTaskKindRecovery, nil
	default:
		return 0, fmt.Errorf("invalid validation kind %q", validation.Kind)
	}
}

func validationMatches(state node.TCPHealthState, validation node.TCPValidation) bool {
	return state.ValidationGeneration == validation.Generation && state.ValidationPending == validation.Kind
}

func logValidationDrop(hash node.Hash, validation node.TCPValidation, reason string) {
	log.Printf(
		"[node-health] event=validation_dropped source=%s node_hash=%s generation=%d reason=%q",
		validation.Kind,
		hash.Hex(),
		validation.Generation,
		reason,
	)
}
