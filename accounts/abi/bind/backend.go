// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package bind

import (
	"context"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

var (
	// ErrNoCode is returned by call and transact operations for which the requested
	// recipient contract to operate on does not exist in the state db or does not
	// have any code associated with it (i.e. suicided).
	ErrNoCode = errors.New("no contract code at given address")

	// This error is raised when attempting to perform a pending state action
	// on a backend that doesn't implement PendingContractCaller.
	ErrNoPendingState = errors.New("backend does not support pending state")

	// This error is returned by WaitDeployed if contract creation leaves an
	// empty contract behind.
	ErrNoCodeAfterDeploy = errors.New("no contract code after deployment")
)

// ContractCaller defines the methods needed to allow operating with contract on a read
// only basis.
// ContractCaller함수는 계약들이 읽기기반으로 동작하는것을 허용하기 위한 메소드를 정의한다 
type ContractCaller interface {
	// CodeAt returns the code of the given account. This is needed to differentiate
	// between contract internal errors and the local chain being out of sync.
	// CodeAt 함수는 주어진 계정의 코드를 반환한다.
	CodeAt(ctx context.Context, contract common.Address, blockNumber *big.Int) ([]byte, error)
	// ContractCall executes an Ethereum contract call with the specified data as the
	// input.
	// ContractCall 함수는 이더리움 계약 call을 정해진 input data를 사용하여 호출한다
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// PendingContractCaller defines methods to perform contract calls on the pending state.
// Call will try to discover this interface when access to the pending state is requested.
// If the backend does not support the pending state, Call returns ErrNoPendingState.
// PendingContractCaller함수는 대기상태에서 계약을 수행하기 위한 메소드를 정의한다
// Call은 펜딩상태에서의 접근이 요구되었을때 이 인터페이스를 발견하려고 노력할 것이다
type PendingContractCaller interface {
	// PendingCodeAt returns the code of the given account in the pending state.
	PendingCodeAt(ctx context.Context, contract common.Address) ([]byte, error)
	// PendingCallContract executes an Ethereum contract call against the pending state.
	PendingCallContract(ctx context.Context, call ethereum.CallMsg) ([]byte, error)
}

// ContractTransactor defines the methods needed to allow operating with contract
// on a write only basis. Beside the transacting method, the remainder are helpers
// used when the user does not provide some needed values, but rather leaves it up
// to the transactor to decide.
// ContractTransactor함수는 쓰기 기반의 계약 동작을 허용하기위한 방법과 
// 트렌젝션하는 방법을 제공하며 나머지는 유저가 필요한 값을 제공하지 않았을때를 위한
// 도움함수들이다. 
type ContractTransactor interface {
	// PendingCodeAt returns the code of the given account in the pending state.
	PendingCodeAt(ctx context.Context, account common.Address) ([]byte, error)
	// PendingNonceAt retrieves the current pending nonce associated with an account.
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	// SuggestGasPrice retrieves the currently suggested gas price to allow a timely
	// execution of a transaction.
	SuggestGasPrice(ctx context.Context) (*big.Int, error)
	// EstimateGas tries to estimate the gas needed to execute a specific
	// transaction based on the current pending state of the backend blockchain.
	// There is no guarantee that this is the true gas limit requirement as other
	// transactions may be added or removed by miners, but it should provide a basis
	// for setting a reasonable default.
	EstimateGas(ctx context.Context, call ethereum.CallMsg) (gas uint64, err error)
	// SendTransaction injects the transaction into the pending pool for execution.
	SendTransaction(ctx context.Context, tx *types.Transaction) error
}

// ContractFilterer defines the methods needed to access log events using one-off
// queries or continuous event subscriptions.
// ContractFilterer함수는 on-off쿼리나 연속적인 이벤트 구독을 사용하여 log event에
// 접근하기 위한 방법을 정의한다
type ContractFilterer interface {
	// FilterLogs executes a log filter operation, blocking during execution and
	// returning all the results in one batch.
	//
	// TODO(karalabe): Deprecate when the subscription one can return past data too.
	FilterLogs(ctx context.Context, query ethereum.FilterQuery) ([]types.Log, error)

	// SubscribeFilterLogs creates a background log filtering operation, returning
	// a subscription immediately, which can be used to stream the found events.
	SubscribeFilterLogs(ctx context.Context, query ethereum.FilterQuery, ch chan<- types.Log) (ethereum.Subscription, error)
}

// DeployBackend wraps the operations needed by WaitMined and WaitDeployed.
type DeployBackend interface {
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
}

// ContractBackend defines the methods needed to work with contracts on a read-write basis.
type ContractBackend interface {
	ContractCaller
	ContractTransactor
	ContractFilterer
}
