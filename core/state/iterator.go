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

package state

import (
	"bytes"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

// NodeIterator is an iterator to traverse the entire state trie post-order,
// including all of the contract code and contract state tries.
// NodeIterator는 계약코드와 계약상태 트라이를 포함한 전체 상태 트리를 
// post-order로 순환참조하기 위한 반복자이다
type NodeIterator struct {
	state *StateDB // State being iterated
	// 반복될 상태

	stateIt trie.NodeIterator // Primary iterator for the global state trie
	// 글로벌 상태 트라이를 위한 주 반복자
	dataIt  trie.NodeIterator // Secondary iterator for the data trie of a contract
	// 계약의 데이터 트라이를 위한 보조 반복자

	accountHash common.Hash // Hash of the node containing the account
	// 계정을 포함하는 노드의 해시
	codeHash    common.Hash // Hash of the contract source code
	// 계약소스코드의 해시
	code        []byte      // Source code associated with a contract
	// 계약과 관련된 소스코드

	Hash   common.Hash // Hash of the current entry being iterated (nil if not standalone)
	// 반복할 현재 엔트리의 해시
	Parent common.Hash // Hash of the first full ancestor node (nil if current is the root)
	// 첫 전체 조상노드의 의 해시
	Error error // Failure set in case of an internal error in the iterator
	// 반복중 발생하는 내부에러
}

// NewNodeIterator creates an post-order state node iterator.
// NewNodeIterator함수는 post-order로 상태노드를 반복한다
func NewNodeIterator(state *StateDB) *NodeIterator {
	return &NodeIterator{
		state: state,
	}
}

// Next moves the iterator to the next node, returning whether there are any
// further nodes. In case of an internal error this method returns false and
// sets the Error field to the encountered failure.
// Next함수는 반복자를 다음 노드로 옮기고, 추가 노드의 존재여부를 반환한다
// 내부에러 발생시 flase를 에러와 함께 반환한다
func (it *NodeIterator) Next() bool {
	// If the iterator failed previously, don't do anything
	// 만약 반복이 실패했다면 아무것도 하지 않는다
	if it.Error != nil {
		return false
	}
	// Otherwise step forward with the iterator and report any errors
	// 아니라면 반복자를 앞으로 하나 옮기고 에러를 레포트한다
	if err := it.step(); err != nil {
		it.Error = err
		return false
	}
	return it.retrieve()
}

// step moves the iterator to the next entry of the state trie.
// step 함수는 반복자를 상태트라이의 다음 엔트리로 옮긴다
func (it *NodeIterator) step() error {
	// Abort if we reached the end of the iteration
	// 반복 종료시 abort
	if it.state == nil {
		return nil
	}
	// Initialize the iterator if we've just started
	// 시작이라면 반복자를 초기화 한다
	if it.stateIt == nil {
		it.stateIt = it.state.trie.NodeIterator(nil)
	}
	// If we had data nodes previously, we surely have at least state nodes
	// 이전에 데이터 노드르 가졌다면 상태 노드들을 가져야한다
	if it.dataIt != nil {
		if cont := it.dataIt.Next(true); !cont {
			if it.dataIt.Error() != nil {
				return it.dataIt.Error()
			}
			it.dataIt = nil
		}
		return nil
	}
	// If we had source code previously, discard that
	if it.code != nil {
		it.code = nil
		return nil
	}
	// Step to the next state trie node, terminating if we're out of nodes
	// 이전에 소스코드를 가졌다면 무시한다
	// 다음 상태 트라이 노드로 옮기고, 노드의 끝에 도달했다면 종료한다
	if cont := it.stateIt.Next(true); !cont {
		if it.stateIt.Error() != nil {
			return it.stateIt.Error()
		}
		it.state, it.stateIt = nil, nil
		return nil
	}
	// If the state trie node is an internal entry, leave as is
	// 만약 상태 트라이 노드가 내부 엔트리라면 그대로 둔다
	if !it.stateIt.Leaf() {
		return nil
	}
	// Otherwise we've reached an account node, initiate data iteration
	// 아니라면 우리는 계정노드에 도달했다 데이터 반복을 초기화 한다
	var account Account
	if err := rlp.Decode(bytes.NewReader(it.stateIt.LeafBlob()), &account); err != nil {
		return err
	}
	dataTrie, err := it.state.db.OpenStorageTrie(common.BytesToHash(it.stateIt.LeafKey()), account.Root)
	if err != nil {
		return err
	}
	it.dataIt = dataTrie.NodeIterator(nil)
	if !it.dataIt.Next(true) {
		it.dataIt = nil
	}
	if !bytes.Equal(account.CodeHash, emptyCodeHash) {
		it.codeHash = common.BytesToHash(account.CodeHash)
		addrHash := common.BytesToHash(it.stateIt.LeafKey())
		it.code, err = it.state.db.ContractCode(addrHash, common.BytesToHash(account.CodeHash))
		if err != nil {
			return fmt.Errorf("code %x: %v", account.CodeHash, err)
		}
	}
	it.accountHash = it.stateIt.Parent()
	return nil
}

// retrieve pulls and caches the current state entry the iterator is traversing.
// The method returns whether there are any more data left for inspection.
// retrieve함수는 반복자가 순환하는 현재 상태 엔트리를 추출하여 캐싱한다.
// 이 메소드는 검증해야할 남은 데이터가 있는지 여부를 반환한다
func (it *NodeIterator) retrieve() bool {
	// Clear out any previously set values
	// 이전의 값을 초기화한다
	it.Hash = common.Hash{}

	// If the iteration's done, return no available data
	// 순환이 끝나면 유효한데이터가 없을을 반환한다
	if it.state == nil {
		return false
	}
	// Otherwise retrieve the current entry
	// 현재 엔트리를 반환한다
	switch {
	case it.dataIt != nil:
		it.Hash, it.Parent = it.dataIt.Hash(), it.dataIt.Parent()
		if it.Parent == (common.Hash{}) {
			it.Parent = it.accountHash
		}
	case it.code != nil:
		it.Hash, it.Parent = it.codeHash, it.accountHash
	case it.stateIt != nil:
		it.Hash, it.Parent = it.stateIt.Hash(), it.stateIt.Parent()
	}
	return true
}
