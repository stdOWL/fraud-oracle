package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"fraud-oracle/service/internal/rules"
	"fraud-oracle/service/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Truncate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestScoreEndpoint(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	A, B, S := common.BigToAddress(big.NewInt(1)), common.BigToAddress(big.NewInt(2)), common.BigToAddress(big.NewInt(666))
	if err := st.InitCheckpoint(ctx, store.Checkpoint{Token: common.HexToAddress("0x1"), ChainID: 1, StartBlock: 1}); err != nil {
		t.Fatal(err)
	}
	ts := []store.Transfer{
		{BlockNumber: 10, TxHash: common.BigToHash(big.NewInt(1)), From: A, To: B, Value: big.NewInt(1)},
		{BlockNumber: 20, TxHash: common.BigToHash(big.NewInt(2)), From: B, To: S, Value: big.NewInt(1)},
	}
	if err := st.CommitRange(ctx, 1, 100, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdvanceCheckpoint(ctx); err != nil {
		t.Fatal(err)
	}

	srv := New(st, []rules.Rule{rules.DefaultPeelChain(), rules.NewSanctions([]common.Address{S}, 3)},
		slog.New(slog.NewTextHandler(os.Stderr, nil)))

	get := func(url string) (int, ScoreResponse, string) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var resp ScoreResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp, rec.Body.String()
	}

	code, resp, body := get("/score?address=" + A.Hex())
	if code != 200 || resp.Score != 50 || resp.RuleBitmask != 2 || len(resp.Rules) != 1 || resp.IndexedThroughBlock != 100 {
		t.Fatalf("A at head: code=%d body=%s", code, body)
	}
	// Same address, pinned before the second hop existed: sanctions unreachable.
	code, resp, body = get("/score?address=" + A.Hex() + "&block=15")
	if code != 200 || resp.Score != 0 || resp.IndexedThroughBlock != 15 {
		t.Fatalf("A at block 15: code=%d body=%s", code, body)
	}
	// block beyond checkpoint is clamped.
	_, resp, _ = get("/score?address=" + A.Hex() + "&block=999999")
	if resp.IndexedThroughBlock != 100 {
		t.Fatalf("clamp: got %d", resp.IndexedThroughBlock)
	}
	// Determinism: identical bytes on repeat.
	_, _, b1 := get("/score?address=" + A.Hex())
	_, _, b2 := get("/score?address=" + A.Hex())
	if b1 != b2 {
		t.Fatalf("non-deterministic body:\n%s\n%s", b1, b2)
	}
	if code, _, _ = get("/score?address=nope"); code != 400 {
		t.Fatalf("bad address: code=%d", code)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "score_latency_seconds_bucket") {
		t.Fatalf("metrics missing histogram")
	}
}
