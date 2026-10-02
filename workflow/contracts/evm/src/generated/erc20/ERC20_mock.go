// Code generated — DO NOT EDIT.

//go:build !wasip1

package erc20

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	evmmock "github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm/mock"
)

var (
	_ = errors.New
	_ = fmt.Errorf
	_ = big.NewInt
	_ = common.Big1
)

// ERC20Mock is a mock implementation of ERC20 for testing.
type ERC20Mock struct {
	BalanceOf func(BalanceOfInput) (*big.Int, error)
}

// NewERC20Mock creates a new ERC20Mock for testing.
func NewERC20Mock(address common.Address, clientMock *evmmock.ClientCapability) *ERC20Mock {
	mock := &ERC20Mock{}

	codec, err := NewCodec()
	if err != nil {
		panic("failed to create codec for mock: " + err.Error())
	}

	abi := codec.(*Codec).abi
	_ = abi

	funcMap := map[string]func([]byte) ([]byte, error){
		string(abi.Methods["balanceOf"].ID[:4]): func(payload []byte) ([]byte, error) {
			if mock.BalanceOf == nil {
				return nil, errors.New("balanceOf method not mocked")
			}
			inputs := abi.Methods["balanceOf"].Inputs

			values, err := inputs.Unpack(payload)
			if err != nil {
				return nil, errors.New("Failed to unpack payload")
			}
			if len(values) != 1 {
				return nil, errors.New("expected 1 input value")
			}

			args := BalanceOfInput{
				Account: values[0].(common.Address),
			}

			result, err := mock.BalanceOf(args)
			if err != nil {
				return nil, err
			}
			return abi.Methods["balanceOf"].Outputs.Pack(result)
		},
	}

	evmmock.AddContractMock(address, clientMock, funcMap, nil)
	return mock
}
