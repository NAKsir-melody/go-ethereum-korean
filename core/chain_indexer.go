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

package core

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
)

// ChainIndexerBackend defines the methods needed to process chain segments in
// the background and write the segment results into the database. These can be
// used to create filter blooms or CHTs.
// 체인 인덱서 백엔드는 체인조각를 백그라운드로 처리하고
// 조각의 결과를 DB에 쓰기위해 위한 방법들을 정의한다
// 이 방법들은 블룸필터나 CHT를 생성하는데 이용될 수 있다.
type ChainIndexerBackend interface {
	// Reset initiates the processing of a new chain segment, potentially terminating
	// any partially completed operations (in case of a reorg).
	// reset함수는 새로운 체인 조각의 처리를 초기화 한다. (재구성의 경우)부분적으로 완료된 동작들에의해
	// 종료될수 있다

	Reset(section uint64, prevHead common.Hash) error

	// Process crunches through the next header in the chain segment. The caller
	// will ensure a sequential order of headers.
	//Process함수는 체인조각의 다음 헤더를 향해 나아간다
	// 호출자는 헤더의 순서를 보장해야 한다
	Process(header *types.Header)

	// Commit finalizes the section metadata and stores it into the database.
	// Commit함수는 메타데이터 섹션을 최종화하고 db에저장한다
	Commit() error
}

// ChainIndexerChain interface is used for connecting the indexer to a blockchain
// ChainIndexerChain은 인덱서를 블록체인에 연결하기 위해 사용되는 인터페이스이다
type ChainIndexerChain interface {
	// CurrentHeader retrieves the latest locally known header.
	// CurrentHeader 함수는 로컬에서 알려진 헤더중 최근값을 반환한다
	CurrentHeader() *types.Header

	// SubscribeChainEvent subscribes to new head header notifications.
	// SubscribeChainEvent함수는 새로운 헤드 헤더의 알람을 구독한다
	SubscribeChainEvent(ch chan<- ChainEvent) event.Subscription
}

// ChainIndexer does a post-processing job for equally sized sections of the
// canonical chain (like BlooomBits and CHT structures). A ChainIndexer is
// connected to the blockchain through the event system by starting a
// ChainEventLoop in a goroutine.
//
// Further child ChainIndexers can be added which use the output of the parent
// section indexer. These child indexers receive new head notifications only
// after an entire section has been finished or in case of rollbacks that might
// affect already finished sections.

// 체인 인덱서는 bloomBits, CHT struct와 같이 동일한 크기로 구획된
// 캐노니컬 체인 상의 업무 후처리를 담당한다 
// 체인 인덱서는 체인이벤트 루프를 go루틴 안에서 시작함으로서
// 이벤트 시스템을 통해 블록체인에 연결된다. 
// @sigmoid: 블록체인과 이벤트로 통신한다는 뜻

// 부모 섹션 인덱서의 출력값을 사용하는 자식 인덱서들이 추가될 수 있다.
// 이러한 자식인덱서들은 모든 섹션이 끝나거나
// 이미 끝난 섹션에 영향을 주는 롤백시에만 새로운 헤드 알림을 받을수있다
type ChainIndexer struct {
	chainDb  ethdb.Database      // Chain database to index the data from
	// 데이터를 얻어올 체인 DB
	indexDb  ethdb.Database      // Prefixed table-view of the db to write index metadata into
	// 인덱스 메타데이터를 쓰기위한 db의 tableVeiw
	backend  ChainIndexerBackend // Background processor generating the index data content
	// 인덱스 데이터를 생성하는 백그라운드 프로세서
	children []*ChainIndexer     // Child indexers to cascade chain updates to
	// 체인을 업데이트하기 위한 중첩 인덱서

	active uint32          // Flag whether the event loop was started
	update chan struct{}   // Notification channel that headers should be processed
	quit   chan chan error // Quit channel to tear down running goroutines
	// 이벤트 루프 시작여부
	// 헤더가 처리되어야 한다는 이벤트 체널
	// 실행중인 고루틴을 종료시키기 위한 채널

	sectionSize uint64 // Number of blocks in a single chain segment to process
	confirmsReq uint64 // Number of confirmations before processing a completed segment
	// 단일 체인조각내의 처리해야할 블록수
	// 체인조각을 완료 처리 시키기전까지의 컨펌수

	storedSections uint64 // Number of sections successfully indexed into the database
	knownSections  uint64 // Number of sections known to be complete (block wise)
	cascadedHead   uint64 // Block number of the last completed section cascaded to subindexers
	// db에 성공적으로 인덱싱된 섹션의수
	// 블록단위로 완료될것으로 알려진 섹션의 수
	// 보조 인덱서에 중첩된 마지막 완료섹션의 블록번호

	throttling time.Duration // Disk throttling to prevent a heavy upgrade from hogging resources
	// 헤비한 업그레이드를 방지하기 위한 디스크 병목값

	log  log.Logger
	lock sync.RWMutex
}

// NewChainIndexer creates a new chain indexer to do background processing on
// chain segments of a given size after certain number of confirmations passed.
// The throttling parameter might be used to prevent database thrashing.
// NewChainIndex 함수는 몇번의 컨펌 지난후 체인 세그먼트를 
// 정해진 사이즈만큼 백그라운드에서 처리하는 체인 인덱서를 생성한다
// 병목 변수는 데이터 베이스 동작의 엉킴을 방지하는데 쓰일수있다
func NewChainIndexer(chainDb, indexDb ethdb.Database, backend ChainIndexerBackend, section, confirm uint64, throttling time.Duration, kind string) *ChainIndexer {
	c := &ChainIndexer{
		chainDb:     chainDb,
		indexDb:     indexDb,
		backend:     backend,
		update:      make(chan struct{}, 1),
		quit:        make(chan chan error),
		sectionSize: section,
		confirmsReq: confirm,
		throttling:  throttling,
		log:         log.New("type", kind),
	}
	// Initialize database dependent fields and start the updater
	// db의존적 필드를 초기화 하고 업데이터를 시작한다
	c.loadValidSections()
	go c.updateLoop()

	return c
}

// AddKnownSectionHead marks a new section head as known/processed if it is newer
// than the already known best section head
// AddKnownSectionHead 함수는 만약 새로운 섹션의 헤드가 이미 알려진 
// best 섹션 헤더보다 새롭다면 새로운 섹션의 헤드를 알려짐/처리됨으로 표시한다
func (c *ChainIndexer) AddKnownSectionHead(section uint64, shead common.Hash) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if section < c.storedSections {
		return
	}
	c.setSectionHead(section, shead)
	c.setValidSections(section + 1)
}

// Start creates a goroutine to feed chain head events into the indexer for
// cascading background processing. Children do not need to be started, they
// are notified about new events by their parents.
// Start함수는 중첩된 백그라운드 처리를 위해 
// 체인 헤드 이벤트를 인덱서안으로 feed할 고루틴을 생성한다
// 자식들은 부모로부터 새로운 이벤트를 수신하므로 이함수를 호출할 필요가 없다.
func (c *ChainIndexer) Start(chain ChainIndexerChain) {
	events := make(chan ChainEvent, 10)
	sub := chain.SubscribeChainEvent(events)

	go c.eventLoop(chain.CurrentHeader(), events, sub)
}

// Close tears down all goroutines belonging to the indexer and returns any error
// that might have occurred internally.
// Close 함수는 이 인덱서에 속한 모든 고루틴을 멈추고 내부적으로 발생한 모든 에러를 반환한다
func (c *ChainIndexer) Close() error {
	var errs []error

	// Tear down the primary update loop
	// 주 업데이트 루프를 끝낸다
	errc := make(chan error)
	c.quit <- errc
	if err := <-errc; err != nil {
		errs = append(errs, err)
	}
	// If needed, tear down the secondary event loop
	// 필요하다면 보조 이벤트 루프를 끝낸다
	if atomic.LoadUint32(&c.active) != 0 {
		c.quit <- errc
		if err := <-errc; err != nil {
			errs = append(errs, err)
		}
	}
	// Close all children
	// 모든 자식을 닫는다
	for _, child := range c.children {
		if err := child.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	// Return any failures
	// 발생한 실패를 리턴한다
	switch {
	case len(errs) == 0:
		return nil

	case len(errs) == 1:
		return errs[0]

	default:
		return fmt.Errorf("%v", errs)
	}
}

// eventLoop is a secondary - optional - event loop of the indexer which is only
// started for the outermost indexer to push chain head events into a processing
// queue.
// 이벤트 루프는 체인헤드 이벤트를 프로세싱 큐에 넣기 위한 최외각의 인덱서에서만 
// 실행되는 부수적인 (or 선택적) 이벤트 루프이다
func (c *ChainIndexer) eventLoop(currentHeader *types.Header, events chan ChainEvent, sub event.Subscription) {
	// Mark the chain indexer as active, requiring an additional teardown
	// 체인 인덱서를 활성화로 표시하고 추가적인 해체를 요구한다
	atomic.StoreUint32(&c.active, 1)

	defer sub.Unsubscribe()

	// Fire the initial new head event to start any outstanding processing
	// 대기중인 처리를 시작하기 위해 초기화한 새로운 헤드이벤트를 발생시킨다
	c.newHead(currentHeader.Number.Uint64(), false)

	var (
		prevHeader = currentHeader
		prevHash   = currentHeader.Hash()
	)
	for {
		select {
		case errc := <-c.quit:
			// Chain indexer terminating, report no failure and abort
			// 체인인덱서가 종류중. 문제가 없을 보고
			errc <- nil
			return

		case ev, ok := <-events:
			// Received a new event, ensure it's not nil (closing) and update
			// 새로운 이벤트를 수신. closing evnet인지 체크하고 업데이트한다
			if !ok {
				errc := <-c.quit
				errc <- nil
				return
			}
			header := ev.Block.Header()
			if header.ParentHash != prevHash {
				// Reorg to the common ancestor (might not exist in light sync mode, skip reorg then)
				// TODO(karalabe, zsfelfoldi): This seems a bit brittle, can we detect this case explicitly?

				// TODO(karalabe): This operation is expensive and might block, causing the event system to
				// potentially also lock up. We need to do with on a different thread somehow.
				// 공통 조상으로 재구성
				// lockup가능성이 있고, 다른 스레드에서 처리하는 방법을 해야한다
				if h := rawdb.FindCommonAncestor(c.chainDb, prevHeader, header); h != nil {
					c.newHead(h.Number.Uint64(), true)
				}
			}
			c.newHead(header.Number.Uint64(), false)

			prevHeader, prevHash = header, header.Hash()
		}
	}
}

// newHead notifies the indexer about new chain heads and/or reorgs.
// newHead 함수는 새체인헤드 혹은 체인재구성을 인덱서에게 알린다
func (c *ChainIndexer) newHead(head uint64, reorg bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// If a reorg happened, invalidate all sections until that point
		// 재구성이 일어난다면 특정포인트까지 모든 섹션을 무효화한다
	if reorg {
		// Revert the known section number to the reorg point
		// 재구성 포인트까지 알려진 섹션 번호를 복원한다
		changed := head / c.sectionSize
		if changed < c.knownSections {
			c.knownSections = changed
		}
		// Revert the stored sections from the database to the reorg point
		// 재구성 포인트까지 db의 저장된 섹션을 복원한다
		if changed < c.storedSections {
			c.setValidSections(changed)
		}
		// Update the new head number to the finalized section end and notify children
		// 새로우 헤드넘버를 갱신하여 섹션을 최종화하고 자식에게 알린다
		head = changed * c.sectionSize

		if head < c.cascadedHead {
			c.cascadedHead = head
			for _, child := range c.children {
				child.newHead(c.cascadedHead, true)
			}
		}
		return
	}
	// No reorg, calculate the number of newly known sections and update if high enough
	// 재구성이 일어나지 않을 경우, 새롭게 알려진 섹션의 번호를 계산하고
	// 충분이 높다면 업데이트한다
	var sections uint64
	if head >= c.confirmsReq {
		sections = (head + 1 - c.confirmsReq) / c.sectionSize
		if sections > c.knownSections {
			c.knownSections = sections

			select {
			case c.update <- struct{}{}:
			default:
			}
		}
	}
}

// updateLoop is the main event loop of the indexer which pushes chain segments
// down into the processing backend.
// 이 함수는 인덱서의 메인 이벤트 루프로서 체인 세그먼트들을 프로세싱 백엔드에 넣는다
func (c *ChainIndexer) updateLoop() {
	var (
		updating bool
		updated  time.Time
	)

	for {
		select {
		case errc := <-c.quit:
			// Chain indexer terminating, report no failure and abort
			// 체인 인덱서 종료. 문제없음을 알린다
			errc <- nil
			return

		case <-c.update:
			// Section headers completed (or rolled back), update the index
			// 섹션 헤더들의 종료나 롤벡. 인덱스를 업데이트 한다
			c.lock.Lock()
			if c.knownSections > c.storedSections {
				// Periodically print an upgrade log message to the user
				// 주기적으로 upgrade log를 유저에제 프린트한다
				if time.Since(updated) > 8*time.Second {
					if c.knownSections > c.storedSections+1 {
						updating = true
						c.log.Info("Upgrading chain index", "percentage", c.storedSections*100/c.knownSections)
					}
					updated = time.Now()
				}
				// Cache the current section count and head to allow unlocking the mutex
				// 현재 섹션 카운트와 헤드를 mutex를 풀기위해 캐싱한다
				section := c.storedSections
				var oldHead common.Hash
				if section > 0 {
					oldHead = c.SectionHead(section - 1)
				}
				// Process the newly defined section in the background
				// 새롭게 정의된 섹션을 백그라운드에서 처리한다
				c.lock.Unlock()
				newHead, err := c.processSection(section, oldHead)
				if err != nil {
					c.log.Error("Section processing failed", "error", err)
				}
				c.lock.Lock()

				// If processing succeeded and no reorgs occcurred, mark the section completed
				// 처리가 성공했고 재구성이 일어나지 않았따면 섹션을 완료상태로 마킹한다
				if err == nil && oldHead == c.SectionHead(section-1) {
					c.setSectionHead(section, newHead)
					c.setValidSections(section + 1)
					if c.storedSections == c.knownSections && updating {
						updating = false
						c.log.Info("Finished upgrading chain index")
					}

					c.cascadedHead = c.storedSections*c.sectionSize - 1
					for _, child := range c.children {
						c.log.Trace("Cascading chain index update", "head", c.cascadedHead)
						child.newHead(c.cascadedHead, false)
					}
				} else {
					// If processing failed, don't retry until further notification
					// 처리가 실패했을 경우 더이상의 노티가 없는동안은 재시도 하지 않는다
					c.log.Debug("Chain index processing failed", "section", section, "err", err)
					c.knownSections = c.storedSections
				}
			}
			// If there are still further sections to process, reschedule
			// 만약 처리해야할 섹션이 더남아있다면 리스케쥴한다
			if c.knownSections > c.storedSections {
				time.AfterFunc(c.throttling, func() {
					select {
					case c.update <- struct{}{}:
					default:
					}
				})
			}
			c.lock.Unlock()
		}
	}
}

// processSection processes an entire section by calling backend functions while
// ensuring the continuity of the passed headers. Since the chain mutex is not
// held while processing, the continuity can be broken by a long reorg, in which
// case the function returns with an error.
// processSection 함수는 전달되는 헤더들의 연속성이 보장되는 동안 
// 백엔드 함수를 불러 전체 섹션을 처리한다 처리하는 동안 체인 뮤텍스가 잡혀있지 않을 경우,
// 긴시간이 걸리는 체인 재구성에 의해 연속성이 깨질수 있는데, 이런경우 함수는 에러를 리턴한다
func (c *ChainIndexer) processSection(section uint64, lastHead common.Hash) (common.Hash, error) {
	c.log.Trace("Processing new chain section", "section", section)

	// Reset and partial processing
	// 재설정 및 부분 처리
	if err := c.backend.Reset(section, lastHead); err != nil {
		c.setValidSections(0)
		return common.Hash{}, err
	}

	for number := section * c.sectionSize; number < (section+1)*c.sectionSize; number++ {
		hash := rawdb.ReadCanonicalHash(c.chainDb, number)
		if hash == (common.Hash{}) {
			return common.Hash{}, fmt.Errorf("canonical block #%d unknown", number)
		}
		header := rawdb.ReadHeader(c.chainDb, hash, number)
		if header == nil {
			return common.Hash{}, fmt.Errorf("block #%d [%x…] not found", number, hash[:4])
		} else if header.ParentHash != lastHead {
			return common.Hash{}, fmt.Errorf("chain reorged during section processing")
		}
		c.backend.Process(header)
		lastHead = header.Hash()
	}
	if err := c.backend.Commit(); err != nil {
		c.log.Error("Section commit failed", "error", err)
		return common.Hash{}, err
	}
	return lastHead, nil
}

// Sections returns the number of processed sections maintained by the indexer
// and also the information about the last header indexed for potential canonical
// verifications.
// Section 함수는 인덱서에의해 관리된 처리완료된 섹션들의 갯수를 반환하고
// 혹시나 있을 캐노니컬 검증을 위해 색인된 마지막 헤더에 대한 정보를 제공한다
func (c *ChainIndexer) Sections() (uint64, uint64, common.Hash) {
	c.lock.Lock()
	defer c.lock.Unlock()

	return c.storedSections, c.storedSections*c.sectionSize - 1, c.SectionHead(c.storedSections - 1)
}

// AddChildIndexer adds a child ChainIndexer that can use the output of this one
// AddChildIndexer함수는 이 인덱서의 아웃풋을 사용할수 있는 자식 인덱서를 추가한다
func (c *ChainIndexer) AddChildIndexer(indexer *ChainIndexer) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.children = append(c.children, indexer)

	// Cascade any pending updates to new children too
	// 대기중인 업데이트르 새로운 자식에게 중첩시킨다
	if c.storedSections > 0 {
		indexer.newHead(c.storedSections*c.sectionSize-1, false)
	}
}

// loadValidSections reads the number of valid sections from the index database
// and caches is into the local state.
// loadValidSections함수는 유효한 색션의 수를 DB로 부터 읽어 로컬 상태에 캐싱한다
func (c *ChainIndexer) loadValidSections() {
	data, _ := c.indexDb.Get([]byte("count"))
	if len(data) == 8 {
		c.storedSections = binary.BigEndian.Uint64(data[:])
	}
}

// setValidSections writes the number of valid sections to the index database
// setValidSections 함수는 유효한 섹션의 갯수를 index 에 쓴다
func (c *ChainIndexer) setValidSections(sections uint64) {
	// Set the current number of valid sections in the database
	// 현재 DB상의 유효한 섹션의 번호를 설정한다
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], sections)
	c.indexDb.Put([]byte("count"), data[:])

	// Remove any reorged sections, caching the valids in the mean time
	// 재구성된 모든 섹션을 버리고 어느정도 시간동안 유효화한것들을 캐싱힌다
	for c.storedSections > sections {
		c.storedSections--
		c.removeSectionHead(c.storedSections)
	}
	c.storedSections = sections // needed if new > old
}

// SectionHead retrieves the last block hash of a processed section from the
// index database.
// 인덱스 DB에서 처리된 섹션의 마지막 블록해시를 반환한다
func (c *ChainIndexer) SectionHead(section uint64) common.Hash {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], section)

	hash, _ := c.indexDb.Get(append([]byte("shead"), data[:]...))
	if len(hash) == len(common.Hash{}) {
		return common.BytesToHash(hash)
	}
	return common.Hash{}
}

// setSectionHead writes the last block hash of a processed section to the index
// database.
// setSectionHead 함수는 처리된 섹션의 마지막 블록 해시를 인덱스 DB에 쓴다
func (c *ChainIndexer) setSectionHead(section uint64, hash common.Hash) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], section)

	c.indexDb.Put(append([]byte("shead"), data[:]...), hash.Bytes())
}

// removeSectionHead removes the reference to a processed section from the index
// database.
// removeSectionHead 함수는 처리된 섹션에 대한 참조를 인덱스 DB로 부터 제거한다
func (c *ChainIndexer) removeSectionHead(section uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], section)

	c.indexDb.Delete(append([]byte("shead"), data[:]...))
}
