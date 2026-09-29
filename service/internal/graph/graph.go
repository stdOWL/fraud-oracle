// Package graph is an in-memory view of transfers around one address, built per scoring call.
// Everything is ordered by (block, logIndex) so rule output is deterministic.
package graph

import (
	"sort"

	"github.com/ethereum/go-ethereum/common"

	"fraud-oracle/service/internal/store"
)

type Graph struct {
	// Out[a] = transfers sent by a, In[a] = transfers received by a. Both ordered.
	Out map[common.Address][]store.Transfer
	In  map[common.Address][]store.Transfer
	// FirstSeen[a] = block of the first transfer touching a within this graph.
	FirstSeen map[common.Address]uint64
}

func New(ts []store.Transfer) *Graph {
	sorted := make([]store.Transfer, len(ts))
	copy(sorted, ts)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].BlockNumber != sorted[j].BlockNumber {
			return sorted[i].BlockNumber < sorted[j].BlockNumber
		}
		return sorted[i].LogIndex < sorted[j].LogIndex
	})
	g := &Graph{
		Out:       map[common.Address][]store.Transfer{},
		In:        map[common.Address][]store.Transfer{},
		FirstSeen: map[common.Address]uint64{},
	}
	seen := map[[32]byte]bool{} // dedupe (tx, logIndex) if the loader overlapped
	for _, t := range sorted {
		var k [32]byte
		copy(k[:], t.TxHash[:])
		k[0] ^= byte(t.LogIndex)
		k[1] ^= byte(t.LogIndex >> 8)
		if seen[k] {
			continue
		}
		seen[k] = true
		g.Out[t.From] = append(g.Out[t.From], t)
		g.In[t.To] = append(g.In[t.To], t)
		for _, a := range [...]common.Address{t.From, t.To} {
			if _, ok := g.FirstSeen[a]; !ok {
				g.FirstSeen[a] = t.BlockNumber
			}
		}
	}
	return g
}

// Neighbors returns counterparties of a in deterministic order (by first transfer).
func (g *Graph) Neighbors(a common.Address) []common.Address {
	var out []common.Address
	seen := map[common.Address]bool{}
	for _, t := range g.Out[a] {
		if !seen[t.To] {
			seen[t.To] = true
			out = append(out, t.To)
		}
	}
	for _, t := range g.In[a] {
		if !seen[t.From] {
			seen[t.From] = true
			out = append(out, t.From)
		}
	}
	return out
}
