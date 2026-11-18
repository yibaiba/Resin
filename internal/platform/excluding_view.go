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
