// Copyright 2016 The go-ethereum Authors
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

package whisperv5

import (
	"crypto/ecdsa"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
)

type Filter struct {
	Src        *ecdsa.PublicKey  // Sender of the message
	// 메시지 전송자
	KeyAsym    *ecdsa.PrivateKey // Private Key of recipient
	// 수신자의 개인키
	KeySym     []byte            // Key associated with the Topic
	// Topic에 관련된 키
	Topics     [][]byte          // Topics to filter messages with
	// 메시지 필터할 토픽
	PoW        float64           // Proof of work as described in the Whisper spec
	// whisper스펙에 표현된 pow
	AllowP2P   bool              // Indicates whether this filter is interested in direct peer-to-peer messages
	// 이필터가 1:1 메시지에 관심이 있는지 여부
	SymKeyHash common.Hash       // The Keccak256Hash of the symmetric key, needed for optimization
	// 대칭키의 256해시

	Messages map[common.Hash]*ReceivedMessage
	mutex    sync.RWMutex
}

type Filters struct {
	watchers map[string]*Filter
	whisper  *Whisper
	mutex    sync.RWMutex
}

func NewFilters(w *Whisper) *Filters {
	return &Filters{
		watchers: make(map[string]*Filter),
		whisper:  w,
	}
}

func (fs *Filters) Install(watcher *Filter) (string, error) {
	if watcher.Messages == nil {
		watcher.Messages = make(map[common.Hash]*ReceivedMessage)
	}

	id, err := GenerateRandomID()
	if err != nil {
		return "", err
	}

	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	if fs.watchers[id] != nil {
		return "", fmt.Errorf("failed to generate unique ID")
	}

	if watcher.expectsSymmetricEncryption() {
		watcher.SymKeyHash = crypto.Keccak256Hash(watcher.KeySym)
	}

	fs.watchers[id] = watcher
	return id, err
}

func (fs *Filters) Uninstall(id string) bool {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()
	if fs.watchers[id] != nil {
		delete(fs.watchers, id)
		return true
	}
	return false
}

func (fs *Filters) Get(id string) *Filter {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()
	return fs.watchers[id]
}

func (fs *Filters) NotifyWatchers(env *Envelope, p2pMessage bool) {
	var msg *ReceivedMessage

	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	i := -1 // only used for logging info
	for _, watcher := range fs.watchers {
		i++
		if p2pMessage && !watcher.AllowP2P {
			log.Trace(fmt.Sprintf("msg [%x], filter [%d]: p2p messages are not allowed", env.Hash(), i))
			continue
		}

		var match bool
		if msg != nil {
			match = watcher.MatchMessage(msg)
		} else {
			match = watcher.MatchEnvelope(env)
			if match {
				msg = env.Open(watcher)
				if msg == nil {
					log.Trace("processing message: failed to open", "message", env.Hash().Hex(), "filter", i)
				}
			} else {
				log.Trace("processing message: does not match", "message", env.Hash().Hex(), "filter", i)
			}
		}

		if match && msg != nil {
			log.Trace("processing message: decrypted", "hash", env.Hash().Hex())
			if watcher.Src == nil || IsPubKeyEqual(msg.Src, watcher.Src) {
				watcher.Trigger(msg)
			}
		}
	}
}

func (f *Filter) processEnvelope(env *Envelope) *ReceivedMessage {
	if f.MatchEnvelope(env) {
		msg := env.Open(f)
		if msg != nil {
			return msg
		} else {
			log.Trace("processing envelope: failed to open", "hash", env.Hash().Hex())
		}
	} else {
		log.Trace("processing envelope: does not match", "hash", env.Hash().Hex())
	}
	return nil
}

func (f *Filter) expectsAsymmetricEncryption() bool {
	return f.KeyAsym != nil
}

func (f *Filter) expectsSymmetricEncryption() bool {
	return f.KeySym != nil
}

func (f *Filter) Trigger(msg *ReceivedMessage) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if _, exist := f.Messages[msg.EnvelopeHash]; !exist {
		f.Messages[msg.EnvelopeHash] = msg
	}
}

func (f *Filter) Retrieve() (all []*ReceivedMessage) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	all = make([]*ReceivedMessage, 0, len(f.Messages))
	for _, msg := range f.Messages {
		all = append(all, msg)
	}

	f.Messages = make(map[common.Hash]*ReceivedMessage) // delete old messages
	return all
}

// 비대칭키 암호화라면 public key가 메시지의 목적지인지
// 대칭키 암호화라면 키 해시가 토픽의 해시와 같은지 확인한다
func (f *Filter) MatchMessage(msg *ReceivedMessage) bool {
	if f.PoW > 0 && msg.PoW < f.PoW {
		return false
	}

	if f.expectsAsymmetricEncryption() && msg.isAsymmetricEncryption() {
		return IsPubKeyEqual(&f.KeyAsym.PublicKey, msg.Dst) && f.MatchTopic(msg.Topic)
	} else if f.expectsSymmetricEncryption() && msg.isSymmetricEncryption() {
		return f.SymKeyHash == msg.SymKeyHash && f.MatchTopic(msg.Topic)
	}
	return false
}

func (f *Filter) MatchEnvelope(envelope *Envelope) bool {
	if f.PoW > 0 && envelope.pow < f.PoW {
		return false
	}

	if f.expectsAsymmetricEncryption() && envelope.isAsymmetric() {
		return f.MatchTopic(envelope.Topic)
	} else if f.expectsSymmetricEncryption() && envelope.IsSymmetric() {
		return f.MatchTopic(envelope.Topic)
	}
	return false
}

func (f *Filter) MatchTopic(topic TopicType) bool {
	if len(f.Topics) == 0 {
		// any topic matches
		return true
	}

	for _, bt := range f.Topics {
		if matchSingleTopic(topic, bt) {
			return true
		}
	}
	return false
}

func matchSingleTopic(topic TopicType, bt []byte) bool {
	if len(bt) > TopicLength {
		bt = bt[:TopicLength]
	}

	if len(bt) < TopicLength {
		return false
	}

	for j, b := range bt {
		if topic[j] != b {
			return false
		}
	}
	return true
}

func IsPubKeyEqual(a, b *ecdsa.PublicKey) bool {
	if !ValidatePublicKey(a) {
		return false
	} else if !ValidatePublicKey(b) {
		return false
	}
	// the curve is always the same, just compare the points
	return a.X.Cmp(b.X) == 0 && a.Y.Cmp(b.Y) == 0
}
