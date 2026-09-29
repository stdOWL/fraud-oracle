// Package rules holds the fraud heuristics. Each rule is one type behind Rule and is
// evaluated against an in-memory graph, so it is unit-testable without a chain or a DB.
package rules

import (
	"github.com/ethereum/go-ethereum/common"

	"fraud-oracle/service/internal/graph"
)

// Finding is a rule's verdict. Score 0 means the rule did not fire.
type Finding struct {
	Score    int           // 0..100
	Evidence []common.Hash // tx hashes that justify the score, sorted
}

type Rule interface {
	Name() string
	// Bit is the rule's position in the on-chain ruleBitmask.
	Bit() uint32
	Evaluate(g *graph.Graph, addr common.Address) Finding
}
