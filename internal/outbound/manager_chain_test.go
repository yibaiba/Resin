package outbound

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

type chainAwarePool struct {
	mockPool
	hasChain bool
	tcpHops  []node.Hash
	udpHops  []node.Hash
}

func (p *chainAwarePool) HasUniqueLandingChain(node.Hash) bool {
	return p.hasChain
}

func (p *chainAwarePool) UniqueLandingChain(node.Hash) ([]node.Hash, bool) {
	if !p.hasChain || len(p.tcpHops) == 0 {
		return nil, false
	}
	return append([]node.Hash(nil), p.tcpHops...), true
}

func (p *chainAwarePool) UniqueLandingChainForNetwork(_ node.Hash, network string) ([]node.Hash, bool) {
	if !p.hasChain {
		return nil, false
	}
	if N.NetworkName(network) == N.NetworkUDP {
		if len(p.udpHops) == 0 {
			return nil, false
		}
		return append([]node.Hash(nil), p.udpHops...), true
	}
	return p.UniqueLandingChain(node.Hash{})
}

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

func TestOutboundForUDPProbeSkipsWhenUniqueChainHasNoUDPHop(t *testing.T) {
	hop := newTestEntry(`{"type":"direct","role":"hop-tcp"}`)
	landing := newTestEntry(`{"type":"direct","role":"land-skip"}`)
	pool := &chainAwarePool{hasChain: true, tcpHops: []node.Hash{hop.Hash}}
	pool.addEntry(hop)
	pool.addEntry(landing)
	mgr := NewOutboundManager(pool, &recordingChainBuilder{})

	_, err := mgr.OutboundForUDPProbe(landing.Hash)
	if !errors.Is(err, ErrSkipUDPProbe) {
		t.Fatalf("UDP probe = %v, want ErrSkipUDPProbe", err)
	}
	ob, err := mgr.OutboundForProbe(landing.Hash)
	if err != nil || ob == nil {
		t.Fatalf("HTTP probe: ob=%v err=%v", ob, err)
	}
}

func TestOutboundForProbeSkipsWhenUniqueChainHasNoHop(t *testing.T) {
	landing := newTestEntry(`{"type":"direct","role":"land-http-skip"}`)
	pool := &chainAwarePool{hasChain: true}
	pool.addEntry(landing)
	mgr := NewOutboundManager(pool, &recordingChainBuilder{})

	_, err := mgr.OutboundForProbe(landing.Hash)
	if !errors.Is(err, ErrSkipProbe) {
		t.Fatalf("HTTP probe = %v, want ErrSkipProbe", err)
	}
}

func TestOutboundForUDPProbeUsesUDPHops(t *testing.T) {
	hop := newTestEntry(`{"type":"direct","role":"hop-udp"}`)
	landing := newTestEntry(`{"type":"direct","role":"land-udp"}`)
	pool := &chainAwarePool{hasChain: true, udpHops: []node.Hash{hop.Hash}}
	pool.addEntry(hop)
	pool.addEntry(landing)
	builder := &recordingChainBuilder{}
	mgr := NewOutboundManager(pool, builder)

	ob, err := mgr.OutboundForUDPProbe(landing.Hash)
	if err != nil || ob == nil {
		t.Fatalf("UDP probe: ob=%v err=%v", ob, err)
	}
	if builder.builds.Load() != 1 {
		t.Fatalf("BuildChained calls = %d, want 1", builder.builds.Load())
	}
}
