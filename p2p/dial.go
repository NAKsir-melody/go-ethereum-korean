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

package p2p

import (
	"container/heap"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/netutil"
)

const (
	// This is the amount of time spent waiting in between
	// redialing a certain node.
	// 특정노드로 재다이얼하는 대기시간
	dialHistoryExpiration = 30 * time.Second

	// Discovery lookups are throttled and can only run
	// once every few seconds.
	// 탐색은 조절되며 매 수초간 한번씩만 실행가능하다 
	lookupInterval = 4 * time.Second

	// If no peers are found for this amount of time, the initial bootnodes are
	// attempted to be connected.
	// 이 시간동안 피어가 찾아지지 않을경우, 부트노드로 연결을 시도한다
	fallbackInterval = 20 * time.Second

	// Endpoint resolution is throttled with bounded backoff.
	// 엔드포인트간 동작은 제한된 무반응으로 조절된다
	initialResolveDelay = 60 * time.Second
	maxResolveDelay     = time.Hour
)

// NodeDialer is used to connect to nodes in the network, typically by using
// an underlying net.Dialer but also using net.Pipe in tests
// NodeDialer는 네트워크의 노드들간 연결에 사용되며, 전형적으로
// net.Dialer에 이용되지만 테스트에서는 net.Pipe를 사용한다
type NodeDialer interface {
	Dial(*discover.Node) (net.Conn, error)
}

// TCPDialer implements the NodeDialer interface by using a net.Dialer to
// create TCP connections to nodes in the network
// TCPDialer는 노드간의 TCP 연결을 생성하기 위해 net.Dialer를 사용하여 NodeDialer를 구현함
type TCPDialer struct {
	*net.Dialer
}

// Dial creates a TCP connection to the node
// Dial 함수는 노드로의 TCP 연결을 생성한다
func (t TCPDialer) Dial(dest *discover.Node) (net.Conn, error) {
	addr := &net.TCPAddr{IP: dest.IP, Port: int(dest.TCP)}
	return t.Dialer.Dial("tcp", addr.String())
}

// dialstate schedules dials and discovery lookups.
// it get's a chance to compute new tasks on every iteration
// of the main loop in Server.run.
// dialstate 구조체는 다이얼과 노드 탐색을 스케쥴한다
// Server.run의 메인루프의 반복을 통해 새로운 업무를 계산할 기회를 갖는다.
type dialstate struct {
	maxDynDials int
	ntab        discoverTable
	netrestrict *netutil.Netlist

	lookupRunning bool
	dialing       map[discover.NodeID]connFlag
	lookupBuf     []*discover.Node // current discovery lookup results
	// 현재 탐색된 결과 
	randomNodes   []*discover.Node // filled from Table
	// 테이블로부터 채워짐
	static        map[discover.NodeID]*dialTask
	hist          *dialHistory

	start     time.Time        // time when the dialer was first used
	// dialer가 처음 사용된 시간
	bootnodes []*discover.Node // default dials when there are no peers
	// 피어가 없을때의 기본 연결요청
}

type discoverTable interface {
	Self() *discover.Node
	Close()
	Resolve(target discover.NodeID) *discover.Node
	Lookup(target discover.NodeID) []*discover.Node
	ReadRandomNodes([]*discover.Node) int
}

// the dial history remembers recent dials.
// dial history는 최근 연결요청을 기억함
type dialHistory []pastDial

// pastDial is an entry in the dial history.
// pastDial은 연결 히스토리의 진입
type pastDial struct {
	id  discover.NodeID
	exp time.Time
}

type task interface {
	Do(*Server)
}

// A dialTask is generated for each node that is dialed. Its
// fields cannot be accessed while the task is running.
// dialTask는 접속요청된 각 노드에 대해 생성된다
// 필드는 task가 실행중일때는 접근되지 않는다
type dialTask struct {
	flags        connFlag
	dest         *discover.Node
	lastResolved time.Time
	resolveDelay time.Duration
}

// discoverTask runs discovery table operations.
// Only one discoverTask is active at any time.
// discoverTask.Do performs a random lookup.
// discoverTask는 발견 테이블 동작을 실행시킨다
// 오직 하나의 탐색업무만이 특정시간에 활성화 된다
// discoverTask는 랜덤 탐색을 수행한다
type discoverTask struct {
	results []*discover.Node
}

// A waitExpireTask is generated if there are no other tasks
// to keep the loop in Server.run ticking.
// waitExpireTask는sever.run의 루프안에 아무것도 없을때 생성된다
type waitExpireTask struct {
	time.Duration
}

func newDialState(static []*discover.Node, bootnodes []*discover.Node, ntab discoverTable, maxdyn int, netrestrict *netutil.Netlist) *dialstate {
	s := &dialstate{
		maxDynDials: maxdyn,
		ntab:        ntab,
		netrestrict: netrestrict,
		static:      make(map[discover.NodeID]*dialTask),
		dialing:     make(map[discover.NodeID]connFlag),
		bootnodes:   make([]*discover.Node, len(bootnodes)),
		randomNodes: make([]*discover.Node, maxdyn/2),
		hist:        new(dialHistory),
	}
	copy(s.bootnodes, bootnodes)
	for _, n := range static {
		s.addStatic(n)
	}
	return s
}

func (s *dialstate) addStatic(n *discover.Node) {
	// This overwites the task instead of updating an existing
	// entry, giving users the opportunity to force a resolve operation.
	// 해결동작을 강제할 기회를 주기 위해, 기존의 존재하는것을 갱신하지않고
	// 덮어쓴다
	s.static[n.ID] = &dialTask{flags: staticDialedConn, dest: n}
}

func (s *dialstate) removeStatic(n *discover.Node) {
	// This removes a task so future attempts to connect will not be made.
	// 미래의 연결이 성사되지 않는것을 방지하기 위해 업무를 삭제
	delete(s.static, n.ID)
	// This removes a previous dial timestamp so that application
	// can force a server to reconnect with chosen peer immediately.
	// 어플리케이션이 서버에게 주어진 피어로 재연결 하는것을 강제하기 위해
	// 지난 다이얼 타임스템프를 제거한다 
	s.hist.remove(n.ID)
}

func (s *dialstate) newTasks(nRunning int, peers map[discover.NodeID]*Peer, now time.Time) []task {
	if s.start.IsZero() {
		s.start = now
	}

	var newtasks []task
	addDial := func(flag connFlag, n *discover.Node) bool {
		if err := s.checkDial(n, peers); err != nil {
			log.Trace("Skipping dial candidate", "id", n.ID, "addr", &net.TCPAddr{IP: n.IP, Port: int(n.TCP)}, "err", err)
			return false
		}
		s.dialing[n.ID] = flag
		newtasks = append(newtasks, &dialTask{flags: flag, dest: n})
		return true
	}

	// Compute number of dynamic dials necessary at this point.
	// 현재 동적으로 필요한 연결할 수를 연산한다
	needDynDials := s.maxDynDials
	for _, p := range peers {
		if p.rw.is(dynDialedConn) {
			needDynDials--
		}
	}
	for _, flag := range s.dialing {
		if flag&dynDialedConn != 0 {
			needDynDials--
		}
	}

	// Expire the dial history on every invocation.
	// 매 실행동안 연결요청한 히스토리를 종료시킨다
	s.hist.expire(now)

	// Create dials for static nodes if they are not connected.
	// static노드가 연결되지 않았다면 dial을 생성한다
	for id, t := range s.static {
		err := s.checkDial(t.dest, peers)
		switch err {
		case errNotWhitelisted, errSelf:
			log.Warn("Removing static dial candidate", "id", t.dest.ID, "addr", &net.TCPAddr{IP: t.dest.IP, Port: int(t.dest.TCP)}, "err", err)
			delete(s.static, t.dest.ID)
		case nil:
			s.dialing[id] = t.flags
			newtasks = append(newtasks, t)
		}
	}
	// If we don't have any peers whatsoever, try to dial a random bootnode. This
	// scenario is useful for the testnet (and private networks) where the discovery
	// table might be full of mostly bad peers, making it hard to find good ones.
	// 더이상 노드가 없다면 랜덤한 부트노드로 연결을 시도한다
	// 이시나리오는 좋은것을 찾기 어려운 안좋은 피어들로 발견테이블이 가득찬
	// 테스트넷에서 도움이 된다 
	if len(peers) == 0 && len(s.bootnodes) > 0 && needDynDials > 0 && now.Sub(s.start) > fallbackInterval {
		bootnode := s.bootnodes[0]
		s.bootnodes = append(s.bootnodes[:0], s.bootnodes[1:]...)
		s.bootnodes = append(s.bootnodes, bootnode)

		if addDial(dynDialedConn, bootnode) {
			needDynDials--
		}
	}
	// Use random nodes from the table for half of the necessary
	// dynamic dials.
	// 전반의 동적인 연결을 위한 테이블로부터 랜덤노드를 사용한다
	randomCandidates := needDynDials / 2
	if randomCandidates > 0 {
		n := s.ntab.ReadRandomNodes(s.randomNodes)
		for i := 0; i < randomCandidates && i < n; i++ {
			if addDial(dynDialedConn, s.randomNodes[i]) {
				needDynDials--
			}
		}
	}
	// Create dynamic dials from random lookup results, removing tried
	// items from the result buffer.
	// 랜덤하게 검색한 결과로부터 동적 접속요청을 생성하고, 실행한 아이템들은
	// 결과 버퍼로 부터 삭제한다
	i := 0
	for ; i < len(s.lookupBuf) && needDynDials > 0; i++ {
		if addDial(dynDialedConn, s.lookupBuf[i]) {
			needDynDials--
		}
	}
	s.lookupBuf = s.lookupBuf[:copy(s.lookupBuf, s.lookupBuf[i:])]
	// Launch a discovery lookup if more candidates are needed.
	// 후보자가 더욱 필요할 경우 탐색을 실행한다
	if len(s.lookupBuf) < needDynDials && !s.lookupRunning {
		s.lookupRunning = true
		newtasks = append(newtasks, &discoverTask{})
	}

	// Launch a timer to wait for the next node to expire if all
	// candidates have been tried and no task is currently active.
	// This should prevent cases where the dialer logic is not ticked
	// because there are no pending events.
	// 모든 후보가 시도되었고 더이상 활성화된 업무가 없을때
	// 다음 노드 종료를 위해 대기 타이머를 실행한다
	if nRunning == 0 && len(newtasks) == 0 && s.hist.Len() > 0 {
		t := &waitExpireTask{s.hist.min().exp.Sub(now)}
		newtasks = append(newtasks, t)
	}
	return newtasks
}

var (
	errSelf             = errors.New("is self")
	errAlreadyDialing   = errors.New("already dialing")
	errAlreadyConnected = errors.New("already connected")
	errRecentlyDialed   = errors.New("recently dialed")
	errNotWhitelisted   = errors.New("not contained in netrestrict whitelist")
)

func (s *dialstate) checkDial(n *discover.Node, peers map[discover.NodeID]*Peer) error {
	_, dialing := s.dialing[n.ID]
	switch {
	case dialing:
		return errAlreadyDialing
	case peers[n.ID] != nil:
		return errAlreadyConnected
	case s.ntab != nil && n.ID == s.ntab.Self().ID:
		return errSelf
	case s.netrestrict != nil && !s.netrestrict.Contains(n.IP):
		return errNotWhitelisted
	case s.hist.contains(n.ID):
		return errRecentlyDialed
	}
	return nil
}

func (s *dialstate) taskDone(t task, now time.Time) {
	switch t := t.(type) {
	case *dialTask:
		s.hist.add(t.dest.ID, now.Add(dialHistoryExpiration))
		delete(s.dialing, t.dest.ID)
	case *discoverTask:
		s.lookupRunning = false
		s.lookupBuf = append(s.lookupBuf, t.results...)
	}
}

func (t *dialTask) Do(srv *Server) {
	if t.dest.Incomplete() {
		if !t.resolve(srv) {
			return
		}
	}
	err := t.dial(srv, t.dest)
	if err != nil {
		log.Trace("Dial error", "task", t, "err", err)
		// Try resolving the ID of static nodes if dialing failed.
		// 다이얼이 실패하면 정적 노드의 id를 결정한다 
		if _, ok := err.(*dialError); ok && t.flags&staticDialedConn != 0 {
			if t.resolve(srv) {
				t.dial(srv, t.dest)
			}
		}
	}
}

// resolve attempts to find the current endpoint for the destination
// using discovery.
//
// Resolve operations are throttled with backoff to avoid flooding the
// discovery network with useless queries for nodes that don't exist.
// The backoff delay resets when the node is found.
// resolve함수는 디스커버리를 이용해 목적지를 위한 현재 엔드포인트를 찾으려고 노력한다
// Resolve 동작은 discoverynetwork가 존재하지 않는 노드에 대한 쓸모없는 쿼리로
// 넘치는 것을 막기위해 제한된다.
func (t *dialTask) resolve(srv *Server) bool {
	if srv.ntab == nil {
		log.Debug("Can't resolve node", "id", t.dest.ID, "err", "discovery is disabled")
		return false
	}
	if t.resolveDelay == 0 {
		t.resolveDelay = initialResolveDelay
	}
	if time.Since(t.lastResolved) < t.resolveDelay {
		return false
	}
	resolved := srv.ntab.Resolve(t.dest.ID)
	t.lastResolved = time.Now()
	if resolved == nil {
		t.resolveDelay *= 2
		if t.resolveDelay > maxResolveDelay {
			t.resolveDelay = maxResolveDelay
		}
		log.Debug("Resolving node failed", "id", t.dest.ID, "newdelay", t.resolveDelay)
		return false
	}
	// The node was found.
	// 노드가 찾아짐
	t.resolveDelay = initialResolveDelay
	t.dest = resolved
	log.Debug("Resolved node", "id", t.dest.ID, "addr", &net.TCPAddr{IP: t.dest.IP, Port: int(t.dest.TCP)})
	return true
}

type dialError struct {
	error
}

// dial performs the actual connection attempt.
// dial 함수는 실제 연결을 수행한다
func (t *dialTask) dial(srv *Server, dest *discover.Node) error {
	fd, err := srv.Dialer.Dial(dest)
	if err != nil {
		return &dialError{err}
	}
	mfd := newMeteredConn(fd, false)
	return srv.SetupConn(mfd, t.flags, dest)
}

func (t *dialTask) String() string {
	return fmt.Sprintf("%v %x %v:%d", t.flags, t.dest.ID[:8], t.dest.IP, t.dest.TCP)
}

func (t *discoverTask) Do(srv *Server) {
	// newTasks generates a lookup task whenever dynamic dials are
	// necessary. Lookups need to take some time, otherwise the
	// event loop spins too fast.
	// newTasks는 dyniamic dials가 필요하던 말던 검색업무를 생성한다.
	// 검색은 시간이 필요한 반면 루프는 빠르다
	next := srv.lastLookup.Add(lookupInterval)
	if now := time.Now(); now.Before(next) {
		time.Sleep(next.Sub(now))
	}
	srv.lastLookup = time.Now()
	var target discover.NodeID
	rand.Read(target[:])
	t.results = srv.ntab.Lookup(target)
}

func (t *discoverTask) String() string {
	s := "discovery lookup"
	if len(t.results) > 0 {
		s += fmt.Sprintf(" (%d results)", len(t.results))
	}
	return s
}

func (t waitExpireTask) Do(*Server) {
	time.Sleep(t.Duration)
}
func (t waitExpireTask) String() string {
	return fmt.Sprintf("wait for dial hist expire (%v)", t.Duration)
}

// Use only these methods to access or modify dialHistory.
// dialHistory를 접근하거나 수정할때는 이 메소드들을 사용하라
func (h dialHistory) min() pastDial {
	return h[0]
}
func (h *dialHistory) add(id discover.NodeID, exp time.Time) {
	heap.Push(h, pastDial{id, exp})

}
func (h *dialHistory) remove(id discover.NodeID) bool {
	for i, v := range *h {
		if v.id == id {
			heap.Remove(h, i)
			return true
		}
	}
	return false
}
func (h dialHistory) contains(id discover.NodeID) bool {
	for _, v := range h {
		if v.id == id {
			return true
		}
	}
	return false
}
func (h *dialHistory) expire(now time.Time) {
	for h.Len() > 0 && h.min().exp.Before(now) {
		heap.Pop(h)
	}
}

// heap.Interface boilerplate
// heap.Interface의 기본코드들
func (h dialHistory) Len() int           { return len(h) }
func (h dialHistory) Less(i, j int) bool { return h[i].exp.Before(h[j].exp) }
func (h dialHistory) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *dialHistory) Push(x interface{}) {
	*h = append(*h, x.(pastDial))
}
func (h *dialHistory) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
