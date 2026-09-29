package rules

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/ethereum/go-ethereum/common"

	"fraud-oracle/service/internal/graph"
)

// Sanctions scores an address by its undirected hop distance to any address on a static list.
// Distance 0 (the address itself) = 100, 1 = 80, 2 = 50, 3 = 25, beyond MaxHops = 0.
// Evidence is the transfer path from the subject to the nearest sanctioned address.
type Sanctions struct {
	MaxHops int
	list    map[common.Address]bool
}

var distanceScore = [...]int{100, 80, 50, 25}

// LoadSanctions reads a JSON file: {"addresses": ["0x...", ...]}.
func LoadSanctions(path string, maxHops int) (*Sanctions, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sanctions: read %s: %w", path, err)
	}
	var f struct {
		Addresses []string `json:"addresses"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("sanctions: parse %s: %w", path, err)
	}
	addrs := make([]common.Address, 0, len(f.Addresses))
	for _, s := range f.Addresses {
		if !common.IsHexAddress(s) {
			return nil, fmt.Errorf("sanctions: bad address %q", s)
		}
		addrs = append(addrs, common.HexToAddress(s))
	}
	return NewSanctions(addrs, maxHops), nil
}

func NewSanctions(addrs []common.Address, maxHops int) *Sanctions {
	if maxHops >= len(distanceScore) {
		maxHops = len(distanceScore) - 1
	}
	s := &Sanctions{MaxHops: maxHops, list: make(map[common.Address]bool, len(addrs))}
	for _, a := range addrs {
		s.list[a] = true
	}
	return s
}

func (*Sanctions) Name() string { return "sanctions_proximity" }
func (*Sanctions) Bit() uint32  { return 1 << 1 }
func (s *Sanctions) Size() int  { return len(s.list) }

func (s *Sanctions) Evaluate(g *graph.Graph, addr common.Address) Finding {
	if s.list[addr] {
		return Finding{Score: distanceScore[0]}
	}
	parent := map[common.Address]node{addr: {addr: addr}}
	frontier := []common.Address{addr}
	for depth := 1; depth <= s.MaxHops && len(frontier) > 0; depth++ {
		var next []common.Address
		for _, a := range frontier {
			for _, e := range edges(g, a) {
				if _, seen := parent[e.other]; seen {
					continue
				}
				parent[e.other] = node{addr: e.other, via: e.tx, prev: a}
				if s.list[e.other] {
					return Finding{Score: distanceScore[depth], Evidence: sortedHashes(pathTo(parent, e.other))}
				}
				next = append(next, e.other)
			}
		}
		frontier = next
	}
	return Finding{}
}

// node is a BFS bookkeeping entry: how we reached addr.
type node struct {
	addr common.Address
	via  common.Hash // transfer that reached this node
	prev common.Address
}

type edge struct {
	other common.Address
	tx    common.Hash
}

// edges lists undirected edges of a in deterministic order (out first, then in, each by block).
func edges(g *graph.Graph, a common.Address) []edge {
	out := make([]edge, 0, len(g.Out[a])+len(g.In[a]))
	for _, t := range g.Out[a] {
		out = append(out, edge{t.To, t.TxHash})
	}
	for _, t := range g.In[a] {
		out = append(out, edge{t.From, t.TxHash})
	}
	return out
}

func pathTo(parent map[common.Address]node, end common.Address) []common.Hash {
	var path []common.Hash
	for cur := end; ; {
		n := parent[cur]
		if n.prev == (common.Address{}) && n.via == (common.Hash{}) {
			break
		}
		path = append(path, n.via)
		cur = n.prev
	}
	return path
}

func sortedHashes(h []common.Hash) []common.Hash {
	out := make([]common.Hash, len(h))
	copy(out, h)
	sort.Slice(out, func(i, j int) bool { return out[i].Hex() < out[j].Hex() })
	return out
}
