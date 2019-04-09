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
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

type account struct {
	stateObject *stateObject
	nstart      uint64
	nonces      []bool
}

type ManagedState struct {
	*StateDB

	mu sync.RWMutex

	accounts map[common.Address]*account
}

// ManagedState returns a new managed state with the statedb as it's backing layer
// ManagedState 함수는 지원하는 레이어로서 DB를 사용해서 새로운 관리 상태를 반환한다
func ManageState(statedb *StateDB) *ManagedState {
	return &ManagedState{
		StateDB:  statedb.Copy(),
		accounts: make(map[common.Address]*account),
	}
}

// SetState sets the backing layer of the managed state
// SetState 함수는 관리된 상태의 지원 레이어를 설정한다
func (ms *ManagedState) SetState(statedb *StateDB) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.StateDB = statedb
}

// RemoveNonce removed the nonce from the managed state and all future pending nonces
// RemoveNonce함수는 관리된 상태에서 논스 및 대기중인 퓨처 논스들을 제거한다 
func (ms *ManagedState) RemoveNonce(addr common.Address, n uint64) {
	if ms.hasAccount(addr) {
		ms.mu.Lock()
		defer ms.mu.Unlock()

		account := ms.getAccount(addr)
		if n-account.nstart <= uint64(len(account.nonces)) {
			reslice := make([]bool, n-account.nstart)
			copy(reslice, account.nonces[:n-account.nstart])
			account.nonces = reslice
		}
	}
}

// NewNonce returns the new canonical nonce for the managed account
// NewNonce함수는 관리되는 계정을 위한 캐노니컬 논스를 반환한다
func (ms *ManagedState) NewNonce(addr common.Address) uint64 {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	account := ms.getAccount(addr)
	for i, nonce := range account.nonces {
		if !nonce {
			return account.nstart + uint64(i)
		}
	}
	account.nonces = append(account.nonces, true)

	return uint64(len(account.nonces)-1) + account.nstart
}

// GetNonce returns the canonical nonce for the managed or unmanaged account.
//
// Because GetNonce mutates the DB, we must take a write lock.
// GetNonce 함수는 관리되거나/되지않은 계정에 대핸 캐노니컬 논스를 반환한다
// GetNonce는 DB에 접근해야하기 때문에, lock이 필요하다
func (ms *ManagedState) GetNonce(addr common.Address) uint64 {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.hasAccount(addr) {
		account := ms.getAccount(addr)
		return uint64(len(account.nonces)) + account.nstart
	} else {
		return ms.StateDB.GetNonce(addr)
	}
}

// SetNonce sets the new canonical nonce for the managed state
// Setnonce 함수는 관리된 상태에 대해 캐노니컬 논스를 설정한다
func (ms *ManagedState) SetNonce(addr common.Address, nonce uint64) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	so := ms.GetOrNewStateObject(addr)
	so.SetNonce(nonce)

	ms.accounts[addr] = newAccount(so)
}

// HasAccount returns whether the given address is managed or not
// 주어진 어드래스가 관리되는지 아닌지 여부를 반환한다
func (ms *ManagedState) HasAccount(addr common.Address) bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.hasAccount(addr)
}

func (ms *ManagedState) hasAccount(addr common.Address) bool {
	_, ok := ms.accounts[addr]
	return ok
}

// populate the managed state
// 관리될 상태를 생성한다
func (ms *ManagedState) getAccount(addr common.Address) *account {
	if account, ok := ms.accounts[addr]; !ok {
		so := ms.GetOrNewStateObject(addr)
		ms.accounts[addr] = newAccount(so)
	} else {
		// Always make sure the state account nonce isn't actually higher
		// than the tracked one.
		// 상태 계정 논스는 추적 대상보다 높을수 없다는 것을 확정함
		so := ms.StateDB.getStateObject(addr)
		if so != nil && uint64(len(account.nonces))+account.nstart < so.Nonce() {
			ms.accounts[addr] = newAccount(so)
		}

	}

	return ms.accounts[addr]
}

func newAccount(so *stateObject) *account {
	return &account{so, so.Nonce(), nil}
}
