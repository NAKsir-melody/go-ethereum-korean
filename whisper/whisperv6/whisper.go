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

package whisperv6

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/syndtr/goleveldb/leveldb/errors"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/sync/syncmap"
	set "gopkg.in/fatih/set.v0"
)

// Statistics holds several message-related counter for analytics
// purposes.
// Statistics는 분석목적의 메시지 관련 카운터들을 저장한다
type Statistics struct {
	messagesCleared      int
	memoryCleared        int
	memoryUsed           int
	cycles               int
	totalMessagesCleared int
}

const (
	maxMsgSizeIdx           = iota // Maximal message length allowed by the whisper node
	// whisper노드가 허용하는 메시지의 최대길이
	overflowIdx                    // Indicator of message queue overflow
	// 메시지 큐 오버플로우 지시자
	minPowIdx                      // Minimal PoW required by the whisper node
	// whisper노드의 최소 요구 pow값
	minPowToleranceIdx             // Minimal PoW tolerated by the whisper node for a limited time
	// whisper노드의 제한시간의 최소 허용pow값
	bloomFilterIdx                 // Bloom filter for topics of interest for this node
	// 노드가 관심있는 토픽에 대한 블룸필터
	bloomFilterToleranceIdx        // Bloom filter tolerated by the whisper node for a limited time
	// 제한시간내 노드에 의해 허용된 블룸필터
)

// Whisper represents a dark communication interface through the Ethereum
// network, using its very own P2P communication layer.
// Whisper는 자체적인 p2p통신 계층을 이용한 이더리움을상의
// 비실명 통신 인터페이스를 나타낸다
type Whisper struct {
	protocol p2p.Protocol // Protocol description and parameters
	// 프로토콜 및 변수들
	filters  *Filters     // Message filters installed with Subscribe function
	// 구독 함수와 함께 설치된 메시지 필터들
	privateKeys map[string]*ecdsa.PrivateKey // Private key storage
	// 개인키 저장소
	symKeys     map[string][]byte            // Symmetric key storage
	// 대칭키 저장소
	keyMu       sync.RWMutex                 // Mutex associated with key storages
	// 키 저장소와 연관된 뮤텍스

	poolMu      sync.RWMutex              // Mutex to sync the message and expiration pools
	// 메시지와 초과풀간의 싱크를 위한 뮤텍스
	envelopes   map[common.Hash]*Envelope // Pool of envelopes currently tracked by this node
	// 현재 노드에 의해 추적되는 봉투들의 풀
	expirations map[uint32]*set.SetNonTS  // Message expiration pool
	// 메시지 초과 풀

	peerMu sync.RWMutex       // Mutex to sync the active peer set
	// 활성 피어들의 싱크를 위한 뮤텍스
	peers  map[*Peer]struct{} // Set of currently active peers
	// 활성화된 피어들

	messageQueue chan *Envelope // Message queue for normal whisper messages
	// 일반 whisper 메시지들을 위한 큐
	p2pMsgQueue  chan *Envelope // Message queue for peer-to-peer messages (not to be forwarded any further)
	// 1:1 통신을 위한 메시지 큐
	quit         chan struct{}  // Channel used for graceful exit

	settings syncmap.Map // holds configuration settings that can be dynamically changed
	// 동적으로 변경가능한 설정값을 저장함

	syncAllowance int // maximum time in seconds allowed to process the whisper-related messages
	// whisper와 관련된 메시지를 처리하는 최대 허용시간

	lightClient bool // indicates is this node is pure light client (does not forward any messages)
	// 아무 메시지도 포워딩하지 않는 순수 라이트 클라이언트

	statsMu sync.Mutex // guard stats
	stats   Statistics // Statistics of whisper node
	// whipser노드의 상태통계

	mailServer MailServer // MailServer interface
	//mailserver 인터페이스
}

// New creates a Whisper client ready to communicate through the Ethereum P2P network.
// 이더리움 p2p 네트워크를 통해 통신할 준비가 된 whisper클라이언트를 생성
func New(cfg *Config) *Whisper {
	if cfg == nil {
		cfg = &DefaultConfig
	}

	whisper := &Whisper{
		privateKeys:   make(map[string]*ecdsa.PrivateKey),
		symKeys:       make(map[string][]byte),
		envelopes:     make(map[common.Hash]*Envelope),
		expirations:   make(map[uint32]*set.SetNonTS),
		peers:         make(map[*Peer]struct{}),
		messageQueue:  make(chan *Envelope, messageQueueLimit),
		p2pMsgQueue:   make(chan *Envelope, messageQueueLimit),
		quit:          make(chan struct{}),
		syncAllowance: DefaultSyncAllowance,
	}

	whisper.filters = NewFilters(whisper)

	whisper.settings.Store(minPowIdx, cfg.MinimumAcceptedPOW)
	whisper.settings.Store(maxMsgSizeIdx, cfg.MaxMessageSize)
	whisper.settings.Store(overflowIdx, false)

	// p2p whisper sub protocol handler
	// p2p 서브 프로토콜 핸들러
	whisper.protocol = p2p.Protocol{
		Name:    ProtocolName,
		Version: uint(ProtocolVersion),
		Length:  NumberOfMessageCodes,
		Run:     whisper.HandlePeer,
		NodeInfo: func() interface{} {
			return map[string]interface{}{
				"version":        ProtocolVersionStr,
				"maxMessageSize": whisper.MaxMessageSize(),
				"minimumPoW":     whisper.MinPow(),
			}
		},
	}

	return whisper
}

// MinPow returns the PoW value required by this node.
func (whisper *Whisper) MinPow() float64 {
	val, exist := whisper.settings.Load(minPowIdx)
	if !exist || val == nil {
		return DefaultMinimumPoW
	}
	v, ok := val.(float64)
	if !ok {
		log.Error("Error loading minPowIdx, using default")
		return DefaultMinimumPoW
	}
	return v
}

// MinPowTolerance returns the value of minimum PoW which is tolerated for a limited
// time after PoW was changed. If sufficient time have elapsed or no change of PoW
// have ever occurred, the return value will be the same as return value of MinPow().
func (whisper *Whisper) MinPowTolerance() float64 {
	val, exist := whisper.settings.Load(minPowToleranceIdx)
	if !exist || val == nil {
		return DefaultMinimumPoW
	}
	return val.(float64)
}

// BloomFilter returns the aggregated bloom filter for all the topics of interest.
// The nodes are required to send only messages that match the advertised bloom filter.
// If a message does not match the bloom, it will tantamount to spam, and the peer will
// be disconnected.
// BloomFilter는 모든 관심 토픽이 누적된 플룸필터를 반환한다
// 노드들은 알려진 블룸필터에 매칭되는 메시지만을 전달해야 한다
// 만약 메시지가 블룸과 맞지 않는다면 스팸이고, 피어와의 연결을 끊는다
func (whisper *Whisper) BloomFilter() []byte {
	val, exist := whisper.settings.Load(bloomFilterIdx)
	if !exist || val == nil {
		return nil
	}
	return val.([]byte)
}

// BloomFilterTolerance returns the bloom filter which is tolerated for a limited
// time after new bloom was advertised to the peers. If sufficient time have elapsed
// or no change of bloom filter have ever occurred, the return value will be the same
// as return value of BloomFilter().
func (whisper *Whisper) BloomFilterTolerance() []byte {
	val, exist := whisper.settings.Load(bloomFilterToleranceIdx)
	if !exist || val == nil {
		return nil
	}
	return val.([]byte)
}

// MaxMessageSize returns the maximum accepted message size.
func (whisper *Whisper) MaxMessageSize() uint32 {
	val, _ := whisper.settings.Load(maxMsgSizeIdx)
	return val.(uint32)
}

// Overflow returns an indication if the message queue is full.
func (whisper *Whisper) Overflow() bool {
	val, _ := whisper.settings.Load(overflowIdx)
	return val.(bool)
}

// APIs returns the RPC descriptors the Whisper implementation offers
func (whisper *Whisper) APIs() []rpc.API {
	return []rpc.API{
		{
			Namespace: ProtocolName,
			Version:   ProtocolVersionStr,
			Service:   NewPublicWhisperAPI(whisper),
			Public:    true,
		},
	}
}

// RegisterServer registers MailServer interface.
// MailServer will process all the incoming messages with p2pRequestCode.
func (whisper *Whisper) RegisterServer(server MailServer) {
	whisper.mailServer = server
}

// Protocols returns the whisper sub-protocols ran by this particular client.
func (whisper *Whisper) Protocols() []p2p.Protocol {
	return []p2p.Protocol{whisper.protocol}
}

// Version returns the whisper sub-protocols version number.
func (whisper *Whisper) Version() uint {
	return whisper.protocol.Version
}

// SetMaxMessageSize sets the maximal message size allowed by this node
func (whisper *Whisper) SetMaxMessageSize(size uint32) error {
	if size > MaxMessageSize {
		return fmt.Errorf("message size too large [%d>%d]", size, MaxMessageSize)
	}
	whisper.settings.Store(maxMsgSizeIdx, size)
	return nil
}

// SetBloomFilter sets the new bloom filter
func (whisper *Whisper) SetBloomFilter(bloom []byte) error {
	if len(bloom) != BloomFilterSize {
		return fmt.Errorf("invalid bloom filter size: %d", len(bloom))
	}

	b := make([]byte, BloomFilterSize)
	copy(b, bloom)

	whisper.settings.Store(bloomFilterIdx, b)
	whisper.notifyPeersAboutBloomFilterChange(b)

	go func() {
		// allow some time before all the peers have processed the notification
		time.Sleep(time.Duration(whisper.syncAllowance) * time.Second)
		whisper.settings.Store(bloomFilterToleranceIdx, b)
	}()

	return nil
}

// SetMinimumPoW sets the minimal PoW required by this node
func (whisper *Whisper) SetMinimumPoW(val float64) error {
	if val < 0.0 {
		return fmt.Errorf("invalid PoW: %f", val)
	}

	whisper.settings.Store(minPowIdx, val)
	whisper.notifyPeersAboutPowRequirementChange(val)

	go func() {
		// allow some time before all the peers have processed the notification
		time.Sleep(time.Duration(whisper.syncAllowance) * time.Second)
		whisper.settings.Store(minPowToleranceIdx, val)
	}()

	return nil
}

// SetMinimumPowTest sets the minimal PoW in test environment
func (whisper *Whisper) SetMinimumPowTest(val float64) {
	whisper.settings.Store(minPowIdx, val)
	whisper.notifyPeersAboutPowRequirementChange(val)
	whisper.settings.Store(minPowToleranceIdx, val)
}

func (whisper *Whisper) notifyPeersAboutPowRequirementChange(pow float64) {
	arr := whisper.getPeers()
	for _, p := range arr {
		err := p.notifyAboutPowRequirementChange(pow)
		if err != nil {
			// allow one retry
			err = p.notifyAboutPowRequirementChange(pow)
		}
		if err != nil {
			log.Warn("failed to notify peer about new pow requirement", "peer", p.ID(), "error", err)
		}
	}
}

func (whisper *Whisper) notifyPeersAboutBloomFilterChange(bloom []byte) {
	arr := whisper.getPeers()
	for _, p := range arr {
		err := p.notifyAboutBloomFilterChange(bloom)
		if err != nil {
			// allow one retry
			err = p.notifyAboutBloomFilterChange(bloom)
		}
		if err != nil {
			log.Warn("failed to notify peer about new bloom filter", "peer", p.ID(), "error", err)
		}
	}
}

func (whisper *Whisper) getPeers() []*Peer {
	arr := make([]*Peer, len(whisper.peers))
	i := 0
	whisper.peerMu.Lock()
	for p := range whisper.peers {
		arr[i] = p
		i++
	}
	whisper.peerMu.Unlock()
	return arr
}

// getPeer retrieves peer by ID
func (whisper *Whisper) getPeer(peerID []byte) (*Peer, error) {
	whisper.peerMu.Lock()
	defer whisper.peerMu.Unlock()
	for p := range whisper.peers {
		id := p.peer.ID()
		if bytes.Equal(peerID, id[:]) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("Could not find peer with ID: %x", peerID)
}

// AllowP2PMessagesFromPeer marks specific peer trusted,
// which will allow it to send historic (expired) messages.
func (whisper *Whisper) AllowP2PMessagesFromPeer(peerID []byte) error {
	p, err := whisper.getPeer(peerID)
	if err != nil {
		return err
	}
	p.trusted = true
	return nil
}

// RequestHistoricMessages sends a message with p2pRequestCode to a specific peer,
// which is known to implement MailServer interface, and is supposed to process this
// request and respond with a number of peer-to-peer messages (possibly expired),
// which are not supposed to be forwarded any further.
// The whisper protocol is agnostic of the format and contents of envelope.
func (whisper *Whisper) RequestHistoricMessages(peerID []byte, envelope *Envelope) error {
	p, err := whisper.getPeer(peerID)
	if err != nil {
		return err
	}
	p.trusted = true
	return p2p.Send(p.ws, p2pRequestCode, envelope)
}

// SendP2PMessage sends a peer-to-peer message to a specific peer.
func (whisper *Whisper) SendP2PMessage(peerID []byte, envelope *Envelope) error {
	p, err := whisper.getPeer(peerID)
	if err != nil {
		return err
	}
	return whisper.SendP2PDirect(p, envelope)
}

// SendP2PDirect sends a peer-to-peer message to a specific peer.
func (whisper *Whisper) SendP2PDirect(peer *Peer, envelope *Envelope) error {
	return p2p.Send(peer.ws, p2pMessageCode, envelope)
}

// NewKeyPair generates a new cryptographic identity for the client, and injects
// it into the known identities for message decryption. Returns ID of the new key pair.
func (whisper *Whisper) NewKeyPair() (string, error) {
	key, err := crypto.GenerateKey()
	if err != nil || !validatePrivateKey(key) {
		key, err = crypto.GenerateKey() // retry once
	}
	if err != nil {
		return "", err
	}
	if !validatePrivateKey(key) {
		return "", fmt.Errorf("failed to generate valid key")
	}

	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}

	whisper.keyMu.Lock()
	defer whisper.keyMu.Unlock()

	if whisper.privateKeys[id] != nil {
		return "", fmt.Errorf("failed to generate unique ID")
	}
	whisper.privateKeys[id] = key
	return id, nil
}

// DeleteKeyPair deletes the specified key if it exists.
func (whisper *Whisper) DeleteKeyPair(key string) bool {
	whisper.keyMu.Lock()
	defer whisper.keyMu.Unlock()

	if whisper.privateKeys[key] != nil {
		delete(whisper.privateKeys, key)
		return true
	}
	return false
}

// AddKeyPair imports a asymmetric private key and returns it identifier.
func (whisper *Whisper) AddKeyPair(key *ecdsa.PrivateKey) (string, error) {
	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}

	whisper.keyMu.Lock()
	whisper.privateKeys[id] = key
	whisper.keyMu.Unlock()

	return id, nil
}

// HasKeyPair checks if the the whisper node is configured with the private key
// of the specified public pair.
func (whisper *Whisper) HasKeyPair(id string) bool {
	whisper.keyMu.RLock()
	defer whisper.keyMu.RUnlock()
	return whisper.privateKeys[id] != nil
}

// GetPrivateKey retrieves the private key of the specified identity.
func (whisper *Whisper) GetPrivateKey(id string) (*ecdsa.PrivateKey, error) {
	whisper.keyMu.RLock()
	defer whisper.keyMu.RUnlock()
	key := whisper.privateKeys[id]
	if key == nil {
		return nil, fmt.Errorf("invalid id")
	}
	return key, nil
}

// GenerateSymKey generates a random symmetric key and stores it under id,
// which is then returned. Will be used in the future for session key exchange.
func (whisper *Whisper) GenerateSymKey() (string, error) {
	key, err := generateSecureRandomData(aesKeyLength)
	if err != nil {
		return "", err
	} else if !validateDataIntegrity(key, aesKeyLength) {
		return "", fmt.Errorf("error in GenerateSymKey: crypto/rand failed to generate random data")
	}

	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}

	whisper.keyMu.Lock()
	defer whisper.keyMu.Unlock()

	if whisper.symKeys[id] != nil {
		return "", fmt.Errorf("failed to generate unique ID")
	}
	whisper.symKeys[id] = key
	return id, nil
}

// AddSymKeyDirect stores the key, and returns its id.
func (whisper *Whisper) AddSymKeyDirect(key []byte) (string, error) {
	if len(key) != aesKeyLength {
		return "", fmt.Errorf("wrong key size: %d", len(key))
	}

	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}

	whisper.keyMu.Lock()
	defer whisper.keyMu.Unlock()

	if whisper.symKeys[id] != nil {
		return "", fmt.Errorf("failed to generate unique ID")
	}
	whisper.symKeys[id] = key
	return id, nil
}

// AddSymKeyFromPassword generates the key from password, stores it, and returns its id.
func (whisper *Whisper) AddSymKeyFromPassword(password string) (string, error) {
	id, err := GenerateRandomID()
	if err != nil {
		return "", fmt.Errorf("failed to generate ID: %s", err)
	}
	if whisper.HasSymKey(id) {
		return "", fmt.Errorf("failed to generate unique ID")
	}

	// kdf should run no less than 0.1 seconds on an average computer,
	// because it's an once in a session experience
	derived := pbkdf2.Key([]byte(password), nil, 65356, aesKeyLength, sha256.New)
	if err != nil {
		return "", err
	}

	whisper.keyMu.Lock()
	defer whisper.keyMu.Unlock()

	// double check is necessary, because deriveKeyMaterial() is very slow
	if whisper.symKeys[id] != nil {
		return "", fmt.Errorf("critical error: failed to generate unique ID")
	}
	whisper.symKeys[id] = derived
	return id, nil
}

// HasSymKey returns true if there is a key associated with the given id.
// Otherwise returns false.
func (whisper *Whisper) HasSymKey(id string) bool {
	whisper.keyMu.RLock()
	defer whisper.keyMu.RUnlock()
	return whisper.symKeys[id] != nil
}

// DeleteSymKey deletes the key associated with the name string if it exists.
func (whisper *Whisper) DeleteSymKey(id string) bool {
	whisper.keyMu.Lock()
	defer whisper.keyMu.Unlock()
	if whisper.symKeys[id] != nil {
		delete(whisper.symKeys, id)
		return true
	}
	return false
}

// GetSymKey returns the symmetric key associated with the given id.
func (whisper *Whisper) GetSymKey(id string) ([]byte, error) {
	whisper.keyMu.RLock()
	defer whisper.keyMu.RUnlock()
	if whisper.symKeys[id] != nil {
		return whisper.symKeys[id], nil
	}
	return nil, fmt.Errorf("non-existent key ID")
}

// Subscribe installs a new message handler used for filtering, decrypting
// and subsequent storing of incoming messages.
// Subscribe는 필터링을 위한 새로운 새로운 메시지 핸들러를 설치하고
// 수신된 메시지를 복호화하고 저장한다
func (whisper *Whisper) Subscribe(f *Filter) (string, error) {
	s, err := whisper.filters.Install(f)
	if err == nil {
		whisper.updateBloomFilter(f)
	}
	return s, err
}

// updateBloomFilter recalculates the new value of bloom filter,
// and informs the peers if necessary.
// updateBloomFilter는 새로움 블룸필터를 재계산한다
// 필요하다면 피어에게 알린다
func (whisper *Whisper) updateBloomFilter(f *Filter) {
	aggregate := make([]byte, BloomFilterSize)
	for _, t := range f.Topics {
		top := BytesToTopic(t)
		b := TopicToBloom(top)
		aggregate = addBloom(aggregate, b)
	}

	if !BloomFilterMatch(whisper.BloomFilter(), aggregate) {
		// existing bloom filter must be updated
		aggregate = addBloom(whisper.BloomFilter(), aggregate)
		whisper.SetBloomFilter(aggregate)
	}
}

// GetFilter returns the filter by id.
func (whisper *Whisper) GetFilter(id string) *Filter {
	return whisper.filters.Get(id)
}

// Unsubscribe removes an installed message handler.
func (whisper *Whisper) Unsubscribe(id string) error {
	ok := whisper.filters.Uninstall(id)
	if !ok {
		return fmt.Errorf("Unsubscribe: Invalid ID")
	}
	return nil
}

// Send injects a message into the whisper send queue, to be distributed in the
// network in the coming cycles.
// Send는 다음사이클에 네트워크로 전달될 메시지를 send queue로 던진다
func (whisper *Whisper) Send(envelope *Envelope) error {
	ok, err := whisper.add(envelope, false)
	if err == nil && !ok {
		return fmt.Errorf("failed to add envelope")
	}
	return err
}

// Start implements node.Service, starting the background data propagation thread
// of the Whisper protocol.
// Start는 node.Service의 구현이며, Whisper 프로토콜의 백그라운드 데이터 전파 스레드를 시작한다
func (whisper *Whisper) Start(*p2p.Server) error {
	log.Info("started whisper v." + ProtocolVersionStr)
	go whisper.update()

	numCPU := runtime.NumCPU()
	for i := 0; i < numCPU; i++ {
		go whisper.processQueue()
	}

	return nil
}

// Stop implements node.Service, stopping the background data propagation thread
// of the Whisper protocol.
func (whisper *Whisper) Stop() error {
	close(whisper.quit)
	log.Info("whisper stopped")
	return nil
}

// HandlePeer is called by the underlying P2P layer when the whisper sub-protocol
// connection is negotiated.
// HandlePeer는 whisper 서브프로토콜 연결이 협상되었을때 저수준의 p2p 계층에 의해 호출된다
func (whisper *Whisper) HandlePeer(peer *p2p.Peer, rw p2p.MsgReadWriter) error {
	// Create the new peer and start tracking it
	// 새로운 peer를 생성하고 추적을 시작한다
	whisperPeer := newPeer(whisper, peer, rw)

	whisper.peerMu.Lock()
	whisper.peers[whisperPeer] = struct{}{}
	whisper.peerMu.Unlock()

	defer func() {
		whisper.peerMu.Lock()
		delete(whisper.peers, whisperPeer)
		whisper.peerMu.Unlock()
	}()

	// Run the peer handshake and state updates
	// handshake를 실행하고, 상태를 갱신한다
	if err := whisperPeer.handshake(); err != nil {
		return err
	}
	whisperPeer.start()
	defer whisperPeer.stop()

	return whisper.runMessageLoop(whisperPeer, rw)
}

// runMessageLoop reads and processes inbound messages directly to merge into client-global state.
func (whisper *Whisper) runMessageLoop(p *Peer, rw p2p.MsgReadWriter) error {
	for {
		// fetch the next packet
		packet, err := rw.ReadMsg()
		if err != nil {
			log.Warn("message loop", "peer", p.peer.ID(), "err", err)
			return err
		}
		if packet.Size > whisper.MaxMessageSize() {
			log.Warn("oversized message received", "peer", p.peer.ID())
			return errors.New("oversized message received")
		}

		switch packet.Code {
		case statusCode:
			// this should not happen, but no need to panic; just ignore this message.
			log.Warn("unxepected status message received", "peer", p.peer.ID())
		case messagesCode:
			// decode the contained envelopes
			var envelopes []*Envelope
			if err := packet.Decode(&envelopes); err != nil {
				log.Warn("failed to decode envelopes, peer will be disconnected", "peer", p.peer.ID(), "err", err)
				return errors.New("invalid envelopes")
			}

			trouble := false
			for _, env := range envelopes {
				cached, err := whisper.add(env, whisper.lightClient)
				if err != nil {
					trouble = true
					log.Error("bad envelope received, peer will be disconnected", "peer", p.peer.ID(), "err", err)
				}
				if cached {
					p.mark(env)
				}
			}

			if trouble {
				return errors.New("invalid envelope")
			}
		case powRequirementCode:
			s := rlp.NewStream(packet.Payload, uint64(packet.Size))
			i, err := s.Uint()
			if err != nil {
				log.Warn("failed to decode powRequirementCode message, peer will be disconnected", "peer", p.peer.ID(), "err", err)
				return errors.New("invalid powRequirementCode message")
			}
			f := math.Float64frombits(i)
			if math.IsInf(f, 0) || math.IsNaN(f) || f < 0.0 {
				log.Warn("invalid value in powRequirementCode message, peer will be disconnected", "peer", p.peer.ID(), "err", err)
				return errors.New("invalid value in powRequirementCode message")
			}
			p.powRequirement = f
		case bloomFilterExCode:
			var bloom []byte
			err := packet.Decode(&bloom)
			if err == nil && len(bloom) != BloomFilterSize {
				err = fmt.Errorf("wrong bloom filter size %d", len(bloom))
			}

			if err != nil {
				log.Warn("failed to decode bloom filter exchange message, peer will be disconnected", "peer", p.peer.ID(), "err", err)
				return errors.New("invalid bloom filter exchange message")
			}
			p.setBloomFilter(bloom)
		case p2pMessageCode:
			// peer-to-peer message, sent directly to peer bypassing PoW checks, etc.
			// this message is not supposed to be forwarded to other peers, and
			// therefore might not satisfy the PoW, expiry and other requirements.
			// these messages are only accepted from the trusted peer.
			// Pow체크를 건너뛴 피어를 향한 P2P메시지
			// 이 메시지는 다른 피어에게 전달되길 바라지 않으므로 
			// Pow, 초과시간 등의 다른 요구조건을 를 만족시키지 않아도 된다.
			// 메시지는 신뢰된 피어로부터 온것만 수락된다
			if p.trusted {
				var envelope Envelope
				if err := packet.Decode(&envelope); err != nil {
					log.Warn("failed to decode direct message, peer will be disconnected", "peer", p.peer.ID(), "err", err)
					return errors.New("invalid direct message")
				}
				whisper.postEvent(&envelope, true)
			}
		case p2pRequestCode:
			// Must be processed if mail server is implemented. Otherwise ignore.
			if whisper.mailServer != nil {
				var request Envelope
				if err := packet.Decode(&request); err != nil {
					log.Warn("failed to decode p2p request message, peer will be disconnected", "peer", p.peer.ID(), "err", err)
					return errors.New("invalid p2p request")
				}
				whisper.mailServer.DeliverMail(p, &request)
			}
		default:
			// New message types might be implemented in the future versions of Whisper.
			// For forward compatibility, just ignore.
		}

		packet.Discard()
	}
}

// add inserts a new envelope into the message pool to be distributed within the
// whisper network. It also inserts the envelope into the expiration pool at the
// appropriate time-stamp. In case of error, connection should be dropped.
// param isP2P indicates whether the message is peer-to-peer (should not be forwarded).
// add는 whisper네트워크를 통해 분산될 새로운 봉투를 메시지풀에 삽입힌다
// 또한 expiration pool에도 삽입힌다.
// 에러의 경우 연결은 반드시 끊겨야 한다
func (whisper *Whisper) add(envelope *Envelope, isP2P bool) (bool, error) {
	now := uint32(time.Now().Unix())
	sent := envelope.Expiry - envelope.TTL

	if sent > now {
		if sent-DefaultSyncAllowance > now {
			return false, fmt.Errorf("envelope created in the future [%x]", envelope.Hash())
		}
		// recalculate PoW, adjusted for the time difference, plus one second for latency
		envelope.calculatePoW(sent - now + 1)
	}

	if envelope.Expiry < now {
		if envelope.Expiry+DefaultSyncAllowance*2 < now {
			return false, fmt.Errorf("very old message")
		}
		log.Debug("expired envelope dropped", "hash", envelope.Hash().Hex())
		return false, nil // drop envelope without error
	}

	if uint32(envelope.size()) > whisper.MaxMessageSize() {
		return false, fmt.Errorf("huge messages are not allowed [%x]", envelope.Hash())
	}

	if envelope.PoW() < whisper.MinPow() {
		// maybe the value was recently changed, and the peers did not adjust yet.
		// in this case the previous value is retrieved by MinPowTolerance()
		// for a short period of peer synchronization.
		if envelope.PoW() < whisper.MinPowTolerance() {
			return false, fmt.Errorf("envelope with low PoW received: PoW=%f, hash=[%v]", envelope.PoW(), envelope.Hash().Hex())
		}
	}

	if !BloomFilterMatch(whisper.BloomFilter(), envelope.Bloom()) {
		// maybe the value was recently changed, and the peers did not adjust yet.
		// in this case the previous value is retrieved by BloomFilterTolerance()
		// for a short period of peer synchronization.
		if !BloomFilterMatch(whisper.BloomFilterTolerance(), envelope.Bloom()) {
			return false, fmt.Errorf("envelope does not match bloom filter, hash=[%v], bloom: \n%x \n%x \n%x",
				envelope.Hash().Hex(), whisper.BloomFilter(), envelope.Bloom(), envelope.Topic)
		}
	}

	hash := envelope.Hash()

	whisper.poolMu.Lock()
	_, alreadyCached := whisper.envelopes[hash]
	if !alreadyCached {
		whisper.envelopes[hash] = envelope
		if whisper.expirations[envelope.Expiry] == nil {
			whisper.expirations[envelope.Expiry] = set.NewNonTS()
		}
		if !whisper.expirations[envelope.Expiry].Has(hash) {
			whisper.expirations[envelope.Expiry].Add(hash)
		}
	}
	whisper.poolMu.Unlock()

	if alreadyCached {
		log.Trace("whisper envelope already cached", "hash", envelope.Hash().Hex())
	} else {
		log.Trace("cached whisper envelope", "hash", envelope.Hash().Hex())
		whisper.statsMu.Lock()
		whisper.stats.memoryUsed += envelope.size()
		whisper.statsMu.Unlock()
		whisper.postEvent(envelope, isP2P) // notify the local node about the new message
		if whisper.mailServer != nil {
			whisper.mailServer.Archive(envelope)
		}
	}
	return true, nil
}

// postEvent queues the message for further processing.
// postEvent는 추가처리를 위한 메시지를 큐잉한다
func (whisper *Whisper) postEvent(envelope *Envelope, isP2P bool) {
	if isP2P {
		whisper.p2pMsgQueue <- envelope
	} else {
		whisper.checkOverflow()
		whisper.messageQueue <- envelope
	}
}

// checkOverflow checks if message queue overflow occurs and reports it if necessary.
func (whisper *Whisper) checkOverflow() {
	queueSize := len(whisper.messageQueue)

	if queueSize == messageQueueLimit {
		if !whisper.Overflow() {
			whisper.settings.Store(overflowIdx, true)
			log.Warn("message queue overflow")
		}
	} else if queueSize <= messageQueueLimit/2 {
		if whisper.Overflow() {
			whisper.settings.Store(overflowIdx, false)
			log.Warn("message queue overflow fixed (back to normal)")
		}
	}
}

// processQueue delivers the messages to the watchers during the lifetime of the whisper node.
// processQueue는 whiper노드의 생존기간 동안 메시지를 관찰자들에게 전달한다
func (whisper *Whisper) processQueue() {
	var e *Envelope
	for {
		select {
		case <-whisper.quit:
			return

		case e = <-whisper.messageQueue:
			whisper.filters.NotifyWatchers(e, false)

		case e = <-whisper.p2pMsgQueue:
			whisper.filters.NotifyWatchers(e, true)
		}
	}
}

// update loops until the lifetime of the whisper node, updating its internal
// state by expiring stale messages from the pool.
// update는 whisper노드가 생존할때까지 반복하며, 풀에서 오래된 메시지들이 만기될때 내부 상태를 업데이트 한다
func (whisper *Whisper) update() {
	// Start a ticker to check for expirations
	// 만기여부 체크를 위한 티커를 시작한다
	expire := time.NewTicker(expirationCycle)

	// Repeat updates until termination is requested
	// 종료때까지 반복
	for {
		select {
		case <-expire.C:
			whisper.expire()

		case <-whisper.quit:
			return
		}
	}
}

// expire iterates over all the expiration timestamps, removing all stale
// messages from the pools.
// 풀로부터 오래된 메시지들을 제거한다
func (whisper *Whisper) expire() {
	whisper.poolMu.Lock()
	defer whisper.poolMu.Unlock()

	whisper.statsMu.Lock()
	defer whisper.statsMu.Unlock()
	whisper.stats.reset()
	now := uint32(time.Now().Unix())
	for expiry, hashSet := range whisper.expirations {
		if expiry < now {
			// Dump all expired messages and remove timestamp
			hashSet.Each(func(v interface{}) bool {
				sz := whisper.envelopes[v.(common.Hash)].size()
				delete(whisper.envelopes, v.(common.Hash))
				whisper.stats.messagesCleared++
				whisper.stats.memoryCleared += sz
				whisper.stats.memoryUsed -= sz
				return true
			})
			whisper.expirations[expiry].Clear()
			delete(whisper.expirations, expiry)
		}
	}
}

// Stats returns the whisper node statistics.
func (whisper *Whisper) Stats() Statistics {
	whisper.statsMu.Lock()
	defer whisper.statsMu.Unlock()

	return whisper.stats
}

// Envelopes retrieves all the messages currently pooled by the node.
func (whisper *Whisper) Envelopes() []*Envelope {
	whisper.poolMu.RLock()
	defer whisper.poolMu.RUnlock()

	all := make([]*Envelope, 0, len(whisper.envelopes))
	for _, envelope := range whisper.envelopes {
		all = append(all, envelope)
	}
	return all
}

// isEnvelopeCached checks if envelope with specific hash has already been received and cached.
func (whisper *Whisper) isEnvelopeCached(hash common.Hash) bool {
	whisper.poolMu.Lock()
	defer whisper.poolMu.Unlock()

	_, exist := whisper.envelopes[hash]
	return exist
}

// reset resets the node's statistics after each expiry cycle.
func (s *Statistics) reset() {
	s.cycles++
	s.totalMessagesCleared += s.messagesCleared

	s.memoryCleared = 0
	s.messagesCleared = 0
}

// ValidatePublicKey checks the format of the given public key.
func ValidatePublicKey(k *ecdsa.PublicKey) bool {
	return k != nil && k.X != nil && k.Y != nil && k.X.Sign() != 0 && k.Y.Sign() != 0
}

// validatePrivateKey checks the format of the given private key.
func validatePrivateKey(k *ecdsa.PrivateKey) bool {
	if k == nil || k.D == nil || k.D.Sign() == 0 {
		return false
	}
	return ValidatePublicKey(&k.PublicKey)
}

// validateDataIntegrity returns false if the data have the wrong or contains all zeros,
// which is the simplest and the most common bug.
func validateDataIntegrity(k []byte, expectedSize int) bool {
	if len(k) != expectedSize {
		return false
	}
	if expectedSize > 3 && containsOnlyZeros(k) {
		return false
	}
	return true
}

// containsOnlyZeros checks if the data contain only zeros.
func containsOnlyZeros(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

// bytesToUintLittleEndian converts the slice to 64-bit unsigned integer.
func bytesToUintLittleEndian(b []byte) (res uint64) {
	mul := uint64(1)
	for i := 0; i < len(b); i++ {
		res += uint64(b[i]) * mul
		mul *= 256
	}
	return res
}

// BytesToUintBigEndian converts the slice to 64-bit unsigned integer.
func BytesToUintBigEndian(b []byte) (res uint64) {
	for i := 0; i < len(b); i++ {
		res *= 256
		res += uint64(b[i])
	}
	return res
}

// GenerateRandomID generates a random string, which is then returned to be used as a key id
// GenerateRandomID 는 key id로 사용될 랜덤 문자열을 생성한다
func GenerateRandomID() (id string, err error) {
	buf, err := generateSecureRandomData(keyIDSize)
	if err != nil {
		return "", err
	}
	if !validateDataIntegrity(buf, keyIDSize) {
		return "", fmt.Errorf("error in generateRandomID: crypto/rand failed to generate random data")
	}
	id = common.Bytes2Hex(buf)
	return id, err
}

func isFullNode(bloom []byte) bool {
	if bloom == nil {
		return true
	}
	for _, b := range bloom {
		if b != 255 {
			return false
		}
	}
	return true
}

func BloomFilterMatch(filter, sample []byte) bool {
	if filter == nil {
		return true
	}

	for i := 0; i < BloomFilterSize; i++ {
		f := filter[i]
		s := sample[i]
		if (f | s) != f {
			return false
		}
	}

	return true
}

func addBloom(a, b []byte) []byte {
	c := make([]byte, BloomFilterSize)
	for i := 0; i < BloomFilterSize; i++ {
		c[i] = a[i] | b[i]
	}
	return c
}
