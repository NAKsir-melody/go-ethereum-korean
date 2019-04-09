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

package trie

import (
	"bytes"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

// Prove constructs a merkle proof for key. The result contains all encoded nodes
// on the path to the value at key. The value itself is also included in the last
// node and can be retrieved by verifying the proof.
//
// If the trie does not contain a value for key, the returned proof contains all
// nodes of the longest existing prefix of the key (at least the root node), ending
// with the node that proves the absence of the key.
// prove함수는 키를 위한 머클증명을 생성한다. 결과는 키의 값 경로의 모든 인코딩된 노드들을 포함한다
// 값은 그 자체로 마지막 노드에 포함되며, 증명 검증을 통해 반환될수 있다

// 만약 트라이가 키를 위한 값을 포함하지 않을 경우, 반횐된 검증은 
// 키의 가징 길게 존재하는 접미사의 모든 노드들를 포함한다
// 노드로 끝나는 것은 키의 부재를 증명한다
func (t *Trie) Prove(key []byte, fromLevel uint, proofDb ethdb.Putter) error {
	// Collect all nodes on the path to key.
	key = keybytesToHex(key)
	nodes := []node{}
	tn := t.root
	for len(key) > 0 && tn != nil {
		switch n := tn.(type) {
		case *shortNode:
			if len(key) < len(n.Key) || !bytes.Equal(n.Key, key[:len(n.Key)]) {
				// The trie doesn't contain the key.
				tn = nil
			} else {
				tn = n.Val
				key = key[len(n.Key):]
			}
			nodes = append(nodes, n)
		case *fullNode:
			tn = n.Children[key[0]]
			key = key[1:]
			nodes = append(nodes, n)
		case hashNode:
			var err error
			tn, err = t.resolveHash(n, nil)
			if err != nil {
				log.Error(fmt.Sprintf("Unhandled trie error: %v", err))
				return err
			}
		default:
			panic(fmt.Sprintf("%T: invalid node: %v", tn, tn))
		}
	}
	hasher := newHasher(0, 0, nil)
	for i, n := range nodes {
		// Don't bother checking for errors here since hasher panics
		// if encoding doesn't work and we're not writing to any database.
		n, _, _ = hasher.hashChildren(n, nil)
		hn, _ := hasher.store(n, nil, false)
		if hash, ok := hn.(hashNode); ok || i == 0 {
			// If the node's database encoding is a hash (or is the
			// root node), it becomes a proof element.
			if fromLevel > 0 {
				fromLevel--
			} else {
				enc, _ := rlp.EncodeToBytes(n)
				if !ok {
					hash = crypto.Keccak256(enc)
				}
				proofDb.Put(hash, enc)
			}
		}
	}
	return nil
}

// Prove constructs a merkle proof for key. The result contains all encoded nodes
// on the path to the value at key. The value itself is also included in the last
// node and can be retrieved by verifying the proof.
//
// If the trie does not contain a value for key, the returned proof contains all
// nodes of the longest existing prefix of the key (at least the root node), ending
// with the node that proves the absence of the key.
// prove함수는 키를 위한 머클증명을 생성한다. 결과는 키의 값 경로의 모든 인코딩된 노드들을 포함한다
// 값은 그 자체로 마지막 노드에 포함되며, 증명 검증을 통해 반환될수 있다

// 만약 트라이가 키를 위한 값을 포함하지 않을 경우, 반횐된 검증은 
// 키의 가징 길게 존재하는 접미사의 모든 노드들를 포함한다
// 노드로 끝나는 것은 키의 부재를 증명한다
func (t *SecureTrie) Prove(key []byte, fromLevel uint, proofDb ethdb.Putter) error {
	return t.trie.Prove(key, fromLevel, proofDb)
}

// VerifyProof checks merkle proofs. The given proof must contain the value for
// key in a trie with the given root hash. VerifyProof returns an error if the
// proof contains invalid trie nodes or the wrong value.
// VerifyProof 함수는 머클증명을 검사한다. 
// 주어진 증명은 키를 위한 값이 주어진 루트해시의 trie에 반드시 포함 되어야 한다
// 이함수는 주어진 값이 잘못되었거나, 검증이 잘못된 트라이 노드를 포함할경우 에러를 반환한다
func VerifyProof(rootHash common.Hash, key []byte, proofDb DatabaseReader) (value []byte, err error, nodes int) {
	key = keybytesToHex(key)
	wantHash := rootHash
	for i := 0; ; i++ {
		buf, _ := proofDb.Get(wantHash[:])
		if buf == nil {
			return nil, fmt.Errorf("proof node %d (hash %064x) missing", i, wantHash), i
		}
		n, err := decodeNode(wantHash[:], buf, 0)
		if err != nil {
			return nil, fmt.Errorf("bad proof node %d: %v", i, err), i
		}
		keyrest, cld := get(n, key)
		switch cld := cld.(type) {
		case nil:
			// The trie doesn't contain the key.
			return nil, nil, i
		case hashNode:
			key = keyrest
			copy(wantHash[:], cld)
		case valueNode:
			return cld, nil, i + 1
		}
	}
}

func get(tn node, key []byte) ([]byte, node) {
	for {
		switch n := tn.(type) {
		case *shortNode:
			if len(key) < len(n.Key) || !bytes.Equal(n.Key, key[:len(n.Key)]) {
				return nil, nil
			}
			tn = n.Val
			key = key[len(n.Key):]
		case *fullNode:
			tn = n.Children[key[0]]
			key = key[1:]
		case hashNode:
			return key, n
		case nil:
			return key, nil
		case valueNode:
			return nil, n
		default:
			panic(fmt.Sprintf("%T: invalid node: %v", tn, tn))
		}
	}
}
