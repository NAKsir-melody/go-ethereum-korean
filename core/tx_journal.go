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
	"errors"
	"io"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

// errNoActiveJournal is returned if a transaction is attempted to be inserted
// into the journal, but no such file is currently open.
// errNoActivejournal은 트렌젝션이 저널에 쓰여지려 하나 
// 그런 파일이 열려있지 않을때 반환된다
var errNoActiveJournal = errors.New("no active journal")

// devNull is a WriteCloser that just discards anything written into it. Its
// goal is to allow the transaction journal to write into a fake journal when
// loading transactions on startup without printing warnings due to no file
// being readt for write.
// devNull은 쓰기 종료자로서 이곳에 쓰이는 모든 것은 무시된다.
// 이것의 목적은 읽혀진 트렌젝션이 시작될때 쓰기위한 아무 파일이 준비되지 않음으로 인해
// 경고 출력없이 없을때 트렌젝션 저널에게 가짜 저널에 쓰는 것을 허용하기 위해서 이다
type devNull struct{}

func (*devNull) Write(p []byte) (n int, err error) { return len(p), nil }
func (*devNull) Close() error                      { return nil }

// txJournal is a rotating log of transactions with the aim of storing locally
// created transactions to allow non-executed ones to survive node restarts.
// txJournal 구조체는 로컬에 저장하는 것을 노려 생성된 트렌젝션들중 실행되지 않은 것들이
// 노드의 재시작에도 살아남는 것을 허용하기 위한 순환로그
// @sigmoid: 로컬에서 발생한 트렌젝션이 아직 실행되지 않은 상태에서 노드를 재시작할때
// 정보가 손실되는것을 막기 위한 것.
type txJournal struct {
	path   string         // Filesystem path to store the transactions at
	writer io.WriteCloser // Output stream to write new transactions into
	// 트렌젝션을 저장할 파일시스템
	// 새로운 트렌젝션을 쓸 출력스트림
}

// newTxJournal creates a new transaction journal to
// newTxJournal함수는 새로운 트렌젝션 저널을 경로에 만든다
func newTxJournal(path string) *txJournal {
	return &txJournal{
		path: path,
	}
}

// load parses a transaction journal dump from disk, loading its contents into
// the specified pool.
// load 함수는 디스크로 부터 트렌젝션 저널의 덤프를 분석하여 내용을 지정된 풀에 넣는다
func (journal *txJournal) load(add func([]*types.Transaction) []error) error {
	// Skip the parsing if the journal file doens't exist at all
	// 저널파일이 더이상 존재하지 않을경우 파싱을 중지한다
	if _, err := os.Stat(journal.path); os.IsNotExist(err) {
		return nil
	}
	// Open the journal for loading any past transactions
	// 과거 트렌젝션을 읽기위한 저널을 연다
	input, err := os.Open(journal.path)
	if err != nil {
		return err
	}
	defer input.Close()

	// Temporarily discard any journal additions (don't double add on load)
	// 임시적으로 저널 추가를 막는다
	journal.writer = new(devNull)
	defer func() { journal.writer = nil }()

	// Inject all transactions from the journal into the pool
	// 저널의 모든 트렌젝션을 풀로 삽입한다
	stream := rlp.NewStream(input, 0)
	total, dropped := 0, 0

	// Create a method to load a limited batch of transactions and bump the
	// appropriate progress counters. Then use this method to load all the
	// journalled transactions in small-ish batches.
	// 정해진 량의 트렌젝션 배치를 읽어들일 방법을 정의하고 
	// 적절한 처리 카운터를 bump한다
	// 이메소드를 이용하여 작은 배치에서 모든 저널된 트렌젝션을 읽어들인다
	loadBatch := func(txs types.Transactions) {
		for _, err := range add(txs) {
			if err != nil {
				log.Debug("Failed to add journaled transaction", "err", err)
				dropped++
			}
		}
	}
	var (
		failure error
		batch   types.Transactions
	)
	for {
		// Parse the next transaction and terminate on error
		// 다음 트렌젝션을 파싱하고, 에러시 끝낸다
		tx := new(types.Transaction)
		if err = stream.Decode(tx); err != nil {
			if err != io.EOF {
				failure = err
			}
			if batch.Len() > 0 {
				loadBatch(batch)
			}
			break
		}
		// New transaction parsed, queue up for later, import if threnshold is reached
		// 새로운 트렌젝션이 파싱되면 큐잉하고 한계에 도달하면 입수한다
		total++

		if batch = append(batch, tx); batch.Len() > 1024 {
			loadBatch(batch)
			batch = batch[:0]
		}
	}
	log.Info("Loaded local transaction journal", "transactions", total, "dropped", dropped)

	return failure
}

// insert adds the specified transaction to the local disk journal.
// insert 함수는 지정된 트렌젝션을 로컬 디스크 저널에 추가한다
func (journal *txJournal) insert(tx *types.Transaction) error {
	if journal.writer == nil {
		return errNoActiveJournal
	}
	if err := rlp.Encode(journal.writer, tx); err != nil {
		return err
	}
	return nil
}

// rotate regenerates the transaction journal based on the current contents of
// the transaction pool.
// rotate 함수는 트렌젝션 풀의 현재 내용을 기반으로 트렌젝션 저널을 만든다
func (journal *txJournal) rotate(all map[common.Address]types.Transactions) error {
	// Close the current journal (if any is open)
	// 현재 저널을 닫는다.
	if journal.writer != nil {
		if err := journal.writer.Close(); err != nil {
			return err
		}
		journal.writer = nil
	}
	// Generate a new journal with the contents of the current pool
	// 새로운 저널을 풀의 컨텐츠와 함께 생성한다
	replacement, err := os.OpenFile(journal.path+".new", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	journaled := 0
	for _, txs := range all {
		for _, tx := range txs {
			if err = rlp.Encode(replacement, tx); err != nil {
				replacement.Close()
				return err
			}
		}
		journaled += len(txs)
	}
	replacement.Close()

	// Replace the live journal with the newly generated one
	// 새롭게 생성된 저널로 교체한다
	if err = os.Rename(journal.path+".new", journal.path); err != nil {
		return err
	}
	sink, err := os.OpenFile(journal.path, os.O_WRONLY|os.O_APPEND, 0755)
	if err != nil {
		return err
	}
	journal.writer = sink
	log.Info("Regenerated local transaction journal", "transactions", journaled, "accounts", len(all))

	return nil
}

// close flushes the transaction journal contents to disk and closes the file.
// close 함수는 트렌젝션 저널의 내용을 디스크에 쓰고 파일을 닫는다
func (journal *txJournal) close() error {
	var err error

	if journal.writer != nil {
		err = journal.writer.Close()
		journal.writer = nil
	}
	return err
}
