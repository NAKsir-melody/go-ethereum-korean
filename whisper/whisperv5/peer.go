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
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
	set "gopkg.in/fatih/set.v0"
)

// Peer represents a whisper protocol peer connection.
// Peer는 whisper프로토콜의 피어연결을 나타낸다
type Peer struct {
	host    *Whisper
	peer    *p2p.Peer
	ws      p2p.MsgReadWriter
	trusted bool

	known *set.Set // Messages already known by the peer to avoid wasting bandwidth
	// 대역폭 낭비를 피하기 위한 이미 피어에게 알려진 메시지들

	quit chan struct{}
}

// newPeer creates a new whisper peer object, but does not run the handshake itself.
// newPeer는 새로운 whisper피어 오브젝트를 생성하지만, handshake는 자체적으로 실행하지 않는다
func newPeer(host *Whisper, remote *p2p.Peer, rw p2p.MsgReadWriter) *Peer {
	return &Peer{
		host:    host,
		peer:    remote,
		ws:      rw,
		trusted: false,
		known:   set.New(),
		quit:    make(chan struct{}),
	}
}

// start initiates the peer updater, periodically broadcasting the whisper packets
// into the network.
// strat는 피어갱신자를 초기화 하고, 주기적으로 whisper패킷을 전송한다
func (peer *Peer) start() {
	go peer.update()
	log.Trace("start", "peer", peer.ID())
}

// stop terminates the peer updater, stopping message forwarding to it.
func (peer *Peer) stop() {
	close(peer.quit)
	log.Trace("stop", "peer", peer.ID())
}

// handshake sends the protocol initiation status message to the remote peer and
// verifies the remote status too.
// handshake는 프로토콜 초기화 상태메시지를 원격 피어로 전소앟고
// 원격의 상태를 검증한다
func (peer *Peer) handshake() error {
	// Send the handshake status message asynchronously
	errc := make(chan error, 1)
	go func() {
		errc <- p2p.Send(peer.ws, statusCode, ProtocolVersion)
	}()
	// Fetch the remote status packet and verify protocol match
	packet, err := peer.ws.ReadMsg()
	if err != nil {
		return err
	}
	if packet.Code != statusCode {
		return fmt.Errorf("peer [%x] sent packet %x before status packet", peer.ID(), packet.Code)
	}
	s := rlp.NewStream(packet.Payload, uint64(packet.Size))
	peerVersion, err := s.Uint()
	if err != nil {
		return fmt.Errorf("peer [%x] sent bad status message: %v", peer.ID(), err)
	}
	if peerVersion != ProtocolVersion {
		return fmt.Errorf("peer [%x]: protocol version mismatch %d != %d", peer.ID(), peerVersion, ProtocolVersion)
	}
	// Wait until out own status is consumed too
	if err := <-errc; err != nil {
		return fmt.Errorf("peer [%x] failed to send status packet: %v", peer.ID(), err)
	}
	return nil
}

// update executes periodic operations on the peer, including message transmission
// and expiration.
// update는 메시지 전송과 만료를 포함한 피어상의 주기적인 동작을 실행한다
func (peer *Peer) update() {
	// Start the tickers for the updates
	// 갱신을 위한 티커 시작
	expire := time.NewTicker(expirationCycle)
	transmit := time.NewTicker(transmissionCycle)

	// Loop and transmit until termination is requested
	for {
		select {
		case <-expire.C:
			peer.expire()

		case <-transmit.C:
			if err := peer.broadcast(); err != nil {
				log.Trace("broadcast failed", "reason", err, "peer", peer.ID())
				return
			}

		case <-peer.quit:
			return
		}
	}
}

// mark marks an envelope known to the peer so that it won't be sent back.
// mark는 봉투가 알려진 피어에게는 되돌아 오지 않도록 마킹한다
func (peer *Peer) mark(envelope *Envelope) {
	peer.known.Add(envelope.Hash())
}

// marked checks if an envelope is already known to the remote peer.
func (peer *Peer) marked(envelope *Envelope) bool {
	return peer.known.Has(envelope.Hash())
}

// expire iterates over all the known envelopes in the host and removes all
// expired (unknown) ones from the known list.
// expire는 호스트상의 알려진 모든 봉투에 대해 반복하며 
// 알려진 리스트로부터 만료된(알려지지않은) 것들을 제거한다
func (peer *Peer) expire() {
	unmark := make(map[common.Hash]struct{})
	peer.known.Each(func(v interface{}) bool {
		if !peer.host.isEnvelopeCached(v.(common.Hash)) {
			unmark[v.(common.Hash)] = struct{}{}
		}
		return true
	})
	// Dump all known but no longer cached
	for hash := range unmark {
		peer.known.Remove(hash)
	}
}

// broadcast iterates over the collection of envelopes and transmits yet unknown
// ones over the network.
// broadcast는 수집된 봉투들을 나복하며 네트워크에 아직 알려지지 않은 것들을 전송한다
func (peer *Peer) broadcast() error {
	var cnt int
	envelopes := peer.host.Envelopes()
	for _, envelope := range envelopes {
		if !peer.marked(envelope) {
			err := p2p.Send(peer.ws, messagesCode, envelope)
			if err != nil {
				return err
			} else {
				peer.mark(envelope)
				cnt++
			}
		}
	}
	if cnt > 0 {
		log.Trace("broadcast", "num. messages", cnt)
	}
	return nil
}

func (peer *Peer) ID() []byte {
	id := peer.peer.ID()
	return id[:]
}
