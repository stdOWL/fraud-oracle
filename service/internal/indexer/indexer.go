// Package indexer backfills a block window once, then follows the chain head.
//
// Lifecycle: Run blocks until ctx is cancelled or a fatal error occurs. Workers share
// the parent ctx through errgroup, so one failing worker cancels the others and Run
// returns the first error. Nothing outlives Run.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"

	"fraud-oracle/service/internal/rpc"
	"fraud-oracle/service/internal/store"
)

const (
	rangeSize     = 2000 // blocks per eth_getLogs call
	finalityDepth = 64   // only index blocks <= head-64; no reorg handling needed
	maxAttempts   = 5
)

// retryBase is a var so tests can shrink the backoff.
var retryBase = 500 * time.Millisecond

type Config struct {
	Token           common.Address
	StartBlocksBack uint64
	Workers         int
	PollInterval    time.Duration
	// FollowHead false = exit after backfill (tests).
	FollowHead bool
}

type Metrics struct {
	BlocksIndexed prometheus.Counter
	HeadLag       prometheus.Gauge
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		BlocksIndexed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "indexer_blocks_indexed_total", Help: "Blocks committed to the checkpoint."}),
		HeadLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "indexer_head_lag_blocks", Help: "Chain head minus indexed_through."}),
	}
	reg.MustRegister(m.BlocksIndexed, m.HeadLag)
	return m
}

type Indexer struct {
	cfg  Config
	src  rpc.LogSource
	st   *store.Store
	m    *Metrics
	log  *slog.Logger
	advM sync.Mutex // serialises AdvanceCheckpoint; workers finish out of order
}

func New(cfg Config, src rpc.LogSource, st *store.Store, m *Metrics, log *slog.Logger) *Indexer {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 12 * time.Second
	}
	return &Indexer{cfg: cfg, src: src, st: st, m: m, log: log}
}

// Run resumes from the checkpoint (or creates one) and indexes up to the safe head.
func (ix *Indexer) Run(ctx context.Context) error {
	chainID, err := ix.src.ChainID(ctx)
	if err != nil {
		return err
	}
	head, err := ix.src.Head(ctx)
	if err != nil {
		return err
	}
	safe := safeHead(head)

	cp, err := ix.st.GetCheckpoint(ctx)
	switch {
	case errors.Is(err, store.ErrNoCheckpoint):
		start := uint64(1)
		if safe > ix.cfg.StartBlocksBack {
			start = safe - ix.cfg.StartBlocksBack
		}
		cp = store.Checkpoint{Token: ix.cfg.Token, ChainID: chainID, StartBlock: start, IndexedThrough: start - 1}
		if err := ix.st.InitCheckpoint(ctx, cp); err != nil {
			return err
		}
		ix.log.Info("checkpoint created", "start", start, "safeHead", safe)
	case err != nil:
		return err
	default:
		if cp.Token != ix.cfg.Token || cp.ChainID != chainID {
			return fmt.Errorf("indexer: checkpoint is for token %s chain %d, config is token %s chain %d; refusing to mix",
				cp.Token.Hex(), cp.ChainID, ix.cfg.Token.Hex(), chainID)
		}
		ix.log.Info("checkpoint resumed", "indexedThrough", cp.IndexedThrough, "safeHead", safe)
	}

	for {
		through, err := ix.catchUp(ctx, cp.IndexedThrough, safe)
		if err != nil {
			return err
		}
		cp.IndexedThrough = through
		if !ix.cfg.FollowHead {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(ix.cfg.PollInterval):
		}
		if head, err = ix.src.Head(ctx); err != nil {
			ix.log.Warn("head poll failed", "err", err)
			continue
		}
		safe = safeHead(head)
		ix.m.HeadLag.Set(float64(head - cp.IndexedThrough))
	}
}

func safeHead(head uint64) uint64 {
	if head <= finalityDepth {
		return 0
	}
	return head - finalityDepth
}

// catchUp indexes (from, to] with concurrent workers and returns the new indexed_through.
func (ix *Indexer) catchUp(ctx context.Context, from, to uint64) (uint64, error) {
	if to <= from {
		return from, nil
	}
	pending, err := ix.st.PendingRanges(ctx)
	if err != nil {
		return 0, err
	}

	type job struct{ from, to uint64 }
	jobs := make(chan job)
	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		defer close(jobs)
		for lo := from + 1; lo <= to; lo += rangeSize {
			hi := min(lo+rangeSize-1, to)
			if done, ok := pending[lo]; ok && done == hi {
				continue // committed before a previous restart, not yet consumed
			}
			select {
			case jobs <- job{lo, hi}:
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		return nil
	})

	for i := 0; i < ix.cfg.Workers; i++ {
		g.Go(func() error {
			for j := range jobs {
				ts, err := ix.fetchWithRetry(gctx, j.from, j.to)
				if err != nil {
					return err
				}
				if err := ix.st.CommitRange(gctx, j.from, j.to, ts); err != nil {
					return err
				}
				if err := ix.advance(gctx); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return 0, err
	}
	// Final advance covers ranges skipped via `pending` when no worker ran.
	if err := ix.advance(ctx); err != nil {
		return 0, err
	}
	cp, err := ix.st.GetCheckpoint(ctx)
	if err != nil {
		return 0, err
	}
	return cp.IndexedThrough, nil
}

func (ix *Indexer) advance(ctx context.Context) error {
	ix.advM.Lock()
	defer ix.advM.Unlock()
	before, err := ix.st.GetCheckpoint(ctx)
	if err != nil {
		return err
	}
	after, err := ix.st.AdvanceCheckpoint(ctx)
	if err != nil {
		return err
	}
	if after > before.IndexedThrough {
		ix.m.BlocksIndexed.Add(float64(after - before.IndexedThrough))
		ix.log.Info("checkpoint advanced", "indexedThrough", after)
	}
	return nil
}

// fetchWithRetry retries transient RPC errors with backoff. A range that is too large for the
// provider is split in half recursively.
func (ix *Indexer) fetchWithRetry(ctx context.Context, from, to uint64) ([]store.Transfer, error) {
	backoff := retryBase
	for attempt := 0; ; attempt++ {
		ts, err := ix.src.Transfers(ctx, from, to)
		if err == nil {
			return ts, nil
		}
		if isTooManyResults(err) && to > from {
			mid := from + (to-from)/2
			a, err := ix.fetchWithRetry(ctx, from, mid)
			if err != nil {
				return nil, err
			}
			b, err := ix.fetchWithRetry(ctx, mid+1, to)
			if err != nil {
				return nil, err
			}
			return append(a, b...), nil
		}
		if attempt >= maxAttempts {
			return nil, fmt.Errorf("indexer: range [%d,%d] failed after retries: %w", from, to, err)
		}
		ix.log.Warn("rpc error, retrying", "from", from, "to", to, "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// isTooManyResults matches provider-specific "log limit" errors (geth, Alchemy, Infura wording).
func isTooManyResults(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "more than") || strings.Contains(s, "too many") ||
		strings.Contains(s, "exceed") || strings.Contains(s, "limit")
}
