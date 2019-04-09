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

// Package core implements the Ethereum consensus protocol.
// 코어패키지는 이더리움 합의 프로토콜을 구현한다
package core

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	mrand "math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/hashicorp/golang-lru"
	"gopkg.in/karalabe/cookiejar.v2/collections/prque"
)

var (
	blockInsertTimer = metrics.NewRegisteredTimer("chain/inserts", nil)

	ErrNoGenesis = errors.New("Genesis not found in chain")
)

const (
	bodyCacheLimit      = 256
	blockCacheLimit     = 256
	maxFutureBlocks     = 256
	maxTimeFutureBlocks = 30
	badBlockLimit       = 10
	triesInMemory       = 128

	// BlockChainVersion ensures that an incompatible database forces a resync from scratch.
	// BlockChainVersion은 연동불가능한 DB를 scratch부터 새롭게 싱크하도록 강제한다
	BlockChainVersion = 3
)

// CacheConfig contains the configuration values for the trie caching/pruning
// that's resident in a blockchain.
// CacheConfig 구조체는 블록체인에 있는 trie의 캐싱/가지치기를 위한 설정값을 포함한다
type CacheConfig struct {
	Disabled      bool          // Whether to disable trie write caching (archive node)
	// trie쓰기 캐싱의 on/off여부
	TrieNodeLimit int           // Memory limit (MB) at which to flush the current in-memory trie to disk
	// 메모리 상의 트라이를 디스크에쓰게하는 메모리 한도
	TrieTimeLimit time.Duration // Time limit after which to flush the current in-memory trie to disk
	// 메모리 상의 트라이를 디스크에쓰게하는 시간 한도
}

// BlockChain represents the canonical chain given a database with a genesis
// block. The Blockchain manages chain imports, reverts, chain reorganisations.
//
// Importing blocks in to the block chain happens according to the set of rules
// defined by the two stage Validator. Processing of blocks is done using the
// Processor which processes the included transaction. The validation of the state
// is done in the second part of the Validator. Failing results in aborting of
// the import.
//
// The BlockChain also helps in returning blocks from **any** chain included
// in the database as well as blocks that represents the canonical chain. It's
// important to note that GetBlock can return any block and does not need to be
// included in the canonical one where as GetBlockByNumber always represents the
// canonical chain.

// 블록체인 구조체는 제네시스 블록을 가진 주어진 DB의 캐노니컬 체인을 표현한다.
// 체인의 수신/되돌리기/재구성 작업을 관리한다
// 블록을 수신하는 것은 두개의 스테이지 검증자의 규칙세트에 따라 발생한다.
// 블록들의 처리는 포함된 트렌젝션을 처리하는 state 처리자를 이용하여 완료한다.
// state  검증은 검증자의 두번째 스테이지에서 끝나고, 검증 실패시 수신이 포기된다
// 이구조체는 db상의 어떤 canonical체인도 지원한다.
// 이부분이 중요한 이유는 GetBlocks이 캐노니컬 채인에 필요하지 안은 블록을 리턴할 가능성이 있으며
// 캐노니컬안에 포함될 필요가 없다. 왜냐하면 GetBlockByNumber가 언제나 캐노니컬 체인을 나타내기 때문이다
type BlockChain struct {
	chainConfig *params.ChainConfig // Chain & network configuration
	// 체인과 네트워크 설정
	cacheConfig *CacheConfig        // Cache configuration for pruning
	// 가지치기를 위한 캐시 설정

	db     ethdb.Database // Low level persistent database to store final content in
	// 최종 컨텐츠를 저장할 저수준의 persistent한 DB
	triegc *prque.Prque   // Priority queue mapping block numbers to tries to gc
	// 가비지 컬렉션을 위한 쁠록넘버가 trie에 매핑된 우선순위 큐 
	gcproc time.Duration  // Accumulates canonical block processing for trie dumping
	// 트라이 덤프를 위해 캐노니컬 블록을 처리한 누적시간

	hc            *HeaderChain
	rmLogsFeed    event.Feed
	chainFeed     event.Feed
	chainSideFeed event.Feed
	chainHeadFeed event.Feed
	logsFeed      event.Feed
	scope         event.SubscriptionScope
	genesisBlock  *types.Block

	mu      sync.RWMutex // global mutex for locking chain operations
	// 체인 Lock을 위한 전역 mutex
	chainmu sync.RWMutex // blockchain insertion lock
	// 블록체인 삽입 lock
	procmu  sync.RWMutex // block processor lock
	// 블록 처리 lock

	checkpoint       int          // checkpoint counts towards the new checkpoint
	// 새로운 체크포인트를 위한 체크포인트 카운트
	currentBlock     atomic.Value // Current head of the block chain
	// 블록체인의 현재 헤드
	currentFastBlock atomic.Value // Current head of the fast-sync chain (may be above the block chain!)
	// fast-sync 체인의 현재 헤드(대부분 블록체인보다 앞선다)

	stateCache   state.Database // State database to reuse between imports (contains state cache)
	// 블록 입수간 재사용될 스테이트 캐시를 포함하는 StateDB
	bodyCache    *lru.Cache     // Cache for the most recent block bodies
	// 최근 블록의 바디들를 위한 캐시
	bodyRLPCache *lru.Cache     // Cache for the most recent block bodies in RLP encoded format
	// 최근 블록의 RLP 인코딩된 바디들
	blockCache   *lru.Cache     // Cache for the most recent entire blocks
	// 최근 블록들의 캐시
	futureBlocks *lru.Cache     // future blocks are blocks added for later processing
	// 나중에 처리되기위해 더해진 블록들

	quit    chan struct{} // blockchain quit channel
	// 블록체인 종료채널
	running int32         // running must be called atomically
	// atomic하게 호출되어야하는 running
	// procInterrupt must be atomically called
	procInterrupt int32          // interrupt signaler for block processing
	// 블록처리를 위한 인터럽트 시그널
	wg            sync.WaitGroup // chain processing wait group for shutting down
	// 종료를 위한 채인처리 대기 그룹

	engine    consensus.Engine
	processor Processor // block processor interface
	// 블록 처리자 인터페이스
	validator Validator // block and state validator interface
	// 블록과 상태 검증자 인터페이스
	vmConfig  vm.Config

	badBlocks *lru.Cache // Bad block cache
	// 배드블록 캐시
}

// NewBlockChain returns a fully initialised block chain using information
// available in the database. It initialises the default Ethereum Validator and
// Processor.
// 이 함수는 DB의 정보를 이용해서 완전히 초기화된 블록체인을 리턴한다.
// 이더리움의 기본 검증자와 처리자를 초기화한다
func NewBlockChain(db ethdb.Database, cacheConfig *CacheConfig, chainConfig *params.ChainConfig, engine consensus.Engine, vmConfig vm.Config) (*BlockChain, error) {
	if cacheConfig == nil {
		cacheConfig = &CacheConfig{
			TrieNodeLimit: 256 * 1024 * 1024,
			TrieTimeLimit: 5 * time.Minute,
		}
	}
	bodyCache, _ := lru.New(bodyCacheLimit)
	bodyRLPCache, _ := lru.New(bodyCacheLimit)
	blockCache, _ := lru.New(blockCacheLimit)
	futureBlocks, _ := lru.New(maxFutureBlocks)
	badBlocks, _ := lru.New(badBlockLimit)

	bc := &BlockChain{
		chainConfig:  chainConfig,
		cacheConfig:  cacheConfig,
		db:           db,
		triegc:       prque.New(),
		stateCache:   state.NewDatabase(db),
		quit:         make(chan struct{}),
		bodyCache:    bodyCache,
		bodyRLPCache: bodyRLPCache,
		blockCache:   blockCache,
		futureBlocks: futureBlocks,
		engine:       engine,
		vmConfig:     vmConfig,
		badBlocks:    badBlocks,
	}
	// 검증자와 스테이트 처리자 설정
	// 블록 검증자는 블록의 헤더와 엉클블락들과 처리된 스테이트를 검증해야한다
	// 상태 처리자는 기본 처리자로서, 한 지점에서 다른 지점으로의 state의 변환을 관리한다
	bc.SetValidator(NewBlockValidator(chainConfig, bc, engine))
	bc.SetProcessor(NewStateProcessor(chainConfig, bc, engine))

	var err error
	// 이 함수는 새로운 헤더체인 구조체를 생성한다.
	bc.hc, err = NewHeaderChain(db, chainConfig, engine, bc.getProcInterrupt)
	if err != nil {
		return nil, err
	}
	bc.genesisBlock = bc.GetBlockByNumber(0)
	if bc.genesisBlock == nil {
		return nil, ErrNoGenesis
	}
	// 이함수는 DB로부터 마지막으로 알려진 state를 읽어온다.
	// 메인 계정 trie를 오픈하고 stateDB를 생성한다
	// 현재 블록과 현재 블록헤더를 설정하고 tota difficulty를 계산한다
	if err := bc.loadLastState(); err != nil {
		return nil, err
	}
	// Check the current state of the block hashes and make sure that we do not have any of the bad blocks in our chain
	// 현재 블록 해시들의 상태를 체크하고 배드블럭들이 체인에 없는지 체크한다
	for hash := range BadHashes {
		if header := bc.GetHeaderByHash(hash); header != nil {
			// get the canonical block corresponding to the offending header's number
			// 문제가되는 헤더의 번호와 관련된 캐노니컬 블록을 찾는다
			headerByNumber := bc.GetHeaderByNumber(header.Number.Uint64())
			// make sure the headerByNumber (if present) is in our current canonical chain
			// 나타난다면 넘버에의한 헤더가 우리의 캐노니컬 체인에 있는지 확인한다
			if headerByNumber != nil && headerByNumber.Hash() == header.Hash() {
				log.Error("Found bad hash, rewinding chain", "number", header.Number, "hash", header.ParentHash)
				bc.SetHead(header.Number.Uint64() - 1)
				log.Error("Chain rewind was successful, resuming normal operation")
			}
		}
	}
	// Take ownership of this particular state
	// 5초마다 퓨처블록들을 체인에 추가하는 루틴 실행
	go bc.update()
	return bc, nil
}

func (bc *BlockChain) getProcInterrupt() bool {
	return atomic.LoadInt32(&bc.procInterrupt) == 1
}

// loadLastState loads the last known chain state from the database. This method
// assumes that the chain manager mutex is held.
// loadLastState 함수는 DB로부터 마지막으로 알려진 state를 읽어온다.
func (bc *BlockChain) loadLastState() error {
	// Restore the last known head block
	// 마지막으로 알려진 헤드블록을 복원한다
	head := rawdb.ReadHeadBlockHash(bc.db)
	if head == (common.Hash{}) {
		// Corrupt or empty database, init from scratch
		// 망가졌거나 빈 DB. 스크래치로부터 초기화한다
		log.Warn("Empty database, resetting chain")
		return bc.Reset()
	}
	// Make sure the entire head block is available
	//헤더블럭을 검증한다
	currentBlock := bc.GetBlockByHash(head)
	if currentBlock == nil {
		// Corrupt or empty database, init from scratch
		// 망가졌거나 빈 DB. 스크래치로부터 초기화한다
		log.Warn("Head block missing, resetting chain", "hash", head)
		return bc.Reset()
	}
	// Make sure the state associated with the block is available
	// 블록과 관련된 상태가 온전한지 확인
	// 메인 계정 trie를 오픈하고 stateDB를 생성한다
	if _, err := state.New(currentBlock.Root(), bc.stateCache); err != nil {
		// Dangling block without a state associated, init from scratch
		// 상태에 연관없는 블록은 초기화한다
		log.Warn("Head state missing, repairing chain", "number", currentBlock.Number(), "hash", currentBlock.Hash())
		if err := bc.repair(&currentBlock); err != nil {
			return err
		}
	}
	// Everything seems to be fine, set as the head block
	// 문제가 없다면 헤드블록으로 설정한다 
	bc.currentBlock.Store(currentBlock)

	// Restore the last known head header
	// 현재블록으로 저장하고 현재 head값을 하나 증가시켜 common.hash값을 가지게 한다.
	// 아까 전엔 현재 블록 헤드가 common hash를 가지고 있었다.
	currentHeader := currentBlock.Header()
	if head := rawdb.ReadHeadHeaderHash(bc.db); head != (common.Hash{}) {
		if header := bc.GetHeaderByHash(head); header != nil {
			currentHeader = header
		}
	}
	//current head 는 불록의 다음 헤드를 가리킨다.
	bc.hc.SetCurrentHeader(currentHeader)

	// Restore the last known head fast block
	// 빠른 블록에 현재 블록을 설정한다
	bc.currentFastBlock.Store(currentBlock)
	if head := rawdb.ReadHeadFastBlockHash(bc.db); head != (common.Hash{}) {
		if block := bc.GetBlockByHash(head); block != nil {
			bc.currentFastBlock.Store(block)
		}
	}

	// Issue a status log for the user
	// 사용자를 위한 상태로그를 이슈잉한다
	currentFastBlock := bc.CurrentFastBlock()

	//Total difficulty 업데이트
	headerTd := bc.GetTd(currentHeader.Hash(), currentHeader.Number.Uint64())
	blockTd := bc.GetTd(currentBlock.Hash(), currentBlock.NumberU64())
	fastTd := bc.GetTd(currentFastBlock.Hash(), currentFastBlock.NumberU64())

	log.Info("Loaded most recent local header", "number", currentHeader.Number, "hash", currentHeader.Hash(), "td", headerTd)
	log.Info("Loaded most recent local full block", "number", currentBlock.Number(), "hash", currentBlock.Hash(), "td", blockTd)
	log.Info("Loaded most recent local fast block", "number", currentFastBlock.Number(), "hash", currentFastBlock.Hash(), "td", fastTd)

	return nil
}

// SetHead rewinds the local chain to a new head. In the case of headers, everything
// above the new head will be deleted and the new one set. In the case of blocks
// though, the head may be further rewound if block bodies are missing (non-archive
// nodes after a fast sync).
// 이 함수는 로컬체인을 새로운 해드로 되돌린다.
// 헤더들의 경우 뉴 헤드 위쪽의 모든 헤더는 모두 삭제되고 새로운 헤더가 설정될 것이다.
// 블록의 경우, 블록 바디가 없을 경우 좀더 되돌려진다.
func (bc *BlockChain) SetHead(head uint64) error {
	log.Warn("Rewinding blockchain", "target", head)

	bc.mu.Lock()
	defer bc.mu.Unlock()

	// Rewind the header chain, deleting all block bodies until then
	// 헤더체인을 되돌린다. 해당 시점까지의 바디를 모두 삭제한다
	delFn := func(hash common.Hash, num uint64) {
		rawdb.DeleteBody(bc.db, hash, num)
	}
	bc.hc.SetHead(head, delFn)
	currentHeader := bc.hc.CurrentHeader()

	// Clear out any stale content from the caches
	// 캐시로부터 대기중인 컨텐츠를 삭제한다
	bc.bodyCache.Purge()
	bc.bodyRLPCache.Purge()
	bc.blockCache.Purge()
	bc.futureBlocks.Purge()

	// Rewind the block chain, ensuring we don't end up with a stateless head block
	// 블록체인을 되돌린다, 상태가 없는 헤드블록으로 끝나지 않음을 보장한다
	if currentBlock := bc.CurrentBlock(); currentBlock != nil && currentHeader.Number.Uint64() < currentBlock.NumberU64() {
		bc.currentBlock.Store(bc.GetBlock(currentHeader.Hash(), currentHeader.Number.Uint64()))
	}
	if currentBlock := bc.CurrentBlock(); currentBlock != nil {
		if _, err := state.New(currentBlock.Root(), bc.stateCache); err != nil {
			// Rewound state missing, rolled back to before pivot, reset to genesis
			// 되돌아갈 상태가 없음. 피봇으로 롤벡하고 제네시스를 재설정한다
			bc.currentBlock.Store(bc.genesisBlock)
		}
	}
	// Rewind the fast block in a simpleton way to the target head
	// 단순하게 Fast block을 타겟헤드로 되돌린다
	if currentFastBlock := bc.CurrentFastBlock(); currentFastBlock != nil && currentHeader.Number.Uint64() < currentFastBlock.NumberU64() {
		bc.currentFastBlock.Store(bc.GetBlock(currentHeader.Hash(), currentHeader.Number.Uint64()))
	}
	// If either blocks reached nil, reset to the genesis state
	// 블록이 nil에 도달하면 genesis상태를 재설정한다
	if currentBlock := bc.CurrentBlock(); currentBlock == nil {
		bc.currentBlock.Store(bc.genesisBlock)
	}
	if currentFastBlock := bc.CurrentFastBlock(); currentFastBlock == nil {
		bc.currentFastBlock.Store(bc.genesisBlock)
	}
	currentBlock := bc.CurrentBlock()
	currentFastBlock := bc.CurrentFastBlock()

	rawdb.WriteHeadBlockHash(bc.db, currentBlock.Hash())
	rawdb.WriteHeadFastBlockHash(bc.db, currentFastBlock.Hash())

	return bc.loadLastState()
}

// FastSyncCommitHead sets the current head block to the one defined by the hash
// irrelevant what the chain contents were prior.
// 현재 해드블록을 해시와 무관한 체인 컨텐츠를 중시하는 블록으로 설정한다.
func (bc *BlockChain) FastSyncCommitHead(hash common.Hash) error {
	// Make sure that both the block as well at its state trie exists
	// 블록뿐만아니라, 상태 트라이도 존재하는지를 확인한다
	block := bc.GetBlockByHash(hash)
	if block == nil {
		return fmt.Errorf("non existent block [%x…]", hash[:4])
	}
	if _, err := trie.NewSecure(block.Root(), bc.stateCache.TrieDB(), 0); err != nil {
		return err
	}
	// If all checks out, manually set the head block
	// 모든 체크가 끝나면 헤드블록을 수동으로 설정한다
	bc.mu.Lock()
	bc.currentBlock.Store(block)
	bc.mu.Unlock()

	log.Info("Committed new head block", "number", block.Number(), "hash", hash)
	return nil
}

// GasLimit returns the gas limit of the current HEAD block.
// GasLimit함수는 현재 헤드블록의 가스 한도를 반환한다
func (bc *BlockChain) GasLimit() uint64 {
	return bc.CurrentBlock().GasLimit()
}

// CurrentBlock retrieves the current head block of the canonical chain. The
// block is retrieved from the blockchain's internal cache.
// 이 함수는 캐노니컬 체인의 해드블록을 반환한다. 
// 이 불록은 블록체인의 내부 캐시로부터 반환된다
func (bc *BlockChain) CurrentBlock() *types.Block {
	return bc.currentBlock.Load().(*types.Block)
}

// CurrentFastBlock retrieves the current fast-sync head block of the canonical
// chain. The block is retrieved from the blockchain's internal cache.
// 이 함수는 캐노니컬 체인의 패스트 싱크 헤드 블록을 반환한다. 
// 이 불록은 블록체인의 내부 캐시로부터 반환된다
func (bc *BlockChain) CurrentFastBlock() *types.Block {
	return bc.currentFastBlock.Load().(*types.Block)
}

// SetProcessor sets the processor required for making state modifications.
// 스테이트 변경을 위해 필요한 프로세서를 설정한다
func (bc *BlockChain) SetProcessor(processor Processor) {
	bc.procmu.Lock()
	defer bc.procmu.Unlock()
	bc.processor = processor
}

// SetValidator sets the validator which is used to validate incoming blocks.
// 새로 들어온 블록의 검증을 위한 검증자를 설정한다
func (bc *BlockChain) SetValidator(validator Validator) {
	bc.procmu.Lock()
	defer bc.procmu.Unlock()
	bc.validator = validator
}

// Validator returns the current validator.
// validator함수는 현재 검증자를 반환한다
func (bc *BlockChain) Validator() Validator {
	bc.procmu.RLock()
	defer bc.procmu.RUnlock()
	return bc.validator
}

// Processor returns the current processor.
// Processor 함수는 현재 처리자를 반환한다
func (bc *BlockChain) Processor() Processor {
	bc.procmu.RLock()
	defer bc.procmu.RUnlock()
	return bc.processor
}

// State returns a new mutable state based on the current HEAD block.
// 이 함수는 현재 헤드 블록을 기반으로 새로운 변환 가능한 스테이트를 반환한다 
func (bc *BlockChain) State() (*state.StateDB, error) {
	return bc.StateAt(bc.CurrentBlock().Root())
}

// StateAt returns a new mutable state based on a particular point in time.
// 이 함수는 특정시간의 한 지점을 기반으로 새로운 변환 가능한 스테이트를 반환한다 
func (bc *BlockChain) StateAt(root common.Hash) (*state.StateDB, error) {
	return state.New(root, bc.stateCache)
}

// Reset purges the entire blockchain, restoring it to its genesis state.
// 전체 블록체인을 깨끗이하고 태초 상태로 되돌린다
func (bc *BlockChain) Reset() error {
	return bc.ResetWithGenesisBlock(bc.genesisBlock)
}

// ResetWithGenesisBlock purges the entire blockchain, restoring it to the
// specified genesis state.
// 전체 블록체인을 깨끗이하고 태초 상태로 되돌린다
func (bc *BlockChain) ResetWithGenesisBlock(genesis *types.Block) error {
	// Dump the entire block chain and purge the caches
	if err := bc.SetHead(0); err != nil {
		return err
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()

	// Prepare the genesis block and reinitialise the chain
	// 제네시스 블록을 준비하고, 체인을 재초기화 한다
	if err := bc.hc.WriteTd(genesis.Hash(), genesis.NumberU64(), genesis.Difficulty()); err != nil {
		log.Crit("Failed to write genesis block TD", "err", err)
	}
	rawdb.WriteBlock(bc.db, genesis)

	bc.genesisBlock = genesis
	bc.insert(bc.genesisBlock)
	bc.currentBlock.Store(bc.genesisBlock)
	bc.hc.SetGenesis(bc.genesisBlock.Header())
	bc.hc.SetCurrentHeader(bc.genesisBlock.Header())
	bc.currentFastBlock.Store(bc.genesisBlock)

	return nil
}

// repair tries to repair the current blockchain by rolling back the current block
// until one with associated state is found. This is needed to fix incomplete db
// writes caused either by crashes/power outages, or simply non-committed tries.
//
// This method only rolls back the current block. The current header and current
// fast block are left intact.
// 이 함수는 특정 스테이트가 찾아질때까지 커런트 블록을 롤백하면서 
// 현재 블록 체인을 수정한다. 이 함수는 아직 커밋되지 않은 트라이
// 크래시를 유발하는 미완성의 DB 쓰기 동작을 수정해야 한다

func (bc *BlockChain) repair(head **types.Block) error {
	for {
		// Abort if we've rewound to a head block that does have associated state
		// 만약 상태가 없는 헤드블록을 되돌리려 했다면 abort
		if _, err := state.New((*head).Root(), bc.stateCache); err == nil {
			log.Info("Rewound blockchain to past state", "number", (*head).Number(), "hash", (*head).Hash())
			return nil
		}
		// Otherwise rewind one block and recheck state availability there
		// 아니라면 블록을 하나 되돌리고 상태가 가능한지 다시 체크한다
		(*head) = bc.GetBlock((*head).ParentHash(), (*head).NumberU64()-1)
	}
}

// Export writes the active chain to the given writer.
// Export함수는 활성체인을 주어진 쓰기기능에 쓴다
func (bc *BlockChain) Export(w io.Writer) error {
	return bc.ExportN(w, uint64(0), bc.CurrentBlock().NumberU64())
}

// ExportN writes a subset of the active chain to the given writer.
// Export함수는 활성체인의 일부를 주어진 쓰기기능에 쓴다
func (bc *BlockChain) ExportN(w io.Writer, first uint64, last uint64) error {
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	if first > last {
		return fmt.Errorf("export failed: first (%d) is greater than last (%d)", first, last)
	}
	log.Info("Exporting batch of blocks", "count", last-first+1)

	for nr := first; nr <= last; nr++ {
		block := bc.GetBlockByNumber(nr)
		if block == nil {
			return fmt.Errorf("export failed on #%d: not found", nr)
		}

		if err := block.EncodeRLP(w); err != nil {
			return err
		}
	}

	return nil
}

// insert injects a new head block into the current block chain. This method
// assumes that the block is indeed a true head. It will also reset the head
// header and the head fast sync block to this very same block if they are older
// or if they are on a different side chain.
//
// Note, this function assumes that the `mu` mutex is held!
// 이 함수는 뉴 헤드 블록을 현재 블록체인에 주입한다. 
// 이때 블록이 진짜 헤드일때를 가정한다.
// 이 함수는 헤드의 헤더와 헤드 패스트 싱크블록이 오래되었거나 
// 다른 사이드 체인에 있을 경우 이 블록으로 초기화 할것이다.

func (bc *BlockChain) insert(block *types.Block) {
	// If the block is on a side chain or an unknown one, force other heads onto it too
	// 블록이 사이드체인에 있거나, 모르는 블록일 경우 다른 헤드를 강제한다
	updateHeads := rawdb.ReadCanonicalHash(bc.db, block.NumberU64()) != block.Hash()

	// Add the block to the canonical chain number scheme and mark as the head
	// 블록을 캐노니컬 체인 숫자구조에 추가하고, 헤드로 설정한다
	rawdb.WriteCanonicalHash(bc.db, block.Hash(), block.NumberU64())
	rawdb.WriteHeadBlockHash(bc.db, block.Hash())

	bc.currentBlock.Store(block)

	// If the block is better than our head or is on a different chain, force update heads
	// 블록이 우리의 헤더보다 좋거나, 다른 체인에 있다면, 헤드를 강제로 업데이트 한다
	if updateHeads {
		bc.hc.SetCurrentHeader(block.Header())
		rawdb.WriteHeadFastBlockHash(bc.db, block.Hash())

		bc.currentFastBlock.Store(block)
	}
}

// Genesis retrieves the chain's genesis block.
// Genesis함수는 체인의 제네시스 블록을 반환한다
func (bc *BlockChain) Genesis() *types.Block {
	return bc.genesisBlock
}

// GetBody retrieves a block body (transactions and uncles) from the database by
// hash, caching it if found.
// GetBody함수는 블록의 바디(트렌젝션과 엉클)를 해시를 이용하여 DB로부터 반환하고
// 찾아졌을 경우 캐싱한다
func (bc *BlockChain) GetBody(hash common.Hash) *types.Body {
	// Short circuit if the body's already in the cache, retrieve otherwise
	// Body가 이미 캐시에 존재힐경우 반환한다
	if cached, ok := bc.bodyCache.Get(hash); ok {
		body := cached.(*types.Body)
		return body
	}
	number := bc.hc.GetBlockNumber(hash)
	if number == nil {
		return nil
	}
	body := rawdb.ReadBody(bc.db, hash, *number)
	if body == nil {
		return nil
	}
	// Cache the found body for next time and return
	// 다음을 위해 찾아진 블록바디를 캐싱한다
	bc.bodyCache.Add(hash, body)
	return body
}

// GetBodyRLP retrieves a block body in RLP encoding from the database by hash,
// caching it if found.
// GetBodyRLP함수는 RLP인코딩된 블록의 바디를 해시를 이용하여 DB로 부터 반환하고 캐싱한다
func (bc *BlockChain) GetBodyRLP(hash common.Hash) rlp.RawValue {
	// Short circuit if the body's already in the cache, retrieve otherwise
	// Body가 이미 캐시에 존재힐경우 반환한다
	if cached, ok := bc.bodyRLPCache.Get(hash); ok {
		return cached.(rlp.RawValue)
	}
	number := bc.hc.GetBlockNumber(hash)
	if number == nil {
		return nil
	}
	body := rawdb.ReadBodyRLP(bc.db, hash, *number)
	if len(body) == 0 {
		return nil
	}
	// Cache the found body for next time and return
	// 다음을 위해 찾아진 블록바디를 캐싱한다
	bc.bodyRLPCache.Add(hash, body)
	return body
}

// HasBlock checks if a block is fully present in the database or not.
// HasBlcok함수는 full 블록이 DB에 존재하는지 여부를 체크한다
func (bc *BlockChain) HasBlock(hash common.Hash, number uint64) bool {
	if bc.blockCache.Contains(hash) {
		return true
	}
	return rawdb.HasBody(bc.db, hash, number)
}

// HasState checks if state trie is fully present in the database or not.
// HasState함수는 상태트라이가 모두 db에 존재하는 지를 체크한다
func (bc *BlockChain) HasState(hash common.Hash) bool {
	_, err := bc.stateCache.OpenTrie(hash)
	return err == nil
}

// HasBlockAndState checks if a block and associated state trie is fully present
// in the database or not, caching it if present.
// 이 함수는 블록과 관련된 스테이트 트라이가 DB에 모두 존재하는지 체크한다
func (bc *BlockChain) HasBlockAndState(hash common.Hash, number uint64) bool {
	// Check first that the block itself is known
	// 알려진 블록인지 먼저 확인한다
	block := bc.GetBlock(hash, number)
	if block == nil {
		return false
	}
	return bc.HasState(block.Root())
}

// GetBlock retrieves a block from the database by hash and number,
// caching it if found.
// GetBlock함수는 해시와 번호를 이용해 블록을 반환하고 캐싱한다
func (bc *BlockChain) GetBlock(hash common.Hash, number uint64) *types.Block {
	// Short circuit if the block's already in the cache, retrieve otherwise
	if block, ok := bc.blockCache.Get(hash); ok {
		return block.(*types.Block)
	}
	block := rawdb.ReadBlock(bc.db, hash, number)
	if block == nil {
		return nil
	}
	// Cache the found block for next time and return
	// 다음을 위해 찾아진 블록을 캐싱한다
	bc.blockCache.Add(block.Hash(), block)
	return block
}

// GetBlockByHash retrieves a block from the database by hash, caching it if found.
// GetBlockByHash 함수는 해시를 이용해 DB의 block을 검색하고, 캐싱한다.
func (bc *BlockChain) GetBlockByHash(hash common.Hash) *types.Block {
	number := bc.hc.GetBlockNumber(hash)
	if number == nil {
		return nil
	}
	return bc.GetBlock(hash, *number)
}

// GetBlockByNumber retrieves a block from the database by number, caching it
// (associated with its hash) if found.
// GetBlockByHash 함수는 number를 이용해 DB의 block을 검색하고, 캐싱한다.
func (bc *BlockChain) GetBlockByNumber(number uint64) *types.Block {
	hash := rawdb.ReadCanonicalHash(bc.db, number)
	if hash == (common.Hash{}) {
		return nil
	}
	return bc.GetBlock(hash, number)
}

// GetReceiptsByHash retrieves the receipts for all transactions in a given block.
// GetReceiptsByHash함수는 주어진 블록의 모든 트렌젝션에 대한 영수증을 반환한다
func (bc *BlockChain) GetReceiptsByHash(hash common.Hash) types.Receipts {
	number := rawdb.ReadHeaderNumber(bc.db, hash)
	if number == nil {
		return nil
	}
	return rawdb.ReadReceipts(bc.db, hash, *number)
}

// GetBlocksFromHash returns the block corresponding to hash and up to n-1 ancestors.
// [deprecated by eth/62]
// GetBlocksFromHash함수는 해시와 관련된 블록부터 n-1조상까지 반환한다
// eth/62에서 제거됨
func (bc *BlockChain) GetBlocksFromHash(hash common.Hash, n int) (blocks []*types.Block) {
	number := bc.hc.GetBlockNumber(hash)
	if number == nil {
		return nil
	}
	for i := 0; i < n; i++ {
		block := bc.GetBlock(hash, *number)
		if block == nil {
			break
		}
		blocks = append(blocks, block)
		hash = block.ParentHash()
		*number--
	}
	return
}

// GetUnclesInChain retrieves all the uncles from a given block backwards until
// a specific distance is reached.
// 이 블록의 뒷쪽 특정 거리까지 존재하는 엉클블록을 반환한다
func (bc *BlockChain) GetUnclesInChain(block *types.Block, length int) []*types.Header {
	uncles := []*types.Header{}
	for i := 0; block != nil && i < length; i++ {
		uncles = append(uncles, block.Uncles()...)
		block = bc.GetBlock(block.ParentHash(), block.NumberU64()-1)
	}
	return uncles
}

// TrieNode retrieves a blob of data associated with a trie node (or code hash)
// either from ephemeral in-memory cache, or from persistent storage.
// 이 함수는 트라이 노드나 코드해시에 관련된 데이터 블롭을 반환한다.
// 금방사라질 인메모리 캐시나 잔존하는 저장소에서
func (bc *BlockChain) TrieNode(hash common.Hash) ([]byte, error) {
	return bc.stateCache.TrieDB().Node(hash)
}

// Stop stops the blockchain service. If any imports are currently in progress
// it will abort them using the procInterrupt.
// Stop함수는 블록체인 서비스를 중지한다. 만약 현재 처리중인 입수된 블록은 
// proInterrupt를 이용해 abort한다
func (bc *BlockChain) Stop() {
	if !atomic.CompareAndSwapInt32(&bc.running, 0, 1) {
		return
	}
	// Unsubscribe all subscriptions registered from blockchain
	// 블록체인에 등록되었던 모든 구독을 해지한다
	bc.scope.Close()
	close(bc.quit)
	atomic.StoreInt32(&bc.procInterrupt, 1)

	bc.wg.Wait()

	// Ensure the state of a recent block is also stored to disk before exiting.
	// We're writing three different states to catch different restart scenarios:
	//  - HEAD:     So we don't need to reprocess any blocks in the general case
	//  - HEAD-1:   So we don't do large reorgs if our HEAD becomes an uncle
	//  - HEAD-127: So we have a hard limit on the number of blocks reexecuted
	// 최근 블록의 상태를 보장한다
	// 우리는 재시작 상태를 지원하기 위한 3가지의 상태를 저장할것이다
	// HEAD: 아무 블록도 재검증할필요 없음
	// HEAD-1: 우리의 헤더가 엉클이 되더라도 크게 재구성할필요는없다
	// HEAD-127: 처리해야할 블록의 숫자에 강제제약이 있다
	if !bc.cacheConfig.Disabled {
		triedb := bc.stateCache.TrieDB()

		for _, offset := range []uint64{0, 1, triesInMemory - 1} {
			if number := bc.CurrentBlock().NumberU64(); number > offset {
				recent := bc.GetBlockByNumber(number - offset)

				log.Info("Writing cached state to disk", "block", recent.Number(), "hash", recent.Hash(), "root", recent.Root())
				if err := triedb.Commit(recent.Root(), true); err != nil {
					log.Error("Failed to commit recent state trie", "err", err)
				}
			}
		}
		for !bc.triegc.Empty() {
			triedb.Dereference(bc.triegc.PopItem().(common.Hash), common.Hash{})
		}
		if size := triedb.Size(); size != 0 {
			log.Error("Dangling trie nodes after full cleanup")
		}
	}
	log.Info("Blockchain manager stopped")
}

// 퓨처블록을 처리한다
func (bc *BlockChain) procFutureBlocks() {
	blocks := make([]*types.Block, 0, bc.futureBlocks.Len())
	// 퓨처블록의 키들을 검색하여 키에 해당하는 해시가 있는 블록을 골라낸다.
	for _, hash := range bc.futureBlocks.Keys() {
		if block, exist := bc.futureBlocks.Peek(hash); exist {
			blocks = append(blocks, block.(*types.Block))
		}
	}
	if len(blocks) > 0 {
		//블록넘버로 정렬해서 체인에 추가한다
		types.BlockBy(types.Number).Sort(blocks)

		// Insert one by one as chain insertion needs contiguous ancestry between blocks
		// 이함수는 주어진 블록들을 캐노니컬 체인에 추가하거나, 포크를 생성한다
		// 추가가 끝나면, 누적되었던 모든 이벤트가 발생함
		for i := range blocks {
			bc.InsertChain(blocks[i : i+1])
		}
	}
}

// WriteStatus status of write
// WriteStatus는 쓰기기능의 상태이다
type WriteStatus byte

const (
	NonStatTy WriteStatus = iota
	CanonStatTy
	SideStatTy
)

// Rollback is designed to remove a chain of links from the database that aren't
// certain enough to be valid.
// Rollback함수는 신뢰할만하지 않은 체인의 연결을 DB로 부터 제거하기 위해 디자인되었다
func (bc *BlockChain) Rollback(chain []common.Hash) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	for i := len(chain) - 1; i >= 0; i-- {
		hash := chain[i]

		currentHeader := bc.hc.CurrentHeader()
		if currentHeader.Hash() == hash {
			bc.hc.SetCurrentHeader(bc.GetHeader(currentHeader.ParentHash, currentHeader.Number.Uint64()-1))
		}
		if currentFastBlock := bc.CurrentFastBlock(); currentFastBlock.Hash() == hash {
			newFastBlock := bc.GetBlock(currentFastBlock.ParentHash(), currentFastBlock.NumberU64()-1)
			bc.currentFastBlock.Store(newFastBlock)
			rawdb.WriteHeadFastBlockHash(bc.db, newFastBlock.Hash())
		}
		if currentBlock := bc.CurrentBlock(); currentBlock.Hash() == hash {
			newBlock := bc.GetBlock(currentBlock.ParentHash(), currentBlock.NumberU64()-1)
			bc.currentBlock.Store(newBlock)
			rawdb.WriteHeadBlockHash(bc.db, newBlock.Hash())
		}
	}
}

// SetReceiptsData computes all the non-consensus fields of the receipts
// SetReceiptsData함수는 영수증상의 합의가 아닌 모든 필드를 계산한다
func SetReceiptsData(config *params.ChainConfig, block *types.Block, receipts types.Receipts) error {
	signer := types.MakeSigner(config, block.Number())

	transactions, logIndex := block.Transactions(), uint(0)
	if len(transactions) != len(receipts) {
		return errors.New("transaction and receipt count mismatch")
	}

	for j := 0; j < len(receipts); j++ {
		// The transaction hash can be retrieved from the transaction itself
		// 트렌젝션 해시가 트렌젝션 자체로부터 반환가능할때
		receipts[j].TxHash = transactions[j].Hash()

		// The contract address can be derived from the transaction itself
		// 계약 주소가 트렌젝션 자체로부터 유도가능할때
		if transactions[j].To() == nil {
			// Deriving the signer is expensive, only do if it's actually needed
			// 서명자를 유도하는것은 비싸기때문에 실제로 필요할때만 진행
			from, _ := types.Sender(signer, transactions[j])
			receipts[j].ContractAddress = crypto.CreateAddress(from, transactions[j].Nonce())
		}
		// The used gas can be calculated based on previous receipts
		// 사용된 가스량이 이전 영수증으로부터 계산가능할때
		if j == 0 {
			receipts[j].GasUsed = receipts[j].CumulativeGasUsed
		} else {
			receipts[j].GasUsed = receipts[j].CumulativeGasUsed - receipts[j-1].CumulativeGasUsed
		}
		// The derived log fields can simply be set from the block and transaction
		// 유도된 로그필드는 블록과 트렌젝션으로 부터 쉽게 채울수 있음
		for k := 0; k < len(receipts[j].Logs); k++ {
			receipts[j].Logs[k].BlockNumber = block.NumberU64()
			receipts[j].Logs[k].BlockHash = block.Hash()
			receipts[j].Logs[k].TxHash = receipts[j].TxHash
			receipts[j].Logs[k].TxIndex = uint(j)
			receipts[j].Logs[k].Index = logIndex
			logIndex++
		}
	}
	return nil
}

// InsertReceiptChain attempts to complete an already existing header chain with
// transaction and receipt data.
// 이 함수는 이미 존재하는 헤더체인을 트렌젝션과 영수증 데이터로 완성한다
func (bc *BlockChain) InsertReceiptChain(blockChain types.Blocks, receiptChain []types.Receipts) (int, error) {
	bc.wg.Add(1)
	defer bc.wg.Done()

	// Do a sanity check that the provided chain is actually ordered and linked
	// 전달된 체인이 실제로 주문되었고, 연결되었는지 확인한다
	for i := 1; i < len(blockChain); i++ {
		if blockChain[i].NumberU64() != blockChain[i-1].NumberU64()+1 || blockChain[i].ParentHash() != blockChain[i-1].Hash() {
			log.Error("Non contiguous receipt insert", "number", blockChain[i].Number(), "hash", blockChain[i].Hash(), "parent", blockChain[i].ParentHash(),
				"prevnumber", blockChain[i-1].Number(), "prevhash", blockChain[i-1].Hash())
			return 0, fmt.Errorf("non contiguous insert: item %d is #%d [%x…], item %d is #%d [%x…] (parent [%x…])", i-1, blockChain[i-1].NumberU64(),
				blockChain[i-1].Hash().Bytes()[:4], i, blockChain[i].NumberU64(), blockChain[i].Hash().Bytes()[:4], blockChain[i].ParentHash().Bytes()[:4])
		}
	}

	var (
		stats = struct{ processed, ignored int32 }{}
		start = time.Now()
		bytes = 0
		batch = bc.db.NewBatch()
	)
	for i, block := range blockChain {
		receipts := receiptChain[i]
		// Short circuit insertion if shutting down or processing failed
		// 종료시나 처리가 실패했을 경우 
		if atomic.LoadInt32(&bc.procInterrupt) == 1 {
			return 0, nil
		}
		// Short circuit if the owner header is unknown
		// 헤더가 알려지지 않았을 경우
		if !bc.HasHeader(block.Hash(), block.NumberU64()) {
			return i, fmt.Errorf("containing header #%d [%x…] unknown", block.Number(), block.Hash().Bytes()[:4])
		}
		// Skip if the entire data is already known
		// 모든데이터가 알려진 경우 넘어감
		if bc.HasBlock(block.Hash(), block.NumberU64()) {
			stats.ignored++
			continue
		}
		// Compute all the non-consensus fields of the receipts
		// 영수증상의 합의가 아닌 모든 부분을 계산
		if err := SetReceiptsData(bc.chainConfig, block, receipts); err != nil {
			return i, fmt.Errorf("failed to set receipts data: %v", err)
		}
		// Write all the data out into the database
		// 모든데이터를 DB에 쓴다
		rawdb.WriteBody(batch, block.Hash(), block.NumberU64(), block.Body())
		rawdb.WriteReceipts(batch, block.Hash(), block.NumberU64(), receipts)
		rawdb.WriteTxLookupEntries(batch, block)

		stats.processed++

		if batch.ValueSize() >= ethdb.IdealBatchSize {
			if err := batch.Write(); err != nil {
				return 0, err
			}
			bytes += batch.ValueSize()
			batch.Reset()
		}
	}
	if batch.ValueSize() > 0 {
		bytes += batch.ValueSize()
		if err := batch.Write(); err != nil {
			return 0, err
		}
	}

	// Update the head fast sync block if better
	// fast sync 헤드블록이 더 좋을 경우 업데이트 한다
	bc.mu.Lock()
	head := blockChain[len(blockChain)-1]
	if td := bc.GetTd(head.Hash(), head.NumberU64()); td != nil { // Rewind may have occurred, skip in that case
		currentFastBlock := bc.CurrentFastBlock()
		if bc.GetTd(currentFastBlock.Hash(), currentFastBlock.NumberU64()).Cmp(td) < 0 {
			rawdb.WriteHeadFastBlockHash(bc.db, head.Hash())
			bc.currentFastBlock.Store(head)
		}
	}
	bc.mu.Unlock()

	log.Info("Imported new block receipts",
		"count", stats.processed,
		"elapsed", common.PrettyDuration(time.Since(start)),
		"number", head.Number(),
		"hash", head.Hash(),
		"size", common.StorageSize(bytes),
		"ignored", stats.ignored)
	return 0, nil
}

var lastWrite uint64

// WriteBlockWithoutState writes only the block and its metadata to the database,
// but does not write any state. This is used to construct competing side forks
// up to the point where they exceed the canonical total difficulty.
// 이 함수는 블록과 메타데이타만을 DB에 저장하고, Sate는 쓰지 않는다.
// 이 함수는 캐노니컬 토탈 디피컬티를 초과한 사이드 포크들의 
// 간결한 경쟁을 생성하기 위해 호출된다.
func (bc *BlockChain) WriteBlockWithoutState(block *types.Block, td *big.Int) (err error) {
	bc.wg.Add(1)
	defer bc.wg.Done()

	if err := bc.hc.WriteTd(block.Hash(), block.NumberU64(), td); err != nil {
		return err
	}
	rawdb.WriteBlock(bc.db, block)

	return nil
}

// WriteBlockWithState writes the block and all associated state to the database.
// 블록과 모든 관련된 스테이트를 DB에 저장한다
func (bc *BlockChain) WriteBlockWithState(block *types.Block, receipts []*types.Receipt, state *state.StateDB) (status WriteStatus, err error) {
	bc.wg.Add(1)
	defer bc.wg.Done()

	// Calculate the total difficulty of the block
	// 블록의 TD를 계산한다
	ptd := bc.GetTd(block.ParentHash(), block.NumberU64()-1)
	if ptd == nil {
		return NonStatTy, consensus.ErrUnknownAncestor
	}
	// Make sure no inconsistent state is leaked during insertion
	// 삽입중에 상태가 깨지지 않도록 한다
	bc.mu.Lock()
	defer bc.mu.Unlock()

	currentBlock := bc.CurrentBlock()
	localTd := bc.GetTd(currentBlock.Hash(), currentBlock.NumberU64())
	externTd := new(big.Int).Add(block.Difficulty(), ptd)

	// Irrelevant of the canonical status, write the block itself to the database
	// 캐노니칼 상태와는 무관함. 블록을 DB에 쓴다
	if err := bc.hc.WriteTd(block.Hash(), block.NumberU64(), externTd); err != nil {
		return NonStatTy, err
	}
	// Write other block data using a batch.
	// 다른 블록을 배치로 쓴다
	batch := bc.db.NewBatch()
	rawdb.WriteBlock(batch, block)

	root, err := state.Commit(bc.chainConfig.IsEIP158(block.Number()))
	if err != nil {
		return NonStatTy, err
	}
	triedb := bc.stateCache.TrieDB()

	// If we're running an archive node, always flush
	// Archive모드라면 언제나 flush한다
	if bc.cacheConfig.Disabled {
		if err := triedb.Commit(root, false); err != nil {
			return NonStatTy, err
		}
	} else {
		// Full but not archive node, do proper garbage collection
		// full sync이지만 아카이브 모드가 아닌경우 적당한 가비지 컬렉션을 한다
		triedb.Reference(root, common.Hash{}) // metadata reference to keep trie alive
		// 트라이를 살려놓기 위한 메타데이터 참조
		bc.triegc.Push(root, -float32(block.NumberU64()))

		if current := block.NumberU64(); current > triesInMemory {
			// Find the next state trie we need to commit
			// 커밋해야할 다음 상태 트라이를 찾는다
			header := bc.GetHeaderByNumber(current - triesInMemory)
			chosen := header.Number.Uint64()

			// Only write to disk if we exceeded our memory allowance *and* also have at
			// least a given number of tries gapped.
			// 메모리 허용량을 초과하거나 트라이 간격을 넘어서는 경우 db에 쓴다
			var (
				size  = triedb.Size()
				limit = common.StorageSize(bc.cacheConfig.TrieNodeLimit) * 1024 * 1024
			)
			if size > limit || bc.gcproc > bc.cacheConfig.TrieTimeLimit {
				// If we're exceeding limits but haven't reached a large enough memory gap,
				// warn the user that the system is becoming unstable.
				// 한도를 초과했지만 충분한 메모리 갭에 도달하지 못햇을 경우 
				// 유저에게 시스템이 불안정해질것을 알린다
				if chosen < lastWrite+triesInMemory {
					switch {
					case size >= 2*limit:
						log.Warn("State memory usage too high, committing", "size", size, "limit", limit, "optimum", float64(chosen-lastWrite)/triesInMemory)
					case bc.gcproc >= 2*bc.cacheConfig.TrieTimeLimit:
						log.Info("State in memory for too long, committing", "time", bc.gcproc, "allowance", bc.cacheConfig.TrieTimeLimit, "optimum", float64(chosen-lastWrite)/triesInMemory)
					}
				}
				// If optimum or critical limits reached, write to disk
				if chosen >= lastWrite+triesInMemory || size >= 2*limit || bc.gcproc >= 2*bc.cacheConfig.TrieTimeLimit {
					triedb.Commit(header.Root, true)
					lastWrite = chosen
					bc.gcproc = 0
				}
			}
			// Garbage collect anything below our required write retention
			// 최소/한계 한도에 도달했을 경우 디스크에 쓴다
			// 쓰기를 유지하기 위한 우리의 요청을 가비지 컬렉션한다
			for !bc.triegc.Empty() {
				root, number := bc.triegc.Pop()
				if uint64(-number) > chosen {
					bc.triegc.Push(root, number)
					break
				}
				triedb.Dereference(root.(common.Hash), common.Hash{})
			}
		}
	}
	rawdb.WriteReceipts(batch, block.Hash(), block.NumberU64(), receipts)

	// If the total difficulty is higher than our known, add it to the canonical chain
	// Second clause in the if statement reduces the vulnerability to selfish mining.
	// Please refer to http://www.cs.cornell.edu/~ie53/publications/btcProcFC.pdf
	// 만약 TD가 우리가 알고있는것보다 높을경우, 캐노니컬 체인에 추가한다
	// 두번쩨 if 상태는 이기적인 마이닝의 취약성을 줄여준다
	reorg := externTd.Cmp(localTd) > 0
	currentBlock = bc.CurrentBlock()
	if !reorg && externTd.Cmp(localTd) == 0 {
		// Split same-difficulty blocks by number, then at random
		// 랜덤하게 동일한 난이도의 블록을 숫자로 나눈다
		reorg = block.NumberU64() < currentBlock.NumberU64() || (block.NumberU64() == currentBlock.NumberU64() && mrand.Float64() < 0.5)
	}
	if reorg {
		// Reorganise the chain if the parent is not the head block
		// 부모노드가 헤드가 아닐경우 재구성한다
		if block.ParentHash() != currentBlock.Hash() {
			if err := bc.reorg(currentBlock, block); err != nil {
				return NonStatTy, err
			}
		}
		// Write the positional metadata for transaction/receipt lookups and preimages
		// 트렌젝션과 영수증을 찾고, 사전이미지를 위한 위치 메타데이터를 쓴다
		rawdb.WriteTxLookupEntries(batch, block)
		rawdb.WritePreimages(batch, block.NumberU64(), state.Preimages())

		status = CanonStatTy
	} else {
		status = SideStatTy
	}
	if err := batch.Write(); err != nil {
		return NonStatTy, err
	}

	// Set new head.
	// 새로운 헤드 설정
	if status == CanonStatTy {
		bc.insert(block)
	}
	bc.futureBlocks.Remove(block.Hash())
	return status, nil
}

// InsertChain attempts to insert the given batch of blocks in to the canonical
// chain or, otherwise, create a fork. If an error is returned it will return
// the index number of the failing block as well an error describing what went
// wrong.
//
// After insertion is done, all accumulated events will be fired.
// 이함수는 주어진 블록들을 캐노니컬 체인에 추가하거나, 포크를 생성한다
// 추가가 끝나면, 누적되었던 모든 이벤트가 발생함
func (bc *BlockChain) InsertChain(chain types.Blocks) (int, error) {
	n, events, logs, err := bc.insertChain(chain)
	bc.PostChainEvents(events, logs)
	return n, err
}

// insertChain will execute the actual chain insertion and event aggregation. The
// only reason this method exists as a separate one is to make locking cleaner
// with deferred statements.
// 이함수는 실제 체인에 블록을 추가하고, 이벤트를 누적할것이다
func (bc *BlockChain) insertChain(chain types.Blocks) (int, []interface{}, []*types.Log, error) {
	// Do a sanity check that the provided chain is actually ordered and linked
	// 주어진 체인이 실제로 요청되었고 연결되었는지 확인
	for i := 1; i < len(chain); i++ {
		if chain[i].NumberU64() != chain[i-1].NumberU64()+1 || chain[i].ParentHash() != chain[i-1].Hash() {
			// Chain broke ancestry, log a messge (programming error) and skip insertion
			// 체인이 윗쪽부터 깨졌다면, 메시지를 남기고, 삽입을 취소한다
			log.Error("Non contiguous block insert", "number", chain[i].Number(), "hash", chain[i].Hash(),
				"parent", chain[i].ParentHash(), "prevnumber", chain[i-1].Number(), "prevhash", chain[i-1].Hash())

			return 0, nil, nil, fmt.Errorf("non contiguous insert: item %d is #%d [%x…], item %d is #%d [%x…] (parent [%x…])", i-1, chain[i-1].NumberU64(),
				chain[i-1].Hash().Bytes()[:4], i, chain[i].NumberU64(), chain[i].Hash().Bytes()[:4], chain[i].ParentHash().Bytes()[:4])
		}
	}
	// Pre-checks passed, start the full block imports
	// 사젠 체크 통과, 전체 블록을 입수하기 시작
	bc.wg.Add(1)
	defer bc.wg.Done()

	bc.chainmu.Lock()
	defer bc.chainmu.Unlock()

	// A queued approach to delivering events. This is generally
	// faster than direct delivery and requires much less mutex
	// acquiring.
	// 전송이벤트에 대한 큐잉 방식.
	// 일반적으로 직접 전달하는 것보다 빠르고 훨씬 적은 뮤텍스 요청이 필요하다
	var (
		stats         = insertStats{startTime: mclock.Now()}
		events        = make([]interface{}, 0, len(chain))
		lastCanon     *types.Block
		coalescedLogs []*types.Log
	)
	// Start the parallel header verifier
	// 병렬 헤더 검증을 시작
	headers := make([]*types.Header, len(chain))
	seals := make([]bool, len(chain))

	for i, block := range chain {
		headers[i] = block.Header()
		seals[i] = true
	}
	abort, results := bc.engine.VerifyHeaders(bc, headers, seals)
	defer close(abort)

	// Iterate over the blocks and insert when the verifier permits
	// 검증자의 허가에 따라 블록을 삽입하는 것을 반복한다
	for i, block := range chain {
		// If the chain is terminating, stop processing blocks
		// 체인이 종료될경우 블록처리를 중지함
		if atomic.LoadInt32(&bc.procInterrupt) == 1 {
			log.Debug("Premature abort during blocks processing")
			break
		}
		// If the header is a banned one, straight out abort
		// 헤더가 퇴장당했다면 블록처리를 중지함
		if BadHashes[block.Hash()] {
			bc.reportBlock(block, nil, ErrBlacklistedHash)
			return i, events, coalescedLogs, ErrBlacklistedHash
		}
		// Wait for the block's verification to complete
		// 블록검증이 끝날때까지 대기
		bstart := time.Now()

		err := <-results
		if err == nil {
			err = bc.Validator().ValidateBody(block)
		}
		switch {
		case err == ErrKnownBlock:
			// Block and state both already known. However if the current block is below
			// this number we did a rollback and we should reimport it nonetheless.
			// 블록과 해시가 모두 알려졌을때. 
			// 그러나 만약 현재 블록이 이숫자보다 크다면 롤백하고 재 입수해야 한다.
			if bc.CurrentBlock().NumberU64() >= block.NumberU64() {
				stats.ignored++
				continue
			}

		case err == consensus.ErrFutureBlock:
			// Allow up to MaxFuture second in the future blocks. If this limit is exceeded
			// the chain is discarded and processed at a later time if given.
			// 미래블록을 위한 최대 시간을 설정한다. 
			// 만약 한도가 넘어서면 체인은 버려지고 나중에 처리될것이다
			max := big.NewInt(time.Now().Unix() + maxTimeFutureBlocks)
			if block.Time().Cmp(max) > 0 {
				return i, events, coalescedLogs, fmt.Errorf("future block: %v > %v", block.Time(), max)
			}
			bc.futureBlocks.Add(block.Hash(), block)
			stats.queued++
			continue

		case err == consensus.ErrUnknownAncestor && bc.futureBlocks.Contains(block.ParentHash()):
			bc.futureBlocks.Add(block.Hash(), block)
			stats.queued++
			continue

		case err == consensus.ErrPrunedAncestor:
			// Block competing with the canonical chain, store in the db, but don't process
			// until the competitor TD goes above the canonical TD
			// 블록이 db의 캐노니컬 체인과 경쟁관계이다. 경쟁자의 TD가 캐노니컬 TD보다 
			// 커질때까지 처리하지 않는다
			currentBlock := bc.CurrentBlock()
			localTd := bc.GetTd(currentBlock.Hash(), currentBlock.NumberU64())
			externTd := new(big.Int).Add(bc.GetTd(block.ParentHash(), block.NumberU64()-1), block.Difficulty())
			if localTd.Cmp(externTd) > 0 {
				if err = bc.WriteBlockWithoutState(block, externTd); err != nil {
					return i, events, coalescedLogs, err
				}
				continue
			}
			// Competitor chain beat canonical, gather all blocks from the common ancestor
			// 경쟁자 체인이 합의되었으므로 모든 블록을 공통조상으로 부터 수집한다
			var winner []*types.Block

			parent := bc.GetBlock(block.ParentHash(), block.NumberU64()-1)
			for !bc.HasState(parent.Root()) {
				winner = append(winner, parent)
				parent = bc.GetBlock(parent.ParentHash(), parent.NumberU64()-1)
			}
			for j := 0; j < len(winner)/2; j++ {
				winner[j], winner[len(winner)-1-j] = winner[len(winner)-1-j], winner[j]
			}
			// Import all the pruned blocks to make the state available
			// 스테이트를 가능하게 하기 위해 가지쳐진 모든 블록을 입수한다
			bc.chainmu.Unlock()
			_, evs, logs, err := bc.insertChain(winner)
			bc.chainmu.Lock()
			events, coalescedLogs = evs, logs

			if err != nil {
				return i, events, coalescedLogs, err
			}

		case err != nil:
			bc.reportBlock(block, nil, err)
			return i, events, coalescedLogs, err
		}
		// Create a new statedb using the parent block and report an
		// error if it fails.
		// 새로운 상태 db를 부모블록을 이용해 생성하고 실패할 경우 error를 보고한다
		var parent *types.Block
		if i == 0 {
			parent = bc.GetBlock(block.ParentHash(), block.NumberU64()-1)
		} else {
			parent = chain[i-1]
		}
		state, err := state.New(parent.Root(), bc.stateCache)
		if err != nil {
			return i, events, coalescedLogs, err
		}
		// Process block using the parent state as reference point.
		// 부모의 상태를 참조포인트로 해서 블록을 처리한다
		receipts, logs, usedGas, err := bc.processor.Process(block, state, bc.vmConfig)
		if err != nil {
			bc.reportBlock(block, receipts, err)
			return i, events, coalescedLogs, err
		}
		// Validate the state using the default validator
		// 기본 검증자를 이용해 상태를 검증한다
		err = bc.Validator().ValidateState(block, parent, state, receipts, usedGas)
		if err != nil {
			bc.reportBlock(block, receipts, err)
			return i, events, coalescedLogs, err
		}
		proctime := time.Since(bstart)

		// Write the block to the chain and get the status.
		// 블록을 체인에 쓰고 상태를 얻어온다
		status, err := bc.WriteBlockWithState(block, receipts, state)
		if err != nil {
			return i, events, coalescedLogs, err
		}
		switch status {
		case CanonStatTy:
			log.Debug("Inserted new block", "number", block.Number(), "hash", block.Hash(), "uncles", len(block.Uncles()),
				"txs", len(block.Transactions()), "gas", block.GasUsed(), "elapsed", common.PrettyDuration(time.Since(bstart)))

			coalescedLogs = append(coalescedLogs, logs...)
			blockInsertTimer.UpdateSince(bstart)
			events = append(events, ChainEvent{block, block.Hash(), logs})
			lastCanon = block

			// Only count canonical blocks for GC processing time
			// 가비지 컬럭센 시간을 위한 캐노니컬 블록의 수
			bc.gcproc += proctime

		case SideStatTy:
			log.Debug("Inserted forked block", "number", block.Number(), "hash", block.Hash(), "diff", block.Difficulty(), "elapsed",
				common.PrettyDuration(time.Since(bstart)), "txs", len(block.Transactions()), "gas", block.GasUsed(), "uncles", len(block.Uncles()))

			blockInsertTimer.UpdateSince(bstart)
			events = append(events, ChainSideEvent{block})
		}
		stats.processed++
		stats.usedGas += usedGas
		stats.report(chain, i, bc.stateCache.TrieDB().Size())
	}
	// Append a single chain head event if we've progressed the chain
	// 체인의 처리를 끝냈다면 하나의 체인 헤드 이벤트를 추가한다
	if lastCanon != nil && bc.CurrentBlock().Hash() == lastCanon.Hash() {
		events = append(events, ChainHeadEvent{lastCanon})
	}
	return 0, events, coalescedLogs, nil
}

// insertStats tracks and reports on block insertion.
// 블록의 삽입을 트래킹하고 보고한다
type insertStats struct {
	queued, processed, ignored int
	usedGas                    uint64
	lastIndex                  int
	startTime                  mclock.AbsTime
}

// statsReportLimit is the time limit during import after which we always print
// out progress. This avoids the user wondering what's going on.
// statsReportLimit은 체인 입수증 프로그래스를 출력한 이후의 시간 제한이다.
const statsReportLimit = 8 * time.Second

// report prints statistics if some number of blocks have been processed
// or more than a few seconds have passed since the last message.
// report함수는 블록이 처리되었거나 최종 로그로부터 몇초가 지났을때 상태를 출력한다
func (st *insertStats) report(chain []*types.Block, index int, cache common.StorageSize) {
	// Fetch the timings for the batch
	// batch를 위한 시간을 가져온다
	var (
		now     = mclock.Now()
		elapsed = time.Duration(now) - time.Duration(st.startTime)
	)
	// If we're at the last block of the batch or report period reached, log
	// 만약 우리가 배치상의 마지막 블록에 있거나, 보고 시간에 도달했을 경우 로깅한다
	if index == len(chain)-1 || elapsed >= statsReportLimit {
		var (
			end = chain[index]
			txs = countTransactions(chain[st.lastIndex : index+1])
		)
		context := []interface{}{
			"blocks", st.processed, "txs", txs, "mgas", float64(st.usedGas) / 1000000,
			"elapsed", common.PrettyDuration(elapsed), "mgasps", float64(st.usedGas) * 1000 / float64(elapsed),
			"number", end.Number(), "hash", end.Hash(), "cache", cache,
		}
		if st.queued > 0 {
			context = append(context, []interface{}{"queued", st.queued}...)
		}
		if st.ignored > 0 {
			context = append(context, []interface{}{"ignored", st.ignored}...)
		}
		log.Info("Imported new chain segment", context...)

		*st = insertStats{startTime: now, lastIndex: index + 1}
	}
}

func countTransactions(chain []*types.Block) (c int) {
	for _, b := range chain {
		c += len(b.Transactions())
	}
	return c
}

// reorgs takes two blocks, an old chain and a new chain and will reconstruct the blocks and inserts them
// to be part of the new canonical chain and accumulates potential missing transactions and post an
// event about them
// 이 함수는 두개의 블록을 재생산하여 캐노니컬 채인의 일부로 넣고,
// 놓칠 가능성이 있는 트렌젝션을 누적한 후 그들에 대한 이벤트를 발생시킨다
func (bc *BlockChain) reorg(oldBlock, newBlock *types.Block) error {
	var (
		newChain    types.Blocks
		oldChain    types.Blocks
		commonBlock *types.Block
		deletedTxs  types.Transactions
		deletedLogs []*types.Log
		// collectLogs collects the logs that were generated during the
		// processing of the block that corresponds with the given hash.
		// These logs are later announced as deleted.
		// collectLogs는 주어진 해시에 대해 블록을 처리하는 동안 발생한 로그의 모임이다.
		// 이 로그들은 나중에 알려지고 삭제된다
		collectLogs = func(hash common.Hash) {
			// Coalesce logs and set 'Removed'.
			number := bc.hc.GetBlockNumber(hash)
			if number == nil {
				return
			}
			receipts := rawdb.ReadReceipts(bc.db, hash, *number)
			for _, receipt := range receipts {
				for _, log := range receipt.Logs {
					del := *log
					del.Removed = true
					deletedLogs = append(deletedLogs, &del)
				}
			}
		}
	)

	// first reduce whoever is higher bound
	// 최조로는 높은 번호를 줄인다
	if oldBlock.NumberU64() > newBlock.NumberU64() {
		// reduce old chain
		// old chain을 줄인다
		for ; oldBlock != nil && oldBlock.NumberU64() != newBlock.NumberU64(); oldBlock = bc.GetBlock(oldBlock.ParentHash(), oldBlock.NumberU64()-1) {
			oldChain = append(oldChain, oldBlock)
			deletedTxs = append(deletedTxs, oldBlock.Transactions()...)

			collectLogs(oldBlock.Hash())
		}
	} else {
		// reduce new chain and append new chain blocks for inserting later on
		// 새로운 체인을 줄이고 나중에 추가하기 위한 새로운 체인블록을 추가한다
		for ; newBlock != nil && newBlock.NumberU64() != oldBlock.NumberU64(); newBlock = bc.GetBlock(newBlock.ParentHash(), newBlock.NumberU64()-1) {
			newChain = append(newChain, newBlock)
		}
	}
	if oldBlock == nil {
		return fmt.Errorf("Invalid old chain")
	}
	if newBlock == nil {
		return fmt.Errorf("Invalid new chain")
	}

	for {
		if oldBlock.Hash() == newBlock.Hash() {
			commonBlock = oldBlock
			break
		}

		oldChain = append(oldChain, oldBlock)
		newChain = append(newChain, newBlock)
		deletedTxs = append(deletedTxs, oldBlock.Transactions()...)
		collectLogs(oldBlock.Hash())

		oldBlock, newBlock = bc.GetBlock(oldBlock.ParentHash(), oldBlock.NumberU64()-1), bc.GetBlock(newBlock.ParentHash(), newBlock.NumberU64()-1)
		if oldBlock == nil {
			return fmt.Errorf("Invalid old chain")
		}
		if newBlock == nil {
			return fmt.Errorf("Invalid new chain")
		}
	}
	// Ensure the user sees large reorgs
	if len(oldChain) > 0 && len(newChain) > 0 {
		logFn := log.Debug
		if len(oldChain) > 63 {
			logFn = log.Warn
		}
		logFn("Chain split detected", "number", commonBlock.Number(), "hash", commonBlock.Hash(),
			"drop", len(oldChain), "dropfrom", oldChain[0].Hash(), "add", len(newChain), "addfrom", newChain[0].Hash())
	} else {
		log.Error("Impossible reorg, please file an issue", "oldnum", oldBlock.Number(), "oldhash", oldBlock.Hash(), "newnum", newBlock.Number(), "newhash", newBlock.Hash())
	}
	// Insert the new chain, taking care of the proper incremental order
	// 새로운 체인을 추가하고 정렬한다
	var addedTxs types.Transactions
	for i := len(newChain) - 1; i >= 0; i-- {
		// insert the block in the canonical way, re-writing history
		// 블록을 합의된곳에 추가하고 히스토리를 새로 쓴다.
		bc.insert(newChain[i])
		// write lookup entries for hash based transaction/receipt searches
		// 해시 베이스의 트렌젝션과 영수증 검색을 위해 룩업 엔트리를 쓴다
		rawdb.WriteTxLookupEntries(bc.db, newChain[i])
		addedTxs = append(addedTxs, newChain[i].Transactions()...)
	}
	// calculate the difference between deleted and added transactions
	// 삭제된것과 추가된 트렌젝션사이의 차이를 계산한다
	diff := types.TxDifference(deletedTxs, addedTxs)
	// When transactions get deleted from the database that means the
	// receipts that were created in the fork must also be deleted
	// 포크상에서 db로 부터 삭제된(영수증이 생성된) 트렌젝션은 삭제되어야 한다
	for _, tx := range diff {
		rawdb.DeleteTxLookupEntry(bc.db, tx.Hash())
	}
	if len(deletedLogs) > 0 {
		go bc.rmLogsFeed.Send(RemovedLogsEvent{deletedLogs})
	}
	if len(oldChain) > 0 {
		go func() {
			for _, block := range oldChain {
				bc.chainSideFeed.Send(ChainSideEvent{Block: block})
			}
		}()
	}

	return nil
}

// PostChainEvents iterates over the events generated by a chain insertion and
// posts them into the event feed.
// TODO: Should not expose PostChainEvents. The chain events should be posted in WriteBlock.
// 이 함수는 체인 삽입에서 발생하는 이벤트나 이벤트 피드로 던진 이벤트들을 반복처리한다.
// 체인이벤트는 WriteBlock함수에서 발생해야 한다.
func (bc *BlockChain) PostChainEvents(events []interface{}, logs []*types.Log) {
	// post event logs for further processing
	// 추가 처리를 위한 이벤트 로그의 전송
	if logs != nil {
		bc.logsFeed.Send(logs)
	}
	for _, event := range events {
		switch ev := event.(type) {
		case ChainEvent:
			bc.chainFeed.Send(ev)

		case ChainHeadEvent:
			bc.chainHeadFeed.Send(ev)

		case ChainSideEvent:
			bc.chainSideFeed.Send(ev)
		}
	}
}

// 5s ticker
// 퓨처블록을 5초마다 처리한다
func (bc *BlockChain) update() {
	futureTimer := time.NewTicker(5 * time.Second)
	defer futureTimer.Stop()
	for {
		select {
		case <-futureTimer.C:
			bc.procFutureBlocks()
		case <-bc.quit:
			return
		}
	}
}

// BadBlockArgs represents the entries in the list returned when bad blocks are queried.
// BadBlockArgs 배드블록이 조회되었을때 리스트의 엔트리를 나타낸다
type BadBlockArgs struct {
	Hash   common.Hash   `json:"hash"`
	Header *types.Header `json:"header"`
}

// BadBlocks returns a list of the last 'bad blocks' that the client has seen on the network
// BadBlocks함수는 클라이언트가 네트워크에서 발견한 최근 배드블록의 리스트를 반환한다
func (bc *BlockChain) BadBlocks() ([]BadBlockArgs, error) {
	headers := make([]BadBlockArgs, 0, bc.badBlocks.Len())
	for _, hash := range bc.badBlocks.Keys() {
		if hdr, exist := bc.badBlocks.Peek(hash); exist {
			header := hdr.(*types.Header)
			headers = append(headers, BadBlockArgs{header.Hash(), header})
		}
	}
	return headers, nil
}

// addBadBlock adds a bad block to the bad-block LRU cache
// addBadBlock함수는 배드블록을 LRU캐시에 추가한다
func (bc *BlockChain) addBadBlock(block *types.Block) {
	bc.badBlocks.Add(block.Header().Hash(), block.Header())
}

// reportBlock logs a bad block error.
// reportBlock함수는 배드블록에러를 기록한다
func (bc *BlockChain) reportBlock(block *types.Block, receipts types.Receipts, err error) {
	bc.addBadBlock(block)

	var receiptString string
	for _, receipt := range receipts {
		receiptString += fmt.Sprintf("\t%v\n", receipt)
	}
	log.Error(fmt.Sprintf(`
########## BAD BLOCK #########
Chain config: %v

Number: %v
Hash: 0x%x
%v

Error: %v
##############################
`, bc.chainConfig, block.Number(), block.Hash(), receiptString, err))
}

// InsertHeaderChain attempts to insert the given header chain in to the local
// chain, possibly creating a reorg. If an error is returned, it will return the
// index number of the failing header as well an error describing what went wrong.
//
// The verify parameter can be used to fine tune whether nonce verification
// should be done or not. The reason behind the optional check is because some
// of the header retrieval mechanisms already need to verify nonces, as well as
// because nonces can be verified sparsely, not needing to check each.

// 이함수는 주어진 헤더체인을 로컬 체인에 넣으려고 노력하는데 reorg를 생성할수도 있다.
// 만약 에러가 리턴되면 에러 로그와 함께 헤더의 인덱스 번호를 리턴한다.

func (bc *BlockChain) InsertHeaderChain(chain []*types.Header, checkFreq int) (int, error) {
	start := time.Now()
	if i, err := bc.hc.ValidateHeaderChain(chain, checkFreq); err != nil {
		return i, err
	}

	// Make sure only one thread manipulates the chain at once
	// 한번에 1개의 스레드 만이 체인을 취급한다
	bc.chainmu.Lock()
	defer bc.chainmu.Unlock()

	bc.wg.Add(1)
	defer bc.wg.Done()

	whFunc := func(header *types.Header) error {
		bc.mu.Lock()
		defer bc.mu.Unlock()

		_, err := bc.hc.WriteHeader(header)
		return err
	}

	return bc.hc.InsertHeaderChain(chain, whFunc, start)
}

// writeHeader writes a header into the local chain, given that its parent is
// already known. If the total difficulty of the newly inserted header becomes
// greater than the current known TD, the canonical chain is re-routed.
//
// Note: This method is not concurrent-safe with inserting blocks simultaneously
// into the chain, as side effects caused by reorganisations cannot be emulated
// without the real blocks. Hence, writing headers directly should only be done
// in two scenarios: pure-header mode of operation (light clients), or properly
// separated header/block phases (non-archive clients).
// 이 함수는 헤더의 부모가 이미 알려진 헤더를 로컬체인에 쓴다.
// 만약 새롭게 삽입할 블록헤더의 토탈 디피컬티가 지금의 것보다 커진다면
// 캐노니컬 체인이 재 배치 된다.
// 라이트 클라이언트의 퓨어헤더모드의 동작이거나, 
// 헤더와 블록이 분리된 페이즈에서만 직접 부르는것이 좋다
func (bc *BlockChain) writeHeader(header *types.Header) error {
	bc.wg.Add(1)
	defer bc.wg.Done()

	bc.mu.Lock()
	defer bc.mu.Unlock()

	_, err := bc.hc.WriteHeader(header)
	return err
}

// CurrentHeader retrieves the current head header of the canonical chain. The
// header is retrieved from the HeaderChain's internal cache.
// 이 함수는 캐노니컬 체인의 현재 해드블록의 헤더를 반환한다.
// 해더는 헤더 체인의 내부 캐시로 부터 반환된다
func (bc *BlockChain) CurrentHeader() *types.Header {
	return bc.hc.CurrentHeader()
}

// GetTd retrieves a block's total difficulty in the canonical chain from the
// database by hash and number, caching it if found.
// 이 함수는 캐노니컬 체인안의 블록의 토탈 디피컬티를 
// 해시와 블록넘버로 찾아 DB로부터 반환하고 캐싱한다
func (bc *BlockChain) GetTd(hash common.Hash, number uint64) *big.Int {
	return bc.hc.GetTd(hash, number)
}

// GetTdByHash retrieves a block's total difficulty in the canonical chain from the
// database by hash, caching it if found.
// 이 함수는 캐노니컬 체인안의 블록의 토탈 디피컬티를 해시로 찾아 DB로부터 반환하고 캐싱한다
func (bc *BlockChain) GetTdByHash(hash common.Hash) *big.Int {
	return bc.hc.GetTdByHash(hash)
}

// GetHeader retrieves a block header from the database by hash and number,
// caching it if found.
// 이 함수는 hash와 블록번호를 이용하여 DB로 부터 블록헤더를 반환하고 캐싱한다
func (bc *BlockChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	return bc.hc.GetHeader(hash, number)
}

// GetHeaderByHash retrieves a block header from the database by hash, caching it if
// found.
// 이 함수는 hash호를 이용하여 DB로 부터 블록헤더를 반환하고 캐싱한다
func (bc *BlockChain) GetHeaderByHash(hash common.Hash) *types.Header {
	return bc.hc.GetHeaderByHash(hash)
}

// HasHeader checks if a block header is present in the database or not, caching
// it if present.
// HasHeader 함수는 블록헤더가 DB에 존재하는지 확인하고 존재한다면 캐싱한다
func (bc *BlockChain) HasHeader(hash common.Hash, number uint64) bool {
	return bc.hc.HasHeader(hash, number)
}

// GetBlockHashesFromHash retrieves a number of block hashes starting at a given
// hash, fetching towards the genesis block.
// GetBlockHashesFromHash함수는 주어진 해시부터 제네시스 방향으로 n개의 블록해시를 반환한다
func (bc *BlockChain) GetBlockHashesFromHash(hash common.Hash, max uint64) []common.Hash {
	return bc.hc.GetBlockHashesFromHash(hash, max)
}

// GetHeaderByNumber retrieves a block header from the database by number,
// caching it (associated with its hash) if found.
// GetHeaderByNumber 함수는 숫자를 이용해 DB로부터 블록 헤더를 반환하고 찾
// 아진다면 캐싱한다
func (bc *BlockChain) GetHeaderByNumber(number uint64) *types.Header {
	return bc.hc.GetHeaderByNumber(number)
}

// Config retrieves the blockchain's chain configuration.
// Config함수는 블록체인의 설정을 반환한다
func (bc *BlockChain) Config() *params.ChainConfig { return bc.chainConfig }

// Engine retrieves the blockchain's consensus engine.
// Engine함수는 블록체인의 합의 엔진을 반환한다
func (bc *BlockChain) Engine() consensus.Engine { return bc.engine }

// SubscribeRemovedLogsEvent registers a subscription of RemovedLogsEvent.
// SubscribeRemovedLogsEvent 함수는 RemovedLogsEvent의 구독을 등록한다
func (bc *BlockChain) SubscribeRemovedLogsEvent(ch chan<- RemovedLogsEvent) event.Subscription {
	return bc.scope.Track(bc.rmLogsFeed.Subscribe(ch))
}

// SubscribeChainEvent registers a subscription of ChainEvent.
// 체인 이벤트 구독
// 전달된 채널을 블록체인의 피드에 추가하고 블록체인의 스코프에서 트렉킹함
func (bc *BlockChain) SubscribeChainEvent(ch chan<- ChainEvent) event.Subscription {
	return bc.scope.Track(bc.chainFeed.Subscribe(ch))
}

// SubscribeChainHeadEvent registers a subscription of ChainHeadEvent.
// 체인헤드 이벤트 구독
// 전달된 채널을 블록체인의 피드에 추가하고 블록체인의 스코프에서 트렉킹함
func (bc *BlockChain) SubscribeChainHeadEvent(ch chan<- ChainHeadEvent) event.Subscription {
	return bc.scope.Track(bc.chainHeadFeed.Subscribe(ch))
}

// SubscribeChainSideEvent registers a subscription of ChainSideEvent.
// ChainSideEvent는 엉클블락의 발생가능성을 체크하기위한 이벤트이다
func (bc *BlockChain) SubscribeChainSideEvent(ch chan<- ChainSideEvent) event.Subscription {
	return bc.scope.Track(bc.chainSideFeed.Subscribe(ch))
}

// SubscribeLogsEvent registers a subscription of []*types.Log.
// SubscribeLogEvent 함수는 log 구독을 등록한다
func (bc *BlockChain) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return bc.scope.Track(bc.logsFeed.Subscribe(ch))
}
