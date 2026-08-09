package routing

import (
	"math/rand/v2"

	"github.com/Resinat/Resin/internal/node"
)

type filteredView struct {
	nodes []node.Hash
	set   map[node.Hash]struct{}
}

func newFilteredView(capacity int) *filteredView {
	return &filteredView{
		nodes: make([]node.Hash, 0, capacity),
		set:   make(map[node.Hash]struct{}, capacity),
	}
}

func (v *filteredView) Add(hash node.Hash) {
	if _, exists := v.set[hash]; exists {
		return
	}
	v.set[hash] = struct{}{}
	v.nodes = append(v.nodes, hash)
}

func (v *filteredView) Contains(hash node.Hash) bool {
	_, ok := v.set[hash]
	return ok
}

func (v *filteredView) Size() int { return len(v.nodes) }

func (v *filteredView) RandomPick(rng *rand.Rand) (node.Hash, bool) {
	if len(v.nodes) == 0 {
		return node.Zero, false
	}
	return v.nodes[rng.IntN(len(v.nodes))], true
}

func (v *filteredView) Range(fn func(node.Hash) bool) {
	for _, hash := range v.nodes {
		if !fn(hash) {
			return
		}
	}
}
