package outbound

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/sagernet/sing-box/adapter"
)

type recordingChainBuilder struct {
	builds atomic.Int32
	ob     adapter.Outbound
}

func (b *recordingChainBuilder) Build(_ json.RawMessage) (adapter.Outbound, error) {
	return testutil.NewNoopOutbound(), nil
}

func (b *recordingChainBuilder) BuildChained(hops []ChainedHop, landing json.RawMessage, landingHash node.Hash) (adapter.Outbound, []string, error) {
	b.builds.Add(1)
	if b.ob == nil {
		b.ob = testutil.NewNoopOutbound()
	}
	hashes := hopHashes(hops)
	tags := make([]string, 0, len(hops)+1)
	for i := range hops {
		tags = append(tags, chainHopTag(hashes, landingHash, i))
	}
	tags = append(tags, chainLandingTag(hashes, landingHash))
	return b.ob, tags, nil
}

func (b *recordingChainBuilder) ReleaseChain([]string) {}

func hopHashes(hops []ChainedHop) []node.Hash {
	out := make([]node.Hash, len(hops))
	for i, hop := range hops {
		out[i] = hop.Hash
	}
	return out
}

func TestEnsureChainedOutboundCachesByHopAndLanding(t *testing.T) {
	hop := newTestEntry(`{"type":"direct","role":"hop"}`)
	landing := newTestEntry(`{"type":"direct","role":"land"}`)
	pool := &mockPool{}
	pool.addEntry(hop)
	pool.addEntry(landing)

	builder := &recordingChainBuilder{}
	mgr := NewOutboundManager(pool, builder)
	first, err := mgr.EnsureChainedOutbound([]node.Hash{hop.Hash}, landing.Hash)
	if err != nil {
		t.Fatalf("first chain: %v", err)
	}
	second, err := mgr.EnsureChainedOutbound([]node.Hash{hop.Hash}, landing.Hash)
	if err != nil {
		t.Fatalf("second chain: %v", err)
	}
	if first != second {
		t.Fatal("expected cached chained outbound")
	}
	if builder.builds.Load() != 1 {
		t.Fatalf("BuildChained calls = %d, want 1", builder.builds.Load())
	}

	mgr.RemoveNodeOutbound(landing)
	third, err := mgr.EnsureChainedOutbound([]node.Hash{hop.Hash}, landing.Hash)
	if err != nil {
		t.Fatalf("rebuild after invalidate: %v", err)
	}
	if third == nil || builder.builds.Load() != 2 {
		t.Fatalf("expected a new chain build after invalidate, builds=%d", builder.builds.Load())
	}
}

type blockingChainBuilder struct {
	started chan struct{}
	release chan struct{}
	builds  atomic.Int32
	ob      adapter.Outbound
}

func (b *blockingChainBuilder) Build(_ json.RawMessage) (adapter.Outbound, error) {
	return testutil.NewNoopOutbound(), nil
}

func (b *blockingChainBuilder) BuildChained(hops []ChainedHop, landing json.RawMessage, landingHash node.Hash) (adapter.Outbound, []string, error) {
	b.builds.Add(1)
	select {
	case <-b.started:
	default:
		close(b.started)
	}
	<-b.release
	if b.ob == nil {
		b.ob = testutil.NewNoopOutbound()
	}
	hashes := hopHashes(hops)
	tags := make([]string, 0, len(hops)+1)
	for i := range hops {
		tags = append(tags, chainHopTag(hashes, landingHash, i))
	}
	tags = append(tags, chainLandingTag(hashes, landingHash))
	return b.ob, tags, nil
}

func (b *blockingChainBuilder) ReleaseChain([]string) {}

func TestEnsureChainedOutboundSingleFlight(t *testing.T) {
	hop := newTestEntry(`{"type":"direct","role":"hop-sf"}`)
	landing := newTestEntry(`{"type":"direct","role":"land-sf"}`)
	pool := &mockPool{}
	pool.addEntry(hop)
	pool.addEntry(landing)

	builder := &blockingChainBuilder{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr := NewOutboundManager(pool, builder)

	type result struct {
		ob  adapter.Outbound
		err error
	}
	results := make(chan result, 2)
	go func() {
		ob, err := mgr.EnsureChainedOutbound([]node.Hash{hop.Hash}, landing.Hash)
		results <- result{ob, err}
	}()
	<-builder.started
	go func() {
		ob, err := mgr.EnsureChainedOutbound([]node.Hash{hop.Hash}, landing.Hash)
		results <- result{ob, err}
	}()
	close(builder.release)

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("single-flight errors: %v %v", first.err, second.err)
	}
	if first.ob != second.ob {
		t.Fatal("single-flight returned different outbounds")
	}
	if builder.builds.Load() != 1 {
		t.Fatalf("BuildChained calls = %d, want 1", builder.builds.Load())
	}
}

func TestEnsureChainedOutboundEvictsOldest(t *testing.T) {
	hop := newTestEntry(`{"type":"direct","role":"hop-lru"}`)
	firstLanding := newTestEntry(`{"type":"direct","role":"land-lru-1"}`)
	secondLanding := newTestEntry(`{"type":"direct","role":"land-lru-2"}`)
	pool := &mockPool{}
	pool.addEntry(hop)
	pool.addEntry(firstLanding)
	pool.addEntry(secondLanding)

	builder := &recordingChainBuilder{}
	mgr := NewOutboundManager(pool, builder)
	mgr.maxCachedChains = 1

	if _, err := mgr.EnsureChainedOutbound([]node.Hash{hop.Hash}, firstLanding.Hash); err != nil {
		t.Fatalf("first chain: %v", err)
	}
	if _, err := mgr.EnsureChainedOutbound([]node.Hash{hop.Hash}, secondLanding.Hash); err != nil {
		t.Fatalf("second chain: %v", err)
	}
	if builder.builds.Load() != 2 {
		t.Fatalf("builds after second insert = %d, want 2", builder.builds.Load())
	}
	if _, err := mgr.EnsureChainedOutbound([]node.Hash{hop.Hash}, firstLanding.Hash); err != nil {
		t.Fatalf("rebuild evicted chain: %v", err)
	}
	if builder.builds.Load() != 3 {
		t.Fatalf("evicted chain was not rebuilt, builds=%d", builder.builds.Load())
	}
}
