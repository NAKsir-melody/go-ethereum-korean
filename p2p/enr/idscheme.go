// Copyright 2018 The go-ethereum Authors
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
	"sync"

	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/sha3"
	"github.com/ethereum/go-ethereum/rlp"
)

// Registry of known identity schemes.
// 알려진 신원정책의 보관소
var schemes sync.Map

// An IdentityScheme is capable of verifying record signatures and
// deriving node addresses.
// IdentityScheme 인터페이스는 기록의 서명을 검증하고 노드 주소를 유도할수 있다
type IdentityScheme interface {
	Verify(r *Record, sig []byte) error
	NodeAddr(r *Record) []byte
}

// RegisterIdentityScheme adds an identity scheme to the global registry.
// RegisterIdentityScheme함수는 전체 보관소에 신원정책을 추가한다
func RegisterIdentityScheme(name string, scheme IdentityScheme) {
	if _, loaded := schemes.LoadOrStore(name, scheme); loaded {
		panic("identity scheme " + name + " already registered")
	}
}

// FindIdentityScheme resolves name to an identity scheme in the global registry.
// FindIdentityScheme함수는 전체 보관소의 신원확인 정책의 이름을 찾는다
func FindIdentityScheme(name string) IdentityScheme {
	s, ok := schemes.Load(name)
	if !ok {
		return nil
	}
	return s.(IdentityScheme)
}

// v4ID is the "v4" identity scheme.
// V4ID 타입은 v4신원정책이다
type v4ID struct{}

func init() {
	RegisterIdentityScheme("v4", v4ID{})
}

// SignV4 signs a record using the v4 scheme.
// SignV4함수는 v4정책으로 기록에 서명한다
func SignV4(r *Record, privkey *ecdsa.PrivateKey) error {
	// Copy r to avoid modifying it if signing fails.
	// 서명이 실패했을때 수정을 회피하기 위해 r을 복사한다
	cpy := *r
	cpy.Set(ID("v4"))
	cpy.Set(Secp256k1(privkey.PublicKey))

	h := sha3.NewKeccak256()
	rlp.Encode(h, cpy.AppendElements(nil))
	sig, err := crypto.Sign(h.Sum(nil), privkey)
	if err != nil {
		return err
	}
	sig = sig[:len(sig)-1] // remove v
	if err = cpy.SetSig("v4", sig); err == nil {
		*r = cpy
	}
	return err
}

// s256raw is an unparsed secp256k1 public key entry.
// s256raw 타입은 파싱되지 않은 secp256k1의 공개 키리스트이다
type s256raw []byte

func (s256raw) ENRKey() string { return "secp256k1" }

func (v4ID) Verify(r *Record, sig []byte) error {
	var entry s256raw
	if err := r.Load(&entry); err != nil {
		return err
	} else if len(entry) != 33 {
		return fmt.Errorf("invalid public key")
	}

	h := sha3.NewKeccak256()
	rlp.Encode(h, r.AppendElements(nil))
	if !crypto.VerifySignature(entry, h.Sum(nil), sig) {
		return errInvalidSig
	}
	return nil
}

func (v4ID) NodeAddr(r *Record) []byte {
	var pubkey Secp256k1
	err := r.Load(&pubkey)
	if err != nil {
		return nil
	}
	buf := make([]byte, 64)
	math.ReadBits(pubkey.X, buf[:32])
	math.ReadBits(pubkey.Y, buf[32:])
	return crypto.Keccak256(buf)
}
