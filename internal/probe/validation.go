package probe

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Resinat/Resin/internal/diagnosticlog"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/topology"
)

var (
	ErrManagerStopped   = errors.New("probe manager stopped")
	ErrFetcherMissing   = errors.New("probe fetcher not configured")
	ErrValidationQueue  = errors.New("validation queue rejected task")
	ErrValidationTarget = errors.New("validation target unavailable")
)

type tcpValidationFetchResult struct {
	body    []byte
	latency time.Duration
}

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
		if errors.Is(err, ErrValidationTarget) {
			return true
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
	body, latency, _, fetchErr := m.fetchEgress(task.key.hash)
	if fetchErr != nil {
		if isSkippedProbe(fetchErr) {
			m.pool.CancelTCPValidation(task.key.hash, validation)
			return
		}
		m.noteChainProbeFailure(task.key.hash)
		m.completeFailedTCPValidation(task.key.hash, entry, validation, fetchErr)
		return
	}
	m.completeSuccessfulTCPValidation(task.key.hash, entry, validation, tcpValidationFetchResult{
		body: body, latency: latency,
	})
}

func (m *ProbeManager) completeFailedTCPValidation(
	hash node.Hash,
	entry *node.NodeEntry,
	validation node.TCPValidation,
	fetchErr error,
) {
	applied := m.pool.CompleteTCPValidationWithEffect(hash, topology.TCPValidationCompletion{
		Validation:    validation,
		Success:       false,
		ExpectedEntry: entry,
		ApplyIfCurrent: func(current *node.NodeEntry) {
			m.pool.UpdateNodeEgressIPForEntry(hash, current, nil, nil)
		},
	})
	if !applied {
		logValidationDrop(hash, validation, "stale after fetch")
		return
	}
	logValidationTransportError(hash, validation, fetchErr)
}

func (m *ProbeManager) completeSuccessfulTCPValidation(
	hash node.Hash,
	entry *node.NodeEntry,
	validation node.TCPValidation,
	result tcpValidationFetchResult,
) {
	ip, loc, traceErr := ParseEgressBody(result.body)
	applied := m.pool.CompleteTCPValidationWithEffect(hash, topology.TCPValidationCompletion{
		Validation:    validation,
		Success:       true,
		ExpectedEntry: entry,
		ApplyIfCurrent: func(current *node.NodeEntry) {
			if result.latency > 0 {
				m.pool.RecordLatencyForEntry(hash, current, egressTraceDomain, &result.latency)
			}
			if traceErr != nil {
				m.pool.UpdateNodeEgressIPForEntry(hash, current, nil, nil)
				return
			}
			m.pool.UpdateNodeEgressIPForEntry(hash, current, &ip, loc)
		},
	})
	if !applied {
		logValidationDrop(hash, validation, "stale after fetch")
		return
	}
	if traceErr != nil {
		diagnosticlog.Print("[node-health]",
			diagnosticlog.F("event", "validation_trace_parse_failed"),
			diagnosticlog.F("source", string(validation.Kind)),
			diagnosticlog.F("node_hash", hash.Hex()),
			diagnosticlog.F("generation", strconv.FormatUint(validation.Generation, 10)),
			diagnosticlog.F("error", traceErr.Error()),
		)
	}
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
	diagnosticlog.Print("[node-health]",
		diagnosticlog.F("event", "validation_dropped"),
		diagnosticlog.F("source", string(validation.Kind)),
		diagnosticlog.F("node_hash", hash.Hex()),
		diagnosticlog.F("generation", strconv.FormatUint(validation.Generation, 10)),
		diagnosticlog.F("reason", reason),
	)
}

func logValidationTransportError(hash node.Hash, validation node.TCPValidation, err error) {
	diagnosticlog.Print("[node-health]",
		diagnosticlog.F("event", "validation_transport_failed"),
		diagnosticlog.F("source", string(validation.Kind)),
		diagnosticlog.F("node_hash", hash.Hex()),
		diagnosticlog.F("generation", strconv.FormatUint(validation.Generation, 10)),
		diagnosticlog.F("error", err.Error()),
	)
}
