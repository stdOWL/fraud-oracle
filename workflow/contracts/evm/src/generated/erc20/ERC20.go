// Code generated — DO NOT EDIT.

package erc20

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/rpc"
	"google.golang.org/protobuf/types/known/emptypb"

	pb2 "github.com/smartcontractkit/chainlink-protos/cre/go/sdk"
	"github.com/smartcontractkit/chainlink-protos/cre/go/values/pb"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm/bindings"
	"github.com/smartcontractkit/cre-sdk-go/cre"
)

var (
	_ = bytes.Equal
	_ = errors.New
	_ = fmt.Sprintf
	_ = big.NewInt
	_ = strings.NewReader
	_ = ethereum.NotFound
	_ = bind.Bind
	_ = common.Big1
	_ = types.BloomLookup
	_ = event.NewSubscription
	_ = abi.ConvertType
	_ = emptypb.Empty{}
	_ = pb.NewBigIntFromInt
	_ = pb2.AggregationType_AGGREGATION_TYPE_COMMON_PREFIX
	_ = bindings.FilterOptions{}
	_ = evm.FilterLogTriggerRequest{}
	_ = cre.ResponseBufferTooSmall
	_ = rpc.API{}
	_ = json.Unmarshal
	_ = reflect.Bool
)

var ERC20MetaData = &bind.MetaData{
	ABI: "[{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"from\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"value\",\"type\":\"uint256\"}],\"name\":\"Transfer\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"account\",\"type\":\"address\"}],\"name\":\"balanceOf\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]",
}

// Structs

// Contract Method Inputs
type BalanceOfInput struct {
	Account common.Address
}

// Contract Method Outputs

// Errors

// Events
// The <Event>Topics struct should be used as a filter (for log triggers).
// Note: It is only possible to filter on indexed fields.
// Indexed (string and bytes) fields will be of type common.Hash.
// They need to he (crypto.Keccak256) hashed and passed in.
// Indexed (tuple/slice/array) fields can be passed in as is, the Encode<Event>Topics function will handle the hashing.
//
// The <Event>Decoded struct will be the result of calling decode (Adapt) on the log trigger result.
// Indexed dynamic type fields will be of type common.Hash.

type TransferTopics struct {
	From common.Address
	To   common.Address
}

type TransferDecoded struct {
	From  common.Address
	To    common.Address
	Value *big.Int
}

// Main Binding Type for ERC20
type ERC20 struct {
	Address common.Address
	Options *bindings.ContractInitOptions
	ABI     *abi.ABI
	client  *evm.Client
	Codec   ERC20Codec
}

type ERC20Codec interface {
	EncodeBalanceOfMethodCall(in BalanceOfInput) ([]byte, error)
	DecodeBalanceOfMethodOutput(data []byte) (*big.Int, error)
	TransferLogHash() []byte
	EncodeTransferTopics(evt abi.Event, values []TransferTopics) ([]*evm.TopicValues, error)
	DecodeTransfer(log *evm.Log) (*TransferDecoded, error)
}

func NewERC20(
	client *evm.Client,
	address common.Address,
	options *bindings.ContractInitOptions,
) (*ERC20, error) {
	parsed, err := abi.JSON(strings.NewReader(ERC20MetaData.ABI))
	if err != nil {
		return nil, err
	}
	codec, err := NewCodec()
	if err != nil {
		return nil, err
	}
	return &ERC20{
		Address: address,
		Options: options,
		ABI:     &parsed,
		client:  client,
		Codec:   codec,
	}, nil
}

type Codec struct {
	abi *abi.ABI
}

func NewCodec() (ERC20Codec, error) {
	parsed, err := abi.JSON(strings.NewReader(ERC20MetaData.ABI))
	if err != nil {
		return nil, err
	}
	return &Codec{abi: &parsed}, nil
}

func (c *Codec) EncodeBalanceOfMethodCall(in BalanceOfInput) ([]byte, error) {
	return c.abi.Pack("balanceOf", in.Account)
}

func (c *Codec) DecodeBalanceOfMethodOutput(data []byte) (*big.Int, error) {
	vals, err := c.abi.Methods["balanceOf"].Outputs.Unpack(data)
	if err != nil {
		return *new(*big.Int), err
	}
	jsonData, err := json.Marshal(vals[0])
	if err != nil {
		return *new(*big.Int), fmt.Errorf("failed to marshal ABI result: %w", err)
	}

	var result *big.Int
	if err := json.Unmarshal(jsonData, &result); err != nil {
		return *new(*big.Int), fmt.Errorf("failed to unmarshal to *big.Int: %w", err)
	}

	return result, nil
}

func (c *Codec) TransferLogHash() []byte {
	return c.abi.Events["Transfer"].ID.Bytes()
}

func (c *Codec) EncodeTransferTopics(
	evt abi.Event,
	values []TransferTopics,
) ([]*evm.TopicValues, error) {
	var fromRule []interface{}
	for _, v := range values {
		if reflect.ValueOf(v.From).IsZero() {
			fromRule = append(fromRule, common.Hash{})
			continue
		}
		fieldVal, err := bindings.PrepareTopicArg(evt.Inputs[0], v.From)
		if err != nil {
			return nil, err
		}
		fromRule = append(fromRule, fieldVal)
	}
	var toRule []interface{}
	for _, v := range values {
		if reflect.ValueOf(v.To).IsZero() {
			toRule = append(toRule, common.Hash{})
			continue
		}
		fieldVal, err := bindings.PrepareTopicArg(evt.Inputs[1], v.To)
		if err != nil {
			return nil, err
		}
		toRule = append(toRule, fieldVal)
	}

	rawTopics, err := abi.MakeTopics(
		fromRule,
		toRule,
	)
	if err != nil {
		return nil, err
	}

	return bindings.PrepareTopics(rawTopics, evt.ID.Bytes()), nil
}

// DecodeTransfer decodes a log into a Transfer struct.
func (c *Codec) DecodeTransfer(log *evm.Log) (*TransferDecoded, error) {
	event := new(TransferDecoded)
	if err := c.abi.UnpackIntoInterface(event, "Transfer", log.Data); err != nil {
		return nil, err
	}
	var indexed abi.Arguments
	for _, arg := range c.abi.Events["Transfer"].Inputs {
		if arg.Indexed {
			if arg.Type.T == abi.TupleTy {
				// abigen throws on tuple, so converting to bytes to
				// receive back the common.Hash as is instead of error
				arg.Type.T = abi.BytesTy
			}
			indexed = append(indexed, arg)
		}
	}
	// Convert [][]byte → []common.Hash
	topics := make([]common.Hash, len(log.Topics))
	for i, t := range log.Topics {
		topics[i] = common.BytesToHash(t)
	}

	if err := abi.ParseTopics(event, indexed, topics[1:]); err != nil {
		return nil, err
	}
	return event, nil
}

func (c ERC20) BalanceOf(
	runtime cre.Runtime,
	args BalanceOfInput,
	blockNumber *big.Int,
) cre.Promise[*big.Int] {
	calldata, err := c.Codec.EncodeBalanceOfMethodCall(args)
	if err != nil {
		return cre.PromiseFromResult[*big.Int](*new(*big.Int), err)
	}

	bn := bindings.FinalizedBlockNumber
	if blockNumber != nil {
		bn = pb.NewBigIntFromInt(blockNumber)
	}

	promise := cre.ThenPromise(cre.PromiseFromResult(bn, nil), func(bn *pb.BigInt) cre.Promise[*evm.CallContractReply] {
		return c.client.CallContract(runtime, &evm.CallContractRequest{
			Call:        &evm.CallMsg{To: c.Address.Bytes(), Data: calldata},
			BlockNumber: bn,
		})
	})
	return cre.Then(promise, func(response *evm.CallContractReply) (*big.Int, error) {
		return c.Codec.DecodeBalanceOfMethodOutput(response.Data)
	})

}

func (c ERC20) WriteReport(
	runtime cre.Runtime,
	report *cre.Report,
	gasConfig *evm.GasConfig,
) cre.Promise[*evm.WriteReportReply] {
	return c.client.WriteReport(runtime, &evm.WriteCreReportRequest{
		Receiver:  c.Address.Bytes(),
		Report:    report,
		GasConfig: gasConfig,
	})
}

func (c *ERC20) UnpackError(data []byte) (any, error) {
	switch common.Bytes2Hex(data[:4]) {
	default:
		return nil, errors.New("unknown error selector")
	}
}

// TransferTrigger wraps the raw log trigger and provides decoded TransferDecoded data
type TransferTrigger struct {
	cre.Trigger[*evm.Log, *evm.Log]        // Embed the raw trigger
	contract                        *ERC20 // Keep reference for decoding
}

// Adapt method that decodes the log into Transfer data
func (t *TransferTrigger) Adapt(l *evm.Log) (*bindings.DecodedLog[TransferDecoded], error) {
	// Decode the log using the contract's codec
	decoded, err := t.contract.Codec.DecodeTransfer(l)
	if err != nil {
		return nil, fmt.Errorf("failed to decode Transfer log: %w", err)
	}

	return &bindings.DecodedLog[TransferDecoded]{
		Log:  l,        // Original log
		Data: *decoded, // Decoded data
	}, nil
}

func (c *ERC20) LogTriggerTransferLog(chainSelector uint64, confidence evm.ConfidenceLevel, filters []TransferTopics) (cre.Trigger[*evm.Log, *bindings.DecodedLog[TransferDecoded]], error) {
	event := c.ABI.Events["Transfer"]
	topics, err := c.Codec.EncodeTransferTopics(event, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to encode topics for Transfer: %w", err)
	}

	rawTrigger := evm.LogTrigger(chainSelector, &evm.FilterLogTriggerRequest{
		Addresses:  [][]byte{c.Address.Bytes()},
		Topics:     topics,
		Confidence: confidence,
	})

	return &TransferTrigger{
		Trigger:  rawTrigger,
		contract: c,
	}, nil
}

func (c *ERC20) FilterLogsTransfer(runtime cre.Runtime, options *bindings.FilterOptions) (cre.Promise[*evm.FilterLogsReply], error) {
	if options == nil {
		return nil, errors.New("FilterLogs options are required.")
	}
	return c.client.FilterLogs(runtime, &evm.FilterLogsRequest{
		FilterQuery: &evm.FilterQuery{
			Addresses: [][]byte{c.Address.Bytes()},
			Topics: []*evm.Topics{
				{Topic: [][]byte{c.Codec.TransferLogHash()}},
			},
			BlockHash: options.BlockHash,
			FromBlock: pb.NewBigIntFromInt(options.FromBlock),
			ToBlock:   pb.NewBigIntFromInt(options.ToBlock),
		},
	}), nil
}
