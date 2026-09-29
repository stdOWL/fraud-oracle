package rules

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"fraud-oracle/service/internal/graph"
	"fraud-oracle/service/internal/store"
)

func addr(n int64) common.Address { return common.BigToAddress(big.NewInt(n)) }
func hash(n int64) common.Hash    { return common.BigToHash(big.NewInt(n)) }

// tx builds a synthetic transfer; value in whole tokens.
func tx(id int64, block uint64, from, to common.Address, value int64) store.Transfer {
	return store.Transfer{
		BlockNumber: block, TxHash: hash(id), LogIndex: uint32(id % 7),
		From: from, To: to, Value: new(big.Int).Mul(big.NewInt(value), big.NewInt(1e18)),
	}
}

func TestPeelChain(t *testing.T) {
	A, B, C, D, E, X := addr(1), addr(2), addr(3), addr(4), addr(5), addr(99)
	cases := []struct {
		name      string
		ts        []store.Transfer
		wantScore int
		wantEvid  int
	}{
		{
			name: "three fresh hops forwarding 95%",
			ts: []store.Transfer{
				tx(1, 100, A, B, 100), tx(2, 101, B, C, 95), tx(3, 102, C, D, 90),
			},
			wantScore: 30, wantEvid: 3,
		},
		{
			name: "four hops scores higher",
			ts: []store.Transfer{
				tx(1, 100, A, B, 100), tx(2, 101, B, C, 95), tx(3, 102, C, D, 90), tx(4, 103, D, E, 86),
			},
			wantScore: 50, wantEvid: 4,
		},
		{
			name: "two hops is below threshold",
			ts:   []store.Transfer{tx(1, 100, A, B, 100), tx(2, 101, B, C, 95)},
		},
		{
			name: "receiver not fresh breaks chain",
			ts: []store.Transfer{
				tx(9, 50, X, C, 1), // C active before the chain reaches it
				tx(1, 100, A, B, 100), tx(2, 101, B, C, 95), tx(3, 102, C, D, 90),
			},
		},
		{
			name: "forwarding only half breaks chain",
			ts: []store.Transfer{
				tx(1, 100, A, B, 100), tx(2, 101, B, C, 50), tx(3, 102, C, D, 45),
			},
		},
		{
			name: "hop outside block window breaks chain",
			ts: []store.Transfer{
				tx(1, 100, A, B, 100), tx(2, 101, B, C, 95), tx(3, 5000, C, D, 90),
			},
		},
		{
			name: "cycle terminates",
			ts: []store.Transfer{
				tx(1, 100, A, B, 100), tx(2, 101, B, C, 95), tx(3, 102, C, A, 90),
			},
		},
		{name: "no transfers"},
	}
	rule := DefaultPeelChain()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := rule.Evaluate(graph.New(c.ts), A)
			if f.Score != c.wantScore || len(f.Evidence) != c.wantEvid {
				t.Fatalf("got score=%d evidence=%d, want score=%d evidence=%d", f.Score, len(f.Evidence), c.wantScore, c.wantEvid)
			}
		})
	}
}

func TestSanctions(t *testing.T) {
	A, B, C, D, S := addr(1), addr(2), addr(3), addr(4), addr(666)
	rule := NewSanctions([]common.Address{S}, 3)
	cases := []struct {
		name      string
		ts        []store.Transfer
		subject   common.Address
		wantScore int
		wantEvid  int
	}{
		{name: "listed address itself", subject: S, wantScore: 100},
		{name: "direct counterparty (received from)", ts: []store.Transfer{tx(1, 1, S, A, 1)}, subject: A, wantScore: 80, wantEvid: 1},
		{name: "direct counterparty (sent to)", ts: []store.Transfer{tx(1, 1, A, S, 1)}, subject: A, wantScore: 80, wantEvid: 1},
		{name: "two hops", ts: []store.Transfer{tx(1, 1, A, B, 1), tx(2, 2, B, S, 1)}, subject: A, wantScore: 50, wantEvid: 2},
		{name: "three hops", ts: []store.Transfer{tx(1, 1, A, B, 1), tx(2, 2, B, C, 1), tx(3, 3, C, S, 1)}, subject: A, wantScore: 25, wantEvid: 3},
		{name: "four hops is out of range", ts: []store.Transfer{tx(1, 1, A, B, 1), tx(2, 2, B, C, 1), tx(3, 3, C, D, 1), tx(4, 4, D, S, 1)}, subject: A},
		{name: "nearest path wins", ts: []store.Transfer{tx(1, 1, A, B, 1), tx(2, 2, B, S, 1), tx(3, 3, A, S, 1)}, subject: A, wantScore: 80, wantEvid: 1},
		{name: "unrelated graph", ts: []store.Transfer{tx(1, 1, B, C, 1)}, subject: A},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := rule.Evaluate(graph.New(c.ts), c.subject)
			if f.Score != c.wantScore || len(f.Evidence) != c.wantEvid {
				t.Fatalf("got score=%d evidence=%d, want score=%d evidence=%d", f.Score, len(f.Evidence), c.wantScore, c.wantEvid)
			}
		})
	}
}

func TestDeterministicAcrossInputOrder(t *testing.T) {
	A, B, C, S := addr(1), addr(2), addr(3), addr(666)
	ts := []store.Transfer{tx(1, 1, A, B, 100), tx(2, 2, B, C, 95), tx(3, 3, C, S, 90)}
	rev := []store.Transfer{ts[2], ts[1], ts[0]}
	for _, r := range []Rule{DefaultPeelChain(), NewSanctions([]common.Address{S}, 3)} {
		f1, f2 := r.Evaluate(graph.New(ts), A), r.Evaluate(graph.New(rev), A)
		if f1.Score != f2.Score || len(f1.Evidence) != len(f2.Evidence) {
			t.Fatalf("%s: not deterministic", r.Name())
		}
		for i := range f1.Evidence {
			if f1.Evidence[i] != f2.Evidence[i] {
				t.Fatalf("%s: evidence order differs", r.Name())
			}
		}
	}
}
