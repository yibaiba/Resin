package platform

import (
	"math/rand/v2"

	"github.com/Resinat/Resin/internal/node"
)

const excludePickAttempts = 8

// ExcludeHash returns a read-only view that omits skip. A zero skip, or a nil
// view, is returned unchanged.
func ExcludeHash(view ReadOnlyView, skip node.Hash) ReadOnlyView {
	if view == nil || skip.IsZero() {
		return view
	}
	return excludingView{inner: view, skip: skip}
}

type excludingView struct {
	inner ReadOnlyView
	skip  node.Hash
}

func (v excludingView) Contains(h node.Hash) bool {
	if h == v.skip {
		return false
	}
	return v.inner.Contains(h)
}

func (v excludingView) Size() int {
	n := v.inner.Size()
	if n > 0 && v.inner.Contains(v.skip) {
		return n - 1
	}
	return n
}

func (v excludingView) RandomPick(rng *rand.Rand) (node.Hash, bool) {
	if v.Size() == 0 {
		return node.Zero, false
	}
	for i := 0; i < excludePickAttempts; i++ {
		h, ok := v.inner.RandomPick(rng)
		if !ok {
			return node.Zero, false
		}
		if h != v.skip {
			return h, true
		}
	}
	var found node.Hash
	ok := false
	v.Range(func(h node.Hash) bool {
		found = h
		ok = true
		return false
	})
	return found, ok
}

func (v excludingView) Range(fn func(node.Hash) bool) {
	v.inner.Range(func(h node.Hash) bool {
		if h == v.skip {
			return true
		}
		return fn(h)
	})
}

// ExcludeHashes omits every hash in skips. Empty skips, or a nil view, is
// returned unchanged.
func ExcludeHashes(view ReadOnlyView, skips []node.Hash) ReadOnlyView {
	if view == nil || len(skips) == 0 {
		return view
	}
	skip := make(map[node.Hash]struct{}, len(skips))
	for _, h := range skips {
		if h.IsZero() {
			continue
		}
		skip[h] = struct{}{}
	}
	switch len(skip) {
	case 0:
		return view
	case 1:
		for h := range skip {
			return ExcludeHash(view, h)
		}
	}
	return excludingSetView{inner: view, skip: skip}
}

type excludingSetView struct {
	inner ReadOnlyView
	skip  map[node.Hash]struct{}
}

func (v excludingSetView) skipped(h node.Hash) bool {
	_, ok := v.skip[h]
	return ok
}

func (v excludingSetView) Contains(h node.Hash) bool {
	if v.skipped(h) {
		return false
	}
	return v.inner.Contains(h)
}

func (v excludingSetView) Size() int {
	n := v.inner.Size()
	for h := range v.skip {
		if v.inner.Contains(h) {
			n--
		}
	}
	if n < 0 {
		return 0
	}
	return n
}

func (v excludingSetView) RandomPick(rng *rand.Rand) (node.Hash, bool) {
	if v.Size() == 0 {
		return node.Zero, false
	}
	for i := 0; i < excludePickAttempts; i++ {
		h, ok := v.inner.RandomPick(rng)
		if !ok {
			return node.Zero, false
		}
		if !v.skipped(h) {
			return h, true
		}
	}
	var found node.Hash
	ok := false
	v.Range(func(h node.Hash) bool {
		found = h
		ok = true
		return false
	})
	return found, ok
}

func (v excludingSetView) Range(fn func(node.Hash) bool) {
	v.inner.Range(func(h node.Hash) bool {
		if v.skipped(h) {
			return true
		}
		return fn(h)
	})
}

// PreferNonVision hides XTLS-Vision hops when the view still has a non-Vision
// node. Vision splices inner TLS and breaks a Reality landing dialed through
// that hop. If every remaining node is Vision, the view is left unchanged.
func PreferNonVision(view ReadOnlyView, hasVision func(node.Hash) bool) ReadOnlyView {
	if view == nil || hasVision == nil || view.Size() == 0 {
		return view
	}
	var vision []node.Hash
	plain := 0
	view.Range(func(h node.Hash) bool {
		if hasVision(h) {
			vision = append(vision, h)
		} else {
			plain++
		}
		return true
	})
	if plain == 0 || len(vision) == 0 {
		return view
	}
	return ExcludeHashes(view, vision)
}
