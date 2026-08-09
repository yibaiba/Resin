package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/Resinat/Resin/internal/netutil"
	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
)

var ErrOutboundNotReady = errors.New("outbound not ready")

// PoolAccessor provides read-only access to the node pool.
type PoolAccessor interface {
	GetEntry(hash node.Hash) (*node.NodeEntry, bool)
	RangeNodes(fn func(node.Hash, *node.NodeEntry) bool)
}

// closeOutbound closes an outbound if it implements io.Closer.
func closeOutbound(ob adapter.Outbound) error {
	if c, ok := ob.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func buildOutboundSafely(builder OutboundBuilder, rawOptions json.RawMessage) (ob adapter.Outbound, err error) {
	defer func() {
		if r := recover(); r != nil {
			if ob != nil {
				_ = closeOutbound(ob)
			}
			ob = nil
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return builder.Build(rawOptions)
}

// OutboundManager manages outbound lifecycle and provides unified HTTP execution.
type OutboundManager struct {
	pool    PoolAccessor
	builder OutboundBuilder

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

type detachedOutbound struct {
	hash     node.Hash
	outbound adapter.Outbound
}

func NewOutboundManager(pool PoolAccessor, builder OutboundBuilder) *OutboundManager {
	return &OutboundManager{pool: pool, builder: builder}
}

func (m *OutboundManager) isLiveEntry(hash node.Hash, entry *node.NodeEntry) bool {
	current, ok := m.pool.GetEntry(hash)
	return ok && current == entry
}

func (m *OutboundManager) canBuild() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.closed
}

// EnsureNodeOutbound idempotently creates and stores an outbound for a node.
// Uses CompareAndSwap(nil, &wrapped) to guarantee only one goroutine's build
// result is stored. Losers close their unpublished build result.
func (m *OutboundManager) EnsureNodeOutbound(hash node.Hash) {
	if !m.canBuild() {
		return
	}
	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return
	}
	// Fast path: already has outbound.
	if entry.Outbound.Load() != nil {
		return
	}

	ob, err := buildOutboundSafely(m.builder, entry.RawOptions)
	if err != nil {
		entry.SetLastError("outbound build: " + err.Error())
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		_ = closeOutbound(ob)
		return
	}

	// Build can race with node deletion/replacement. If this entry is no longer
	// the pool's live value for the hash, discard the build result.
	if !m.isLiveEntry(hash, entry) {
		_ = closeOutbound(ob)
		return
	}

	if !entry.Outbound.CompareAndSwap(nil, &ob) {
		// Another goroutine won the race. Close the losing build result.
		_ = closeOutbound(ob)
		return
	}

	// Close-and-clear if the node disappeared/replaced right after CAS.
	if !m.isLiveEntry(hash, entry) {
		old := entry.Outbound.Swap(nil)
		if old != nil {
			_ = closeOutbound(*old)
		}
	}
}

// RemoveNodeOutbound clears a node's outbound reference.
// Accepts the entry directly because the node may already be deleted from the pool
// (RemoveNodeFromSub deletes before firing onNodeRemoved callback).
func (m *OutboundManager) RemoveNodeOutbound(entry *node.NodeEntry) {
	if entry == nil {
		return
	}
	old := entry.Outbound.Swap(nil)
	if old != nil {
		_ = closeOutbound(*old)
	}
}

// Close prevents future builds, clears every published outbound, and closes
// the detached resources. Repeated calls return the same aggregated error.
func (m *OutboundManager) Close() error {
	m.closeOnce.Do(func() {
		outbounds := m.detachAllOutbounds()
		var errs []error
		for _, detached := range outbounds {
			if err := closeOutbound(detached.outbound); err != nil {
				errs = append(errs, fmt.Errorf("close outbound %s: %w", detached.hash.Hex(), err))
			}
		}
		m.closeErr = errors.Join(errs...)
	})
	return m.closeErr
}

func (m *OutboundManager) detachAllOutbounds() []detachedOutbound {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closed = true
	var outbounds []detachedOutbound
	m.pool.RangeNodes(func(hash node.Hash, entry *node.NodeEntry) bool {
		if entry == nil {
			return true
		}
		if old := entry.Outbound.Swap(nil); old != nil {
			outbounds = append(outbounds, detachedOutbound{hash: hash, outbound: *old})
		}
		return true
	})
	sort.Slice(outbounds, func(i, j int) bool {
		return bytes.Compare(outbounds[i].hash[:], outbounds[j].hash[:]) < 0
	})
	return outbounds
}

// WarmupAll iterates all nodes in the pool and ensures each has an outbound.
// Called once after bootstrap to avoid ErrOutboundNotReady on restart.
func (m *OutboundManager) WarmupAll() {
	m.pool.RangeNodes(func(h node.Hash, _ *node.NodeEntry) bool {
		m.EnsureNodeOutbound(h)
		return true
	})
}

// Fetch executes HTTP request using the node's outbound.
// Returns ErrOutboundNotReady if the node's outbound is not yet initialized.
// ctx controls timeout/cancellation.
func (m *OutboundManager) Fetch(ctx context.Context, hash node.Hash, url string) ([]byte, time.Duration, error) {
	return m.FetchWithUserAgent(ctx, hash, url, "")
}

// FetchWithUserAgent executes HTTP request using the node's outbound and
// applies the given User-Agent if non-empty.
func (m *OutboundManager) FetchWithUserAgent(
	ctx context.Context,
	hash node.Hash,
	url string,
	userAgent string,
) ([]byte, time.Duration, error) {
	entry, ok := m.pool.GetEntry(hash)
	if !ok {
		return nil, 0, errors.New("node not found")
	}
	outboundPtr := entry.Outbound.Load() // *adapter.Outbound
	if outboundPtr == nil {
		return nil, 0, ErrOutboundNotReady
	}
	return netutil.HTTPGetViaOutbound(ctx, *outboundPtr, url, netutil.OutboundHTTPOptions{
		RequireStatusOK: true,
		UserAgent:       userAgent,
	})
}
