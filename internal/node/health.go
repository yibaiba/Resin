package node

// TCPValidationKind identifies an independent TCP health validation.
type TCPValidationKind string

const (
	TCPValidationNone         TCPValidationKind = "none"
	TCPValidationConfirmation TCPValidationKind = "confirmation"
	TCPValidationRecovery     TCPValidationKind = "recovery"
)

// TCPValidation identifies one process-local validation attempt.
type TCPValidation struct {
	Generation uint64
	Kind       TCPValidationKind
}

// TCPHealthState is an immutable snapshot published through one atomic pointer.
type TCPHealthState struct {
	FailureCount         int32
	CircuitOpenSince     int64
	ValidationGeneration uint64
	ValidationPending    TCPValidationKind
}

// UDPHealthState is the immutable UDP health snapshot.
type UDPHealthState struct {
	FailureCount     int32
	CircuitOpenSince int64
}

// TCPHealthTransition describes one atomic TCP state transition.
type TCPHealthTransition struct {
	Previous   TCPHealthState
	Current    TCPHealthState
	Validation *TCPValidation
}

func (t TCPHealthTransition) Changed() bool {
	return t.Previous != t.Current
}

func (t TCPHealthTransition) PersistentChanged() bool {
	return t.Previous.FailureCount != t.Current.FailureCount ||
		t.Previous.CircuitOpenSince != t.Current.CircuitOpenSince
}

func (t TCPHealthTransition) CircuitChanged() bool {
	return t.Previous.CircuitOpenSince != t.Current.CircuitOpenSince
}

// UDPHealthTransition describes one atomic UDP state transition.
type UDPHealthTransition struct {
	Previous UDPHealthState
	Current  UDPHealthState
}

func (t UDPHealthTransition) PersistentChanged() bool {
	return t.Previous != t.Current
}

func (t UDPHealthTransition) CircuitChanged() bool {
	return t.Previous.CircuitOpenSince != t.Current.CircuitOpenSince
}

func (e *NodeEntry) TCPHealth() TCPHealthState {
	if e == nil {
		return TCPHealthState{}
	}
	state := e.tcpHealth.Load()
	if state == nil {
		return TCPHealthState{}
	}
	return *state
}

func (e *NodeEntry) UDPHealthState() UDPHealthState {
	if e == nil {
		return UDPHealthState{}
	}
	state := e.udpHealth.Load()
	if state == nil {
		return UDPHealthState{}
	}
	return *state
}

// RestoreTCPHealth rebuilds persisted TCP fields and resets process-local validation state.
func (e *NodeEntry) RestoreTCPHealth(failureCount int32, circuitOpenSince int64) {
	e.tcpHealth.Store(&TCPHealthState{
		FailureCount:      failureCount,
		CircuitOpenSince:  circuitOpenSince,
		ValidationPending: TCPValidationNone,
	})
}

// RestoreUDPHealth rebuilds the persisted UDP snapshot.
func (e *NodeEntry) RestoreUDPHealth(failureCount int32, circuitOpenSince int64) {
	e.udpHealth.Store(&UDPHealthState{
		FailureCount:     failureCount,
		CircuitOpenSince: circuitOpenSince,
	})
}

// RecordTCPHealth applies ordinary success/failure feedback. Threshold failures
// request confirmation but do not open the circuit themselves.
func (e *NodeEntry) RecordTCPHealth(success bool, threshold int) TCPHealthTransition {
	return e.updateTCPHealth(func(current TCPHealthState) (TCPHealthState, *TCPValidation) {
		if success {
			return healthyTCPState(current.ValidationGeneration), nil
		}
		if current.CircuitOpenSince != 0 || current.ValidationPending != TCPValidationNone {
			return current, nil
		}

		next := current
		if threshold <= 0 || int(next.FailureCount) < threshold {
			next.FailureCount = incrementFailureCount(next.FailureCount)
		}
		if threshold <= 0 || int(next.FailureCount) < threshold {
			return next, nil
		}

		validation := TCPValidation{
			Generation: nextValidationGeneration(current.ValidationGeneration),
			Kind:       TCPValidationConfirmation,
		}
		next.ValidationGeneration = validation.Generation
		next.ValidationPending = validation.Kind
		return next, &validation
	})
}

// BeginTCPValidation rebuilds a pending confirmation or recovery, such as at startup.
func (e *NodeEntry) BeginTCPValidation(kind TCPValidationKind, threshold int) TCPHealthTransition {
	return e.updateTCPHealth(func(current TCPHealthState) (TCPHealthState, *TCPValidation) {
		if current.ValidationPending != TCPValidationNone || !canBeginValidation(current, kind, threshold) {
			return current, nil
		}
		validation := TCPValidation{
			Generation: nextValidationGeneration(current.ValidationGeneration),
			Kind:       kind,
		}
		next := current
		next.ValidationGeneration = validation.Generation
		next.ValidationPending = kind
		return next, &validation
	})
}

// CancelTCPValidation clears an exact pending generation after enqueue failure.
func (e *NodeEntry) CancelTCPValidation(validation TCPValidation) TCPHealthTransition {
	return e.updateTCPHealth(func(current TCPHealthState) (TCPHealthState, *TCPValidation) {
		if !matchesValidation(current, validation) {
			return current, nil
		}
		next := current
		next.ValidationPending = TCPValidationNone
		return next, nil
	})
}

// CompleteTCPValidation applies only the currently pending generation.
func (e *NodeEntry) CompleteTCPValidation(
	validation TCPValidation,
	success bool,
	circuitOpenedAt int64,
) TCPHealthTransition {
	return e.updateTCPHealth(func(current TCPHealthState) (TCPHealthState, *TCPValidation) {
		if !matchesValidation(current, validation) {
			return current, nil
		}
		if success {
			return healthyTCPState(current.ValidationGeneration), nil
		}

		next := current
		next.ValidationPending = TCPValidationNone
		if validation.Kind == TCPValidationConfirmation {
			next.CircuitOpenSince = circuitOpenedAt
		}
		return next, nil
	})
}

// RecordUDPHealth keeps the existing direct-threshold UDP semantics.
func (e *NodeEntry) RecordUDPHealth(success bool, threshold int, circuitOpenedAt int64) UDPHealthTransition {
	return e.updateUDPHealth(func(current UDPHealthState) UDPHealthState {
		if success {
			return UDPHealthState{}
		}
		next := current
		next.FailureCount = incrementFailureCount(next.FailureCount)
		if threshold > 0 && int(next.FailureCount) >= threshold && next.CircuitOpenSince == 0 {
			next.CircuitOpenSince = circuitOpenedAt
		}
		return next
	})
}

func (e *NodeEntry) updateTCPHealth(
	update func(TCPHealthState) (TCPHealthState, *TCPValidation),
) TCPHealthTransition {
	for {
		previousPtr := e.tcpHealth.Load()
		previous := TCPHealthState{}
		if previousPtr != nil {
			previous = *previousPtr
		}
		current, validation := update(previous)
		transition := TCPHealthTransition{Previous: previous, Current: current, Validation: validation}
		if previous == current {
			return transition
		}
		currentPtr := new(TCPHealthState)
		*currentPtr = current
		if e.tcpHealth.CompareAndSwap(previousPtr, currentPtr) {
			return transition
		}
	}
}

func (e *NodeEntry) updateUDPHealth(update func(UDPHealthState) UDPHealthState) UDPHealthTransition {
	for {
		previousPtr := e.udpHealth.Load()
		previous := UDPHealthState{}
		if previousPtr != nil {
			previous = *previousPtr
		}
		current := update(previous)
		transition := UDPHealthTransition{Previous: previous, Current: current}
		if previous == current {
			return transition
		}
		currentPtr := new(UDPHealthState)
		*currentPtr = current
		if e.udpHealth.CompareAndSwap(previousPtr, currentPtr) {
			return transition
		}
	}
}

func canBeginValidation(current TCPHealthState, kind TCPValidationKind, threshold int) bool {
	switch kind {
	case TCPValidationConfirmation:
		return current.CircuitOpenSince == 0 && threshold > 0 && int(current.FailureCount) >= threshold
	case TCPValidationRecovery:
		return current.CircuitOpenSince != 0
	default:
		return false
	}
}

func matchesValidation(current TCPHealthState, validation TCPValidation) bool {
	return validation.Kind != TCPValidationNone &&
		current.ValidationGeneration == validation.Generation &&
		current.ValidationPending == validation.Kind
}

func healthyTCPState(generation uint64) TCPHealthState {
	return TCPHealthState{
		ValidationGeneration: generation,
		ValidationPending:    TCPValidationNone,
	}
}

func nextValidationGeneration(current uint64) uint64 {
	next := current + 1
	if next == 0 {
		return 1
	}
	return next
}

func incrementFailureCount(current int32) int32 {
	const maxFailureCount = int32(^uint32(0) >> 1)
	if current == maxFailureCount {
		return current
	}
	return current + 1
}
