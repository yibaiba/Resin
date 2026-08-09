package outbound

import (
	"errors"
	"sync"
	"testing"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
)

func TestOutboundManager_CloseRejectsInFlightBuild(t *testing.T) {
	entry := newTestEntry(`{"type":"slow-build-close"}`)
	pool := &mockPool{}
	pool.addEntry(entry)
	builder := newBlockingClosableBuilder()
	mgr := NewOutboundManager(pool, builder)

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.EnsureNodeOutbound(entry.Hash)
	}()
	<-builder.started

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	close(builder.release)
	<-done

	if entry.Outbound.Load() != nil {
		t.Fatal("expected no outbound publication after Close")
	}
	if built := builder.firstBuilt(t); !built.closed.Load() {
		t.Fatal("expected in-flight build result to be closed")
	}
}

func TestOutboundManager_CloseClearsAllAndAggregatesErrors(t *testing.T) {
	firstErr := errors.New("close first")
	secondErr := errors.New("close second")
	first := &closableOnly{closeErr: firstErr}
	second := &closableOnly{closeErr: secondErr}
	firstEntry := entryWithOutbound(`{"type":"first"}`, first)
	secondEntry := entryWithOutbound(`{"type":"second"}`, second)
	pool := &mockPool{}
	pool.addEntry(firstEntry)
	pool.addEntry(secondEntry)
	mgr := NewOutboundManager(pool, &countingBuilder{})

	err := mgr.Close()
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("Close() error: got %v, want both close errors", err)
	}
	if firstEntry.HasOutbound() || secondEntry.HasOutbound() {
		t.Fatal("expected all outbound pointers to be cleared")
	}
	secondCloseErr := mgr.Close()
	if secondCloseErr == nil || secondCloseErr.Error() != err.Error() {
		t.Fatalf("second Close() error: got %v, want stable %v", secondCloseErr, err)
	}
	if first.closeCalls.Load() != 1 || second.closeCalls.Load() != 1 {
		t.Fatalf("close calls: got first=%d second=%d, want 1 each", first.closeCalls.Load(), second.closeCalls.Load())
	}
}

func TestOutboundManager_ClosePreventsNewBuilds(t *testing.T) {
	entry := newTestEntry(`{"type":"closed"}`)
	pool := &mockPool{}
	pool.addEntry(entry)
	builder := &countingBuilder{}
	mgr := NewOutboundManager(pool, builder)

	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	mgr.EnsureNodeOutbound(entry.Hash)
	mgr.WarmupAll()

	if builder.Count() != 0 {
		t.Fatalf("Build() calls after close: got %d, want 0", builder.Count())
	}
	if entry.HasOutbound() {
		t.Fatal("expected outbound to remain nil after close")
	}
}

func TestOutboundManager_ConcurrentCloseRunsOnce(t *testing.T) {
	closer := &closableOnly{}
	entry := entryWithOutbound(`{"type":"concurrent-close"}`, closer)
	pool := &mockPool{}
	pool.addEntry(entry)
	mgr := NewOutboundManager(pool, &countingBuilder{})

	const callers = 20
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if err := mgr.Close(); err != nil {
				t.Errorf("Close() error: %v", err)
			}
		}()
	}
	wg.Wait()

	if closer.closeCalls.Load() != 1 {
		t.Fatalf("outbound close calls: got %d, want 1", closer.closeCalls.Load())
	}
}

func entryWithOutbound(raw string, ob adapter.Outbound) *node.NodeEntry {
	entry := newTestEntry(raw)
	entry.Outbound.Store(&ob)
	return entry
}
