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
	// The subject may be a mule in the middle of a chain, not its origin. Walk back along
	// qualifying hops to the origin first; the chain is then scored from there, so every
	// address on it gets the same score and the same evidence.
	origin := p.origin(g, addr)
	var best []common.Hash
	for _, first := range g.Out[origin] {
		chain := p.follow(g, first, map[common.Address]bool{origin: true})
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

// origin walks upstream from addr while addr (then its payer, and so on) looks like a
// peel hop: fresh at the incoming transfer and forwarding most of it. Returns the first
// address that is not itself a hop. visited guards cycles.
func (p PeelChain) origin(g *graph.Graph, addr common.Address) common.Address {
	visited := map[common.Address]bool{addr: true}
	cur := addr
	for {
		var in store.Transfer
		found := false
		for _, t := range g.In[cur] {
			if g.FirstSeen[cur] != t.BlockNumber || visited[t.From] {
				continue
			}
			if _, ok := p.forwardHop(g, cur, t); !ok {
				continue
			}
			if !found || t.Value.Cmp(in.Value) > 0 {
				in, found = t, true
			}
		}
		if !found {
			return cur
		}
		visited[in.From] = true
		cur = in.From
	}
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
