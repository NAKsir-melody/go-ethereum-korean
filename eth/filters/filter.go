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

package filters

import (
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/bloombits"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/rpc"
)

type Backend interface {
	ChainDb() ethdb.Database
	EventMux() *event.TypeMux
	HeaderByNumber(ctx context.Context, blockNr rpc.BlockNumber) (*types.Header, error)
	GetReceipts(ctx context.Context, blockHash common.Hash) (types.Receipts, error)
	GetLogs(ctx context.Context, blockHash common.Hash) ([][]*types.Log, error)

	SubscribeNewTxsEvent(chan<- core.NewTxsEvent) event.Subscription
	SubscribeChainEvent(ch chan<- core.ChainEvent) event.Subscription
	SubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent) event.Subscription
	SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription

	BloomStatus() (uint64, uint64)
	ServiceFilter(ctx context.Context, session *bloombits.MatcherSession)
}

// Filter can be used to retrieve and filter logs.
// Filter 는 로그의 반환이나 필터로 사용될수 있다
type Filter struct {
	backend Backend

	db         ethdb.Database
	begin, end int64
	addresses  []common.Address
	topics     [][]common.Hash

	matcher *bloombits.Matcher
}

// New creates a new filter which uses a bloom filter on blocks to figure out whether
// a particular block is interesting or not.
// New함수는 특정 블록이 관심있는 블록인지 아닌지를 구별하기 위해
// 블록들 위에서 동작하는 새로운 블룸필터를 생성한다
func New(backend Backend, begin, end int64, addresses []common.Address, topics [][]common.Hash) *Filter {
	// Flatten the address and topic filter clauses into a single bloombits filter
	// system. Since the bloombits are not positional, nil topics are permitted,
	// which get flattened into a nil byte slice.
	// 주소와 토픽 필터들을 평활화 하여 블룸비트 필터 시스템에 넣는다.
	// nil byte 조각들은 블룸비트들이 제자리가 아니기 때문에 nil 토픽들은 허가된다
	var filters [][][]byte
	if len(addresses) > 0 {
		filter := make([][]byte, len(addresses))
		for i, address := range addresses {
			filter[i] = address.Bytes()
		}
		filters = append(filters, filter)
	}
	for _, topicList := range topics {
		filter := make([][]byte, len(topicList))
		for i, topic := range topicList {
			filter[i] = topic.Bytes()
		}
		filters = append(filters, filter)
	}
	// Assemble and return the filter
	// 조합하고 필터를 반환한다
	size, _ := backend.BloomStatus()

	return &Filter{
		backend:   backend,
		begin:     begin,
		end:       end,
		addresses: addresses,
		topics:    topics,
		db:        backend.ChainDb(),
		matcher:   bloombits.NewMatcher(size, filters),
	}
}

// Logs searches the blockchain for matching log entries, returning all from the
// first block that contains matches, updating the start of the filter accordingly.
// Logs 함수는 적합한 로그 엔트리를 찾기위해 블록체인을 검색하고 
// 매칭되는 첫블록부터 반환하며, 필터의 시작을 업데이트 한다
func (f *Filter) Logs(ctx context.Context) ([]*types.Log, error) {
	// Figure out the limits of the filter range
	// 필터의 범위를 정한다
	header, _ := f.backend.HeaderByNumber(ctx, rpc.LatestBlockNumber)
	if header == nil {
		return nil, nil
	}
	head := header.Number.Uint64()

	if f.begin == -1 {
		f.begin = int64(head)
	}
	end := uint64(f.end)
	if f.end == -1 {
		end = heak
	}
	// Gather all indexed logs, and finish with non indexed ones
	// 모든로그의 항목을 모아서 인덱스 되지 않은 것들과 함께 종료한다
	var (
		logs []*types.Log
		err  error
	)
	size, sections := f.backend.BloomStatus()
	if indexed := sections * size; indexed > uint64(f.begin) {
		if indexed > end {
			logs, err = f.indexedLogs(ctx, end)
		} else {
			logs, err = f.indexedLogs(ctx, indexed-1)
		}
		if err != nil {
			return logs, err
		}
	}
	rest, err := f.unindexedLogs(ctx, end)
	logs = append(logs, rest...)
	return logs, err
}

// indexedLogs returns the logs matching the filter criteria based on the bloom
// bits indexed available locally or via the network.
// IndexedLogs함수는 네트워크상이나 로컬에서 가용한 블룸빗트 인덱스드 된 
// 필터범위에 매칭된 로그를 반환한다
func (f *Filter) indexedLogs(ctx context.Context, end uint64) ([]*types.Log, error) {
	// Create a matcher session and request servicing from the backend
	// 매치세션을 만들고 백엔드로 부터 서비스를 요청한다
	matches := make(chan uint64, 64)

	session, err := f.matcher.Start(ctx, uint64(f.begin), end, matches)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	f.backend.ServiceFilter(ctx, session)

	// Iterate over the matches until exhausted or context closed
	// context가 끝나거나, 다 끝날때까지 매칭된 결과상을 반복한다
	var logs []*types.Log

	for {
		select {
		case number, ok := <-matches:
			// Abort if all matches have been fulfilled
			// 모든 매치가결과가 이행되었다면 abort
			if !ok {
				err := session.Error()
				if err == nil {
					f.begin = int64(end) + 1
				}
				return logs, err
			}
			f.begin = int64(number) + 1

			// Retrieve the suggested block and pull any truly matching logs
			// 제안된 블록을 반환하고 매칭된 로그를 뽑아낸다
			header, err := f.backend.HeaderByNumber(ctx, rpc.BlockNumber(number))
			if header == nil || err != nil {
				return logs, err
			}
			found, err := f.checkMatches(ctx, header)
			if err != nil {
				return logs, err
			}
			logs = append(logs, found...)

		case <-ctx.Done():
			return logs, ctx.Err()
		}
	}
}

// indexedLogs returns the logs matching the filter criteria based on raw block
// iteration and bloom matching.
// indexedLogs함수는 저수준블록 반복과 블룸 매칭을 통해 필터 영역에 맞는 로그들을 반환한다
func (f *Filter) unindexedLogs(ctx context.Context, end uint64) ([]*types.Log, error) {
	var logs []*types.Log

	for ; f.begin <= int64(end); f.begin++ {
		header, err := f.backend.HeaderByNumber(ctx, rpc.BlockNumber(f.begin))
		if header == nil || err != nil {
			return logs, err
		}
		if bloomFilter(header.Bloom, f.addresses, f.topics) {
			found, err := f.checkMatches(ctx, header)
			if err != nil {
				return logs, err
			}
			logs = append(logs, found...)
		}
	}
	return logs, nil
}

// checkMatches checks if the receipts belonging to the given header contain any log events that
// match the filter criteria. This function is called when the bloom filter signals a potential match.
//checkMatches함수는 주어진 헤더에 속한 영수증이 필터영역에 맞는 로그 이벤트를 포함할경우를 체크한다
// 이함수는 블룸필터가 매칭 가능성을 알릴경우 호출된다
func (f *Filter) checkMatches(ctx context.Context, header *types.Header) (logs []*types.Log, err error) {
	// Get the logs of the block
	// 블록의 로그들을 가져온다
	logsList, err := f.backend.GetLogs(ctx, header.Hash())
	if err != nil {
		return nil, err
	}
	var unfiltered []*types.Log
	for _, logs := range logsList {
		unfiltered = append(unfiltered, logs...)
	}
	logs = filterLogs(unfiltered, nil, nil, f.addresses, f.topics)
	if len(logs) > 0 {
		// We have matching logs, check if we need to resolve full logs via the light client
		// 매칭된 로그가 있다 라이트 클라이언트 위에서 풀로그로 처리해야하는 경우를 체크한다
		if logs[0].TxHash == (common.Hash{}) {
			receipts, err := f.backend.GetReceipts(ctx, header.Hash())
			if err != nil {
				return nil, err
			}
			unfiltered = unfiltered[:0]
			for _, receipt := range receipts {
				unfiltered = append(unfiltered, receipt.Logs...)
			}
			logs = filterLogs(unfiltered, nil, nil, f.addresses, f.topics)
		}
		return logs, nil
	}
	return nil, nil
}

func includes(addresses []common.Address, a common.Address) bool {
	for _, addr := range addresses {
		if addr == a {
			return true
		}
	}

	return false
}

// filterLogs creates a slice of logs matching the given criteria.
// filterLogs 함수는 주어진 영역에 맞는 로그 조각을 생성한다
func filterLogs(logs []*types.Log, fromBlock, toBlock *big.Int, addresses []common.Address, topics [][]common.Hash) []*types.Log {
	var ret []*types.Log
Logs:
	for _, log := range logs {
		if fromBlock != nil && fromBlock.Int64() >= 0 && fromBlock.Uint64() > log.BlockNumber {
			continue
		}
		if toBlock != nil && toBlock.Int64() >= 0 && toBlock.Uint64() < log.BlockNumber {
			continue
		}

		if len(addresses) > 0 && !includes(addresses, log.Address) {
			continue
		}
		// If the to filtered topics is greater than the amount of topics in logs, skip.
		// 만약 수행한 토픽들이 로그에있는 토픽보다 많은경우 스킵한다.
		if len(topics) > len(log.Topics) {
			continue Logs
		}
		for i, topics := range topics {
			match := len(topics) == 0 // empty rule set == wildcard
			for _, topic := range topics {
				if log.Topics[i] == topic {
					match = true
					break
				}
			}
			if !match {
				continue Logs
			}
		}
		ret = append(ret, log)
	}
	return ret
}

func bloomFilter(bloom types.Bloom, addresses []common.Address, topics [][]common.Hash) bool {
	if len(addresses) > 0 {
		var included bool
		for _, addr := range addresses {
			if types.BloomLookup(bloom, addr) {
				included = true
				break
			}
		}
		if !included {
			return false
		}
	}

	for _, sub := range topics {
		included := len(sub) == 0 // empty rule set == wildcard
		for _, topic := range sub {
			if types.BloomLookup(bloom, topic) {
				included = true
				break
			}
		}
		if !included {
			return false
		}
	}
	return true
}
