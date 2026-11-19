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

func TestExcludeHashes_OmitsAllSkipped(t *testing.T) {
	inner := NewRoutableView()
	keep := node.HashFromRawOptions([]byte(`{"keep":true}`))
	skipA := node.HashFromRawOptions([]byte(`{"skip":"a"}`))
	skipB := node.HashFromRawOptions([]byte(`{"skip":"b"}`))
	inner.Add(keep)
	inner.Add(skipA)
	inner.Add(skipB)

	view := ExcludeHashes(inner, []node.Hash{skipA, skipB, node.Zero})
	if view.Contains(skipA) || view.Contains(skipB) {
		t.Fatal("excluded hashes still contained")
	}
	if !view.Contains(keep) {
		t.Fatal("kept hash missing")
	}
	if view.Size() != 1 {
		t.Fatalf("size = %d, want 1", view.Size())
	}
	rng := rand.New(rand.NewPCG(5, 6))
	for i := 0; i < 20; i++ {
		h, ok := view.RandomPick(rng)
		if !ok || h != keep {
			t.Fatalf("random pick = %v ok=%v, want keep", h, ok)
		}
	}
}

func TestPreferNonVision_HidesVisionWhenPlainExists(t *testing.T) {
	inner := NewRoutableView()
	plain := node.HashFromRawOptions([]byte(`{"flow":""}`))
	vision := node.HashFromRawOptions([]byte(`{"flow":"xtls-rprx-vision"}`))
	inner.Add(plain)
	inner.Add(vision)

	view := PreferNonVision(inner, func(h node.Hash) bool { return h == vision })
	if view.Contains(vision) {
		t.Fatal("vision hop still contained")
	}
	if !view.Contains(plain) {
		t.Fatal("plain hop missing")
	}
	if view.Size() != 1 {
		t.Fatalf("size = %d, want 1", view.Size())
	}
}

func TestPreferNonVision_KeepsVisionWhenOnlyVisionRemains(t *testing.T) {
	inner := NewRoutableView()
	vision := node.HashFromRawOptions([]byte(`{"only":"vision"}`))
	inner.Add(vision)
	view := PreferNonVision(inner, func(node.Hash) bool { return true })
	if !view.Contains(vision) || view.Size() != 1 {
		t.Fatal("vision-only view should stay selectable")
	}
}
