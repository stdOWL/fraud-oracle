// Package rpc fetches ERC20 Transfer logs over JSON-RPC.
package rpc

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"fraud-oracle/service/internal/store"
)

// TransferTopic is keccak256("Transfer(address,address,uint256)").
var TransferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

// LogSource is what the indexer needs from a chain. Two implementations: Client (real) and Fake (tests).
type LogSource interface {
	Head(ctx context.Context) (uint64, error)
	ChainID(ctx context.Context) (uint64, error)
	// Transfers returns Transfer logs of the token in [from, to] inclusive.
	Transfers(ctx context.Context, from, to uint64) ([]store.Transfer, error)
}

type Client struct {
	ec    *ethclient.Client
	token common.Address
}

func Dial(ctx context.Context, url string, token common.Address) (*Client, error) {
	ec, err := ethclient.DialContext(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("rpc: dial: %w", err)
	}
	return &Client{ec: ec, token: token}, nil
}

func (c *Client) Close() { c.ec.Close() }

func (c *Client) Head(ctx context.Context) (uint64, error) {
	return c.ec.BlockNumber(ctx)
}

func (c *Client) ChainID(ctx context.Context) (uint64, error) {
	id, err := c.ec.ChainID(ctx)
	if err != nil {
		return 0, err
	}
	return id.Uint64(), nil
}

func (c *Client) Transfers(ctx context.Context, from, to uint64) ([]store.Transfer, error) {
	logs, err := c.ec.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: new(big.Int).SetUint64(from),
		ToBlock:   new(big.Int).SetUint64(to),
		Addresses: []common.Address{c.token},
		Topics:    [][]common.Hash{{TransferTopic}},
	})
	if err != nil {
		return nil, fmt.Errorf("rpc: eth_getLogs [%d,%d]: %w", from, to, err)
	}
	out := make([]store.Transfer, 0, len(logs))
	for _, l := range logs {
		if len(l.Topics) != 3 || len(l.Data) != 32 {
			continue // not a standard ERC20 Transfer (e.g. ERC721 with indexed tokenId)
		}
		out = append(out, store.Transfer{
			BlockNumber: l.BlockNumber,
			TxHash:      l.TxHash,
			LogIndex:    uint32(l.Index),
			From:        common.BytesToAddress(l.Topics[1].Bytes()),
			To:          common.BytesToAddress(l.Topics[2].Bytes()),
			Value:       new(big.Int).SetBytes(l.Data),
		})
	}
	return out, nil
}
