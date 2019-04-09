// Copyright 2016 The go-ethereum Authors
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

// Contains a wrapper for the Ethereum client.
// 이더리움 클라이언트를 위한 레퍼를 포함함

package geth

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// EthereumClient provides access to the Ethereum APIs.
// EthereumClient는 이더리움 API에 대한 접근을 제공한다
type EthereumClient struct {
	client *ethclient.Client
}

// NewEthereumClient connects a client to the given URL.
// NewEthereumCleint는 클라이언트를 주어진 URL로 연결한다
func NewEthereumClient(rawurl string) (client *EthereumClient, _ error) {
	rawClient, err := ethclient.Dial(rawurl)
	return &EthereumClient{rawClient}, err
}

// GetBlockByHash returns the given full block.
// GetBlockByHash는 주어진 풀불록을 반환한다
func (ec *EthereumClient) GetBlockByHash(ctx *Context, hash *Hash) (block *Block, _ error) {
	rawBlock, err := ec.client.BlockByHash(ctx.context, hash.hash)
	return &Block{rawBlock}, err
}

// GetBlockByNumber returns a block from the current canonical chain. If number is <0, the
// latest known block is returned.
// GetBlockByNumber는 현재 캐노니컬 체인으로 부터 블록을 반환한다
// 만약 숫자가 음수라면 마지막 알려진 블록이 반환한다
func (ec *EthereumClient) GetBlockByNumber(ctx *Context, number int64) (block *Block, _ error) {
	if number < 0 {
		rawBlock, err := ec.client.BlockByNumber(ctx.context, nil)
		return &Block{rawBlock}, err
	}
	rawBlock, err := ec.client.BlockByNumber(ctx.context, big.NewInt(number))
	return &Block{rawBlock}, err
}

// GetHeaderByHash returns the block header with the given hash.
// GetHeaderByHash는 주어진 해시에 대한 블록 헤더를 반환한다
func (ec *EthereumClient) GetHeaderByHash(ctx *Context, hash *Hash) (header *Header, _ error) {
	rawHeader, err := ec.client.HeaderByHash(ctx.context, hash.hash)
	return &Header{rawHeader}, err
}

// GetHeaderByNumber returns a block header from the current canonical chain. If number is <0,
// the latest known header is returned.
// GetHeaderByNumber는 현재 합의된 체인으로부터 블록헤더를 반환한다
// 만약 숫자가 음수라면 마지막 알려진 헤더가 반환된다
func (ec *EthereumClient) GetHeaderByNumber(ctx *Context, number int64) (header *Header, _ error) {
	if number < 0 {
		rawHeader, err := ec.client.HeaderByNumber(ctx.context, nil)
		return &Header{rawHeader}, err
	}
	rawHeader, err := ec.client.HeaderByNumber(ctx.context, big.NewInt(number))
	return &Header{rawHeader}, err
}

// GetTransactionByHash returns the transaction with the given hash.
// GetTransactionByHash 함수는 주어진 해시에 대한 트렌젝션을 반환한다
func (ec *EthereumClient) GetTransactionByHash(ctx *Context, hash *Hash) (tx *Transaction, _ error) {
	// TODO(karalabe): handle isPending
	rawTx, _, err := ec.client.TransactionByHash(ctx.context, hash.hash)
	return &Transaction{rawTx}, err
}

// GetTransactionSender returns the sender address of a transaction. The transaction must
// be included in blockchain at the given block and index.
// GetTransactionSender는 트렌젝션의 전송자 주소를 반환한다
// 트렌젝션은 반드시 체인내 주어진 블록과 인덱스에 존재해야 한다
func (ec *EthereumClient) GetTransactionSender(ctx *Context, tx *Transaction, blockhash *Hash, index int) (sender *Address, _ error) {
	addr, err := ec.client.TransactionSender(ctx.context, tx.tx, blockhash.hash, uint(index))
	return &Address{addr}, err
}

// GetTransactionCount returns the total number of transactions in the given block.
func (ec *EthereumClient) GetTransactionCount(ctx *Context, hash *Hash) (count int, _ error) {
	rawCount, err := ec.client.TransactionCount(ctx.context, hash.hash)
	return int(rawCount), err
}

// GetTransactionInBlock returns a single transaction at index in the given block.
// GetTransactionInBlock은 주어진 블록의 인덱스의 단일 트렌젝션을 반환한다
func (ec *EthereumClient) GetTransactionInBlock(ctx *Context, hash *Hash, index int) (tx *Transaction, _ error) {
	rawTx, err := ec.client.TransactionInBlock(ctx.context, hash.hash, uint(index))
	return &Transaction{rawTx}, err

}

// GetTransactionReceipt returns the receipt of a transaction by transaction hash.
// Note that the receipt is not available for pending transactions.
// GetTransactionReceipt는 트렌젝션 해시에의해 트렌젝션의 영수증을 반환한다
// 영수증은 펜딩된 트렌젝션에 대해서는 유효하지 않다
func (ec *EthereumClient) GetTransactionReceipt(ctx *Context, hash *Hash) (receipt *Receipt, _ error) {
	rawReceipt, err := ec.client.TransactionReceipt(ctx.context, hash.hash)
	return &Receipt{rawReceipt}, err
}

// SyncProgress retrieves the current progress of the sync algorithm. If there's
// no sync currently running, it returns nil.
// SyncProgress는 싱크 알고리즘의 현재 상황을 반환한다
// 현재 싱크가 실행되고 있지 않다면, nil을 반환한다
func (ec *EthereumClient) SyncProgress(ctx *Context) (progress *SyncProgress, _ error) {
	rawProgress, err := ec.client.SyncProgress(ctx.context)
	if rawProgress == nil {
		return nil, err
	}
	return &SyncProgress{*rawProgress}, err
}

// NewHeadHandler is a client-side subscription callback to invoke on events and
// subscription failure.
// NewHeadHander는 이벤트를 발생과 구독 실패에 대한 클라이언트 측의 콜백이다
type NewHeadHandler interface {
	OnNewHead(header *Header)
	OnError(failure string)
}

// SubscribeNewHead subscribes to notifications about the current blockchain head
// on the given channel.
// SubscribeNewHead는 주어진 채널의 현재 블록체인 해드에 대한 알람을 구독하기 위해 사용된다
func (ec *EthereumClient) SubscribeNewHead(ctx *Context, handler NewHeadHandler, buffer int) (sub *Subscription, _ error) {
	// Subscribe to the event internally
	// 이벤트를 내부적으로 구독한다
	ch := make(chan *types.Header, buffer)
	rawSub, err := ec.client.SubscribeNewHead(ctx.context, ch)
	if err != nil {
		return nil, err
	}
	// Start up a dispatcher to feed into the callback
	// 콜백으로 넣기위한 분리로직을 시작함
	go func() {
		for {
			select {
			case header := <-ch:
				handler.OnNewHead(&Header{header})

			case err := <-rawSub.Err():
				handler.OnError(err.Error())
				return
			}
		}
	}()
	return &Subscription{rawSub}, nil
}

// State Access
// 상태접근

// GetBalanceAt returns the wei balance of the given account.
// The block number can be <0, in which case the balance is taken from the latest known block.
// GetBalaceAt은 주어진 계정에 대한 wei잔고를 반환한다
// 마지막 알려진 블록으로부터 잔고를 읽을 경우 블록넘버는 음수가 될수 있다
func (ec *EthereumClient) GetBalanceAt(ctx *Context, account *Address, number int64) (balance *BigInt, _ error) {
	if number < 0 {
		rawBalance, err := ec.client.BalanceAt(ctx.context, account.address, nil)
		return &BigInt{rawBalance}, err
	}
	rawBalance, err := ec.client.BalanceAt(ctx.context, account.address, big.NewInt(number))
	return &BigInt{rawBalance}, err
}

// GetStorageAt returns the value of key in the contract storage of the given account.
// The block number can be <0, in which case the value is taken from the latest known block.
// GetSTorageAt함수는 주어진 계정의 계약 저장소의 키에 해당하는 값을 반환한다
// 마지막 알려진 블록으로부터 읽을 경우 블록넘버는 음수가 될수 있다
func (ec *EthereumClient) GetStorageAt(ctx *Context, account *Address, key *Hash, number int64) (storage []byte, _ error) {
	if number < 0 {
		return ec.client.StorageAt(ctx.context, account.address, key.hash, nil)
	}
	return ec.client.StorageAt(ctx.context, account.address, key.hash, big.NewInt(number))
}

// GetCodeAt returns the contract code of the given account.
// The block number can be <0, in which case the code is taken from the latest known block.
// GetCodeAt 함수는 주어진 계정의 계약코드를 반환한다
// 마지막 알려진 블록으로부터 읽을 경우 블록넘버는 음수가 될수 있다
func (ec *EthereumClient) GetCodeAt(ctx *Context, account *Address, number int64) (code []byte, _ error) {
	if number < 0 {
		return ec.client.CodeAt(ctx.context, account.address, nil)
	}
	return ec.client.CodeAt(ctx.context, account.address, big.NewInt(number))
}

// GetNonceAt returns the account nonce of the given account.
// The block number can be <0, in which case the nonce is taken from the latest known block.
// GetNonceAt 함수는 주어진 계정의 논스르 반환한다
// 마지막 알려진 블록으로부터 읽을 경우 블록넘버는 음수가 될수 있다
func (ec *EthereumClient) GetNonceAt(ctx *Context, account *Address, number int64) (nonce int64, _ error) {
	if number < 0 {
		rawNonce, err := ec.client.NonceAt(ctx.context, account.address, nil)
		return int64(rawNonce), err
	}
	rawNonce, err := ec.client.NonceAt(ctx.context, account.address, big.NewInt(number))
	return int64(rawNonce), err
}

// Filters

// FilterLogs executes a filter query.
// FilterLogs는 필터쿼리를 실행한다
func (ec *EthereumClient) FilterLogs(ctx *Context, query *FilterQuery) (logs *Logs, _ error) {
	rawLogs, err := ec.client.FilterLogs(ctx.context, query.query)
	if err != nil {
		return nil, err
	}
	// Temp hack due to vm.Logs being []*vm.Log
	res := make([]*types.Log, len(rawLogs))
	for i := range rawLogs {
		res[i] = &rawLogs[i]
	}
	return &Logs{res}, nil
}

// FilterLogsHandler is a client-side subscription callback to invoke on events and
// subscription failure.
// FilterLogsHandler는 이벤트와 구독 실패에 대한 클라이언트 측의 구독 콜백이다
type FilterLogsHandler interface {
	OnFilterLogs(log *Log)
	OnError(failure string)
}

// SubscribeFilterLogs subscribes to the results of a streaming filter query.
// SubscribeFilterLogs는 스트리밍 필터 쿼리의 결과를 구독한다
func (ec *EthereumClient) SubscribeFilterLogs(ctx *Context, query *FilterQuery, handler FilterLogsHandler, buffer int) (sub *Subscription, _ error) {
	// Subscribe to the event internally
	// 이벤트를 내부적으로 구독한다
	ch := make(chan types.Log, buffer)
	rawSub, err := ec.client.SubscribeFilterLogs(ctx.context, query.query, ch)
	if err != nil {
		return nil, err
	}
	// Start up a dispatcher to feed into the callback
	// 콜백을 호출하기 위한 분리로직 실행
	go func() {
		for {
			select {
			case log := <-ch:
				handler.OnFilterLogs(&Log{&log})

			case err := <-rawSub.Err():
				handler.OnError(err.Error())
				return
			}
		}
	}()
	return &Subscription{rawSub}, nil
}

// Pending State

// GetPendingBalanceAt returns the wei balance of the given account in the pending state.
// GetPendingBalaceAt함수는 펜딩상태인 계정의 wei잔고를 반환한다
func (ec *EthereumClient) GetPendingBalanceAt(ctx *Context, account *Address) (balance *BigInt, _ error) {
	rawBalance, err := ec.client.PendingBalanceAt(ctx.context, account.address)
	return &BigInt{rawBalance}, err
}

// GetPendingStorageAt returns the value of key in the contract storage of the given account in the pending state.
// GetPendingStorageAt 함수는 펜딩상태인 계약 계정의 저장소에 대한 키에 해당하는 값을 반환한다
func (ec *EthereumClient) GetPendingStorageAt(ctx *Context, account *Address, key *Hash) (storage []byte, _ error) {
	return ec.client.PendingStorageAt(ctx.context, account.address, key.hash)
}

// GetPendingCodeAt returns the contract code of the given account in the pending state.
// GetPendingCodeAt은 펜딩상태의 계약계정의 코드를 반환한다
func (ec *EthereumClient) GetPendingCodeAt(ctx *Context, account *Address) (code []byte, _ error) {
	return ec.client.PendingCodeAt(ctx.context, account.address)
}

// GetPendingNonceAt returns the account nonce of the given account in the pending state.
// This is the nonce that should be used for the next transaction.
// GetPendingNonceAt은 펜딩상태의 계정의 논스를 반환한다
// 이 논스는 다음 트렉젠션에 사용되어야 한다
func (ec *EthereumClient) GetPendingNonceAt(ctx *Context, account *Address) (nonce int64, _ error) {
	rawNonce, err := ec.client.PendingNonceAt(ctx.context, account.address)
	return int64(rawNonce), err
}

// GetPendingTransactionCount returns the total number of transactions in the pending state.
// GetPendingTransactionCount는 펜딩 상태의 총 트렌젝션 수를 반환한다.
func (ec *EthereumClient) GetPendingTransactionCount(ctx *Context) (count int, _ error) {
	rawCount, err := ec.client.PendingTransactionCount(ctx.context)
	return int(rawCount), err
}

// Contract Calling

// CallContract executes a message call transaction, which is directly executed in the VM
// of the node, but never mined into the blockchain.
//
// blockNumber selects the block height at which the call runs. It can be <0, in which
// case the code is taken from the latest known block. Note that state from very old
// blocks might not be available.
// CallContract는 노드의 VM에서 직접 실행되었지만 
// 블록체인에 마이닝되지 않는 메시지 콜 트렌젝션을 실행한다.
// blockNumber는 호출이 실행될 볼록높이이를 선택하며 최종 알려진 블록을 사용할 경우 음수가 될수 있다.
// 아주 오래된 블록으로부터 온 상태는 유효하지 않을 수 있다
func (ec *EthereumClient) CallContract(ctx *Context, msg *CallMsg, number int64) (output []byte, _ error) {
	if number < 0 {
		return ec.client.CallContract(ctx.context, msg.msg, nil)
	}
	return ec.client.CallContract(ctx.context, msg.msg, big.NewInt(number))
}

// PendingCallContract executes a message call transaction using the EVM.
// The state seen by the contract call is the pending state.
// PendingCallContract 함수는 EVM을 사용하여 메시지 콜을 실행한다
// 계약호출에 의해 보여진 상태는 대기상태이다
func (ec *EthereumClient) PendingCallContract(ctx *Context, msg *CallMsg) (output []byte, _ error) {
	return ec.client.PendingCallContract(ctx.context, msg.msg)
}

// SuggestGasPrice retrieves the currently suggested gas price to allow a timely
// execution of a transaction.
// SuggestGasPrice는 적절하게 트렌젝션을 실행하기 위한 현재 제안되는 가스가격을 바환한다
func (ec *EthereumClient) SuggestGasPrice(ctx *Context) (price *BigInt, _ error) {
	rawPrice, err := ec.client.SuggestGasPrice(ctx.context)
	return &BigInt{rawPrice}, err
}

// EstimateGas tries to estimate the gas needed to execute a specific transaction based on
// the current pending state of the backend blockchain. There is no guarantee that this is
// the true gas limit requirement as other transactions may be added or removed by miners,
// but it should provide a basis for setting a reasonable default.
// EstimateGas는 백앤드 블록체의늬 대기 상태를 바탕으로 
// 특정 트렌젝션을 실행시키기 위해 필요한 가스를 예측하려 노력한다
// 이것이 마이너들에의해 추가되거나 제거된 다른 트렌젝션의 실제 가스 한도 요구라고는 볼수 없지만
// 이유있는 기본값 설정을 위한 기본을 제공해야 한다
func (ec *EthereumClient) EstimateGas(ctx *Context, msg *CallMsg) (gas int64, _ error) {
	rawGas, err := ec.client.EstimateGas(ctx.context, msg.msg)
	return int64(rawGas), err
}

// SendTransaction injects a signed transaction into the pending pool for execution.
//
// If the transaction was a contract creation use the TransactionReceipt method to get the
// contract address after the transaction has been mined.
// SendTransaction 함수는 서명된 트렌젝션을 실행하기 위해 펜딩풀로 트렌젝션을 삽입한다
// 트렌젝션이 계약의 생성일 경우 트렌젝션이 마이닝된 후 
// TransactionREceipt함수를 사용하여 계약 주소를 얻는다.
func (ec *EthereumClient) SendTransaction(ctx *Context, tx *Transaction) error {
	return ec.client.SendTransaction(ctx.context, tx.tx)
}
