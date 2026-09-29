// Package score combines rule findings into the API result.
package score

import (
	"github.com/ethereum/go-ethereum/common"

	"fraud-oracle/service/internal/graph"
	"fraud-oracle/service/internal/rules"
)

type RuleResult struct {
	Name     string        `json:"name"`
	Evidence []common.Hash `json:"evidence"`
}

type Result struct {
	Score       int          // max over rules; 0..100
	RuleBitmask uint32       // OR of fired rules' bits
	Rules       []RuleResult // fired rules only, in evaluation order
}

// Evaluate runs every rule. Score is the max, not the sum, so 100 stays "certain".
func Evaluate(g *graph.Graph, addr common.Address, rs []rules.Rule) Result {
	var r Result
	for _, rule := range rs {
		f := rule.Evaluate(g, addr)
		if f.Score == 0 {
			continue
		}
		r.Score = max(r.Score, f.Score)
		r.RuleBitmask |= rule.Bit()
		ev := f.Evidence
		if ev == nil {
			ev = []common.Hash{}
		}
		r.Rules = append(r.Rules, RuleResult{Name: rule.Name(), Evidence: ev})
	}
	return r
}
