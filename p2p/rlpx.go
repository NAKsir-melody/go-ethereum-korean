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

package p2p

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/ioutil"
	mrand "math/rand"
	"net"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"github.com/ethereum/go-ethereum/crypto/secp256k1"
	"github.com/ethereum/go-ethereum/crypto/sha3"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/golang/snappy"
)

const (
	maxUint24 = ^uint32(0) >> 8

	sskLen = 16 // ecies.MaxSharedKeyLength(pubKey) / 2
	sigLen = 65 // elliptic S256
	pubLen = 64 // 512 bit pubkey in uncompressed representation without format byte
	shaLen = 32 // hash length (for nonce etc)

	authMsgLen  = sigLen + shaLen + pubLen + shaLen + 1
	authRespLen = pubLen + shaLen + 1

	eciesOverhead = 65 /* pubkey */ + 16 /* IV */ + 32 /* MAC */

	encAuthMsgLen  = authMsgLen + eciesOverhead  // size of encrypted pre-EIP-8 initiator handshake
	encAuthRespLen = authRespLen + eciesOverhead // size of encrypted pre-EIP-8 handshake reply

	// total timeout for encryption handshake and protocol
	// handshake in both directions.
	// 암호화 핸드쉐이크와 프로토콜 핸드 쉐이크의 총 timeout
	handshakeTimeout = 5 * time.Second

	// This is the timeout for sending the disconnect reason.
	// This is shorter than the usual timeout because we don't want
	// to wait if the connection is known to be bad anyway.
	// 접속을 끊기 위한 타임아웃.
	// 일반적인 타임우웃보다 짧은 이유는, bad로 알려지지 않기 위함
	discWriteTimeout = 1 * time.Second
)

// errPlainMessageTooLarge is returned if a decompressed message length exceeds
// the allowed 24 bits (i.e. length >= 16MB).
// errPlainMessageTooLarge함수는 만약 해독된 메시지 길이가 24bit을 초과할 경우 반환된다
// 16MB이상
var errPlainMessageTooLarge = errors.New("message length >= 16MB")

// rlpx is the transport protocol used by actual (non-test) connections.
// It wraps the frame encoder with locks and read/write deadlines.
// rlpx 구조체는 실제 연결에 사용되는 transport 프로토콜이다
// 이것은 lock 과 read/write deadlines와 함께 frame encoder를 포괄한다
type rlpx struct {
	fd net.Conn

	rmu, wmu sync.Mutex
	rw       *rlpxFrameRW //실제 p2p전송이 일어나는 RW
}

func newRLPX(fd net.Conn) transport {
	fd.SetDeadline(time.Now().Add(handshakeTimeout))
	return &rlpx{fd: fd}
}

func (t *rlpx) ReadMsg() (Msg, error) {
	t.rmu.Lock()
	defer t.rmu.Unlock()
	t.fd.SetReadDeadline(time.Now().Add(frameReadTimeout))
	return t.rw.ReadMsg()
}

func (t *rlpx) WriteMsg(msg Msg) error {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	t.fd.SetWriteDeadline(time.Now().Add(frameWriteTimeout))
	return t.rw.WriteMsg(msg)
}

func (t *rlpx) close(err error) {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	// Tell the remote end why we're disconnecting if possible.
	// 우리가 왜 연결을 끊으려고 하는지 원격 쪽에 알려준다
	// 만약 연결이 net.Pipe라면 (메모리 시뮬레이션이라면) hangs이 지속된다.
	// 그러므로 net.Pipe는 write deadline을 구현하지 말아야 한다
	if t.rw != nil {
		if r, ok := err.(DiscReason); ok && r != DiscNetworkError {
			// rlpx tries to send DiscReason to disconnected peer
			// if the connection is net.Pipe (in-memory simulation)
			// it hangs forever, since net.Pipe does not implement
			// a write deadline. Because of this only try to send
			// the disconnect reason message if there is no error.
			// 우리가 왜 연결을 끊으려고 하는지 원격 쪽에 알려준다
			// 만약 연결이 net.Pipe라면 (메모리 시뮬레이션이라면) hangs이 지속된다.
			// 그러므로 net.Pipe는 write deadline을 구현하지 말아야 한다
			if err := t.fd.SetWriteDeadline(time.Now().Add(discWriteTimeout)); err == nil {
				SendItems(t.rw, discMsg, r)
			}
		}
	}
	t.fd.Close()
}

func (t *rlpx) doProtoHandshake(our *protoHandshake) (their *protoHandshake, err error) {
	// Writing our handshake happens concurrently, we prefer
	// returning the handshake read error. If the remote side
	// disconnects us early with a valid reason, we should return it
	// as the error so it can be tracked elsewhere.
	// 우리의 핸드쉐이크를 동시에 쓴다, 우리는 핸드쉐이크 리드 에러를 반환하기 원한다
	// 만약 원격쪽이 정당한 원인에 의해 우리쪽으로의 연결을 끊는다면 error를 리턴하여 트랙킹해야한다
	werr := make(chan error, 1)
	go func() { werr <- Send(t.rw, handshakeMsg, our) }()
	if their, err = readProtocolHandshake(t.rw, our); err != nil {
		<-werr // make sure the write terminates too
		// 쓰기가 종료되었음을 확인
		return nil, err
	}
	if err := <-werr; err != nil {
		return nil, fmt.Errorf("write error: %v", err)
	}
	// If the protocol version supports Snappy encoding, upgrade immediately
	// 프로토콜 버전이 snappy 인코딩을 지원한다면 업그레이드 한다
	t.rw.snappy = their.Version >= snappyProtocolVersion

	return their, nil
}

func readProtocolHandshake(rw MsgReader, our *protoHandshake) (*protoHandshake, error) {
	msg, err := rw.ReadMsg()
	if err != nil {
		return nil, err
	}
	if msg.Size > baseProtocolMaxMsgSize {
		return nil, fmt.Errorf("message too big")
	}
	if msg.Code == discMsg {
		// Disconnect before protocol handshake is valid according to the
		// spec and we send it ourself if the posthanshake checks fail.
		// We can't return the reason directly, though, because it is echoed
		// back otherwise. Wrap it in a string instead.
		// 프로토콜 핸드쉐이크 전의 spec에 의거한 유효한 조기 종료는 
		// 우리 스스로 헨드쉐이크가 실패했음을 알려야한다
		// 스스로 리턴할수 없기때문에 스트링대신에 리즌을 감싼다
		var reason [1]DiscReason
		rlp.Decode(msg.Payload, &reason)
		return nil, reason[0]
	}
	if msg.Code != handshakeMsg {
		return nil, fmt.Errorf("expected handshake, got %x", msg.Code)
	}
	var hs protoHandshake
	if err := msg.Decode(&hs); err != nil {
		return nil, err
	}
	if (hs.ID == discover.NodeID{}) {
		return nil, DiscInvalidIdentity
	}
	return &hs, nil
}

// doEncHandshake runs the protocol handshake using authenticated
// messages. the protocol handshake is the first authenticated message
// and also verifies whether the encryption handshake 'worked' and the
// remote side actually provided the right public key.
// doEncHandShake 함수는 인증메시지를 이용하여 프로토콜 핸드쉐이크를 실행한다
// 프로토콜 핸드쉐이크는 첫번째 인증 메시지 이고 암호화된 핸드쉐이크가 동작했는지와
// 원격측에서 정상적은 공용키를 제공했는지 검증한다
func (t *rlpx) doEncHandshake(prv *ecdsa.PrivateKey, dial *discover.Node) (discover.NodeID, error) {
	var (
		sec secrets
		err error
	)
	if dial == nil {
		sec, err = receiverEncHandshake(t.fd, prv, nil)
	} else {
		sec, err = initiatorEncHandshake(t.fd, prv, dial.ID, nil)
	}
	if err != nil {
		return discover.NodeID{}, err
	}
	t.wmu.Lock()
	t.rw = newRLPXFrameRW(t.fd, sec)
	t.wmu.Unlock()
	return sec.RemoteID, nil
}

// encHandshake contains the state of the encryption handshake.
// encHandshake 구조체는 암호화된 핸드쉐이크의 상태를 포함한다
type encHandshake struct {
	initiator bool
	remoteID  discover.NodeID

	remotePub            *ecies.PublicKey  // remote-pubk
	initNonce, respNonce []byte            // nonce
	randomPrivKey        *ecies.PrivateKey // ecdhe-random
	remoteRandomPub      *ecies.PublicKey  // ecdhe-random-pubk
	// 원격 퍼블릭키
	// 논스
	// 랜덤
	// 랜덤 퍼블릭키
}

// secrets represents the connection secrets
// which are negotiated during the encryption handshake.
// secrets 구조체는 암호화된 핸드쉐이크 동안 협의된 연결비밀을 나타낸다
type secrets struct {
	RemoteID              discover.NodeID
	AES, MAC              []byte
	EgressMAC, IngressMAC hash.Hash
	Token                 []byte
}

// RLPx v4 handshake auth (defined in EIP-8).
// RLPx v4 핸드쉐이크 인증
type authMsgV4 struct {
	gotPlain bool // whether read packet had plain format.
	// 읽혀진 패킷이 플래인 포멧인지

	Signature       [sigLen]byte
	InitiatorPubkey [pubLen]byte
	Nonce           [shaLen]byte
	Version         uint

	// Ignore additional fields (forward-compatibility)
	// 향후 호환을 위함. 추가 필드는 무시
	Rest []rlp.RawValue `rlp:"tail"`
}

// RLPx v4 handshake response (defined in EIP-8).
// RLPx v4 핸드쉐이크 응답
type authRespV4 struct {
	RandomPubkey [pubLen]byte
	Nonce        [shaLen]byte
	Version      uint

	// Ignore additional fields (forward-compatibility)
	// 향후 호환을 위함. 추가 필드는 무시
	Rest []rlp.RawValue `rlp:"tail"`
}

// secrets is called after the handshake is completed.
// It extracts the connection secrets from the handshake values.
// secrets 함수는 핸드쉐이크가 완료된 후 불린다.
// 이 함수는 핸드쉐이크 결과로부터 연결비밀을 풀어낸다
func (h *encHandshake) secrets(auth, authResp []byte) (secrets, error) {
	ecdheSecret, err := h.randomPrivKey.GenerateShared(h.remoteRandomPub, sskLen, sskLen)
	if err != nil {
		return secrets{}, err
	}

	// derive base secrets from ephemeral key agreement
	// 임시적인 키 동의로 부터 기본 비밀을 유도한다
	sharedSecret := crypto.Keccak256(ecdheSecret, crypto.Keccak256(h.respNonce, h.initNonce))
	aesSecret := crypto.Keccak256(ecdheSecret, sharedSecret)
	s := secrets{
		RemoteID: h.remoteID,
		AES:      aesSecret,
		MAC:      crypto.Keccak256(ecdheSecret, aesSecret),
	}

	// setup sha3 instances for the MACs
	// MAC을 위한 sha3 인스턴스를 설정한다
	mac1 := sha3.NewKeccak256()
	mac1.Write(xor(s.MAC, h.respNonce))
	mac1.Write(auth)
	mac2 := sha3.NewKeccak256()
	mac2.Write(xor(s.MAC, h.initNonce))
	mac2.Write(authResp)
	if h.initiator {
		s.EgressMAC, s.IngressMAC = mac1, mac2
	} else {
		s.EgressMAC, s.IngressMAC = mac2, mac1
	}

	return s, nil
}

// staticSharedSecret returns the static shared secret, the result
// of key agreement between the local and remote static node key.
// staticSharedSecret 함수는 정적으로 공유된 비밀인 
// 로컬과 원격 정적 노드 키 사이의 키 동의 결과를 반환한다
func (h *encHandshake) staticSharedSecret(prv *ecdsa.PrivateKey) ([]byte, error) {
	return ecies.ImportECDSA(prv).GenerateShared(h.remotePub, sskLen, sskLen)
}

// initiatorEncHandshake negotiates a session token on conn.
// it should be called on the dialing side of the connection.
//
// prv is the local client's private key.
// initiatorEncHandshake 함수는 연결에서 세션토큰을 협상한다
// 이함수는 연결의 dialing 쪽에서 호출되어야 한다
// prv는 로컬 클라이언트의 개인키이다
func initiatorEncHandshake(conn io.ReadWriter, prv *ecdsa.PrivateKey, remoteID discover.NodeID, token []byte) (s secrets, err error) {
	h := &encHandshake{initiator: true, remoteID: remoteID}
	authMsg, err := h.makeAuthMsg(prv, token)
	if err != nil {
		return s, err
	}
	authPacket, err := sealEIP8(authMsg, h)
	if err != nil {
		return s, err
	}
	if _, err = conn.Write(authPacket); err != nil {
		return s, err
	}

	authRespMsg := new(authRespV4)
	authRespPacket, err := readHandshakeMsg(authRespMsg, encAuthRespLen, prv, conn)
	if err != nil {
		return s, err
	}
	if err := h.handleAuthResp(authRespMsg); err != nil {
		return s, err
	}
	return h.secrets(authPacket, authRespPacket)
}

// makeAuthMsg creates the initiator handshake message.
// makeAuthMsg 함수는 핸드쉐이크 메시지의 개시인을 생성한다
func (h *encHandshake) makeAuthMsg(prv *ecdsa.PrivateKey, token []byte) (*authMsgV4, error) {
	rpub, err := h.remoteID.Pubkey()
	if err != nil {
		return nil, fmt.Errorf("bad remoteID: %v", err)
	}
	h.remotePub = ecies.ImportECDSAPublic(rpub)
	// Generate random initiator nonce.
	// 랜덤한 개시인의 논스를 생성한다
	h.initNonce = make([]byte, shaLen)
	if _, err := rand.Read(h.initNonce); err != nil {
		return nil, err
	}
	// Generate random keypair to for ECDH.
	// ECDH를 위한 랜덤한 키패어를 생성한다
	h.randomPrivKey, err = ecies.GenerateKey(rand.Reader, crypto.S256(), nil)
	if err != nil {
		return nil, err
	}

	// Sign known message: static-shared-secret ^ nonce
	// 알려진 메시지로 사인한다
	token, err = h.staticSharedSecret(prv)
	if err != nil {
		return nil, err
	}
	signed := xor(token, h.initNonce)
	signature, err := crypto.Sign(signed, h.randomPrivKey.ExportECDSA())
	if err != nil {
		return nil, err
	}

	msg := new(authMsgV4)
	copy(msg.Signature[:], signature)
	copy(msg.InitiatorPubkey[:], crypto.FromECDSAPub(&prv.PublicKey)[1:])
	copy(msg.Nonce[:], h.initNonce)
	msg.Version = 4
	return msg, nil
}

func (h *encHandshake) handleAuthResp(msg *authRespV4) (err error) {
	h.respNonce = msg.Nonce[:]
	h.remoteRandomPub, err = importPublicKey(msg.RandomPubkey[:])
	return err
}

// receiverEncHandshake negotiates a session token on conn.
// it should be called on the listening side of the connection.
//
// prv is the local client's private key.
// token is the token from a previous session with this node.
// receiverEncHandshake는 conn상에서 세션 토큰을 협상한다
// 이함수는 연결의 수신쪽에서 호출되야 한다
// prv는 로컬 클라이언트의 프라이빗 키이다
// 토큰은 이 노드와의 과거 세션으로부터의 토큰이다
func receiverEncHandshake(conn io.ReadWriter, prv *ecdsa.PrivateKey, token []byte) (s secrets, err error) {
	authMsg := new(authMsgV4)
	authPacket, err := readHandshakeMsg(authMsg, encAuthMsgLen, prv, conn)
	if err != nil {
		return s, err
	}
	h := new(encHandshake)
	if err := h.handleAuthMsg(authMsg, prv); err != nil {
		return s, err
	}

	authRespMsg, err := h.makeAuthResp()
	if err != nil {
		return s, err
	}
	var authRespPacket []byte
	if authMsg.gotPlain {
		authRespPacket, err = authRespMsg.sealPlain(h)
	} else {
		authRespPacket, err = sealEIP8(authRespMsg, h)
	}
	if err != nil {
		return s, err
	}
	if _, err = conn.Write(authRespPacket); err != nil {
		return s, err
	}
	return h.secrets(authPacket, authRespPacket)
}

func (h *encHandshake) handleAuthMsg(msg *authMsgV4, prv *ecdsa.PrivateKey) error {
	// Import the remote identity.
	// 외부 ID를 입수한다
	h.initNonce = msg.Nonce[:]
	h.remoteID = msg.InitiatorPubkey
	rpub, err := h.remoteID.Pubkey()
	if err != nil {
		return fmt.Errorf("bad remoteID: %#v", err)
	}
	h.remotePub = ecies.ImportECDSAPublic(rpub)

	// Generate random keypair for ECDH.
	// If a private key is already set, use it instead of generating one (for testing).
	// ECDH를 위한 랜덤 키패어를 생성한다
	// 만약 개인키가 이미 설정되면 생성된것대신 이것을 쓴다(테스팅용)
	if h.randomPrivKey == nil {
		h.randomPrivKey, err = ecies.GenerateKey(rand.Reader, crypto.S256(), nil)
		if err != nil {
			return err
		}
	}

	// Check the signature.
	// 서명을 확인한다
	token, err := h.staticSharedSecret(prv)
	if err != nil {
		return err
	}
	signedMsg := xor(token, h.initNonce)
	remoteRandomPub, err := secp256k1.RecoverPubkey(signedMsg, msg.Signature[:])
	if err != nil {
		return err
	}
	h.remoteRandomPub, _ = importPublicKey(remoteRandomPub)
	return nil
}

func (h *encHandshake) makeAuthResp() (msg *authRespV4, err error) {
	// Generate random nonce.
	// 랜덤 논스를 생성한다
	h.respNonce = make([]byte, shaLen)
	if _, err = rand.Read(h.respNonce); err != nil {
		return nil, err
	}

	msg = new(authRespV4)
	copy(msg.Nonce[:], h.respNonce)
	copy(msg.RandomPubkey[:], exportPubkey(&h.randomPrivKey.PublicKey))
	msg.Version = 4
	return msg, nil
}

func (msg *authMsgV4) sealPlain(h *encHandshake) ([]byte, error) {
	buf := make([]byte, authMsgLen)
	n := copy(buf, msg.Signature[:])
	n += copy(buf[n:], crypto.Keccak256(exportPubkey(&h.randomPrivKey.PublicKey)))
	n += copy(buf[n:], msg.InitiatorPubkey[:])
	n += copy(buf[n:], msg.Nonce[:])
	buf[n] = 0 // token-flag
	return ecies.Encrypt(rand.Reader, h.remotePub, buf, nil, nil)
}

func (msg *authMsgV4) decodePlain(input []byte) {
	n := copy(msg.Signature[:], input)
	n += shaLen // skip sha3(initiator-ephemeral-pubk)
	n += copy(msg.InitiatorPubkey[:], input[n:])
	copy(msg.Nonce[:], input[n:])
	msg.Version = 4
	msg.gotPlain = true
}

func (msg *authRespV4) sealPlain(hs *encHandshake) ([]byte, error) {
	buf := make([]byte, authRespLen)
	n := copy(buf, msg.RandomPubkey[:])
	copy(buf[n:], msg.Nonce[:])
	return ecies.Encrypt(rand.Reader, hs.remotePub, buf, nil, nil)
}

func (msg *authRespV4) decodePlain(input []byte) {
	n := copy(msg.RandomPubkey[:], input)
	copy(msg.Nonce[:], input[n:])
	msg.Version = 4
}

var padSpace = make([]byte, 300)

func sealEIP8(msg interface{}, h *encHandshake) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := rlp.Encode(buf, msg); err != nil {
		return nil, err
	}
	// pad with random amount of data. the amount needs to be at least 100 bytes to make
	// the message distinguishable from pre-EIP-8 handshakes.
	// random량의 데이터를 패딩한다. preEIP-8 핸드쉐이크와 구별하기 위해 최소한 100 바이트가 필요하다
	pad := padSpace[:mrand.Intn(len(padSpace)-100)+100]
	buf.Write(pad)
	prefix := make([]byte, 2)
	binary.BigEndian.PutUint16(prefix, uint16(buf.Len()+eciesOverhead))

	enc, err := ecies.Encrypt(rand.Reader, h.remotePub, buf.Bytes(), nil, prefix)
	return append(prefix, enc...), err
}

type plainDecoder interface {
	decodePlain([]byte)
}

func readHandshakeMsg(msg plainDecoder, plainSize int, prv *ecdsa.PrivateKey, r io.Reader) ([]byte, error) {
	buf := make([]byte, plainSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return buf, err
	}
	// Attempt decoding pre-EIP-8 "plain" format.
	// pre EIP8의 순수한 포멧을 디코딩한다
	key := ecies.ImportECDSA(prv)
	if dec, err := key.Decrypt(buf, nil, nil); err == nil {
		msg.decodePlain(dec)
		return buf, nil
	}
	// Could be EIP-8 format, try that.
	// EIP-8포멧일수 있으니 시도
	prefix := buf[:2]
	size := binary.BigEndian.Uint16(prefix)
	if size < uint16(plainSize) {
		return buf, fmt.Errorf("size underflow, need at least %d bytes", plainSize)
	}
	buf = append(buf, make([]byte, size-uint16(plainSize)+2)...)
	if _, err := io.ReadFull(r, buf[plainSize:]); err != nil {
		return buf, err
	}
	dec, err := key.Decrypt(buf[2:], nil, prefix)
	if err != nil {
		return buf, err
	}
	// Can't use rlp.DecodeBytes here because it rejects
	// trailing data (forward-compatibility).
	// 따라오는 데이터를 거절하기 때문에 rlpDecodeBytes를 사용할수 없다
	s := rlp.NewStream(bytes.NewReader(dec), 0)
	return buf, s.Decode(msg)
}

// importPublicKey unmarshals 512 bit public keys.
// importPublicKey 함수는 512bit 공개키를 언마샬링 한다
func importPublicKey(pubKey []byte) (*ecies.PublicKey, error) {
	var pubKey65 []byte
	switch len(pubKey) {
	case 64:
		// add 'uncompressed key' flag
		// 압축해제 플래그 추가
		pubKey65 = append([]byte{0x04}, pubKey...)
	case 65:
		pubKey65 = pubKey
	default:
		return nil, fmt.Errorf("invalid public key length %v (expect 64/65)", len(pubKey))
	}
	// TODO: fewer pointless conversions
	pub := crypto.ToECDSAPub(pubKey65)
	if pub.X == nil {
		return nil, fmt.Errorf("invalid public key")
	}
	return ecies.ImportECDSAPublic(pub), nil
}

func exportPubkey(pub *ecies.PublicKey) []byte {
	if pub == nil {
		panic("nil pubkey")
	}
	return elliptic.Marshal(pub.Curve, pub.X, pub.Y)[1:]
}

func xor(one, other []byte) (xor []byte) {
	xor = make([]byte, len(one))
	for i := 0; i < len(one); i++ {
		xor[i] = one[i] ^ other[i]
	}
	return xor
}

var (
	// this is used in place of actual frame header data.
	// TODO: replace this when Msg contains the protocol type code.
	// 실제 프레임 더데 데이타대신 사용됨
	// 프로토콜 타입 코드를 포함한 메시지일때는 대체
	zeroHeader = []byte{0xC2, 0x80, 0x80}
	// sixteen zero bytes
	// 16개의 제로 바이트
	zero16 = make([]byte, 16)
)

// rlpxFrameRW implements a simplified version of RLPx framing.
// chunked messages are not supported and all headers are equal to
// zeroHeader.
//
// rlpxFrameRW is not safe for concurrent use from multiple goroutines.
// rlpxFraemRW 구조체는 RLPx프레임의 단순 버전을 구현한다
// 덩어리 메시지들은 지원되지 않으며 모든 헤더는 제로 헤더와 같다
// rlpxFraemRW는 병렬성을 보장하지 않는다
type rlpxFrameRW struct {
	conn io.ReadWriter
	enc  cipher.Stream
	dec  cipher.Stream

	macCipher  cipher.Block
	egressMAC  hash.Hash
	ingressMAC hash.Hash

	snappy bool
}

func newRLPXFrameRW(conn io.ReadWriter, s secrets) *rlpxFrameRW {
	macc, err := aes.NewCipher(s.MAC)
	if err != nil {
		panic("invalid MAC secret: " + err.Error())
	}
	encc, err := aes.NewCipher(s.AES)
	if err != nil {
		panic("invalid AES secret: " + err.Error())
	}
	// we use an all-zeroes IV for AES because the key used
	// for encryption is ephemeral.
	// 인코딩에 사용하는 키가 임시적이므로 우리는 모드 제로인 IV를 AES에서 사용할 것이다 
	iv := make([]byte, encc.BlockSize())
	return &rlpxFrameRW{
		conn:       conn,
		enc:        cipher.NewCTR(encc, iv),
		dec:        cipher.NewCTR(encc, iv),
		macCipher:  macc,
		egressMAC:  s.EgressMAC,
		ingressMAC: s.IngressMAC,
	}
}

func (rw *rlpxFrameRW) WriteMsg(msg Msg) error {
	ptype, _ := rlp.EncodeToBytes(msg.Code)

	// if snappy is enabled, compress message now
	// snappy가 켜져있다면 메시지를 압축한다
	if rw.snappy {
		if msg.Size > maxUint24 {
			return errPlainMessageTooLarge
		}
		payload, _ := ioutil.ReadAll(msg.Payload)
		payload = snappy.Encode(nil, payload)

		msg.Payload = bytes.NewReader(payload)
		msg.Size = uint32(len(payload))
	}
	// write header
	// 헤더를 쓴다
	headbuf := make([]byte, 32)
	fsize := uint32(len(ptype)) + msg.Size
	if fsize > maxUint24 {
		return errors.New("message size overflows uint24")
	}
	putInt24(fsize, headbuf) // TODO: check overflow
	// 오버플로우 체크
	copy(headbuf[3:], zeroHeader)
	rw.enc.XORKeyStream(headbuf[:16], headbuf[:16]) // first half is now encrypted
	// 첫번째 절반이 암호화됨

	// write header MAC
	// MAC 헤더를 쓴다
	copy(headbuf[16:], updateMAC(rw.egressMAC, rw.macCipher, headbuf[:16]))
	if _, err := rw.conn.Write(headbuf); err != nil {
		return err
	}

	// write encrypted frame, updating the egress MAC hash with
	// the data written to conn.
	// 암호회된 프레임을 쓰고, 외부로 나가는 MAC hash를 data와 함께 업데이트한다
	tee := cipher.StreamWriter{S: rw.enc, W: io.MultiWriter(rw.conn, rw.egressMAC)}
	if _, err := tee.Write(ptype); err != nil {
		return err
	}
	if _, err := io.Copy(tee, msg.Payload); err != nil {
		return err
	}
	if padding := fsize % 16; padding > 0 {
		if _, err := tee.Write(zero16[:16-padding]); err != nil {
			return err
		}
	}

	// write frame MAC. egress MAC hash is up to date because
	// frame content was written to it as well.
	// frame MAC을 쓴다. 외부로 나가는 MAC 해시는 프레임 컨텐츠가 써졌기 때문에 갱신되었다
	fmacseed := rw.egressMAC.Sum(nil)
	mac := updateMAC(rw.egressMAC, rw.macCipher, fmacseed)
	_, err := rw.conn.Write(mac)
	return err
}

func (rw *rlpxFrameRW) ReadMsg() (msg Msg, err error) {
	// read the header
	// 헤더를 읽는다
	headbuf := make([]byte, 32)
	if _, err := io.ReadFull(rw.conn, headbuf); err != nil {
		return msg, err
	}
	// verify header mac
	// 헤더 MAC을 검증한다
	shouldMAC := updateMAC(rw.ingressMAC, rw.macCipher, headbuf[:16])
	if !hmac.Equal(shouldMAC, headbuf[16:]) {
		return msg, errors.New("bad header MAC")
	}
	rw.dec.XORKeyStream(headbuf[:16], headbuf[:16]) // first half is now decrypted
	// 첫번째 절반이 해독됨
	fsize := readInt24(headbuf)
	// ignore protocol type for now
	// 지금부터는 프로토콜 타입을 무시한다

	// read the frame content
	// frame내용을 읽는다
	var rsize = fsize // frame size rounded up to 16 byte boundary
	// frame size는 16바이트 단위로 반올림됨
	if padding := fsize % 16; padding > 0 {
		rsize += 16 - padding
	}
	framebuf := make([]byte, rsize)
	if _, err := io.ReadFull(rw.conn, framebuf); err != nil {
		return msg, err
	}

	// read and validate frame MAC. we can re-use headbuf for that.
	// frameMAC를 읽고 검증한다. 우리는 헤더 버퍼를 재사용할 수 있다
	rw.ingressMAC.Write(framebuf)
	fmacseed := rw.ingressMAC.Sum(nil)
	if _, err := io.ReadFull(rw.conn, headbuf[:16]); err != nil {
		return msg, err
	}
	shouldMAC = updateMAC(rw.ingressMAC, rw.macCipher, fmacseed)
	if !hmac.Equal(shouldMAC, headbuf[:16]) {
		return msg, errors.New("bad frame MAC")
	}

	// decrypt frame content
	// frame 내용을 해독한다
	rw.dec.XORKeyStream(framebuf, framebuf)

	// decode message code
	// 메시지코드를 디코딩한다
	content := bytes.NewReader(framebuf[:fsize])
	if err := rlp.Decode(content, &msg.Code); err != nil {
		return msg, err
	}
	msg.Size = uint32(content.Len())
	msg.Payload = content

	// if snappy is enabled, verify and decompress message
	// 스내피가 켜져있따면 검증하고 해독한다
	if rw.snappy {
		payload, err := ioutil.ReadAll(msg.Payload)
		if err != nil {
			return msg, err
		}
		size, err := snappy.DecodedLen(payload)
		if err != nil {
			return msg, err
		}
		if size > int(maxUint24) {
			return msg, errPlainMessageTooLarge
		}
		payload, err = snappy.Decode(nil, payload)
		if err != nil {
			return msg, err
		}
		msg.Size, msg.Payload = uint32(size), bytes.NewReader(payload)
	}
	return msg, nil
}

// updateMAC reseeds the given hash with encrypted seed.
// it returns the first 16 bytes of the hash sum after seeding.
// updateMAC함수는 주어진 해시를 암호화된 시드로 재 시드한다
// 이함수는 해시 섬의 첫 16바이트를 리턴한다
func updateMAC(mac hash.Hash, block cipher.Block, seed []byte) []byte {
	aesbuf := make([]byte, aes.BlockSize)
	block.Encrypt(aesbuf, mac.Sum(nil))
	for i := range aesbuf {
		aesbuf[i] ^= seed[i]
	}
	mac.Write(aesbuf)
	return mac.Sum(nil)[:16]
}

func readInt24(b []byte) uint32 {
	return uint32(b[2]) | uint32(b[1])<<8 | uint32(b[0])<<16
}

func putInt24(v uint32, b []byte) {
	b[0] = byte(v >> 16)
	b[1] = byte(v >> 8)
	b[2] = byte(v)
}
