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

// Package discover implements the Node Discovery Protocol.
//
// The Node Discovery protocol provides a way to find RLPx nodes that
// can be connected to. It uses a Kademlia-like protocol to maintain a
// distributed database of the IDs and endpoints of all listening
// nodes.
// discover패키지는 Node Discovery Protocol을 구현한다
// 노드 디스커버리 프로토콜은 연결이 가능한 RLPX를 찾응 방법을 제공한다
// ID와 수신 대기중인 노드의 엔드포인트들의 분산 DB를 유지하기 위하여
// 카뎀리아와 유사한 프로토콜을 사용한다
package discover

import (
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	mrand "math/rand"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/netutil"
)

const (
	alpha           = 3  // Kademlia concurrency factor
	bucketSize      = 16 // Kademlia bucket size
	maxReplacements = 10 // Size of per-bucket replacement list
	// 카뎀리아 병렬 펙터
	// 카뎀리아 버켓 사이즈
	// 버켓 별 대체 리스트의 크기

	// We keep buckets for the upper 1/15 of distances because
	// it's very unlikely we'll ever encounter a node that's closer.
	// 우리는 우리가 이미 닫힌 노드를 맞닥드릴수 있기 때분에 1/15의 거리보다 가까운쪽에대한 버켓을 저장할것이다
	hashBits          = len(common.Hash{}) * 8
	nBuckets          = hashBits / 15       // Number of buckets
	bucketMinDistance = hashBits - nBuckets // Log distance of closest bucket

	// IP address limits.
	// IP 주소 제한
	bucketIPLimit, bucketSubnet = 2, 24 // at most 2 addresses from the same /24
	tableIPLimit, tableSubnet   = 10, 24

	maxBondingPingPongs = 16 // Limit on the number of concurrent ping/pong interactions
	maxFindnodeFailures = 5  // Nodes exceeding this limit are dropped
	// 동시에 ping/pong 통신할 숫자
	// 한도를 넘어가는 노드는 버린다

	refreshInterval    = 30 * time.Minute
	revalidateInterval = 10 * time.Second
	copyNodesInterval  = 30 * time.Second
	seedMinTableTime   = 5 * time.Minute
	seedCount          = 30
	seedMaxAge         = 5 * 24 * time.Hour
)

type Table struct {
	mutex   sync.Mutex        // protects buckets, bucket content, nursery, rand
	buckets [nBuckets]*bucket // index of known nodes by distance
	nursery []*Node           // bootstrap nodes
	rand    *mrand.Rand       // source of randomness, periodically reseeded
	ips     netutil.DistinctNetSet
	// 버켓과 버켓의 내용 및 다양한 것을 보호함
	// 거리에 의해 알려진 노드의 인덱스
	// 부트스트렙 노드
	// 랜덤 소스. 주기적으로 재 시딩됨

	db         *nodeDB // database of known nodes
	// 알려진 노드의 db
	refreshReq chan chan struct{}
	initDone   chan struct{}
	closeReq   chan struct{}
	closed     chan struct{}

	bondmu    sync.Mutex
	bonding   map[NodeID]*bondproc
	bondslots chan struct{} // limits total number of active bonding processes

	nodeAddedHook func(*Node) // for testing

	net  transport
	self *Node // metadata of the local node
	// 활동중인 결합된 프로세서의 총숫자
	// 로컬 노드의 메타데이터
}

type bondproc struct {
	err  error
	n    *Node
	done chan struct{}
}

// transport is implemented by the UDP transport.
// it is an interface so we can test without opening lots of UDP
// sockets and without generating a private key.
// transport 인터페이스는 udp trasport에의해 구현된다
// 이것은 우리가 수많은 udp소켓과 개인키를 생성하지 않고 테스트 할수 있는 인터페이스이다
type transport interface {
	ping(NodeID, *net.UDPAddr) error
	waitping(NodeID) error
	findnode(toid NodeID, addr *net.UDPAddr, target NodeID) ([]*Node, error)
	close()
}

// bucket contains nodes, ordered by their last activity. the entry
// that was most recently active is the first element in entries.
// bucket구조체는 마지막 활동에 의해 정렬된 노드를 포함한다. 목록은 가장 최근에 활동한 것이 첫번째순이다
type bucket struct {
	entries      []*Node // live entries, sorted by time of last contact
	replacements []*Node // recently seen nodes to be used if revalidation fails
	// 생존 목록(마지막 활동순으로 정렬된)
	// 검증이 실패할 경우 사용할 최근 노드
	ips          netutil.DistinctNetSet
}

func newTable(t transport, ourID NodeID, ourAddr *net.UDPAddr, nodeDBPath string, bootnodes []*Node) (*Table, error) {
	// If no node database was given, use an in-memory one
	// db가 주어지지 않으면 메모리 상에 사용
	db, err := newNodeDB(nodeDBPath, Version, ourID)
	if err != nil {
		return nil, err
	}
	tab := &Table{
		net:        t,
		db:         db,
		self:       NewNode(ourID, ourAddr.IP, uint16(ourAddr.Port), uint16(ourAddr.Port)),
		bonding:    make(map[NodeID]*bondproc),
		bondslots:  make(chan struct{}, maxBondingPingPongs),
		refreshReq: make(chan chan struct{}),
		initDone:   make(chan struct{}),
		closeReq:   make(chan struct{}),
		closed:     make(chan struct{}),
		rand:       mrand.New(mrand.NewSource(0)),
		ips:        netutil.DistinctNetSet{Subnet: tableSubnet, Limit: tableIPLimit},
	}
	if err := tab.setFallbackNodes(bootnodes); err != nil {
		return nil, err
	}
	for i := 0; i < cap(tab.bondslots); i++ {
		tab.bondslots <- struct{}{}
	}
	for i := range tab.buckets {
		tab.buckets[i] = &bucket{
			ips: netutil.DistinctNetSet{Subnet: bucketSubnet, Limit: bucketIPLimit},
		}
	}
	tab.seedRand()
	tab.loadSeedNodes(false)
	// Start the background expiration goroutine after loading seeds so that the search for
	// seed nodes also considers older nodes that would otherwise be removed by the
	// expiration.
	// 시드를 읽은 후 백그라운드에서 만료루틴을 시작한다.
	// 시드 노드의 검색은 오래되어 만료된 노드도 고려 한다
	tab.db.ensureExpirer()
	go tab.loop()
	return tab, nil
}

func (tab *Table) seedRand() {
	var b [8]byte
	crand.Read(b[:])

	tab.mutex.Lock()
	tab.rand.Seed(int64(binary.BigEndian.Uint64(b[:])))
	tab.mutex.Unlock()
}

// Self returns the local node.
// The returned node should not be modified by the caller.
// Self함수는 로컬노드를 반환한다
// 반환된 노드는 수정되어서는 안된다
func (tab *Table) Self() *Node {
	return tab.self
}

// ReadRandomNodes fills the given slice with random nodes from the
// table. It will not write the same node more than once. The nodes in
// the slice are copies and can be modified by the caller.
// REadRandomNodes함수는 주어진 슬라이스를 테이블의 랜덤노드로 채운다
// 동일한 노드는 한번이상 쓰지 않난다
// 슬라이스 상의 노드들은 복사되고 수정되어질수 있다
func (tab *Table) ReadRandomNodes(buf []*Node) (n int) {
	if !tab.isInitDone() {
		return 0
	}
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	// Find all non-empty buckets and get a fresh slice of their entries.
	// 비어있지 않은 모든 버켓을 찾고 신선한 조각들만 가져온다
	var buckets [][]*Node
	for _, b := range tab.buckets {
		if len(b.entries) > 0 {
			buckets = append(buckets, b.entries[:])
		}
	}
	if len(buckets) == 0 {
		return 0
	}
	// Shuffle the buckets.
	// 버켓을 셔플한다
	for i := len(buckets) - 1; i > 0; i-- {
		j := tab.rand.Intn(len(buckets))
		buckets[i], buckets[j] = buckets[j], buckets[i]
	}
	// Move head of each bucket into buf, removing buckets that become empty.
	// 각버켓의 헤드를 buf로 옮기고 비어진 버켓은 제거한다
	var i, j int
	for ; i < len(buf); i, j = i+1, (j+1)%len(buckets) {
		b := buckets[j]
		buf[i] = &(*b[0])
		buckets[j] = b[1:]
		if len(b) == 1 {
			buckets = append(buckets[:j], buckets[j+1:]...)
		}
		if len(buckets) == 0 {
			break
		}
	}
	return i + 1
}

// Close terminates the network listener and flushes the node database.
// Close함수는 네트워크 대기를 종료하고 노드 db를 플러쉬한다
func (tab *Table) Close() {
	select {
	case <-tab.closed:
		// already closed.
		// 이미 닫힘
	case tab.closeReq <- struct{}{}:
		<-tab.closed // wait for refreshLoop to end.
		// 종료를 위한 새로고침 루프를 대기
	}
}

// setFallbackNodes sets the initial points of contact. These nodes
// are used to connect to the network if the table is empty and there
// are no known nodes in the database.
// setFallbackNodes함수는 연결의 초기 포인트를 설정한다
// 이 노드들은 테이블이 비어있고 알려진 노드가 dB에 없을때 
// 네트워크에 연결하기 위해 사용된다
func (tab *Table) setFallbackNodes(nodes []*Node) error {
	for _, n := range nodes {
		if err := n.validateComplete(); err != nil {
			return fmt.Errorf("bad bootstrap/fallback node %q (%v)", n, err)
		}
	}
	tab.nursery = make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		cpy := *n
		// Recompute cpy.sha because the node might not have been
		// created by NewNode or ParseNode.
		// newnode나 parse노드에 의해 만들어지지 않은 노드 일수 있으므로
		// cpy.sha를 재계산한다
		cpy.sha = crypto.Keccak256Hash(n.ID[:])
		tab.nursery = append(tab.nursery, &cpy)
	}
	return nil
}

// isInitDone returns whether the table's initial seeding procedure has completed.
// isInitDone함수는 테이블의 초기 시딩이 완료되었는지 여부를 반환한다
func (tab *Table) isInitDone() bool {
	select {
	case <-tab.initDone:
		return true
	default:
		return false
	}
}

// Resolve searches for a specific node with the given ID.
// It returns nil if the node could not be found.
// Resove함수는 주어진 아이도로 노드를 찾는다
// 만약 노드가 찾아지지 않는다면 nil을 리턴한다
func (tab *Table) Resolve(targetID NodeID) *Node {
	// If the node is present in the local table, no
	// network interaction is required.
	// 만약 노드가 로컬 테이블에 존재한다면 네트워크 동작이 필요없음
	hash := crypto.Keccak256Hash(targetID[:])
	tab.mutex.Lock()
	cl := tab.closest(hash, 1)
	tab.mutex.Unlock()
	if len(cl.entries) > 0 && cl.entries[0].ID == targetID {
		return cl.entries[0]
	}
	// Otherwise, do a network lookup.
	// 아니라면 네트워크 검색
	result := tab.Lookup(targetID)
	for _, n := range result {
		if n.ID == targetID {
			return n
		}
	}
	return nil
}

// Lookup performs a network search for nodes close
// to the given target. It approaches the target by querying
// nodes that are closer to it on each iteration.
// The given target does not need to be an actual node
// identifier.
// Lookup함수는 주어진 타겟에 가까운 노드를 네트워크 검색한다
// 각 반복에서 가장 가까운 노드에 ㅈㅓㅂ근한다
// 주어진 타겟노드는 실제 노드 ID가 필요하지 않다
func (tab *Table) Lookup(targetID NodeID) []*Node {
	return tab.lookup(targetID, true)
}

func (tab *Table) lookup(targetID NodeID, refreshIfEmpty bool) []*Node {
	var (
		target         = crypto.Keccak256Hash(targetID[:])
		asked          = make(map[NodeID]bool)
		seen           = make(map[NodeID]bool)
		reply          = make(chan []*Node, alpha)
		pendingQueries = 0
		result         *nodesByDistance
	)
	// don't query further if we hit ourself.
	// unlikely to happen often in practice.
	// 스스로일경우 더이상 쿼리하지 말것
	// 잘 일어나지 않지만 연습에서 자주 발생
	asked[tab.self.ID] = true

	for {
		tab.mutex.Lock()
		// generate initial result set
		// 최초 결과 셋을 생성
		result = tab.closest(target, bucketSize)
		tab.mutex.Unlock()
		if len(result.entries) > 0 || !refreshIfEmpty {
			break
		}
		// The result set is empty, all nodes were dropped, refresh.
		// We actually wait for the refresh to complete here. The very
		// first query will hit this case and run the bootstrapping
		// logic.
		// 결과가 비어으므로 모든 노드가 드롭된다. 새로고침
		// 새로고침이 완료될때까지 여기서 기다린다.
		// 최초 쿼리는 여기서 히트되며 부트스트랩 로직이 실행될것이다
		<-tab.refresh()
		refreshIfEmpty = false
	}

	for {
		// ask the alpha closest nodes that we haven't asked yet
		// 아직 질의하지 않은 가장 가까운 알파노드를 요청
		for i := 0; i < len(result.entries) && pendingQueries < alpha; i++ {
			n := result.entries[i]
			if !asked[n.ID] {
				asked[n.ID] = true
				pendingQueries++
				go func() {
					// Find potential neighbors to bond with
					// 연동할 가능성있는 이웃들을 찾음
					r, err := tab.net.findnode(n.ID, n.addr(), targetID)
					if err != nil {
						// Bump the failure counter to detect and evacuate non-bonded entries
						// 감지하고 연동안된 엔트리로 보내기 위해 실패 카운터를 증가시킨다
						fails := tab.db.findFails(n.ID) + 1
						tab.db.updateFindFails(n.ID, fails)
						log.Trace("Bumping findnode failure counter", "id", n.ID, "failcount", fails)

						if fails >= maxFindnodeFailures {
							log.Trace("Too many findnode failures, dropping", "id", n.ID, "failcount", fails)
							tab.delete(n)
						}
					}
					reply <- tab.bondall(r)
				}()
			}
		}
		if pendingQueries == 0 {
			// we have asked all closest nodes, stop the search
			// 모든 근접노드를 찾았으므로 검색을 중단
			break
		}
		// wait for the next reply
		// 다음 응답까지 대기
		for _, n := range <-reply {
			if n != nil && !seen[n.ID] {
				seen[n.ID] = true
				result.push(n, bucketSize)
			}
		}
		pendingQueries--
	}
	return result.entries
}

func (tab *Table) refresh() <-chan struct{} {
	done := make(chan struct{})
	select {
	case tab.refreshReq <- done:
	case <-tab.closed:
		close(done)
	}
	return done
}

// loop schedules refresh, revalidate runs and coordinates shutdown.
// loop 함수는 새로고침을 예약하고 재검증을 실행하고 종료를 지시한다
func (tab *Table) loop() {
	var (
		revalidate     = time.NewTimer(tab.nextRevalidateTime())
		refresh        = time.NewTicker(refreshInterval)
		copyNodes      = time.NewTicker(copyNodesInterval)
		revalidateDone = make(chan struct{})
		refreshDone    = make(chan struct{})           // where doRefresh reports completion
		waiting        = []chan struct{}{tab.initDone} // holds waiting callers while doRefresh runs
		// doRefresh가 종료를 보고할곳
		// doRefersh동작시 대기중인 호출자를 잡고있음
	)
	defer refresh.Stop()
	defer revalidate.Stop()
	defer copyNodes.Stop()

	// Start initial refresh.
	// 초기 새로고침을 시작
	go tab.doRefresh(refreshDone)

loop:
	for {
		select {
		case <-refresh.C:
			tab.seedRand()
			if refreshDone == nil {
				refreshDone = make(chan struct{})
				go tab.doRefresh(refreshDone)
			}
		case req := <-tab.refreshReq:
			waiting = append(waiting, req)
			if refreshDone == nil {
				refreshDone = make(chan struct{})
				go tab.doRefresh(refreshDone)
			}
		case <-refreshDone:
			for _, ch := range waiting {
				close(ch)
			}
			waiting, refreshDone = nil, nil
		case <-revalidate.C:
			go tab.doRevalidate(revalidateDone)
		case <-revalidateDone:
			revalidate.Reset(tab.nextRevalidateTime())
		case <-copyNodes.C:
			go tab.copyBondedNodes()
		case <-tab.closeReq:
			break loop
		}
	}

	if tab.net != nil {
		tab.net.close()
	}
	if refreshDone != nil {
		<-refreshDone
	}
	for _, ch := range waiting {
		close(ch)
	}
	tab.db.close()
	close(tab.closed)
}

// doRefresh performs a lookup for a random target to keep buckets
// full. seed nodes are inserted if the table is empty (initial
// bootstrap or discarded faulty peers).
// doRefresh함수는 버켓을 채운상태로 유지하기 위한 랜덤 타켓을 위한 검색을 수행한다
// 씨드노드는 테이블이 비어있을때 삽입된다(초기 부트스트랩 이거나 제거되었던 피어들)
func (tab *Table) doRefresh(done chan struct{}) {
	defer close(done)

	// Load nodes from the database and insert
	// them. This should yield a few previously seen nodes that are
	// (hopefully) still alive.
	// db로부터 노드를 읽고 인서트 한다. 여전히 살아있길 희망하는
	// 몇몇 과거 노드를 포함한다
	tab.loadSeedNodes(true)

	// Run self lookup to discover new neighbor nodes.
	// 세로운 이웃 노드를 발견하기 위해 스스로 검색을 시작한다
	tab.lookup(tab.self.ID, false)

	// The Kademlia paper specifies that the bucket refresh should
	// perform a lookup in the least recently used bucket. We cannot
	// adhere to this because the findnode target is a 512bit value
	// (not hash-sized) and it is not easily possible to generate a
	// sha3 preimage that falls into a chosen bucket.
	// We perform a few lookups with a random target instead.
	// 카뎀리아 논문은 버켓의 갱신이 최근 사용된 버켓에서 실행되어야 한다고 정의한다
	// 우리는 찾아진 노드가 512bit 값이고 선택된 버켓에 들어갈 sha3 preimage를 생성하는것이 쉽지 않기 때문에
	// 여기에 해당로직을 추가하지 않는다
	// 우리는 대신 랜덤타겟에 대한 몇번의 검색을 수행한다
	for i := 0; i < 3; i++ {
		var target NodeID
		crand.Read(target[:])
		tab.lookup(target, false)
	}
}

func (tab *Table) loadSeedNodes(bond bool) {
	seeds := tab.db.querySeeds(seedCount, seedMaxAge)
	seeds = append(seeds, tab.nursery...)
	if bond {
		seeds = tab.bondall(seeds)
	}
	for i := range seeds {
		seed := seeds[i]
		age := log.Lazy{Fn: func() interface{} { return time.Since(tab.db.bondTime(seed.ID)) }}
		log.Debug("Found seed node in database", "id", seed.ID, "addr", seed.addr(), "age", age)
		tab.add(seed)
	}
}

// doRevalidate checks that the last node in a random bucket is still live
// and replaces or deletes the node if it isn't.
// doRevalidate함수는 랜덤 버켓의 마지막 노드가 여전히 살아있는지 확인하고
// 아니라면 대체하거나 삭제한다
func (tab *Table) doRevalidate(done chan<- struct{}) {
	defer func() { done <- struct{}{} }()

	last, bi := tab.nodeToRevalidate()
	if last == nil {
		// No non-empty bucket found.
		// 비어있지 않은 버켓이 찾아짐
		return
	}

	// Ping the selected node and wait for a pong.
	// 선택된 노드로 ping을하고 pong을 기다림
	err := tab.ping(last.ID, last.addr())

	tab.mutex.Lock()
	defer tab.mutex.Unlock()
	b := tab.buckets[bi]
	if err == nil {
		// The node responded, move it to the front.
		// 노드가 응답했음, 앞쪽으로 옮긴다
		log.Debug("Revalidated node", "b", bi, "id", last.ID)
		b.bump(last)
		return
	}
	// No reply received, pick a replacement or delete the node if there aren't
	// any replacements.
	// 응답이 없음 재위치 시키거나 노드를 삭제 시킨다
	if r := tab.replace(b, last); r != nil {
		log.Debug("Replaced dead node", "b", bi, "id", last.ID, "ip", last.IP, "r", r.ID, "rip", r.IP)
	} else {
		log.Debug("Removed dead node", "b", bi, "id", last.ID, "ip", last.IP)
	}
}

// nodeToRevalidate returns the last node in a random, non-empty bucket.
// nodeToRevalidate 함수는 랜덤하고 비어있지 않은 버켓의 마지막 노드를 반환한다
func (tab *Table) nodeToRevalidate() (n *Node, bi int) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	for _, bi = range tab.rand.Perm(len(tab.buckets)) {
		b := tab.buckets[bi]
		if len(b.entries) > 0 {
			last := b.entries[len(b.entries)-1]
			return last, bi
		}
	}
	return nil, 0
}

func (tab *Table) nextRevalidateTime() time.Duration {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	return time.Duration(tab.rand.Int63n(int64(revalidateInterval)))
}

// copyBondedNodes adds nodes from the table to the database if they have been in the table
// longer then minTableTime.
// copyBondedNodes함수는 mintableTime이상 테이블에 존재했을 경우
// table의 노드를 db로 추가한다
func (tab *Table) copyBondedNodes() {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	now := time.Now()
	for _, b := range tab.buckets {
		for _, n := range b.entries {
			if now.Sub(n.addedAt) >= seedMinTableTime {
				tab.db.updateNode(n)
			}
		}
	}
}

//closest함수는 주어진 노드로부터 가장 가까운 n 노드를 테이블에서 반환한다
// closest returns the n nodes in the table that are closest to the
// given id. The caller must hold tab.mutex.
func (tab *Table) closest(target common.Hash, nresults int) *nodesByDistance {
	// This is a very wasteful way to find the closest nodes but
	// obviously correct. I believe that tree-based buckets would make
	// this easier to implement efficiently.
	// 가까운 노드를 찾기에 낭비스러운 방법이지만 명백하게 맞다
	// 트리베이스의 구현이 좀더 쉽고 편해질것이라고 생각한다
	close := &nodesByDistance{target: target}
	for _, b := range tab.buckets {
		for _, n := range b.entries {
			close.push(n, nresults)
		}
	}
	return close
}

func (tab *Table) len() (n int) {
	for _, b := range tab.buckets {
		n += len(b.entries)
	}
	return n
}

// bondall bonds with all given nodes concurrently and returns
// those nodes for which bonding has probably succeeded.
// bondall 함수는 주어진 노드를동시에 연결하며 
// 성공적으로 붙은 노드를 반환한다
func (tab *Table) bondall(nodes []*Node) (result []*Node) {
	rc := make(chan *Node, len(nodes))
	for i := range nodes {
		go func(n *Node) {
			nn, _ := tab.bond(false, n.ID, n.addr(), n.TCP)
			rc <- nn
		}(nodes[i])
	}
	for range nodes {
		if n := <-rc; n != nil {
			result = append(result, n)
		}
	}
	return result
}

// bond ensures the local node has a bond with the given remote node.
// It also attempts to insert the node into the table if bonding succeeds.
// The caller must not hold tab.mutex.
//
// A bond is must be established before sending findnode requests.
// Both sides must have completed a ping/pong exchange for a bond to
// exist. The total number of active bonding processes is limited in
// order to restrain network use.
//
// bond is meant to operate idempotently in that bonding with a remote
// node which still remembers a previously established bond will work.
// The remote node will simply not send a ping back, causing waitping
// to time out.
//
// If pinged is true, the remote node has just pinged us and one half
// of the process can be skipped.
// bond함수는 로컬노드가 원격노드와 붙었는지를 보장한다
// 또한 성공적으로 연결되었을때 노드를 테이블로 넣으려고 시도한다
// 호출자는 뮤택스를 잡아서는 안된다
// 하나의 연결은 findnode 요청전에 완성되어야 한다.
// 본드를 존재시키기 위해 양측은 핑퐁을의 교환을 끝내야 한다
// 활성화된 연결 프로세스의 총 수는 네트워크를 유지하기 위해 제한된다
// 본드는 이미 동작한적이 있는것으로 기억된 연결안에서 항상적으로 동작할것이다
// 원격노드는 ping을 돌려보내지 않음으로서 waitping을 타임아웃 시킨다
// 만약 핑이 되었다면 원격 노드는 다시 핑을 주고 나머지 반의 프로세스를 스킵한다
func (tab *Table) bond(pinged bool, id NodeID, addr *net.UDPAddr, tcpPort uint16) (*Node, error) {
	if id == tab.self.ID {
		return nil, errors.New("is self")
	}
	if pinged && !tab.isInitDone() {
		return nil, errors.New("still initializing")
	}
	// Start bonding if we haven't seen this node for a while or if it failed findnode too often.
	// 한동안 본적이 없거나 자주 find node에 실패할 경우 본딩을 시작한다
	node, fails := tab.db.node(id), tab.db.findFails(id)
	age := time.Since(tab.db.bondTime(id))
	var result error
	if fails > 0 || age > nodeDBNodeExpiration {
		log.Trace("Starting bonding ping/pong", "id", id, "known", node != nil, "failcount", fails, "age", age)

		tab.bondmu.Lock()
		w := tab.bonding[id]
		if w != nil {
			// Wait for an existing bonding process to complete.
			// 존재하는 연결 프로세스의 완료를 위한 대기
			tab.bondmu.Unlock()
			<-w.done
		} else {
			// Register a new bonding process.
			// 새로운 연결 프로세스를 등록
			w = &bondproc{done: make(chan struct{})}
			tab.bonding[id] = w
			tab.bondmu.Unlock()
			// Do the ping/pong. The result goes into w.
			// 핑퐁을 하고 결과는 w에 저장된다
			tab.pingpong(w, pinged, id, addr, tcpPort)
			// Unregister the process after it's done.
			// 끝낫을 경우 프로세스를 등록해지 한다
			tab.bondmu.Lock()
			delete(tab.bonding, id)
			tab.bondmu.Unlock()
		}
		// Retrieve the bonding results
		// 연결 결과를 반환한다
		result = w.err
		if result == nil {
			node = w.n
		}
	}
	// Add the node to the table even if the bonding ping/pong
	// fails. It will be relaced quickly if it continues to be
	// unresponsive.
	// 연결 핑퐁의 실패여부와 상관없이 노드를 테이블에 추가한다
	// 계속 반응이 없을경우 빠르게 대체될것이다
	if node != nil {
		tab.add(node)
		tab.db.updateFindFails(id, 0)
	}
	return node, result
}

func (tab *Table) pingpong(w *bondproc, pinged bool, id NodeID, addr *net.UDPAddr, tcpPort uint16) {
	// Request a bonding slot to limit network usage
	// 네트워크 사용을 제한하기 위해 연결 슬롯을 요청한다
	<-tab.bondslots
	defer func() { tab.bondslots <- struct{}{} }()

	// Ping the remote side and wait for a pong.
	// 원격쪽에 ping을 보내고 pong을 대기
	if w.err = tab.ping(id, addr); w.err != nil {
		close(w.done)
		return
	}
	if !pinged {
		// Give the remote node a chance to ping us before we start
		// sending findnode requests. If they still remember us,
		// waitping will simply time out.
		// findnode request를 하기 전에 원격 노드가 우리쪽으로 핑할 기회를 준다
		// 그들이 우리를 여전히 기억한다면 핑 대기는 타임아웃 될것이다
		tab.net.waitping(id)
	}
	// Bonding succeeded, update the node database.
	// 연결이 성공하였으므로 노드 디비를 업데이트 한다
	w.n = NewNode(id, addr.IP, uint16(addr.Port), tcpPort)
	close(w.done)
}

// ping a remote endpoint and wait for a reply, also updating the node
// database accordingly.
// 원격 엔드포인트에 핑을 하고 응답을 대기하며, 노드 디비를 주어진대로 업데이트한다
func (tab *Table) ping(id NodeID, addr *net.UDPAddr) error {
	tab.db.updateLastPing(id, time.Now())
	if err := tab.net.ping(id, addr); err != nil {
		return err
	}
	tab.db.updateBondTime(id, time.Now())
	return nil
}

// bucket returns the bucket for the given node ID hash.
func (tab *Table) bucket(sha common.Hash) *bucket {
	d := logdist(tab.self.sha, sha)
	if d <= bucketMinDistance {
		return tab.buckets[0]
	}
	return tab.buckets[d-bucketMinDistance-1]
}

// add attempts to add the given node its corresponding bucket. If the
// bucket has space available, adding the node succeeds immediately.
// Otherwise, the node is added if the least recently active node in
// the bucket does not respond to a ping packet.
//
// The caller must not hold tab.mutex.
// bucket함수는 주어진 노드 id해시를 위한 버켓을 반환한다
// add함수는 주어진 노드를 연관된 베켓에 추가하려고 한다
// 만약 버켓에 공간이 있다면 노드 추가는 즉시성공한다
// 아니라면 노드는 버켓상의 최근 활동한 액티브 노드가 핑 패킷에 반응하지 않을 경우 추가된다
func (tab *Table) add(new *Node) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	b := tab.bucket(new.sha)
	if !tab.bumpOrAdd(b, new) {
		// Node is not in table. Add it to the replacement list.
		// 노드가 테이블에 없음. 대체 리스트에 추가
		tab.addReplacement(b, new)
	}
}

// stuff adds nodes the table to the end of their corresponding bucket
// if the bucket is not full. The caller must not hold tab.mutex.
//stuff함수는 만약 버켓이 가득차지 않았을 경우 노드를 관련 버켓의 테이블의 끝에 추가한다
func (tab *Table) stuff(nodes []*Node) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	for _, n := range nodes {
		if n.ID == tab.self.ID {
			continue // don't add self
		}
		b := tab.bucket(n.sha)
		if len(b.entries) < bucketSize {
			tab.bumpOrAdd(b, n)
		}
	}
}

// delete removes an entry from the node table (used to evacuate
// failed/non-bonded discovery peers).
// delete함수는 엔트리를 노드 테이블로부터 제거한다
// 실패하거나 본딩 되지 않은 발견된 피어들)
func (tab *Table) delete(node *Node) {
	tab.mutex.Lock()
	defer tab.mutex.Unlock()

	tab.deleteInBucket(tab.bucket(node.sha), node)
}

func (tab *Table) addIP(b *bucket, ip net.IP) bool {
	if netutil.IsLAN(ip) {
		return true
	}
	if !tab.ips.Add(ip) {
		log.Debug("IP exceeds table limit", "ip", ip)
		return false
	}
	if !b.ips.Add(ip) {
		log.Debug("IP exceeds bucket limit", "ip", ip)
		tab.ips.Remove(ip)
		return false
	}
	return true
}

func (tab *Table) removeIP(b *bucket, ip net.IP) {
	if netutil.IsLAN(ip) {
		return
	}
	tab.ips.Remove(ip)
	b.ips.Remove(ip)
}

func (tab *Table) addReplacement(b *bucket, n *Node) {
	for _, e := range b.replacements {
		if e.ID == n.ID {
			return // already in list
		}
	}
	if !tab.addIP(b, n.IP) {
		return
	}
	var removed *Node
	b.replacements, removed = pushNode(b.replacements, n, maxReplacements)
	if removed != nil {
		tab.removeIP(b, removed.IP)
	}
}

// replace removes n from the replacement list and replaces 'last' with it if it is the
// last entry in the bucket. If 'last' isn't the last entry, it has either been replaced
// with someone else or became active.
// replace함수는 n을 대체리스트로 부터 제거하고 버켓의 마지막 엔트리일 경우 마지막을 대체한다
// 만약 마지막이 마지막 엔트리가 아니라면 다른 것으로 대체되거나 활성화 된다
func (tab *Table) replace(b *bucket, last *Node) *Node {
	if len(b.entries) == 0 || b.entries[len(b.entries)-1].ID != last.ID {
		// Entry has moved, don't replace it.
		// 엔트리가 옮겨짐. 대체하지 말것
		return nil
	}
	// Still the last entry.
	// 여전히 마지막 엔트리
	if len(b.replacements) == 0 {
		tab.deleteInBucket(b, last)
		return nil
	}
	r := b.replacements[tab.rand.Intn(len(b.replacements))]
	b.replacements = deleteNode(b.replacements, r)
	b.entries[len(b.entries)-1] = r
	tab.removeIP(b, last.IP)
	return r
}

// bump moves the given node to the front of the bucket entry list
// if it is contained in that list.
// bump함수는 만약 리스트에 포함될 경우 주어진 노드를 버켓의 맨앞으로 옮긴다
func (b *bucket) bump(n *Node) bool {
	for i := range b.entries {
		if b.entries[i].ID == n.ID {
			// move it to the front>
			// 맨앞으로 옮긴다
			copy(b.entries[1:], b.entries[:i])
			b.entries[0] = n
			return true
		}
	}
	return false
}

// bumpOrAdd moves n to the front of the bucket entry list or adds it if the list isn't
// full. The return value is true if n is in the bucket.
// bumpOrAdd함수는 n을 버켓 엔트리 리스트의 맨앞으로 옮기거나, 리스트가 가득차지 않았을 경우 추가한다
// 반환값은 n이 버켓에 존재할 경우 참
func (tab *Table) bumpOrAdd(b *bucket, n *Node) bool {
	if b.bump(n) {
		return true
	}
	if len(b.entries) >= bucketSize || !tab.addIP(b, n.IP) {
		return false
	}
	b.entries, _ = pushNode(b.entries, n, bucketSize)
	b.replacements = deleteNode(b.replacements, n)
	n.addedAt = time.Now()
	if tab.nodeAddedHook != nil {
		tab.nodeAddedHook(n)
	}
	return true
}

func (tab *Table) deleteInBucket(b *bucket, n *Node) {
	b.entries = deleteNode(b.entries, n)
	tab.removeIP(b, n.IP)
}

// pushNode adds n to the front of list, keeping at most max items.
// pushNode함수는 n을 리스트의 맨앞에 추가한다. 최대한의 아이템을 가지고 있는다
func pushNode(list []*Node, n *Node, max int) ([]*Node, *Node) {
	if len(list) < max {
		list = append(list, nil)
	}
	removed := list[len(list)-1]
	copy(list[1:], list)
	list[0] = n
	return list, removed
}

// deleteNode removes n from list.
// deleteNode함수는 n을 리스트로부터 제거한다
func deleteNode(list []*Node, n *Node) []*Node {
	for i := range list {
		if list[i].ID == n.ID {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// nodesByDistance is a list of nodes, ordered by
// distance to target.
// nodeByDistance 구조체는 노드의 리스트이며 거리로 정렬된다
type nodesByDistance struct {
	entries []*Node
	target  common.Hash
}


// push adds the given node to the list, keeping the total size below maxElems.
// push함수는 주어진 노드를 리스트에 추가하고 최대 갯수아래의 전체 사이즈를 저장한다
func (h *nodesByDistance) push(n *Node, maxElems int) {
	ix := sort.Search(len(h.entries), func(i int) bool {
		return distcmp(h.target, h.entries[i].sha, n.sha) > 0
	})
	if len(h.entries) < maxElems {
		h.entries = append(h.entries, n)
	}
	if ix == len(h.entries) {
		// farther away than all nodes we already have.
		// if there was room for it, the node is now the last element.
		// 우리가 가진 노드들보다 멀다
		// 공간이 있다면 last 원소로 한다
	} else {
		// slide existing entries down to make room
		// this will overwrite the entry we just appended.
		// 공간을 만들기 위해 전체 엔트리를 슬라이드 한다
		// 방금 추가한 엔트리를 덮어쓸것이다
		copy(h.entries[ix+1:], h.entries[ix:])
		h.entries[ix] = n
	}
}
