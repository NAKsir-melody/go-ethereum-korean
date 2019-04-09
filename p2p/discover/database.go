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

// Contains the node database, storing previously seen nodes and any collected
// metadata about them for QoS purposes.
// 이전에 보였던 노드를 저장하고 qos를 위해 수집된 메타데이터를 저장하는
// node DB를 포함한다 

package discover

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/util"
)

var (
	nodeDBNilNodeID      = NodeID{}       // Special node ID to use as a nil element.
	// nil원소로서 사용할 특수한 노드 ID
	nodeDBNodeExpiration = 24 * time.Hour // Time after which an unseen node should be dropped.
	// 특정시간동안 보이지 않는 노드는 드롭되어야 한다
	nodeDBCleanupCycle   = time.Hour      // Time period for running the expiration task.
	// 초과 업무를 실행할 시간단위
)

// nodeDB stores all nodes we know about.
// nodeDB는 우리가 알고있는 모든 노드를 저장한다
type nodeDB struct {
	lvl    *leveldb.DB   // Interface to the database itself
	// db 자체 인터페이스
	self   NodeID        // Own node id to prevent adding it into the database
	// DB에 추가하는 것을 박기위한 자기의 node ID
	runner sync.Once     // Ensures we can start at most one expirer
	// 하나가 끝났을때만 실행가능한것을 보장함
	quit   chan struct{} // Channel to signal the expiring thread to stop
	// 스레드를 종료시키기 위한 시그널을 전송할 채널
}

// Schema layout for the node database
// 노드 DB의 스키마 레이아웃
var (
	nodeDBVersionKey = []byte("version") // Version of the database to flush if changes
	// 변화가 있을때  flush할 DB의 버전
	nodeDBItemPrefix = []byte("n:")      // Identifier to prefix node entries with
	// 확인을 위한 node 접두사

	nodeDBDiscoverRoot      = ":discover"
	nodeDBDiscoverPing      = nodeDBDiscoverRoot + ":lastping"
	nodeDBDiscoverPong      = nodeDBDiscoverRoot + ":lastpong"
	nodeDBDiscoverFindFails = nodeDBDiscoverRoot + ":findfail"
)

// newNodeDB creates a new node database for storing and retrieving infos about
// known peers in the network. If no path is given, an in-memory, temporary
// database is constructed.
// newNodeDB 함수는 네트워크 상에 알려진 노드에 대한 정보를 저장하고 반환하기 위한
// 새로운 node db를 생성한다. 만약 경루가 주어지지 않으면 메모리상에 임시 DB가 생성된다
func newNodeDB(path string, version int, self NodeID) (*nodeDB, error) {
	if path == "" {
		return newMemoryNodeDB(self)
	}
	return newPersistentNodeDB(path, version, self)
}

// newMemoryNodeDB creates a new in-memory node database without a persistent
// backend.
// newMemoryNodeDB 함수는 항상성을 갖는 백앤드 없이 메모리상에 노드 DB를 생성한다
func newMemoryNodeDB(self NodeID) (*nodeDB, error) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		return nil, err
	}
	return &nodeDB{
		lvl:  db,
		self: self,
		quit: make(chan struct{}),
	}, nil
}

// newPersistentNodeDB creates/opens a leveldb backed persistent node database,
// also flushing its contents in case of a version mismatch.
// newPersistenNodeDB는항상성을 갖는 no DB로서  leveldb를 생성하거나 오픈한며
// 버전이 맞지 않을 때 모두 플러시한다
func newPersistentNodeDB(path string, version int, self NodeID) (*nodeDB, error) {
	opts := &opt.Options{OpenFilesCacheCapacity: 5}
	db, err := leveldb.OpenFile(path, opts)
	if _, iscorrupted := err.(*errors.ErrCorrupted); iscorrupted {
		db, err = leveldb.RecoverFile(path, nil)
	}
	if err != nil {
		return nil, err
	}
	// The nodes contained in the cache correspond to a certain protocol version.
	// Flush all nodes if the version doesn't match.
	// 노드들은 특정 프로토콜 버전과 연관된 캐시에 적재된다
	// 버전이 맞지 않을 경우 모든 노드를 플러시한다
	currentVer := make([]byte, binary.MaxVarintLen64)
	currentVer = currentVer[:binary.PutVarint(currentVer, int64(version))]

	blob, err := db.Get(nodeDBVersionKey, nil)
	switch err {
	case leveldb.ErrNotFound:
		// Version not found (i.e. empty cache), insert it
		// 버전이 찾아지지 않음. 삽입
		if err := db.Put(nodeDBVersionKey, currentVer, nil); err != nil {
			db.Close()
			return nil, err
		}

	case nil:
		// Version present, flush if different
		// 버전이 존재. 다를경우 플러시
		if !bytes.Equal(blob, currentVer) {
			db.Close()
			if err = os.RemoveAll(path); err != nil {
				return nil, err
			}
			return newPersistentNodeDB(path, version, self)
		}
	}
	return &nodeDB{
		lvl:  db,
		self: self,
		quit: make(chan struct{}),
	}, nil
}

// makeKey generates the leveldb key-blob from a node id and its particular
// field of interest.
// madeKey함수는 노드 아이디와 특정 관심 필드로부터 level db의 keyblob을 생성한다
func makeKey(id NodeID, field string) []byte {
	if bytes.Equal(id[:], nodeDBNilNodeID[:]) {
		return []byte(field)
	}
	return append(nodeDBItemPrefix, append(id[:], field...)...)
}

// splitKey tries to split a database key into a node id and a field part.
// splitkey 함수는 db key를 node id와필드 파트를 구분하려 시도한다
func splitKey(key []byte) (id NodeID, field string) {
	// If the key is not of a node, return it plainly
	// 키가 노드가 아닐경우 순수하게 리턴한다
	if !bytes.HasPrefix(key, nodeDBItemPrefix) {
		return NodeID{}, string(key)
	}
	// Otherwise split the id and field
	// 아니라면 id와 필드를 분리한다
	item := key[len(nodeDBItemPrefix):]
	copy(id[:], item[:len(id)])
	field = string(item[len(id):])

	return id, field
}

// fetchInt64 retrieves an integer instance associated with a particular
// database key.
// fetchInt64는 특정 db키와 관련된 정수 인스턴스를 반환한다
func (db *nodeDB) fetchInt64(key []byte) int64 {
	blob, err := db.lvl.Get(key, nil)
	if err != nil {
		return 0
	}
	val, read := binary.Varint(blob)
	if read <= 0 {
		return 0
	}
	return val
}

// storeInt64 update a specific database entry to the current time instance as a
// unix timestamp.
// storeInt64는 DB의 특정엔트리를 현재 유닉스 타임스템프 인스턴스로 갱신한다
func (db *nodeDB) storeInt64(key []byte, n int64) error {
	blob := make([]byte, binary.MaxVarintLen64)
	blob = blob[:binary.PutVarint(blob, n)]

	return db.lvl.Put(key, blob, nil)
}

// node retrieves a node with a given id from the database.
// Node함수는 주어진 id의 노드를 db로 부터 반환한다
func (db *nodeDB) node(id NodeID) *Node {
	blob, err := db.lvl.Get(makeKey(id, nodeDBDiscoverRoot), nil)
	if err != nil {
		return nil
	}
	node := new(Node)
	if err := rlp.DecodeBytes(blob, node); err != nil {
		log.Error("Failed to decode node RLP", "err", err)
		return nil
	}
	node.sha = crypto.Keccak256Hash(node.ID[:])
	return node
}

// updateNode inserts - potentially overwriting - a node into the peer database.
// updateNode는 노드를 피어 db에 삽입하거나/덮어쓴다
func (db *nodeDB) updateNode(node *Node) error {
	blob, err := rlp.EncodeToBytes(node)
	if err != nil {
		return err
	}
	return db.lvl.Put(makeKey(node.ID, nodeDBDiscoverRoot), blob, nil)
}

// deleteNode deletes all information/keys associated with a node.
// deleteNode는 노드와 관련된 모든 정보와 키들을 삭제한다
func (db *nodeDB) deleteNode(id NodeID) error {
	deleter := db.lvl.NewIterator(util.BytesPrefix(makeKey(id, "")), nil)
	for deleter.Next() {
		if err := db.lvl.Delete(deleter.Key(), nil); err != nil {
			return err
		}
	}
	return nil
}

// ensureExpirer is a small helper method ensuring that the data expiration
// mechanism is running. If the expiration goroutine is already running, this
// method simply returns.
//
// The goal is to start the data evacuation only after the network successfully
// bootstrapped itself (to prevent dumping potentially useful seed nodes). Since
// it would require significant overhead to exactly trace the first successful
// convergence, it's simpler to "ensure" the correct state when an appropriate
// condition occurs (i.e. a successful bonding), and discard further events.
// ensureExpirerer는 헬퍼 함수로 데이터 만료 메커니즘이 동작중인지를 확인한다
// 만료 고루틴이 이미 실행중이라면 단순하게 리턴한다
// 성공적으로 네트워크에 붙었을때 data의 철수를 시작하기 위해서 사용된다
// (좋은 시드의 노드를 덤핑하는것을 방지하기 위해)
// 첫번째 성공적인 컨버전스를 정확하게 추출하기 위해서는 많은 오버헤드가 필요하기 때문에
// 특정 컨디션이 발생했을때 상태의 올바름을 보장하는것이 심플하며, 더이상의 이벤트들을 버린다
func (db *nodeDB) ensureExpirer() {
	db.runner.Do(func() { go db.expirer() })
}

// expirer should be started in a go routine, and is responsible for looping ad
// infinitum and dropping stale data from the database.
// expirer는 고루틴에서 실행되어야 하며 무한정 루프와 db로 부터 오레된데이터를 드롭할 책임이 있따
func (db *nodeDB) expirer() {
	tick := time.NewTicker(nodeDBCleanupCycle)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if err := db.expireNodes(); err != nil {
				log.Error("Failed to expire nodedb items", "err", err)
			}
		case <-db.quit:
			return
		}
	}
}

// expireNodes iterates over the database and deletes all nodes that have not
// been seen (i.e. received a pong from) for some allotted time.
// expirenodes는 dB를 반복하고 특정 슬랏 시간동안 더이상 보이지 않는 모든 노드를 삭제한다
func (db *nodeDB) expireNodes() error {
	threshold := time.Now().Add(-nodeDBNodeExpiration)

	// Find discovered nodes that are older than the allowance
	// 허용치보다 오래된 발견된 노드를 찾는다
	it := db.lvl.NewIterator(nil, nil)
	defer it.Release()

	for it.Next() {
		// Skip the item if not a discovery node
		// 디스커버리 노드가 아니면 스킵
		id, field := splitKey(it.Key())
		if field != nodeDBDiscoverRoot {
			continue
		}
		// Skip the node if not expired yet (and not self)
		// 아직 시간초과 되지 않았다면 스킵
		if !bytes.Equal(id[:], db.self[:]) {
			if seen := db.bondTime(id); seen.After(threshold) {
				continue
			}
		}
		// Otherwise delete all associated information
		// 아니라면 관련된 모든정보를 삭제한다
		db.deleteNode(id)
	}
	return nil
}

// lastPing retrieves the time of the last ping packet send to a remote node,
// requesting binding.
// lastPing함수는 마지막 핑 패킷이 원격 노드로 전송된 시간을 반환하고 바인딩을 요청한다
func (db *nodeDB) lastPing(id NodeID) time.Time {
	return time.Unix(db.fetchInt64(makeKey(id, nodeDBDiscoverPing)), 0)
}

// updateLastPing updates the last time we tried contacting a remote node.
// updateLastPing함수는 원격노드에 마지막으로 연결을 시도한 시간을 업데이트한다
func (db *nodeDB) updateLastPing(id NodeID, instance time.Time) error {
	return db.storeInt64(makeKey(id, nodeDBDiscoverPing), instance.Unix())
}

// bondTime retrieves the time of the last successful pong from remote node.
// bondTime함수는 마지막 pong이 원격 노드로부터 성공한 시간을 반환한다
func (db *nodeDB) bondTime(id NodeID) time.Time {
	return time.Unix(db.fetchInt64(makeKey(id, nodeDBDiscoverPong)), 0)
}

// hasBond reports whether the given node is considered bonded.
// hasBond 함수는 주어진 노드가 붙어있다고 고려되었는지를 보고한다
func (db *nodeDB) hasBond(id NodeID) bool {
	return time.Since(db.bondTime(id)) < nodeDBNodeExpiration
}

// updateBondTime updates the last pong time of a node.
// updateBondTime함수는 노드의 마지막 pong시간을 갱신한다
func (db *nodeDB) updateBondTime(id NodeID, instance time.Time) error {
	return db.storeInt64(makeKey(id, nodeDBDiscoverPong), instance.Unix())
}

// findFails retrieves the number of findnode failures since bonding.
// findFails함수는 본딩 이후로 부터 findnode실패 횟수를 반환한다
func (db *nodeDB) findFails(id NodeID) int {
	return int(db.fetchInt64(makeKey(id, nodeDBDiscoverFindFails)))
}

// updateFindFails updates the number of findnode failures since bonding.
// updateFindFails 함수는 본딩 이후부터의 find node 실패수를 갱신한다
func (db *nodeDB) updateFindFails(id NodeID, fails int) error {
	return db.storeInt64(makeKey(id, nodeDBDiscoverFindFails), int64(fails))
}

// querySeeds retrieves random nodes to be used as potential seed nodes
// for bootstrapping.
// querySeeds함수는 부트 스트래핑을 위한 시드노드로서 가능성있는 랜덤 노드를 반환한다
func (db *nodeDB) querySeeds(n int, maxAge time.Duration) []*Node {
	var (
		now   = time.Now()
		nodes = make([]*Node, 0, n)
		it    = db.lvl.NewIterator(nil, nil)
		id    NodeID
	)
	defer it.Release()

seek:
	for seeks := 0; len(nodes) < n && seeks < n*5; seeks++ {
		// Seek to a random entry. The first byte is incremented by a
		// random amount each time in order to increase the likelihood
		// of hitting all existing nodes in very small databases.
		// 랜덤 엔트리를 위한 검색
		// 첫바이트는 작은 db에 존재하는 모든 노드를 히트할 확률을 높이기 위해
		// 매시간 랜덤량으로 증가된다
		ctr := id[0]
		rand.Read(id[:])
		id[0] = ctr + id[0]%16
		it.Seek(makeKey(id, nodeDBDiscoverRoot))

		n := nextNode(it)
		if n == nil {
			id[0] = 0
			continue seek // iterator exhausted
			// 반복자 종료
		}
		if n.ID == db.self {
			continue seek
		}
		if now.Sub(db.bondTime(n.ID)) > maxAge {
			continue seek
		}
		for i := range nodes {
			if nodes[i].ID == n.ID {
				continue seek // duplicate
				// 중복
			}
		}
		nodes = append(nodes, n)
	}
	return nodes
}

// reads the next node record from the iterator, skipping over other
// database entries.
// 다음 노드 기록을 반복자로부터 읽고 다른 db 엔트리는 스킵한다
func nextNode(it iterator.Iterator) *Node {
	for end := false; !end; end = !it.Next() {
		id, field := splitKey(it.Key())
		if field != nodeDBDiscoverRoot {
			continue
		}
		var n Node
		if err := rlp.DecodeBytes(it.Value(), &n); err != nil {
			log.Warn("Failed to decode node RLP", "id", id, "err", err)
			continue
		}
		return &n
	}
	return nil

// close flushes and closes the database files.
// close함수는 db file을 flush하고 닫는다
func (db *nodeDB) close() {
	close(db.quit)
	db.lvl.Close()
}
