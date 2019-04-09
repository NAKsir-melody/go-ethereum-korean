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

package trie

// Trie keys are dealt with in three distinct encodings:
//
// KEYBYTES encoding contains the actual key and nothing else. This encoding is the
// input to most API functions.
//
// HEX encoding contains one byte for each nibble of the key and an optional trailing
// 'terminator' byte of value 0x10 which indicates whether or not the node at the key
// contains a value. Hex key encoding is used for nodes loaded in memory because it's
// convenient to access.
//
// COMPACT encoding is defined by the Ethereum Yellow Paper (it's called "hex prefix
// encoding" there) and contains the bytes of the key and a flag. The high nibble of the
// first byte contains the flag; the lowest bit encoding the oddness of the length and
// the second-lowest encoding whether the node at the key is a value node. The low nibble
// of the first byte is zero in the case of an even number of nibbles and the first nibble
// in the case of an odd number. All remaining nibbles (now an even number) fit properly
// into the remaining bytes. Compact encoding is used for nodes stored on disk.

// Trie 키는 3가지 인코딩으로 다뤄진다.
// KEYBYTES encoding은 키만을 포함한다. 이 인코딩은 대부분의 api함수의 input이다
// Hex encoding은 키의 각 4bit를 위한 1바이트를 포함하고 
// 키의 해당하는 노드가 값을 가지고 있는지 여부로 종결자 바이트인 0x10을 를 연결한다
// 이 인코딩은 접근하기가 쉽기때문에 메모리에 로드된 노드들을 위해 사용된다
// COMPACT encoding은 (hex-prefix encoding이라) 이더리움 황서에 의해 정의된 것으로
// 키와 플레그의 바이트를 포함한다
// 첫번째 바이트의 높은 4bit는 flag이다. 하위비트는 길이의 홀짝을, 하위 두번째 비트는 키의 노드가 값 노드인지
// 첫 바이트의 하위 4bit는 짝수의 니블이고 첫 니블이 홀수이면 0이다 나머지 니블들은 남은 바이트에 의해 맞춰진다
// compact인코딩은 디스크에 저장하기 위해 사용된다

func hexToCompact(hex []byte) []byte {
	terminator := byte(0)
	if hasTerm(hex) {
		terminator = 1
		hex = hex[:len(hex)-1]
	}
	buf := make([]byte, len(hex)/2+1)
	buf[0] = terminator << 5 // the flag byte
	if len(hex)&1 == 1 {
		buf[0] |= 1 << 4 // odd flag
		buf[0] |= hex[0] // first nibble is contained in the first byte
		hex = hex[1:]
	}
	decodeNibbles(hex, buf[1:])
	return buf
}

func compactToHex(compact []byte) []byte {
	base := keybytesToHex(compact)
	base = base[:len(base)-1]
	// apply terminator flag
	if base[0] >= 2 {
		base = append(base, 16)
	}
	// apply odd flag
	chop := 2 - base[0]&1
	return base[chop:]
}

func keybytesToHex(str []byte) []byte {
	l := len(str)*2 + 1
	var nibbles = make([]byte, l)
	for i, b := range str {
		nibbles[i*2] = b / 16
		nibbles[i*2+1] = b % 16
	}
	nibbles[l-1] = 16
	return nibbles
}

// hexToKeybytes turns hex nibbles into key bytes.
// This can only be used for keys of even length.
// hexToKeybyte 함수는 hex 니블을 키바이트로 바꾼다
// 키가 짝수일때만 가능하다
func hexToKeybytes(hex []byte) []byte {
	if hasTerm(hex) {
		hex = hex[:len(hex)-1]
	}
	if len(hex)&1 != 0 {
		panic("can't convert hex key of odd length")
	}
	key := make([]byte, (len(hex)+1)/2)
	decodeNibbles(hex, key)
	return key
}

func decodeNibbles(nibbles []byte, bytes []byte) {
	for bi, ni := 0, 0; ni < len(nibbles); bi, ni = bi+1, ni+2 {
		bytes[bi] = nibbles[ni]<<4 | nibbles[ni+1]
	}
}

// prefixLen returns the length of the common prefix of a and b.
// prefixLen함수는 a와 b의 공통 접두어의 길이를 반환한다
func prefixLen(a, b []byte) int {
	var i, length = 0, len(a)
	if len(b) < length {
		length = len(b)
	}
	for ; i < length; i++ {
		if a[i] != b[i] {
			break
		}
	}
	return i
}

// hasTerm returns whether a hex key has the terminator flag.
// hasTerm함수는 hex 키가 terminator flag를 가지고 있는지를 리턴한다
func hasTerm(s []byte) bool {
	return len(s) > 0 && s[len(s)-1] == 16
}
