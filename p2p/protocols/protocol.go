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

/*
Package protocols is an extension to p2p. It offers a user friendly simple way to define
devp2p subprotocols by abstracting away code standardly shared by protocols.

* automate assigments of code indexes to messages
* automate RLP decoding/encoding based on reflecting
* provide the forever loop to read incoming messages
* standardise error handling related to communication
* standardised	handshake negotiation
* TODO: automatic generation of wire protocol specification for peers
*/
// protocol패키지는 p2p를 위한 확장이다
// 이 패키지는 devp2p 프로토콜을 정의하기 위해 프로토콜에 의해 제공되는 
// 스탠다드 코드를 추상한 유저에게 친숙한 방법을 제공한다
// 메시지에 대해 코드 인덱스 할당을 자동화한다
// 리플렉팅에 기반한 RLP 인/디코딩을 자동화한다
// 들어오는 메시지를 읽기 위한 루프를 제공한다
// 통신과 관련된 에러 핸들링을 정규화한다
// 핸드쉐이크 협상을 정규화한다
// TODO: 피어를 위한 와이어 프로토콜 정의 의 생성 자동화
package protocols

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/ethereum/go-ethereum/p2p"
)

// error codes used by this  protocol scheme
// 프로토콜 구조를 위해 사용될 에러코드들
const (
	ErrMsgTooLong = iota
	ErrDecode
	ErrWrite
	ErrInvalidMsgCode
	ErrInvalidMsgType
	ErrHandshake
	ErrNoHandler
	ErrHandler
)

// error description strings associated with the codes
// 코드에 관련된 에러 설명 문자열들
var errorToString = map[int]string{
	ErrMsgTooLong:     "Message too long",
	ErrDecode:         "Invalid message (RLP error)",
	ErrWrite:          "Error sending message",
	ErrInvalidMsgCode: "Invalid message code",
	ErrInvalidMsgType: "Invalid message type",
	ErrHandshake:      "Handshake error",
	ErrNoHandler:      "No handler registered error",
	ErrHandler:        "Message handler error",
}

/*
Error implements the standard go error interface.
Use:

  errorf(code, format, params ...interface{})

Prints as:

 <description>: <details>

where description is given by code in errorToString
and details is fmt.Sprintf(format, params...)

exported field Code can be checked
*/
// Error 구조체는 기준이되는 go 에러 인터페이스를 구현한다
type Error struct {
	Code    int
	message string
	format  string
	params  []interface{}
}

func (e Error) Error() (message string) {
	if len(e.message) == 0 {
		name, ok := errorToString[e.Code]
		if !ok {
			panic("invalid message code")
		}
		e.message = name
		if e.format != "" {
			e.message += ": " + fmt.Sprintf(e.format, e.params...)
		}
	}
	return e.message
}

func errorf(code int, format string, params ...interface{}) *Error {
	return &Error{
		Code:   code,
		format: format,
		params: params,
	}
}

// Spec is a protocol specification including its name and version as well as
// the types of messages which are exchanged
// Spec 구조체는 이름과 버전, 교환될 메새지의 타압을 포함한 프로토콜 정의이다
type Spec struct {
	// Name is the name of the protocol, often a three-letter word
	// 3글자로 표현되는 프로토콜의 이름
	Name string

	// Version is the version number of the protocol
	// 프로토콜의 버전넘버
	Version uint

	// MaxMsgSize is the maximum accepted length of the message payload
	// 메시지사이즈는 최대 수용가능한 메시지 페이로드의 길이이다
	MaxMsgSize uint32

	// Messages is a list of message data types which this protocol uses, with
	// each message type being sent with its array index as the code (so
	// [&foo{}, &bar{}, &baz{}] would send foo, bar and baz with codes
	// 0, 1 and 2 respectively)
	// each message must have a single unique data type
	// Messages는 프로토콜에서 사용할 메시지 데이터 타입의 리스트이며
	// 각 메시지 타입은 어레이 인덱스를 코드로서 전송할것이다
	// foo, bar, baz의 경우 foo, bar, baz는 0, 1, 2이다
	// 모든 메시지는 유일한 타입을 갖는다
	Messages []interface{}

	initOnce sync.Once
	codes    map[reflect.Type]uint64
	types    map[uint64]reflect.Type
}

func (s *Spec) init() {
	s.initOnce.Do(func() {
		s.codes = make(map[reflect.Type]uint64, len(s.Messages))
		s.types = make(map[uint64]reflect.Type, len(s.Messages))
		for i, msg := range s.Messages {
			code := uint64(i)
			typ := reflect.TypeOf(msg)
			if typ.Kind() == reflect.Ptr {
				typ = typ.Elem()
			}
			s.codes[typ] = code
			s.types[code] = typ
		}
	})
}

// Length returns the number of message types in the protocol
// Length함수는 프로토콜에서 사용하는 메시지 타입의 갯수를 반환한다
func (s *Spec) Length() uint64 {
	return uint64(len(s.Messages))
}

// GetCode returns the message code of a type, and boolean second argument is
// false if the message type is not found
// GetCode함수는 타입의 메시지코드를 반환하고 
// 메시지 타입이 찾아지지 않을 경우 두번째 인자가 false이다
func (s *Spec) GetCode(msg interface{}) (uint64, bool) {
	s.init()
	typ := reflect.TypeOf(msg)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	code, ok := s.codes[typ]
	return code, ok
}

// NewMsg construct a new message type given the code
// NewMsg 함수는 주어진 코드에 대한 새 메시지 타입을 생성한다
func (s *Spec) NewMsg(code uint64) (interface{}, bool) {
	s.init()
	typ, ok := s.types[code]
	if !ok {
		return nil, false
	}
	return reflect.New(typ).Interface(), true
}

// Peer represents a remote peer or protocol instance that is running on a peer connection with
// a remote peer
// Peer 구조체는 원격피어나 원격 피어의 연결에서 실행되는 프로토콜 인스턴스를 나타낸다
type Peer struct {
	*p2p.Peer                   // the p2p.Peer object representing the remote
	rw        p2p.MsgReadWriter // p2p.MsgReadWriter to send messages to and read messages from
	spec      *Spec
	// 원격을 나타내는 p2p.Peer 오브젝트
	// 메시지를 보내고 읽기위한 p2p.MsgReadWrite
}

// NewPeer constructs a new peer
// this constructor is called by the p2p.Protocol#Run function
// the first two arguments are the arguments passed to p2p.Protocol.Run function
// the third argument is the Spec describing the protocol
// NewPeer는 새로운 피어를 생성한다.
// 이 생성자는 p2p.Protocol#Run 함수에 의해 실행된다.
// 첫 두 인자는 호출한 함수에서 받은 인자이며, 세번째 인자는 프로토콜을 나타내는 스펙이다
func NewPeer(p *p2p.Peer, rw p2p.MsgReadWriter, spec *Spec) *Peer {
	return &Peer{
		Peer: p,
		rw:   rw,
		spec: spec,
	}
}

// Run starts the forever loop that handles incoming messages
// called within the p2p.Protocol#Run function
// the handler argument is a function which is called for each message received
// from the remote peer, a returned error causes the loop to exit
// resulting in disconnection
// Run 함수는 p2p.Protocol의 run함수에의해 호출되는 메시지를 처리하기 위한 영속적인 루프를 시작한다
// 핸들러 인자는 각 메시지가 수신되었을때 호출될 함수이고 루프가 접속끝김으로 인해 종료될경우
// 에러를 반환한다
func (p *Peer) Run(handler func(msg interface{}) error) error {
	for {
		if err := p.handleIncoming(handler); err != nil {
			return err
		}
	}
}

// Drop disconnects a peer.
// TODO: may need to implement protocol drop only? don't want to kick off the peer
// if they are useful for other protocols
// 접속종료
func (p *Peer) Drop(err error) {
	p.Disconnect(p2p.DiscSubprotocolError)
}

// Send takes a message, encodes it in RLP, finds the right message code and sends the
// message off to the peer
// this low level call will be wrapped by libraries providing routed or broadcast sends
// but often just used to forward and push messages to directly connected peers
// Send는 메시지를 받아서 RLP로 인코딩하고 올바른 메시지 코드를 찾아 피어들에게 전송한다
// 이 저수준 콜은 라우팅과 브로드캐스팅 전송을 지원하는 라이브러리로 포장될 얘정이지만
// 때때로 단순히 직접연결된 피어들에게 메지시를 전송하는 용도로 쓰인다
func (p *Peer) Send(msg interface{}) error {
	code, found := p.spec.GetCode(msg)
	if !found {
		return errorf(ErrInvalidMsgType, "%v", code)
	}
	return p2p.Send(p.rw, code, msg)
}

// handleIncoming(code)
// is called each cycle of the main forever loop that dispatches incoming messages
// if this returns an error the loop returns and the peer is disconnected with the error
// this generic handler
// * checks message size,
// * checks for out-of-range message codes,
// * handles decoding with reflection,
// * call handlers as callbacks
// handleIncoming 함수는 메인 루프에서 호출되며 들어온 메시지를 분석한다
// 만약 이함수가 반환된다면 루프가 반환한 에러가 반환된다. 
// 피어는 일반적인 에러 핸들러에 의해 접속이 끊긴다
// 메시지 사이즈를 확인
// 메시지 코드의 범위를 확인
// 타입에따른 디코딩을 지원
// 핸들러를 콜백으로서 호출
func (p *Peer) handleIncoming(handle func(msg interface{}) error) error {
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return err
	}
	// make sure that the payload has been fully consumed
	// 페이로드가 모두 소비되었을음을 보장
	defer msg.Discard()

	if msg.Size > p.spec.MaxMsgSize {
		return errorf(ErrMsgTooLong, "%v > %v", msg.Size, p.spec.MaxMsgSize)
	}

	val, ok := p.spec.NewMsg(msg.Code)
	if !ok {
		return errorf(ErrInvalidMsgCode, "%v", msg.Code)
	}
	if err := msg.Decode(val); err != nil {
		return errorf(ErrDecode, "<= %v: %v", msg, err)
	}

	// call the registered handler callbacks
	// a registered callback take the decoded message as argument as an interface
	// which the handler is supposed to cast to the appropriate type
	// it is entirely safe not to check the cast in the handler since the handler is
	// chosen based on the proper type in the first place
	// 등록된 콜백을 호출한다
	// 등록된 콜백은 해독된 메시지를 적절한 타입으로 캐스팅 될 핸들러의 인터페이스 인자로 받는다
	if err := handle(val); err != nil {
		return errorf(ErrHandler, "(msg code %v): %v", msg.Code, err)
	}
	return nil
}

// Handshake negotiates a handshake on the peer connection
// * arguments
//   * context
//   * the local handshake to be sent to the remote peer
//   * funcion to be called on the remote handshake (can be nil)
// * expects a remote handshake back of the same type
// * the dialing peer needs to send the handshake first and then waits for remote
// * the listening peer waits for the remote handshake and then sends it
// returns the remote handshake and an error
// Handshake함수는 피어 연결에서 핸드쉐이크를 협상한다
// 인자들
// 컨텍스트
// 원격으로 전송될 로컬의 핸드쉐이크
// 처음으로 핸드쉐이크를 보내고 대기할 디이얼링 피어
// 수신 대기중인 피어
// 반환값은 원격의 핸드쉐이크와 에러이다
func (p *Peer) Handshake(ctx context.Context, hs interface{}, verify func(interface{}) error) (rhs interface{}, err error) {
	if _, ok := p.spec.GetCode(hs); !ok {
		return nil, errorf(ErrHandshake, "unknown handshake message type: %T", hs)
	}
	errc := make(chan error, 2)
	handle := func(msg interface{}) error {
		rhs = msg
		if verify != nil {
			return verify(rhs)
		}
		return nil
	}
	send := func() { errc <- p.Send(hs) }
	receive := func() { errc <- p.handleIncoming(handle) }

	go func() {
		if p.Inbound() {
			receive()
			send()
		} else {
			send()
			receive()
		}
	}()

	for i := 0; i < 2; i++ {
		select {
		case err = <-errc:
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err != nil {
			return nil, errorf(ErrHandshake, err.Error())
		}
	}
	return rhs, nil
}
