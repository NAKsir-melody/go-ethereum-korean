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

	"github.com/ethereum/go-ethereum/p2p/discover"
)

// Protocol represents a P2P subprotocol implementation.
// Protocol은 p2p 서브 프로토콜의 구현을 나타낸다
type Protocol struct {
	// Name should contain the official protocol name,
	// often a three-letter word.
	// 이름은 공식적인 프로토콜 이름으로되어야 하며 주로 3글자 단어이다
	Name string

	// Version should contain the version number of the protocol.
	// version은 프로토콜의 버전을 포함해야한다
	Version uint

	// Length should contain the number of message codes used
	// by the protocol.
	// 길이는 프로토콜에서 사용하는 메시지 코드의 길이이다
	Length uint64

	// Run is called in a new groutine when the protocol has been
	// negotiated with a peer. It should read and write messages from
	// rw. The Payload for each message must be fully consumed.
	//
	// The peer connection is closed when Start returns. It should return
	// any protocol-level error (such as an I/O error) that is
	// encountered.
	// Run은 프로토콜이 피어와 협상하는 동안 새로운 고루틴 안에서 호출된다
	// rw로 부터 메시지를 읽거나 써야한다. 각 메시지의 페이로드는 모두 소비되어야 한다
	// 연결은 Start가 리턴하면 닫힌다. 발생하는 모든에러를 반환해야 한다 
	Run func(peer *Peer, rw MsgReadWriter) error

	// NodeInfo is an optional helper method to retrieve protocol specific metadata
	// about the host node.
	// NodeInfo는 호스트 노드에 대한 프로토콜의 메타데이터를 반환받기위한 핼퍼함수이다
	NodeInfo func() interface{}

	// PeerInfo is an optional helper method to retrieve protocol specific metadata
	// about a certain peer in the network. If an info retrieval function is set,
	// but returns nil, it is assumed that the protocol handshake is still running.
	// PeerInfo는 특정 노드에 대한 프로토콜의 메타데이터를 반환받기위한 핼퍼함수이다. 
	// 만약 반환함수가 설정되고 null이 반환된다면 프로토콜 핸드쉐이킹이 진행중이라고 가정한다
	PeerInfo func(id discover.NodeID) interface{}
}

func (p Protocol) cap() Cap {
	return Cap{p.Name, p.Version}
}

// Cap is the structure of a peer capability.
// Cap은 피어의 능력의 구조체이다
type Cap struct {
	Name    string
	Version uint
}

func (cap Cap) RlpData() interface{} {
	return []interface{}{cap.Name, cap.Version}
}

func (cap Cap) String() string {
	return fmt.Sprintf("%s/%d", cap.Name, cap.Version)
}

type capsByNameAndVersion []Cap

func (cs capsByNameAndVersion) Len() int      { return len(cs) }
func (cs capsByNameAndVersion) Swap(i, j int) { cs[i], cs[j] = cs[j], cs[i] }
func (cs capsByNameAndVersion) Less(i, j int) bool {
	return cs[i].Name < cs[j].Name || (cs[i].Name == cs[j].Name && cs[i].Version < cs[j].Version)
}
