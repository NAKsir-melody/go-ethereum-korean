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

package discover

import (
	"bytes"
	"container/list"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/nat"
	"github.com/ethereum/go-ethereum/p2p/netutil"
	"github.com/ethereum/go-ethereum/rlp"
)

const Version = 4

// Errors
var (
	errPacketTooSmall   = errors.New("too small")
	errBadHash          = errors.New("bad hash")
	errExpired          = errors.New("expired")
	errUnsolicitedReply = errors.New("unsolicited reply")
	errUnknownNode      = errors.New("unknown node")
	errTimeout          = errors.New("RPC timeout")
	errClockWarp        = errors.New("reply deadline too far in the future")
	errClosed           = errors.New("socket closed")
)

// Timeouts
const (
	respTimeout = 500 * time.Millisecond
	expiration  = 20 * time.Second

	ntpFailureThreshold = 32               // Continuous timeouts after which to check NTP
	ntpWarningCooldown  = 10 * time.Minute // Minimum amount of time to pass before repeating NTP warning
	driftThreshold      = 10 * time.Second // Allowed clock drift before warning user
// NPT체크를 위한 연속적인 타임아웃
// NPT 경고를 반복하기 전 최소 패싱타임
// 유저 경고전 clock드리프트
)

// RPC 패킷 타입
// RPC packet types
const (
	pingPacket = iota + 1 // zero is 'reserved'
	pongPacket
	findnodePacket
	neighborsPacket
)

// RPC request structures
// RPC 요청 구조
type (
	ping struct {
		Version    uint
		From, To   rpcEndpoint
		Expiration uint64
		// Ignore additional fields (for forward compatibility).
		// 추가 필드는 무시한ㄷ
		Rest []rlp.RawValue `rlp:"tail"`
	}

	// pong is the reply to ping.
	// pong은 ping의 응답이다
	pong struct {
		// This field should mirror the UDP envelope address
		// of the ping packet, which provides a way to discover the
		// the external address (after NAT).
		// 이 필드는 NAT 후에 외부주소를 발견하기 위한 방법을 제공하는 ping패킷의
		// udp 봉투주소를 미러링 해야한다 
		To rpcEndpoint

		ReplyTok   []byte // This contains the hash of the ping packet.
		Expiration uint64 // Absolute timestamp at which the packet becomes invalid.
		// Ignore additional fields (for forward compatibility).
		// 핑패킷의 해시를 포함
		// 패킷이 무효화 되는 절대시간
		// 추가 필드는 무시한다
		Rest []rlp.RawValue `rlp:"tail"`
	}

	// findnode is a query for nodes close to the given target.
	// findnode 구조체는 주어진 타겟에 가까운 노드를 위한 쿼리이다
	findnode struct {
		Target     NodeID // doesn't need to be an actual public key
		// 실제 공개키가 필요하지 않음
		Expiration uint64
		// Ignore additional fields (for forward compatibility).
		//추가 필드는 무시
		Rest []rlp.RawValue `rlp:"tail"`
	}

	// reply to findnode
	// find node에 대한 응답
	neighbors struct {
		Nodes      []rpcNode
		Expiration uint64
		// Ignore additional fields (for forward compatibility).
		Rest []rlp.RawValue `rlp:"tail"`
	}

	rpcNode struct {
		IP  net.IP // len 4 for IPv4 or 16 for IPv6
		UDP uint16 // for discovery protocol
		TCP uint16 // for RLPx protocol
		ID  NodeID
	}

	rpcEndpoint struct {
		IP  net.IP // len 4 for IPv4 or 16 for IPv6
		UDP uint16 // for discovery protocol
		TCP uint16 // for RLPx protocol
		// ipv4/6를 위한 4byte
		// 디스커버리 프로토콜
		// RLPx프로토콜
	}
)

func makeEndpoint(addr *net.UDPAddr, tcpPort uint16) rpcEndpoint {
	ip := addr.IP.To4()
	if ip == nil {
		ip = addr.IP.To16()
	}
	return rpcEndpoint{IP: ip, UDP: uint16(addr.Port), TCP: tcpPort}
}

func (t *udp) nodeFromRPC(sender *net.UDPAddr, rn rpcNode) (*Node, error) {
	if rn.UDP <= 1024 {
		return nil, errors.New("low port")
	}
	if err := netutil.CheckRelayIP(sender.IP, rn.IP); err != nil {
		return nil, err
	}
	if t.netrestrict != nil && !t.netrestrict.Contains(rn.IP) {
		return nil, errors.New("not contained in netrestrict whitelist")
	}
	n := NewNode(rn.ID, rn.IP, rn.UDP, rn.TCP)
	err := n.validateComplete()
	return n, err
}

func nodeToRPC(n *Node) rpcNode {
	return rpcNode{ID: n.ID, IP: n.IP, UDP: n.UDP, TCP: n.TCP}
}

type packet interface {
	handle(t *udp, from *net.UDPAddr, fromID NodeID, mac []byte) error
	name() string
}

type conn interface {
	ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error)
	WriteToUDP(b []byte, addr *net.UDPAddr) (n int, err error)
	Close() error
	LocalAddr() net.Addr
}

// udp implements the RPC protocol.
// udp구조체는 RPC프로토콜을 구현한다
type udp struct {
	conn        conn
	netrestrict *netutil.Netlist
	priv        *ecdsa.PrivateKey
	ourEndpoint rpcEndpoint

	addpending chan *pending
	gotreply   chan reply

	closing chan struct{}
	nat     nat.Interface

	*Table
}

// pending represents a pending reply.
//
// some implementations of the protocol wish to send more than one
// reply packet to findnode. in general, any neighbors packet cannot
// be matched up with a specific findnode packet.
//
// our implementation handles this by storing a callback function for
// each pending reply. incoming packets from a node are dispatched
// to all the callback functions for that node.
// pending구조체는 지연중인 응답을 나타낸다
// 프로토콜의 어떤 구현은 노드를 찾기 위하 하나 이상의 응답패킷을 보내길 원한다
// 일반적으로 이웃 패킷은 특정 findnode패킷에 매칭되지 않는다
// 우리의 구현은 각 지연된 응답에 대한 콜백펑션을 저장함으로서 관리한다
// 노드들로 부터 수신되는 패킷들은 해당 노드를 위한 콜백함세들에의해 처리된다
type pending struct {
	// these fields must match in the reply.
	from  NodeID
	// 이 필드들은 응답에 매칭되어야 한다
	ptype byte

	// time when the request must complete
	deadline time.Time
	// 요청이 완료되어야 하는 시간

	// callback is called when a matching reply arrives. if it returns
	// true, the callback is removed from the pending reply queue.
	// if it returns false, the reply is considered incomplete and
	// the callback will be invoked again for the next matching reply.
	// callback은 매칭되는 응답이 도달했을 경우 호출된다
	// 만약 참을 리턴하면 함수는 지연 대기 큐에서제거된다
	// 거짓을 리턴하면 완료되지 않은것으로 간주되어 재다음 매칭을 위해 실행된다
	callback func(resp interface{}) (done bool)

	// errc receives nil when the callback indicates completion or an
	// error if no further reply is received within the timeout.
	// errc 함수는 콜백함수가 완료되었다면 nil,
	// 타임아웃되어 더이상의 수신이 없을때는 error를 수신한다
	errc chan<- error
}

type reply struct {
	from  NodeID
	ptype byte
	data  interface{}
	// loop indicates whether there was
	// a matching request by sending on this channel.
	// 루프는 매칭되는 리퀘스트가 있는지를 이 채널을 통해 전송함으로서 알린다
	matched chan<- bool
}

// ReadPacket is sent to the unhandled channel when it could not be processed
// ReadPacket은 처리될수 없을때 처리되지 않은 채널로 전달된다
type ReadPacket struct {
	Data []byte
	Addr *net.UDPAddr
}

// Config holds Table-related settings.
// Config구조체는 테이블 관련된 설정을 저장한다
type Config struct {
	// These settings are required and configure the UDP listener:
	// 이 설정들은 요구되며 udp수신자를 설정한다
	PrivateKey *ecdsa.PrivateKey

	// These settings are optional:
	// 이 설정들은 옵션이다
	AnnounceAddr *net.UDPAddr      // local address announced in the DHT
	NodeDBPath   string            // if set, the node database is stored at this filesystem location
	NetRestrict  *netutil.Netlist  // network whitelist
	Bootnodes    []*Node           // list of bootstrap nodes
	Unhandled    chan<- ReadPacket // unhandled packets are sent on this channel
	// DHT에서 사용될 로컬어드레스
	// 설정되어있다면 node db는 이 파일시스템 위치에 저장됨
	// 네트워크 화이트리스트
	// 부트스트랩 노드 리스트
	// 처리되지 않은 패킷들은 이채널로 보내진다
}

// ListenUDP returns a new table that listens for UDP packets on laddr.
// ListenUDP 함수는 laddr상에서 udp 패킷에 대한 수신자의 새 테이블을 반환한다 
func ListenUDP(c conn, cfg Config) (*Table, error) {
	tab, _, err := newUDP(c, cfg)
	if err != nil {
		return nil, err
	}
	log.Info("UDP listener up", "self", tab.self)
	return tab, nil
}

func newUDP(c conn, cfg Config) (*Table, *udp, error) {
	udp := &udp{
		conn:        c,
		priv:        cfg.PrivateKey,
		netrestrict: cfg.NetRestrict,
		closing:     make(chan struct{}),
		gotreply:    make(chan reply),
		addpending:  make(chan *pending),
	}
	realaddr := c.LocalAddr().(*net.UDPAddr)
	if cfg.AnnounceAddr != nil {
		realaddr = cfg.AnnounceAddr
	}
	// TODO: separate TCP port
	udp.ourEndpoint = makeEndpoint(realaddr, uint16(realaddr.Port))
	tab, err := newTable(udp, PubkeyID(&cfg.PrivateKey.PublicKey), realaddr, cfg.NodeDBPath, cfg.Bootnodes)
	if err != nil {
		return nil, nil, err
	}
	udp.Table = tab

	go udp.loop()
	go udp.readLoop(cfg.Unhandled)
	return udp.Table, udp, nil
}

func (t *udp) close() {
	close(t.closing)
	t.conn.Close()
	// TODO: wait for the loops to end.
}

// ping sends a ping message to the given node and waits for a reply.
// ping 함수는 주어진 노드로 핑메시지를 전송하고 응답을 대기한다
func (t *udp) ping(toid NodeID, toaddr *net.UDPAddr) error {
	req := &ping{
		Version:    Version,
		From:       t.ourEndpoint,
		To:         makeEndpoint(toaddr, 0), // TODO: maybe use known TCP port from DB
		Expiration: uint64(time.Now().Add(expiration).Unix()),
	}
	packet, hash, err := encodePacket(t.priv, pingPacket, req)
	if err != nil {
		return err
	}
	errc := t.pending(toid, pongPacket, func(p interface{}) bool {
		return bytes.Equal(p.(*pong).ReplyTok, hash)
	})
	t.write(toaddr, req.name(), packet)
	return <-errc
}

func (t *udp) waitping(from NodeID) error {
	return <-t.pending(from, pingPacket, func(interface{}) bool { return true })
}

// findnode sends a findnode request to the given node and waits until
// the node has sent up to k neighbors.
// findnode는 findnode 리퀘스트를 주어진 노드로 전송하고 
// 노드가 k개의 이웃에게 전달될때까지 대기한다
func (t *udp) findnode(toid NodeID, toaddr *net.UDPAddr, target NodeID) ([]*Node, error) {
	nodes := make([]*Node, 0, bucketSize)
	nreceived := 0
	errc := t.pending(toid, neighborsPacket, func(r interface{}) bool {
		reply := r.(*neighbors)
		for _, rn := range reply.Nodes {
			nreceived++
			n, err := t.nodeFromRPC(toaddr, rn)
			if err != nil {
				log.Trace("Invalid neighbor node received", "ip", rn.IP, "addr", toaddr, "err", err)
				continue
			}
			nodes = append(nodes, n)
		}
		return nreceived >= bucketSize
	})
	t.send(toaddr, findnodePacket, &findnode{
		Target:     target,
		Expiration: uint64(time.Now().Add(expiration).Unix()),
	})
	err := <-errc
	return nodes, err
}

// pending adds a reply callback to the pending reply queue.
// see the documentation of type pending for a detailed explanation.
// pending함수는 응답 콜백을 대기중인 응답 큐에 추가한다
func (t *udp) pending(id NodeID, ptype byte, callback func(interface{}) bool) <-chan error {
	ch := make(chan error, 1)
	p := &pending{from: id, ptype: ptype, callback: callback, errc: ch}
	select {
	case t.addpending <- p:
		// loop will handle it
	case <-t.closing:
		ch <- errClosed
	}
	return ch
}

func (t *udp) handleReply(from NodeID, ptype byte, req packet) bool {
	matched := make(chan bool, 1)
	select {
	case t.gotreply <- reply{from, ptype, req, matched}:
		// loop will handle it
		return <-matched
	case <-t.closing:
		return false
	}
}

// loop runs in its own goroutine. it keeps track of
// the refresh timer and the pending reply queue.
// loop 함수는 자체 고루틴을 실행한다. 리프레시 타이머와 펜딩 리플라이 큐를 관리한다
func (t *udp) loop() {
	var (
		plist        = list.New()
		timeout      = time.NewTimer(0)
		nextTimeout  *pending // head of plist when timeout was last reset
		// 타임아웃이 마지막으로 리셋되었을때의 plist 헤드
		contTimeouts = 0      // number of continuous timeouts to do NTP checks
		// NTP 체크를 하기 위한 연속적인 타임아웃 수
		ntpWarnTime  = time.Unix(0, 0)
	)
	<-timeout.C // ignore first timeout
	// 첫 타임아웃은 무시
	defer timeout.Stop()

	resetTimeout := func() {
		if plist.Front() == nil || nextTimeout == plist.Front().Value {
			return
		}
		// Start the timer so it fires when the next pending reply has expired.
		// 다음 펜딩 응답이 만료되었을때를 위한 타이머를 시작한다
		now := time.Now()
		for el := plist.Front(); el != nil; el = el.Next() {
			nextTimeout = el.Value.(*pending)
			if dist := nextTimeout.deadline.Sub(now); dist < 2*respTimeout {
				timeout.Reset(dist)
				return
			}
			// Remove pending replies whose deadline is too far in the
			// future. These can occur if the system clock jumped
			// backwards after the deadline was assigned.
			// 데드라인이 먼미래인 대기중 응답을 제거한다. 데드라인이 설정된후
			// 시스템 클럭의 시간이 뒤쪽으로 점프했을때 발생가능하다
			nextTimeout.errc <- errClockWarp
			plist.Remove(el)
		}
		nextTimeout = nil
		timeout.Stop()
	}

	for {
		resetTimeout()

		select {
		case <-t.closing:
			for el := plist.Front(); el != nil; el = el.Next() {
				el.Value.(*pending).errc <- errClosed
			}
			return

		case p := <-t.addpending:
			p.deadline = time.Now().Add(respTimeout)
			plist.PushBack(p)

		case r := <-t.gotreply:
			var matched bool
			for el := plist.Front(); el != nil; el = el.Next() {
				p := el.Value.(*pending)
				if p.from == r.from && p.ptype == r.ptype {
					matched = true
					// Remove the matcher if its callback indicates
					// that all replies have been received. This is
					// required for packet types that expect multiple
					// reply packets.
					// 매쳐의 콜백이 모든 응답을 수신하였을때, 매처를 제거한다
					// 이것은 여러개의 응답 패킷을 기대하는 패킷타입을 위해 필요하다
					if p.callback(r.data) {
						p.errc <- nil
						plist.Remove(el)
					}
					// Reset the continuous timeout counter (time drift detection)
					// 연속적인 타임아웃 카운터를 초기화한다
					contTimeouts = 0
				}
			}
			r.matched <- matched

		case now := <-timeout.C:
			nextTimeout = nil

			// Notify and remove callbacks whose deadline is in the past.
			// 데드라인이 과거인 콜백을 알리고 제거한다
			for el := plist.Front(); el != nil; el = el.Next() {
				p := el.Value.(*pending)
				if now.After(p.deadline) || now.Equal(p.deadline) {
					p.errc <- errTimeout
					plist.Remove(el)
					contTimeouts++
				}
			}
			// If we've accumulated too many timeouts, do an NTP time sync check
			// 만약 너무 많은 타임아웃을 누적했다면 NTP 타임싱크 체크를 한다
			if contTimeouts > ntpFailureThreshold {
				if time.Since(ntpWarnTime) >= ntpWarningCooldown {
					ntpWarnTime = time.Now()
					go checkClockDrift()
				}
				contTimeouts = 0
			}
		}
	}
}

const (
	macSize  = 256 / 8
	sigSize  = 520 / 8
	headSize = macSize + sigSize // space of packet frame data
	// 패킷 프레임 데이터의 공간
)

var (
	headSpace = make([]byte, headSize)

	// Neighbors replies are sent across multiple packets to
	// stay below the 1280 byte limit. We compute the maximum number
	// of entries by stuffing a packet until it grows too large.
	// 이웃들의 응답들은 1280byte 리밋 안에서 여러 패킷에 걸쳐 보내진다
	// 우리는 너무 커지지 않을때까지 패킷 스터핑을 통해 최대 엔트리 숫자를
	// 계산한다
	maxNeighbors int
)

func init() {
	p := neighbors{Expiration: ^uint64(0)}
	maxSizeNode := rpcNode{IP: make(net.IP, 16), UDP: ^uint16(0), TCP: ^uint16(0)}
	for n := 0; ; n++ {
		p.Nodes = append(p.Nodes, maxSizeNode)
		size, _, err := rlp.EncodeToReader(p)
		if err != nil {
			// If this ever happens, it will be caught by the unit tests.
			// 이것이 발생하면 유닛테스트에서 잡혀야 한다
			panic("cannot encode: " + err.Error())
		}
		if headSize+size+1 >= 1280 {
			maxNeighbors = n
			break
		}
	}
}

func (t *udp) send(toaddr *net.UDPAddr, ptype byte, req packet) ([]byte, error) {
	packet, hash, err := encodePacket(t.priv, ptype, req)
	if err != nil {
		return hash, err
	}
	return hash, t.write(toaddr, req.name(), packet)
}

func (t *udp) write(toaddr *net.UDPAddr, what string, packet []byte) error {
	_, err := t.conn.WriteToUDP(packet, toaddr)
	log.Trace(">> "+what, "addr", toaddr, "err", err)
	return err
}

func encodePacket(priv *ecdsa.PrivateKey, ptype byte, req interface{}) (packet, hash []byte, err error) {
	b := new(bytes.Buffer)
	b.Write(headSpace)
	b.WriteByte(ptype)
	if err := rlp.Encode(b, req); err != nil {
		log.Error("Can't encode discv4 packet", "err", err)
		return nil, nil, err
	}
	packet = b.Bytes()
	sig, err := crypto.Sign(crypto.Keccak256(packet[headSize:]), priv)
	if err != nil {
		log.Error("Can't sign discv4 packet", "err", err)
		return nil, nil, err
	}
	copy(packet[macSize:], sig)
	// add the hash to the front. Note: this doesn't protect the
	// packet in any way. Our public key will be part of this hash in
	// The future.
	// 해시를 앞쪽에 추가한다
	// 패킷을 보호하진 않는다. 우리의 공통키는 미래에 해시의 일부가 될것이다
	hash = crypto.Keccak256(packet[macSize:])
	copy(packet, hash)
	return packet, hash, nil
}

// readLoop runs in its own goroutine. it handles incoming UDP packets.
// readLoop 함수는 자체 고루틴 안에서 시행되며, 들어오는 udp 패킷을 처리한다
func (t *udp) readLoop(unhandled chan<- ReadPacket) {
	defer t.conn.Close()
	if unhandled != nil {
		defer close(unhandled)
	}
	// Discovery packets are defined to be no larger than 1280 bytes.
	// Packets larger than this size will be cut at the end and treated
	// as invalid because their hash won't match.
	// 디스커버리 패킷들은 1280보다 길게 정의되어서는 안된다
	// 이것보다 ㅋ믄 패킷은 끝부분이 잘려 매칭되지 않을것이므로 
	// 무효한 것으로 간주된다
	buf := make([]byte, 1280)
	for {
		nbytes, from, err := t.conn.ReadFromUDP(buf)
		if netutil.IsTemporaryError(err) {
			// Ignore temporary read errors.
			// 임시적인 읽기 에러는 무시
			log.Debug("Temporary UDP read error", "err", err)
			continue
		} else if err != nil {
			// Shut down the loop for permament errors.
			// 영구적인 에러는 루프를 닫는다
			log.Debug("UDP read error", "err", err)
			return
		}
		if t.handlePacket(from, buf[:nbytes]) != nil && unhandled != nil {
			select {
			case unhandled <- ReadPacket{buf[:nbytes], from}:
			default:
			}
		}
	}
}

func (t *udp) handlePacket(from *net.UDPAddr, buf []byte) error {
	packet, fromID, hash, err := decodePacket(buf)
	if err != nil {
		log.Debug("Bad discv4 packet", "addr", from, "err", err)
		return err
	}
	err = packet.handle(t, from, fromID, hash)
	log.Trace("<< "+packet.name(), "addr", from, "err", err)
	return err
}

func decodePacket(buf []byte) (packet, NodeID, []byte, error) {
	if len(buf) < headSize+1 {
		return nil, NodeID{}, nil, errPacketTooSmall
	}
	hash, sig, sigdata := buf[:macSize], buf[macSize:headSize], buf[headSize:]
	shouldhash := crypto.Keccak256(buf[macSize:])
	if !bytes.Equal(hash, shouldhash) {
		return nil, NodeID{}, nil, errBadHash
	}
	fromID, err := recoverNodeID(crypto.Keccak256(buf[headSize:]), sig)
	if err != nil {
		return nil, NodeID{}, hash, err
	}
	var req packet
	switch ptype := sigdata[0]; ptype {
	case pingPacket:
		req = new(ping)
	case pongPacket:
		req = new(pong)
	case findnodePacket:
		req = new(findnode)
	case neighborsPacket:
		req = new(neighbors)
	default:
		return nil, fromID, hash, fmt.Errorf("unknown type: %d", ptype)
	}
	s := rlp.NewStream(bytes.NewReader(sigdata[1:]), 0)
	err = s.Decode(req)
	return req, fromID, hash, err
}

func (req *ping) handle(t *udp, from *net.UDPAddr, fromID NodeID, mac []byte) error {
	if expired(req.Expiration) {
		return errExpired
	}
	t.send(from, pongPacket, &pong{
		To:         makeEndpoint(from, req.From.TCP),
		ReplyTok:   mac,
		Expiration: uint64(time.Now().Add(expiration).Unix()),
	})
	if !t.handleReply(fromID, pingPacket, req) {
		// Note: we're ignoring the provided IP address right now
		// 제공된 ip address는 현재로서는 무시할것이다
		go t.bond(true, fromID, from, req.From.TCP)
	}
	return nil
}

func (req *ping) name() string { return "PING/v4" }

func (req *pong) handle(t *udp, from *net.UDPAddr, fromID NodeID, mac []byte) error {
	if expired(req.Expiration) {
		return errExpired
	}
	if !t.handleReply(fromID, pongPacket, req) {
		return errUnsolicitedReply
	}
	return nil
}

func (req *pong) name() string { return "PONG/v4" }

func (req *findnode) handle(t *udp, from *net.UDPAddr, fromID NodeID, mac []byte) error {
	if expired(req.Expiration) {
		return errExpired
	}
	if !t.db.hasBond(fromID) {
		// No bond exists, we don't process the packet. This prevents
		// an attack vector where the discovery protocol could be used
		// to amplify traffic in a DDOS attack. A malicious actor
		// would send a findnode request with the IP address and UDP
		// port of the target as the source address. The recipient of
		// the findnode packet would then send a neighbors packet
		// (which is a much bigger packet than findnode) to the victim.
		// 본드가 존재하지 않아 패킷을 처리하지 않는다
		// 이것은 ddos 공격에서 디스커버리 프로토콜이 트래픽을 증가시키는
		// 공격백터로 쓰이는것을 막는다.
		// 악의적인 행위자는 findnode 요청을 타겟의 ip주소와 udp포트 주소로
		// 전송할 것이다. 수신자는 findnode보다 엄청 큰 이웃 패킷을 희생자에게
		// 전송한다
		return errUnknownNode
	}
	target := crypto.Keccak256Hash(req.Target[:])
	t.mutex.Lock()
	closest := t.closest(target, bucketSize).entries
	t.mutex.Unlock()

	p := neighbors{Expiration: uint64(time.Now().Add(expiration).Unix())}
	var sent bool
	// Send neighbors in chunks with at most maxNeighbors per packet
	// to stay below the 1280 byte limit.
	// 1280 바이트한도 안쪽으로 이웃들을 전송한다
	for _, n := range closest {
		if netutil.CheckRelayIP(from.IP, n.IP) == nil {
			p.Nodes = append(p.Nodes, nodeToRPC(n))
		}
		if len(p.Nodes) == maxNeighbors {
			t.send(from, neighborsPacket, &p)
			p.Nodes = p.Nodes[:0]
			sent = true
		}
	}
	if len(p.Nodes) > 0 || !sent {
		t.send(from, neighborsPacket, &p)
	}
	return nil
}

func (req *findnode) name() string { return "FINDNODE/v4" }

func (req *neighbors) handle(t *udp, from *net.UDPAddr, fromID NodeID, mac []byte) error {
	if expired(req.Expiration) {
		return errExpired
	}
	if !t.handleReply(fromID, neighborsPacket, req) {
		return errUnsolicitedReply
	}
	return nil
}

func (req *neighbors) name() string { return "NEIGHBORS/v4" }

func expired(ts uint64) bool {
	return time.Unix(int64(ts), 0).Before(time.Now())
}
