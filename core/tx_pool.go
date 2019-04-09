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

package core

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
	"gopkg.in/karalabe/cookiejar.v2/collections/prque"
)

const (
	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	// chainHeadCahnSize는 체인 헤드 이벤트를 대기할 채널의 크기이다
	chainHeadChanSize = 10
)

var (
	// ErrInvalidSender is returned if the transaction contains an invalid signature.
	// ErrInvalidSender는 트렌젝션이 유효하지 않은 사인을 포함할 경우 반환된다
	ErrInvalidSender = errors.New("invalid sender")

	// ErrNonceTooLow is returned if the nonce of a transaction is lower than the
	// one present in the local chain.
	// ErrNonceTooLow는 현재 로컬 체인에 나타는 트렌젝션의 nonce보다 낮을경우 반환된다
	ErrNonceTooLow = errors.New("nonce too low")

	// ErrUnderpriced is returned if a transaction's gas price is below the minimum
	// configured for the transaction pool.
	// ErrUnderpriced는 트렌젝션의 가스 가격이 트렌젝션 풀의 최소 설정값보다 낮을때 반환된다
	ErrUnderpriced = errors.New("transaction underpriced")

	// ErrReplaceUnderpriced is returned if a transaction is attempted to be replaced
	// with a different one without the required price bump.
	// ErrReplaceUnderpriced는 트렌젝션이 요구된 가격상승 없이 다른 트렌젝션으로 교체되려할때 반환된다
	ErrReplaceUnderpriced = errors.New("replacement transaction underpriced")

	// ErrInsufficientFunds is returned if the total cost of executing a transaction
	// is higher than the balance of the user's account.
	// ErrInsufficientFunds는실행되는 전체 트렌젝션의 가격이 유저가 가진 잔고보다 높을때 발생한다
	ErrInsufficientFunds = errors.New("insufficient funds for gas * price + value")

	// ErrIntrinsicGas is returned if the transaction is specified to use less gas
	// than required to start the invocation.
	// ErrIntrinsicGas는 만약 트렌젝션이 시작가스값보다 낮은 값의 가스를 사용했을때 발생한다
	ErrIntrinsicGas = errors.New("intrinsic gas too low")

	// ErrGasLimit is returned if a transaction's requested gas limit exceeds the
	// maximum allowance of the current block.
	// ErrGasLimit은 트렌젝션이 요구하는 가스 한도가 현재 블록의 허용하는
	// 최대 가스보다 클경우 발생한다
	ErrGasLimit = errors.New("exceeds block gas limit")

	// ErrNegativeValue is a sanity error to ensure noone is able to specify a
	// transaction with a negative value.
	// ErrNegativeValue는 음수값을 지정하는 트렌젝션을 확인하기위한 무결성에러
	ErrNegativeValue = errors.New("negative value")

	// ErrOversizedData is returned if the input data of a transaction is greater
	// than some meaningful limit a user might use. This is not a consensus error
	// making the transaction invalid, rather a DOS protection.
	// ErrOversizedData는 주어진 트렌젝션의 데이터가 의미있는 값을 넘길경우 발생한다
	// 합의 에러는 아니며 DOS 보호에 가깝다
	ErrOversizedData = errors.New("oversized data")
)

var (
	evictionInterval    = time.Minute     // Time interval to check for evictable transactions
	statsReportInterval = 8 * time.Second // Time interval to report transaction pool stats
	// 최출가능한 트렌젝션의 타임 인터벌
	// 트렌젝션 풀의 상태를 보고할 인터벌
)

var (
	// Metrics for the pending pool
	// pending풀에 대한 상태 메트릭스
	pendingDiscardCounter   = metrics.NewRegisteredCounter("txpool/pending/discard", nil)
	pendingReplaceCounter   = metrics.NewRegisteredCounter("txpool/pending/replace", nil)
	pendingRateLimitCounter = metrics.NewRegisteredCounter("txpool/pending/ratelimit", nil) // Dropped due to rate limiting
	pendingNofundsCounter   = metrics.NewRegisteredCounter("txpool/pending/nofunds", nil)   // Dropped due to out-of-funds

	// Metrics for the queued pool
	// queued풀에대한 상태 메트릭스
	queuedDiscardCounter   = metrics.NewRegisteredCounter("txpool/queued/discard", nil)
	queuedReplaceCounter   = metrics.NewRegisteredCounter("txpool/queued/replace", nil)
	queuedRateLimitCounter = metrics.NewRegisteredCounter("txpool/queued/ratelimit", nil) // Dropped due to rate limiting
	queuedNofundsCounter   = metrics.NewRegisteredCounter("txpool/queued/nofunds", nil)   // Dropped due to out-of-funds

	// General tx metrics
	// 일반적인 트렌젝션 메트릭스
	invalidTxCounter     = metrics.NewRegisteredCounter("txpool/invalid", nil)
	underpricedTxCounter = metrics.NewRegisteredCounter("txpool/underpriced", nil)
)

// TxStatus is the current status of a transaction as seen by the pool.
// 풀에존재하는 트렌젝션의 현재상태이다
type TxStatus uint

const (
	TxStatusUnknown TxStatus = iota
	TxStatusQueued
	TxStatusPending
	TxStatusIncluded
)

// blockChain provides the state of blockchain and current gas limit to do
// some pre checks in tx pool and event subscribers.
// 블록체인 인터페이스는 txpool과 이벤트 등록자들이 어떤 선행 검증을 할수 있도록
// 블록체인의 상태와 현재 가스 리밋을 제공한다
type blockChain interface {
	CurrentBlock() *types.Block
	GetBlock(hash common.Hash, number uint64) *types.Block
	StateAt(root common.Hash) (*state.StateDB, error)

	SubscribeChainHeadEvent(ch chan<- ChainHeadEvent) event.Subscription
}

// TxPoolConfig are the configuration parameters of the transaction pool.
// TxPoolConfig는 트렌젝션풀의 설정파라미터이다
type TxPoolConfig struct {
	NoLocals  bool          // Whether local transaction handling should be disabled
	// 로컬 트렌젝션을 처리하지 않음
	Journal   string        // Journal of local transactions to survive node restarts
	// 노드가 재시작되더라도 생존하기 위한 로컬 트렌젝션의 저널
	Rejournal time.Duration // Time interval to regenerate the local transaction journal
	// 로컬 트렌젝션의 저널을 새로 생성할때의 시간 인터벌

	PriceLimit uint64 // Minimum gas price to enforce for acceptance into the pool
	// 풀에 허용되기 위한 최소 가스
	PriceBump  uint64 // Minimum price bump percentage to replace an already existing transaction (nonce)
	// 이미 존재하는 트렌젝션을 대체하기 위한 최소 증가 가격

	AccountSlots uint64 // Minimum number of executable transaction slots guaranteed per account
	GlobalSlots  uint64 // Maximum number of executable transaction slots for all accounts
	AccountQueue uint64 // Maximum number of non-executable transaction slots permitted per account
	GlobalQueue  uint64 // Maximum number of non-executable transaction slots for all accounts
	// 단일 계정당 실행가능한 최소 트렌젝션 슬롯
	// 모든 계정당 실행가능한 최소 트렌젝션 슬롯
	// 단일 계정당 실행불가능한 최소 트렌젝션 슬롯
	// 모든 계정당 실행불가능한 최소 트렌젝션 슬롯

	Lifetime time.Duration // Maximum amount of time non-executable transaction are queued
	// 실행가능하지 않은 트렌잭션이 큐된 최대 시간
}

// DefaultTxPoolConfig contains the default configurations for the transaction
// pool.
// tx pool 기본설정
// 어카운트당 보장가능한 실행가능 트렌젝션수
// 모든 어카운트가 사용가능한 실행가능 트렌젝션수
// 어카운트당 보장가능한 아직 실행불가한 트렌젝션수
// 모든 어카운트가 사용가능한 아직 실행 불가능한 트렌젝션수
var DefaultTxPoolConfig = TxPoolConfig{
	Journal:   "transactions.rlp",
	Rejournal: time.Hour,

	PriceLimit: 1,
	PriceBump:  10,

	AccountSlots: 16,
	GlobalSlots:  4096,
	AccountQueue: 64,
	GlobalQueue:  1024,

	Lifetime: 3 * time.Hour,
}

// sanitize checks the provided user configurations and changes anything that's
// unreasonable or unworkable.
// sanitize함수는 전달된 유저의 설정을 체크하고, 이유없거나 수행 불가능한 변화를 바꾼다
func (config *TxPoolConfig) sanitize() TxPoolConfig {
	conf := *config
	if conf.Rejournal < time.Second {
		log.Warn("Sanitizing invalid txpool journal time", "provided", conf.Rejournal, "updated", time.Second)
		conf.Rejournal = time.Second
	}
	if conf.PriceLimit < 1 {
		log.Warn("Sanitizing invalid txpool price limit", "provided", conf.PriceLimit, "updated", DefaultTxPoolConfig.PriceLimit)
		conf.PriceLimit = DefaultTxPoolConfig.PriceLimit
	}
	if conf.PriceBump < 1 {
		log.Warn("Sanitizing invalid txpool price bump", "provided", conf.PriceBump, "updated", DefaultTxPoolConfig.PriceBump)
		conf.PriceBump = DefaultTxPoolConfig.PriceBump
	}
	return conf
}

// TxPool contains all currently known transactions. Transactions
// enter the pool when they are received from the network or submitted
// locally. They exit the pool when they are included in the blockchain.
//
// The pool separates processable transactions (which can be applied to the
// current state) and future transactions. Transactions move between those
// two states over time as they are received and processed.
// 트렌젝션 풀은 현재까지 알려진 모든 트렌젝션을 포함한다.
// 네트워크를 통해 수신되거나, 로컬하게 생성된 트렌젝션이 풀에 들어가게 된다.
// 트렌젝션이 블록체인에 포함되면, 풀에서 나가게 된다.

//풀은 현재 상태에 적용가능한 처리가능 트렌젝션과 퓨처트렌젝션으로 나뉜다.
// 트렌젝션들은 그들의 수신/처리에 따라 이 두 스테이트를 오간다
type TxPool struct {
	config       TxPoolConfig
	chainconfig  *params.ChainConfig
	chain        blockChain
	gasPrice     *big.Int
	txFeed       event.Feed
	scope        event.SubscriptionScope
	chainHeadCh  chan ChainHeadEvent
	chainHeadSub event.Subscription
	signer       types.Signer
	mu           sync.RWMutex

	currentState  *state.StateDB      // Current state in the blockchain head
	pendingState  *state.ManagedState // Pending state tracking virtual nonces
	currentMaxGas uint64              // Current gas limit for transaction caps
	// 블록체인 헤드의 현재상태
	// 가상 논스를 트렉킹하는 대기 상태
	// 트렌젝션 한도를 위한 가스 제한

	locals  *accountSet // Set of local transaction to exempt from eviction rules
	journal *txJournal  // Journal of local transaction to back up to disk
	// 로컬트렌젝션을 디스크 백업할 저널

	pending map[common.Address]*txList         // All currently processable transactions
	queue   map[common.Address]*txList         // Queued but non-processable transactions
	beats   map[common.Address]time.Time       // Last heartbeat from each known account
	all     map[common.Hash]*types.Transaction // All transactions to allow lookups
	priced  *txPricedList                      // All transactions sorted by price
	// 현재까지 처리된 트렌젝션들
	// 큐잉되었으나 처리불가능한 트렌젝션들
	// 각 계정의 마지막 하트비트
	// 검색을 허용하기 위한 모든 트렌젝션들
	// 가격으로 정렬된 트렌젝션들

	wg sync.WaitGroup // for shutdown sync

	homestead bool
}

// NewTxPool creates a new transaction pool to gather, sort and filter inbound
// transactions from the network.
// 이함수는 네트워크로 부터 들어오는 트렌젝션들을 수집하고 정렬하고 필터링할 새로운 트렌젝션 풀을 생성한다 
func NewTxPool(config TxPoolConfig, chainconfig *params.ChainConfig, chain blockChain) *TxPool {
	// Sanitize the input to ensure no vulnerable gas prices are set
	// 입력과, 가스갑을 확인
	config = (&config).sanitize()

	// Create the transaction pool with its initial settings
	// 초기 설정으로 트렌젝션 풀을 생성
	pool := &TxPool{
		config:      config,
		chainconfig: chainconfig,
		chain:       chain,
		signer:      types.NewEIP155Signer(chainconfig.ChainId),
		pending:     make(map[common.Address]*txList),
		queue:       make(map[common.Address]*txList),
		beats:       make(map[common.Address]time.Time),
		all:         make(map[common.Hash]*types.Transaction),
		chainHeadCh: make(chan ChainHeadEvent, chainHeadChanSize),
		gasPrice:    new(big.Int).SetUint64(config.PriceLimit),
	}
	pool.locals = newAccountSet(pool.signer)
	pool.priced = newTxPricedList(&pool.all)
	pool.reset(nil, chain.CurrentBlock().Header())

	// If local transactions and journaling is enabled, load from disk
	// 로컬트렌젝션과 저널링이 켜져있다면 디스크로 부터 읽는다
	if !config.NoLocals && config.Journal != "" {
		pool.journal = newTxJournal(config.Journal)

		if err := pool.journal.load(pool.AddLocals); err != nil {
			log.Warn("Failed to load transaction journal", "err", err)
		}
		if err := pool.journal.rotate(pool.local()); err != nil {
			log.Warn("Failed to rotate transaction journal", "err", err)
		}
	}
	// Subscribe events from blockchain
	// 블록체인의 체인 헤드 이벤트를 구독한다
	// 전달된 채널을 블록체인의 피드에 추가하고 블록체인의 스코프에서 트렉킹함
	pool.chainHeadSub = pool.chain.SubscribeChainHeadEvent(pool.chainHeadCh)

	// Start the event loop and return
	// 풀루프를 시작한다
	pool.wg.Add(1)
	go pool.loop()

	return pool
}

// loop is the transaction pool's main event loop, waiting for and reacting to
// outside blockchain events as well as for various reporting and transaction
// eviction events.
// 트렌젝션 풀의 메인 루프로서 다양한 레포팅이나 트렌젝션 퇴거 뿐만 아니라 
// 외부 불록체인 이벤트를 기다리거나 반응한다
func (pool *TxPool) loop() {
	defer pool.wg.Done()

	// Start the stats reporting and transaction eviction tickers
	// 상태보고를 시작한다 
	var prevPending, prevQueued, prevStales int

	report := time.NewTicker(statsReportInterval)
	defer report.Stop()

	evict := time.NewTicker(evictionInterval)
	defer evict.Stop()

	journal := time.NewTicker(pool.config.Rejournal)
	defer journal.Stop()

	// Track the previous head headers for transaction reorgs
	// 트렌젝션 재구성을 위해 기존 헤드 헤더들을 트랙킹한다
	head := pool.chain.CurrentBlock()

	// Keep waiting for and reacting to the various events
	// 다양한 이벤트 처리
	for {
		select {
		// Handle ChainHeadEvent
		// 블록체인에서 체인 헤드 이벤트 발생
		case ev := <-pool.chainHeadCh:
			if ev.Block != nil {
				pool.mu.Lock()
				if pool.chainconfig.IsHomestead(ev.Block.Number()) {
					pool.homestead = true
				}
				pool.reset(head.Header(), ev.Block.Header())
				head = ev.Block

				pool.mu.Unlock()
			}
		// Be unsubscribed due to system stopped
		// 시스템중지로 인한 구독종료
		case <-pool.chainHeadSub.Err():
			return

		// Handle stats reporting ticks
		// 상태보고 틱
		case <-report.C:
			pool.mu.RLock()
			pending, queued := pool.stats()
			stales := pool.priced.stales
			pool.mu.RUnlock()

			if pending != prevPending || queued != prevQueued || stales != prevStales {
				log.Debug("Transaction pool status report", "executable", pending, "queued", queued, "stales", stales)
				prevPending, prevQueued, prevStales = pending, queued, stales
			}

		// Handle inactive account transaction eviction
		// 비활성화된 계정의 트렌젝션 퇴출
		case <-evict.C:
			pool.mu.Lock()
			for addr := range pool.queue {
				// Skip local transactions from the eviction mechanism
				if pool.locals.contains(addr) {
					continue
				}
				// Any non-locals old enough should be removed
				if time.Since(pool.beats[addr]) > pool.config.Lifetime {
					for _, tx := range pool.queue[addr].Flatten() {
						pool.removeTx(tx.Hash(), true)
					}
				}
			}
			pool.mu.Unlock()

		// Handle local transaction journal rotation
		// 로컬트렌젝션의 저널 순회
		case <-journal.C:
			if pool.journal != nil {
				pool.mu.Lock()
				if err := pool.journal.rotate(pool.local()); err != nil {
					log.Warn("Failed to rotate local tx journal", "err", err)
				}
				pool.mu.Unlock()
			}
		}
	}
}

// lockedReset is a wrapper around reset to allow calling it in a thread safe
// manner. This method is only ever used in the tester!
// lockedReset함수는 스레드 세이프하게 reset을 호출하기 위한 랩퍼함수이다
func (pool *TxPool) lockedReset(oldHead, newHead *types.Header) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pool.reset(oldHead, newHead)
}

// reset retrieves the current state of the blockchain and ensures the content
// of the transaction pool is valid with regard to the chain state.
// reset함수는 블록체인의 현재 상태를 반환하고, 트렌젝션 풀의 컨텐츠들이
// 체인상태에 대하여 유효한지 보장한다
func (pool *TxPool) reset(oldHead, newHead *types.Header) {
	// If we're reorging an old state, reinject all dropped transactions
	// 오래된 상태를 재구성할때, 누락되었던 모든 트렌젝션을 재삽입한다
	var reinject types.Transactions

	if oldHead != nil && oldHead.Hash() != newHead.ParentHash {
		// If the reorg is too deep, avoid doing it (will happen during fast sync)
		// 재구성이 오래걸리면 회피한다
		oldNum := oldHead.Number.Uint64()
		newNum := newHead.Number.Uint64()

		if depth := uint64(math.Abs(float64(oldNum) - float64(newNum))); depth > 64 {
			log.Debug("Skipping deep transaction reorg", "depth", depth)
		} else {
			// Reorg seems shallow enough to pull in all transactions into memory
			// 재구성이 모든 트렌젝션을 메모리에 올릴만큼 얕을때
			var discarded, included types.Transactions

			var (
				rem = pool.chain.GetBlock(oldHead.Hash(), oldHead.Number.Uint64())
				add = pool.chain.GetBlock(newHead.Hash(), newHead.Number.Uint64())
			)
			for rem.NumberU64() > add.NumberU64() {
				discarded = append(discarded, rem.Transactions()...)
				if rem = pool.chain.GetBlock(rem.ParentHash(), rem.NumberU64()-1); rem == nil {
					log.Error("Unrooted old chain seen by tx pool", "block", oldHead.Number, "hash", oldHead.Hash())
					return
				}
			}
			for add.NumberU64() > rem.NumberU64() {
				included = append(included, add.Transactions()...)
				if add = pool.chain.GetBlock(add.ParentHash(), add.NumberU64()-1); add == nil {
					log.Error("Unrooted new chain seen by tx pool", "block", newHead.Number, "hash", newHead.Hash())
					return
				}
			}
			for rem.Hash() != add.Hash() {
				discarded = append(discarded, rem.Transactions()...)
				if rem = pool.chain.GetBlock(rem.ParentHash(), rem.NumberU64()-1); rem == nil {
					log.Error("Unrooted old chain seen by tx pool", "block", oldHead.Number, "hash", oldHead.Hash())
					return
				}
				included = append(included, add.Transactions()...)
				if add = pool.chain.GetBlock(add.ParentHash(), add.NumberU64()-1); add == nil {
					log.Error("Unrooted new chain seen by tx pool", "block", newHead.Number, "hash", newHead.Hash())
					return
				}
			}
			reinject = types.TxDifference(discarded, included)
		}
	}
	// Initialize the internal state to the current head
	// 현재상태에 대해 내부 상태를 초기화한다
	if newHead == nil {
		newHead = pool.chain.CurrentBlock().Header() // Special case during testing
	}
	statedb, err := pool.chain.StateAt(newHead.Root)
	if err != nil {
		log.Error("Failed to reset txpool state", "err", err)
		return
	}
	pool.currentState = statedb
	pool.pendingState = state.ManageState(statedb)
	pool.currentMaxGas = newHead.GasLimit

	// Inject any transactions discarded due to reorgs
	// 재구성때 제거되었던 트렌젝션을 재삽입한다
	log.Debug("Reinjecting stale transactions", "count", len(reinject))
	pool.addTxsLocked(reinject, false)

	// validate the pool of pending transactions, this will remove
	// any transactions that have been included in the block or
	// have been invalidated because of another transaction (e.g.
	// higher gas price)
	// 펜딩 트렌젝션의 풀을 검증한다.
	// 이미 블록에 포함되었거나 다른 트렌젝션에 의해 비활성화된
	// 트렌젝션을 제거한다
	pool.demoteUnexecutables()

	// Update all accounts to the latest known pending nonce
	// 모든 계정을 마지막 대기중인 논스로 업데이트한다
	for addr, list := range pool.pending {
		txs := list.Flatten() // Heavy but will be cached and is needed by the miner anyway
		pool.pendingState.SetNonce(addr, txs[len(txs)-1].Nonce()+1)
	}
	// Check the queue and move transactions over to the pending if possible
	// or remove those that have become invalid
	// 큐상태를 체크하고 가능하다면 트렌젝션을 대기로 옮기거나 무효화된 것은 제거한다
	pool.promoteExecutables(nil)
}

// Stop terminates the transaction pool.
// Stop함수는 트렌젝션 풀을 제거한다
func (pool *TxPool) Stop() {
	// Unsubscribe all subscriptions registered from txpool
	// txpool로부터 등록된 구독을 해지한다
	pool.scope.Close()

	// Unsubscribe subscriptions registered from blockchain
	// 블록체인으로 부터 등록된 구독을 제거한다
	pool.chainHeadSub.Unsubscribe()
	pool.wg.Wait()

	if pool.journal != nil {
		pool.journal.close()
	}
	log.Info("Transaction pool stopped")
}

// SubscribeNewTxsEvent registers a subscription of NewTxsEvent and
// starts sending event to the given channel.
// SubscribeNewTxsEvent함수는 NewTxsEvent에 대한 구독을 등록하고
// 주어진 채널로 이벤트를 전송하기 시작한다
func (pool *TxPool) SubscribeNewTxsEvent(ch chan<- NewTxsEvent) event.Subscription {
	return pool.scope.Track(pool.txFeed.Subscribe(ch))
}

// GasPrice returns the current gas price enforced by the transaction pool.
// GasPrice함수는 트렌젝션 풀에의해 강제된 현재 가스 가격을 반환한다 
func (pool *TxPool) GasPrice() *big.Int {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return new(big.Int).Set(pool.gasPrice)
}

// SetGasPrice updates the minimum price required by the transaction pool for a
// new transaction, and drops all transactions below this threshold.
// SetGasPrice함수는 새로운 트렌젝션에 대해 트렌젝션 풀에의해 요구되는 최소 가격을 갱신하고,
// 한도보다 작은 모든 트렌젹션을 제거한다
func (pool *TxPool) SetGasPrice(price *big.Int) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pool.gasPrice = price
	for _, tx := range pool.priced.Cap(price, pool.locals) {
		pool.removeTx(tx.Hash(), false)
	}
	log.Info("Transaction pool price threshold updated", "price", price)
}

// State returns the virtual managed state of the transaction pool.
// State 함수는 가상으로 관리되는 트렌젝션 풀의 상태를 반환한다
func (pool *TxPool) State() *state.ManagedState {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.pendingState
}

// Stats retrieves the current pool stats, namely the number of pending and the
// number of queued (non-executable) transactions.
// Stats함수는 현재 풀의 상태(대기중/큐잉되었으나 실행불가능한)를 반환한다
func (pool *TxPool) Stats() (int, int) {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.stats()
}

// stats retrieves the current pool stats, namely the number of pending and the
// number of queued (non-executable) transactions.
// stats 함수는 현재 펜딩된 트렌젝션의 수와 
// 큐된(실행 가능하지 않은) 트렌젝션의 수를 반환한다
func (pool *TxPool) stats() (int, int) {
	pending := 0
	for _, list := range pool.pending {
		pending += list.Len()
	}
	queued := 0
	for _, list := range pool.queue {
		queued += list.Len()
	}
	return pending, queued
}

// Content retrieves the data content of the transaction pool, returning all the
// pending as well as queued transactions, grouped by account and sorted by nonce.
// Contents 함수는 트렌젝션풀의 데이터 컨텐츠를 반환하고, 
// 대기, 큐잉 트렌젝션을 계정별로 그룹하고 논스로 정렬하여 반환한다
func (pool *TxPool) Content() (map[common.Address]types.Transactions, map[common.Address]types.Transactions) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pending := make(map[common.Address]types.Transactions)
	for addr, list := range pool.pending {
		pending[addr] = list.Flatten()
	}
	queued := make(map[common.Address]types.Transactions)
	for addr, list := range pool.queue {
		queued[addr] = list.Flatten()
	}
	return pending, queued
}

// Pending retrieves all currently processable transactions, groupped by origin
// account and sorted by nonce. The returned transaction set is a copy and can be
// freely modified by calling code.
// 이 함수는 현재 프로세싱이 가능한 트렌젝션을 검색하고 
// 관련 어카운트별로 그룹핑한 후 논스로 정렬한다.
func (pool *TxPool) Pending() (map[common.Address]types.Transactions, error) {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	pending := make(map[common.Address]types.Transactions)
	for addr, list := range pool.pending {
		pending[addr] = list.Flatten()
	}
	return pending, nil
}

// local retrieves all currently known local transactions, groupped by origin
// account and sorted by nonce. The returned transaction set is a copy and can be
// freely modified by calling code.
// local함수는현재까지 알려진 모든 로컬 트렌젝션을 계정별로 논스로 정렬하여 반환한다 
// 반환된 트렌젝션 세트는 복사된것이기 때문에 자유롭게 수정가능하다.
func (pool *TxPool) local() map[common.Address]types.Transactions {
	txs := make(map[common.Address]types.Transactions)
	for addr := range pool.locals.accounts {
		if pending := pool.pending[addr]; pending != nil {
			txs[addr] = append(txs[addr], pending.Flatten()...)
		}
		if queued := pool.queue[addr]; queued != nil {
			txs[addr] = append(txs[addr], queued.Flatten()...)
		}
	}
	return txs
}

// validateTx checks whether a transaction is valid according to the consensus
// rules and adheres to some heuristic limits of the local node (price and size).
// validateTx함수는 트렌젝션이 합의룰에 따라 유효한지 체크하고
// 가격과 크기가 로컬노드에 부합한지 확인한다
func (pool *TxPool) validateTx(tx *types.Transaction, local bool) error {
	// Heuristic limit, reject transactions over 32KB to prevent DOS attacks
	// 휴리스틱하게 정해진 한도로 32KB가 넘는 트렌젝션은 
	// DOS attck을 방지하기 위해 거절한다
	if tx.Size() > 32*1024 {
		return ErrOversizedData
	}
	// Transactions can't be negative. This may never happen using RLP decoded
	// transactions but may occur if you create a transaction using the RPC.
	// 트렌젝션은 음수가 될수 없다. RLP 디코딩된 트렌젝션에서는 절대불가하지만
	// RPC를 이용해 생성한 트렌젝션의 경우 일어날 수있다.
	if tx.Value().Sign() < 0 {
		return ErrNegativeValue
	}
	// Ensure the transaction doesn't exceed the current block limit gas.
	// 트렌젝션이 현재 블록의 한도를 넘지 않도록 한다
	if pool.currentMaxGas < tx.Gas() {
		return ErrGasLimit
	}
	// Make sure the transaction is signed properly
	// 트렌젝션이 적절하게 서명되었는지 확인한다
	from, err := types.Sender(pool.signer, tx)
	if err != nil {
		return ErrInvalidSender
	}
	// Drop non-local transactions under our own minimal accepted gas price
	// 최소 수용가스 가격보다 낮은 외부 트렌젝션을 drop한다
	local = local || pool.locals.contains(from) // account may be local even if the transaction arrived from the network
	// 트렌젝션이 외부에서 왔더라도 계정이 local일 수도 있다.
	if !local && pool.gasPrice.Cmp(tx.GasPrice()) > 0 {
		return ErrUnderpriced
	}
	// Ensure the transaction adheres to nonce ordering
	// 트렌젝션이 논스 오더링에 부합하는지 확인
	if pool.currentState.GetNonce(from) > tx.Nonce() {
		return ErrNonceTooLow
	}
	// Transactor should have enough funds to cover the costs
	// cost == V + GP * GL
	// 트렌젝터는 가격을 커버하기 위한 충분한 잔고를 가지고 있어야 함.
	if pool.currentState.GetBalance(from).Cmp(tx.Cost()) < 0 {
		return ErrInsufficientFunds
	}
	intrGas, err := IntrinsicGas(tx.Data(), tx.To() == nil, pool.homestead)
	if err != nil {
		return err
	}
	if tx.Gas() < intrGas {
		return ErrIntrinsicGas
	}
	return nil
}

// add validates a transaction and inserts it into the non-executable queue for
// later pending promotion and execution. If the transaction is a replacement for
// an already pending or queued one, it overwrites the previous and returns this
// so outer code doesn't uselessly call promote.
//
// If a newly added transaction is marked as local, its sending account will be
// whitelisted, preventing any associated transaction from being dropped out of
// the pool due to pricing constraints.

// 트랜젝션을 검증하고, 차후 pending 에서 승격되어 실행되도록 실행불가 큐에 추가한다.
// 만약 이 트렌젝션이 이미 pending되었거나 승격되어 실행되기 전이라면 기존 트렌젝션에 덮어씌워
// promote함수를 재호출하지 않도록 한다
// 만약 새롭게 추가된 트렌젝션이 로컬로 표시된다면, 트렌젝션을 생성한 어카운트는 화이트 리스트에 등록되어
// 다른 관련된 트렌젝션의 영향으로 가격 제약이 생겨 풀에서 드랍되지 않도록 한다.
func (pool *TxPool) add(tx *types.Transaction, local bool) (bool, error) {
	// If the transaction is already known, discard it
	// 이미 알려진 트렌젝션이라면 처리하지 않음
	hash := tx.Hash()
	if pool.all[hash] != nil {
		log.Trace("Discarding already known transaction", "hash", hash)
		return false, fmt.Errorf("known transaction: %x", hash)
	}
	// If the transaction fails basic validation, discard it
	// 트렌젝션이 기본검증을 통과하지 못한다면 배재
	if err := pool.validateTx(tx, local); err != nil {
		log.Trace("Discarding invalid transaction", "hash", hash, "err", err)
		invalidTxCounter.Inc(1)
		return false, err
	}
	// If the transaction pool is full, discard underpriced transactions
	// 트렌젝션 풀이 가득찾다면 낮은 가격의 트렌젝션을 버린다
	if uint64(len(pool.all)) >= pool.config.GlobalSlots+pool.config.GlobalQueue {
		// If the new transaction is underpriced, don't accept it
		// 새트렌젝션이 낮은가겨이라면 수락하지 않음
		if !local && pool.priced.Underpriced(tx, pool.locals) {
			log.Trace("Discarding underpriced transaction", "hash", hash, "price", tx.GasPrice())
			underpricedTxCounter.Inc(1)
			return false, ErrUnderpriced
		}
		// New transaction is better than our worse ones, make room for it
		// 새 트렌젝션이 기존것보다 좋을경우 공간을 만든다
		drop := pool.priced.Discard(len(pool.all)-int(pool.config.GlobalSlots+pool.config.GlobalQueue-1), pool.locals)
		for _, tx := range drop {
			log.Trace("Discarding freshly underpriced transaction", "hash", tx.Hash(), "price", tx.GasPrice())
			underpricedTxCounter.Inc(1)
			pool.removeTx(tx.Hash(), false)
		}
	}
	// If the transaction is replacing an already pending one, do directly
	// 트렌젝션이 이미 대기중인 것을 대체할경우 바로처리 
	from, _ := types.Sender(pool.signer, tx) // already validated
	if list := pool.pending[from]; list != nil && list.Overlaps(tx) {
		// Nonce already pending, check if required price bump is met
		// 논스가 이미 대기중, 가격이 충분히 올려져있는지 체크한다
		inserted, old := list.Add(tx, pool.config.PriceBump)
		if !inserted {
			pendingDiscardCounter.Inc(1)
			return false, ErrReplaceUnderpriced
		}
		// New transaction is better, replace old one
		// 새 트렌젝션이 더 좋으므로 기존것을 대체
		if old != nil {
			delete(pool.all, old.Hash())
			pool.priced.Removed()
			pendingReplaceCounter.Inc(1)
		}
		pool.all[tx.Hash()] = tx
		pool.priced.Put(tx)
		pool.journalTx(from, tx)

		log.Trace("Pooled new executable transaction", "hash", hash, "from", from, "to", tx.To())

		// We've directly injected a replacement transaction, notify subsystems
		// 대체할 트렌젝션을 주입하고 서브시스템에 알린다
		go pool.txFeed.Send(NewTxsEvent{types.Transactions{tx}})

		return old != nil, nil
	}
	// New transaction isn't replacing a pending one, push into queue
	// 새 트렌젝션이 기존것을 대체하지 않으므로 큐에 넣는다
	replace, err := pool.enqueueTx(hash, tx)
	if err != nil {
		return false, err
	}
	// Mark local addresses and journal local transactions
	// 로컬의 주소를 마킹하고, 저널링한다
	if local {
		pool.locals.add(from)
	}
	pool.journalTx(from, tx)

	log.Trace("Pooled new future transaction", "hash", hash, "from", from, "to", tx.To())
	return replace, nil
}

// enqueueTx inserts a new transaction into the non-executable transaction queue.
//
// Note, this method assumes the pool lock is held!
// enqueueTx 함수는 새로운 트렌젝션을 실행 가능하지 않은 트렌젝션큐에 넣는다
// 이 함수는 pool lock이 잡혀있을때를 가정한다
func (pool *TxPool) enqueueTx(hash common.Hash, tx *types.Transaction) (bool, error) {
	// Try to insert the transaction into the future queue
	// 미래큐에 트렌젝션을 삽입시도한다
	from, _ := types.Sender(pool.signer, tx) // already validated
	// 이미 검증됨
	if pool.queue[from] == nil {
		pool.queue[from] = newTxList(false)
	}
	inserted, old := pool.queue[from].Add(tx, pool.config.PriceBump)
	if !inserted {
		// An older transaction was better, discard this
		// 기존의 것이 더 좋으니 무시
		queuedDiscardCounter.Inc(1)
		return false, ErrReplaceUnderpriced
	}
	// Discard any previous transaction and mark this
	// 기존 트렌젝션을 배제하고 마킹한다
	if old != nil {
		delete(pool.all, old.Hash())
		pool.priced.Removed()
		queuedReplaceCounter.Inc(1)
	}
	if pool.all[hash] == nil {
		pool.all[hash] = tx
		pool.priced.Put(tx)
	}
	return old != nil, nil
}

// journalTx adds the specified transaction to the local disk journal if it is
// deemed to have been sent from a local account.
// journalTx함수는 지정된 트렌젝션이 로컬 계정에서 온것으로 간주될때
// 로컬 디스크 저널에 추가한다
func (pool *TxPool) journalTx(from common.Address, tx *types.Transaction) {
	// Only journal if it's enabled and the transaction is local
	if pool.journal == nil || !pool.locals.contains(from) {
		return
	}
	if err := pool.journal.insert(tx); err != nil {
		log.Warn("Failed to journal local transaction", "err", err)
	}
}

// promoteTx adds a transaction to the pending (processable) list of transactions
// and returns whether it was inserted or an older was better.
//
// Note, this method assumes the pool lock is held!
// promoteTx함수는 대기중인 트렌젝션 리스트에 트렌젝션을 추가하고 
// 추가여부와 대체유효여부를 반환한다
// 이 함수는 pool lock이 잡혀있을때를 가정한다
func (pool *TxPool) promoteTx(addr common.Address, hash common.Hash, tx *types.Transaction) bool {
	// Try to insert the transaction into the pending queue
	// 대기큐에 트렌젝션을 삽입시도한다
	if pool.pending[addr] == nil {
		pool.pending[addr] = newTxList(true)
	}
	list := pool.pending[addr]

	inserted, old := list.Add(tx, pool.config.PriceBump)
	if !inserted {
		// An older transaction was better, discard this
		// 기존의 것이 더 좋으니 무시
		delete(pool.all, hash)
		pool.priced.Removed()

		pendingDiscardCounter.Inc(1)
		return false
	}
	// Otherwise discard any previous transaction and mark this
	// 아니라면 기존의 트렌젝션을 제외하고 마킹한다
	if old != nil {
		delete(pool.all, old.Hash())
		pool.priced.Removed()

		pendingReplaceCounter.Inc(1)
	}
	// Failsafe to work around direct pending inserts (tests)
	// 테스트를 위한 워크 어라운드
	if pool.all[hash] == nil {
		pool.all[hash] = tx
		pool.priced.Put(tx)
	}
	// Set the potentially new pending nonce and notify any subsystems of the new tx
	// 새로운 대기 논스를 설정하고 새로운 트렌젝션의 서브시스템에 알린다
	pool.beats[addr] = time.Now()
	pool.pendingState.SetNonce(addr, tx.Nonce()+1)

	return true
}

// AddLocal enqueues a single transaction into the pool if it is valid, marking
// the sender as a local one in the mean time, ensuring it goes around the local
// pricing constraints.
// AddLocal 함수는 문제가 없는 하나의 트렌젝션을 풀에 넣고
// 로컬 가격제약을 확인하기 위해 전송자를 로컬에 존재하는 것으로 마킹한다
func (pool *TxPool) AddLocal(tx *types.Transaction) error {
	return pool.addTx(tx, !pool.config.NoLocals)
}

// AddRemote enqueues a single transaction into the pool if it is valid. If the
// sender is not among the locally tracked ones, full pricing constraints will
// apply.
// AddRemote함수는 문제가 없는 단일 트렌젝션을 풀에 넣는다
// 전송자가 로컬에서 트렉킹되는 것이 아니라면 전체 가격 제약이 적용될것이다
func (pool *TxPool) AddRemote(tx *types.Transaction) error {
	return pool.addTx(tx, false)
}

// AddLocals enqueues a batch of transactions into the pool if they are valid,
// marking the senders as a local ones in the mean time, ensuring they go around
// the local pricing constraints.
// AddLocals 함수는 문제가 없는 트렌젝션들을 풀에 넣고
// 로컬 가격제약을 확인하기 위해 전송자를 로컬에 존재하는 것으로 마킹한다
func (pool *TxPool) AddLocals(txs []*types.Transaction) []error {
	return pool.addTxs(txs, !pool.config.NoLocals)
}

// AddRemotes enqueues a batch of transactions into the pool if they are valid.
// If the senders are not among the locally tracked ones, full pricing constraints
// will apply.
// AddRemotes함수는 문제가 없는 트렌젝션들을 풀에 넣는다
// 전송자가 로컬에서 트렉킹되는 것이 아니라면 전체 가격 제약이 적용될것이다
func (pool *TxPool) AddRemotes(txs []*types.Transaction) []error {
	return pool.addTxs(txs, false)
}

// addTx enqueues a single transaction into the pool if it is valid.
// 이 함수는 문제가 없는 하나의 트렌젝션을 풀에 넣는다 
func (pool *TxPool) addTx(tx *types.Transaction, local bool) error {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	// Try to inject the transaction and update any state
	// 트렌젝션을 삽입하고, 상태를 업데이트한다
	replace, err := pool.add(tx, local)
	if err != nil {
		return err
	}
	// If we added a new transaction, run promotion checks and return
	// 새로운 트렌젝션을 추가한다면 promte 체크를 하고 반환한다
	if !replace {
		from, _ := types.Sender(pool.signer, tx) // already validated
		// 이미 검증됨
		pool.promoteExecutables([]common.Address{from})
	}
	return nil
}

// addTxs attempts to queue a batch of transactions if they are valid.
// addTxs는 여러개의 트렌젝션을 큐한다
func (pool *TxPool) addTxs(txs []*types.Transaction, local bool) []error {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	return pool.addTxsLocked(txs, local)
}

// addTxsLocked attempts to queue a batch of transactions if they are valid,
// whilst assuming the transaction pool lock is already held.
// addTxsLocked는 여러개의 트렌젝션을 큐한다
func (pool *TxPool) addTxsLocked(txs []*types.Transaction, local bool) []error {
	// Add the batch of transaction, tracking the accepted ones
	// 여러개의 트렌젝션을 추가하고 허용된 것들을 트렉킹한다
	dirty := make(map[common.Address]struct{})
	errs := make([]error, len(txs))

	for i, tx := range txs {
		var replace bool
		if replace, errs[i] = pool.add(tx, local); errs[i] == nil {
			if !replace {
				from, _ := types.Sender(pool.signer, tx) // already validated
		  		// 이미 검증됨
				dirty[from] = struct{}{}
			}
		}
	}
	// Only reprocess the internal state if something was actually added
	// 실제로 무엇인가 추가되었을 경우 내부 스테이트를 재처리한다
	if len(dirty) > 0 {
		addrs := make([]common.Address, 0, len(dirty))
		for addr := range dirty {
			addrs = append(addrs, addr)
		}
		pool.promoteExecutables(addrs)
	}
	return errs
}

// Status returns the status (unknown/pending/queued) of a batch of transactions
// identified by their hashes.
// Status함수는 해시를 통해 여러 트렌젝션들의 상태를 반환한다
func (pool *TxPool) Status(hashes []common.Hash) []TxStatus {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	status := make([]TxStatus, len(hashes))
	for i, hash := range hashes {
		if tx := pool.all[hash]; tx != nil {
			from, _ := types.Sender(pool.signer, tx) // already validated
			// 이미 검증됨
			if pool.pending[from] != nil && pool.pending[from].txs.items[tx.Nonce()] != nil {
				status[i] = TxStatusPending
			} else {
				status[i] = TxStatusQueued
			}
		}
	}
	return status
}

// Get returns a transaction if it is contained in the pool
// and nil otherwise.
// Get함수는 풀에 트렌젝션이 존재할경우 반환하고 아니라면 nil을 반환
func (pool *TxPool) Get(hash common.Hash) *types.Transaction {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	return pool.all[hash]
}

// removeTx removes a single transaction from the queue, moving all subsequent
// transactions back to the future queue.
// removeTx함수는 단일 트렌젝션을 큐에서 제거하고 
// 이후의 트렌젝션들을 미래큐로 옮긴다
func (pool *TxPool) removeTx(hash common.Hash, outofbound bool) {
	// Fetch the transaction we wish to delete
	// 삭제를 원하는 트렌젝션을 가져옴
	tx, ok := pool.all[hash]
	if !ok {
		return
	}
	addr, _ := types.Sender(pool.signer, tx) // already validated during insertion
	// 삽입과정에서 이미 검증됨

	// Remove it from the list of known transactions
	// 알려진 트렌젝션 리스트에서 제거
	delete(pool.all, hash)
	if outofbound {
		pool.priced.Removed()
	}
	// Remove the transaction from the pending lists and reset the account nonce
	// 대기리스트로부터 트렌젝션을 제거하고, 계정의 논스를 재설정한다
	if pending := pool.pending[addr]; pending != nil {
		if removed, invalids := pending.Remove(tx); removed {
			// If no more pending transactions are left, remove the list
			// 리스트에 트렌젝션이 남아있지 않을 경우 리스트를 제거
			if pending.Empty() {
				delete(pool.pending, addr)
				delete(pool.beats, addr)
			}
			// Postpone any invalidated transactions
			// 무효화된 트렌젝션들을 뒤로 미룬다
			for _, tx := range invalids {
				pool.enqueueTx(tx.Hash(), tx)
			}
			// Update the account nonce if needed
			// 필요할 경우 계정의 논스를 업데이트한다
			if nonce := tx.Nonce(); pool.pendingState.GetNonce(addr) > nonce {
				pool.pendingState.SetNonce(addr, nonce)
			}
			return
		}
	}
	// Transaction is in the future queue
	// 트렌젝션이 미래큐에 있을경우
	if future := pool.queue[addr]; future != nil {
		future.Remove(tx)
		if future.Empty() {
			delete(pool.queue, addr)
		}
	}
}

// promoteExecutables moves transactions that have become processable from the
// future queue to the set of pending transactions. During this process, all
// invalidated transactions (low nonce, low balance) are deleted.
// promoteExecutables함수는미래큐로부터  처리 가능해질 대기 트렌젝션들
// 대기큐로 옮긴다, 이 과정동안 모든 무효화되었던 트렌젝션들은 삭제된다
func (pool *TxPool) promoteExecutables(accounts []common.Address) {
	// Track the promoted transactions to broadcast them at once
	// 한번에 알리기 위해 프로모션된 트렌젝션들을 트랙킹한다
	var promoted []*types.Transaction

	// Gather all the accounts potentially needing updates
	// 업데이트가 필요할만한 계정들을 수집한다
	if accounts == nil {
		accounts = make([]common.Address, 0, len(pool.queue))
		for addr := range pool.queue {
			accounts = append(accounts, addr)
		}
	}
	// Iterate over all accounts and promote any executable transactions
	// 모든 계정을 반복하면서 실행가능한 트렌젝션들을 프로모션한다
	for _, addr := range accounts {
		list := pool.queue[addr]
		if list == nil {
			continue // Just in case someone calls with a non existing account
			// 존재하지 않은 계정을 호출했을때
		}
		// Drop all transactions that are deemed too old (low nonce)
		// 오래되어보이는 트렌젝션들을 드롭한다
		for _, tx := range list.Forward(pool.currentState.GetNonce(addr)) {
			hash := tx.Hash()
			log.Trace("Removed old queued transaction", "hash", hash)
			delete(pool.all, hash)
			pool.priced.Removed()
		}
		// Drop all transactions that are too costly (low balance or out of gas)
		// 잔고가 모자르거나 가스가 모자른 경우 트렌젝션들을 드롭한다
		drops, _ := list.Filter(pool.currentState.GetBalance(addr), pool.currentMaxGas)
		for _, tx := range drops {
			hash := tx.Hash()
			log.Trace("Removed unpayable queued transaction", "hash", hash)
			delete(pool.all, hash)
			pool.priced.Removed()
			queuedNofundsCounter.Inc(1)
		}
		// Gather all executable transactions and promote them
		// 실행가능한 트렌젝션들을 모아 프로모션한다
		for _, tx := range list.Ready(pool.pendingState.GetNonce(addr)) {
			hash := tx.Hash()
			if pool.promoteTx(addr, hash, tx) {
				log.Trace("Promoting queued transaction", "hash", hash)
				promoted = append(promoted, tx)
			}
		}
		// Drop all transactions over the allowed limit
		// 한도를 넘는 트렌젝션을 드롭한다
		if !pool.locals.contains(addr) {
			for _, tx := range list.Cap(int(pool.config.AccountQueue)) {
				hash := tx.Hash()
				delete(pool.all, hash)
				pool.priced.Removed()
				queuedRateLimitCounter.Inc(1)
				log.Trace("Removed cap-exceeding queued transaction", "hash", hash)
			}
		}
		// Delete the entire queue entry if it became empty.
		// 비워진 전체 큐엔트리를 삭제한다
		if list.Empty() {
			delete(pool.queue, addr)
		}
	}
	// Notify subsystem for new promoted transactions.
	// 프로모션된 트렌젝션을 알린다
	if len(promoted) > 0 {
		pool.txFeed.Send(NewTxsEvent{promoted})
	}
	// If the pending limit is overflown, start equalizing allowances
	// 대기한도가 넘어서면, 허용수치로 맞춘다
	pending := uint64(0)
	for _, list := range pool.pending {
		pending += uint64(list.Len())
	}
	if pending > pool.config.GlobalSlots {
		pendingBeforeCap := pending
		// Assemble a spam order to penalize large transactors first
		// 큰 트렌젝터를 먼저 처벌하기 위한 스팸오더를 작성한다
		spammers := prque.New()
		for addr, list := range pool.pending {
			// Only evict transactions from high rollers
			// 많이 쓴사람들의 트렌젝션만 훼손한다
			if !pool.locals.contains(addr) && uint64(list.Len()) > pool.config.AccountSlots {
				spammers.Push(addr, float32(list.Len()))
			}
		}
		// Gradually drop transactions from offenders
		// 공격자들로부터의 트렌젠션을 드롭시킨다
		offenders := []common.Address{}
		for pending > pool.config.GlobalSlots && !spammers.Empty() {
			// Retrieve the next offender if not local address
			// 로컬 계정이 아닌경우 다음 범죄자를 반환한다.
			offender, _ := spammers.Pop()
			offenders = append(offenders, offender.(common.Address))

			// Equalize balances until all the same or below threshold
			// 한도에 맞을때까지 잔고를 맞춘다
			if len(offenders) > 1 {
				// Calculate the equalization threshold for all current offenders
				// 현재 범죄자들을 위한 한도를 계산한다
				threshold := pool.pending[offender.(common.Address)].Len()

				// Iteratively reduce all offenders until below limit or threshold reached
				// 반복적으로 범죄자들을 감소시킨다
				for pending > pool.config.GlobalSlots && pool.pending[offenders[len(offenders)-2]].Len() > threshold {
					for i := 0; i < len(offenders)-1; i++ {
						list := pool.pending[offenders[i]]
						for _, tx := range list.Cap(list.Len() - 1) {
							// Drop the transaction from the global pools too
							// 전체풀에서도 트렌젝션을 제거한다
							hash := tx.Hash()
							delete(pool.all, hash)
							pool.priced.Removed()

							// Update the account nonce to the dropped transaction
							// 제거된 트렌젝션의 계정논스를 갱신한다
							if nonce := tx.Nonce(); pool.pendingState.GetNonce(offenders[i]) > nonce {
								pool.pendingState.SetNonce(offenders[i], nonce)
							}
							log.Trace("Removed fairness-exceeding pending transaction", "hash", hash)
						}
						pending--
					}
				}
			}
		}
		// If still above threshold, reduce to limit or min allowance
		// 여전히 한도보다 높다면 한도를 줄이거나 최소 허용량을 줄인다
		if pending > pool.config.GlobalSlots && len(offenders) > 0 {
			for pending > pool.config.GlobalSlots && uint64(pool.pending[offenders[len(offenders)-1]].Len()) > pool.config.AccountSlots {
				for _, addr := range offenders {
					list := pool.pending[addr]
					for _, tx := range list.Cap(list.Len() - 1) {
						// Drop the transaction from the global pools too
						// 전체풀에서 트렌젝션을 제거한다
						hash := tx.Hash()
						delete(pool.all, hash)
						pool.priced.Removed()

						// Update the account nonce to the dropped transaction
							// 제거된 트렌젝션의 계정논스를 갱신한다
						if nonce := tx.Nonce(); pool.pendingState.GetNonce(addr) > nonce {
							pool.pendingState.SetNonce(addr, nonce)
						}
						log.Trace("Removed fairness-exceeding pending transaction", "hash", hash)
					}
					pending--
				}
			}
		}
		pendingRateLimitCounter.Inc(int64(pendingBeforeCap - pending))
	}
	// If we've queued more transactions than the hard limit, drop oldest ones
	// 하드 리밋보다 많은 트렌젝션이 큐잉되면 오래된것부터 제거한다
	queued := uint64(0)
	for _, list := range pool.queue {
		queued += uint64(list.Len())
	}
	if queued > pool.config.GlobalQueue {
		// Sort all accounts with queued transactions by heartbeat
		// 최근활동시간에 의해 큐잉된 트렌젝션들의 모든 계정을 정렬한다 
		addresses := make(addresssByHeartbeat, 0, len(pool.queue))
		for addr := range pool.queue {
			if !pool.locals.contains(addr) { // don't drop locals
				addresses = append(addresses, addressByHeartbeat{addr, pool.beats[addr]})
			}
		}
		sort.Sort(addresses)

		// Drop transactions until the total is below the limit or only locals remain
		// 로컬만 남았거나, 전체가 한도를 넘을때까지 트렌젝션을 드롭
		for drop := queued - pool.config.GlobalQueue; drop > 0 && len(addresses) > 0; {
			addr := addresses[len(addresses)-1]
			list := pool.queue[addr.address]

			addresses = addresses[:len(addresses)-1]

			// Drop all transactions if they are less than the overflow
			// overflow가 안날때까지 모든 트렌젝션을 드롭한다
			if size := uint64(list.Len()); size <= drop {
				for _, tx := range list.Flatten() {
					pool.removeTx(tx.Hash(), true)
				}
				drop -= size
				queuedRateLimitCounter.Inc(int64(size))
				continue
			}
			// Otherwise drop only last few transactions
			// 아니라면 마지막 몇개만 드롭한다 
			txs := list.Flatten()
			for i := len(txs) - 1; i >= 0 && drop > 0; i-- {
				pool.removeTx(txs[i].Hash(), true)
				drop--
				queuedRateLimitCounter.Inc(1)
			}
		}
	}
}

// demoteUnexecutables removes invalid and processed transactions from the pools
// executable/pending queue and any subsequent transactions that become unexecutable
// are moved back into the future queue.
// demoteUnexecutables 함수는 무효하거나 처리된 트렌젝션들을 풀에서 제거한다
// 실행/대기 큐와 이후 실행불가능해질 트렌젝션들은 미래 큐로 되돌려진다
func (pool *TxPool) demoteUnexecutables() {
	// Iterate over all accounts and demote any non-executable transactions
	// 모든 계정을 반복하면서 실행 불가능한 트렌젝션들을 demote한다
	for addr, list := range pool.pending {
		nonce := pool.currentState.GetNonce(addr)

		// Drop all transactions that are deemed too old (low nonce)
		// 오래된 트렌젝션들을 제거한다(논스가 낮을경우)
		for _, tx := range list.Forward(nonce) {
			hash := tx.Hash()
			log.Trace("Removed old pending transaction", "hash", hash)
			delete(pool.all, hash)
			pool.priced.Removed()
		}
		// Drop all transactions that are too costly (low balance or out of gas), and queue any invalids back for later
		// 잔고가 모자르거나 가스가 모자른 경우 트렌젝션들을 드롭한다, 나중을 위해 큐잉한다
		drops, invalids := list.Filter(pool.currentState.GetBalance(addr), pool.currentMaxGas)
		for _, tx := range drops {
			hash := tx.Hash()
			log.Trace("Removed unpayable pending transaction", "hash", hash)
			delete(pool.all, hash)
			pool.priced.Removed()
			pendingNofundsCounter.Inc(1)
		}
		for _, tx := range invalids {
			hash := tx.Hash()
			log.Trace("Demoting pending transaction", "hash", hash)
			pool.enqueueTx(hash, tx)
		}
		// If there's a gap in front, warn (should never happen) and postpone all transactions
		// 앞쪽에 공간이 있음, 일어나면 안되므로 경고를 하고 모든 트렌젝션의 처리를 대기한다
		if list.Len() > 0 && list.txs.Get(nonce) == nil {
			for _, tx := range list.Cap(0) {
				hash := tx.Hash()
				log.Error("Demoting invalidated transaction", "hash", hash)
				pool.enqueueTx(hash, tx)
			}
		}
		// Delete the entire queue entry if it became empty.
		// 비워진 전체 큐엔트리를 삭제한다
		if list.Empty() {
			delete(pool.pending, addr)
			delete(pool.beats, addr)
		}
	}
}

// addressByHeartbeat is an account address tagged with its last activity timestamp.
// addressByHeartbeat은 최근 활동의 타임스템프로 태그된 계정의 주소이다
type addressByHeartbeat struct {
	address   common.Address
	heartbeat time.Time
}

type addresssByHeartbeat []addressByHeartbeat

func (a addresssByHeartbeat) Len() int           { return len(a) }
func (a addresssByHeartbeat) Less(i, j int) bool { return a[i].heartbeat.Before(a[j].heartbeat) }
func (a addresssByHeartbeat) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

// accountSet is simply a set of addresses to check for existence, and a signer
// capable of deriving addresses from transactions.
// accountSet은 존재확인을 위한 어드레스의 세트이다
type accountSet struct {
	accounts map[common.Address]struct{}
	signer   types.Signer
}

// newAccountSet creates a new address set with an associated signer for sender
// derivations.
// newAccountSet함수는 새로운 조소를 생성하고 전송자 연산을 위해 연관된 서명자를 설정한다
func newAccountSet(signer types.Signer) *accountSet {
	return &accountSet{
		accounts: make(map[common.Address]struct{}),
		signer:   signer,
	}
}

// contains checks if a given address is contained within the set.
// contains함수는 주어진 주소가 세트에 포함되는지 체크한다
func (as *accountSet) contains(addr common.Address) bool {
	_, exist := as.accounts[addr]
	return exist
}

// containsTx checks if the sender of a given tx is within the set. If the sender
// cannot be derived, this method returns false.
// containsTx함수는 전송자가 세트에포함되는지를 체크하고
// 전송자를 연산하지 못할경우 false를 리턴한다
func (as *accountSet) containsTx(tx *types.Transaction) bool {
	if addr, err := types.Sender(as.signer, tx); err == nil {
		return as.contains(addr)
	}
	return false
}

// add inserts a new address into the set to track.
// add함수는 새로운 주소를 트래킹 셋에 추가한다
func (as *accountSet) add(addr common.Address) {
	as.accounts[addr] = struct{}{}
}
