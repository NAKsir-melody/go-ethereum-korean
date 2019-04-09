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

package core

import (
	crand "crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	mrand "math/rand"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/hashicorp/golang-lru"
)

const (
	headerCacheLimit = 512
	tdCacheLimit     = 1024
	numberCacheLimit = 2048
)

// HeaderChain implements the basic block header chain logic that is shared by
// core.BlockChain and light.LightChain. It is not usable in itself, only as
// a part of either structure.
// It is not thread safe either, the encapsulating chain structures should do
// the necessary mutex locking/unlocking.
// 헤더체인은 기본적인 블록 헤더 체인을의 로직을 구현한 것으로, 이 로직은 코어노드의 블록체인구조와 
// 량노드의 블록체인구조에 공유된다 이 로직은 혼자서만 동작하지 않고, 각 구조의 일부로서 동작한다
// 또한 병렬처리에 안전하지 않기 때문에, 이 로직을 포함하는 체인 구조체는 반드시 뮤텍스 락/언락이 필요하다
// @sigmoid: 싱크로직을 보면 full노드의 fast싱크, 라이트 노드의 싱크로직 양쪽에서 헤더 체인을 받습니다.
type HeaderChain struct {
	config *params.ChainConfig

	chainDb       ethdb.Database
	genesisHeader *types.Header

	currentHeader     atomic.Value // Current head of the header chain (may be above the block chain!)
	// 헤더체인의 현재 헤더를 나타내며, block chain보다 앞설것입니다
	// @sigmoid: 블록체인의 header는 latest를 나타내며, 동기화시 헤더를 먼저 받기 때문에, 
	// 블록체인의 latest보다 주로 앞서게 됩니다.

	currentHeaderHash common.Hash  // Hash of the current head of the header chain (prevent recomputing all the time)
	//헤더체인의 현재 해시

	headerCache *lru.Cache // Cache for the most recent block headers
	tdCache     *lru.Cache // Cache for the most recent block total difficulties
	numberCache *lru.Cache // Cache for the most recent block numbers
	// 가장 최근에 사용된 블록헤더, 블록의 TD, 번호

	procInterrupt func() bool

	rand   *mrand.Rand
	engine consensus.Engine
}

// NewHeaderChain creates a new HeaderChain structure.
//  getValidator should return the parent's validator
//  procInterrupt points to the parent's interrupt semaphore
//  wg points to the parent's shutdown wait group
// NewHeaderChain 함수는 새로운 헤더체인 구조체를 생성한다.
// getValidator는 반드시 부모의 검증자를 반환해야하며
// procInterrupt는 부모의 인터럽트 세마포어를 가리킨다
// wg는 부모의 종료 대기 그룹을 가리킨다
func NewHeaderChain(chainDb ethdb.Database, config *params.ChainConfig, engine consensus.Engine, procInterrupt func() bool) (*HeaderChain, error) {
	headerCache, _ := lru.New(headerCacheLimit)
	tdCache, _ := lru.New(tdCacheLimit)
	numberCache, _ := lru.New(numberCacheLimit)

	// Seed a fast but crypto originating random generator
	// 빠르지만 암호화된 랜덤 생성자
	seed, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return nil, err
	}

	hc := &HeaderChain{
		config:        config,
		chainDb:       chainDb,
		headerCache:   headerCache,
		tdCache:       tdCache,
		numberCache:   numberCache,
		procInterrupt: procInterrupt,
		rand:          mrand.New(mrand.NewSource(seed.Int64())),
		engine:        engine,
	}

	hc.genesisHeader = hc.GetHeaderByNumber(0)
	if hc.genesisHeader == nil {
		return nil, ErrNoGenesis
	}

	hc.currentHeader.Store(hc.genesisHeader)
	if head := rawdb.ReadHeadBlockHash(chainDb); head != (common.Hash{}) {
		if chead := hc.GetHeaderByHash(head); chead != nil {
			hc.currentHeader.Store(chead)
		}
	}
	hc.currentHeaderHash = hc.CurrentHeader().Hash()

	return hc, nil
}

// GetBlockNumber retrieves the block number belonging to the given hash
// from the cache or database
// GetBlockNumber 함수는 주어진 해시가 속한 블록의 번호를 캐시나 DB로 부터 반환한다
func (hc *HeaderChain) GetBlockNumber(hash common.Hash) *uint64 {
	if cached, ok := hc.numberCache.Get(hash); ok {
		number := cached.(uint64)
		return &number
	}
	number := rawdb.ReadHeaderNumber(hc.chainDb, hash)
	if number != nil {
		hc.numberCache.Add(hash, *number)
	}
	return number
}

// WriteHeader writes a header into the local chain, given that its parent is
// already known. If the total difficulty of the newly inserted header becomes
// greater than the current known TD, the canonical chain is re-routed.
//
// Note: This method is not concurrent-safe with inserting blocks simultaneously
// into the chain, as side effects caused by reorganisations cannot be emulated
// without the real blocks. Hence, writing headers directly should only be done
// in two scenarios: pure-header mode of operation (light clients), or properly
// separated header/block phases (non-archive clients).
// WriteHeader 함수는 헤더의 부모가 이미 알려진 헤더를 로컬 체인에 쓴다
// 만약 새롭게 삽입된 헤더의 토탈디피컬티가 현재 알려진 TD보다 커진다면 캐노니컬 체인이 재구성된다
// @simgoid: TD가 커진다는 의미는 블록이 길어졌다는 의미이고, 내 블록이 길어지면 합의과정을 통해
// 타 노드에 전파될 것이고, 갱신될것입니다. 캐노니컬 체인은 모든 노드가 공통으로 가지고 있는 길이까지의 
// 블록체인을 말합니다.
func (hc *HeaderChain) WriteHeader(header *types.Header) (status WriteStatus, err error) {
	// Cache some values to prevent constant recalculation
	// 상수를 재연산하는것을 막기 위한 캐시값들
	var (
		hash   = header.Hash()
		number = header.Number.Uint64()
	)
	// Calculate the total difficulty of the header
	// 헤더의 TD를 계산한다
	ptd := hc.GetTd(header.ParentHash, number-1)
	if ptd == nil {
		return NonStatTy, consensus.ErrUnknownAncestor
	}
	localTd := hc.GetTd(hc.currentHeaderHash, hc.CurrentHeader().Number.Uint64())
	externTd := new(big.Int).Add(header.Difficulty, ptd)

	// Irrelevant of the canonical status, write the td and header to the database
	// 캐노니컬 상태와 무관한것들인 TD와 헤더를 DB에쓴다
	if err := hc.WriteTd(hash, number, externTd); err != nil {
		log.Crit("Failed to write header total difficulty", "err", err)
	}
	rawdb.WriteHeader(hc.chainDb, header)

	// If the total difficulty is higher than our known, add it to the canonical chain
	// Second clause in the if statement reduces the vulnerability to selfish mining.
	// Please refer to http://www.cs.cornell.edu/~ie53/publications/btcProcFC.pdf
	// 만약 TD가 알려진것보다 크다면, 이것을 캐노니컬 체인에 추가한다
	// 두번째 절의 if는 이기적인 마이닝에 대한 취약성을 줄여준다
	if externTd.Cmp(localTd) > 0 || (externTd.Cmp(localTd) == 0 && mrand.Float64() < 0.5) {
		// Delete any canonical number assignments above the new head
		// 새로운 해드보다 높은 캐노니컬 번호를 삭제한다
		for i := number + 1; ; i++ {
			hash := rawdb.ReadCanonicalHash(hc.chainDb, i)
			if hash == (common.Hash{}) {
				break
			}
			rawdb.DeleteCanonicalHash(hc.chainDb, i)
		}
		// Overwrite any stale canonical number assignments
		// 오래된 캐노니컬 번호 할당을 덮어쓴다
		var (
			headHash   = header.ParentHash
			headNumber = header.Number.Uint64() - 1
			headHeader = hc.GetHeader(headHash, headNumber)
		)
		for rawdb.ReadCanonicalHash(hc.chainDb, headNumber) != headHash {
			rawdb.WriteCanonicalHash(hc.chainDb, headHash, headNumber)

			headHash = headHeader.ParentHash
			headNumber = headHeader.Number.Uint64() - 1
			headHeader = hc.GetHeader(headHash, headNumber)
		}
		// Extend the canonical chain with the new header
		// 새로운 헤더와함께 캐노니컬 체인을 확장한다
		rawdb.WriteCanonicalHash(hc.chainDb, hash, number)
		rawdb.WriteHeadHeaderHash(hc.chainDb, hash)

		hc.currentHeaderHash = hash
		hc.currentHeader.Store(types.CopyHeader(header))

		status = CanonStatTy
	} else {
		status = SideStatTy
	}

	hc.headerCache.Add(hash, header)
	hc.numberCache.Add(hash, number)

	return
}

// WhCallback is a callback function for inserting individual headers.
// A callback is used for two reasons: first, in a LightChain, status should be
// processed and light chain events sent, while in a BlockChain this is not
// necessary since chain events are sent after inserting blocks. Second, the
// header writes should be protected by the parent chain mutex individually.
// whCallback함수는 각각의 헤더를 넣는 콜백함수이다 콜백은 2가지 이유로 사용된다. 
// 첫번째로 라이트 체인에서 상태들은 반드시 처리되어야 하고 라이트체인 이벤트를 전송해야 한다
// 반면에 블록체인에서는 블록을 삽입하고 체인이벤트가 발생하기 전까지는 이과정이 필요하지 않다.
// 두번째로, 쓰여지는 헤더들은 부모체인의 뮤텍스로 부터 각각 지켜져야 한다.
type WhCallback func(*types.Header) error

func (hc *HeaderChain) ValidateHeaderChain(chain []*types.Header, checkFreq int) (int, error) {
	// Do a sanity check that the provided chain is actually ordered and linked
	// 제공된 체인이 실제 주문되고 링크되었는지 확인한다
	for i := 1; i < len(chain); i++ {
		if chain[i].Number.Uint64() != chain[i-1].Number.Uint64()+1 || chain[i].ParentHash != chain[i-1].Hash() {
			// Chain broke ancestry, log a messge (programming error) and skip insertion
			// 공통조상이 깨진 체인일경우 로그를 남기고 삽입을 스킵한다
			log.Error("Non contiguous header insert", "number", chain[i].Number, "hash", chain[i].Hash(),
				"parent", chain[i].ParentHash, "prevnumber", chain[i-1].Number, "prevhash", chain[i-1].Hash())

			return 0, fmt.Errorf("non contiguous insert: item %d is #%d [%x…], item %d is #%d [%x…] (parent [%x…])", i-1, chain[i-1].Number,
				chain[i-1].Hash().Bytes()[:4], i, chain[i].Number, chain[i].Hash().Bytes()[:4], chain[i].ParentHash[:4])
		}
	}

	// Generate the list of seal verification requests, and start the parallel verifier
	// 인장 검증 요청의 리스트를 생성하고 병렬 검증을 시작한다
	seals := make([]bool, len(chain))
	for i := 0; i < len(seals)/checkFreq; i++ {
		index := i*checkFreq + hc.rand.Intn(checkFreq)
		if index >= len(seals) {
			index = len(seals) - 1
		}
		seals[index] = true
	}
	seals[len(seals)-1] = true // Last should always be verified to avoid junk

	abort, results := hc.engine.VerifyHeaders(hc, chain, seals)
	defer close(abort)

	// Iterate over the headers and ensure they all check out
	// 헤더를 반복하면서 모두 체크되었는지 확인한다
	for i, header := range chain {
		// If the chain is terminating, stop processing blocks
		// 체인이 끝났을 경우 블록처리도 끝낸다
		if hc.procInterrupt() {
			log.Debug("Premature abort during headers verification")
			return 0, errors.New("aborted")
		}
		// If the header is a banned one, straight out abort
		// 헤더가 퇴장당했다면, abort
		if BadHashes[header.Hash()] {
			return i, ErrBlacklistedHash
		}
		// Otherwise wait for headers checks and ensure they pass
		// 아니라면 헤더체크를 기다리고 통과를 확정한다
		if err := <-results; err != nil {
			return i, err
		}
	}

	return 0, nil
}

// InsertHeaderChain attempts to insert the given header chain in to the local
// chain, possibly creating a reorg. If an error is returned, it will return the
// index number of the failing header as well an error describing what went wrong.
//
// The verify parameter can be used to fine tune whether nonce verification
// should be done or not. The reason behind the optional check is because some
// of the header retrieval mechanisms already need to verfy nonces, as well as
// because nonces can be verified sparsely, not needing to check each.
// InsertHeaderChain 함수는 주어진 헤더체인을 로컬체인에 넣으려고 노력하며 
// 이과정에서 체인의 재구성을 유도할수도 있다. 만약 삽입과정에서 에러가 반환된다면 
// 이함수는 실패한 헤더의 번호와 무엇이 잘못되었는지 에 대한 에러를 반환한다
// 검증 파라미터는 논스 검증을 할것인지 결정하는 튜닝을 제공하는데, 그이유는
// 어떤헤더 반환 매커니즘이 이미 논스를 검증하거나, 부분적으로 검증될수 있기 때문에
// 각각을 체크할 필요가 없다
func (hc *HeaderChain) InsertHeaderChain(chain []*types.Header, writeHeader WhCallback, start time.Time) (int, error) {
	// Collect some import statistics to report on
	// 보고할 입수상태 정보를 모은다
	stats := struct{ processed, ignored int }{}
	// All headers passed verification, import them into the database
	// 모든 헤더의 검증이 통과했으므로 DB로 넣는다
	for i, header := range chain {
		// Short circuit insertion if shutting down
		// 노드 종료시 빠른 삽입
		if hc.procInterrupt() {
			log.Debug("Premature abort during headers import")
			return i, errors.New("aborted")
		}
		// If the header's already known, skip it, otherwise store
		// 헤더가 이미 알려진것이라면 스킵하고 아니라면 저장
		if hc.HasHeader(header.Hash(), header.Number.Uint64()) {
			stats.ignored++
			continue
		}
		if err := writeHeader(header); err != nil {
			return i, err
		}
		stats.processed++
	}
	// Report some public statistics so the user has a clue what's going on
	// 오픈된 현재 상태 정보를 보고한다
	last := chain[len(chain)-1]
	log.Info("Imported new block headers", "count", stats.processed, "elapsed", common.PrettyDuration(time.Since(start)),
		"number", last.Number, "hash", last.Hash(), "ignored", stats.ignored)

	return 0, nil
}

// GetBlockHashesFromHash retrieves a number of block hashes starting at a given
// hash, fetching towards the genesis block.
// GetBlockHashesFromHash함수는 주어진 해시로부터 제네시스 블록 방향으로의 블록해시의 갯수를 반환한다
func (hc *HeaderChain) GetBlockHashesFromHash(hash common.Hash, max uint64) []common.Hash {
	// Get the origin header from which to fetch
	// fetch를 위한 원본 헤더를 준비한다
	header := hc.GetHeaderByHash(hash)
	if header == nil {
		return nil
	}
	// Iterate the headers until enough is collected or the genesis reached
	// 충분히 반복하거나 제네시스에 도달할때 까지 반복
	chain := make([]common.Hash, 0, max)
	for i := uint64(0); i < max; i++ {
		next := header.ParentHash
		if header = hc.GetHeader(next, header.Number.Uint64()-1); header == nil {
			break
		}
		chain = append(chain, next)
		if header.Number.Sign() == 0 {
			break
		}
	}
	return chain
}

// GetTd retrieves a block's total difficulty in the canonical chain from the
// database by hash and number, caching it if found.
// GetTd 함수는 주어진 해시와 번호를 이용하여 캐노니컬 체인의 블록의 총 난이도를
// DB로 부터 반환하고 만약 발견되었을 경우 캐싱한다
func (hc *HeaderChain) GetTd(hash common.Hash, number uint64) *big.Int {
	// Short circuit if the td's already in the cache, retrieve otherwise
	// TD가 이미 캐시에 있다면 캐싱하고, 아니라면 반환한다
	if cached, ok := hc.tdCache.Get(hash); ok {
		return cached.(*big.Int)
	}
	td := rawdb.ReadTd(hc.chainDb, hash, number)
	if td == nil {
		return nil
	}
	// Cache the found body for next time and return
	// 찾아진 바디를 다음 타임을 위해 캐싱하고 반환한다
	hc.tdCache.Add(hash, td)
	return td
}

// GetTdByHash retrieves a block's total difficulty in the canonical chain from the
// database by hash, caching it if found.
// GetTdByHash함수는 해시를 이용하여 캐노니컬 체인의 블록의 총 난이도를 DB로 부터 반환한다
// 찾았다면 캐싱한다
func (hc *HeaderChain) GetTdByHash(hash common.Hash) *big.Int {
	number := hc.GetBlockNumber(hash)
	if number == nil {
		return nil
	}
	return hc.GetTd(hash, *number)
}

// WriteTd stores a block's total difficulty into the database, also caching it
// along the way.
// WriteTd 함수는 블록의 총 난이도를 DB에 저장하고 캐싱한다
func (hc *HeaderChain) WriteTd(hash common.Hash, number uint64, td *big.Int) error {
	rawdb.WriteTd(hc.chainDb, hash, number, td)
	hc.tdCache.Add(hash, new(big.Int).Set(td))
	return nil
}

// GetHeader retrieves a block header from the database by hash and number,
// caching it if found.
// GetHeader함수는 해시와 번호를 이용하여 DB로부터 블록 헤더를 반환하고 캐싱한다
func (hc *HeaderChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	// Short circuit if the header's already in the cache, retrieve otherwise
	// 헤더들이 이미 캐시에 있을때 짧게끝낸다
	if header, ok := hc.headerCache.Get(hash); ok {
		return header.(*types.Header)
	}
	header := rawdb.ReadHeader(hc.chainDb, hash, number)
	if header == nil {
		return nil
	}
	// Cache the found header for next time and return
	// 다음번을 위해 헤더를 캐싱하고 리턴한다

	hc.headerCache.Add(hash, header)
	return header
}

// GetHeaderByHash retrieves a block header from the database by hash, caching it if
// found.
//GetHeaderByHash함수는 해시를 이용해 블록헤더를 DB로 부터 반환하고 캐싱한다
func (hc *HeaderChain) GetHeaderByHash(hash common.Hash) *types.Header {
	number := hc.GetBlockNumber(hash)
	if number == nil {
		return nil
	}
	return hc.GetHeader(hash, *number)
}

// HasHeader checks if a block header is present in the database or not.
// HasHeader함수는 블록헤더가 DB상에 있는지 확인한다
func (hc *HeaderChain) HasHeader(hash common.Hash, number uint64) bool {
	if hc.numberCache.Contains(hash) || hc.headerCache.Contains(hash) {
		return true
	}
	return rawdb.HasHeader(hc.chainDb, hash, number)
}

// GetHeaderByNumber retrieves a block header from the database by number,
// caching it (associated with its hash) if found.
// GetHeaderByNumber함수는 번호를 이용해 DB로부터 블록헤더를 반환하고
// 캐싱한다
func (hc *HeaderChain) GetHeaderByNumber(number uint64) *types.Header {
	hash := rawdb.ReadCanonicalHash(hc.chainDb, number)
	if hash == (common.Hash{}) {
		return nil
	}
	return hc.GetHeader(hash, number)
}

// CurrentHeader retrieves the current head header of the canonical chain. The
// header is retrieved from the HeaderChain's internal cache.
// CurrentHeader함수는 현재 캐노니컬 체인의 헤드의 헤더를 반환한다
// 그 해더는 헤더체인의 내부 캐시로 부터 반환된다
func (hc *HeaderChain) CurrentHeader() *types.Header {
	return hc.currentHeader.Load().(*types.Header)
}

// SetCurrentHeader sets the current head header of the canonical chain.
// SetCurrentHeadeer함수는 캐노니컬 체인의 헤드 헤더를 설정한다
func (hc *HeaderChain) SetCurrentHeader(head *types.Header) {
	rawdb.WriteHeadHeaderHash(hc.chainDb, head.Hash())

	hc.currentHeader.Store(head)
	hc.currentHeaderHash = head.Hash()
}

// DeleteCallback is a callback function that is called by SetHead before
// each header is deleted.
// DeleteCallback는 각 헤더가 삭제되기 전 SetHead에 의해서 불리는 콜백함수이다
type DeleteCallback func(common.Hash, uint64)

// SetHead rewinds the local chain to a new head. Everything above the new head
// will be deleted and the new one set.
// SetHead 함수는 로컬체인을 새로운 헤더로 돌린다. 
// 새로운 해드 위쪽의 모든것은 삭제될것이고 새로운 것이 설정된다
func (hc *HeaderChain) SetHead(head uint64, delFn DeleteCallback) {
	height := uint64(0)

	if hdr := hc.CurrentHeader(); hdr != nil {
		height = hdr.Number.Uint64()
	}

	for hdr := hc.CurrentHeader(); hdr != nil && hdr.Number.Uint64() > head; hdr = hc.CurrentHeader() {
		hash := hdr.Hash()
		num := hdr.Number.Uint64()
		if delFn != nil {
			delFn(hash, num)
		}
		rawdb.DeleteHeader(hc.chainDb, hash, num)
		rawdb.DeleteTd(hc.chainDb, hash, num)

		hc.currentHeader.Store(hc.GetHeader(hdr.ParentHash, hdr.Number.Uint64()-1))
	}
	// Roll back the canonical chain numbering
	// 캐노니컬 체인 넘버링을 롤백한다
	for i := height; i > head; i-- {
		rawdb.DeleteCanonicalHash(hc.chainDb, i)
	}
	// Clear out any stale content from the caches
	// 오래된 컨텐츠를 캐시로 부터 제거한다
	hc.headerCache.Purge()
	hc.tdCache.Purge()
	hc.numberCache.Purge()

	if hc.CurrentHeader() == nil {
		hc.currentHeader.Store(hc.genesisHeader)
	}
	hc.currentHeaderHash = hc.CurrentHeader().Hash()

	rawdb.WriteHeadHeaderHash(hc.chainDb, hc.currentHeaderHash)
}

// SetGenesis sets a new genesis block header for the chain
// SetGenesis함수는 새로운 제네시스 블록 헤더를 체인에 설정한다
func (hc *HeaderChain) SetGenesis(head *types.Header) {
	hc.genesisHeader = head
}

// Config retrieves the header chain's chain configuration.
// Config함수는 헤더체인의 체인 설정을 반환한다
func (hc *HeaderChain) Config() *params.ChainConfig { return hc.config }

// Engine retrieves the header chain's consensus engine.
// Engine 함수는 헤더체인의 합의엔진을 반환한다
func (hc *HeaderChain) Engine() consensus.Engine { return hc.engine }

// GetBlock implements consensus.ChainReader, and returns nil for every input as
// a header chain does not have blocks available for retrieval.
// GetBlock 함수는 consensus.ChainReader를 구현하며, 
// 헤더체인은 반환에 필요한 블록을 가지고 있지 않기 때문에 
// 모든 입력에 대해 nil을 반환한다
func (hc *HeaderChain) GetBlock(hash common.Hash, number uint64) *types.Block {
	return nil
}
