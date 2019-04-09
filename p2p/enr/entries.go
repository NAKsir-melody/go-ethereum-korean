// Copyright 2017 The go-ethereum Authors
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

package enr

import (
	"crypto/ecdsa"
	"fmt"
	"io"
	"net"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

// Entry is implemented by known node record entry types.
//
// To define a new entry that is to be included in a node record,
// create a Go type that satisfies this interface. The type should
// also implement rlp.Decoder if additional checks are needed on the value.
// Entry는 알려진 노드 기록 리스트 타입을 구현한다
// 노드 기록상에 포함될 좋은 엔트리를 정의하기 위해서
// 이 인터페이스를 만족시키는 고 타입을 생성한다
// 타입은 값의 겁증이 추가적으로 필요한 경우 rlp.Decoder를 함께 구현해야 한다
type Entry interface {
	ENRKey() string
}

type generic struct {
	key   string
	value interface{}
}

func (g generic) ENRKey() string { return g.key }

func (g generic) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, g.value)
}

func (g *generic) DecodeRLP(s *rlp.Stream) error {
	return s.Decode(g.value)
}

// WithEntry wraps any value with a key name. It can be used to set and load arbitrary values
// in a record. The value v must be supported by rlp. To use WithEntry with Load, the value
// must be a pointer.
// WithEntry 함수는 키이름과 함께 값을 포장한다. 이 함수는 기록상의 명확하지 않은 데이터를
// 읽거나 쓸때 사용가능하다. 값은 rlp를 지원해야 한다
// 이함수를 load에서 사용하기 위해서는 값은 포인터여야한다
func WithEntry(k string, v interface{}) Entry {
	return &generic{key: k, value: v}
}

// TCP is the "tcp" key, which holds the TCP port of the node.
// TCP는 노드의 TCP 포트를 담고있는 tcp key이다
type TCP uint16

func (v TCP) ENRKey() string { return "tcp" }

// UDP is the "udp" key, which holds the UDP port of the node.
// UDP는 노드의 UDP포트를 담고있는 udp key이다k
type UDP uint16

func (v UDP) ENRKey() string { return "udp" }

// ID is the "id" key, which holds the name of the identity scheme.
// ID는 신원 제도의 이름을 담고있는 id키이다
type ID string

const IDv4 = ID("v4") // the default identity scheme
// 기본 신원정책

func (v ID) ENRKey() string { return "id" }

// IP is the "ip" key, which holds the IP address of the node.
// IP는 노드의 ip주소를 담고있는 ip키이다
type IP net.IP

func (v IP) ENRKey() string { return "ip" }

// EncodeRLP implements rlp.Encoder.
// EncodeRLP는 rlp.Encoder를 구현한다
func (v IP) EncodeRLP(w io.Writer) error {
	if ip4 := net.IP(v).To4(); ip4 != nil {
		return rlp.Encode(w, ip4)
	}
	return rlp.Encode(w, net.IP(v))
}

// DecodeRLP implements rlp.Decoder.
// DecodeRLP는 rlp.Decoder를 구현한다
func (v *IP) DecodeRLP(s *rlp.Stream) error {
	if err := s.Decode((*net.IP)(v)); err != nil {
		return err
	}
	if len(*v) != 4 && len(*v) != 16 {
		return fmt.Errorf("invalid IP address, want 4 or 16 bytes: %v", *v)
	}
	return nil
}

// Secp256k1 is the "secp256k1" key, which holds a public key.
// Secp256k1는 공개키를 저장하는 secp256k1키이다
type Secp256k1 ecdsa.PublicKey

func (v Secp256k1) ENRKey() string { return "secp256k1" }

// EncodeRLP implements rlp.Encoder.
// EncodeRLP는 rlp.Encoder를 구현한다
func (v Secp256k1) EncodeRLP(w io.Writer) error {
	return rlp.Encode(w, crypto.CompressPubkey((*ecdsa.PublicKey)(&v)))
}

// DecodeRLP implements rlp.Decoder.
// DecodeRLP는 rlp.Decoder를 구현한다
func (v *Secp256k1) DecodeRLP(s *rlp.Stream) error {
	buf, err := s.Bytes()
	if err != nil {
		return err
	}
	pk, err := crypto.DecompressPubkey(buf)
	if err != nil {
		return err
	}
	*v = (Secp256k1)(*pk)
	return nil
}

// KeyError is an error related to a key.
// KeyError는 키와 관련된 에러이다
type KeyError struct {
	Key string
	Err error
}

// Error implements error.
// Error함수는 error를 구현한다
func (err *KeyError) Error() string {
	if err.Err == errNotFound {
		return fmt.Sprintf("missing ENR key %q", err.Key)
	}
	return fmt.Sprintf("ENR key %q: %v", err.Key, err.Err)
}

// IsNotFound reports whether the given error means that a key/value pair is
// missing from a record.
// IsNotFound 함수는 주어진 에러가 키값쌍이 레코드상에 없는지 여부를 보고한다
func IsNotFound(err error) bool {
	kerr, ok := err.(*KeyError)
	return ok && kerr.Err == errNotFound
}
