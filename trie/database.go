// Copyright 2018 The go-ethereum Authors
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

package trie

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

// secureKeyPrefix is the database key prefix used to store trie node preimages.
// secureKeyPrefix는 트라이 노드의 사전 이미지를 저정하기 위해 사용되는 DB key 접두어
var secureKeyPrefix = []byte("secure-key-")

// secureKeyLength is the length of the above prefix + 32byte hash.
// secureKeyLength는 securekeyprefix + 32byte 해시
const secureKeyLength = 11 + 32

// DatabaseReader wraps the Get and Has method of a backing store for the trie.
type DatabaseReader interface {
	// Get retrieves the value associated with key form the database.
	Get(key []byte) (value []byte, err error)

	// Has retrieves whether a key is present in the database.
	Has(key []byte) (bool, error)
}

// Database is an intermediate write layer between the trie data structures and
// the disk database. The aim is to accumulate trie writes in-memory and only
// periodically flush a couple tries to disk, garbage collecting the remainder.
// Database 구조체는 트라이 데이터 구조체와 디스크 데이터베이스 사이의 중간 쓰기 계층이다.
// 목적은 메모레에 트라이를 누적하고 주기적으로만 몇개의 트리를 디스크에 플러쉬하고
// 남은것에 대한 가비지 컬렉션을 위해서이다
type Database struct {
	diskdb ethdb.Database // Persistent storage for matured trie nodes

	nodes     map[common.Hash]*cachedNode // Data and references relationships of a node
	preimages map[common.Hash][]byte      // Preimages of nodes from the secure trie
	seckeybuf [secureKeyLength]byte       // Ephemeral buffer for calculating preimage keys

	gctime  time.Duration      // Time spent on garbage collection since last commit
	gcnodes uint64             // Nodes garbage collected since last commit
	gcsize  common.StorageSize // Data storage garbage collected since last commit

	nodesSize     common.StorageSize // Storage size of the nodes cache
	preimagesSize common.StorageSize // Storage size of the preimages cache

	lock sync.RWMutex
}

// cachedNode is all the information we know about a single cached node in the
// memory database write layer.
// cachedNode는 메모리 데이터베이스 쓰기 계층에 캐싱된 하나의 노드에 대한 모든 정보이다
type cachedNode struct {
	blob     []byte              // Cached data block of the trie node
	parents  int                 // Number of live nodes referencing this one
	children map[common.Hash]int // Children referenced by this nodes
}

// NewDatabase creates a new trie database to store ephemeral trie content before
// its written out to disk or garbage collected.
// NewDatabase 함수는 디스크에 쓰여지거나, 가비지 콜렉트 되기전의 
// 수명이 짧은 트라이 컨텐츠를 저장하기 위한 새로운 트라이 데이테베이스 를 생성한다
func NewDatabase(diskdb ethdb.Database) *Database {
	return &Database{
		diskdb: diskdb,
		nodes: map[common.Hash]*cachedNode{
			{}: {children: make(map[common.Hash]int)},
		},
		preimages: make(map[common.Hash][]byte),
	}
}

// DiskDB retrieves the persistent storage backing the trie database.
// DiskDB는 trie DB를 지원하기 위한 지속되는 스토리지를 반환한다
func (db *Database) DiskDB() DatabaseReader {
	return db.diskdb
}

// Insert writes a new trie node to the memory database if it's yet unknown. The
// method will make a copy of the slice.
// Insert 함수는 만약 이 노드가 알려지지 않았다면 새로운 트라이 노드를 메모리 DB에 쓴다 
// 조각의 복사본을 만들것이다
func (db *Database) Insert(hash common.Hash, blob []byte) {
	db.lock.Lock()
	defer db.lock.Unlock()

	db.insert(hash, blob)
}

// insert is the private locked version of Insert.
// insert함수는 lock버전의 Insert 이다
func (db *Database) insert(hash common.Hash, blob []byte) {
	if _, ok := db.nodes[hash]; ok {
		return
	}
	db.nodes[hash] = &cachedNode{
		blob:     common.CopyBytes(blob),
		children: make(map[common.Hash]int),
	}
	db.nodesSize += common.StorageSize(common.HashLength + len(blob))
}

// insertPreimage writes a new trie node pre-image to the memory database if it's
// yet unknown. The method will make a copy of the slice.
//
// Note, this method assumes that the database's lock is held!
// insertPreimage함수는 알려지지 않은 새로운 트라이노드의 사전이미지를 메모리 db에 쓴다
// 조각의 사본을 만들것이다
// 이 메소드는 DB lock이 걸려있다고 가정한다
func (db *Database) insertPreimage(hash common.Hash, preimage []byte) {
	if _, ok := db.preimages[hash]; ok {
		return
	}
	db.preimages[hash] = common.CopyBytes(preimage)
	db.preimagesSize += common.StorageSize(common.HashLength + len(preimage))
}

// Node retrieves a cached trie node from memory. If it cannot be found cached,
// the method queries the persistent database for the content.
// Node함수는 메모리로 부터 캐싱된 트라이노드를 반환한다. 
// 만약 캐시에서 찾지 못한다면 지속적인 DB로부 컨텐츠를 문의한다
func (db *Database) Node(hash common.Hash) ([]byte, error) {
	// Retrieve the node from cache if available
	db.lock.RLock()
	node := db.nodes[hash]
	db.lock.RUnlock()

	if node != nil {
		return node.blob, nil
	}
	// Content unavailable in memory, attempt to retrieve from disk
	return db.diskdb.Get(hash[:])
}

// preimage retrieves a cached trie node pre-image from memory. If it cannot be
// found cached, the method queries the persistent database for the content.
// preimage 함수는 캐싱된 트라이노드 사전이미지를 반환한다
// 만약 캐시에서 찾지 못한다면 지속적인 DB로부 컨텐츠를 문의한다
func (db *Database) preimage(hash common.Hash) ([]byte, error) {
	// Retrieve the node from cache if available
	db.lock.RLock()
	preimage := db.preimages[hash]
	db.lock.RUnlock()

	if preimage != nil {
		return preimage, nil
	}
	// Content unavailable in memory, attempt to retrieve from disk
	return db.diskdb.Get(db.secureKey(hash[:]))
}

// secureKey returns the database key for the preimage of key, as an ephemeral
// buffer. The caller must not hold onto the return value because it will become
// invalid on the next call.
// secureKey함수는 키의 사전 이미지를 위한 DB키를 임시버퍼에 반환한다
// 해당값은 다음 호출에서 무효해지기 때문에 호출자는 해당 값을 저장하면 안된다 
func (db *Database) secureKey(key []byte) []byte {
	buf := append(db.seckeybuf[:0], secureKeyPrefix...)
	buf = append(buf, key...)
	return buf
}

// Nodes retrieves the hashes of all the nodes cached within the memory database.
// This method is extremely expensive and should only be used to validate internal
// states in test code.
// nodes함수는 메모리 DB에 캐싱된 모든 노드의 해시를 반환한다.
// 이 메소드는 굉장비 비싸고 테스트 코드의 내부 상태 검증에만 쓰여야한다
func (db *Database) Nodes() []common.Hash {
	db.lock.RLock()
	defer db.lock.RUnlock()

	var hashes = make([]common.Hash, 0, len(db.nodes))
	for hash := range db.nodes {
		if hash != (common.Hash{}) { // Special case for "root" references/nodes
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

// Reference adds a new reference from a parent node to a child node.
// Reference함수는 부모로부터 자식으로 가는 새로운 참조를 추가한다
func (db *Database) Reference(child common.Hash, parent common.Hash) {
	db.lock.RLock()
	defer db.lock.RUnlock()

	db.reference(child, parent)
}

// reference is the private locked version of Reference.
// reference함수는 Reference함수의 lock버전이다
func (db *Database) reference(child common.Hash, parent common.Hash) {
	// If the node does not exist, it's a node pulled from disk, skip
	node, ok := db.nodes[child]
	if !ok {
		return
	}
	// If the reference already exists, only duplicate for roots
	if _, ok = db.nodes[parent].children[child]; ok && parent != (common.Hash{}) {
		return
	}
	node.parents++
	db.nodes[parent].children[child]++
}

// Dereference removes an existing reference from a parent node to a child node.
// Dereference함수는 부모노드에서 자식노드로의 참조를 제거한다
func (db *Database) Dereference(child common.Hash, parent common.Hash) {
	db.lock.Lock()
	defer db.lock.Unlock()

	nodes, storage, start := len(db.nodes), db.nodesSize, time.Now()
	db.dereference(child, parent)

	db.gcnodes += uint64(nodes - len(db.nodes))
	db.gcsize += storage - db.nodesSize
	db.gctime += time.Since(start)

	log.Debug("Dereferenced trie from memory database", "nodes", nodes-len(db.nodes), "size", storage-db.nodesSize, "time", time.Since(start),
		"gcnodes", db.gcnodes, "gcsize", db.gcsize, "gctime", db.gctime, "livenodes", len(db.nodes), "livesize", db.nodesSize)
}

// dereference is the private locked version of Dereference.
// dereference함수는 Dereference함수의 lock버전이다
func (db *Database) dereference(child common.Hash, parent common.Hash) {
	// Dereference the parent-child
	node := db.nodes[parent]

	node.children[child]--
	if node.children[child] == 0 {
		delete(node.children, child)
	}
	// If the node does not exist, it's a previously committed node.
	node, ok := db.nodes[child]
	if !ok {
		return
	}
	// If there are no more references to the child, delete it and cascade
	node.parents--
	if node.parents == 0 {
		for hash := range node.children {
			db.dereference(hash, child)
		}
		delete(db.nodes, child)
		db.nodesSize -= common.StorageSize(common.HashLength + len(node.blob))
	}
}

// Commit iterates over all the children of a particular node, writes them out
// to disk, forcefully tearing down all references in both directions.
//
// As a side effect, all pre-images accumulated up to this point are also written.
// Commit함수는 특정 노드의 모든 자식노드에 대해 반복하며 디스크에 쓰고
// 양쪽 방향의 참조를 강제로 해체한다
// 부수효과로 현재까지 누적된 모든 사전이미지도 같이 써진다
func (db *Database) Commit(node common.Hash, report bool) error {
	// Create a database batch to flush persistent data out. It is important that
	// outside code doesn't see an inconsistent state (referenced data removed from
	// memory cache during commit but not yet in persistent storage). This is ensured
	// by only uncaching existing data when the database write finalizes.
	db.lock.RLock()

	start := time.Now()
	batch := db.diskdb.NewBatch()

	// Move all of the accumulated preimages into a write batch
	for hash, preimage := range db.preimages {
		if err := batch.Put(db.secureKey(hash[:]), preimage); err != nil {
			log.Error("Failed to commit preimage from trie database", "err", err)
			db.lock.RUnlock()
			return err
		}
		if batch.ValueSize() > ethdb.IdealBatchSize {
			if err := batch.Write(); err != nil {
				return err
			}
			batch.Reset()
		}
	}
	// Move the trie itself into the batch, flushing if enough data is accumulated
	nodes, storage := len(db.nodes), db.nodesSize+db.preimagesSize
	if err := db.commit(node, batch); err != nil {
		log.Error("Failed to commit trie from trie database", "err", err)
		db.lock.RUnlock()
		return err
	}
	// Write batch ready, unlock for readers during persistence
	if err := batch.Write(); err != nil {
		log.Error("Failed to write trie to disk", "err", err)
		db.lock.RUnlock()
		return err
	}
	db.lock.RUnlock()

	// Write successful, clear out the flushed data
	db.lock.Lock()
	defer db.lock.Unlock()

	db.preimages = make(map[common.Hash][]byte)
	db.preimagesSize = 0

	db.uncache(node)

	logger := log.Info
	if !report {
		logger = log.Debug
	}
	logger("Persisted trie from memory database", "nodes", nodes-len(db.nodes), "size", storage-db.nodesSize, "time", time.Since(start),
		"gcnodes", db.gcnodes, "gcsize", db.gcsize, "gctime", db.gctime, "livenodes", len(db.nodes), "livesize", db.nodesSize)

	// Reset the garbage collection statistics
	db.gcnodes, db.gcsize, db.gctime = 0, 0, 0

	return nil
}

// commit is the private locked version of Commit.
// commit함수는 Commit함수의 lock버전이다
func (db *Database) commit(hash common.Hash, batch ethdb.Batch) error {
	// If the node does not exist, it's a previously committed node
	node, ok := db.nodes[hash]
	if !ok {
		return nil
	}
	for child := range node.children {
		if err := db.commit(child, batch); err != nil {
			return err
		}
	}
	if err := batch.Put(hash[:], node.blob); err != nil {
		return err
	}
	// If we've reached an optimal match size, commit and start over
	if batch.ValueSize() >= ethdb.IdealBatchSize {
		if err := batch.Write(); err != nil {
			return err
		}
		batch.Reset()
	}
	return nil
}

// uncache is the post-processing step of a commit operation where the already
// persisted trie is removed from the cache. The reason behind the two-phase
// commit is to ensure consistent data availability while moving from memory
// to disk.
// uncache함수는 commit 동작의 후처리 스텝으로 이미 기록된 트라이가 캐시로부터 제거된다.
// commit이 두번으로 이뤄진 이유는 메모리에서 db로 데이터를 옮길때의 
// 데이터의 유효성을 보장하기 위해서이다
func (db *Database) uncache(hash common.Hash) {
	// If the node does not exist, we're done on this path
	node, ok := db.nodes[hash]
	if !ok {
		return
	}
	// Otherwise uncache the node's subtries and remove the node itself too
	for child := range node.children {
		db.uncache(child)
	}
	delete(db.nodes, hash)
	db.nodesSize -= common.StorageSize(common.HashLength + len(node.blob))
}

// Size returns the current storage size of the memory cache in front of the
// persistent database layer.
// Size 함수는 지속되는 db레이어의 앞쪽의 메모리캐시의 저장소 사이즈를 반환한다
func (db *Database) Size() common.StorageSize {
	db.lock.RLock()
	defer db.lock.RUnlock()

	return db.nodesSize + db.preimagesSize
}
