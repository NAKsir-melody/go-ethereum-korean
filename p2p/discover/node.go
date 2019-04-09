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
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
)

const NodeIDBits = 512

// Node represents a host on the network.
// The fields of Node may not be modified.
// Node 구조체는 네트워크상의 하나의 호스트를 나타낸다
// 노드 구조체의 필드는 대부분 수정불가능하다
type Node struct {
	IP       net.IP // len 4 for IPv4 or 16 for IPv6
	UDP, TCP uint16 // port numbers
	ID       NodeID // the node's public key
	// 노드의 IP
	// 포트넘버
	// 노드의 공개키

	// This is a cached copy of sha3(ID) which is used for node
	// distance calculations. This is part of Node in order to make it
	// possible to write tests that need a node at a certain distance.
	// In those tests, the content of sha will not actually correspond
	// with ID.
	// 캐싱된 sha3 복사값은 노드의 거리를 계산할때 사용된다
	// 이값은 노드의 일부로서 특정거리의 노드가 필요한 테스트를 설계할때 사용가능하다
	// 그런 테스트에서는 sha의 내용이 실제 ID와 관련있지 않다.
	sha common.Hash

	// Time when the node was added to the table.
	// 노드가 테이블에 추가된 시간
	addedAt time.Time
}

// NewNode creates a new node. It is mostly meant to be used for
// testing purposes.
// newNode 함수는 새로운 노드를 생성한다. 대부분의 경우 테스트 목적으로 사용된다
func NewNode(id NodeID, ip net.IP, udpPort, tcpPort uint16) *Node {
	if ipv4 := ip.To4(); ipv4 != nil {
		ip = ipv4
	}
	return &Node{
		IP:  ip,
		UDP: udpPort,
		TCP: tcpPort,
		ID:  id,
		sha: crypto.Keccak256Hash(id[:]),
	}
}

func (n *Node) addr() *net.UDPAddr {
	return &net.UDPAddr{IP: n.IP, Port: int(n.UDP)}
}

// Incomplete returns true for nodes with no IP address.
// Incomplete 함수는 IP주소가 없는 노드일때 참을 반환한다 
func (n *Node) Incomplete() bool {
	return n.IP == nil
}

// checks whether n is a valid complete node.
// n이 유효한 노드인지 반환한다
func (n *Node) validateComplete() error {
	if n.Incomplete() {
		return errors.New("incomplete node")
	}
	if n.UDP == 0 {
		return errors.New("missing UDP port")
	}
	if n.TCP == 0 {
		return errors.New("missing TCP port")
	}
	if n.IP.IsMulticast() || n.IP.IsUnspecified() {
		return errors.New("invalid IP (multicast/unspecified)")
	}
	_, err := n.ID.Pubkey() // validate the key (on curve, etc.)
	// 키를 검증한다
	return err
}

// The string representation of a Node is a URL.
// Please see ParseNode for a description of the format.
// 노드의 문자열 표현인 URL
// 포멧의 표현식을 위한 parseNode를 참조
func (n *Node) String() string {
	u := url.URL{Scheme: "enode"}
	if n.Incomplete() {
		u.Host = fmt.Sprintf("%x", n.ID[:])
	} else {
		addr := net.TCPAddr{IP: n.IP, Port: int(n.TCP)}
		u.User = url.User(fmt.Sprintf("%x", n.ID[:]))
		u.Host = addr.String()
		if n.UDP != n.TCP {
			u.RawQuery = "discport=" + strconv.Itoa(int(n.UDP))
		}
	}
	return u.String()
}

var incompleteNodeURL = regexp.MustCompile("(?i)^(?:enode://)?([0-9a-f]+)$")

// ParseNode parses a node designator.
//
// There are two basic forms of node designators
//   - incomplete nodes, which only have the public key (node ID)
//   - complete nodes, which contain the public key and IP/Port information
//
// For incomplete nodes, the designator must look like one of these
//
//    enode://<hex node id>
//    <hex node id>
//
// For complete nodes, the node ID is encoded in the username portion
// of the URL, separated from the host by an @ sign. The hostname can
// only be given as an IP address, DNS domain names are not allowed.
// The port in the host name section is the TCP listening port. If the
// TCP and UDP (discovery) ports differ, the UDP port is specified as
// query parameter "discport".
//
// In the following example, the node URL describes
// a node with IP address 10.3.58.6, TCP listening port 30303
// and UDP discovery port 30301.
//
//    enode://<hex node id>@10.3.58.6:30303?discport=30301
// ParseNode함수는 노드의 지시자를 분석한다
// 두종류의 노드 지시자 형태가 있음
// - 퍼블릭 키만 가진 완벽하지 않은 노드
// - 퍼블릭 키와 IP/Port 정보를 포함하는 완벽한노드
// 완벽하지 않은 노드의 경우 지시자는 둘중 하나의 형태로 표현된다
//    enode://<hex node id>
//    <hex node id>
// 완벽한 노드의 경우, 노드ID는 url의 유저이름 위치에 인코딩되며
// 호스트와는 @sign으로 분리된다. 호스트 이름은 IP주소로만 허용되며 
// 도메인 이름은 허용되지 않는다
// 호스트 이름의 포트는 TCP 수신 포트이다
// 만약 tcp와 udp포트가 다를 경우, udp port는쿼리인자인 discport로 정의된다 
//    enode://<hex node id>@10.3.58.6:30303?discport=30301

func ParseNode(rawurl string) (*Node, error) {
	if m := incompleteNodeURL.FindStringSubmatch(rawurl); m != nil {
		id, err := HexID(m[1])
		if err != nil {
			return nil, fmt.Errorf("invalid node ID (%v)", err)
		}
		return NewNode(id, nil, 0, 0), nil
	}
	return parseComplete(rawurl)
}

func parseComplete(rawurl string) (*Node, error) {
	var (
		id               NodeID
		ip               net.IP
		tcpPort, udpPort uint64
	)
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "enode" {
		return nil, errors.New("invalid URL scheme, want \"enode\"")
	}
	// Parse the Node ID from the user portion.
	// user위치의 노드아이디를 파싱한다
	if u.User == nil {
		return nil, errors.New("does not contain node ID")
	}
	if id, err = HexID(u.User.String()); err != nil {
		return nil, fmt.Errorf("invalid node ID (%v)", err)
	}
	// Parse the IP address.
	// ip주소를 파싱한다
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid host: %v", err)
	}
	if ip = net.ParseIP(host); ip == nil {
		return nil, errors.New("invalid IP address")
	}
	// Ensure the IP is 4 bytes long for IPv4 addresses.
	// ip4 adress인 4byte임을 확인한다
	if ipv4 := ip.To4(); ipv4 != nil {
		ip = ipv4
	}
	// Parse the port numbers.
	// 포트를 파싱한다
	if tcpPort, err = strconv.ParseUint(port, 10, 16); err != nil {
		return nil, errors.New("invalid port")
	}
	udpPort = tcpPort
	qv := u.Query()
	if qv.Get("discport") != "" {
		udpPort, err = strconv.ParseUint(qv.Get("discport"), 10, 16)
		if err != nil {
			return nil, errors.New("invalid discport in query")
		}
	}
	return NewNode(id, ip, uint16(udpPort), uint16(tcpPort)), nil
}

// MustParseNode parses a node URL. It panics if the URL is not valid.
// MustParseNode 함수는 node URL을 파싱한다. 
// url이 유효하지 않을 경우 패닉이 발생한다 
func MustParseNode(rawurl string) *Node {
	n, err := ParseNode(rawurl)
	if err != nil {
		panic("invalid node URL: " + err.Error())
	}
	return n
}

// MarshalText implements encoding.TextMarshaler.
// MarshalText는 encoding.TextMarshaler를 구현한다
func (n *Node) MarshalText() ([]byte, error) {
	return []byte(n.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
// UnmarshalText는 encoding.TextUnmarshaler를 구현한다
func (n *Node) UnmarshalText(text []byte) error {
	dec, err := ParseNode(string(text))
	if err == nil {
		*n = *dec
	}
	return err
}

// NodeID is a unique identifier for each node.
// The node identifier is a marshaled elliptic curve public key.
// NodeID는 각 노드의 단일한 구분자이다
// 노드 ID는 변환된 타원형 곡선 공개키이다
type NodeID [NodeIDBits / 8]byte

// Bytes returns a byte slice representation of the NodeID
// Bytes 함수는 노드 ID의 바이트 표현을 반환한다
func (n NodeID) Bytes() []byte {
	return n[:]
}

// NodeID prints as a long hexadecimal number.
// NodeID 함수는 긴 헥사 숫자를 프린트한다
func (n NodeID) String() string {
	return fmt.Sprintf("%x", n[:])
}

// The Go syntax representation of a NodeID is a call to HexID.
// GoString 함수는 hexID를 호출하기 위한 node id의 고표현을 호출한다
func (n NodeID) GoString() string {
	return fmt.Sprintf("discover.HexID(\"%x\")", n[:])
}

// TerminalString returns a shortened hex string for terminal logging.
// TerminalString함수는 터미널 로깅을 위한 짧은 헥사 문자열을 반환한다
func (n NodeID) TerminalString() string {
	return hex.EncodeToString(n[:8])
}

// MarshalText implements the encoding.TextMarshaler interface.
// MarshalText함수는 encoding.TextMarshaler interface를 구현한다
func (n NodeID) MarshalText() ([]byte, error) {
	return []byte(hex.EncodeToString(n[:])), nil
}

// UnmarshalText implements the encoding.TextUnmarshaler interface.
// UnmarshalText는 encoding.TextUnmarshaler interface를 구현한다
func (n *NodeID) UnmarshalText(text []byte) error {
	id, err := HexID(string(text))
	if err != nil {
		return err
	}
	*n = id
	return nil
}

// BytesID converts a byte slice to a NodeID
// BytesID는 byte들을 nodeID로 변환한다
func BytesID(b []byte) (NodeID, error) {
	var id NodeID
	if len(b) != len(id) {
		return id, fmt.Errorf("wrong length, want %d bytes", len(id))
	}
	copy(id[:], b)
	return id, nil
}

// MustBytesID converts a byte slice to a NodeID.
// It panics if the byte slice is not a valid NodeID.
// MustBytesID는 byte들을 nodeID로 변환한다
// 바이트 슬라이스가 유효한 noid가 아닐경우 panic이 발생한다
func MustBytesID(b []byte) NodeID {
	id, err := BytesID(b)
	if err != nil {
		panic(err)
	}
	return id
}

// HexID converts a hex string to a NodeID.
// The string may be prefixed with 0x.
// HexID는 헥사 문자열을 noID로 변환한다
// 문자열은 0x prefix가 붙는다
func HexID(in string) (NodeID, error) {
	var id NodeID
	b, err := hex.DecodeString(strings.TrimPrefix(in, "0x"))
	if err != nil {
		return id, err
	} else if len(b) != len(id) {
		return id, fmt.Errorf("wrong length, want %d hex chars", len(id)*2)
	}
	copy(id[:], b)
	return id, nil
}

// MustHexID converts a hex string to a NodeID.
// It panics if the string is not a valid NodeID.
// MustHexID는 헥사 문자열을 노드ID로 변환한다
// 문자열이 유효한 noid가 아닐경우 panic이 발생한다
func MustHexID(in string) NodeID {
	id, err := HexID(in)
	if err != nil {
		panic(err)
	}
	return id
}

// PubkeyID returns a marshaled representation of the given public key.
// PubkeyID는 주어진 퍼블릭키의 변환된 표현을 반환한다
func PubkeyID(pub *ecdsa.PublicKey) NodeID {
	var id NodeID
	pbytes := elliptic.Marshal(pub.Curve, pub.X, pub.Y)
	if len(pbytes)-1 != len(id) {
		panic(fmt.Errorf("need %d bit pubkey, got %d bits", (len(id)+1)*8, len(pbytes)))
	}
	copy(id[:], pbytes[1:])
	return id
}

// Pubkey returns the public key represented by the node ID.
// It returns an error if the ID is not a point on the curve.
// PubKey함수는 노드 아이디에 의해 나타내지는 공통키를 반환한다
// 이함수는 ID가 커브상의 포인트가 아니라면 에러를 반환한다
func (id NodeID) Pubkey() (*ecdsa.PublicKey, error) {
	p := &ecdsa.PublicKey{Curve: crypto.S256(), X: new(big.Int), Y: new(big.Int)}
	half := len(id) / 2
	p.X.SetBytes(id[:half])
	p.Y.SetBytes(id[half:])
	if !p.Curve.IsOnCurve(p.X, p.Y) {
		return nil, errors.New("id is invalid secp256k1 curve point")
	}
	return p, nil
}

// recoverNodeID computes the public key used to sign the
// given hash from the signature.
// recoverNodeID는 서명으로부터 주어진 해시에 대한 서명에 사용된
// 공통키를 연산한다
func recoverNodeID(hash, sig []byte) (id NodeID, err error) {
	pubkey, err := secp256k1.RecoverPubkey(hash, sig)
	if err != nil {
		return id, err
	}
	if len(pubkey)-1 != len(id) {
		return id, fmt.Errorf("recovered pubkey has %d bits, want %d bits", len(pubkey)*8, (len(id)+1)*8)
	}
	for i := range id {
		id[i] = pubkey[i+1]
	}
	return id, nil
}

// distcmp compares the distances a->target and b->target.
// Returns -1 if a is closer to target, 1 if b is closer to target
// and 0 if they are equal.
// distcmp 함수는 a와b의 타겟까지의 거리를 비교한다
// a가 타겟에 더 가깝다면 -1을 반환하고, b가 더 가깝다면 1을 반환한다
// 0의 경우 거리는 같다
func distcmp(target, a, b common.Hash) int {
	for i := range target {
		da := a[i] ^ target[i]
		db := b[i] ^ target[i]
		if da > db {
			return 1
		} else if da < db {
			return -1
		}
	}
	return 0
}

// table of leading zero counts for bytes [0..255]
var lzcount = [256]int{
	8, 7, 6, 6, 5, 5, 5, 5,
	4, 4, 4, 4, 4, 4, 4, 4,
	3, 3, 3, 3, 3, 3, 3, 3,
	3, 3, 3, 3, 3, 3, 3, 3,
	2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2,
	2, 2, 2, 2, 2, 2, 2, 2,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 1, 1, 1, 1, 1, 1,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0,
}

// logdist returns the logarithmic distance between a and b, log2(a ^ b).
// logdist 함수는 a와 b의 로그거리를 반환한다
func logdist(a, b common.Hash) int {
	lz := 0
	for i := range a {
		x := a[i] ^ b[i]
		if x == 0 {
			lz += 8
		} else {
			lz += lzcount[x]
			break
		}
	}
	return len(a)*8 - lz
}

// hashAtDistance returns a random hash such that logdist(a, b) == n
// hashAtDistance 함수는 log거리가 n인 random 해시를 반환한다
func hashAtDistance(a common.Hash, n int) (b common.Hash) {
	if n == 0 {
		return a
	}
	// flip bit at position n, fill the rest with random bits
	// n위치의 비트를 뒤집고, 남은것을 랜덤비트로 채운다
	b = a
	pos := len(a) - n/8 - 1
	bit := byte(0x01) << (byte(n%8) - 1)
	if bit == 0 {
		pos++
		bit = 0x80
	}
	b[pos] = a[pos]&^bit | ^a[pos]&bit // TODO: randomize end bits
	for i := pos + 1; i < len(a); i++ {
		b[i] = byte(rand.Intn(255))
	}
	return b
}
