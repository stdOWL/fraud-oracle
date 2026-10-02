package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/smartcontractkit/chainlink-protos/cre/go/values/pb"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm/bindings"
	evmmock "github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm/mock"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/networking/http"
	httpmock "github.com/smartcontractkit/cre-sdk-go/capabilities/networking/http/mock"
	"github.com/smartcontractkit/cre-sdk-go/cre/testutils"
	"github.com/stretchr/testify/require"

	"fraud-oracle/workflow/contracts/evm/src/generated/erc20"
)

var (
	alice    = common.HexToAddress("0x1111111111111111111111111111111111111111")
	bob      = common.HexToAddress("0x2222222222222222222222222222222222222222")
	registry = common.HexToAddress("0x3333333333333333333333333333333333333333")
)

func testConfig() *Config {
	return &Config{
		ChainName: "ethereum-testnet-sepolia", TokenAddress: "0x4444444444444444444444444444444444444444",
		RegistryAddress: registry.Hex(), ScoreURL: "http://svc", Threshold: 50, GasLimit: 500000,
	}
}

func transferLog(from, to common.Address, block int64) *bindings.DecodedLog[erc20.TransferDecoded] {
	return &bindings.DecodedLog[erc20.TransferDecoded]{
		Data: erc20.TransferDecoded{From: from, To: to, Value: big.NewInt(1)},
		Log:  &evm.Log{BlockNumber: pb.NewBigIntFromInt(big.NewInt(block)), TxHash: make([]byte, 32)},
	}
}

// scores maps address hex (lowercase) -> score; the stub mimics GET /score.
func wire(t *testing.T, scores map[string]int) (*testutils.TestRuntime, *[]FlagReportSeen) {
	t.Helper()
	httpMock, err := httpmock.NewClientCapability(t)
	require.NoError(t, err)
	httpMock.SendRequest = func(_ context.Context, req *http.Request) (*http.Response, error) {
		require.Equal(t, "GET", req.Method)
		require.Contains(t, req.Url, "&block=777")
		addr := strings.ToLower(req.Url[strings.Index(req.Url, "address=")+8 : strings.Index(req.Url, "&block")])
		s, ok := scores[addr]
		if !ok {
			return &http.Response{StatusCode: 500}, nil
		}
		body, _ := json.Marshal(map[string]any{"address": addr, "score": s, "ruleBitmask": 2, "indexedThroughBlock": 777})
		return &http.Response{StatusCode: 200, Body: body}, nil
	}

	evmMock, err := evmmock.NewClientCapability(uint64(evm.EthereumTestnetSepolia), t)
	require.NoError(t, err)
	var seen []FlagReportSeen
	evmmock.AddContractMock(registry, evmMock, nil, func(report []byte, cfg *evm.GasConfig) (*evm.WriteReportReply, error) {
		seen = append(seen, FlagReportSeen{Report: report, Gas: cfg.GasLimit})
		h := common.HexToHash(fmt.Sprintf("0x%064x", len(seen)))
		return &evm.WriteReportReply{TxStatus: evm.TxStatus_TX_STATUS_SUCCESS, TxHash: h[:]}, nil
	})
	return testutils.NewRuntime(t, nil), &seen
}

type FlagReportSeen struct {
	Report []byte
	Gas    uint64
}

func TestFlagsOnlyAboveThreshold(t *testing.T) {
	rt, seen := wire(t, map[string]int{strings.ToLower(alice.Hex()): 80, strings.ToLower(bob.Hex()): 10})
	res, err := onTransfer(testConfig(), rt, transferLog(alice, bob, 777))
	require.NoError(t, err)
	require.Equal(t, uint64(777), res.Block)
	require.Len(t, res.Verdicts, 2)
	require.True(t, res.Verdicts[0].Flagged)
	require.NotEmpty(t, res.Verdicts[0].TxHash)
	require.False(t, res.Verdicts[1].Flagged)
	require.Len(t, *seen, 1)
	require.Equal(t, uint64(500000), (*seen)[0].Gas)
	// Report is abi.encode(FlagReport{subject, score, bitmask}): 3 words, subject in word 0.
	require.Len(t, (*seen)[0].Report, 96)
	require.Equal(t, alice, common.BytesToAddress((*seen)[0].Report[12:32]))
	require.Equal(t, int64(80), new(big.Int).SetBytes((*seen)[0].Report[32:64]).Int64())
	require.Equal(t, int64(2), new(big.Int).SetBytes((*seen)[0].Report[64:96]).Int64())
}

func TestSkipsZeroAddressAndDedupes(t *testing.T) {
	rt, seen := wire(t, map[string]int{strings.ToLower(alice.Hex()): 99})
	res, err := onTransfer(testConfig(), rt, transferLog(common.Address{}, alice, 777)) // mint
	require.NoError(t, err)
	require.Len(t, res.Verdicts, 1)
	require.Len(t, *seen, 1)

}

func TestSelfTransferScoredOnce(t *testing.T) {
	rt, seen := wire(t, map[string]int{strings.ToLower(alice.Hex()): 99})
	res, err := onTransfer(testConfig(), rt, transferLog(alice, alice, 777))
	require.NoError(t, err)
	require.Len(t, res.Verdicts, 1)
	require.Len(t, *seen, 1)
}

func TestServiceErrorAbortsWithoutWrite(t *testing.T) {
	rt, seen := wire(t, map[string]int{}) // every lookup -> HTTP 500
	_, err := onTransfer(testConfig(), rt, transferLog(alice, bob, 777))
	require.Error(t, err)
	require.Contains(t, err.Error(), "HTTP 500")
	require.Empty(t, *seen)
}

func TestInitRejectsBadThreshold(t *testing.T) {
	cfg := testConfig()
	cfg.Threshold = 0
	_, err := InitWorkflow(cfg, nil, nil)
	require.Error(t, err)
}
