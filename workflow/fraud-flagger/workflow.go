// Package main is the CRE workflow: on every finalized ERC20 Transfer, ask the offchain
// fraud service to score both parties (each DON node asks independently, results must
// match), and write a FlagReport to FraudRegistry for any party at or above the threshold.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm/bindings"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/networking/http"
	"github.com/smartcontractkit/cre-sdk-go/cre"
	"google.golang.org/protobuf/types/known/durationpb"

	"fraud-oracle/workflow/contracts/evm/src/generated/erc20"
	"fraud-oracle/workflow/contracts/evm/src/generated/fraud_registry"
)

type Config struct {
	ChainName       string `json:"chainName"`
	TokenAddress    string `json:"tokenAddress"`
	RegistryAddress string `json:"registryAddress"`
	ScoreURL        string `json:"scoreUrl"`  // base URL of the fraud service, no trailing slash
	Threshold       int    `json:"threshold"` // flag when score >= threshold
	GasLimit        uint64 `json:"gasLimit"`
}

// ScoreResponse is the subset of GET /score the workflow needs. Every node must see the
// same score and bitmask; indexedThroughBlock may differ between nodes and is ignored.
type ScoreResponse struct {
	Address             string `json:"address" consensus_aggregation:"identical"`
	Score               int    `json:"score" consensus_aggregation:"identical"`
	RuleBitmask         uint32 `json:"ruleBitmask" consensus_aggregation:"identical"`
	IndexedThroughBlock uint64 `json:"indexedThroughBlock" consensus_aggregation:"ignore"`
}

type Verdict struct {
	Address string `json:"address"`
	Score   int    `json:"score"`
	Flagged bool   `json:"flagged"`
	TxHash  string `json:"txHash,omitempty"`
}

type Result struct {
	Block    uint64    `json:"block"`
	Verdicts []Verdict `json:"verdicts"`
}

func InitWorkflow(config *Config, _ *slog.Logger, _ cre.SecretsProvider) (cre.Workflow[*Config], error) {
	if config.Threshold < 1 || config.Threshold > 100 {
		return nil, fmt.Errorf("threshold must be 1..100, got %d", config.Threshold)
	}
	selector, err := evm.ChainSelectorFromName(config.ChainName)
	if err != nil {
		return nil, fmt.Errorf("chain %q: %w", config.ChainName, err)
	}
	token, err := erc20.NewERC20(&evm.Client{ChainSelector: selector}, common.HexToAddress(config.TokenAddress), nil)
	if err != nil {
		return nil, err
	}
	// nil filter = every Transfer of this token. FINALIZED so the service can score at that block.
	trigger, err := token.LogTriggerTransferLog(selector, evm.ConfidenceLevel_CONFIDENCE_LEVEL_FINALIZED, nil)
	if err != nil {
		return nil, err
	}
	return cre.Workflow[*Config]{cre.Handler(trigger, onTransfer)}, nil
}

func onTransfer(config *Config, runtime cre.Runtime, payload *bindings.DecodedLog[erc20.TransferDecoded]) (*Result, error) {
	logger := runtime.Logger()
	block := new(big.Int).SetBytes(payload.Log.BlockNumber.GetAbsVal()).Uint64()

	// Mint and burn use the zero address; a self-transfer has one party.
	var parties []common.Address
	for _, a := range []common.Address{payload.Data.From, payload.Data.To} {
		if a != (common.Address{}) && (len(parties) == 0 || parties[0] != a) {
			parties = append(parties, a)
		}
	}

	// Start all score requests before awaiting any: each Await is a consensus round.
	client := &http.Client{}
	promises := make([]cre.Promise[*ScoreResponse], len(parties))
	for i, a := range parties {
		promises[i] = http.SendRequest(config, runtime, client, fetchScore(a, block),
			cre.ConsensusAggregationFromTags[*ScoreResponse]())
	}

	selector, err := evm.ChainSelectorFromName(config.ChainName)
	if err != nil {
		return nil, err
	}
	registry, err := fraud_registry.NewFraudRegistry(&evm.Client{ChainSelector: selector}, common.HexToAddress(config.RegistryAddress), nil)
	if err != nil {
		return nil, err
	}

	res := &Result{Block: block}
	for i, a := range parties {
		s, err := promises[i].Await()
		if err != nil {
			return nil, fmt.Errorf("score %s: %w", a.Hex(), err)
		}
		if s.Score < 0 || s.Score > 100 {
			return nil, fmt.Errorf("score %s: out of range %d", a.Hex(), s.Score)
		}
		v := Verdict{Address: a.Hex(), Score: s.Score}
		logger.Info("scored", "address", v.Address, "score", s.Score, "ruleBitmask", s.RuleBitmask, "block", block)
		if s.Score >= config.Threshold {
			reply, err := registry.WriteReportFromFlagReport(runtime, fraud_registry.FlagReport{
				Subject: a, Score: uint8(s.Score), RuleBitmask: s.RuleBitmask,
			}, &evm.GasConfig{GasLimit: config.GasLimit}).Await()
			if err != nil {
				return nil, fmt.Errorf("flag %s: %w", a.Hex(), err)
			}
			v.Flagged = true
			v.TxHash = common.BytesToHash(reply.TxHash).Hex()
			logger.Info("flag written", "address", v.Address, "txHash", v.TxHash)
		}
		res.Verdicts = append(res.Verdicts, v)
	}
	return res, nil
}

// fetchScore returns the per-node function for http.SendRequest. Pinning ?block= makes
// nodes with different indexer lag return the same answer.
func fetchScore(addr common.Address, block uint64) func(*Config, *slog.Logger, *http.SendRequester) (*ScoreResponse, error) {
	return func(config *Config, _ *slog.Logger, sr *http.SendRequester) (*ScoreResponse, error) {
		resp, err := sr.SendRequest(&http.Request{
			Url:     fmt.Sprintf("%s/score?address=%s&block=%d", config.ScoreURL, addr.Hex(), block),
			Method:  "GET",
			Timeout: durationpb.New(4 * time.Second),
		}).Await()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("fraud service returned HTTP %d", resp.StatusCode)
		}
		var out ScoreResponse
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return nil, fmt.Errorf("decode score: %w", err)
		}
		return &out, nil
	}
}
