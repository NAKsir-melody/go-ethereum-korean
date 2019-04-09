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

// 이 패키지는 test helper 패키지이다
// 다양한 테스트 패키지에서 블록을 생성할때 사용함
// @sigmoid: mining을 하지 않아도 블록을 생성할 수 있음


package core

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
)

// So we can deterministically seed different blockchains
var (
	canonicalSeed = 1
	forkSeed      = 2
)

// BlockGen creates blocks for testing.
// See GenerateChain for a detailed explanation.
// 블록젠은 테스트를 위한 블록들을 생성한다
// 자세한 설명은 GenerateChain을 보라
type BlockGen struct {
	i           int
	parent      *types.Block
	chain       []*types.Block
	chainReader consensus.ChainReader
	header      *types.Header
	statedb     *state.StateDB

	gasPool  *GasPool
	txs      []*types.Transaction
	receipts []*types.Receipt
	uncles   []*types.Header

	config *params.ChainConfig
	engine consensus.Engine
}

// SetCoinbase sets the coinbase of the generated block.
// It can be called at most once.
// SetCoinBase함수는 생성된 블록의 코인베이스를 설정한다
// 한번만 불릴수 있다
func (b *BlockGen) SetCoinbase(addr common.Address) {
	if b.gasPool != nil {
		if len(b.txs) > 0 {
			panic("coinbase must be set before adding transactions")
		}
		panic("coinbase can only be set once")
	}
	b.header.Coinbase = addr
	b.gasPool = new(GasPool).AddGas(b.header.GasLimit)
}

// SetExtra sets the extra data field of the generated block.
// SetExtra 함수는 생성된 블록의 extra data 필드를 설정한다
func (b *BlockGen) SetExtra(data []byte) {
	b.header.Extra = data
}

// AddTx adds a transaction to the generated block. If no coinbase has
// been set, the block's coinbase is set to the zero address.
//
// AddTx panics if the transaction cannot be executed. In addition to
// the protocol-imposed limitations (gas limit, etc.), there are some
// further limitations on the content of transactions that can be
// added. Notably, contract code relying on the BLOCKHASH instruction
// will panic during execution.
// AddTx는 트렌젝션을 생성된 블록에 더한다. 만약 코인베이스가 셋팅되지 않았다면
// 블록의 코인베이스는 제로주소로 설정된다

// 이 함수는 트렌젝션이 실행되지 못할 경우 패닉을 리턴한다.
// 프로토콜적인 제약(가스 한도)에 추가적으로 트렌젝션 내용에 대한 
// 추가적인 제약이 좀더 있을수 있다
// 특히, 블록해시 명령어가 포함된 계약코드의 경우 실행중 패닉이 난다
func (b *BlockGen) AddTx(tx *types.Transaction) {
	b.AddTxWithChain(nil, tx)
}

// AddTxWithChain adds a transaction to the generated block. If no coinbase has
// been set, the block's coinbase is set to the zero address.
//
// AddTxWithChain panics if the transaction cannot be executed. In addition to
// the protocol-imposed limitations (gas limit, etc.), there are some
// further limitations on the content of transactions that can be
// added. If contract code relies on the BLOCKHASH instruction,
// AddTxWithChain는 트렌젝션을 생성된 블록에 더한다. 만약 코인베이스가 셋팅되지 않았다면
// 블록의 코인베이스는 제로주소로 설정된다
// 이 함수는 트렌젝션이 실행되지 못할 경우 패닉을 리턴한다.
// 프로토콜적인 제약(가스 한도)에 추가적으로 트렌젝션 내용에 대한 
// 추가적인 제약이 좀더 있을수 있다
// 특히, 블록해시 명령어가 포함된 계약코드의 경우 실행중 패닉이 난다
func (b *BlockGen) AddTxWithChain(bc *BlockChain, tx *types.Transaction) {
	if b.gasPool == nil {
		b.SetCoinbase(common.Address{})
	}
	b.statedb.Prepare(tx.Hash(), common.Hash{}, len(b.txs))
	receipt, _, err := ApplyTransaction(b.config, bc, &b.header.Coinbase, b.gasPool, b.statedb, b.header, tx, &b.header.GasUsed, vm.Config{})
	if err != nil {
		panic(err)
	}
	b.txs = append(b.txs, tx)
	b.receipts = append(b.receipts, receipt)
}

// Number returns the block number of the block being generated.
// Number함수는 생성될 블록의 번호를 반환한다
func (b *BlockGen) Number() *big.Int {
	return new(big.Int).Set(b.header.Number)
}

// AddUncheckedReceipt forcefully adds a receipts to the block without a
// backing transaction.
//
// AddUncheckedReceipt will cause consensus failures when used during real
// chain processing. This is best used in conjunction with raw block insertion.
//AddUncheckedReceipt 함수는 강제적으로 트렌젝션의 지원없이 영수증을 블록에 더한다
// 이함수는 실제 체인을 처리하는 동안 사용될경우 합의 실패를 유발한다
// 이함수는 raw블록 삽입에 결합하여 사용하면 좋다
func (b *BlockGen) AddUncheckedReceipt(receipt *types.Receipt) {
	b.receipts = append(b.receipts, receipt)
}

// TxNonce returns the next valid transaction nonce for the
// account at addr. It panics if the account does not exist.
// TxNonce함수는 주소상 계정이 다음에 사용할 유효한 트렌젝션 논스값을 반환한다
// 계정이 존재하지 않을 경우 패닉이 발생한다
func (b *BlockGen) TxNonce(addr common.Address) uint64 {
	if !b.statedb.Exist(addr) {
		panic("account does not exist")
	}
	return b.statedb.GetNonce(addr)
}

// AddUncle adds an uncle header to the generated block.
// AddUncle함수는 엉클헤더를 생성된 블록에 추가한다
func (b *BlockGen) AddUncle(h *types.Header) {
	b.uncles = append(b.uncles, h)
}

// PrevBlock returns a previously generated block by number. It panics if
// num is greater or equal to the number of the block being generated.
// For index -1, PrevBlock returns the parent block given to GenerateChain.
// PrevBlock함수는 숫자를 이용해 이전에 생성된 블록을 반환한다
// 만약 숫자가 생성될 블록보다 크거나 같다면 패닉이 발생한다
// -1 인덱스의 경우, 이함수는 주어진 체인의 부모 블록을 리턴한다
func (b *BlockGen) PrevBlock(index int) *types.Block {
	if index >= b.i {
		panic("block index out of range")
	}
	if index == -1 {
		return b.parent
	}
	return b.chain[index]
}

// OffsetTime modifies the time instance of a block, implicitly changing its
// associated difficulty. It's useful to test scenarios where forking is not
// tied to chain length directly.
func (b *BlockGen) OffsetTime(seconds int64) {
	b.header.Time.Add(b.header.Time, new(big.Int).SetInt64(seconds))
	if b.header.Time.Cmp(b.parent.Header().Time) <= 0 {
		panic("block time out of range")
	}
	b.header.Difficulty = b.engine.CalcDifficulty(b.chainReader, b.header.Time.Uint64(), b.parent.Header())
}

// GenerateChain creates a chain of n blocks. The first block's
// parent will be the provided parent. db is used to store
// intermediate states and should contain the parent's state trie.
//
// The generator function is called with a new block generator for
// every block. Any transactions and uncles added to the generator
// become part of the block. If gen is nil, the blocks will be empty
// and their coinbase will be the zero address.
//
// Blocks created by GenerateChain do not contain valid proof of work
// values. Inserting them into BlockChain requires use of FakePow or
// a similar non-validating proof of work implementation.
// GenerateChain함수는 n개의 블록의 체인을 만든다. 첫블록의 부모블록은 제공된다.
// 데이터 베이스는 중간 스테이트를 저장하며 부보의 상태 트라이를 포함해야 한다
// 이 생성함수는 매 블록마다 새로운 블록 생성자에 의해 불려진다.
// 생성자에 더해진 모든 트렌젝션과 엉클은 블록의 일부가 된다.
// 이 함수에 의해 생성된 블록은 아직 유효한 검증 값을 포함하고 있지 않다.
// 이 블록들을 체인에 넣기 위해서는 fakepow나 유사한 비검증 구현을 이용해야한다
func GenerateChain(config *params.ChainConfig, parent *types.Block, engine consensus.Engine, db ethdb.Database, n int, gen func(int, *BlockGen)) ([]*types.Block, []types.Receipts) {
	if config == nil {
		config = params.TestChainConfig
	}
	blocks, receipts := make(types.Blocks, n), make([]types.Receipts, n)
	genblock := func(i int, parent *types.Block, statedb *state.StateDB) (*types.Block, types.Receipts) {
		// TODO(karalabe): This is needed for clique, which depends on multiple blocks.
		// It's nonetheless ugly to spin up a blockchain here. Get rid of this somehow.
		blockchain, _ := NewBlockChain(db, nil, config, engine, vm.Config{})
		defer blockchain.Stop()

		b := &BlockGen{i: i, parent: parent, chain: blocks, chainReader: blockchain, statedb: statedb, config: config, engine: engine}
		b.header = makeHeader(b.chainReader, parent, statedb, b.engine)

		// Mutate the state and block according to any hard-fork specs
		if daoBlock := config.DAOForkBlock; daoBlock != nil {
			limit := new(big.Int).Add(daoBlock, params.DAOForkExtraRange)
			if b.header.Number.Cmp(daoBlock) >= 0 && b.header.Number.Cmp(limit) < 0 {
				if config.DAOForkSupport {
					b.header.Extra = common.CopyBytes(params.DAOForkBlockExtra)
				}
			}
		}
		if config.DAOForkSupport && config.DAOForkBlock != nil && config.DAOForkBlock.Cmp(b.header.Number) == 0 {
			misc.ApplyDAOHardFork(statedb)
		}
		// Execute any user modifications to the block and finalize it
		if gen != nil {
			gen(i, b)
		}

		if b.engine != nil {
			block, _ := b.engine.Finalize(b.chainReader, b.header, statedb, b.txs, b.uncles, b.receipts)
			// Write state changes to db
			root, err := statedb.Commit(config.IsEIP158(b.header.Number))
			if err != nil {
				panic(fmt.Sprintf("state write error: %v", err))
			}
			if err := statedb.Database().TrieDB().Commit(root, false); err != nil {
				panic(fmt.Sprintf("trie write error: %v", err))
			}
			return block, b.receipts
		}
		return nil, nil
	}
	for i := 0; i < n; i++ {
		statedb, err := state.New(parent.Root(), state.NewDatabase(db))
		if err != nil {
			panic(err)
		}
		block, receipt := genblock(i, parent, statedb)
		blocks[i] = block
		receipts[i] = receipt
		parent = block
	}
	return blocks, receipts
}

func makeHeader(chain consensus.ChainReader, parent *types.Block, state *state.StateDB, engine consensus.Engine) *types.Header {
	var time *big.Int
	if parent.Time() == nil {
		time = big.NewInt(10)
	} else {
		time = new(big.Int).Add(parent.Time(), big.NewInt(10)) // block time is fixed at 10 seconds
	}

	return &types.Header{
		Root:       state.IntermediateRoot(chain.Config().IsEIP158(parent.Number())),
		ParentHash: parent.Hash(),
		Coinbase:   parent.Coinbase(),
		Difficulty: engine.CalcDifficulty(chain, time.Uint64(), &types.Header{
			Number:     parent.Number(),
			Time:       new(big.Int).Sub(time, big.NewInt(10)),
			Difficulty: parent.Difficulty(),
			UncleHash:  parent.UncleHash(),
		}),
		GasLimit: CalcGasLimit(parent),
		Number:   new(big.Int).Add(parent.Number(), common.Big1),
		Time:     time,
	}
}

// newCanonical creates a chain database, and injects a deterministic canonical
// chain. Depending on the full flag, if creates either a full block chain or a
// header only chain.
// 이함수는 체인 데이터 베이스를 생성하고 캐노니컬 블럭을 삽입한다.
// full 인자에 따라 풀 블록체인이나 헤더온리 체인을 만든다 
func newCanonical(engine consensus.Engine, n int, full bool) (ethdb.Database, *BlockChain, error) {
	var (
		db      = ethdb.NewMemDatabase()
		genesis = new(Genesis).MustCommit(db)
	)

	// Initialize a fresh chain with only a genesis block
	blockchain, _ := NewBlockChain(db, nil, params.AllEthashProtocolChanges, engine, vm.Config{})
	// Create and inject the requested chain
	if n == 0 {
		return db, blockchain, nil
	}
	if full {
		// Full block-chain requested
		blocks := makeBlockChain(genesis, n, engine, db, canonicalSeed)
		_, err := blockchain.InsertChain(blocks)
		return db, blockchain, err
	}
	// Header-only chain requested
	headers := makeHeaderChain(genesis.Header(), n, engine, db, canonicalSeed)
	_, err := blockchain.InsertHeaderChain(headers, 1)
	return db, blockchain, err
}

// makeHeaderChain creates a deterministic chain of headers rooted at parent.
func makeHeaderChain(parent *types.Header, n int, engine consensus.Engine, db ethdb.Database, seed int) []*types.Header {
	blocks := makeBlockChain(types.NewBlockWithHeader(parent), n, engine, db, seed)
	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	return headers
}

// makeBlockChain creates a deterministic chain of blocks rooted at parent.
func makeBlockChain(parent *types.Block, n int, engine consensus.Engine, db ethdb.Database, seed int) []*types.Block {
	blocks, _ := GenerateChain(params.TestChainConfig, parent, engine, db, n, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{0: byte(seed), 19: byte(i)})
	})
	return blocks
}
