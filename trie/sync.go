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
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"gopkg.in/karalabe/cookiejar.v2/collections/prque"
)

// ErrNotRequested is returned by the trie sync when it's requested to process a
// node it did not request.
var ErrNotRequested = errors.New("not requested")

// ErrAlreadyProcessed is returned by the trie sync when it's requested to process a
// node it already processed previously.
var ErrAlreadyProcessed = errors.New("already processed")

// request represents a scheduled or already in-flight state retrieval request.
// request 구조체는 스케쥴되거나 처리상태의 반환요청을 나타낸다
type request struct {
	hash common.Hash // Hash of the node data content to retrieve
	// 반환될 노드데이터의 해시
	data []byte      // Data content of the node, cached until all subtrees complete
	// 노드의 데이터. 모든 서브트리가 완성될때까지 캐싱됨
	raw  bool        // Whether this is a raw entry (code) or a trie node
	// raw entry인지 trie노드인지 구분

	parents []*request // Parent state nodes referencing this entry (notify all upon completion)
	// 이 엔트리를 참조하는 부모상태노드들
	depth   int        // Depth level within the trie the node is located to prioritise DFS
	// 우선순위된 DFS에 저장된 노드의 트라이의 깊이레벨
	deps    int        // Number of dependencies before allowed to commit this node
	// 노드를 commit하기 전 허용된 종속성의 수

	callback LeafCallback // Callback to invoke if a leaf node it reached on this branch
	// 이 브랜치에 leaf노드가 도달했을때 호출될 콜백함수
}

// SyncResult is a simple list to return missing nodes along with their request
// hashes.
// SyncResult구조체는 요청된 해시들에 missing 노드들을 리턴하기 위한 단순 리스트 이다
type SyncResult struct {
	Hash common.Hash // Hash of the originally unknown trie node
	// 원래 알려지지 않은 trie node의 해시
	Data []byte      // Data content of the retrieved node
	// 반환된 노드의 데이터 내용
}

// syncMemBatch is an in-memory buffer of successfully downloaded but not yet
// persisted data items.
// syncMemBatch 구조체는 성공적으로 다운로드 되었지만 
// 데이터가 아직 계속되지 않은 메모리상의 버퍼이다
type syncMemBatch struct {
	batch map[common.Hash][]byte // In-memory membatch of recently completed items
	// 최근 완성된 아이템의 메모리상 batch
	order []common.Hash          // Order of completion to prevent out-of-order data loss
	// 비순차적 데이터 손실을 방지하기위한 완료 순서
}

// newSyncMemBatch allocates a new memory-buffer for not-yet persisted trie nodes.
// 아직 연속되지않은 트라이 노드를 위한 새로운 메모리 버퍼를 할당한다
func newSyncMemBatch() *syncMemBatch {
	return &syncMemBatch{
		batch: make(map[common.Hash][]byte),
		order: make([]common.Hash, 0, 256),
	}
}

// TrieSync is the main state trie synchronisation scheduler, which provides yet
// unknown trie hashes to retrieve, accepts node data associated with said hashes
// and reconstructs the trie step by step until all is done.
// trieSync 구조체는 상태 트라이 동기화를 위한 메인 스케쥴러이다
// 반환해야하는 아직 알려지지 않은 트라이 해시를 제공하고
// 해시와 관련된 노드데이터를 수락하고
// 모든것이 끝날때까지 단계뼐로 트라이를 제생성한다 
type TrieSync struct {
	database DatabaseReader           // Persistent database to check for existing entries
	//주어진 목록을 체크하기 위해 연결된 DB
	membatch *syncMemBatch            // Memory buffer to avoid frequest database writes
	// db의 잦은 쓰기를 방지하기위한 메모리 버퍼
	requests map[common.Hash]*request // Pending requests pertaining to a key hash
	// 키 해시에 관계된 대기중인 요청
	queue    *prque.Prque             // Priority queue with the pending requests
	// 대기중 요청을 위한 우선순위큐
}

// NewTrieSync creates a new trie data download scheduler.
// NewTrieSync함수는 트라이 데이터 다운로드 스케쥴러를 생성한다
func NewTrieSync(root common.Hash, database DatabaseReader, callback LeafCallback) *TrieSync {
	ts := &TrieSync{
		database: database,
		membatch: newSyncMemBatch(),
		requests: make(map[common.Hash]*request),
		queue:    prque.New(),
	}
	ts.AddSubTrie(root, 0, common.Hash{}, callback)
	return ts
}

// AddSubTrie registers a new trie to the sync code, rooted at the designated parent.
// AddSubTrie함수는 새로운 트라이를 싱크 코드에 등록하고, 지정된 부모에 자리잡도록 한다
func (s *TrieSync) AddSubTrie(root common.Hash, depth int, parent common.Hash, callback LeafCallback) {
	// Short circuit if the trie is empty or already known
	if root == emptyRoot {
		return
	}
	if _, ok := s.membatch.batch[root]; ok {
		return
	}
	key := root.Bytes()
	blob, _ := s.database.Get(key)
	if local, err := decodeNode(key, blob, 0); local != nil && err == nil {
		return
	}
	// Assemble the new sub-trie sync request
	req := &request{
		hash:     root,
		depth:    depth,
		callback: callback,
	}
	// If this sub-trie has a designated parent, link them together
	if parent != (common.Hash{}) {
		ancestor := s.requests[parent]
		if ancestor == nil {
			panic(fmt.Sprintf("sub-trie ancestor not found: %x", parent))
		}
		ancestor.deps++
		req.parents = append(req.parents, ancestor)
	}
	s.schedule(req)
}

// AddRawEntry schedules the direct retrieval of a state entry that should not be
// interpreted as a trie node, but rather accepted and stored into the database
// as is. This method's goal is to support misc state metadata retrievals (e.g.
// contract code).
// AddRawEntry함수는 트라이 노드로 해석되지 않지만 수락되어 DB에 저장될 스테이트 엔트리에 대한 
// 직접적인 반환을 스케쥴한다
// 이러한 메소드의 목적은 콘트렉트 코드처럼 정형화 되지않은 
// 상태의 메타데이터 반환을 지원하기 위함이다
func (s *TrieSync) AddRawEntry(hash common.Hash, depth int, parent common.Hash) {
	// Short circuit if the entry is empty or already known
	if hash == emptyState {
		return
	}
	if _, ok := s.membatch.batch[hash]; ok {
		return
	}
	if ok, _ := s.database.Has(hash.Bytes()); ok {
		return
	}
	// Assemble the new sub-trie sync request
	req := &request{
		hash:  hash,
		raw:   true,
		depth: depth,
	}
	// If this sub-trie has a designated parent, link them together
	if parent != (common.Hash{}) {
		ancestor := s.requests[parent]
		if ancestor == nil {
			panic(fmt.Sprintf("raw-entry ancestor not found: %x", parent))
		}
		ancestor.deps++
		req.parents = append(req.parents, ancestor)
	}
	s.schedule(req)
}

// Missing retrieves the known missing nodes from the trie for retrieval.
// Missing함수는 트라이로부터 알려진 미싱노드를 반환한다
func (s *TrieSync) Missing(max int) []common.Hash {
	requests := []common.Hash{}
	for !s.queue.Empty() && (max == 0 || len(requests) < max) {
		requests = append(requests, s.queue.PopItem().(common.Hash))
	}
	return requests
}

// Process injects a batch of retrieved trie nodes data, returning if something
// was committed to the database and also the index of an entry if processing of
// it failed.
// Process함수는 반환된 다수의 트라이노드 데이터를 주입하고, db에 무언가 써졌거나
// 처리가 실패했을 경우 엔트리의 인덱스를 반환한다
func (s *TrieSync) Process(results []SyncResult) (bool, int, error) {
	committed := false

	for i, item := range results {
		// If the item was not requested, bail out
		request := s.requests[item.Hash]
		if request == nil {
			return committed, i, ErrNotRequested
		}
		if request.data != nil {
			return committed, i, ErrAlreadyProcessed
		}
		// If the item is a raw entry request, commit directly
		if request.raw {
			request.data = item.Data
			s.commit(request)
			committed = true
			continue
		}
		// Decode the node data content and update the request
		node, err := decodeNode(item.Hash[:], item.Data, 0)
		if err != nil {
			return committed, i, err
		}
		request.data = item.Data

		// Create and schedule a request for all the children nodes
		requests, err := s.children(request, node)
		if err != nil {
			return committed, i, err
		}
		if len(requests) == 0 && request.deps == 0 {
			s.commit(request)
			committed = true
			continue
		}
		request.deps += len(requests)
		for _, child := range requests {
			s.schedule(child)
		}
	}
	return committed, 0, nil
}

// Commit flushes the data stored in the internal membatch out to persistent
// storage, returning the number of items written and any occurred error.
// Commit 함수는 내부 메모리에 저장된 데이터를 영속적인 저장소에 저장하고, 
// 쓰여진 아이템의 갯수와 발생한 에러를 반환한다
func (s *TrieSync) Commit(dbw ethdb.Putter) (int, error) {
	// Dump the membatch into a database dbw
	for i, key := range s.membatch.order {
		if err := dbw.Put(key[:], s.membatch.batch[key]); err != nil {
			return i, err
		}
	}
	written := len(s.membatch.order)

	// Drop the membatch data and return
	s.membatch = newSyncMemBatch()
	return written, nil
}

// Pending returns the number of state entries currently pending for download.
// pending함수는 다운로드를 위해 대기중인 상태의 엔트리를 반환한다
func (s *TrieSync) Pending() int {
	return len(s.requests)
}

// schedule inserts a new state retrieval request into the fetch queue. If there
// is already a pending request for this node, the new request will be discarded
// and only a parent reference added to the old one.
// Schedule함수는 페치큐에 새로운 상태 반환 요청을 삽입한다.
// 만약 이미 이노드를 위한 대기 요청이 있을경우, 새로운 요청은 무시되고 
// 부모의 참조만 기존것으로 추가된다
func (s *TrieSync) schedule(req *request) {
	// If we're already requesting this node, add a new reference and stop
	if old, ok := s.requests[req.hash]; ok {
		old.parents = append(old.parents, req.parents...)
		return
	}
	// Schedule the request for future retrieval
	s.queue.Push(req.hash, float32(req.depth))
	s.requests[req.hash] = req
}

// children retrieves all the missing children of a state trie entry for future
// retrieval scheduling.
// children 함수는 미래의 반환 스케쥴을 위한 misiing 자녀 상태 엔트리를 반환한다
func (s *TrieSync) children(req *request, object node) ([]*request, error) {
	// Gather all the children of the node, irrelevant whether known or not
	type child struct {
		node  node
		depth int
	}
	children := []child{}

	switch node := (object).(type) {
	case *shortNode:
		children = []child{{
			node:  node.Val,
			depth: req.depth + len(node.Key),
		}}
	case *fullNode:
		for i := 0; i < 17; i++ {
			if node.Children[i] != nil {
				children = append(children, child{
					node:  node.Children[i],
					depth: req.depth + 1,
				})
			}
		}
	default:
		panic(fmt.Sprintf("unknown node: %+v", node))
	}
	// Iterate over the children, and request all unknown ones
	requests := make([]*request, 0, len(children))
	for _, child := range children {
		// Notify any external watcher of a new key/value node
		if req.callback != nil {
			if node, ok := (child.node).(valueNode); ok {
				if err := req.callback(node, req.hash); err != nil {
					return nil, err
				}
			}
		}
		// If the child references another node, resolve or schedule
		if node, ok := (child.node).(hashNode); ok {
			// Try to resolve the node from the local database
			hash := common.BytesToHash(node)
			if _, ok := s.membatch.batch[hash]; ok {
				continue
			}
			if ok, _ := s.database.Has(node); ok {
				continue
			}
			// Locally unknown node, schedule for retrieval
			requests = append(requests, &request{
				hash:     hash,
				parents:  []*request{req},
				depth:    child.depth,
				callback: req.callback,
			})
		}
	}
	return requests, nil
}

// commit finalizes a retrieval request and stores it into the membatch. If any
// of the referencing parent requests complete due to this commit, they are also
// committed themselves.
// coomit 함수는 반환요청을 최종화하고, 메모리 배치에 저장한다. 
// 만약 참조하는 부모의 요청들이 이 commit으로 완료되면 그들도 스스로 commit 된다
func (s *TrieSync) commit(req *request) (err error) {
	// Write the node content to the membatch
	s.membatch.batch[req.hash] = req.data
	s.membatch.order = append(s.membatch.order, req.hash)

	delete(s.requests, req.hash)

	// Check all parents for completion
	for _, parent := range req.parents {
		parent.deps--
		if parent.deps == 0 {
			if err := s.commit(parent); err != nil {
				return err
			}
		}
	}
	return nil
}
