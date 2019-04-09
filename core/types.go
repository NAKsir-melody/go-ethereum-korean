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

package core

import (
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

// Validator is an interface which defines the standard for block validation. It
// is only responsible for validating block contents, as the header validation is
// done by the specific consensus engines.
// Validator는 블록의 검증을 위한 기준을 정의하는 인터페이스 이다
// 이 인터페이스는 헤더 검증이 지정된 합의 엔진에 의해 검증되는 것 처럼
// 블록의 내용을 검증하는것 만을 담고있다.
type Validator interface {
	// ValidateBody validates the given block's content.
	// ValidateBody함수는 주어진 블록의 내용을 검즈한다
	ValidateBody(block *types.Block) error

	// ValidateState validates the given statedb and optionally the receipts and
	// gas used.
	// ValidateState함수는 주어진 db와 영수증과 가스 사용량을 검증한다
	ValidateState(block, parent *types.Block, state *state.StateDB, receipts types.Receipts, usedGas uint64) error
}

// Processor is an interface for processing blocks using a given initial state.
//
// Process takes the block to be processed and the statedb upon which the
// initial state is based. It should return the receipts generated, amount
// of gas used in the process and return an error if any of the internal rules
// failed.
// Processer는 주어진 초기 상태르 ㄹ이용해 블록을 처리하는 인터페이스이다
// Process는 처리될 블록과 전달된 초기 상태가 베이스인 스테이트 DB를 받는다
// 이함수는 생성된 영수증과 처리에 사용된 가스사용량과 내부룰에 실패했을 경우 에러를 리턴해야한다

type Processor interface {
	Process(block *types.Block, statedb *state.StateDB, cfg vm.Config) (types.Receipts, []*types.Log, uint64, error)
}
