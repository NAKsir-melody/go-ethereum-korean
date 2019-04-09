// Copyright 2014 The go-ethereum Authors
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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// NewTxsEvent is posted when a batch of transactions enter the transaction pool.
// 트렌젝션 풀에 트렌젝션들이 들어갈경우 이 이벤트가 발생한다
type NewTxsEvent struct{ Txs []*types.Transaction }

// PendingLogsEvent is posted pre mining and notifies of pending logs.
// pendingLogsEvent는 프리마이닝과 대기중인 로그의 알람에의해 발생된다
type PendingLogsEvent struct {
	Logs []*types.Log
}

// PendingStateEvent is posted pre mining and notifies of pending state changes.
// 이 이벤트는 프리마이닝과 펜딩 상테의 변화를 포스팅한다
type PendingStateEvent struct{}

// NewMinedBlockEvent is posted when a block has been imported.
// 이 이벤트는 새로운 블록이 입수되었을때 발생한다
type NewMinedBlockEvent struct{ Block *types.Block }

// RemovedLogsEvent is posted when a reorg happens
// RemovedLogsEvent는 재구성이 발생하면 던져진다
type RemovedLogsEvent struct{ Logs []*types.Log }

type ChainEvent struct {
	Block *types.Block
	Hash  common.Hash
	Logs  []*types.Log
}

type ChainSideEvent struct {
	Block *types.Block
}

type ChainHeadEvent struct{ Block *types.Block }
