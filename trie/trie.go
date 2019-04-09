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

// Package trie implements Merkle Patricia Tries.
// trie 패키지는 머클 패트리샤 트라이를 구현한다
package trie

import (
	"bytes"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	// emptyRoot is the known root hash of an empty trie.
	// emptyRoot는 비어있는 트라이의 루트 해시 초기값
	emptyRoot = common.HexToHash("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

	// emptyState is the known hash of an empty state trie entry.
	// emptyState는 비어있는 상태 트라이명부의 초기값
	emptyState = crypto.Keccak256Hash(nil)
)

var (
	cacheMissCounter   = metrics.NewRegisteredCounter("trie/cachemiss", nil)
	cacheUnloadCounter = metrics.NewRegisteredCounter("trie/cacheunload", nil)
)

// CacheMisses retrieves a global counter measuring the number of cache misses
// the trie had since process startup. This isn't useful for anything apart from
// trie debugging purposes.
// CacheMisses 함수는 트라이가 처리되기 시작했을때부터 발생한 캐시 미스 횟수를 반환한다
// 이 함수는 트라이 디버깅 목적 이외에는 유용성이 없다
func CacheMisses() int64 {
	return cacheMissCounter.Count()
}

// CacheUnloads retrieves a global counter measuring the number of cache unloads
// the trie did since process startup. This isn't useful for anything apart from
// trie debugging purposes.
// CacheUnloads 함수는 트라이가 처리되기 시작했을때부터 발생한 캐시 언로드 횟수를 반환한다
// 이 함수는 트라이 디버깅 목적 이외에는 유용성이 없다
func CacheUnloads() int64 {
	return cacheUnloadCounter.Count()
}

// LeafCallback is a callback type invoked when a trie operation reaches a leaf
// node. It's used by state sync and commit to allow handling external references
// between account and storage tries.
// leafCallback은 트라이 오퍼레이션이 리프노드에 도달했을때 불리는 콜백타입이다.
// 이 타입은 계정과 스토리지 트라이 사이의 외부 참조를 허용하기 위해 state sync나 commit에 사용된다

type LeafCallback func(leaf []byte, parent common.Hash) error

// Trie is a Merkle Patricia Trie.
// The zero value is an empty trie with no database.
// Use New to create a trie that sits on top of a database.
//
// Trie is not safe for concurrent use.
// Trie 구조체는 머클 패트리샤 트라이이다. 값이 0일때는 DB에 존재하지 않는 빈 트라이이다
// New함수를 사용하여 DB상에 트라이를 생성한다
// thread safe하지 않다
type Trie struct {
	db           *Database
	root         node
	originalRoot common.Hash

	// Cache generation values.
	// cachegen increases by one with each commit operation.
	// new nodes are tagged with the current generation and unloaded
	// when their generation is older than than cachegen-cachelimit.
	// 캐시 생성 값
	// cachegen 변수는 매 commit동작마다 1씩 증가한다
	// 새로운 노드들은 현재 세대에 태그되며, 그들의 세대가 cachegen-cachelimit보다 오래되었을 경우
	// unload된다
	cachegen, cachelimit uint16
}

// SetCacheLimit sets the number of 'cache generations' to keep.
// A cache generation is created by a call to Commit.
// SetCacheLimit 함수는 캐시 세대의 숫자를 저장하기 위해 사용된다
// 캐시세대는 commit함수를 호출함에 의해 생성된다
func (t *Trie) SetCacheLimit(l uint16) {
	t.cachelimit = l
}

// newFlag returns the cache flag value for a newly created node.
// 새롭게 생성된 노드의 캐시 flag를 반환한다
func (t *Trie) newFlag() nodeFlag {
	return nodeFlag{dirty: true, gen: t.cachegen}
}

// New creates a trie with an existing root node from db.
//
// If root is the zero hash or the sha3 hash of an empty string, the
// trie is initially empty and does not require a database. Otherwise,
// New will panic if db is nil and returns a MissingNodeError if root does
// not exist in the database. Accessing the trie loads nodes from db on demand.
// New 함수는 DB에 존재하는 루트 노드로 부터 trie를 생성한다
// 만약 root가 제로해시거나 빈 문자열의 sha3 해시일경우, 트라이는 빈값으로 초기화되며 DB가 필요하지 않다
// 그렇지 않을경우, db가 null이거나 루트가 db에 없을 경우 이함수는 panic을 발생시킨다.
// trie 에 접근하는 것은 요구에 따라 db로부터 node들를 읽어들인다
func New(root common.Hash, db *Database) (*Trie, error) {
	if db == nil {
		panic("trie.New called without a database")
	}
	trie := &Trie{
		db:           db,
		originalRoot: root,
	}
	if (root != common.Hash{}) && root != emptyRoot {
		rootnode, err := trie.resolveHash(root[:], nil)
		if err != nil {
			return nil, err
		}
		trie.root = rootnode
	}
	return trie, nil
}

// NodeIterator returns an iterator that returns nodes of the trie. Iteration starts at
// the key after the given start key.
// NodeIterator 함수는 트라이의 노드를 반환하는 반복자를 반환한다
// 반복은 주어진 시작키의 다음부터 시작한다
func (t *Trie) NodeIterator(start []byte) NodeIterator {
	return newNodeIterator(t, start)
}

// Get returns the value for key stored in the trie.
// The value bytes must not be modified by the caller.
// Get 함수는 트라이에 저장된 key를 위한 값을 반환한다
// 값의 바이트는 호출된 함수에의해 수정되면 안된다
func (t *Trie) Get(key []byte) []byte {
	res, err := t.TryGet(key)
	if err != nil {
		log.Error(fmt.Sprintf("Unhandled trie error: %v", err))
	}
	return res
}

// TryGet returns the value for key stored in the trie.
// The value bytes must not be modified by the caller.
// If a node was not found in the database, a MissingNodeError is returned.
// TryGet 함수는 트라이에 저장된 key를 위한 값을 반환한다
// 값의 바이트는 호출된 함수에의해 수정되면 안된다
// 만약 노드가 db에서 찾아지지 안흔다면 MissingNodeError가 반환된다
func (t *Trie) TryGet(key []byte) ([]byte, error) {
	key = keybytesToHex(key)
	value, newroot, didResolve, err := t.tryGet(t.root, key, 0)
	if err == nil && didResolve {
		t.root = newroot
	}
	return value, err
}

func (t *Trie) tryGet(origNode node, key []byte, pos int) (value []byte, newnode node, didResolve bool, err error) {
	switch n := (origNode).(type) {
	case nil:
		return nil, nil, false, nil
	case valueNode:
		return n, n, false, nil
	case *shortNode:
		if len(key)-pos < len(n.Key) || !bytes.Equal(n.Key, key[pos:pos+len(n.Key)]) {
			// key not found in trie
			return nil, n, false, nil
		}
		value, newnode, didResolve, err = t.tryGet(n.Val, key, pos+len(n.Key))
		if err == nil && didResolve {
			n = n.copy()
			n.Val = newnode
			n.flags.gen = t.cachegen
		}
		return value, n, didResolve, err
	case *fullNode:
		value, newnode, didResolve, err = t.tryGet(n.Children[key[pos]], key, pos+1)
		if err == nil && didResolve {
			n = n.copy()
			n.flags.gen = t.cachegen
			n.Children[key[pos]] = newnode
		}
		return value, n, didResolve, err
	case hashNode:
		child, err := t.resolveHash(n, key[:pos])
		if err != nil {
			return nil, n, true, err
		}
		value, newnode, _, err := t.tryGet(child, key, pos)
		return value, newnode, true, err
	default:
		panic(fmt.Sprintf("%T: invalid node: %v", origNode, origNode))
	}
}

// Update associates key with value in the trie. Subsequent calls to
// Get will return value. If value has length zero, any existing value
// is deleted from the trie and calls to Get will return nil.
//
// The value bytes must not be modified by the caller while they are
// stored in the trie.
// Update 함수는 트라이의 값을 키와 연관시킨다
// 다음 콜들이 값을 반환한다
// 만약 값의 길이가 0일경우 트라이에 존재하는 모든값은 삭제되고 get함수는 nil을 반환한다
// 값의 바이트들은 그들이 트라이에 저장되어있는 동안에는 호출자에의해 수정되어서는 안된다
func (t *Trie) Update(key, value []byte) {
	if err := t.TryUpdate(key, value); err != nil {
		log.Error(fmt.Sprintf("Unhandled trie error: %v", err))
	}
}

// TryUpdate associates key with value in the trie. Subsequent calls to
// Get will return value. If value has length zero, any existing value
// is deleted from the trie and calls to Get will return nil.
//
// The value bytes must not be modified by the caller while they are
// stored in the trie.
//
// If a node was not found in the database, a MissingNodeError is returned.
// TryUpdate 함수는 트라이의 값을 키와 연관시킨다
// 다음 콜들이 값을 반환한다
// 만약 값의 길이가 0일경우 트라이에 존재하는 모든값은 삭제되고 get함수는 nil을 반환한다
// 값의 바이트들은 그들이 트라이에 저장되어있는 동안에는 호출자에의해 수정되어서는 안된다
// 만약 노드가 db에서 찾아지지 안흔다면 MissingNodeError가 반환된다
func (t *Trie) TryUpdate(key, value []byte) error {
	k := keybytesToHex(key)
	if len(value) != 0 {
		_, n, err := t.insert(t.root, nil, k, valueNode(value))
		if err != nil {
			return err
		}
		t.root = n
	} else {
		_, n, err := t.delete(t.root, nil, k)
		if err != nil {
			return err
		}
		t.root = n
	}
	return nil
}

func (t *Trie) insert(n node, prefix, key []byte, value node) (bool, node, error) {
	if len(key) == 0 {
		if v, ok := n.(valueNode); ok {
			return !bytes.Equal(v, value.(valueNode)), value, nil
		}
		return true, value, nil
	}
	switch n := n.(type) {
	case *shortNode:
		matchlen := prefixLen(key, n.Key)
		// If the whole key matches, keep this short node as is
		// and only update the value.
        // 숏노드이면서 키가 동일하면 값만 업데이트한다
		if matchlen == len(n.Key) {
			dirty, nn, err := t.insert(n.Val, append(prefix, key[:matchlen]...), key[matchlen:], value)
			if !dirty || err != nil {
				return false, n, err
			}
			return true, &shortNode{n.Key, nn, t.newFlag()}, nil
		}
		// Otherwise branch out at the index where they differ.
        // 아니라면 풀노드로 브랜치
		branch := &fullNode{flags: t.newFlag()}
		var err error
		_, branch.Children[n.Key[matchlen]], err = t.insert(nil, append(prefix, n.Key[:matchlen+1]...), n.Key[matchlen+1:], n.Val)
		if err != nil {
			return false, nil, err
		}
		_, branch.Children[key[matchlen]], err = t.insert(nil, append(prefix, key[:matchlen+1]...), key[matchlen+1:], value)
		if err != nil {
			return false, nil, err
		}
		// Replace this shortNode with the branch if it occurs at index 0.
        // 하나도 매칭이 안되었을경우 기존 숏노드를 브랜치노드로 치환
		if matchlen == 0 {
			return true, branch, nil
		}
		// Otherwise, replace it with a short node leading up to the branch.
        // 기존 short node의 키/값을 매칭키와 branch노드로 변경한다
		return true, &shortNode{key[:matchlen], branch, t.newFlag()}, nil

	case *fullNode:
		dirty, nn, err := t.insert(n.Children[key[0]], append(prefix, key[0]), key[1:], value)
		if !dirty || err != nil {
			return false, n, err
		}
		n = n.copy()
		n.flags = t.newFlag()
		n.Children[key[0]] = nn
		return true, n, nil

	case nil:
		return true, &shortNode{key, value, t.newFlag()}, nil

	case hashNode:
		// We've hit a part of the trie that isn't loaded yet. Load
		// the node and insert into it. This leaves all child nodes on
		// the path to the value in the trie.
		rn, err := t.resolveHash(n, prefix)
		if err != nil {
			return false, nil, err
		}
		dirty, nn, err := t.insert(rn, prefix, key, value)
		if !dirty || err != nil {
			return false, rn, err
		}
		return true, nn, nil

	default:
		panic(fmt.Sprintf("%T: invalid node: %v", n, n))
	}
}

// Delete removes any existing value for key from the trie.
// Delete 함수는 키를 위한 값을 트라이로부터 제거한다
func (t *Trie) Delete(key []byte) {
	if err := t.TryDelete(key); err != nil {
		log.Error(fmt.Sprintf("Unhandled trie error: %v", err))
	}
}

// TryDelete removes any existing value for key from the trie.
// If a node was not found in the database, a MissingNodeError is returned.
// TryDelete 함수는 키를 위한 값을 트라이로부터 제거한다
// 만약 노드가 db에서 찾아지지 안흔다면 MissingNodeError가 반환된다
func (t *Trie) TryDelete(key []byte) error {
	k := keybytesToHex(key)
	_, n, err := t.delete(t.root, nil, k)
	if err != nil {
		return err
	}
	t.root = n
	return nil
}

// delete returns the new root of the trie with key deleted.
// It reduces the trie to minimal form by simplifying
// nodes on the way up after deleting recursively.
// delete함수는 삭제된 키로부터 새로운 트라이의 루트를 반환한다
// 재귀적으로 삭제된 노드들이 최소의 트라이를 유지하도록하는 방법이다
func (t *Trie) delete(n node, prefix, key []byte) (bool, node, error) {
	switch n := n.(type) {
	case *shortNode:
		matchlen := prefixLen(key, n.Key)
		if matchlen < len(n.Key) {
			return false, n, nil // don't replace n on mismatch
		}
		if matchlen == len(key) {
			return true, nil, nil // remove n entirely for whole matches
		}
		// The key is longer than n.Key. Remove the remaining suffix
		// from the subtrie. Child can never be nil here since the
		// subtrie must contain at least two other values with keys
		// longer than n.Key.
		// 키가 n.Key의 값보다 길다. 서브 트라이로 부터 남겨진 접미사들을 제거한다
		// 자식들은 nil이여서는 안된다 서브트리는 n.key보다 긴 키와 관계된 
		// 최소한 2개의 다른 값을 반드시 가져야 한다
		dirty, child, err := t.delete(n.Val, append(prefix, key[:len(n.Key)]...), key[len(n.Key):])
		if !dirty || err != nil {
			return false, n, err
		}
		switch child := child.(type) {
		case *shortNode:
			// Deleting from the subtrie reduced it to another
			// short node. Merge the nodes to avoid creating a
			// shortNode{..., shortNode{...}}. Use concat (which
			// always creates a new slice) instead of append to
			// avoid modifying n.Key since it might be shared with
			// other nodes.
			// 서브트리로부터의 삭제는 다른 short 노드를 위해 감소된다.
			// 새로운 shortnode의 생성을 피하기 위해 노드들을 합병한다. 
			// 다른 노드들에게 공유될수 있기 때문에 append 대신 concat을 사용하여 
			// n.Key의 수정을 피한다 
			return true, &shortNode{concat(n.Key, child.Key...), child.Val, t.newFlag()}, nil
		default:
			return true, &shortNode{n.Key, child, t.newFlag()}, nil
		}

	case *fullNode:
		dirty, nn, err := t.delete(n.Children[key[0]], append(prefix, key[0]), key[1:])
		if !dirty || err != nil {
			return false, n, err
		}
		n = n.copy()
		n.flags = t.newFlag()
		n.Children[key[0]] = nn

		// Check how many non-nil entries are left after deleting and
		// reduce the full node to a short node if only one entry is
		// left. Since n must've contained at least two children
		// before deletion (otherwise it would not be a full node) n
		// can never be reduced to nil.
		//
		// When the loop is done, pos contains the index of the single
		// value that is left in n or -2 if n contains at least two
		// values.
		// 삭제 이후에 얼마나 많은 비어있지 않은 엔트리가 남았는지 체크하고
		// 오직 하나의 엔트리가 남았을때 full노드를 short노드로 감소시킨다
		// n은 삭제되기전 최소한 2개의 자식을 가지고 있어야 하므로 n은 절대로 nil로 줄어들수 없다
		// 반복이 끝나면 만약 n이 최소한 2개의 값을 가질 경우 
		// pos는 n이나 -2에 남겨진 단일값의 인덱스를 포함한다
		pos := -1
		for i, cld := range n.Children {
			if cld != nil {
				if pos == -1 {
					pos = i
				} else {
					pos = -2
					break
				}
			}
		}
		if pos >= 0 {
			if pos != 16 {
				// If the remaining entry is a short node, it replaces
				// n and its key gets the missing nibble tacked to the
				// front. This avoids creating an invalid
				// shortNode{..., shortNode{...}}.  Since the entry
				// might not be loaded yet, resolve it just for this
				// check.
				// 만약 남아있는 엔트리가 short node라면, 이것은 n을 대체하고 
				// 앞쪽을 향하는 일어버린 니블을 키로서 갖는다
				// 이것은 유효하지 않은 숏노드의 생성을 피한다
				// 엔트리가 아직 로딩되지 않았기때문에 이러한 체크로만 해결한다
				cnode, err := t.resolve(n.Children[pos], prefix)
				if err != nil {
					return false, nil, err
				}
				if cnode, ok := cnode.(*shortNode); ok {
					k := append([]byte{byte(pos)}, cnode.Key...)
					return true, &shortNode{k, cnode.Val, t.newFlag()}, nil
				}
			}
			// Otherwise, n is replaced by a one-nibble short node
			// containing the child.
			// 아니라면, n은 자식을 포함하는 one-nibble short node에 의해 대체된다
			return true, &shortNode{[]byte{byte(pos)}, n.Children[pos], t.newFlag()}, nil
		}
		// n still contains at least two values and cannot be reduced.
		// n은 아직 최소 2개의 값을 포함하고 있으며, 줄어들수 없다
		return true, n, nil

	case valueNode:
		return true, nil, nil

	case nil:
		return false, nil, nil

	case hashNode:
		// We've hit a part of the trie that isn't loaded yet. Load
		// the node and delete from it. This leaves all child nodes on
		// the path to the value in the trie.
		// 아직 load되지 않은 트라이의 일부를 만났을때, 노드를 load하고 삭제한다
		// 트리 안의 값에 대한 경로상의 모든 자식 노드를 떠난다
		rn, err := t.resolveHash(n, prefix)
		if err != nil {
			return false, nil, err
		}
		dirty, nn, err := t.delete(rn, prefix, key)
		if !dirty || err != nil {
			return false, rn, err
		}
		return true, nn, nil

	default:
		panic(fmt.Sprintf("%T: invalid node: %v (%v)", n, n, key))
	}
}

func concat(s1 []byte, s2 ...byte) []byte {
	r := make([]byte, len(s1)+len(s2))
	copy(r, s1)
	copy(r[len(s1):], s2)
	return r
}

func (t *Trie) resolve(n node, prefix []byte) (node, error) {
	if n, ok := n.(hashNode); ok {
		return t.resolveHash(n, prefix)
	}
	return n, nil
}

func (t *Trie) resolveHash(n hashNode, prefix []byte) (node, error) {
	cacheMissCounter.Inc(1)

	hash := common.BytesToHash(n)

	enc, err := t.db.Node(hash)
	if err != nil || enc == nil {
		return nil, &MissingNodeError{NodeHash: hash, Path: prefix}
	}
	return mustDecodeNode(n, enc, t.cachegen), nil
}

// Root returns the root hash of the trie.
// Deprecated: use Hash instead.
// Root함수는 트라이의 루트해시를 반환한다
// 제거됨: Hash함수를 사용하세요
func (t *Trie) Root() []byte { return t.Hash().Bytes() }

// Hash returns the root hash of the trie. It does not write to the
// database and can be used even if the trie doesn't have one.
// Root함수는 트라이의 루트해시를 반환한다
// DB에 쓰지 않고, trie가 해시를 가지고 있지 않을 때에도 사용가능하다
func (t *Trie) Hash() common.Hash {
	hash, cached, _ := t.hashRoot(nil, nil)
	t.root = cached
	return common.BytesToHash(hash.(hashNode))
}

// Commit writes all nodes to the trie's memory database, tracking the internal
// and external (for account tries) references.
// Commit함수는 트라이의 메모리 dB에 모든 노드를 쓰고, 내부참조나 계정트라이를 위한 외부참조를 관리한다
func (t *Trie) Commit(onleaf LeafCallback) (root common.Hash, err error) {
	if t.db == nil {
		panic("commit called on trie with nil database")
	}
	hash, cached, err := t.hashRoot(t.db, onleaf)
	if err != nil {
		return common.Hash{}, err
	}
	t.root = cached
	t.cachegen++
	return common.BytesToHash(hash.(hashNode)), nil
}

func (t *Trie) hashRoot(db *Database, onleaf LeafCallback) (node, node, error) {
	if t.root == nil {
		return hashNode(emptyRoot.Bytes()), nil, nil
	}
	h := newHasher(t.cachegen, t.cachelimit, onleaf)
	defer returnHasherToPool(h)
	return h.hash(t.root, db, true)
}
