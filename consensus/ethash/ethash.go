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

// Package ethash implements the ethash proof-of-work consensus engine.
// ethash package는 ethash pow 합의엔진을 구현한다
package ethash

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unsafe"

	mmap "github.com/edsrzf/mmap-go"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/hashicorp/golang-lru/simplelru"
)

var ErrInvalidDumpMagic = errors.New("invalid dump magic")

var (
	// maxUint256 is a big integer representing 2^256-1
	// 2^256 -1을 표현하는 정수
	maxUint256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), big.NewInt(0))

	// sharedEthash is a full instance that can be shared between multiple users.
	// 다수의 유저에게 sharing가능한 full instance
	sharedEthash = New(Config{"", 3, 0, "", 1, 0, ModeNormal})

	// algorithmRevision is the data structure version used for file naming.
	// 파일 이름을 위한 데이터 구조 버전
	algorithmRevision = 23

	// dumpMagic is a dataset dump header to sanity check a data dump.
	// 무결성 체크를 위한 데이터셋 덤프 헤더
	dumpMagic = []uint32{0xbaddcafe, 0xfee1dead}
)

// isLittleEndian returns whether the local system is running in little or big
// endian byte order.
// isLittleEndian 함수는 로컬 시스템이 어떤바이트 오더에서 동작하는지 반환한다
func isLittleEndian() bool {
	n := uint32(0x01020304)
	return *(*byte)(unsafe.Pointer(&n)) == 0x04
}

// memoryMap tries to memory map a file of uint32s for read only access.
// memoryMap 함수는 메모리를 uin32의 읽기전용 파일로 맵핑한다
func memoryMap(path string) (*os.File, mmap.MMap, []uint32, error) {
	file, err := os.OpenFile(path, os.O_RDONLY, 0644)
	if err != nil {
		return nil, nil, nil, err
	}
	mem, buffer, err := memoryMapFile(file, false)
	if err != nil {
		file.Close()
		return nil, nil, nil, err
	}
	for i, magic := range dumpMagic {
		if buffer[i] != magic {
			mem.Unmap()
			file.Close()
			return nil, nil, nil, ErrInvalidDumpMagic
		}
	}
	return file, mem, buffer[len(dumpMagic):], err
}

// memoryMapFile tries to memory map an already opened file descriptor.
// memoryMapFile 함수는 메모리를 이미 열린 fd에 map한다
func memoryMapFile(file *os.File, write bool) (mmap.MMap, []uint32, error) {
	// Try to memory map the file
	flag := mmap.RDONLY
	if write {
		flag = mmap.RDWR
	}
	mem, err := mmap.Map(file, flag, 0)
	if err != nil {
		return nil, nil, err
	}
	// Yay, we managed to memory map the file, here be dragons
	header := *(*reflect.SliceHeader)(unsafe.Pointer(&mem))
	header.Len /= 4
	header.Cap /= 4

	return mem, *(*[]uint32)(unsafe.Pointer(&header)), nil
}

// memoryMapAndGenerate tries to memory map a temporary file of uint32s for write
// access, fill it with the data from a generator and then move it into the final
// path requested.
// memoryMapAndGenerate 함수는 쓰기전용의 uin32의 임시파일을 위한 메모리맵을 만들고
// 데이터 생성자로부터 오는 데이터를 쓰고 최종 경로로 보낸다
func memoryMapAndGenerate(path string, size uint64, generator func(buffer []uint32)) (*os.File, mmap.MMap, []uint32, error) {
	// Ensure the data folder exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, nil, nil, err
	}
	// Create a huge temporary empty file to fill with data
	temp := path + "." + strconv.Itoa(rand.Int())

	dump, err := os.Create(temp)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = dump.Truncate(int64(len(dumpMagic))*4 + int64(size)); err != nil {
		return nil, nil, nil, err
	}
	// Memory map the file for writing and fill it with the generator
	mem, buffer, err := memoryMapFile(dump, true)
	if err != nil {
		dump.Close()
		return nil, nil, nil, err
	}
	copy(buffer, dumpMagic)

	data := buffer[len(dumpMagic):]
	generator(data)

	if err := mem.Unmap(); err != nil {
		return nil, nil, nil, err
	}
	if err := dump.Close(); err != nil {
		return nil, nil, nil, err
	}
	if err := os.Rename(temp, path); err != nil {
		return nil, nil, nil, err
	}
	return memoryMap(path)
}

// lru tracks caches or datasets by their last use time, keeping at most N of them.
// lru구조체는 마지막 사용된 시간에 따라 캐시나 데이터셋을 트랙킹하며
// 그들중 최신의 N개를 저장한다
type lru struct {
	what string
	new  func(epoch uint64) interface{}
	mu   sync.Mutex
	// Items are kept in a LRU cache, but there is a special case:
	// We always keep an item for (highest seen epoch) + 1 as the 'future item'.
	cache      *simplelru.LRU
	future     uint64
	futureItem interface{}
}

// newlru create a new least-recently-used cache for either the verification caches
// or the mining datasets.
// newlru함수는 검증캐시나 마이닝 데이터셋으로 사용되기 위한 least-recently-used cache
// 를 생성한다
func newlru(what string, maxItems int, new func(epoch uint64) interface{}) *lru {
	if maxItems <= 0 {
		maxItems = 1
	}
	cache, _ := simplelru.NewLRU(maxItems, func(key, value interface{}) {
		log.Trace("Evicted ethash "+what, "epoch", key)
	})
	return &lru{what: what, new: new, cache: cache}
}

// get retrieves or creates an item for the given epoch. The first return value is always
// non-nil. The second return value is non-nil if lru thinks that an item will be useful in
// the near future.
// get 함수는 주어진 에포크에 대한 아이템을 반환/생성한다.
// 첫 반환값은 언제나 nil이 아니다.
// 두번째 반환값은 item이 가까운 미래에 유효하다고 lru가 판단하면nil이 아니다
func (lru *lru) get(epoch uint64) (item, future interface{}) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	// Get or create the item for the requested epoch.
	item, ok := lru.cache.Get(epoch)
	if !ok {
		if lru.future > 0 && lru.future == epoch {
			item = lru.futureItem
		} else {
			log.Trace("Requiring new ethash "+lru.what, "epoch", epoch)
			item = lru.new(epoch)
		}
		lru.cache.Add(epoch, item)
	}
	// Update the 'future item' if epoch is larger than previously seen.
	if epoch < maxEpoch-1 && lru.future < epoch+1 {
		log.Trace("Requiring new future ethash "+lru.what, "epoch", epoch+1)
		future = lru.new(epoch + 1)
		lru.future = epoch + 1
		lru.futureItem = future
	}
	return item, future
}

// cache wraps an ethash cache with some metadata to allow easier concurrent use.
// cache 구조체는 쉽고 병렬적인 사용을 위해
// ethash 캐시를 약간의 메타데이터와 함께 포함한다
type cache struct {
	epoch uint64    // Epoch for which this cache is relevant
	dump  *os.File  // File descriptor of the memory mapped cache
	mmap  mmap.MMap // Memory map itself to unmap before releasing
	cache []uint32  // The actual cache data content (may be memory mapped)
	once  sync.Once // Ensures the cache is generated only once
}

// newCache creates a new ethash verification cache and returns it as a plain Go
// interface to be usable in an LRU cache.
// newCache 함수는 새로운 ethash 검증캐시를 만들고 LRU 캐시에 유용할만한 
// 고인터페이스로 반환한다
func newCache(epoch uint64) interface{} {
	return &cache{epoch: epoch}
}

// generate ensures that the cache content is generated before use.
// generate함수는 캐시컨텐츠가 사용되기 전에 생성되었는지 보증한다
func (c *cache) generate(dir string, limit int, test bool) {
	c.once.Do(func() {
		size := cacheSize(c.epoch*epochLength + 1)
		seed := seedHash(c.epoch*epochLength + 1)
		if test {
			size = 1024
		}
		// If we don't store anything on disk, generate and return.
		if dir == "" {
			c.cache = make([]uint32, size/4)
			generateCache(c.cache, c.epoch, seed)
			return
		}
		// Disk storage is needed, this will get fancy
		var endian string
		if !isLittleEndian() {
			endian = ".be"
		}
		path := filepath.Join(dir, fmt.Sprintf("cache-R%d-%x%s", algorithmRevision, seed[:8], endian))
		logger := log.New("epoch", c.epoch)

		// We're about to mmap the file, ensure that the mapping is cleaned up when the
		// cache becomes unused.
		runtime.SetFinalizer(c, (*cache).finalizer)

		// Try to load the file from disk and memory map it
		var err error
		c.dump, c.mmap, c.cache, err = memoryMap(path)
		if err == nil {
			logger.Debug("Loaded old ethash cache from disk")
			return
		}
		logger.Debug("Failed to load old ethash cache", "err", err)

		// No previous cache available, create a new cache file to fill
		c.dump, c.mmap, c.cache, err = memoryMapAndGenerate(path, size, func(buffer []uint32) { generateCache(buffer, c.epoch, seed) })
		if err != nil {
			logger.Error("Failed to generate mapped ethash cache", "err", err)

			c.cache = make([]uint32, size/4)
			generateCache(c.cache, c.epoch, seed)
		}
		// Iterate over all previous instances and delete old ones
		for ep := int(c.epoch) - limit; ep >= 0; ep-- {
			seed := seedHash(uint64(ep)*epochLength + 1)
			path := filepath.Join(dir, fmt.Sprintf("cache-R%d-%x%s", algorithmRevision, seed[:8], endian))
			os.Remove(path)
		}
	})
}

// finalizer unmaps the memory and closes the file.
// finalize는 메모리를 언맵하고 파일을 닫는다
func (c *cache) finalizer() {
	if c.mmap != nil {
		c.mmap.Unmap()
		c.dump.Close()
		c.mmap, c.dump = nil, nil
	}
}

// dataset wraps an ethash dataset with some metadata to allow easier concurrent use.
// dataset 구조체는 쉽고 병렬적인 사용을 위해
// ethash 캐시를 약간의 메타데이터와 함께 포함한다
type dataset struct {
	epoch   uint64    // Epoch for which this cache is relevant
	// 이 캐시가 관련된 에포크
	dump    *os.File  // File descriptor of the memory mapped cache
	// 메모리 맵 캐시의 파일 디스크립터
	mmap    mmap.MMap // Memory map itself to unmap before releasing
	// 해지전에 unmap할 매모리맵
	dataset []uint32  // The actual cache data content
	// 캐시 데이터의 실제 내용
	once    sync.Once // Ensures the cache is generated only once
	// 캐시가 한번만 생성되었음을 보증
}

// newDataset creates a new ethash mining dataset and returns it as a plain Go
// interface to be usable in an LRU cache.
// newDataset함수는 새로운 ethash 마이닝 데이터셋을 생성하고
// LRU캐시에 사용될수 있또록 고인터페이스로 전달한다
func newDataset(epoch uint64) interface{} {
	return &dataset{epoch: epoch}
}

// generate ensures that the dataset content is generated before use.
// generate 함수는 데이터셋 컨텐츠가 사용전에 생성되었는지 확증한다
func (d *dataset) generate(dir string, limit int, test bool) {
	d.once.Do(func() {
		csize := cacheSize(d.epoch*epochLength + 1)
		dsize := datasetSize(d.epoch*epochLength + 1)
		seed := seedHash(d.epoch*epochLength + 1)
		if test {
			csize = 1024
			dsize = 32 * 1024
		}
		// If we don't store anything on disk, generate and return
		if dir == "" {
			cache := make([]uint32, csize/4)
			generateCache(cache, d.epoch, seed)

			d.dataset = make([]uint32, dsize/4)
			generateDataset(d.dataset, d.epoch, cache)
		}
		// Disk storage is needed, this will get fancy
		var endian string
		if !isLittleEndian() {
			endian = ".be"
		}
		path := filepath.Join(dir, fmt.Sprintf("full-R%d-%x%s", algorithmRevision, seed[:8], endian))
		logger := log.New("epoch", d.epoch)

		// We're about to mmap the file, ensure that the mapping is cleaned up when the
		// cache becomes unused.
		runtime.SetFinalizer(d, (*dataset).finalizer)

		// Try to load the file from disk and memory map it
		var err error
		d.dump, d.mmap, d.dataset, err = memoryMap(path)
		if err == nil {
			logger.Debug("Loaded old ethash dataset from disk")
			return
		}
		logger.Debug("Failed to load old ethash dataset", "err", err)

		// No previous dataset available, create a new dataset file to fill
		cache := make([]uint32, csize/4)
		generateCache(cache, d.epoch, seed)

		d.dump, d.mmap, d.dataset, err = memoryMapAndGenerate(path, dsize, func(buffer []uint32) { generateDataset(buffer, d.epoch, cache) })
		if err != nil {
			logger.Error("Failed to generate mapped ethash dataset", "err", err)

			d.dataset = make([]uint32, dsize/2)
			generateDataset(d.dataset, d.epoch, cache)
		}
		// Iterate over all previous instances and delete old ones
		for ep := int(d.epoch) - limit; ep >= 0; ep-- {
			seed := seedHash(uint64(ep)*epochLength + 1)
			path := filepath.Join(dir, fmt.Sprintf("full-R%d-%x%s", algorithmRevision, seed[:8], endian))
			os.Remove(path)
		}
	})
}

// finalizer closes any file handlers and memory maps open.
// finalizer 함수는 열려있는 파일 핸들과 메모리맵을 닫는다
func (d *dataset) finalizer() {
	if d.mmap != nil {
		d.mmap.Unmap()
		d.dump.Close()
		d.mmap, d.dump = nil, nil
	}
}

// MakeCache generates a new ethash cache and optionally stores it to disk.
// MakeCache 함수는 새로운 ehtash 캐시를 생성하고 디스크에 선택적으로 저장한다
func MakeCache(block uint64, dir string) {
	c := cache{epoch: block / epochLength}
	c.generate(dir, math.MaxInt32, false)
}

// MakeDataset generates a new ethash dataset and optionally stores it to disk.
// MakeDataset함수는 새로운 ehtash 데이터셋을 생성하고 디스크에 선택적으로 저장한다
func MakeDataset(block uint64, dir string) {
	d := dataset{epoch: block / epochLength}
	d.generate(dir, math.MaxInt32, false)
}

// Mode defines the type and amount of PoW verification an ethash engine makes.
// Mode는 ethhash 엔진이 만드는 pow 검증량과 타입을 정의한다
type Mode uint

const (
	ModeNormal Mode = iota
	ModeShared
	ModeTest
	ModeFake
	ModeFullFake
)

// Config are the configuration parameters of the ethash.
// Config는 ethash의 설정파라미터이다
type Config struct {
	CacheDir       string
	CachesInMem    int
	CachesOnDisk   int
	DatasetDir     string
	DatasetsInMem  int
	DatasetsOnDisk int
	PowMode        Mode
}

// Ethash is a consensus engine based on proot-of-work implementing the ethash
// algorithm.
// Ethash구조체는 ethash 알고리즘을 구현한 pow 합의 엔진이다
type Ethash struct {
	config Config

	caches   *lru // In memory caches to avoid regenerating too often
	// 빈번하게 생성되는것을 피하기 위한 In memory cache
	datasets *lru // In memory datasets to avoid regenerating too often
	// 빈번하게 생성되는것을 피하기 위한 In memory dataset

	// Mining related fields
	// 마이닝 관련 필드
	rand     *rand.Rand    // Properly seeded random source for nonces
	// 논스를 위한 적절히 시드된 랜덤소스
	threads  int           // Number of threads to mine on if mining
	// 마이닝시 사용될 스레드의 갯수
	update   chan struct{} // Notification channel to update mining parameters
	// 마이닝 파라미터 변경을 위한 노티채널
	hashrate metrics.Meter // Meter tracking the average hashrate

	// The fields below are hooks for testing
	// 테스트에 관련된 훅들
	shared    *Ethash       // Shared PoW verifier to avoid cache regeneration
	fakeFail  uint64        // Block number which fails PoW check even in fake mode
	fakeDelay time.Duration // Time delay to sleep for before returning from verify

	lock sync.Mutex // Ensures thread safety for the in-memory caches and mining fields
}

// New creates a full sized ethash PoW scheme.
// 전체 크기의 ethash pow 계획을 생성한다
func New(config Config) *Ethash {
	if config.CachesInMem <= 0 {
		log.Warn("One ethash cache must always be in memory", "requested", config.CachesInMem)
		config.CachesInMem = 1
	}
	if config.CacheDir != "" && config.CachesOnDisk > 0 {
		log.Info("Disk storage enabled for ethash caches", "dir", config.CacheDir, "count", config.CachesOnDisk)
	}
	if config.DatasetDir != "" && config.DatasetsOnDisk > 0 {
		log.Info("Disk storage enabled for ethash DAGs", "dir", config.DatasetDir, "count", config.DatasetsOnDisk)
	}
	return &Ethash{
		config:   config,
		caches:   newlru("cache", config.CachesInMem, newCache),
		datasets: newlru("dataset", config.DatasetsInMem, newDataset),
		update:   make(chan struct{}),
		hashrate: metrics.NewMeter(),
	}
}

// NewTester creates a small sized ethash PoW scheme useful only for testing
// purposes.
// NewTester 함수는 오직 테스트의 목적으로 작은 사이즈의 ethash pow 스킴을 생성한다 
func NewTester() *Ethash {
	return New(Config{CachesInMem: 1, PowMode: ModeTest})
}

// NewFaker creates a ethash consensus engine with a fake PoW scheme that accepts
// all blocks' seal as valid, though they still have to conform to the Ethereum
// consensus rules.
// NewFaker 함수는 아직 이더리움 합의 룰에의해 허가가 필요한 블록을 포함한
// 모든 블록의 seal이 유효한것 처럼 가짜의 pow 스킴으로 ehtash 합의 엔진을 생성한다

func NewFaker() *Ethash {
	return &Ethash{
		config: Config{
			PowMode: ModeFake,
		},
	}
}

// NewFakeFailer creates a ethash consensus engine with a fake PoW scheme that
// accepts all blocks as valid apart from the single one specified, though they
// still have to conform to the Ethereum consensus rules.
// NewFakerFailer 함수는 아직 이더리움 합의 룰에의해 지정된 하나를 제외한 
// 모든 블록이 유효한것처럼 가짜의 pow 스킴으로 ehtash 합의 엔진을 생성한다
func NewFakeFailer(fail uint64) *Ethash {
	return &Ethash{
		config: Config{
			PowMode: ModeFake,
		},
		fakeFail: fail,
	}
}

// NewFakeDelayer creates a ethash consensus engine with a fake PoW scheme that
// accepts all blocks as valid, but delays verifications by some time, though
// they still have to conform to the Ethereum consensus rules.
// NewFakeDelayer 함수는 아직 이더리움 합의 룰에의해 허가가 필요한 블록을 포함한
// 모든 블록의 seal이 유효하지만 약간의 시간으로 
// 딜레이를 검증하도록  가짜의 pow 스킴으로 ehtash 합의 엔진을 생성한다
func NewFakeDelayer(delay time.Duration) *Ethash {
	return &Ethash{
		config: Config{
			PowMode: ModeFake,
		},
		fakeDelay: delay,
	}
}

// NewFullFaker creates an ethash consensus engine with a full fake scheme that
// accepts all blocks as valid, without checking any consensus rules whatsoever.
// NewFullFaker 함수는 모든 블록이 컨센서스 합의 없이 valid하도록 가짜 스킴으로
// ehtash 합의 엔진을 생성한다
func NewFullFaker() *Ethash {
	return &Ethash{
		config: Config{
			PowMode: ModeFullFake,
		},
	}
}

// NewShared creates a full sized ethash PoW shared between all requesters running
// in the same process.
// NewShared 함수는 동일한 프로세스에서 동작하는
// 모든 요청자들 사이에 공유될전체사이즈의 ethash pow를 생성한다  
func NewShared() *Ethash {
	return &Ethash{shared: sharedEthash}
}

// cache tries to retrieve a verification cache for the specified block number
// by first checking against a list of in-memory caches, then against caches
// stored on disk, and finally generating one if none can be found.
// cache함수는 지정된 블록넘버를 위한 검증캐시를 in memory 캐시를 검색하고
// disk에 저장된 캐시를 검색하고, 찾지못했다면 최종적으로 생성한다
func (ethash *Ethash) cache(block uint64) *cache {
	epoch := block / epochLength
	currentI, futureI := ethash.caches.get(epoch)
	current := currentI.(*cache)

	// Wait for generation finish.
	current.generate(ethash.config.CacheDir, ethash.config.CachesOnDisk, ethash.config.PowMode == ModeTest)

	// If we need a new future cache, now's a good time to regenerate it.
	if futureI != nil {
		future := futureI.(*cache)
		go future.generate(ethash.config.CacheDir, ethash.config.CachesOnDisk, ethash.config.PowMode == ModeTest)
	}
	return current
}

// dataset tries to retrieve a mining dataset for the specified block number
// by first checking against a list of in-memory datasets, then against DAGs
// stored on disk, and finally generating one if none can be found.
// dataset함수는 지정된 블록넘버를 위한 마이닝 데이터셋을 in memory 캐시를 검색하고
// disk에 저장된 캐시를 검색하고, 찾지못했다면 최종적으로 생성한다
func (ethash *Ethash) dataset(block uint64) *dataset {
	epoch := block / epochLength
	currentI, futureI := ethash.datasets.get(epoch)
	current := currentI.(*dataset)

	// Wait for generation finish.
	current.generate(ethash.config.DatasetDir, ethash.config.DatasetsOnDisk, ethash.config.PowMode == ModeTest)

	// If we need a new future dataset, now's a good time to regenerate it.
	if futureI != nil {
		future := futureI.(*dataset)
		go future.generate(ethash.config.DatasetDir, ethash.config.DatasetsOnDisk, ethash.config.PowMode == ModeTest)
	}

	return current
}

// Threads returns the number of mining threads currently enabled. This doesn't
// necessarily mean that mining is running!
// Threads 함수는 사용가능한 마이닝 스레드의 수를 반환한다
// 반드시 실행중일 필요는 없다
func (ethash *Ethash) Threads() int {
	ethash.lock.Lock()
	defer ethash.lock.Unlock()

	return ethash.threads
}

// SetThreads updates the number of mining threads currently enabled. Calling
// this method does not start mining, only sets the thread count. If zero is
// specified, the miner will use all cores of the machine. Setting a thread
// count below zero is allowed and will cause the miner to idle, without any
// work being done.
// SetThreads 함수는 현재 활성화된 마이닝 스레드의 넘버를 업데이트 한다.
// 이 함수는 마이닝을 시작하지 않고, 스레드 카운트만 변경시킨다.
// 0이할당되면 마이너는 머신의 모든 코어를 사용한다.
// 영보다 작은 값이 할당되면 마이너들은 아무일도 하지 않고 쉬게된다
func (ethash *Ethash) SetThreads(threads int) {
	ethash.lock.Lock()
	defer ethash.lock.Unlock()

	// If we're running a shared PoW, set the thread count on that instead
	if ethash.shared != nil {
		ethash.shared.SetThreads(threads)
		return
	}
	// Update the threads and ping any running seal to pull in any changes
	ethash.threads = threads
	select {
	case ethash.update <- struct{}{}:
	default:
	}
}

// Hashrate implements PoW, returning the measured rate of the search invocations
// per second over the last minute.
// Hashrate 함수는 pow를 구현하며,, 최근 1분동안 논스 서치가 측정된 비율울 반환한다
func (ethash *Ethash) Hashrate() float64 {
	return ethash.hashrate.Rate1()
}

// APIs implements consensus.Engine, returning the user facing RPC APIs. Currently
// that is empty.
// APIs 함수는 합의엔진을 구현하며, 유저가 마주칠 RPC API들을 반환한다
func (ethash *Ethash) APIs(chain consensus.ChainReader) []rpc.API {
	return nil
}

// SeedHash is the seed to use for generating a verification cache and the mining
// dataset.
// SeedHash는 검증캐시와 마이닝데이터셋을 생성하기위해 사용되는 seed이다
func SeedHash(block uint64) []byte {
	return seedHash(block)
}
