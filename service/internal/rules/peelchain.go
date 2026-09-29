package rules

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"

	"fraud-oracle/service/internal/graph"
	"fraud-oracle/service/internal/store"
)

// PeelChain detects funds moving through a chain of fresh addresses, each hop forwarding
// most of what it received and leaving a small remainder behind.
//
// A hop A -> B counts when:
//   - B had no activity before receiving from A (fresh),
//   - B then sends onward, within MaxHopBlocks, at least ForwardRatio of what it received.
//
// The chain is followed greedily along the largest qualifying outgoing transfer.
// Score: 0 below MinHops, then 30 + 20 per extra hop, capped at 100.
type PeelChain struct {
	MinHops      int
	ForwardRatio float64 // e.g. 0.90
	MaxHopBlocks uint64
}

func DefaultPeelChain() PeelChain {
	return PeelChain{MinHops: 3, ForwardRatio: 0.90, MaxHopBlocks: 1000}
}

func (PeelChain) Name() string { return "peel_chain" }
func (PeelChain) Bit() uint32  { return 1 << 0 }

func (p PeelChain) Evaluate(g *graph.Graph, addr common.Address) Finding {
	var best []common.Hash
	// The subject may be the origin of the chain: try each outgoing transfer as hop 1.
	for _, first := range g.Out[addr] {
		chain := p.follow(g, first, map[common.Address]bool{addr: true})
		if len(chain) > len(best) {
			best = chain
		}
	}
	hops := len(best)
	if hops < p.MinHops {
		return Finding{}
	}
	return Finding{Score: min(100, 30+20*(hops-p.MinHops)), Evidence: sortedHashes(best)}
}

// follow extends the chain from transfer t as long as the receiver keeps peeling.
func (p PeelChain) follow(g *graph.Graph, t store.Transfer, visited map[common.Address]bool) []common.Hash {
	chain := []common.Hash{t.TxHash}
	cur := t
	for {
		recv := cur.To
		if visited[recv] {
			return chain
		}
		visited[recv] = true
		if g.FirstSeen[recv] != cur.BlockNumber {
			return chain // receiver was active before this hop: not fresh
		}
		next, ok := p.forwardHop(g, recv, cur)
		if !ok || visited[next.To] {
			return chain // no forward, or funds loop back: a round trip is not a peel
		}
		chain = append(chain, next.TxHash)
		cur = next
	}
}

// forwardHop finds the largest transfer out of recv, after received, within the window,
// and accepts it if it forwards at least ForwardRatio of the received value.
func (p PeelChain) forwardHop(g *graph.Graph, recv common.Address, received store.Transfer) (store.Transfer, bool) {
	var best store.Transfer
	found := false
	for _, o := range g.Out[recv] {
		if o.BlockNumber < received.BlockNumber || o.BlockNumber > received.BlockNumber+p.MaxHopBlocks {
			continue
		}
		if o.BlockNumber == received.BlockNumber && o.LogIndex <= received.LogIndex {
			continue
		}
		if !found || o.Value.Cmp(best.Value) > 0 {
			best, found = o, true
		}
	}
	if !found {
		return store.Transfer{}, false
	}
	// best.Value >= ForwardRatio * received.Value, in integer math: best*1000 >= ratio*1000*received
	lhs := new(big.Int).Mul(best.Value, big.NewInt(1000))
	rhs := new(big.Int).Mul(received.Value, big.NewInt(int64(p.ForwardRatio*1000)))
	if lhs.Cmp(rhs) < 0 {
		return store.Transfer{}, false
	}
	return best, true
}
