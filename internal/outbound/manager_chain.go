package outbound

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
	N "github.com/sagernet/sing/common/network"
)

type landingChainResolver interface {
	HasUniqueLandingChain(hash node.Hash) bool
	UniqueLandingChain(hash node.Hash) ([]node.Hash, bool)
	UniqueLandingChainForNetwork(hash node.Hash, network string) ([]node.Hash, bool)
}

func chainCacheKey(hops []node.Hash, landing node.Hash) string {
	parts := make([]string, 0, len(hops)+1)
	for _, hop := range hops {
		parts = append(parts, hop.Hex())
	}
	parts = append(parts, landing.Hex())
	return strings.Join(parts, ">")
}

type chainRelease struct {
	key   string
	chain *chainedOutbound
	tags  []string
	done  chan struct{}
}

func (m *OutboundManager) EnsureChainedOutbound(hops []node.Hash, landing node.Hash) (adapter.Outbound, func(), error) {
	if len(hops) == 0 {
		return nil, nopChainRelease, fmt.Errorf("chained outbound requires at least one hop")
	}
	if landing.IsZero() {
		return nil, nopChainRelease, fmt.Errorf("chained outbound requires a landing node")
	}
	key := chainCacheKey(hops, landing)
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, nopChainRelease, ErrOutboundNotReady
		}
		if tombstone, ok := m.chainTombstones[key]; ok {
			m.mu.Unlock()
			<-tombstone
			continue
		}
		if existing, ok := m.chains[key]; ok {
			existing.lastUsed = time.Now().UnixNano()
			ob := existing.outbound
			release := m.holdChainLocked(key)
			m.mu.Unlock()
			return ob, release, nil
		}
		if waiter, ok := m.chainInflight[key]; ok {
			m.mu.Unlock()
			<-waiter.done
			if waiter.err != nil {
				return nil, nopChainRelease, waiter.err
			}
			continue
		}
		waiter := &chainBuildWaiter{done: make(chan struct{})}
		m.chainInflight[key] = waiter
		m.mu.Unlock()

		ob, tags, hashes, err := m.buildChainedOutbound(hops, landing)

		m.mu.Lock()
		delete(m.chainInflight, key)
		if err != nil {
			waiter.err = err
			close(waiter.done)
			m.mu.Unlock()
			return nil, nopChainRelease, err
		}
		if m.closed || !m.chainHashesPresent(hashes) {
			if m.closed {
				waiter.err = ErrOutboundNotReady
			} else {
				waiter.err = fmt.Errorf("chain node was removed during build")
			}
			job := m.beginTagReleaseLocked(key, tags, nil)
			close(waiter.done)
			m.mu.Unlock()
			m.finishChainReleases([]chainRelease{job})
			return nil, nopChainRelease, waiter.err
		}
		now := time.Now().UnixNano()
		wrapped := m.wrapCachedChain(key, ob)
		m.chains[key] = &chainedOutbound{outbound: wrapped, tags: tags, hashes: hashes, lastUsed: now}
		release := m.holdChainLocked(key)
		stale := m.evictExcessChainsLocked(key)
		close(waiter.done)
		m.mu.Unlock()
		m.finishChainReleases(stale)
		return wrapped, release, nil
	}
}

func (m *OutboundManager) buildChainedOutbound(hops []node.Hash, landing node.Hash) (adapter.Outbound, []string, []node.Hash, error) {
	builder, ok := m.builder.(ChainedOutboundBuilder)
	if !ok {
		return nil, nil, nil, fmt.Errorf("outbound builder does not support chained outbounds")
	}
	hopSpecs := make([]ChainedHop, 0, len(hops))
	for i, hop := range hops {
		entry, exists := m.pool.GetEntry(hop)
		if !exists || entry == nil || len(entry.RawOptions) == 0 {
			return nil, nil, nil, fmt.Errorf("chain hop %d %s not found", i+1, hop.Hex())
		}
		m.EnsureNodeOutbound(hop)
		hopSpecs = append(hopSpecs, ChainedHop{Hash: hop, Raw: append(json.RawMessage(nil), entry.RawOptions...)})
	}
	landingEntry, exists := m.pool.GetEntry(landing)
	if !exists || landingEntry == nil || len(landingEntry.RawOptions) == 0 {
		return nil, nil, nil, fmt.Errorf("chain landing %s not found", landing.Hex())
	}
	m.EnsureNodeOutbound(landing)

	ob, tags, err := builder.BuildChained(hopSpecs, append(json.RawMessage(nil), landingEntry.RawOptions...), landing)
	if err != nil {
		return nil, nil, nil, err
	}
	hashes := append(append([]node.Hash(nil), hops...), landing)
	return ob, tags, hashes, nil
}

func (m *OutboundManager) chainHashesPresent(hashes []node.Hash) bool {
	for _, hash := range hashes {
		if _, ok := m.pool.GetEntry(hash); !ok {
			return false
		}
	}
	return true
}

func (m *OutboundManager) evictExcessChainsLocked(keepKey string) []chainRelease {
	limit := m.maxCachedChains
	if limit <= 0 {
		limit = defaultMaxCachedChains
	}
	excess := len(m.chains) - limit
	if excess <= 0 {
		return nil
	}
	type ranked struct {
		key  string
		used int64
	}
	items := make([]ranked, 0, len(m.chains))
	for key, chain := range m.chains {
		if key == keepKey || chain.refs > 0 {
			continue
		}
		items = append(items, ranked{key: key, used: chain.lastUsed})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].used < items[j].used })
	if len(items) > excess {
		items = items[:excess]
	}
	stale := make([]chainRelease, 0, len(items))
	for _, item := range items {
		stale = append(stale, m.beginChainReleaseLocked(item.key, m.chains[item.key]))
	}
	return stale
}

func (m *OutboundManager) beginChainReleaseLocked(key string, chain *chainedOutbound) chainRelease {
	delete(m.chains, key)
	return m.beginTagReleaseLocked(key, nil, chain)
}

func (m *OutboundManager) beginTagReleaseLocked(key string, tags []string, chain *chainedOutbound) chainRelease {
	done := make(chan struct{})
	m.chainTombstones[key] = done
	return chainRelease{key: key, chain: chain, tags: tags, done: done}
}

func (m *OutboundManager) finishChainReleases(jobs []chainRelease) {
	for _, job := range jobs {
		if job.chain != nil {
			m.releaseChain(job.chain)
		} else {
			m.releaseBuiltTags(job.tags)
		}
		close(job.done)
		m.mu.Lock()
		if current, ok := m.chainTombstones[job.key]; ok && current == job.done {
			delete(m.chainTombstones, job.key)
		}
		m.mu.Unlock()
	}
}

func (m *OutboundManager) releaseBuiltTags(tags []string) {
	if builder, ok := m.builder.(ChainedOutboundBuilder); ok {
		builder.ReleaseChain(tags)
	}
}

func (m *OutboundManager) OutboundForProbe(hash node.Hash) (adapter.Outbound, error) {
	return m.outboundForProbe(hash)
}

func (m *OutboundManager) OutboundForUDPProbe(hash node.Hash) (adapter.Outbound, error) {
	if resolver, ok := m.pool.(landingChainResolver); ok && resolver.HasUniqueLandingChain(hash) {
		hops, found := resolver.UniqueLandingChainForNetwork(hash, N.NetworkUDP)
		if !found {
			log.Printf("[probe] skip udp node %s: chained landing has no usable udp hops", hash.Hex())
			return nil, ErrSkipUDPProbe
		}
		log.Printf("[probe] node %s udp using chain hops %s", hash.Hex(), formatHopHashes(hops))
		ob, release, err := m.EnsureChainedOutbound(hops, hash)
		if err != nil {
			return nil, err
		}
		return bindChainLease(ob, release), nil
	}
	return m.directProbeOutbound(hash)
}

func (m *OutboundManager) outboundForProbe(hash node.Hash) (adapter.Outbound, error) {
	if resolver, ok := m.pool.(landingChainResolver); ok {
		if hops, found := resolver.UniqueLandingChain(hash); found {
			log.Printf("[probe] node %s using chain hops %s", hash.Hex(), formatHopHashes(hops))
			ob, release, err := m.EnsureChainedOutbound(hops, hash)
			if err != nil {
				return nil, err
			}
			return bindChainLease(ob, release), nil
		}
		if resolver.HasUniqueLandingChain(hash) {
			log.Printf("[probe] skip node %s: chained landing has no usable hops", hash.Hex())
			return nil, ErrSkipProbe
		}
	}
	return m.directProbeOutbound(hash)
}

func formatHopHashes(hops []node.Hash) string {
	parts := make([]string, len(hops))
	for i, hop := range hops {
		parts[i] = hop.Hex()
	}
	return strings.Join(parts, ">")
}

func (m *OutboundManager) directProbeOutbound(hash node.Hash) (adapter.Outbound, error) {
	entry, exists := m.pool.GetEntry(hash)
	if !exists {
		return nil, fmt.Errorf("node not found")
	}
	outboundPtr := entry.Outbound.Load()
	if outboundPtr == nil {
		return nil, ErrOutboundNotReady
	}
	return *outboundPtr, nil
}

func (m *OutboundManager) releaseChainsContaining(hash node.Hash) {
	m.mu.Lock()
	pending := make([]chainRelease, 0)
	for key, chain := range m.chains {
		if !chainContainsHash(chain, hash) {
			continue
		}
		pending = append(pending, m.beginChainReleaseLocked(key, chain))
	}
	m.mu.Unlock()
	m.finishChainReleases(pending)
}

func (m *OutboundManager) detachAllChains() []*chainedOutbound {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*chainedOutbound, 0, len(m.chains))
	for _, chain := range m.chains {
		out = append(out, chain)
	}
	clear(m.chains)
	return out
}

func (m *OutboundManager) releaseChain(chain *chainedOutbound) {
	if chain == nil {
		return
	}
	if builder, ok := m.builder.(ChainedOutboundBuilder); ok {
		builder.ReleaseChain(chain.tags)
		return
	}
	_ = closeOutbound(chain.outbound)
}

func chainContainsHash(chain *chainedOutbound, hash node.Hash) bool {
	if chain == nil {
		return false
	}
	for _, item := range chain.hashes {
		if item == hash {
			return true
		}
	}
	return false
}
