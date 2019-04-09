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

package trie

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

// SecureTrie wraps a trie with key hashing. In a secure trie, all
// access operations hash the key using keccak256. This prevents
// calling code from creating long chains of nodes that
// increase the access time.
//
// Contrary to a regular trie, a SecureTrie can only be created with
// New and must have an attached database. The database also stores
// the preimage of each key.
//
// SecureTrie is not safe for concurrent use.
// SecureTrie는 트라이를 키 해싱으로 감싼다
// 시큐어 트리에서는 키에대한 모든 해싱은 keccak256이다
// 이것은 접속시간이 증거하는 노드의 긴 체인을 생성하는 코드를 방지한다
// 일반적인 트라이와 반대로 시큐어 트라이는 New에 의해서만 생성할수 있으며, 
// 연결된 db를 가져야 한다. db는 각 키의 사전이미지 역시 저장하고 있어야 한다
// thread safe하지 않다
type SecureTrie struct {
	trie             Trie
	hashKeyBuf       [common.HashLength]byte
	secKeyCache      map[string][]byte
	secKeyCacheOwner *SecureTrie // Pointer to self, replace the key cache on mismatch
}

// NewSecure creates a trie with an existing root node from a backing database
// and optional intermediate in-memory node pool.
//
// If root is the zero hash or the sha3 hash of an empty string, the
// trie is initially empty. Otherwise, New will panic if db is nil
// and returns MissingNodeError if the root node cannot be found.
//
// Accessing the trie loads nodes from the database or node pool on demand.
// Loaded nodes are kept around until their 'cache generation' expires.
// A new cache generation is created by each call to Commit.
// cachelimit sets the number of past cache generations to keep.
// NewSecure 함수는 지원하는 DB로부터 존재하는 루트 노드를 이용해 트라이를 생성하고
// 추가적인 임시 메모리 노드풀을 생성한다
// 만약 루트가 zero해시이거나 빈 문자열의 sha3해시라면, 트라이는 빈상태이다.
// 반면에 , db가 nil이면 패닉이 발생하고 노드가 찾아지지 않을 경우 missingNodeError를 발생시킨다
// 트라이의 접근은 노드를 db나 메모리 풀로부터 로드한다
// 읽혀진 노드들은 케시 세대가 만료될때까지 저장된다
// 새로운 캐시 세대는 매 commit때마다 생성된다.
// 캐시리밋은 보관해야할 지난 세대의 갯수를 설정한다
func NewSecure(root common.Hash, db *Database, cachelimit uint16) (*SecureTrie, error) {
	if db == nil {
		panic("trie.NewSecure called without a database")
	}
	trie, err := New(root, db)
	if err != nil {
		return nil, err
	}
	trie.SetCacheLimit(cachelimit)
	return &SecureTrie{trie: *trie}, nil
}

// Get returns the value for key stored in the trie.
// The value bytes must not be modified by the caller.
// Get함수는 트라이에 저장된 키를 위한 값을 반환한다
// 값은 호출자에의해 수정되어서는 안된다
func (t *SecureTrie) Get(key []byte) []byte {
	res, err := t.TryGet(key)
	if err != nil {
		log.Error(fmt.Sprintf("Unhandled trie error: %v", err))
	}
	return res
}

// TryGet returns the value for key stored in the trie.
// The value bytes must not be modified by the caller.
// If a node was not found in the database, a MissingNodeError is returned.
// Get함수는 트라이에 저장된 키를 위한 값을 반환한다
// 값은 호출자에의해 수정되어서는 안된다
// 만약 노드가 db에서 찾아지지 않는다면 missingnodeerror가 반환된다
func (t *SecureTrie) TryGet(key []byte) ([]byte, error) {
	return t.trie.TryGet(t.hashKey(key))
}

// Update associates key with value in the trie. Subsequent calls to
// Get will return value. If value has length zero, any existing value
// is deleted from the trie and calls to Get will return nil.
//
// The value bytes must not be modified by the caller while they are
// stored in the trie.
// Update 함수는 트라이의 키와 값을 연결시킨다
// 다음의 호출들이 값을 리턴할 것이다.
// 만약 값의 길이가 0이라면 존재하는 값은 트라이로부터 삭제되고 Get함수들은 nill을 리턴한다
func (t *SecureTrie) Update(key, value []byte) {
	if err := t.TryUpdate(key, value); err != nil {
		log.Error(fmt.Sprintf("Unhandled trie error: %v", err))
	}
}

// TryUpdate associates key with value in the trie. Subsequent calls to
// Get will return value. If value has length zero, any existing value
// is deleted from the trie and calls to Get will return nil.
//
// The value bytes must not be modified by the caller while they are
// stored in the trie.
//
// If a node was not found in the database, a MissingNodeError is returned.
// Try Update 함수는 트라이의 키와 값을 연결시킨다
// 다음의 호출들이 값을 리턴할 것이다.
// 만약 값의 길이가 0이라면 존재하는 값은 트라이로부터 삭제되고 Get함수들은 nill을 리턴한다
// 값은 호출자에의해 수정되어서는 안된다
// 만약 노드가 db에서 찾아지지 않는다면 missingnodeerror가 반환된다
func (t *SecureTrie) TryUpdate(key, value []byte) error {
	hk := t.hashKey(key)
	err := t.trie.TryUpdate(hk, value)
	if err != nil {
		return err
	}
	t.getSecKeyCache()[string(hk)] = common.CopyBytes(key)
	return nil
}

// Delete removes any existing value for key from the trie.
// Delete 함수는 키를 위한 모든 값을 트라이로부터 제거한다
func (t *SecureTrie) Delete(key []byte) {
	if err := t.TryDelete(key); err != nil {
		log.Error(fmt.Sprintf("Unhandled trie error: %v", err))
	}
}

// TryDelete removes any existing value for key from the trie.
// If a node was not found in the database, a MissingNodeError is returned.
// TryDelete removes any existing value for key from the trie.
// 만약 노드가 db에서 찾아지지 않는다면 missingnodeerror가 반환된다
func (t *SecureTrie) TryDelete(key []byte) error {
	hk := t.hashKey(key)
	delete(t.getSecKeyCache(), string(hk))
	return t.trie.TryDelete(hk)
}

// GetKey returns the sha3 preimage of a hashed key that was
// previously used to store a value.
//GetKey함수는 value를 저장하기위해 쓰였던 해싱된 키의 sha3 사전이미지를 반환한다
func (t *SecureTrie) GetKey(shaKey []byte) []byte {
	if key, ok := t.getSecKeyCache()[string(shaKey)]; ok {
		return key
	}
	key, _ := t.trie.db.preimage(common.BytesToHash(shaKey))
	return key
}

// Commit writes all nodes and the secure hash pre-images to the trie's database.
// Nodes are stored with their sha3 hash as the key.
//
// Committing flushes nodes from memory. Subsequent Get calls will load nodes
// from the database.
// Commit 함수는 모든 노드와 secure hash 사전이미지를 트라이 DB에 쓴다
// 노드는 그들의 sha3 hash를 키로서 저장된다
// 커밋을 하는 것은 노드를 메모리로 부터 플러시시킨다. 다음 get 호출이 노드를 DB로 부터 읽을것이다
func (t *SecureTrie) Commit(onleaf LeafCallback) (root common.Hash, err error) {
	// Write all the pre-images to the actual disk database
	if len(t.getSecKeyCache()) > 0 {
		t.trie.db.lock.Lock()
		for hk, key := range t.secKeyCache {
			t.trie.db.insertPreimage(common.BytesToHash([]byte(hk)), key)
		}
		t.trie.db.lock.Unlock()

		t.secKeyCache = make(map[string][]byte)
	}
	// Commit the trie to its intermediate node database
	return t.trie.Commit(onleaf)
}

func (t *SecureTrie) Hash() common.Hash {
	return t.trie.Hash()
}

func (t *SecureTrie) Root() []byte {
	return t.trie.Root()
}

func (t *SecureTrie) Copy() *SecureTrie {
	cpy := *t
	return &cpy
}

// NodeIterator returns an iterator that returns nodes of the underlying trie. Iteration
// starts at the key after the given start key.
// NodeIterator 함수는 트라이 아래의 노드들을 반환하는 반복자를 반환한다.
// 반복은 주어진 startkey다음부터 시작한다
func (t *SecureTrie) NodeIterator(start []byte) NodeIterator {
	return t.trie.NodeIterator(start)
}

// hashKey returns the hash of key as an ephemeral buffer.
// The caller must not hold onto the return value because it will become
// invalid on the next call to hashKey or secKey.
// hashKey함수는 임시버퍼에 키의 해시를 반환한다
// 호출자는 버퍼를 수정하면 안된다, 왜냐하면 다음 호출에서 값이 무효화되기 때문이다
func (t *SecureTrie) hashKey(key []byte) []byte {
	h := newHasher(0, 0, nil)
	h.sha.Reset()
	h.sha.Write(key)
	buf := h.sha.Sum(t.hashKeyBuf[:0])
	returnHasherToPool(h)
	return buf
}

// getSecKeyCache returns the current secure key cache, creating a new one if
// ownership changed (i.e. the current secure trie is a copy of another owning
// the actual cache).
// getSecKeyCache 함수는 현재 secure key의 캐시를 반환하고, 만약 소유권이 변할경우 새로 생성한다
func (t *SecureTrie) getSecKeyCache() map[string][]byte {
	if t != t.secKeyCacheOwner {
		t.secKeyCacheOwner = t
		t.secKeyCache = make(map[string][]byte)
	}
	return t.secKeyCache
}
