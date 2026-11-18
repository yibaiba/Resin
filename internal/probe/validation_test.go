package probe

import (
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/topology"
)

func newValidationTestNode(t *testing.T, threshold int) (*topology.GlobalNodePool, node.Hash, *node.NodeEntry) {
	t.Helper()
	pool := topology.NewGlobalNodePool(topology.PoolConfig{
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return threshold },
	})
	hash := node.HashFromRawOptions([]byte(`{"type":"validation-test"}`))
	pool.AddNodeFromSub(hash, []byte(`{"type":"validation-test"}`), "sub1")
	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatal("validation test node missing")
	}
	storeOutbound(entry)
	pool.RecordResult(hash, true)
	return pool, hash, entry
}

func runNextQueuedTask(t *testing.T, manager *ProbeManager) {
	t.Helper()
	manager.taskQueue.mu.Lock()
	var task probeTask
	switch {
	case manager.taskQueue.high.len() > 0:
		task = manager.taskQueue.high.pop()
	case manager.taskQueue.normal.len() > 0:
		task = manager.taskQueue.normal.pop()
	default:
		manager.taskQueue.mu.Unlock()
		t.Fatal("expected queued probe task")
	}
	manager.taskQueue.mu.Unlock()
	state, ok := manager.markTaskRunning(task.key)
	if !ok {
		t.Fatal("queued task state missing")
	}
	manager.executeTask(task)
	manager.finishTask(task.key, state)
}

func TestTCPValidation_ConfirmationFetchSuccessIgnoresTraceParseFailure(t *testing.T) {
	pool, hash, entry := newValidationTestNode(t, 1)
	manager := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
			return []byte("trace without ip"), 10 * time.Millisecond, nil
		},
	})

	if err := pool.RecordTCPHealthResult(hash, topology.HealthSourcePassive, false, "request-1"); err != nil {
		t.Fatalf("record threshold failure: %v", err)
	}
	runNextQueuedTask(t, manager)
	state := entry.TCPHealth()
	if state.FailureCount != 0 || state.CircuitOpenSince != 0 ||
		state.ValidationPending != node.TCPValidationNone {
		t.Fatalf("confirmation success state: %+v", state)
	}
	if entry.GetEgressIP().IsValid() {
		t.Fatal("invalid trace unexpectedly updated egress IP")
	}
}

func TestTCPValidation_ConfirmationFetchFailureOpensCircuit(t *testing.T) {
	pool, hash, entry := newValidationTestNode(t, 1)
	manager := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
			return nil, 0, errors.New("connection refused")
		},
	})

	if err := pool.RecordTCPHealthResult(hash, topology.HealthSourceEgressProbe, false, ""); err != nil {
		t.Fatalf("record threshold failure: %v", err)
	}
	runNextQueuedTask(t, manager)
	state := entry.TCPHealth()
	if state.FailureCount != 1 || state.CircuitOpenSince == 0 ||
		state.ValidationPending != node.TCPValidationNone {
		t.Fatalf("confirmation failure state: %+v", state)
	}
}

func TestTCPValidation_RecoveryResultTransitions(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		pool, hash, entry := newValidationTestNode(t, 2)
		entry.RestoreTCPHealth(2, time.Now().Add(-time.Minute).UnixNano())
		manager := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: successfulValidationFetcher})

		if err := pool.BeginTCPValidation(hash, node.TCPValidationRecovery, topology.HealthSourceRecovery); err != nil {
			t.Fatalf("begin recovery: %v", err)
		}
		if state := entry.TCPHealth(); state.ValidationPending != node.TCPValidationRecovery || state.CircuitOpenSince == 0 {
			t.Fatalf("recovery pending state: %+v", state)
		}
		runNextQueuedTask(t, manager)
		if state := entry.TCPHealth(); state.FailureCount != 0 || state.CircuitOpenSince != 0 {
			t.Fatalf("recovery success state: %+v", state)
		}
	})

	t.Run("failure", func(t *testing.T) {
		pool, hash, entry := newValidationTestNode(t, 2)
		openedAt := time.Now().Add(-time.Minute).UnixNano()
		entry.RestoreTCPHealth(2, openedAt)
		manager := NewProbeManager(ProbeConfig{
			Pool: pool,
			Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
				return nil, 0, errors.New("timeout")
			},
		})

		if err := pool.BeginTCPValidation(hash, node.TCPValidationRecovery, topology.HealthSourceRecovery); err != nil {
			t.Fatalf("begin recovery: %v", err)
		}
		runNextQueuedTask(t, manager)
		state := entry.TCPHealth()
		if state.FailureCount != 2 || state.CircuitOpenSince != openedAt ||
			state.ValidationPending != node.TCPValidationNone {
			t.Fatalf("recovery failure state: %+v", state)
		}
	})
}

func TestTCPValidation_SkipCancelsWithoutHealthChange(t *testing.T) {
	pool, hash, entry := newValidationTestNode(t, 2)
	openedAt := time.Now().Add(-time.Minute).UnixNano()
	entry.RestoreTCPHealth(2, openedAt)
	manager := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
			return nil, 0, outbound.ErrSkipProbe
		},
	})
	if err := pool.BeginTCPValidation(hash, node.TCPValidationRecovery, topology.HealthSourceRecovery); err != nil {
		t.Fatalf("begin recovery: %v", err)
	}
	runNextQueuedTask(t, manager)
	state := entry.TCPHealth()
	if state.FailureCount != 2 || state.CircuitOpenSince != openedAt ||
		state.ValidationPending != node.TCPValidationNone {
		t.Fatalf("skip validation state: %+v", state)
	}
}

func TestTCPValidation_OrdinarySuccessInvalidatesQueuedGeneration(t *testing.T) {
	pool, hash, entry := newValidationTestNode(t, 1)
	var fetchCalls atomic.Int32
	manager := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
			fetchCalls.Add(1)
			return []byte("ip=198.51.100.10"), time.Millisecond, nil
		},
	})
	if err := pool.RecordTCPHealthResult(hash, topology.HealthSourcePassive, false, "request-2"); err != nil {
		t.Fatalf("record threshold failure: %v", err)
	}
	if err := manager.performLatencyProbe(hash, "https://www.gstatic.com/generate_204"); err != nil {
		t.Fatalf("ordinary latency success: %v", err)
	}

	runNextQueuedTask(t, manager)
	if fetchCalls.Load() != 1 {
		t.Fatalf("fetch calls = %d, stale validation should not fetch", fetchCalls.Load())
	}
	if state := entry.TCPHealth(); state.FailureCount != 0 || state.CircuitOpenSince != 0 {
		t.Fatalf("ordinary success state: %+v", state)
	}
}

func TestTCPValidation_OrdinarySuccessDuringFetchPreventsStaleMetadataWriteback(t *testing.T) {
	pool, hash, entry := newValidationTestNode(t, 1)
	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	manager := NewProbeManager(ProbeConfig{
		Pool: pool,
		Fetcher: func(node.Hash, string) ([]byte, time.Duration, error) {
			close(fetchStarted)
			<-releaseFetch
			return []byte("ip=198.51.100.20\nloc=CA"), 5 * time.Millisecond, nil
		},
	})
	if err := pool.RecordTCPHealthResult(hash, topology.HealthSourcePassive, false, "request-inflight"); err != nil {
		t.Fatalf("record threshold failure: %v", err)
	}
	state := entry.TCPHealth()
	task := probeTask{key: probeTaskKey{
		hash:       hash,
		kind:       probeTaskKindConfirmation,
		generation: state.ValidationGeneration,
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.executeTCPValidation(task)
	}()
	<-fetchStarted

	if err := pool.RecordTCPHealthResult(hash, topology.HealthSourceLatencyProbe, true, ""); err != nil {
		t.Fatalf("record newer success: %v", err)
	}
	newIP := netip.MustParseAddr("203.0.113.99")
	newRegion := "jp"
	newLatency := 99 * time.Millisecond
	pool.UpdateNodeEgressIP(hash, &newIP, &newRegion)
	pool.RecordLatency(hash, egressTraceDomain, &newLatency)
	close(releaseFetch)
	<-done

	if got := entry.GetEgressIP(); got != newIP {
		t.Fatalf("egress IP = %s, want newer %s", got, newIP)
	}
	if got := entry.GetEgressRegion(); got != newRegion {
		t.Fatalf("egress region = %q, want %q", got, newRegion)
	}
	stats, ok := entry.LatencyTable.GetDomainStats(egressTraceDomain)
	if !ok || stats.Ewma != newLatency {
		t.Fatalf("latency stats = %+v, ok=%v, want %s", stats, ok, newLatency)
	}
}

func TestTCPValidation_EnqueueFailuresCancelPending(t *testing.T) {
	t.Run("outbound missing", func(t *testing.T) {
		pool, hash, entry := newValidationTestNode(t, 1)
		entry.Outbound.Store(nil)
		NewProbeManager(ProbeConfig{Pool: pool, Fetcher: successfulValidationFetcher})
		err := pool.RecordTCPHealthResult(hash, topology.HealthSourcePassive, false, "request-3")
		if !errors.Is(err, ErrValidationTarget) {
			t.Fatalf("error = %v, want ErrValidationTarget", err)
		}
		assertValidationCanceled(t, entry)
	})

	t.Run("manager stopped", func(t *testing.T) {
		pool, hash, entry := newValidationTestNode(t, 1)
		manager := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: successfulValidationFetcher})
		manager.Stop()
		err := pool.RecordTCPHealthResult(hash, topology.HealthSourcePassive, false, "request-4")
		if !errors.Is(err, ErrManagerStopped) {
			t.Fatalf("error = %v, want ErrManagerStopped", err)
		}
		assertValidationCanceled(t, entry)
	})

	t.Run("queue full", func(t *testing.T) {
		pool, hash, entry := newValidationTestNode(t, 1)
		manager := NewProbeManager(ProbeConfig{
			Pool: pool, QueueCapacity: 1, Fetcher: successfulValidationFetcher,
		})
		if !manager.enqueueProbe(hash, probeTaskKindEgress, probePriorityHigh) {
			t.Fatal("failed to fill high-priority queue")
		}
		err := pool.RecordTCPHealthResult(hash, topology.HealthSourcePassive, false, "request-5")
		if !errors.Is(err, ErrValidationQueue) {
			t.Fatalf("error = %v, want ErrValidationQueue", err)
		}
		assertValidationCanceled(t, entry)
		manager.Stop()
	})
}

func TestTCPValidation_StopCancelsQueuedGeneration(t *testing.T) {
	pool, hash, entry := newValidationTestNode(t, 1)
	manager := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: successfulValidationFetcher})
	if err := pool.RecordTCPHealthResult(hash, topology.HealthSourcePassive, false, "request-6"); err != nil {
		t.Fatalf("record threshold failure: %v", err)
	}
	manager.Stop()
	assertValidationCanceled(t, entry)
}

func TestTCPValidation_RecoveryScanQueuesPersistedStates(t *testing.T) {
	pool, circuitHash, circuitEntry := newValidationTestNode(t, 2)
	unavailableHash := node.HashFromRawOptions([]byte(`{"type":"validation-unavailable"}`))
	pool.AddNodeFromSub(unavailableHash, []byte(`{"type":"validation-unavailable"}`), "sub1")
	unavailableEntry, _ := pool.GetEntry(unavailableHash)
	unavailableOpenedAt := time.Now().Add(-2 * time.Minute).UnixNano()
	unavailableEntry.RestoreTCPHealth(2, unavailableOpenedAt)
	confirmationHash := node.HashFromRawOptions([]byte(`{"type":"validation-confirmation"}`))
	pool.AddNodeFromSub(confirmationHash, []byte(`{"type":"validation-confirmation"}`), "sub1")
	confirmationEntry, _ := pool.GetEntry(confirmationHash)
	storeOutbound(confirmationEntry)
	belowThresholdHash := node.HashFromRawOptions([]byte(`{"type":"validation-below-threshold"}`))
	pool.AddNodeFromSub(belowThresholdHash, []byte(`{"type":"validation-below-threshold"}`), "sub1")
	belowThresholdEntry, _ := pool.GetEntry(belowThresholdHash)
	storeOutbound(belowThresholdEntry)
	circuitEntry.RestoreTCPHealth(2, time.Now().Add(-time.Minute).UnixNano())
	confirmationEntry.RestoreTCPHealth(2, 0)
	belowThresholdEntry.RestoreTCPHealth(1, 0)
	manager := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: successfulValidationFetcher})

	if err := manager.EnqueueTCPRecoveryValidations(); err != nil {
		t.Fatalf("recovery scan: %v", err)
	}
	if circuitEntry.TCPHealth().ValidationPending != node.TCPValidationRecovery {
		t.Fatalf("circuit node state: %+v", circuitEntry.TCPHealth())
	}
	if confirmationEntry.TCPHealth().ValidationPending != node.TCPValidationConfirmation {
		t.Fatalf("confirmation node state: %+v", confirmationEntry.TCPHealth())
	}
	if belowThresholdEntry.TCPHealth().ValidationPending != node.TCPValidationNone {
		t.Fatalf("below-threshold node state: %+v", belowThresholdEntry.TCPHealth())
	}
	unavailableState := unavailableEntry.TCPHealth()
	if unavailableState.CircuitOpenSince != unavailableOpenedAt ||
		unavailableState.ValidationPending != node.TCPValidationNone {
		t.Fatalf("unavailable node state: %+v", unavailableState)
	}
	runNextQueuedTask(t, manager)
	runNextQueuedTask(t, manager)
	for hash, entry := range map[node.Hash]*node.NodeEntry{
		circuitHash:      circuitEntry,
		confirmationHash: confirmationEntry,
	} {
		state := entry.TCPHealth()
		if state.FailureCount != 0 || state.CircuitOpenSince != 0 {
			t.Fatalf("node %s recovery state: %+v", hash.Hex(), state)
		}
	}
}

func TestTCPValidation_RecoveryScanReturnsSystemErrors(t *testing.T) {
	t.Run("manager stopped", func(t *testing.T) {
		pool, _, entry := newValidationTestNode(t, 1)
		entry.RestoreTCPHealth(1, time.Now().Add(-time.Minute).UnixNano())
		manager := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: successfulValidationFetcher})
		manager.Stop()

		err := manager.EnqueueTCPRecoveryValidations()
		if !errors.Is(err, ErrManagerStopped) {
			t.Fatalf("error = %v, want ErrManagerStopped", err)
		}
	})

	t.Run("queue full", func(t *testing.T) {
		pool, hash, entry := newValidationTestNode(t, 1)
		entry.RestoreTCPHealth(1, time.Now().Add(-time.Minute).UnixNano())
		manager := NewProbeManager(ProbeConfig{
			Pool: pool, QueueCapacity: 1, Fetcher: successfulValidationFetcher,
		})
		if !manager.enqueueProbe(hash, probeTaskKindEgress, probePriorityHigh) {
			t.Fatal("failed to fill high-priority queue")
		}

		err := manager.EnqueueTCPRecoveryValidations()
		if !errors.Is(err, ErrValidationQueue) {
			t.Fatalf("error = %v, want ErrValidationQueue", err)
		}
		manager.Stop()
	})
}

func successfulValidationFetcher(node.Hash, string) ([]byte, time.Duration, error) {
	return []byte("ip=198.51.100.9\nloc=US"), 5 * time.Millisecond, nil
}

func assertValidationCanceled(t *testing.T, entry *node.NodeEntry) {
	t.Helper()
	state := entry.TCPHealth()
	if state.FailureCount != 1 || state.CircuitOpenSince != 0 ||
		state.ValidationPending != node.TCPValidationNone {
		t.Fatalf("validation was not canceled cleanly: %+v", state)
	}
}

func TestTCPValidation_UpdatesEgressMetadataOnValidTrace(t *testing.T) {
	pool, hash, entry := newValidationTestNode(t, 1)
	manager := NewProbeManager(ProbeConfig{Pool: pool, Fetcher: successfulValidationFetcher})
	if err := pool.RecordTCPHealthResult(hash, topology.HealthSourcePassive, false, "request-7"); err != nil {
		t.Fatal(err)
	}
	runNextQueuedTask(t, manager)
	if got := entry.GetEgressIP(); got != netip.MustParseAddr("198.51.100.9") {
		t.Fatalf("egress IP = %s, want 198.51.100.9", got)
	}
}
