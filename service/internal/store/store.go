// Package store is the only place that talks to Postgres.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Transfer is one ERC20 Transfer log.
type Transfer struct {
	BlockNumber uint64
	TxHash      common.Hash
	LogIndex    uint32
	From        common.Address
	To          common.Address
	Value       *big.Int
}

// Checkpoint is the single-row indexer state.
type Checkpoint struct {
	Token          common.Address
	ChainID        uint64
	StartBlock     uint64
	IndexedThrough uint64
}

// ErrNoCheckpoint is returned when the indexer has never run.
var ErrNoCheckpoint = errors.New("store: no checkpoint")

type Store struct {
	pool *pgxpool.Pool
}

// Open connects and applies migrations. Migrations are idempotent (IF NOT EXISTS).
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) migrate(ctx context.Context) error {
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		sql, err := migrations.ReadFile("migrations/" + n)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("store: migrate %s: %w", n, err)
		}
	}
	return nil
}

// GetCheckpoint returns ErrNoCheckpoint on first run.
func (s *Store) GetCheckpoint(ctx context.Context) (Checkpoint, error) {
	var cp Checkpoint
	var token []byte
	err := s.pool.QueryRow(ctx,
		`SELECT token, chain_id, start_block, indexed_through FROM checkpoint WHERE id = 1`,
	).Scan(&token, &cp.ChainID, &cp.StartBlock, &cp.IndexedThrough)
	if errors.Is(err, pgx.ErrNoRows) {
		return cp, ErrNoCheckpoint
	}
	if err != nil {
		return cp, fmt.Errorf("store: get checkpoint: %w", err)
	}
	cp.Token = common.BytesToAddress(token)
	return cp, nil
}

// InitCheckpoint creates the checkpoint row. indexed_through starts at start_block-1.
func (s *Store) InitCheckpoint(ctx context.Context, cp Checkpoint) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO checkpoint (id, token, chain_id, start_block, indexed_through)
		 VALUES (1, $1, $2, $3, $4)`,
		cp.Token.Bytes(), cp.ChainID, cp.StartBlock, cp.StartBlock-1)
	if err != nil {
		return fmt.Errorf("store: init checkpoint: %w", err)
	}
	return nil
}

// CommitRange inserts transfers for [from,to] and records the range as done, atomically.
// Re-running a range is safe: duplicate transfers are ignored, range_done is upserted.
func (s *Store) CommitRange(ctx context.Context, from, to uint64, ts []Transfer) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit

	if len(ts) > 0 {
		batch := &pgx.Batch{}
		for _, t := range ts {
			batch.Queue(
				`INSERT INTO transfers (block_number, tx_hash, log_index, from_addr, to_addr, value)
				 VALUES ($1, $2, $3, $4, $5, $6::numeric) ON CONFLICT DO NOTHING`,
				t.BlockNumber, t.TxHash.Bytes(), t.LogIndex, t.From.Bytes(), t.To.Bytes(), t.Value.String())
		}
		br := tx.SendBatch(ctx, batch)
		for range ts {
			if _, err := br.Exec(); err != nil {
				br.Close()
				return fmt.Errorf("store: insert transfer: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return fmt.Errorf("store: batch close: %w", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO range_done (from_block, to_block) VALUES ($1, $2)
		 ON CONFLICT (from_block) DO UPDATE SET to_block = EXCLUDED.to_block`, from, to); err != nil {
		return fmt.Errorf("store: range_done: %w", err)
	}
	return tx.Commit(ctx)
}

// AdvanceCheckpoint moves indexed_through forward over contiguous completed ranges
// and deletes the consumed range_done rows. Returns the new high-water mark.
func (s *Store) AdvanceCheckpoint(ctx context.Context) (uint64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var through uint64
	if err := tx.QueryRow(ctx,
		`SELECT indexed_through FROM checkpoint WHERE id = 1 FOR UPDATE`).Scan(&through); err != nil {
		return 0, fmt.Errorf("store: lock checkpoint: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT from_block, to_block FROM range_done ORDER BY from_block`)
	if err != nil {
		return 0, fmt.Errorf("store: read range_done: %w", err)
	}
	type rng struct{ from, to uint64 }
	var done []rng
	for rows.Next() {
		var r rng
		if err := rows.Scan(&r.from, &r.to); err != nil {
			rows.Close()
			return 0, err
		}
		done = append(done, r)
	}
	rows.Close()

	// Markers at or below the checkpoint are stale (a range committed twice by concurrent
	// workers or a restart). Consume them without moving the checkpoint, otherwise the
	// contiguity check below would wait forever for a range that already landed.
	var consumed []uint64
	for _, r := range done {
		if r.to <= through {
			consumed = append(consumed, r.from)
			continue
		}
		if r.from > through+1 {
			break
		}
		through = r.to
		consumed = append(consumed, r.from)
	}
	if len(consumed) == 0 {
		return through, nil
	}
	if _, err := tx.Exec(ctx,
		`UPDATE checkpoint SET indexed_through = $1, updated_at = now() WHERE id = 1`, through); err != nil {
		return 0, fmt.Errorf("store: update checkpoint: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM range_done WHERE from_block = ANY($1)`, consumed); err != nil {
		return 0, fmt.Errorf("store: delete range_done: %w", err)
	}
	return through, tx.Commit(ctx)
}

// PendingRanges returns completed-but-not-consumed ranges (used on restart to skip re-fetch).
func (s *Store) PendingRanges(ctx context.Context) (map[uint64]uint64, error) {
	rows, err := s.pool.Query(ctx, `SELECT from_block, to_block FROM range_done`)
	if err != nil {
		return nil, fmt.Errorf("store: pending ranges: %w", err)
	}
	defer rows.Close()
	out := map[uint64]uint64{}
	for rows.Next() {
		var f, t uint64
		if err := rows.Scan(&f, &t); err != nil {
			return nil, err
		}
		out[f] = t
	}
	return out, rows.Err()
}

// CountTransfers is a test/ops helper.
func (s *Store) CountTransfers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM transfers`).Scan(&n)
	return n, err
}

// TransfersTouching returns every transfer where addr is sender or receiver, at or below atBlock,
// ordered by (block_number, log_index) so callers get deterministic graphs.
func (s *Store) TransfersTouching(ctx context.Context, addrs []common.Address, atBlock uint64, limit int) ([]Transfer, error) {
	raw := make([][]byte, len(addrs))
	for i, a := range addrs {
		raw[i] = a.Bytes()
	}
	rows, err := s.pool.Query(ctx,
		`SELECT block_number, tx_hash, log_index, from_addr, to_addr, value::text
		 FROM transfers
		 WHERE (from_addr = ANY($1) OR to_addr = ANY($1)) AND block_number <= $2
		 ORDER BY block_number, log_index
		 LIMIT $3`, raw, atBlock, limit)
	if err != nil {
		return nil, fmt.Errorf("store: transfers touching: %w", err)
	}
	defer rows.Close()
	var out []Transfer
	for rows.Next() {
		var t Transfer
		var h, f, to []byte
		var v string
		if err := rows.Scan(&t.BlockNumber, &h, &t.LogIndex, &f, &to, &v); err != nil {
			return nil, err
		}
		t.TxHash = common.BytesToHash(h)
		t.From = common.BytesToAddress(f)
		t.To = common.BytesToAddress(to)
		t.Value, _ = new(big.Int).SetString(v, 10)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Truncate wipes all tables. Test helper.
func (s *Store) Truncate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `TRUNCATE transfers, checkpoint, range_done`)
	return err
}
