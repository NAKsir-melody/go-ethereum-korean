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

package shhclient

import (
	"context"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	whisper "github.com/ethereum/go-ethereum/whisper/whisperv6"
)

// Client defines typed wrappers for the Whisper v6 RPC API.
// Client는 whisper v6 RPC  api를 위한 래퍼들을 정의한다
type Client struct {
	c *rpc.Client
}

// Dial connects a client to the given URL.
// Dial은 주어진 url에 대해 클라이언트를 연결한다
func Dial(rawurl string) (*Client, error) {
	c, err := rpc.Dial(rawurl)
	if err != nil {
		return nil, err
	}
	return NewClient(c), nil
}

// NewClient creates a client that uses the given RPC client.
// NewClient는 주어진 RPC클라이언트를 이용하는 클라이언트를 생성한다
func NewClient(c *rpc.Client) *Client {
	return &Client{c}
}

// Version returns the Whisper sub-protocol version.
// Version은 whisper 서브프로토콜의 버전을 반환한다
func (sc *Client) Version(ctx context.Context) (string, error) {
	var result string
	err := sc.c.CallContext(ctx, &result, "shh_version")
	return result, err
}

// Info returns diagnostic information about the whisper node.
// Info는whisper노드에 대한 진단정보를 반환한다.
func (sc *Client) Info(ctx context.Context) (whisper.Info, error) {
	var info whisper.Info
	err := sc.c.CallContext(ctx, &info, "shh_info")
	return info, err
}

// SetMaxMessageSize sets the maximal message size allowed by this node. Incoming
// and outgoing messages with a larger size will be rejected. Whisper message size
// can never exceed the limit imposed by the underlying P2P protocol (10 Mb).
// SetMaxMessageSize는 노드에의해 허용되는 메시지의 최대값을 설정한다
// 더큰 모든 메시지는 거절된다.
// whisper메시지 크기는 내부적 p2p프로토콜에 의해 부과된 10Mb를 최과할수 없다
func (sc *Client) SetMaxMessageSize(ctx context.Context, size uint32) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_setMaxMessageSize", size)
}

// SetMinimumPoW (experimental) sets the minimal PoW required by this node.
// This experimental function was introduced for the future dynamic adjustment of
// PoW requirement. If the node is overwhelmed with messages, it should raise the
// PoW requirement and notify the peers. The new value should be set relative to
// the old value (e.g. double). The old value could be obtained via shh_info call.
// SetMinimumPoW는노드가 요구하는 최소 pow를 설정한다.
// 이 실험적인 함수는 pow 요구사항의 동적인 변화에 대비한 것이다.
// 만약 노드가 메시지에 의해 압도당한다면 pow 요구사항을 상승시키고
// 피어들에게 알려야만 한다. 새로운 값은 과거의 값과 연관성이 있어야 한다.(2배등)
// 과거의 값은 ssh_info call에 의해 얻을수 있다
func (sc *Client) SetMinimumPoW(ctx context.Context, pow float64) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_setMinPoW", pow)
}

// MarkTrustedPeer marks specific peer trusted, which will allow it to send historic (expired) messages.
// Note This function is not adding new nodes, the node needs to exists as a peer.
// MarkTrustedPeer는 만료된 과거 메시지를 전송하는 것을 허용하는 신뢰하는 특정피어를 마크한다
// 이함수는 새로운 노드를 추가하지 않는다.
func (sc *Client) MarkTrustedPeer(ctx context.Context, enode string) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_markTrustedPeer", enode)
}

// NewKeyPair generates a new public and private key pair for message decryption and encryption.
// It returns an identifier that can be used to refer to the key.
// NewKeyPair 는 메시지의 암/복호화를 위한 새로운 공개키/개인키 조합을 생성한다
// 이 함수는 키 참조를 가능케 하기 위한 id를 반환한다
func (sc *Client) NewKeyPair(ctx context.Context) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_newKeyPair")
}

// AddPrivateKey stored the key pair, and returns its ID.
// AddPrivateKey는 키패어를 저장하고 ID를 반환한다
func (sc *Client) AddPrivateKey(ctx context.Context, key []byte) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_addPrivateKey", hexutil.Bytes(key))
}

// DeleteKeyPair delete the specifies key.
// DeleteKeyPair는 특정키를 제거한다
func (sc *Client) DeleteKeyPair(ctx context.Context, id string) (string, error) {
	var ignored bool
	return id, sc.c.CallContext(ctx, &ignored, "shh_deleteKeyPair", id)
}

// HasKeyPair returns an indication if the node has a private key or
// key pair matching the given ID.
// HasKeyPair함수는 노드가 개인키나 주어진 id에 대한 매칭되는 키를 가지고 있는지를
// 알려준다
func (sc *Client) HasKeyPair(ctx context.Context, id string) (bool, error) {
	var has bool
	return has, sc.c.CallContext(ctx, &has, "shh_hasKeyPair", id)
}

// PublicKey return the public key for a key ID.
// PublicKey는 키 아이디에 대한 공개키를 반환한다
func (sc *Client) PublicKey(ctx context.Context, id string) ([]byte, error) {
	var key hexutil.Bytes
	return []byte(key), sc.c.CallContext(ctx, &key, "shh_getPublicKey", id)
}

// PrivateKey return the private key for a key ID.
// PrivateKey는 키 아이디에 대한 개인키를 반환한다
func (sc *Client) PrivateKey(ctx context.Context, id string) ([]byte, error) {
	var key hexutil.Bytes
	return []byte(key), sc.c.CallContext(ctx, &key, "shh_getPrivateKey", id)
}

// NewSymmetricKey generates a random symmetric key and returns its identifier.
// Can be used encrypting and decrypting messages where the key is known to both parties.
// NewSymmetricKey는 랜덤한 대칭키를 생성하고 id를 반환한다
// 키가 양쪽에 알려져있을때 암/복호화하는데 사용할 수 있다.
func (sc *Client) NewSymmetricKey(ctx context.Context) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_newSymKey")
}

// AddSymmetricKey stores the key, and returns its identifier.
// AddSymmetircKey는 키를 저장하고 id를 반환한다
func (sc *Client) AddSymmetricKey(ctx context.Context, key []byte) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_addSymKey", hexutil.Bytes(key))
}

// GenerateSymmetricKeyFromPassword generates the key from password, stores it, and returns its identifier.
// GenerateSymmetricKeyFromPassword는 암호로부터 키를 생성하고, 저장하고, id를 반환한다
func (sc *Client) GenerateSymmetricKeyFromPassword(ctx context.Context, passwd string) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_generateSymKeyFromPassword", passwd)
}

// HasSymmetricKey returns an indication if the key associated with the given id is stored in the node.
// HasSymmetircKey는 주어진 id에 관련된 키가 노드에 저장되어 있는지 알려준다
func (sc *Client) HasSymmetricKey(ctx context.Context, id string) (bool, error) {
	var found bool
	return found, sc.c.CallContext(ctx, &found, "shh_hasSymKey", id)
}

// GetSymmetricKey returns the symmetric key associated with the given identifier.
// GetSymmetircKey는 주어진 id에 대한 대칭키를 반환한다
func (sc *Client) GetSymmetricKey(ctx context.Context, id string) ([]byte, error) {
	var key hexutil.Bytes
	return []byte(key), sc.c.CallContext(ctx, &key, "shh_getSymKey", id)
}

// DeleteSymmetricKey deletes the symmetric key associated with the given identifier.
// DeleteSymmetircKey는 id에 해당하는 대칭키를 삭제한다
func (sc *Client) DeleteSymmetricKey(ctx context.Context, id string) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_deleteSymKey", id)
}

// Post a message onto the network.
// 메시지를 네트워크로 전달한다
func (sc *Client) Post(ctx context.Context, message whisper.NewMessage) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_post", message)
}

// SubscribeMessages subscribes to messages that match the given criteria. This method
// is only supported on bi-directional connections such as websockets and IPC.
// NewMessageFilter uses polling and is supported over HTTP.
// SubscirbeMessages는 주어진 기준에 부합하는 메시지들을 구독한다
// 이함수는 websocket이나 ipc같은 양방향 연결만 지원한다
// NewMessageFilter는 폴링을 사용하며, http위에서 제공된다
func (sc *Client) SubscribeMessages(ctx context.Context, criteria whisper.Criteria, ch chan<- *whisper.Message) (ethereum.Subscription, error) {
	return sc.c.ShhSubscribe(ctx, ch, "messages", criteria)
}

// NewMessageFilter creates a filter within the node. This filter can be used to poll
// for new messages (see FilterMessages) that satisfy the given criteria. A filter can
// timeout when it was polled for in whisper.filterTimeout.
// NewMessageFilter는 노드의 필터를 생성한다. 이필터는 조건을 만족하는
// 새로운 메시지를 위한 폴링에 사용될수 있다. 필터는 필터 타임아웃 기간동안 폴링되고 
// 타임아웃될수 있다
func (sc *Client) NewMessageFilter(ctx context.Context, criteria whisper.Criteria) (string, error) {
	var id string
	return id, sc.c.CallContext(ctx, &id, "shh_newMessageFilter", criteria)
}

// DeleteMessageFilter removes the filter associated with the given id.
// DeleteMessageFilter는 주어진 id에 대한 필터를 제거한다
func (sc *Client) DeleteMessageFilter(ctx context.Context, id string) error {
	var ignored bool
	return sc.c.CallContext(ctx, &ignored, "shh_deleteMessageFilter", id)
}

// FilterMessages retrieves all messages that are received between the last call to
// this function and match the criteria that where given when the filter was created.
// FilterMessages는 마지막 호출로부터 필터 생성시 지정된 조건에 맞는 메시지들을
// 반환한다.
func (sc *Client) FilterMessages(ctx context.Context, id string) ([]*whisper.Message, error) {
	var messages []*whisper.Message
	return messages, sc.c.CallContext(ctx, &messages, "shh_getFilterMessages", id)
}
