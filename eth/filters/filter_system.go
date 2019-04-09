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

// Package filters implements an ethereum filtering system for block,
// transactions and log events.
// filters패키지는 블록, 트렌젝션, 로그이벤트들을 위한 이더리움 필터링 시스템을 구현한다
package filters

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

// Type determines the kind of filter and is used to put the filter in to
// the correct bucket when added.
// Type은 필터의 종류를 결정하며, 필터 추가시 필터를 올바른 버켓으로 던진다
type Type byte

const (
	// UnknownSubscription indicates an unknown subscription type
	// UnkownSubscription은 알려지지않은 구독타입을 나타낸다
	UnknownSubscription Type = iota
	// LogsSubscription queries for new or removed (chain reorg) logs
	// LogsSubscription은 새로운 것을 쿼리하거나 로그를 제거한다
	LogsSubscription
	// PendingLogsSubscription queries for logs in pending blocks
	// PendingLogsSubscription은 펜딩블록의 로그를 쿼리하는데 쓰인다
	PendingLogsSubscription
	// MinedAndPendingLogsSubscription queries for logs in mined and pending blocks.
	// MinedAndPendingLogsSubscriton은 채굴되었거나 대기중인 블록의 로그를 쿼리한다
	MinedAndPendingLogsSubscription
	// PendingTransactionsSubscription queries tx hashes for pending
	// transactions entering the pending state
	// PendingTransactionSubscriton은 대기중상태로 들어간 디기 트렌젝션 해시를 쿼리한다
	PendingTransactionsSubscription
	// BlocksSubscription queries hashes for blocks that are imported
	// BlocksSubscription은 수입된 블록의 해시를 쿼리한다
	BlocksSubscription
	// LastSubscription keeps track of the last index
	// LastIndexSubscription은 마지막 인덱스를 트래킹한다
	LastIndexSubscription
)

const (

	// txChanSize is the size of channel listening to NewTxsEvent.
	// The number is referenced from the size of tx pool.
	// txChanSize는 NewTXsEvent를 수신하기 위한 채널의 크기이다
	// 이 숫자는 Txpoll의 크기로 부터 참조된다
	txChanSize = 4096
	// rmLogsChanSize is the size of channel listening to RemovedLogsEvent.
	// rmLogsChanSize는 RemovedLogsEvent를 수신하기 위한 채널의 크기이다
	rmLogsChanSize = 10
	// logsChanSize is the size of channel listening to LogsEvent.
	// logsChanSize는 LogsEvent를 수신하기위한 채널의 크기이다
	logsChanSize = 10
	// chainEvChanSize is the size of channel listening to ChainEvent.
	// chainEvChanSize는 ChainEvent를 수신하기 위한 채널의 크기이다
	chainEvChanSize = 10
)

var (
	ErrInvalidSubscriptionID = errors.New("invalid id")
)

type subscription struct {
	id        rpc.ID
	typ       Type
	created   time.Time
	logsCrit  ethereum.FilterQuery
	logs      chan []*types.Log
	hashes    chan []common.Hash
	headers   chan *types.Header
	installed chan struct{} // closed when the filter is installed
	// filter가 설치되면 닫힌다
	err       chan error    // closed when the filter is uninstalled
	// filter가 해체되면 닫힌다
}

// EventSystem creates subscriptions, processes events and broadcasts them to the
// subscription which match the subscription criteria.
// EventSystem은 구독을 생성하고, 이벤트를 처리하며, 구독영역에 맞는 구독자에게 그들을 전파한다
type EventSystem struct {
	mux       *event.TypeMux
	backend   Backend
	lightMode bool
	lastHead  *types.Header

	// Subscriptions
	txsSub        event.Subscription         // Subscription for new transaction event
	logsSub       event.Subscription         // Subscription for new log event
	rmLogsSub     event.Subscription         // Subscription for removed log event
	chainSub      event.Subscription         // Subscription for new chain event
	pendingLogSub *event.TypeMuxSubscription // Subscription for pending log event
	// 새로운 트렌젝션 이벤트에 대한 구독
	// 새로운 로그이벤트에 대한 구독
	// 제거돈 로그 이벤트에 대한구독
	// 새로운 체인 이벤트에 대한 구독
	// 대기중인 로그 이벤트에 대한 구독

	// Channels
	install   chan *subscription         // install filter for event notification
	uninstall chan *subscription         // remove filter for event notification
	txsCh     chan core.NewTxsEvent      // Channel to receive new transactions event
	logsCh    chan []*types.Log          // Channel to receive new log event
	rmLogsCh  chan core.RemovedLogsEvent // Channel to receive removed log event
	chainCh   chan core.ChainEvent       // Channel to receive new chain event
	// 이벤트 알람을 위한 필터 설치
	// 이벤트 알람을 위한 필터 제거
	// 새로운 트렌젝션 이벤트를 받을 채널
	// 새로운 로그 이벤트를 받을 채널
	// 새로운 제거된 로그 이벤트를 받을 채널
	// 새로운 체인이벤트를 받을 체널
}

// NewEventSystem creates a new manager that listens for event on the given mux,
// parses and filters them. It uses the all map to retrieve filter changes. The
// work loop holds its own index that is used to forward events to filters.
//
// The returned manager has a loop that needs to be stopped with the Stop function
// or by stopping the given mux.
// NewEventSystem 함수는 주어진 먹스위에 이벤트를 수신하기 위한 새로운 매니저를 생성하고
// 이벤트를 분석하고 필터링한다
// 이 함수는 필터의 변화를 반환하기 위한 모든 맵을 사용한다
// 워크 루프는 필터들에게 이벤트를 포워딩하는데 사용될 자신만의 인덱스를 가진다
// 반환되는 매니져는 정지 함수나, 주어진 먹스에 의해 정지가 필요한 루프를 가진다
func NewEventSystem(mux *event.TypeMux, backend Backend, lightMode bool) *EventSystem {
	m := &EventSystem{
		mux:       mux,
		backend:   backend,
		lightMode: lightMode,
		install:   make(chan *subscription),
		uninstall: make(chan *subscription),
		txsCh:     make(chan core.NewTxsEvent, txChanSize),
		logsCh:    make(chan []*types.Log, logsChanSize),
		rmLogsCh:  make(chan core.RemovedLogsEvent, rmLogsChanSize),
		chainCh:   make(chan core.ChainEvent, chainEvChanSize),
	}

	// Subscribe events
	// 이벤트 구독
	m.txsSub = m.backend.SubscribeNewTxsEvent(m.txsCh)
	m.logsSub = m.backend.SubscribeLogsEvent(m.logsCh)
	m.rmLogsSub = m.backend.SubscribeRemovedLogsEvent(m.rmLogsCh)
	m.chainSub = m.backend.SubscribeChainEvent(m.chainCh)
	// TODO(rjl493456442): use feed to subscribe pending log event
	m.pendingLogSub = m.mux.Subscribe(core.PendingLogsEvent{})

	// Make sure none of the subscriptions are empty
	// 모든 구독이 비어있지 않은지 체크함
	if m.txsSub == nil || m.logsSub == nil || m.rmLogsSub == nil || m.chainSub == nil ||
		m.pendingLogSub.Closed() {
		log.Crit("Subscribe for event system failed")
	}

	go m.eventLoop()
	return m
}

// Subscription is created when the client registers itself for a particular event.
// 클라이언트가 특정이벤트에 자신을 등록함
type Subscription struct {
	ID        rpc.ID
	f         *subscription
	es        *EventSystem
	unsubOnce sync.Once
}

// Err returns a channel that is closed when unsubscribed.
// Err함수는 구독해지시 체넬이 닫힌 채널을 반환한다
func (sub *Subscription) Err() <-chan error {
	return sub.f.err
}

// Unsubscribe uninstalls the subscription from the event broadcast loop.
// Unsubscribe함수는 이벤트 광고 루프로부터 구독을 해지한다 
func (sub *Subscription) Unsubscribe() {
	sub.unsubOnce.Do(func() {
	uninstallLoop:
		for {
			// write uninstall request and consume logs/hashes. This prevents
			// the eventLoop broadcast method to deadlock when writing to the
			// filter event channel while the subscription loop is waiting for
			// this method to return (and thus not reading these events).
			// 구독해지 요청을 쓰고, 로그와 해시를 소모한다. 
			// 이 것은 구독 루프가 이 함수의 리턴을 기다리는 동안 
			// 필터 이벤트 채널에 쓸때 이벤트 방송 루프에서 데드락이 발생하는 것을 방지한다
			select {
			case sub.es.uninstall <- sub.f:
				break uninstallLoop
			case <-sub.f.logs:
			case <-sub.f.hashes:
			case <-sub.f.headers:
			}
		}

		// wait for filter to be uninstalled in work loop before returning
		// this ensures that the manager won't use the event channel which
		// will probably be closed by the client asap after this method returns.
		// 리턴하기전에 루프안에서 필터가 해지되기를 기다린다. 
		// 이것은 매니져가 이함수가 끝난후에 클라이언트에의해 
		// 닫힐지 모르는 이벤트 체널을 사용하지 못하게 한다
		<-sub.Err()
	})
}

// subscribe installs the subscription in the event broadcast loop.
// 구독함수는 이벤트 광고 루프에 구독을 설치한다
func (es *EventSystem) subscribe(sub *subscription) *Subscription {
	es.install <- sub
	<-sub.installed
	return &Subscription{ID: sub.id, f: sub, es: es}
}

// SubscribeLogs creates a subscription that will write all logs matching the
// given criteria to the given logs channel. Default value for the from and to
// block is "latest". If the fromBlock > toBlock an error is returned.
// SubscribeLogs 함수는 주어진 영역에 맞는 모든 로그를 주어진 로그 채널에 쓸 구독을 생성한다.
// from, to 영역의 기본값은 latest이다. 만약 fromBlock > toBlock이라면 에러를 리턴한다
func (es *EventSystem) SubscribeLogs(crit ethereum.FilterQuery, logs chan []*types.Log) (*Subscription, error) {
	var from, to rpc.BlockNumber
	if crit.FromBlock == nil {
		from = rpc.LatestBlockNumber
	} else {
		from = rpc.BlockNumber(crit.FromBlock.Int64())
	}
	if crit.ToBlock == nil {
		to = rpc.LatestBlockNumber
	} else {
		to = rpc.BlockNumber(crit.ToBlock.Int64())
	}

	// only interested in pending logs
	// 펜딩 로그만 관심이 있다
	if from == rpc.PendingBlockNumber && to == rpc.PendingBlockNumber {
		return es.subscribePendingLogs(crit, logs), nil
	}
	// only interested in new mined logs
	// 새롭게 채굴된 로그만 관심이 있다
	if from == rpc.LatestBlockNumber && to == rpc.LatestBlockNumber {
		return es.subscribeLogs(crit, logs), nil
	}
	// only interested in mined logs within a specific block range
	// 특정 블록 영역의 채굴된 로그만 관심이있다
	if from >= 0 && to >= 0 && to >= from {
		return es.subscribeLogs(crit, logs), nil
	}
	// interested in mined logs from a specific block number, new logs and pending logs
	// 특정 블록 영역의 채굴된 로그와 새로운 로그와 펜딩로그에만 관심이 있다
	if from >= rpc.LatestBlockNumber && to == rpc.PendingBlockNumber {
		return es.subscribeMinedPendingLogs(crit, logs), nil
	}
	// interested in logs from a specific block number to new mined blocks
	// 특정블록부터 새롭게 채굴된 블록까지의 로그에 관심이 있다
	if from >= 0 && to == rpc.LatestBlockNumber {
		return es.subscribeLogs(crit, logs), nil
	}
	return nil, fmt.Errorf("invalid from and to block combination: from > to")
}

// subscribeMinedPendingLogs creates a subscription that returned mined and
// pending logs that match the given criteria.
// subscribeMinePendingLogs함수는 채굴되었거나 대기중인 영역에 맞는 로그에 대한 구독을 생성한다
func (es *EventSystem) subscribeMinedPendingLogs(crit ethereum.FilterQuery, logs chan []*types.Log) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       MinedAndPendingLogsSubscription,
		logsCrit:  crit,
		created:   time.Now(),
		logs:      logs,
		hashes:    make(chan []common.Hash),
		headers:   make(chan *types.Header),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

// subscribeLogs creates a subscription that will write all logs matching the
// given criteria to the given logs channel.
// subscribeLogs함수는 전달된 채널에 영역에 맞는 모든 로그를 쓸 구독을 생성한다
func (es *EventSystem) subscribeLogs(crit ethereum.FilterQuery, logs chan []*types.Log) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       LogsSubscription,
		logsCrit:  crit,
		created:   time.Now(),
		logs:      logs,
		hashes:    make(chan []common.Hash),
		headers:   make(chan *types.Header),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

// subscribePendingLogs creates a subscription that writes transaction hashes for
// transactions that enter the transaction pool.
// subscribePendingLogs함수는 트렌젝션 풀에 들어간 트렌젝션들의 트렌젝션 해시 구독을 생성한다
func (es *EventSystem) subscribePendingLogs(crit ethereum.FilterQuery, logs chan []*types.Log) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       PendingLogsSubscription,
		logsCrit:  crit,
		created:   time.Now(),
		logs:      logs,
		hashes:    make(chan []common.Hash),
		headers:   make(chan *types.Header),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

// SubscribeNewHeads creates a subscription that writes the header of a block that is
// imported in the chain.
// SubscribeNewHeads 함수는 체인에 들어오는 블록의 헤더를 쓸 구독을 생성한다
func (es *EventSystem) SubscribeNewHeads(headers chan *types.Header) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       BlocksSubscription,
		created:   time.Now(),
		logs:      make(chan []*types.Log),
		hashes:    make(chan []common.Hash),
		headers:   headers,
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

// SubscribePendingTxs creates a subscription that writes transaction hashes for
// transactions that enter the transaction pool.
// SubscribePendingTxs 함수는 트렌젝션 풀에 들어간 트렌젝션들의 트렌젝션 해시를 쓸구독을 생성한다
func (es *EventSystem) SubscribePendingTxs(hashes chan []common.Hash) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       PendingTransactionsSubscription,
		created:   time.Now(),
		logs:      make(chan []*types.Log),
		hashes:    hashes,
		headers:   make(chan *types.Header),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

type filterIndex map[Type]map[rpc.ID]*subscription

// broadcast event to filters that match criteria.
// braodcast함수는 매칭된 이벤트를 필터로 광고한다
func (es *EventSystem) broadcast(filters filterIndex, ev interface{}) {
	if ev == nil {
		return
	}

	switch e := ev.(type) {
	case []*types.Log:
		if len(e) > 0 {
			for _, f := range filters[LogsSubscription] {
				if matchedLogs := filterLogs(e, f.logsCrit.FromBlock, f.logsCrit.ToBlock, f.logsCrit.Addresses, f.logsCrit.Topics); len(matchedLogs) > 0 {
					f.logs <- matchedLogs
				}
			}
		}
	case core.RemovedLogsEvent:
		for _, f := range filters[LogsSubscription] {
			if matchedLogs := filterLogs(e.Logs, f.logsCrit.FromBlock, f.logsCrit.ToBlock, f.logsCrit.Addresses, f.logsCrit.Topics); len(matchedLogs) > 0 {
				f.logs <- matchedLogs
			}
		}
	case *event.TypeMuxEvent:
		switch muxe := e.Data.(type) {
		case core.PendingLogsEvent:
			for _, f := range filters[PendingLogsSubscription] {
				if e.Time.After(f.created) {
					if matchedLogs := filterLogs(muxe.Logs, nil, f.logsCrit.ToBlock, f.logsCrit.Addresses, f.logsCrit.Topics); len(matchedLogs) > 0 {
						f.logs <- matchedLogs
					}
				}
			}
		}
	case core.NewTxsEvent:
		hashes := make([]common.Hash, 0, len(e.Txs))
		for _, tx := range e.Txs {
			hashes = append(hashes, tx.Hash())
		}
		for _, f := range filters[PendingTransactionsSubscription] {
			f.hashes <- hashes
		}
	case core.ChainEvent:
		for _, f := range filters[BlocksSubscription] {
			f.headers <- e.Block.Header()
		}
		if es.lightMode && len(filters[LogsSubscription]) > 0 {
			es.lightFilterNewHead(e.Block.Header(), func(header *types.Header, remove bool) {
				for _, f := range filters[LogsSubscription] {
					if matchedLogs := es.lightFilterLogs(header, f.logsCrit.Addresses, f.logsCrit.Topics, remove); len(matchedLogs) > 0 {
						f.logs <- matchedLogs
					}
				}
			})
		}
	}
}

func (es *EventSystem) lightFilterNewHead(newHeader *types.Header, callBack func(*types.Header, bool)) {
	oldh := es.lastHead
	es.lastHead = newHeader
	if oldh == nil {
		return
	}
	newh := newHeader
	// find common ancestor, create list of rolled back and new block hashes
	// 공통의 조상을 찾고 롤복 리스트와 새블록해시를 생성한다
	var oldHeaders, newHeaders []*types.Header
	for oldh.Hash() != newh.Hash() {
		if oldh.Number.Uint64() >= newh.Number.Uint64() {
			oldHeaders = append(oldHeaders, oldh)
			oldh = rawdb.ReadHeader(es.backend.ChainDb(), oldh.ParentHash, oldh.Number.Uint64()-1)
		}
		if oldh.Number.Uint64() < newh.Number.Uint64() {
			newHeaders = append(newHeaders, newh)
			newh = rawdb.ReadHeader(es.backend.ChainDb(), newh.ParentHash, newh.Number.Uint64()-1)
			if newh == nil {
				// happens when CHT syncing, nothing to do
				// CHT 싱크때 발생, nop
				newh = oldh
			}
		}
	}
	// roll back old blocks
	// 오래된 블록들을 롤백한다
	for _, h := range oldHeaders {
		callBack(h, true)
	}
	// check new blocks (array is in reverse order)
	// 새로운 블록을 체크한다
	for i := len(newHeaders) - 1; i >= 0; i-- {
		callBack(newHeaders[i], false)
	}
}

// filter logs of a single header in light client mode
// 라이트 클라이언트 모드의 단일 해더의 필터 로그들
func (es *EventSystem) lightFilterLogs(header *types.Header, addresses []common.Address, topics [][]common.Hash, remove bool) []*types.Log {
	if bloomFilter(header.Bloom, addresses, topics) {
		// Get the logs of the block
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
		defer cancel()
		logsList, err := es.backend.GetLogs(ctx, header.Hash())
		if err != nil {
			return nil
		}
		var unfiltered []*types.Log
		for _, logs := range logsList {
			for _, log := range logs {
				logcopy := *log
				logcopy.Removed = remove
				unfiltered = append(unfiltered, &logcopy)
			}
		}
		logs := filterLogs(unfiltered, nil, nil, addresses, topics)
		if len(logs) > 0 && logs[0].TxHash == (common.Hash{}) {
			// We have matching but non-derived logs
			receipts, err := es.backend.GetReceipts(ctx, header.Hash())
			if err != nil {
				return nil
			}
			unfiltered = unfiltered[:0]
			for _, receipt := range receipts {
				for _, log := range receipt.Logs {
					logcopy := *log
					logcopy.Removed = remove
					unfiltered = append(unfiltered, &logcopy)
				}
			}
			logs = filterLogs(unfiltered, nil, nil, addresses, topics)
		}
		return logs
	}
	return nil
}

// eventLoop (un)installs filters and processes mux events.
// eventLoop는 필터를 설치제거하고 먹스 이벤트를 처리한다
func (es *EventSystem) eventLoop() {
	// Ensure all subscriptions get cleaned up
	defer func() {
		es.pendingLogSub.Unsubscribe()
		es.txsSub.Unsubscribe()
		es.logsSub.Unsubscribe()
		es.rmLogsSub.Unsubscribe()
		es.chainSub.Unsubscribe()
	}()

	index := make(filterIndex)
	for i := UnknownSubscription; i < LastIndexSubscription; i++ {
		index[i] = make(map[rpc.ID]*subscription)
	}

	for {
		select {
		// Handle subscribed events
		case ev := <-es.txsCh:
			es.broadcast(index, ev)
		case ev := <-es.logsCh:
			es.broadcast(index, ev)
		case ev := <-es.rmLogsCh:
			es.broadcast(index, ev)
		case ev := <-es.chainCh:
			es.broadcast(index, ev)
		case ev, active := <-es.pendingLogSub.Chan():
			if !active { // system stopped
			// 시스템 중지
				return
			}
			es.broadcast(index, ev)

		case f := <-es.install:
			if f.typ == MinedAndPendingLogsSubscription {
				// the type are logs and pending logs subscriptions
				// 타입은 로그와 펜딩 로그 구독이다
				index[LogsSubscription][f.id] = f
				index[PendingLogsSubscription][f.id] = f
			} else {
				index[f.typ][f.id] = f
			}
			close(f.installed)

		case f := <-es.uninstall:
			if f.typ == MinedAndPendingLogsSubscription {
				// the type are logs and pending logs subscriptions
				// 타입은 로그와 펜딩 로그 구독이다
				delete(index[LogsSubscription], f.id)
				delete(index[PendingLogsSubscription], f.id)
			} else {
				delete(index[f.typ], f.id)
			}
			close(f.err)

		// System stopped
		// 시스템 중지
		case <-es.txsSub.Err():
			return
		case <-es.logsSub.Err():
			return
		case <-es.rmLogsSub.Err():
			return
		case <-es.chainSub.Err():
			return
		}
	}
}
