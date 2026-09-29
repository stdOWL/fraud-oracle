package rpc

import (
	"context"
	"errors"
	"sync"

	"fraud-oracle/service/internal/store"
)

// Fake is an in-memory LogSource for tests. FailRangesLeft makes the next N calls fail,
// which is how the resume test forces a partial run.
type Fake struct {
	mu             sync.Mutex
	head           uint64
	chainID        uint64
	transfers      []store.Transfer
	Calls          int
	FailRangesLeft int
}

func NewFake(head, chainID uint64, ts []store.Transfer) *Fake {
	return &Fake{head: head, chainID: chainID, transfers: ts}
}

func (f *Fake) SetHead(h uint64) { f.mu.Lock(); f.head = h; f.mu.Unlock() }

func (f *Fake) Head(context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, nil
}

func (f *Fake) ChainID(context.Context) (uint64, error) { return f.chainID, nil }

func (f *Fake) Transfers(_ context.Context, from, to uint64) ([]store.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.FailRangesLeft > 0 {
		f.FailRangesLeft--
		return nil, errors.New("fake rpc: injected failure")
	}
	var out []store.Transfer
	for _, t := range f.transfers {
		if t.BlockNumber >= from && t.BlockNumber <= to {
			out = append(out, t)
		}
	}
	return out, nil
}
