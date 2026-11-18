package platform

import (
	"math/rand/v2"
	"testing"

	"github.com/Resinat/Resin/internal/node"
)

func TestExcludeHash_OmitsSkippedNode(t *testing.T) {
	inner := NewRoutableView()
	keep := node.HashFromRawOptions([]byte(`{"keep":true}`))
	skip := node.HashFromRawOptions([]byte(`{"skip":true}`))
	inner.Add(keep)
	inner.Add(skip)

	view := ExcludeHash(inner, skip)
	if view.Contains(skip) {
		t.Fatal("excluded hash still contained")
	}
	if !view.Contains(keep) {
		t.Fatal("kept hash missing")
	}
	if view.Size() != 1 {
		t.Fatalf("size = %d, want 1", view.Size())
	}

	seen := 0
	view.Range(func(h node.Hash) bool {
		if h == skip {
			t.Fatal("range visited excluded hash")
		}
		seen++
		return true
	})
	if seen != 1 {
		t.Fatalf("range count = %d, want 1", seen)
	}

	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 20; i++ {
		h, ok := view.RandomPick(rng)
		if !ok || h != keep {
			t.Fatalf("random pick = %v ok=%v, want keep", h, ok)
		}
	}
}

func TestExcludeHash_EmptyWhenOnlySkipRemains(t *testing.T) {
	inner := NewRoutableView()
	skip := node.HashFromRawOptions([]byte(`{"only":true}`))
	inner.Add(skip)
	view := ExcludeHash(inner, skip)
	if view.Size() != 0 {
		t.Fatalf("size = %d, want 0", view.Size())
	}
	if _, ok := view.RandomPick(rand.New(rand.NewPCG(3, 4))); ok {
		t.Fatal("random pick succeeded on empty excluding view")
	}
}

func TestExcludeHash_ZeroSkipReturnsInner(t *testing.T) {
	inner := NewRoutableView()
	if got := ExcludeHash(inner, node.Zero); got != inner {
		t.Fatal("zero skip should return the original view")
	}
}
