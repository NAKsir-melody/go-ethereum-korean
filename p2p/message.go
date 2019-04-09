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

package p2p

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/rlp"
)

// Msg defines the structure of a p2p message.
//
// Note that a Msg can only be sent once since the Payload reader is
// consumed during sending. It is not possible to create a Msg and
// send it any number of times. If you want to reuse an encoded
// structure, encode the payload into a byte array and create a
// separate Msg with a bytes.Reader as Payload for each send.
// Msg는 p2p 메시지의 구조를 정의한다
// Msg는 페이로드 리더가 전송중에 소모될때 한번만 보내질수 있다
// 메시지를 생성하는 것과 여러번 보내는것은 불가능하다
// 만약 인코딩된 구조를 재사용하기 원한다면, 페이로드를 바이트 어레이에 인코딩하고
// 바이트 어레이를 분리된 새로운 메시지로 생성하라
type Msg struct {
	Code       uint64
	Size       uint32 // size of the paylod
	Payload    io.Reader
	ReceivedAt time.Time
}

// Decode parses the RLP content of a message into
// the given value, which must be a pointer.
//
// For the decoding rules, please see package rlp.
// Decode 함수는 메새지의 RLP 컨텐츠를 값을 나타내는 포인터로 파싱한다
// 디코딩 룰은 rlp패키지 참조
func (msg Msg) Decode(val interface{}) error {
	s := rlp.NewStream(msg.Payload, uint64(msg.Size))
	if err := s.Decode(val); err != nil {
		return newPeerError(errInvalidMsg, "(code %x) (size %d) %v", msg.Code, msg.Size, err)
	}
	return nil
}

func (msg Msg) String() string {
	return fmt.Sprintf("msg #%v (%v bytes)", msg.Code, msg.Size)
}

// Discard reads any remaining payload data into a black hole.
// Discard 함수는 남은 페이로드 데이터를 블랙홀로 넣는다
func (msg Msg) Discard() error {
	_, err := io.Copy(ioutil.Discard, msg.Payload)
	return err
}

type MsgReader interface {
	ReadMsg() (Msg, error)
}

type MsgWriter interface {
	// WriteMsg sends a message. It will block until the message's
	// Payload has been consumed by the other end.
	//
	// Note that messages can be sent only once because their
	// payload reader is drained.
	// WriteMsg 인터페이스는 메시지를 전송한다. 메세지의 페이로드가 끝까지 소비될때까지 블록된다
	// 페이로드 리더가 소비하기 때문에 메시지는 한번반 보내질수 있다
	WriteMsg(Msg) error
}

// MsgReadWriter provides reading and writing of encoded messages.
// Implementations should ensure that ReadMsg and WriteMsg can be
// called simultaneously from multiple goroutines.
// MsgReadWriter 인터페이스는 암호화된 메시지의 읽기/쓰기를 제공한다
// 구현은 ReadMsg/WriteMsg가 여러 고루틴에서 동시에 
// 수행될수 있음을 보장해야 한다
type MsgReadWriter interface {
	MsgReader
	MsgWriter
}

// Send writes an RLP-encoded message with the given code.
// data should encode as an RLP list.
// 트렌젝션들을 RLP로 인코딩하고, 인코딩된 메시지를 보낸다
func Send(w MsgWriter, msgcode uint64, data interface{}) error {
	size, r, err := rlp.EncodeToReader(data)
	if err != nil {
		return err
	}
	return w.WriteMsg(Msg{Code: msgcode, Size: uint32(size), Payload: r})
}

// SendItems writes an RLP with the given code and data elements.
// For a call such as:
//
//    SendItems(w, code, e1, e2, e3)
//
// the message payload will be an RLP list containing the items:
//
//    [e1, e2, e3]
//
// SendItems함수는 주어진 코드와 데이터를 RLP로 쓴다
// e1,e2,e3 순서로 인코딩된다
func SendItems(w MsgWriter, msgcode uint64, elems ...interface{}) error {
	return Send(w, msgcode, elems)
}

// eofSignal wraps a reader with eof signaling. the eof channel is
// closed when the wrapped reader returns an error or when count bytes
// have been read.
// eofSignal는 eof 시그널과 함께 리더를 나타낸다.
// eof채널은 리더가 error를 리턴하거나 count byte가 읽혀졌을때 닫힌다
type eofSignal struct {
	wrapped io.Reader
	count   uint32 // number of bytes left
	eof     chan<- struct{}
}

// note: when using eofSignal to detect whether a message payload
// has been read, Read might not be called for zero sized messages.
// 메시지 페이로드가 읽혔는지 감지하기 위해서 eof signal을 사용할때는 
// 0크기의 메시지에 대한 read는 호출되어서는 안된다
func (r *eofSignal) Read(buf []byte) (int, error) {
	if r.count == 0 {
		if r.eof != nil {
			r.eof <- struct{}{}
			r.eof = nil
		}
		return 0, io.EOF
	}

	max := len(buf)
	if int(r.count) < len(buf) {
		max = int(r.count)
	}
	n, err := r.wrapped.Read(buf[:max])
	r.count -= uint32(n)
	if (err != nil || r.count == 0) && r.eof != nil {
		r.eof <- struct{}{} // tell Peer that msg has been consumed
		// 피어에 msg가 소비되었음을 알린다
		r.eof = nil
	}
	return n, err
}

// MsgPipe creates a message pipe. Reads on one end are matched
// with writes on the other. The pipe is full-duplex, both ends
// implement MsgReadWriter.
// MsgPipe함수는 메시지 파이프를 생성한다. 읽은 량과 쓰는 량이 같아야한다
// pipe는 양방향이고 양쪽끝은 MsgReadWriter로 구현된다
func MsgPipe() (*MsgPipeRW, *MsgPipeRW) {
	var (
		c1, c2  = make(chan Msg), make(chan Msg)
		closing = make(chan struct{})
		closed  = new(int32)
		rw1     = &MsgPipeRW{c1, c2, closing, closed}
		rw2     = &MsgPipeRW{c2, c1, closing, closed}
	)
	return rw1, rw2
}

// ErrPipeClosed is returned from pipe operations after the
// pipe has been closed.
// ErrPipeClosed는 파이프가 끝난 시점의 파이프 동작으로 부터 반환된다
var ErrPipeClosed = errors.New("p2p: read or write on closed message pipe")

// MsgPipeRW is an endpoint of a MsgReadWriter pipe.
// MsgPipeRW는 MsgReadWriter파이프의 엔드포인트이다
type MsgPipeRW struct {
	w       chan<- Msg
	r       <-chan Msg
	closing chan struct{}
	closed  *int32
}

// WriteMsg sends a messsage on the pipe.
// It blocks until the receiver has consumed the message payload.
// WriteMsg 함수는 메시지를 파이프로 전달한다
// 이 함수는 수신자가 메시지 페이로드를 모두 소모할때까지 블록킹된다
func (p *MsgPipeRW) WriteMsg(msg Msg) error {
	if atomic.LoadInt32(p.closed) == 0 {
		consumed := make(chan struct{}, 1)
		msg.Payload = &eofSignal{msg.Payload, msg.Size, consumed}
		select {
		case p.w <- msg:
			if msg.Size > 0 {
				// wait for payload read or discard
				// payload 읽기 및 버림 처리를 대기
				select {
				case <-consumed:
				case <-p.closing:
				}
			}
			return nil
		case <-p.closing:
		}
	}
	return ErrPipeClosed
}

// ReadMsg returns a message sent on the other end of the pipe.
// ReadMsg 함수는 파이프의 반대쪽 end에서 보내온 메시지를 반환한다
func (p *MsgPipeRW) ReadMsg() (Msg, error) {
	if atomic.LoadInt32(p.closed) == 0 {
		select {
		case msg := <-p.r:
			return msg, nil
		case <-p.closing:
		}
	}
	return Msg{}, ErrPipeClosed
}

// Close unblocks any pending ReadMsg and WriteMsg calls on both ends
// of the pipe. They will return ErrPipeClosed. Close also
// interrupts any reads from a message payload.
// Close함수는 양쪽 End에 대기중인 read/write msg함수를 unblock한다
// 그들은 모두 ErrPipeClosed를 반환할것이다. close함수는 message payload의 read도 제한한다
func (p *MsgPipeRW) Close() error {
	if atomic.AddInt32(p.closed, 1) != 1 {
		// someone else is already closing
		atomic.StoreInt32(p.closed, 1) // avoid overflow
		// 이미 닫혔다면 overflow방지
		return nil
	}
	close(p.closing)
	return nil
}

// ExpectMsg reads a message from r and verifies that its
// code and encoded RLP content match the provided values.
// If content is nil, the payload is discarded and not verified.
// ExpectMsg 함수는 메시지 리더로 부터 메시지를 읽고, 코드와 인코딩된 rlp내용이 
// 제공된 값과 매칭되는지 검증한다
// 내용이 nil이라면 페이로드는 무시되고 검증하지 않는다
func ExpectMsg(r MsgReader, code uint64, content interface{}) error {
	msg, err := r.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Code != code {
		return fmt.Errorf("message code mismatch: got %d, expected %d", msg.Code, code)
	}
	if content == nil {
		return msg.Discard()
	}
	contentEnc, err := rlp.EncodeToBytes(content)
	if err != nil {
		panic("content encode error: " + err.Error())
	}
	if int(msg.Size) != len(contentEnc) {
		return fmt.Errorf("message size mismatch: got %d, want %d", msg.Size, len(contentEnc))
	}
	actualContent, err := ioutil.ReadAll(msg.Payload)
	if err != nil {
		return err
	}
	if !bytes.Equal(actualContent, contentEnc) {
		return fmt.Errorf("message payload mismatch:\ngot:  %x\nwant: %x", actualContent, contentEnc)
	}
	return nil
}

// msgEventer wraps a MsgReadWriter and sends events whenever a message is sent
// or received
// msgEventer는 MsgReadWriter를 포함하며 메시지 수신/발신에 관계업이 이벤트를 전송한다
type msgEventer struct {
	MsgReadWriter

	feed     *event.Feed
	peerID   discover.NodeID
	Protocol string
}

// newMsgEventer returns a msgEventer which sends message events to the given
// feed
// newMsgEventer는 주어진 feed로 메시지를 보내는 msgEventer를 반환한다
func newMsgEventer(rw MsgReadWriter, feed *event.Feed, peerID discover.NodeID, proto string) *msgEventer {
	return &msgEventer{
		MsgReadWriter: rw,
		feed:          feed,
		peerID:        peerID,
		Protocol:      proto,
	}
}

// ReadMsg reads a message from the underlying MsgReadWriter and emits a
// "message received" event
// ReadMsgs 함수는 msgReadWriter로 부터 메시지를 읽고 
// Mssage recevied 이벤트를 내보낸다
func (ev *msgEventer) ReadMsg() (Msg, error) {
	msg, err := ev.MsgReadWriter.ReadMsg()
	if err != nil {
		return msg, err
	}
	ev.feed.Send(&PeerEvent{
		Type:     PeerEventTypeMsgRecv,
		Peer:     ev.peerID,
		Protocol: ev.Protocol,
		MsgCode:  &msg.Code,
		MsgSize:  &msg.Size,
	})
	return msg, nil
}

// WriteMsg writes a message to the underlying MsgReadWriter and emits a
// "message sent" event
// 이 함수는 주어진 메시지 리드라이터로 메시지를 쓰고, message sent 메시지를 feed에 씀으로서
// 이벤트를 구독중인 피어에게 브로드 캐스팅한다
func (ev *msgEventer) WriteMsg(msg Msg) error {
	err := ev.MsgReadWriter.WriteMsg(msg)
	if err != nil {
		return err
	}
	ev.feed.Send(&PeerEvent{
		Type:     PeerEventTypeMsgSend,
		Peer:     ev.peerID,
		Protocol: ev.Protocol,
		MsgCode:  &msg.Code,
		MsgSize:  &msg.Size,
	})
	return nil
}

// Close closes the underlying MsgReadWriter if it implements the io.Closer
// interface
// Close 함수는 io.Close인터페이스를 구현한 MsgReadWriter를 닫는다
func (ev *msgEventer) Close() error {
	if v, ok := ev.MsgReadWriter.(io.Closer); ok {
		return v.Close()
	}
	return nil
}
