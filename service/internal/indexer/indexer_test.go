package indexer

import (
	"context"
	"log/slog"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"

	"fraud-oracle/service/internal/rpc"
	"fraud-oracle/service/internal/store"
)

// Tests need a real Postgres. Set TEST_DATABASE_URL (docker compose up gives
// postgres://fraud:fraud@localhost:5432/fraud?sslmode=disable). Skipped otherwise.
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

func synthTransfers(n int, firstBlock uint64) []store.Transfer {
	out := make([]store.Transfer, n)
	for i := range out {
		out[i] = store.Transfer{
			BlockNumber: firstBlock + uint64(i),
			TxHash:      common.BigToHash(big.NewInt(int64(i + 1))),
			LogIndex:    0,
			From:        common.BigToAddress(big.NewInt(int64(i + 100))),
			To:          common.BigToAddress(big.NewInt(int64(i + 101))),
			Value:       big.NewInt(1e18),
		}
	}
	return out
}

func newIndexer(t *testing.T, src rpc.LogSource, st *store.Store, workers int) *Indexer {
	t.Helper()
	m := NewMetrics(prometheus.NewRegistry())
	return New(Config{Token: common.HexToAddress("0x1"), StartBlocksBack: 10000, Workers: workers},
		src, st, m, slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func TestBackfillThenResume(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	const head, chainID = 20_064, 11155111 // safe head = 20_000; start = 10_000
	ts := synthTransfers(50, 15_000)
	fake := rpc.NewFake(head, chainID, ts)

	// Run 1: clean backfill of (10000, 20000] with 2 concurrent workers.
	ix := newIndexer(t, fake, st, 2)
	if err := ix.Run(ctx); err != nil {
		t.Fatalf("run1: %v", err)
	}
	cp, err := st.GetCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cp.StartBlock != 10_000 || cp.IndexedThrough != 20_000 {
		t.Fatalf("checkpoint = %+v, want start 10000 through 20000", cp)
	}
	n, _ := st.CountTransfers(ctx)
	if n != 50 {
		t.Fatalf("transfers = %d, want 50", n)
	}
	callsAfterRun1 := fake.Calls // 5 ranges of 2000

	// Run 2 with a higher head: only the new window is fetched, nothing re-scanned.
	fake.SetHead(head + 4000)
	ix2 := newIndexer(t, fake, st, 2)
	if err := ix2.Run(ctx); err != nil {
		t.Fatalf("run2: %v", err)
	}
	cp, _ = st.GetCheckpoint(ctx)
	if cp.IndexedThrough != 24_000 {
		t.Fatalf("through = %d, want 24000", cp.IndexedThrough)
	}
	if got := fake.Calls - callsAfterRun1; got != 2 {
		t.Fatalf("run2 made %d getLogs calls, want 2 (no re-scan)", got)
	}
	n, _ = st.CountTransfers(ctx)
	if n != 50 {
		t.Fatalf("transfers after resume = %d, want 50 (idempotent)", n)
	}
}

func TestPartialFailureResumesWithoutRescan(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	fake := rpc.NewFake(20_064, 1, synthTransfers(20, 12_000))
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = 500 * time.Millisecond })

	// Fail hard on every call after the first two succeed: retries exhaust, Run returns error.
	ix := newIndexer(t, fake, st, 1) // single worker => deterministic range order
	ix.src = &countingSource{LogSource: fake, failAfter: 2}
	if err := ix.Run(ctx); err == nil {
		t.Fatal("run1 expected error")
	}
	cp, _ := st.GetCheckpoint(ctx)
	if cp.IndexedThrough != 13_999 { // ranges [10000,11999] and [12000,13999] committed
		t.Fatalf("through after failure = %d, want 13999", cp.IndexedThrough)
	}

	// Run 2: no failures; only the remaining 4 ranges ([14000..20000]) are fetched.
	fake.Calls = 0
	ix2 := newIndexer(t, fake, st, 1)
	if err := ix2.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.Calls != 4 {
		t.Fatalf("run2 getLogs calls = %d, want 4", fake.Calls)
	}
	cp, _ = st.GetCheckpoint(ctx)
	if cp.IndexedThrough != 20_000 {
		t.Fatalf("through = %d, want 20000", cp.IndexedThrough)
	}
	n, _ := st.CountTransfers(ctx)
	if n != 20 {
		t.Fatalf("transfers = %d, want 20", n)
	}
}

func TestRefusesDifferentToken(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	fake := rpc.NewFake(20_064, 1, nil)
	if err := newIndexer(t, fake, st, 1).Run(ctx); err != nil {
		t.Fatal(err)
	}
	ix := newIndexer(t, fake, st, 1)
	ix.cfg.Token = common.HexToAddress("0x2")
	if err := ix.Run(ctx); err == nil {
		t.Fatal("expected refusal on token mismatch")
	}
}

// countingSource fails permanently after N successful Transfers calls.
type countingSource struct {
	rpc.LogSource
	ok, failAfter int
}

func (c *countingSource) Transfers(ctx context.Context, from, to uint64) ([]store.Transfer, error) {
	if c.ok >= c.failAfter {
		return nil, errFatal
	}
	c.ok++
	return c.LogSource.Transfers(ctx, from, to)
}

var errFatal = &fatalErr{}

type fatalErr struct{}

func (*fatalErr) Error() string { return "fake rpc: permanent failure" }
