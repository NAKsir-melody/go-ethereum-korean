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

// Package enr implements Ethereum Node Records as defined in EIP-778. A node record holds
// arbitrary information about a node on the peer-to-peer network.
//
// Records contain named keys. To store and retrieve key/values in a record, use the Entry
// interface.
//
// Records must be signed before transmitting them to another node. Decoding a record verifies
// its signature. When creating a record, set the entries you want, then call Sign to add the
// signature. Modifying a record invalidates the signature.
//
// Package enr supports the "secp256k1-keccak" identity scheme.
// enr패키지는  EIP-778에 정의된대로 이더리움 노드를 기록한다
// 노드의 기록은 p2p 네트워크상의 노드에 대한 무형의 정보를 가진다
// 기록은 이름을 가진 키들을 포함한다. 키/값 쌍을 찾고 반환하기 위해서 
// 엔트리 인터페이스를 사용한다
// 기록들은 다른 노드로 전송되기 전에 서명되어야 한다
// 기록의 디코딩은 서명을 검증한다. 기록을 생성할때 엔트리를 설정하고
// Sign함수를 호출하여 서명한다. 레코드를 수정하면 서명은 무효화된다
// enr패키지는 secp256k1-keccak 신원 확인 제도를 지원한다
package enr

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/ethereum/go-ethereum/rlp"
)

const SizeLimit = 300 // maximum encoded size of a node record in bytes
// 노드가 바이트로 레코딩될 최대 인코딩된 크기

var (
	errNoID           = errors.New("unknown or unspecified identity scheme")
	errInvalidSig     = errors.New("invalid signature")
	errNotSorted      = errors.New("record key/value pairs are not sorted by key")
	errDuplicateKey   = errors.New("record contains duplicate key")
	errIncompletePair = errors.New("record contains incomplete k/v pair")
	errTooBig         = fmt.Errorf("record bigger than %d bytes", SizeLimit)
	errEncodeUnsigned = errors.New("can't encode unsigned record")
	errNotFound       = errors.New("no such key in record")
)

// Record represents a node record. The zero value is an empty record.
// Record 구조체는 노드의 기록을 나타낸다. 0은 빈 레코드이다
type Record struct {
	seq       uint64 // sequence number
	signature []byte // the signature
	raw       []byte // RLP encoded record
	pairs     []pair // sorted list of all key/value pairs
}

// pair is a key/value pair in a record.
// pair는 레코드 상의 키/값 쌍이다.
type pair struct {
	k string
	v rlp.RawValue
}

// Signed reports whether the record has a valid signature.
// Signed 함수는 레코드가 유효한 서명을 가지고있는지를 보고한다
func (r *Record) Signed() bool {
	return r.signature != nil
}

// Seq returns the sequence number.
// seq함수는 시퀀스 넘버를 반환한다.
func (r *Record) Seq() uint64 {
	return r.seq
}

// SetSeq updates the record sequence number. This invalidates any signature on the record.
// Calling SetSeq is usually not required because setting any key in a signed record
// increments the sequence number.
// SetSeq함수는 기록의 시퀀스 넘버를 갱신한다. 레코드의 모든 서명을 무효화한다
// 때때로 서명된 레코드에 키를 설정할 경우 시퀀스 넘버가 증가하기 때문에, 호출이 대부분 필요하지 않다
func (r *Record) SetSeq(s uint64) {
	r.signature = nil
	r.raw = nil
	r.seq = s
}

// Load retrieves the value of a key/value pair. The given Entry must be a pointer and will
// be set to the value of the entry in the record.
//
// Errors returned by Load are wrapped in KeyError. You can distinguish decoding errors
// from missing keys using the IsNotFound function.
// Load 함수는 키/값 쌍의 값을 반환한다. 주어진 목록은 포인터여야 하며 
// 레코드상의 목록 값에 설정될것이다
// 함수의 반환된 에러는 keyerror로 포장된다. IsNotFiound함수를 이용하여 
// 읽어버린 키로부터 에러를 디코딩할수 있다
func (r *Record) Load(e Entry) error {
	i := sort.Search(len(r.pairs), func(i int) bool { return r.pairs[i].k >= e.ENRKey() })
	if i < len(r.pairs) && r.pairs[i].k == e.ENRKey() {
		if err := rlp.DecodeBytes(r.pairs[i].v, e); err != nil {
			return &KeyError{Key: e.ENRKey(), Err: err}
		}
		return nil
	}
	return &KeyError{Key: e.ENRKey(), Err: errNotFound}
}

// Set adds or updates the given entry in the record. It panics if the value can't be
// encoded. If the record is signed, Set increments the sequence number and invalidates
// the sequence number.
// Set함수는 주어진 목록을 기록상에 추가하거나 갱신한다. 값이 인코딩될수 없다면 패닉이 발생한다
// 레코드가 서명되었을 경우 시퀀스 번호를 증가시키고 시퀀스 번호를 무효화한다
func (r *Record) Set(e Entry) {
	blob, err := rlp.EncodeToBytes(e)
	if err != nil {
		panic(fmt.Errorf("enr: can't encode %s: %v", e.ENRKey(), err))
	}
	r.invalidate()

	pairs := make([]pair, len(r.pairs))
	copy(pairs, r.pairs)
	i := sort.Search(len(pairs), func(i int) bool { return pairs[i].k >= e.ENRKey() })
	switch {
	case i < len(pairs) && pairs[i].k == e.ENRKey():
		// element is present at r.pairs[i]
		// 원소가 r.pairs[i]위치에 존재함
		pairs[i].v = blob
	case i < len(r.pairs):
		// insert pair before i-th elem
		// 패어를 i번째 원소 앞에 삽입
		el := pair{e.ENRKey(), blob}
		pairs = append(pairs, pair{})
		copy(pairs[i+1:], pairs[i:])
		pairs[i] = el
	default:
		// element should be placed at the end of r.pairs
		// 원소가 r.pairs의 맨끝에 위치해야한다
		pairs = append(pairs, pair{e.ENRKey(), blob})
	}
	r.pairs = pairs
}

func (r *Record) invalidate() {
	if r.signature == nil {
		r.seq++
	}
	r.signature = nil
	r.raw = nil
}

// EncodeRLP implements rlp.Encoder. Encoding fails if
// the record is unsigned.
// EncodeRLP함수는 rlp.Encoder를 구현한다. 
// 레코드가 서명되지 않았다면 인코딩은 실패한다
func (r Record) EncodeRLP(w io.Writer) error {
	if !r.Signed() {
		return errEncodeUnsigned
	}
	_, err := w.Write(r.raw)
	return err
}

// DecodeRLP implements rlp.Decoder. Decoding verifies the signature.
// DecodeRLP함수는 rlp.Decoder를 구현한다 Decoding함수는 서명을 검증한다
func (r *Record) DecodeRLP(s *rlp.Stream) error {
	raw, err := s.Raw()
	if err != nil {
		return err
	}
	if len(raw) > SizeLimit {
		return errTooBig
	}

	// Decode the RLP container.
	// rlp컨테이너를 해독한다
	dec := Record{raw: raw}
	s = rlp.NewStream(bytes.NewReader(raw), 0)
	if _, err := s.List(); err != nil {
		return err
	}
	if err = s.Decode(&dec.signature); err != nil {
		return err
	}
	if err = s.Decode(&dec.seq); err != nil {
		return err
	}
	// The rest of the record contains sorted k/v pairs.
	// 남은 기록은 정렬된 키/값 쌍을 포함한다
	var prevkey string
	for i := 0; ; i++ {
		var kv pair
		if err := s.Decode(&kv.k); err != nil {
			if err == rlp.EOL {
				break
			}
			return err
		}
		if err := s.Decode(&kv.v); err != nil {
			if err == rlp.EOL {
				return errIncompletePair
			}
			return err
		}
		if i > 0 {
			if kv.k == prevkey {
				return errDuplicateKey
			}
			if kv.k < prevkey {
				return errNotSorted
			}
		}
		dec.pairs = append(dec.pairs, kv)
		prevkey = kv.k
	}
	if err := s.ListEnd(); err != nil {
		return err
	}

	_, scheme := dec.idScheme()
	if scheme == nil {
		return errNoID
	}
	if err := scheme.Verify(&dec, dec.signature); err != nil {
		return err
	}
	*r = dec
	return nil
}

// NodeAddr returns the node address. The return value will be nil if the record is
// unsigned or uses an unknown identity scheme.
// NodeAddr함수는 노드의 주소를 반환한다. 기록이 서명되지 않았거나,
// 알려지지 않은 신원확인 방법을 사용할 경우 반환값은 nil이다
func (r *Record) NodeAddr() []byte {
	_, scheme := r.idScheme()
	if scheme == nil {
		return nil
	}
	return scheme.NodeAddr(r)
}

// SetSig sets the record signature. It returns an error if the encoded record is larger
// than the size limit or if the signature is invalid according to the passed scheme.
// Setsig함수는 기록에 서명한다. 이함수는 인코딩된 결과가 사이즈 리밋을 넘거나 
// 서명이 올바르지 않을때 에러를 리턴한다
func (r *Record) SetSig(idscheme string, sig []byte) error {
	// Check that "id" is set and matches the given scheme. This panics because
	// inconsitencies here are always implementation bugs in the signing function calling
	// this method.
	// id가 설정되었고 주어진 방식에 적합한지 확인한다
	// 이부분이 완결하지 않다면 서명 함수에 언재나 구현 에러가 있는것이기 때문에 패닉이 발생한다
	id, s := r.idScheme()
	if s == nil {
		panic(errNoID)
	}
	if id != idscheme {
		panic(fmt.Errorf("identity scheme mismatch in Sign: record has %s, want %s", id, idscheme))
	}

	// Verify against the scheme.
	// 방식에대해 검증
	if err := s.Verify(r, sig); err != nil {
		return err
	}
	raw, err := r.encode(sig)
	if err != nil {
		return err
	}
	r.signature, r.raw = sig, raw
	return nil
}

// AppendElements appends the sequence number and entries to the given slice.
// AppendElemets함수는 시퀀스 번호와 리스트를 주어진 조각에 추가한다
func (r *Record) AppendElements(list []interface{}) []interface{} {
	list = append(list, r.seq)
	for _, p := range r.pairs {
		list = append(list, p.k, p.v)
	}
	return list
}

func (r *Record) encode(sig []byte) (raw []byte, err error) {
	list := make([]interface{}, 1, 2*len(r.pairs)+1)
	list[0] = sig
	list = r.AppendElements(list)
	if raw, err = rlp.EncodeToBytes(list); err != nil {
		return nil, err
	}
	if len(raw) > SizeLimit {
		return nil, errTooBig
	}
	return raw, nil
}

func (r *Record) idScheme() (string, IdentityScheme) {
	var id ID
	if err := r.Load(&id); err != nil {
		return "", nil
	}
	return string(id), FindIdentityScheme(string(id))

