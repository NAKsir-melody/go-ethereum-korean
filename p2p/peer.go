// Copyright 2014 The go-ethereum Authors
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
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	baseProtocolVersion    = 5
	baseProtocolLength     = uint64(16)
	baseProtocolMaxMsgSize = 2 * 1024

	snappyProtocolVersion = 5

	pingInterval = 15 * time.Second
)

const (
	// devp2p message codes
	// devp2p 메시지 코드들
	handshakeMsg = 0x00
	discMsg      = 0x01
	pingMsg      = 0x02
	pongMsg      = 0x03
)

// protoHandshake is the RLP structure of the protocol handshake.
// protoHandshake는 핸드쉐이크 프로토콜의 RLP 구조이다
type protoHandshake struct {
	Version    uint64
	Name       string
	Caps       []Cap
	ListenPort uint64
	ID         discover.NodeID

	// Ignore additional fields (for forward compatibility).
	// 추가필드는 무시한다(향후 호환을 위해)
	Rest []rlp.RawValue `rlp:"tail"`
}

// PeerEventType is the type of peer events emitted by a p2p.Server
// 피어 이벤트 타입은 p2p서버에 의해 퍼질 피어 이벤트의 종류이다
type PeerEventType string

const (
	// PeerEventTypeAdd is the type of event emitted when a peer is added
	// to a p2p.Server
	// PeerEventTypeAdd는 피어가 p2p.server에 추가되었을때 내보내는 이벤트 이다
	PeerEventTypeAdd PeerEventType = "add"

	// PeerEventTypeDrop is the type of event emitted when a peer is
	// dropped from a p2p.Server
	// PeerEventTypeDrop은 피어가 p2p.Server에서 제거 되었을때 내보내는 이벤트이다.
	PeerEventTypeDrop PeerEventType = "drop"

	// PeerEventTypeMsgSend is the type of event emitted when a
	// message is successfully sent to a peer
	// PeerEventTypeMsgSend는 메시지가 성공적으로 피어로 전달되었을때 발생하는 이벤트이다
	PeerEventTypeMsgSend PeerEventType = "msgsend"

	// PeerEventTypeMsgRecv is the type of event emitted when a
	// message is received from a peer
	// PeerEventTypeMsgRecv는 메시지가 피어로부터 성공적으로 수신되었을때 보내지는 이벤트이다
	PeerEventTypeMsgRecv PeerEventType = "msgrecv"
)

// PeerEvent is an event emitted when peers are either added or dropped from
// a p2p.Server or when a message is sent or received on a peer connection
// PeerEvent구조체는 피어가 p2p.Server로 부터 추가되거나, 제거되었을때 발생하거나, 
// 피어간 연결에서 메시지가 주고받아졌을때 발생한다
type PeerEvent struct {
	Type     PeerEventType   `json:"type"`
	Peer     discover.NodeID `json:"peer"`
	Error    string          `json:"error,omitempty"`
	Protocol string          `json:"protocol,omitempty"`
	MsgCode  *uint64         `json:"msg_code,omitempty"`
	MsgSize  *uint32         `json:"msg_size,omitempty"`
}

// Peer represents a connected remote node.
// peer 구조체는 연결된 원격 노드를 나타낸다
type Peer struct {
	rw      *conn
	running map[string]*protoRW
	log     log.Logger
	created mclock.AbsTime

	wg       sync.WaitGroup
	protoErr chan error
	closed   chan struct{}
	disc     chan DiscReason

	// events receives message send / receive events if set
	// 이벤트들은 message send/recevie이벤트를 받는다
	events *event.Feed
}

// NewPeer returns a peer for testing purposes.
// NewPeer함수는 테스트 목적의 피어를 반환한다
func NewPeer(id discover.NodeID, name string, caps []Cap) *Peer {
	pipe, _ := net.Pipe()
	conn := &conn{fd: pipe, transport: nil, id: id, caps: caps, name: name}
	peer := newPeer(conn, nil)
	close(peer.closed) // ensures Disconnect doesn't block
	return peer
}

// ID returns the node's public key.
// ID함수는 노드의 공용키를 반환한다
func (p *Peer) ID() discover.NodeID {
	return p.rw.id
}

// Name returns the node name that the remote node advertised.
// Name함수는 원격 노드가 알려온 노드의 이름을 반환한다
func (p *Peer) Name() string {
	return p.rw.name
}

// Caps returns the capabilities (supported subprotocols) of the remote peer.
// Caps함수는 원격 피어의 능력(서브 프로토콜의 지원여부)를 반환한다
func (p *Peer) Caps() []Cap {
	// TODO: maybe return copy
	return p.rw.caps
}

// RemoteAddr returns the remote address of the network connection.
// RemoteAddr함수는 네트워크 연결의 원격 주소를 반환한다
func (p *Peer) RemoteAddr() net.Addr {
	return p.rw.fd.RemoteAddr()
}

// LocalAddr returns the local address of the network connection.
// LocalAddr 함수는 네트워크 연결의 로컬주소를 반환한다
func (p *Peer) LocalAddr() net.Addr {
	return p.rw.fd.LocalAddr()
}

// Disconnect terminates the peer connection with the given reason.
// It returns immediately and does not wait until the connection is closed.
// Disconnect 함수는 주어진 이유로 피어와의 연결을 끝낸다. 
// 이함수는 즉시 반환되며 연결이 닫힐때까지 대기하지 않는다
func (p *Peer) Disconnect(reason DiscReason) {
	select {
	case p.disc <- reason:
	case <-p.closed:
	}
}

// String implements fmt.Stringer.
// String 함수는 fmt.Stringer를 구현한다
func (p *Peer) String() string {
	return fmt.Sprintf("Peer %x %v", p.rw.id[:8], p.RemoteAddr())
}

// Inbound returns true if the peer is an inbound connection
// Inbound함수는 피어가 내부로 들어온 연결일때 참을 반환한다
func (p *Peer) Inbound() bool {
	return p.rw.flags&inboundConn != 0
}

func newPeer(conn *conn, protocols []Protocol) *Peer {
	protomap := matchProtocols(protocols, conn.caps, conn)
	p := &Peer{
		rw:       conn,
		running:  protomap,
		created:  mclock.Now(),
		disc:     make(chan DiscReason),
		protoErr: make(chan error, len(protomap)+1), // protocols + pingLoop
		closed:   make(chan struct{}),
		log:      log.New("id", conn.id, "conn", conn.flags),
	}
	return p
}

func (p *Peer) Log() log.Logger {
	return p.log
}

func (p *Peer) run() (remoteRequested bool, err error) {
	var (
		writeStart = make(chan struct{}, 1)
		writeErr   = make(chan error, 1)
		readErr    = make(chan error, 1)
		reason     DiscReason // sent to the peer
		// 피어에게 보냄
	)
	p.wg.Add(2)
	go p.readLoop(readErr)
	go p.pingLoop()

	// Start all protocol handlers.
	// 모든 프로토콜 핸들러를 시작
	writeStart <- struct{}{}
	p.startProtocols(writeStart, writeErr)

	// Wait for an error or disconnect.
	// 에러나 연결단절을 위해 대기
loop:
	for {
		select {
		case err = <-writeErr:
			// A write finished. Allow the next write to start if
			// there was no error.
			// 쓰기동작 종료. 에러가 없다면 다음 쓰기를 허용한다
			if err != nil {
				reason = DiscNetworkError
				break loop
			}
			writeStart <- struct{}{}
		case err = <-readErr:
			if r, ok := err.(DiscReason); ok {
				remoteRequested = true
				reason = r
			} else {
				reason = DiscNetworkError
			}
			break loop
		case err = <-p.protoErr:
			reason = discReasonForError(err)
			break loop
		case err = <-p.disc:
			reason = discReasonForError(err)
			break loop
		}
	}

	close(p.closed)
	p.rw.close(reason)
	p.wg.Wait()
	return remoteRequested, err
}

func (p *Peer) pingLoop() {
	ping := time.NewTimer(pingInterval)
	defer p.wg.Done()
	defer ping.Stop()
	for {
		select {
		case <-ping.C:
			if err := SendItems(p.rw, pingMsg); err != nil {
				p.protoErr <- err
				return
			}
			ping.Reset(pingInterval)
		case <-p.closed:
			return
		}
	}
}

func (p *Peer) readLoop(errc chan<- error) {
	defer p.wg.Done()
	for {
		msg, err := p.rw.ReadMsg()
		if err != nil {
			errc <- err
			return
		}
		msg.ReceivedAt = time.Now()
		if err = p.handle(msg); err != nil {
			errc <- err
			return
		}
	}
}

func (p *Peer) handle(msg Msg) error {
	switch {
	case msg.Code == pingMsg:
		msg.Discard()
		go SendItems(p.rw, pongMsg)
	case msg.Code == discMsg:
		var reason [1]DiscReason
		// This is the last message. We don't need to discard or
		// check errors because, the connection will be closed after it.
		// 이것은 마지막 메시지 이다. 연결이 곧 끊길 것이기 때문에
		// 우리는 이것을 무시하거나 체크할 필요가 없다
		rlp.Decode(msg.Payload, &reason)
		return reason[0]
	case msg.Code < baseProtocolLength:
		// ignore other base protocol messages
		// 다른 기본 프로토콜 베시지를 무시한다
		return msg.Discard()
	default:
		// it's a subprotocol message
		// 서브프로토콜 메시지이다
		proto, err := p.getProto(msg.Code)
		if err != nil {
			return fmt.Errorf("msg code out of range: %v", msg.Code)
		}
		select {
		case proto.in <- msg:
			return nil
		case <-p.closed:
			return io.EOF
		}
	}
	return nil
}

func countMatchingProtocols(protocols []Protocol, caps []Cap) int {
	n := 0
	for _, cap := range caps {
		for _, proto := range protocols {
			if proto.Name == cap.Name && proto.Version == cap.Version {
				n++
			}
		}
	}
	return n
}

// matchProtocols creates structures for matching named subprotocols.
// matchProtocols함수는 이름이 있는 서브 프로토콜을 위한 구조를 생성한다
func matchProtocols(protocols []Protocol, caps []Cap, rw MsgReadWriter) map[string]*protoRW {
	sort.Sort(capsByNameAndVersion(caps))
	offset := baseProtocolLength
	result := make(map[string]*protoRW)

outer:
	for _, cap := range caps {
		for _, proto := range protocols {
			if proto.Name == cap.Name && proto.Version == cap.Version {
				// If an old protocol version matched, revert it
				// 오래된 프로토콜버전과 매칭될경우 리버트 한다
				if old := result[cap.Name]; old != nil {
					offset -= old.Length
				}
				// Assign the new match
				// 새로운 매치를 설정한다
				result[cap.Name] = &protoRW{Protocol: proto, offset: offset, in: make(chan Msg), w: rw}
				offset += proto.Length

				continue outer
			}
		}
	}
	return result
}

func (p *Peer) startProtocols(writeStart <-chan struct{}, writeErr chan<- error) {
	p.wg.Add(len(p.running))
	for _, proto := range p.running {
		proto := proto
		proto.closed = p.closed
		proto.wstart = writeStart
		proto.werr = writeErr
		var rw MsgReadWriter = proto
		if p.events != nil {
			rw = newMsgEventer(rw, p.events, p.ID(), proto.Name)
		}
		p.log.Trace(fmt.Sprintf("Starting protocol %s/%d", proto.Name, proto.Version))
		go func() {
			err := proto.Run(p, rw)
			if err == nil {
				p.log.Trace(fmt.Sprintf("Protocol %s/%d returned", proto.Name, proto.Version))
				err = errProtocolReturned
			} else if err != io.EOF {
				p.log.Trace(fmt.Sprintf("Protocol %s/%d failed", proto.Name, proto.Version), "err", err)
			}
			p.protoErr <- err
			p.wg.Done()
		}()
	}
}

// getProto finds the protocol responsible for handling
// the given message code.
// getProto함수는 주어진 메시지 코드를 처리할 프로토콜을 검색한다
func (p *Peer) getProto(code uint64) (*protoRW, error) {
	for _, proto := range p.running {
		if code >= proto.offset && code < proto.offset+proto.Length {
			return proto, nil
		}
	}
	return nil, newPeerError(errInvalidMsgCode, "%d", code)
}

type protoRW struct {
	Protocol
	in     chan Msg        // receices read messages
	closed <-chan struct{} // receives when peer is shutting down
	wstart <-chan struct{} // receives when write may start
	werr   chan<- error    // for write results
	offset uint64
	w      MsgWriter
	// 수신한 메시지
	// 피어가 닫힐때 수신
	// 쓰기가 시작될때
	// 결과를 받기위함
}

func (rw *protoRW) WriteMsg(msg Msg) (err error) {
	if msg.Code >= rw.Length {
		return newPeerError(errInvalidMsgCode, "not handled")
	}
	msg.Code += rw.offset
	select {
	case <-rw.wstart:
		err = rw.w.WriteMsg(msg)
		// Report write status back to Peer.run. It will initiate
		// shutdown if the error is non-nil and unblock the next write
		// otherwise. The calling protocol code should exit for errors
		// as well but we don't want to rely on that.
		// peer.run을 위해 쓰기 상태를 되돌림을 레포트 한다
		// 에러가 나거나 다음 쓰기의 블록을 해제할 경우 셧다운을 유발한다.
		// 호출 프로토콜 코드는 에러에 대해 exit이여야 한다
		rw.werr <- err
	case <-rw.closed:
		err = fmt.Errorf("shutting down")
	}
	return err
}

func (rw *protoRW) ReadMsg() (Msg, error) {
	select {
	case msg := <-rw.in:
		msg.Code -= rw.offset
		return msg, nil
	case <-rw.closed:
		return Msg{}, io.EOF
	}
}

// PeerInfo represents a short summary of the information known about a connected
// peer. Sub-protocol independent fields are contained and initialized here, with
// protocol specifics delegated to all connected sub-protocols.
// PeerInfo 구조체는 연결된 피어에 대한 알려진 정보를 요약한 것을 나타낸다
// 서브 프로토콜에 독립적인 필드들은 여기서 초기화 되며, 
// 서프 포로토콜과 관련된 것들은 위임된다
type PeerInfo struct {
	ID      string   `json:"id"`   // Unique node identifier (also the encryption key)
	Name    string   `json:"name"` // Name of the node, including client type, version, OS, custom data
	Caps    []string `json:"caps"` // Sum-protocols advertised by this particular peer
	Network struct {
		LocalAddress  string `json:"localAddress"`  // Local endpoint of the TCP data connection
		RemoteAddress string `json:"remoteAddress"` // Remote endpoint of the TCP data connection
		Inbound       bool   `json:"inbound"`
		Trusted       bool   `json:"trusted"`
		Static        bool   `json:"static"`
	} `json:"network"`
	Protocols map[string]interface{} `json:"protocols"` // Sub-protocol specific metadata fields
	// 노드의 id (암호화된 키)
	// 노드의 이름(타입, version, os, 임의 데이터)
	// 특정 피어에서 알려진 서브 프로토콜들 
	// TCP연결의 로컬 엔드포인트
	// TCP연결의 원격 엔드포인트
	// 서브프로토콜을 위한 메타 데이터 필드
}

// Info gathers and returns a collection of metadata known about a peer.
// Info함수는 피어에 대해 알려진 메타데이터의 모음을 반환한다
func (p *Peer) Info() *PeerInfo {
	// Gather the protocol capabilities
	// 프로토콜 능력을 수집힌다
	var caps []string
	for _, cap := range p.Caps() {
		caps = append(caps, cap.String())
	}
	// Assemble the generic peer metadata
	// 피어의 메타데이터를 생성
	info := &PeerInfo{
		ID:        p.ID().String(),
		Name:      p.Name(),
		Caps:      caps,
		Protocols: make(map[string]interface{}),
	}
	info.Network.LocalAddress = p.LocalAddr().String()
	info.Network.RemoteAddress = p.RemoteAddr().String()
	info.Network.Inbound = p.rw.is(inboundConn)
	info.Network.Trusted = p.rw.is(trustedConn)
	info.Network.Static = p.rw.is(staticDialedConn)

	// Gather all the running protocol infos
	// 실행중인 프로토콜 정보를 수집
	for _, proto := range p.running {
		protoInfo := interface{}("unknown")
		if query := proto.Protocol.PeerInfo; query != nil {
			if metadata := query(p.ID()); metadata != nil {
				protoInfo = metadata
			} else {
				protoInfo = "handshake"
			}
		}
		info.Protocols[proto.Name] = protoInfo
	}
	return info
}
