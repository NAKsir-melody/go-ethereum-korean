// Copyright 2017 The go-ethereum Authors
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

// Package consensus implements different Ethereum consensus engines.
// Consensus 패키지는 서로다른 이더리움 함의 엔진을 구현한다
package consensus

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// ChainReader defines a small collection of methods needed to access the local
// blockchain during header and/or uncle verification.
// ChainReader 인터페이스는 헤더나 엉클 검증을 하는 동안 로컬 블록체인에 접근하는 
// 메소드들의 작은 모임이다
type ChainReader interface {
	// Config retrieves the blockchain's chain configuration.
	// Config함수는 블록체인의 체인 설정을 반환한다
	Config() *params.ChainConfig

	// CurrentHeader retrieves the current header from the local chain.
	// CurrentHeader함수는 로컬체인으로부터 현재 헤더를 반환한다
	CurrentHeader() *types.Header

	// GetHeader retrieves a block header from the database by hash and number.
	// GetHeader함수는 해시와 블록넘버를 사용해 DB로부터 블록해더를 반환한다
	GetHeader(hash common.Hash, number uint64) *types.Header

	// GetHeaderByNumber retrieves a block header from the database by number.
	// GetHeaderByNumber함수는 블록번호를 이용하여 DB로부터 블록 헤더를 반환한다
	GetHeaderByNumber(number uint64) *types.Header

	// GetHeaderByHash retrieves a block header from the database by its hash.
	// GetHeaderByHash 함수는 해시를 사용하여 DB로부터 블록헤더를 반환한다
	GetHeaderByHash(hash common.Hash) *types.Header

	// GetBlock retrieves a block from the database by hash and number.
	// GetBlock 함수는 해시와 넘버를 사용하여 DB로부터 블록을 반환한다
	GetBlock(hash common.Hash, number uint64) *types.Block
}

// Engine is an algorithm agnostic consensus engine.
// 엔진 인터페이스는 알고리즘 독립적인 합의 엔진이다
type Engine interface {
	// Author retrieves the Ethereum address of the account that minted the given
	// block, which may be different from the header's coinbase if a consensus
	// engine is based on signatures.
	// Author함수는 만약 합의 엔진이 서명기반이라면 헤더의 코인베이스와 달라질수 있는
	// 주어진 블록을 마이닝한 계정의 이더리움 주소를 반환한다
	Author(header *types.Header) (common.Address, error)

	// VerifyHeader checks whether a header conforms to the consensus rules of a
	// given engine. Verifying the seal may be done optionally here, or explicitly
	// via the VerifySeal method.
	// VerifyHeader함수는 헤더가 주어진 엔진의 합의 룰을 따르는지 확인한다
	// 인장을 검증하는 것은 선택적으로 진행될수도 있으며, VerifySeal함수를 통해
	// 명시적으로 진행되기도 한다
	VerifyHeader(chain ChainReader, header *types.Header, seal bool) error

	// VerifyHeaders is similar to VerifyHeader, but verifies a batch of headers
	// concurrently. The method returns a quit channel to abort the operations and
	// a results channel to retrieve the async verifications (the order is that of
	// the input slice).
	// VerifyHeaders함수는 VerifyHeader와 유사하나 여러개의 헤더를 동시에 검증한다
	// 이함수는 동작을 중지시키기 위한 quit 채널과 비동기검증을 반환하기 위한 결과채널을 반환한다 
	VerifyHeaders(chain ChainReader, headers []*types.Header, seals []bool) (chan<- struct{}, <-chan error)

	// VerifyUncles verifies that the given block's uncles conform to the consensus
	// rules of a given engine.
	// VerifyUncles 함수는 주어진 블록의 엉클이 
	// 주어진 합의 엔진의 룰을 따르는지 검증한다
	VerifyUncles(chain ChainReader, block *types.Block) error

	// VerifySeal checks whether the crypto seal on a header is valid according to
	// the consensus rules of the given engine.
	// VerifySeal 함수는 헤더의 암호화된 인장이 주어진 엔진의 룰을 따르는지 검증한다
	VerifySeal(chain ChainReader, header *types.Header) error

	// Prepare initializes the consensus fields of a block header according to the
	// rules of a particular engine. The changes are executed inline.
	// Prepare 함소는 블록 헤더의 합의 필드를 각 엔진의 룰에 따라 초기화 한다
	// 변화는 inline으로 실행된다
	Prepare(chain ChainReader, header *types.Header) error

	// Finalize runs any post-transaction state modifications (e.g. block rewards)
	// and assembles the final block.
	// Note: The block header and state database might be updated to reflect any
	// consensus rules that happen at finalization (e.g. block rewards).
	// Finalize 함수는 상태 변화의 모든 후처리를 담당한다(예: 블록 리워드)
	// 그리고 최종 블록을 조합한다. 블록 헤더와 상태DB는 
	// 최종과정에서 발생한 모든 합의 룰을 반영하기 위해 업데이트 될것이다
	Finalize(chain ChainReader, header *types.Header, state *state.StateDB, txs []*types.Transaction,
		uncles []*types.Header, receipts []*types.Receipt) (*types.Block, error)

	// Seal generates a new block for the given input block with the local miner's
	// seal place on top.
	// Seal 함수는 주어진 인풋을 위해  블록 로컬 마이너의 인장을 탑으로 설정한 새 블록을 생성한다
	Seal(chain ChainReader, block *types.Block, stop <-chan struct{}) (*types.Block, error)

	// CalcDifficulty is the difficulty adjustment algorithm. It returns the difficulty
	// that a new block should have.
	// CalcDifficulty함수는 난이도 수정 알고리즘이다. 이함수는 새로운 블록이
	// 가져야 할 난이도를 반환한다
	CalcDifficulty(chain ChainReader, time uint64, parent *types.Header) *big.Int

	// APIs returns the RPC APIs this consensus engine provides.
	// APUs 함수는 이 합의 엔진이 제공하는 RPC API를 반환한다
	APIs(chain ChainReader) []rpc.API
}

// PoW is a consensus engine based on proof-of-work.
// PoW 인터페이스는 POW 기반의 합의 엔진이다
type PoW interface {
	Engine

	// Hashrate returns the current mining hashrate of a PoW consensus engine.
	Hashrate() float64
}
