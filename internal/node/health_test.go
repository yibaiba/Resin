package node

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTCPHealth_StaleValidationCannotOverrideSuccess(t *testing.T) {
	entry := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	transition := entry.RecordTCPHealth(false, 1)
	if transition.Validation == nil {
		t.Fatal("threshold failure did not create confirmation")
	}

	entry.RecordTCPHealth(true, 1)
	stale := entry.CompleteTCPValidation(*transition.Validation, false, time.Now().UnixNano())
	if stale.Changed() {
		t.Fatalf("stale validation changed state: %+v", stale)
	}
	state := entry.TCPHealth()
	if state.FailureCount != 0 || state.CircuitOpenSince != 0 ||
		state.ValidationPending != TCPValidationNone {
		t.Fatalf("success state was overwritten: %+v", state)
	}
}

func TestTCPHealth_ValidationGenerationIsUniqueAcrossEntries(t *testing.T) {
	first := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	second := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	firstValidation := first.RecordTCPHealth(false, 1).Validation
	secondValidation := second.RecordTCPHealth(false, 1).Validation

	if firstValidation == nil || secondValidation == nil {
		t.Fatal("threshold failures did not create confirmations")
	}
	if firstValidation.Generation == 0 || secondValidation.Generation == 0 {
		t.Fatal("validation generation must be non-zero")
	}
	if firstValidation.Generation == secondValidation.Generation {
		t.Fatalf("validation generations collided: %d", firstValidation.Generation)
	}
}

func TestTCPHealth_ConcurrentSnapshotsStayConsistent(t *testing.T) {
	entry := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	const iterations = 10_000

	var invalid atomic.Bool
	var readers sync.WaitGroup
	done := make(chan struct{})
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				state := entry.TCPHealth()
				if state.ValidationPending == TCPValidationConfirmation && state.CircuitOpenSince != 0 {
					invalid.Store(true)
				}
				if state.ValidationPending == TCPValidationRecovery && state.CircuitOpenSince == 0 {
					invalid.Store(true)
				}
			}
		}()
	}

	for i := 0; i < iterations; i++ {
		confirmation := entry.RecordTCPHealth(false, 1)
		if confirmation.Validation == nil {
			t.Fatalf("iteration %d: confirmation missing", i)
		}
		entry.CompleteTCPValidation(*confirmation.Validation, false, int64(i+1))
		recovery := entry.BeginTCPValidation(TCPValidationRecovery, 1)
		if recovery.Validation == nil {
			t.Fatalf("iteration %d: recovery missing", i)
		}
		entry.CompleteTCPValidation(*recovery.Validation, true, 0)
	}
	close(done)
	readers.Wait()
	if invalid.Load() {
		t.Fatal("reader observed an invalid TCP health snapshot")
	}
}

func TestUDPHealth_ConcurrentSnapshotsStayConsistent(t *testing.T) {
	entry := NewNodeEntry(Hash{}, nil, time.Now(), 0)
	const iterations = 10_000

	var invalid atomic.Bool
	var readers sync.WaitGroup
	done := make(chan struct{})
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				state := entry.UDPHealthState()
				if state.CircuitOpenSince != 0 && state.FailureCount < 2 {
					invalid.Store(true)
				}
			}
		}()
	}

	for i := 0; i < iterations; i++ {
		entry.RecordUDPHealth(false, 2, int64(i+1))
		entry.RecordUDPHealth(false, 2, int64(i+1))
		entry.RecordUDPHealth(true, 2, 0)
	}
	close(done)
	readers.Wait()
	if invalid.Load() {
		t.Fatal("reader observed an invalid UDP health snapshot")
	}
}
