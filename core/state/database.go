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

package state

import (
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/trie"
	lru "github.com/hashicorp/golang-lru"
)

// Trie cache generation limit after which to evict trie nodes from memory.
// 메모리상의 트라이 노드들을 퇴거시키기 위한 캐시 생성 한도
var MaxTrieCacheGen = uint16(120)

const (
	// Number of past tries to keep. This value is chosen such that
	// reasonable chain reorg depths will hit an existing trie.
	// 보유할 과거 트라이의 숫자. 이 값은 존재중인 트라이의 
	// 재구성을 발생시킬만한 값으로 선택되었음
	maxPastTries = 12

	// Number of codehash->size associations to keep.
	// 보유할 코드해시->크기 연결 쌍의 수
	codeSizeCacheSize = 100000
)

// Database wraps access to tries and contract code.
// Database인터페이스는 trie들과 계약 코드를 접근하는 메소드들을 포함한다
type Database interface {
	// OpenTrie opens the main account trie.
	// OpenTrie함수는 메인 계정 트라이를 연다
	OpenTrie(root common.Hash) (Trie, error)

	// OpenStorageTrie opens the storage trie of an account.
	// OpenStorageTrie함수는 계정의 스토리지 트라이를 연다
	OpenStorageTrie(addrHash, root common.Hash) (Trie, error)

	// CopyTrie returns an independent copy of the given trie.
	// CopyTrie 함수는 주어진 트라이의 독립적인 사본을 반환한다
	CopyTrie(Trie) Trie

	// ContractCode retrieves a particular contract's code.
	// ContractCode함수는 특정 계약의 코드를 반환한다
	ContractCode(addrHash, codeHash common.Hash) ([]byte, error)

	// ContractCodeSize retrieves a particular contracts code's size.
	// ContractCodeSize함수는 특정 계약 코드의 길이를 반환한다
	ContractCodeSize(addrHash, codeHash common.Hash) (int, error)

	// TrieDB retrieves the low level trie database used for data storage.
	// TireDB함수는 데이터를 저장하는데 사용될 저수준의 트라이 DB를 반환한다
	TrieDB() *trie.Database
}

// Trie is a Ethereum Merkle Trie.
// Tire는 이더리움 머클트리이다
type Trie interface {
	TryGet(key []byte) ([]byte, error)
	TryUpdate(key, value []byte) error
	TryDelete(key []byte) error
	Commit(onleaf trie.LeafCallback) (common.Hash, error)
	Hash() common.Hash
	NodeIterator(startKey []byte) trie.NodeIterator
	GetKey([]byte) []byte // TODO(fjl): remove this when SecureTrie is removed
	Prove(key []byte, fromLevel uint, proofDb ethdb.Putter) error
}

// NewDatabase creates a backing store for state. The returned database is safe for
// concurrent use and retains cached trie nodes in memory. The pool is an optional
// intermediate trie-node memory pool between the low level storage layer and the
// high level trie abstraction.
// NewDatabase함수는 상태를 저장하기 위핸 저장소를 생성한다
// 반환된 DB는 동시 사용과 메모리의 캐싱된 트라이 노드들을 유지하기에 안전하다
// Pool은 저수준 저장 계층과 고수준 트라이 추상화 사이의 임시적인 트라이 노드 메모리 풀 이다
func NewDatabase(db ethdb.Database) Database {
	csc, _ := lru.New(codeSizeCacheSize)
	return &cachingDB{
		db:            trie.NewDatabase(db),
		codeSizeCache: csc,
	}
}

type cachingDB struct {
	db            *trie.Database
	mu            sync.Mutex
	pastTries     []*trie.SecureTrie
	codeSizeCache *lru.Cache
}

// OpenTrie opens the main account trie.
// 이함수는 메인 계정 trie를 오픈한다
func (db *cachingDB) OpenTrie(root common.Hash) (Trie, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	for i := len(db.pastTries) - 1; i >= 0; i-- {
		if db.pastTries[i].Hash() == root {
			return cachedTrie{db.pastTries[i].Copy(), db}, nil
		}
	}
	tr, err := trie.NewSecure(root, db.db, MaxTrieCacheGen)
	if err != nil {
		return nil, err
	}
	return cachedTrie{tr, db}, nil
}

func (db *cachingDB) pushTrie(t *trie.SecureTrie) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if len(db.pastTries) >= maxPastTries {
		copy(db.pastTries, db.pastTries[1:])
		db.pastTries[len(db.pastTries)-1] = t
	} else {
		db.pastTries = append(db.pastTries, t)
	}
}

// OpenStorageTrie opens the storage trie of an account.
// OpenStroageTrie 함수는 계정의 저장소 트라이를 연다
func (db *cachingDB) OpenStorageTrie(addrHash, root common.Hash) (Trie, error) {
	return trie.NewSecure(root, db.db, 0)
}

// CopyTrie returns an independent copy of the given trie.
// CopyTrie 함수는 주어진 트라이의 독립적인 사본을 반환한다
func (db *cachingDB) CopyTrie(t Trie) Trie {
	switch t := t.(type) {
	case cachedTrie:
		return cachedTrie{t.SecureTrie.Copy(), db}
	case *trie.SecureTrie:
		return t.Copy()
	default:
		panic(fmt.Errorf("unknown trie type %T", t))
	}
}

// ContractCode retrieves a particular contract's code.
// ContractCode함수는 특정 계약의 코드를 반환한다
func (db *cachingDB) ContractCode(addrHash, codeHash common.Hash) ([]byte, error) {
	code, err := db.db.Node(codeHash)
	if err == nil {
		db.codeSizeCache.Add(codeHash, len(code))
	}
	return code, err
}

// ContractCodeSize retrieves a particular contracts code's size.
// ContractCodeSize함수는 특정 계약 코드의 길이를 반환한다
func (db *cachingDB) ContractCodeSize(addrHash, codeHash common.Hash) (int, error) {
	if cached, ok := db.codeSizeCache.Get(codeHash); ok {
		return cached.(int), nil
	}
	code, err := db.ContractCode(addrHash, codeHash)
	return len(code), err
}

// TrieDB retrieves any intermediate trie-node caching layer.
// TireDB함수는 캐싱 레이어의 중간단계 트라이노드를 모두 반환한다
// TireDB함수는 데이터를 저장하는데 사용될 저수준의 트라이 DB를 반환한다
func (db *cachingDB) TrieDB() *trie.Database {
	return db.db
}

// cachedTrie inserts its trie into a cachingDB on commit.
// cachedTrie는 commiit때 자신의 트라이를 캐싱DB에 삽입한다
type cachedTrie struct {
	*trie.SecureTrie
	db *cachingDB
}

func (m cachedTrie) Commit(onleaf trie.LeafCallback) (common.Hash, error) {
	root, err := m.SecureTrie.Commit(onleaf)
	if err == nil {
		m.db.pushTrie(m.SecureTrie)
	}
	return root, err
}

func (m cachedTrie) Prove(key []byte, fromLevel uint, proofDb ethdb.Putter) error {
	return m.SecureTrie.Prove(key, fromLevel, proofDb)
}
