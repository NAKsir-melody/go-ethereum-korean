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

// Package p2p implements the Ethereum p2p network protocols.
// p2p패키지는 이더리움 p2p네트워크 프로토콜들을 구현한다
package p2p

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/discover"
	"github.com/ethereum/go-ethereum/p2p/discv5"
	"github.com/ethereum/go-ethereum/p2p/nat"
	"github.com/ethereum/go-ethereum/p2p/netutil"
)

const (
	defaultDialTimeout = 15 * time.Second

	// Connectivity defaults.
	// 연결 기본
	maxActiveDialTasks     = 16
	defaultMaxPendingPeers = 50
	defaultDialRatio       = 3

	// Maximum time allowed for reading a complete message.
	// This is effectively the amount of time a connection can be idle.
	// 완료된 메시지를 읽기위해 허용된 최대 시간
	// 실질적으로는 연결이 유휴상태로 가능한 시간
	frameReadTimeout = 30 * time.Second

	// Maximum amount of time allowed for writing a complete message.
	// 완료된 메시지를 쓰는데 허용된 최대시간
	frameWriteTimeout = 20 * time.Second
)

var errServerStopped = errors.New("server stopped")

// Config holds Server options.
// Config구조체는 서버의 옵션을 저장한다
type Config struct {
	// This field must be set to a valid secp256k1 private key.
	// 이 필드는 유효한 secp256k1 개인키로 설정되어야 한다
	PrivateKey *ecdsa.PrivateKey `toml:"-"`

	// MaxPeers is the maximum number of peers that can be
	// connected. It must be greater than zero.
	// MaxPeers는 연결이 가능한 최대수이다. 0보다 커야한다
	MaxPeers int

	// MaxPendingPeers is the maximum number of peers that can be pending in the
	// handshake phase, counted separately for inbound and outbound connections.
	// Zero defaults to preset values.
	// MaxPendingPeers는 핸드쉐이크 과정에서 대기중일수 있는 
	// 최대 피어수로 in/out 연결을 분리해서 센다
	// 기본값은 0 이다
	MaxPendingPeers int `toml:",omitempty"`

	// DialRatio controls the ratio of inbound to dialed connections.
	// Example: a DialRatio of 2 allows 1/2 of connections to be dialed.
	// Setting DialRatio to zero defaults it to 3.
	// DialRatio는 내부로 다이얼된 연결의 비율을 조절한다
	// 만약 2라면 절반이 dialed될것이고, 0으로 설정하면 3이된다
	DialRatio int `toml:",omitempty"`

	// NoDiscovery can be used to disable the peer discovery mechanism.
	// Disabling is useful for protocol debugging (manual topology).
	// NoDiscovery는 피어 발견 메커니즘을 끄기위해 사용된다
	// 낀다는것은 프로토콜을 디버깅하기에 유용하다(수동 토폴로지)
	NoDiscovery bool

	// DiscoveryV5 specifies whether the the new topic-discovery based V5 discovery
	// protocol should be started or not.
	// DiscoveryV5는 v5디스커버리 프로토콜에 기초한 
	// 새로운 topic-discovery를 시작할것인지 여부이다
	DiscoveryV5 bool `toml:",omitempty"`

	// Name sets the node name of this server.
	// Use common.MakeName to create a name that follows existing conventions.
	// Name은 이 서버의 노드이름을 설정한다.
	// 주어진 명명법을 사용하기 위해 common.MakeName함수를 사용할것
	Name string `toml:"-"`

	// BootstrapNodes are used to establish connectivity
	// with the rest of the network.
	// BootstrapNodes는 네트워크의 나머지와 연결하기 위해 사용된다
	BootstrapNodes []*discover.Node

	// BootstrapNodesV5 are used to establish connectivity
	// with the rest of the network using the V5 discovery
	// protocol.
	// BootstrapNodesV5는 v5 discovery protocol을 사용하는
	// 네트워크의 나머지와 연결하기 위해 사용된다
	BootstrapNodesV5 []*discv5.Node `toml:",omitempty"`

	// Static nodes are used as pre-configured connections which are always
	// maintained and re-connected on disconnects.
	// Static nodes는 언제나 유지되고 재연결되는 이미 설정된 연결들로 사용된다
	StaticNodes []*discover.Node

	// Trusted nodes are used as pre-configured connections which are always
	// allowed to connect, even above the peer limit.
	// Trusted nodes는 피어 리밋을 넘어서더라도 언제난 연결할
	// 이미 설정된 연결이다
	TrustedNodes []*discover.Node

	// Connectivity can be restricted to certain IP networks.
	// If this option is set to a non-nil value, only hosts which match one of the
	// IP networks contained in the list are considered.
	// Connectivity는 특정 ip를 제약할수 있다.
	// 이 옵션이 0이 아닐경우 리스트에 매칭되는 호스트들만 고려된다
	NetRestrict *netutil.Netlist `toml:",omitempty"`

	// NodeDatabase is the path to the database containing the previously seen
	// live nodes in the network.
	// NodeDatabase는 기존에 네트워크에 존재하던 live 노드들을 포함하는 DB의 위치이다 
	NodeDatabase string `toml:",omitempty"`

	// Protocols should contain the protocols supported
	// by the server. Matching protocols are launched for
	// each peer.
	// Protocols는 서버에의해 제공되는 프로토콜들을 포함해야 한다
	// 매칭되는 프로토콜들은 각 피어에서 실행된다
	Protocols []Protocol `toml:"-"`

	// If ListenAddr is set to a non-nil address, the server
	// will listen for incoming connections.
	//
	// If the port is zero, the operating system will pick a port. The
	// ListenAddr field will be updated with the actual address when
	// the server is started.
	// ListenAddr이 설정되어 있다면 서버는 내부로 연결되는 요청을 대기할것이다
	// 만약 포트가 0이라면 OS가 port를 선택한다. 서버가 시작되면
	// ListenAddr필드도 실제주소로 업데이트 된다
	ListenAddr string

	// If set to a non-nil value, the given NAT port mapper
	// is used to make the listening port available to the
	// Internet.
	// 만약 nil이 아니라면 주어진 NAT port mapper가 
	// 네트워크에 연결 가능한 listening port를 생성하기 위해 사용된다
	NAT nat.Interface `toml:",omitempty"`

	// If Dialer is set to a non-nil value, the given Dialer
	// is used to dial outbound peer connections.
	// Dialer가 설정되면 주어진 다이얼러가 
	// 외부 피어 연결을 요청하기 위해 사용된다
	Dialer NodeDialer `toml:"-"`

	// If NoDial is true, the server will not dial any peers.
	// 만약 NoDial이 true라면 서버는 아무피어에게도 dial을 하지 않는다
	NoDial bool `toml:",omitempty"`

	// If EnableMsgEvents is set then the server will emit PeerEvents
	// whenever a message is sent to or received from a peer
	// 만약 EnableMsgEvnets가 설정되면 서버는
	// 메시지가 보내지거나 피어로부터 수신되었을때 이벤트를 내보낸다
	EnableMsgEvents bool

	// Logger is a custom logger to use with the p2p.Server.
	// Logger는 p2p.Server를 위한 커스텀 로거.
	Logger log.Logger `toml:",omitempty"`
}

// Server manages all peer connections.
// 서버 구조체는 모든 피어들과의 연결을 관리한다
type Server struct {
	// Config fields may not be modified while the server is running.
	// Config fields는 서버가 동작하는동안 수정되어서는 안된다
	Config

	// Hooks for testing. These are useful because we can inhibit
	// the whole protocol stack.
	// 테스트를 위한 훅들. 전체 프로토콜 스펙을 제어가능함
	newTransport func(net.Conn) transport
	newPeerHook  func(*Peer)

	lock    sync.Mutex // protects running
	running bool

	ntab         discoverTable
	listener     net.Listener
	ourHandshake *protoHandshake
	lastLookup   time.Time
	DiscV5       *discv5.Network

	// These are for Peers, PeerCount (and nothing else).
	// peer들을 위한 필드들
	peerOp     chan peerOpFunc
	peerOpDone chan struct{}

	quit          chan struct{}
	addstatic     chan *discover.Node
	removestatic  chan *discover.Node
	posthandshake chan *conn
	addpeer       chan *conn
	delpeer       chan peerDrop
	loopWG        sync.WaitGroup // loop, listenLoop
	// 수신대기 루프
	peerFeed      event.Feed
	log           log.Logger
}

type peerOpFunc func(map[discover.NodeID]*Peer)

type peerDrop struct {
	*Peer
	err       error
	requested bool // true if signaled by the peer
	// peer에 의해 시그널 될 경우 참
}

type connFlag int

const (
	dynDialedConn connFlag = 1 << iota
	staticDialedConn
	inboundConn
	trustedConn
)

// conn wraps a network connection with information gathered
// during the two handshakes.
// conn 구조체는 두번의 핸드쉐이크중에 얻어진 정보와 함께 네트워크를 포괄한다
type conn struct {
	fd net.Conn
	transport
	flags connFlag
	cont  chan error      // The run loop uses cont to signal errors to SetupConn.
	// setupConn쪽으로 signal을 계속 보낼 루프
	id    discover.NodeID // valid after the encryption handshake
	// 암호화된 핸드쉐이크 이후에 유효함
	caps  []Cap           // valid after the protocol handshake
	//프로토콜 핸드쉐이크 이후에 유효함
	name  string          // valid after the protocol handshake
	//프로토콜 핸드쉐이크 이후에 유효함
}

type transport interface {
	// The two handshakes.
	// 2번의 핸드쉐이크
	doEncHandshake(prv *ecdsa.PrivateKey, dialDest *discover.Node) (discover.NodeID, error)
	doProtoHandshake(our *protoHandshake) (*protoHandshake, error)
	// The MsgReadWriter can only be used after the encryption
	// handshake has completed. The code uses conn.id to track this
	// by setting it to a non-nil value after the encryption handshake.
	// MsgReadWriter함수는 암호화 핸드쉐이크가 끝난후 사용가능하다
	// 코드는 핸드쉐이크가 끝난후에 nil이 아닌 값으로 설정하여 conn.id를 사용해
	// 이것을 트랙킹한다
	MsgReadWriter
	// transports must provide Close because we use MsgPipe in some of
	// the tests. Closing the actual network connection doesn't do
	// anything in those tests because NsgPipe doesn't use it.
	// transports는 우리가 MsgPipe를 테스트들에서 사용하기 땜누에
	// Close를 반드시 제공해야 한다 실제 네트워크 연결을 닫는것은
	// MSGPipe가 이것을 사용하지 않기 때문에
	// 이러한 테스트에선 아무것도 하지 않는다 
	close(err error)
}

func (c *conn) String() string {
	s := c.flags.String()
	if (c.id != discover.NodeID{}) {
		s += " " + c.id.String()
	}
	s += " " + c.fd.RemoteAddr().String()
	return s
}

func (f connFlag) String() string {
	s := ""
	if f&trustedConn != 0 {
		s += "-trusted"
	}
	if f&dynDialedConn != 0 {
		s += "-dyndial"
	}
	if f&staticDialedConn != 0 {
		s += "-staticdial"
	}
	if f&inboundConn != 0 {
		s += "-inbound"
	}
	if s != "" {
		s = s[1:]
	}
	return s
}

func (c *conn) is(f connFlag) bool {
	return c.flags&f != 0
}

// Peers returns all connected peers.
// Peers함수는 모든 연결된 피어를 반환한다
func (srv *Server) Peers() []*Peer {
	var ps []*Peer
	select {
	// Note: We'd love to put this function into a variable but
	// that seems to cause a weird compiler error in some
	// environments.
	// 이함수를 다양한곳에 넣고싶지만 , 어떤 환경에서는 이상한 컴파일 에러가 난다
	case srv.peerOp <- func(peers map[discover.NodeID]*Peer) {
		for _, p := range peers {
			ps = append(ps, p)
		}
	}:
		<-srv.peerOpDone
	case <-srv.quit:
	}
	return ps
}

// PeerCount returns the number of connected peers.
// PeerCount함수는 연결된 피어의 수를 반환한다
func (srv *Server) PeerCount() int {
	var count int
	select {
	case srv.peerOp <- func(ps map[discover.NodeID]*Peer) { count = len(ps) }:
		<-srv.peerOpDone
	case <-srv.quit:
	}
	return count
}

// AddPeer connects to the given node and maintains the connection until the
// server is shut down. If the connection fails for any reason, the server will
// attempt to reconnect the peer.
// AddPeer 함수는 주어진 노드에 연결하고 서버가 끝날때까지 연결을 유지한다
// 어떤 이유로 커넥션이 종료될 경우 서버는 재연결을 시도한다
func (srv *Server) AddPeer(node *discover.Node) {
	select {
	case srv.addstatic <- node:
	case <-srv.quit:
	}
}

// RemovePeer disconnects from the given node
// RemovePeer는 주어진 노드로부터 연결을 끊는다
func (srv *Server) RemovePeer(node *discover.Node) {
	select {
	case srv.removestatic <- node:
	case <-srv.quit:
	}
}

// SubscribePeers subscribes the given channel to peer events
// SubscribePeers함수는 주어진 체널로 피어이벤트에 대한 구독을 한다
func (srv *Server) SubscribeEvents(ch chan *PeerEvent) event.Subscription {
	return srv.peerFeed.Subscribe(ch)
}

// Self returns the local node's endpoint information.
// Self 함수는 현재 노드의 엔드포인트 정보를 반환한다
func (srv *Server) Self() *discover.Node {
	srv.lock.Lock()
	defer srv.lock.Unlock()

	if !srv.running {
		return &discover.Node{IP: net.ParseIP("0.0.0.0")}
	}
	return srv.makeSelf(srv.listener, srv.ntab)
}

func (srv *Server) makeSelf(listener net.Listener, ntab discoverTable) *discover.Node {
	// If the server's not running, return an empty node.
	// If the node is running but discovery is off, manually assemble the node infos.
	// 만약 서버가 실행중이 아니라면 empty node를 반환하고
	// 서버가 실행중이나 discovery가 꺼져있다면 수동으로 노드정보를 조합한다.
	if ntab == nil {
		// Inbound connections disabled, use zero address.
		// 내부로 들어오는 연결이 꺼져있음. 0주소 사용
		if listener == nil {
			return &discover.Node{IP: net.ParseIP("0.0.0.0"), ID: discover.PubkeyID(&srv.PrivateKey.PublicKey)}
		}
		// Otherwise inject the listener address too
		// 아니라면 리스너의 어드레스를 넣는다
		addr := listener.Addr().(*net.TCPAddr)
		return &discover.Node{
			ID:  discover.PubkeyID(&srv.PrivateKey.PublicKey),
			IP:  addr.IP,
			TCP: uint16(addr.Port),
		}
	}
	// Otherwise return the discovery node.
	// 아니라면 discovery node를 반환한다
	return ntab.Self()
}

// Stop terminates the server and all active peer connections.
// It blocks until all active connections have been closed.
// Stop함수는 서버와 모든 활성화된 피어연결을 종료한다
// 이 함수는 활성화된 연결이 모두 닫힐때까지 대기한다
func (srv *Server) Stop() {
	srv.lock.Lock()
	defer srv.lock.Unlock()
	if !srv.running {
		return
	}
	srv.running = false
	if srv.listener != nil {
		// this unblocks listener Accept
		// listener의 연결허용을 막지 않음
		srv.listener.Close()
	}
	close(srv.quit)
	srv.loopWG.Wait()
}

// sharedUDPConn implements a shared connection. Write sends messages to the underlying connection while read returns
// messages that were found unprocessable and sent to the unhandled channel by the primary listener.
// sharedUDPConn 구조체는 공유된 연결을 구현한다.
// Write는 메시지를 기본 연결들에 보내는 반면, read는 처리 불가능하거나 주 연결 수신자에 의해 
// 처리가 불가능한 메시지들을 반환한다.
type sharedUDPConn struct {
	*net.UDPConn
	unhandled chan discover.ReadPacket
}

// ReadFromUDP implements discv5.conn
// ReadFromUDP는 discv5.conn을 구현한다
func (s *sharedUDPConn) ReadFromUDP(b []byte) (n int, addr *net.UDPAddr, err error) {
	packet, ok := <-s.unhandled
	if !ok {
		return 0, nil, fmt.Errorf("Connection was closed")
	}
	l := len(packet.Data)
	if l > len(b) {
		l = len(b)
	}
	copy(b[:l], packet.Data[:l])
	return l, packet.Addr, nil
}

// Close implements discv5.conn
// Close는 discv5.conn을 구현한다
func (s *sharedUDPConn) Close() error {
	return nil
}

// Start starts running the server.
// Servers can not be re-used after stopping.
// 서버를 시작한다
// 서버들을 정지된 이후 재사용될 수 없다
func (srv *Server) Start() (err error) {
	srv.lock.Lock()
	defer srv.lock.Unlock()
	if srv.running {
		return errors.New("server already running")
	}
	srv.running = true
	srv.log = srv.Config.Logger
	if srv.log == nil {
		srv.log = log.New()
	}
	srv.log.Info("Starting P2P networking")

	// static fields
	// newRLPX
	if srv.PrivateKey == nil {
		return fmt.Errorf("Server.PrivateKey must be set to a non-nil key")
	}
	if srv.newTransport == nil {
		srv.newTransport = newRLPX
	}
	if srv.Dialer == nil {
		srv.Dialer = TCPDialer{&net.Dialer{Timeout: defaultDialTimeout}}
	}
	srv.quit = make(chan struct{})
	srv.addpeer = make(chan *conn)
	srv.delpeer = make(chan peerDrop)
	srv.posthandshake = make(chan *conn)
	srv.addstatic = make(chan *discover.Node)
	srv.removestatic = make(chan *discover.Node)
	srv.peerOp = make(chan peerOpFunc)
	srv.peerOpDone = make(chan struct{})

	var (
		conn      *net.UDPConn
		sconn     *sharedUDPConn
		realaddr  *net.UDPAddr
		unhandled chan discover.ReadPacket
	)

	if !srv.NoDiscovery || srv.DiscoveryV5 {
		addr, err := net.ResolveUDPAddr("udp", srv.ListenAddr)
		if err != nil {
			return err
		}
		conn, err = net.ListenUDP("udp", addr)
		if err != nil {
			return err
		}
		realaddr = conn.LocalAddr().(*net.UDPAddr)
		if srv.NAT != nil {
			if !realaddr.IP.IsLoopback() {
				go nat.Map(srv.NAT, srv.quit, "udp", realaddr.Port, realaddr.Port, "ethereum discovery")
			}
			// TODO: react to external IP changes over time.
			// 시간 초과로 외부 IP가 변경되었을 때 반응
			if ext, err := srv.NAT.ExternalIP(); err == nil {
				realaddr = &net.UDPAddr{IP: ext, Port: realaddr.Port}
			}
		}
	}

	if !srv.NoDiscovery && srv.DiscoveryV5 {
		unhandled = make(chan discover.ReadPacket, 100)
		sconn = &sharedUDPConn{conn, unhandled}
	}

	// node table
	// 노드 테이블
	if !srv.NoDiscovery {
		cfg := discover.Config{
			PrivateKey:   srv.PrivateKey,
			AnnounceAddr: realaddr,
			NodeDBPath:   srv.NodeDatabase,
			NetRestrict:  srv.NetRestrict,
			Bootnodes:    srv.BootstrapNodes,
			Unhandled:    unhandled,
		}
		ntab, err := discover.ListenUDP(conn, cfg)
		if err != nil {
			return err
		}
		srv.ntab = ntab
	}

	if srv.DiscoveryV5 {
		var (
			ntab *discv5.Network
			err  error
		)
		if sconn != nil {
			ntab, err = discv5.ListenUDP(srv.PrivateKey, sconn, realaddr, "", srv.NetRestrict) //srv.NodeDatabase)
		} else {
			ntab, err = discv5.ListenUDP(srv.PrivateKey, conn, realaddr, "", srv.NetRestrict) //srv.NodeDatabase)
		}
		if err != nil {
			return err
		}
		if err := ntab.SetFallbackNodes(srv.BootstrapNodesV5); err != nil {
			return err
		}
		srv.DiscV5 = ntab
	}

	dynPeers := srv.maxDialedConns()
	dialer := newDialState(srv.StaticNodes, srv.BootstrapNodes, srv.ntab, dynPeers, srv.NetRestrict)

	// handshake
	// 핸드쉐이크
	srv.ourHandshake = &protoHandshake{Version: baseProtocolVersion, Name: srv.Name, ID: discover.PubkeyID(&srv.PrivateKey.PublicKey)}
	for _, p := range srv.Protocols {
		srv.ourHandshake.Caps = append(srv.ourHandshake.Caps, p.cap())
	}
	// listen/dial
	// 연결대기 및 연결 요청
	if srv.ListenAddr != "" {
		if err := srv.startListening(); err != nil {
			return err
		}
	}
	if srv.NoDial && srv.ListenAddr == "" {
		srv.log.Warn("P2P server will be useless, neither dialing nor listening")
	}

	srv.loopWG.Add(1)
	go srv.run(dialer)
	srv.running = true
	return nil
}

func (srv *Server) startListening() error {
	// Launch the TCP listener.
	// TCP 수신자를 실행한다
	listener, err := net.Listen("tcp", srv.ListenAddr)
	if err != nil {
		return err
	}
	laddr := listener.Addr().(*net.TCPAddr)
	srv.ListenAddr = laddr.String()
	srv.listener = listener
	srv.loopWG.Add(1)
	go srv.listenLoop()
	// Map the TCP listening port if NAT is configured.
	// 만약 NAT가 설정되었을 경우 TCP listening port를 매핑한다
	if !laddr.IP.IsLoopback() && srv.NAT != nil {
		srv.loopWG.Add(1)
		go func() {
			nat.Map(srv.NAT, srv.quit, "tcp", laddr.Port, laddr.Port, "ethereum p2p")
			srv.loopWG.Done()
		}()
	}
	return nil
}

type dialer interface {
	newTasks(running int, peers map[discover.NodeID]*Peer, now time.Time) []task
	taskDone(task, time.Time)
	addStatic(*discover.Node)
	removeStatic(*discover.Node)
}

func (srv *Server) run(dialstate dialer) {
	defer srv.loopWG.Done()
	var (
		peers        = make(map[discover.NodeID]*Peer)
		inboundCount = 0
		trusted      = make(map[discover.NodeID]bool, len(srv.TrustedNodes))
		taskdone     = make(chan task, maxActiveDialTasks)
		runningTasks []task
		queuedTasks  []task // tasks that can't run yet
		// 아직 실행되지 않은 테스크들
	)
	// Put trusted nodes into a map to speed up checks.
	// Trusted peers are loaded on startup and cannot be
	// modified while the server is running.
	// 스피드 업을 체크하기 위해 신뢰하는 노드들을 맵에 넣는다
	// 신뢰된 피어들은 시작때 로딩되며 서버가 동작하는 동안 수정될 수없다
	for _, n := range srv.TrustedNodes {
		trusted[n.ID] = true
	}

	// removes t from runningTasks
	// 실행중인 테스트에서 t를 제거함
	delTask := func(t task) {
		for i := range runningTasks {
			if runningTasks[i] == t {
				runningTasks = append(runningTasks[:i], runningTasks[i+1:]...)
				break
			}
		}
	}
	// starts until max number of active tasks is satisfied
	// 활성화된 task가 최대값이 되기 전까지 시작한다
	startTasks := func(ts []task) (rest []task) {
		i := 0
		for ; len(runningTasks) < maxActiveDialTasks && i < len(ts); i++ {
			t := ts[i]
			srv.log.Trace("New dial task", "task", t)
			go func() { t.Do(srv); taskdone <- t }()
			runningTasks = append(runningTasks, t)
		}
		return ts[i:]
	}
	scheduleTasks := func() {
		// Start from queue first.
		// Queue를 먼저 시작
		queuedTasks = append(queuedTasks[:0], startTasks(queuedTasks)...)
		// Query dialer for new tasks and start as many as possible now.
		// 새로운 업무를 위해 dialer를 요청하고 최대한 많이 시작한다 
		if len(runningTasks) < maxActiveDialTasks {
			nt := dialstate.newTasks(len(runningTasks)+len(queuedTasks), peers, time.Now())
			queuedTasks = append(queuedTasks, startTasks(nt)...)
		}
	}

running:
	for {
		scheduleTasks()

		select {
		case <-srv.quit:
			// The server was stopped. Run the cleanup logic.
			// 서버가 중지됨. 클린업
			break running
		case n := <-srv.addstatic:
			// This channel is used by AddPeer to add to the
			// ephemeral static peer list. Add it to the dialer,
			// it will keep the node connected.
			// 이 채널은 addpeer가 임시적인 정적 피어리스트를 추가할때
			// 사용된다. 다이얼러에 추가하고 노드가 연결될때까지 유지된다
			srv.log.Debug("Adding static node", "node", n)
			dialstate.addStatic(n)
		case n := <-srv.removestatic:
			// This channel is used by RemovePeer to send a
			// disconnect request to a peer and begin the
			// stop keeping the node connected
			// 이 채널은 RemovePeer에서 연결종료 요청을 피어에게 전달하고
			// node의 연결에 대한 관리를 중지할때 사용된다
			srv.log.Debug("Removing static node", "node", n)
			dialstate.removeStatic(n)
			if p, ok := peers[n.ID]; ok {
				p.Disconnect(DiscRequested)
			}
		case op := <-srv.peerOp:
			// This channel is used by Peers and PeerCount.
			// 피어들과 피어 카운트에의해 사용된다
			op(peers)
			srv.peerOpDone <- struct{}{}
		case t := <-taskdone:
			// A task got done. Tell dialstate about it so it
			// can update its state and remove it from the active
			// tasks list.
			// task가 끝났음. 이것에 대한 dialstate를 알려
			// 이것의 상태를 업데이트 하고 active tasks list
			// 로부터 제거하도록 한다
			srv.log.Trace("Dial task done", "task", t)
			dialstate.taskDone(t, time.Now())
			delTask(t)
		case c := <-srv.posthandshake:
			// A connection has passed the encryption handshake so
			// the remote identity is known (but hasn't been verified yet).
			// 연결이 암호화된 핸드쉐이크로 부터 전달되었으므로
			// 원격신원이  알려짐(아직 검증되진 않음)
			if trusted[c.id] {
				// Ensure that the trusted flag is set before checking against MaxPeers.
				// 신뢰된 플래그가 체크되기전 maxpeer에 대한 체크가 이미 되었는지
				c.flags |= trustedConn
			}
			// TODO: track in-progress inbound node IDs (pre-Peer) to avoid dialing them.
			// 처리중인 연결요청에 dial을 피하기 위해 그들의 node ID를 관리해야 한다
			select {
			case c.cont <- srv.encHandshakeChecks(peers, inboundCount, c):
			case <-srv.quit:
				break running
			}
		case c := <-srv.addpeer:
			// At this point the connection is past the protocol handshake.
			// Its capabilities are known and the remote identity is verified.
			// 이시점에서 연결은 프로토콜 핸드쉐이크 뒤이다
			// 이미 능력은 알려졌고, 원격의 신원을 검증되었다.
			err := srv.protoHandshakeChecks(peers, inboundCount, c)
			if err == nil {
				// The handshakes are done and it passed all checks.
				// 핸드쉐이크는 끝났고 모든 체크가 패스함
				p := newPeer(c, srv.Protocols)
				// If message events are enabled, pass the peerFeed
				// to the peer
				// 만약 메시지 이벤트가 켜져있다면 피어로가는 peerFeed로 전달
				if srv.EnableMsgEvents {
					p.events = &srv.peerFeed
				}
				name := truncateName(c.name)
				srv.log.Debug("Adding p2p peer", "name", name, "addr", c.fd.RemoteAddr(), "peers", len(peers)+1)
				go srv.runPeer(p)
				peers[c.id] = p
				if p.Inbound() {
					inboundCount++
				}
			}
			// The dialer logic relies on the assumption that
			// dial tasks complete after the peer has been added or
			// discarded. Unblock the task last.
			// dialer로직은 피어가 추가되거나 제거된후 완료될것이라는 가정위에 놓여있다
			// 마지막 테스크를 막지않는다
			select {
			case c.cont <- err:
			case <-srv.quit:
				break running
			}
		case pd := <-srv.delpeer:
			// A peer disconnected.
			// 연결이 끊김
			d := common.PrettyDuration(mclock.Now() - pd.created)
			pd.log.Debug("Removing p2p peer", "duration", d, "peers", len(peers)-1, "req", pd.requested, "err", pd.err)
			delete(peers, pd.ID())
			if pd.Inbound() {
				inboundCount--
			}
		}
	}

	srv.log.Trace("P2P networking is spinning down")

	// Terminate discovery. If there is a running lookup it will terminate soon.
	// discovery를 끝난다. 검색중이라면 곧 끝남
	if srv.ntab != nil {
		srv.ntab.Close()
	}
	if srv.DiscV5 != nil {
		srv.DiscV5.Close()
	}
	// Disconnect all peers.
	// 모든 피어와의 연결을 끝낸다
	for _, p := range peers {
		p.Disconnect(DiscQuitting)
	}
	// Wait for peers to shut down. Pending connections and tasks are
	// not handled here and will terminate soon-ish because srv.quit
	// is closed.
	// 피어들을 기다린다. 대기중인 연결과 동작은 여기서 관리되지 않고
	// srv.quit이 닫힐때 곧 끝내질 것이다
	for len(peers) > 0 {
		p := <-srv.delpeer
		p.log.Trace("<-delpeer (spindown)", "remainingTasks", len(runningTasks))
		delete(peers, p.ID())
	}
}

func (srv *Server) protoHandshakeChecks(peers map[discover.NodeID]*Peer, inboundCount int, c *conn) error {
	// Drop connections with no matching protocols.
	// 매칭된 프로토콜이 없을 경우 drop
	if len(srv.Protocols) > 0 && countMatchingProtocols(srv.Protocols, c.caps) == 0 {
		return DiscUselessPeer
	}
	// Repeat the encryption handshake checks because the
	// peer set might have changed between the handshakes.
	// 피어셋이 핸드쉐이크중에 변경되었을수 있기 때문에 암호화된 핸드쉐이크를 반복한다
	return srv.encHandshakeChecks(peers, inboundCount, c)
}

func (srv *Server) encHandshakeChecks(peers map[discover.NodeID]*Peer, inboundCount int, c *conn) error {
	switch {
	case !c.is(trustedConn|staticDialedConn) && len(peers) >= srv.MaxPeers:
		return DiscTooManyPeers
	case !c.is(trustedConn) && c.is(inboundConn) && inboundCount >= srv.maxInboundConns():
		return DiscTooManyPeers
	case peers[c.id] != nil:
		return DiscAlreadyConnected
	case c.id == srv.Self().ID:
		return DiscSelf
	default:
		return nil
	}
}

func (srv *Server) maxInboundConns() int {
	return srv.MaxPeers - srv.maxDialedConns()
}

func (srv *Server) maxDialedConns() int {
	if srv.NoDiscovery || srv.NoDial {
		return 0
	}
	r := srv.DialRatio
	if r == 0 {
		r = defaultDialRatio
	}
	return srv.MaxPeers / r
}

type tempError interface {
	Temporary() bool
}

// listenLoop runs in its own goroutine and accepts
// inbound connections.
// listenLoop는 들어오는 연결에 대한 수락을 고유의 고루틴에서 수행한다
func (srv *Server) listenLoop() {
	defer srv.loopWG.Done()
	srv.log.Info("RLPx listener up", "self", srv.makeSelf(srv.listener, srv.ntab))

	tokens := defaultMaxPendingPeers
	if srv.MaxPendingPeers > 0 {
		tokens = srv.MaxPendingPeers
	}
	slots := make(chan struct{}, tokens)
	for i := 0; i < tokens; i++ {
		slots <- struct{}{}
	}

	for {
		// Wait for a handshake slot before accepting.
		// 수신전 handshake를 위한 슬롯 대기
		<-slots

		var (
			fd  net.Conn
			err error
		)
		for {
			fd, err = srv.listener.Accept()
			if tempErr, ok := err.(tempError); ok && tempErr.Temporary() {
				srv.log.Debug("Temporary read error", "err", err)
				continue
			} else if err != nil {
				srv.log.Debug("Read error", "err", err)
				return
			}
			break
		}

		// Reject connections that do not match NetRestrict.
		// 네트워크 제약에 맞지 않는 연결은 거절한다.
		if srv.NetRestrict != nil {
			if tcp, ok := fd.RemoteAddr().(*net.TCPAddr); ok && !srv.NetRestrict.Contains(tcp.IP) {
				srv.log.Debug("Rejected conn (not whitelisted in NetRestrict)", "addr", fd.RemoteAddr())
				fd.Close()
				slots <- struct{}{}
				continue
			}
		}

		fd = newMeteredConn(fd, true)
		srv.log.Trace("Accepted connection", "addr", fd.RemoteAddr())
		go func() {
			srv.SetupConn(fd, inboundConn, nil)
			slots <- struct{}{}
		}()
	}
}

// SetupConn runs the handshakes and attempts to add the connection
// as a peer. It returns when the connection has been added as a peer
// or the handshakes have failed.
// SetupConn함수는 핸드쉐이크를 수행하고 피어의 연결을 추가한다
// 이 함수는 커넥션이 피어로 추가되거나 핸드쉐이크가 실패했을 경우 리턴한다
func (srv *Server) SetupConn(fd net.Conn, flags connFlag, dialDest *discover.Node) error {
	self := srv.Self()
	if self == nil {
		return errors.New("shutdown")
	}
	c := &conn{fd: fd, transport: srv.newTransport(fd), flags: flags, cont: make(chan error)}
	err := srv.setupConn(c, flags, dialDest)
	if err != nil {
		c.close(err)
		srv.log.Trace("Setting up connection failed", "id", c.id, "err", err)
	}
	return err
}

func (srv *Server) setupConn(c *conn, flags connFlag, dialDest *discover.Node) error {
	// Prevent leftover pending conns from entering the handshake.
	// 핸드쉐이크로 들어간 남은 대기 연결을 방지하기 위함 
	srv.lock.Lock()
	running := srv.running
	srv.lock.Unlock()
	if !running {
		return errServerStopped
	}
	// Run the encryption handshake.
	// 암호화된 핸드쉐이크를 실행한다
	var err error
	if c.id, err = c.doEncHandshake(srv.PrivateKey, dialDest); err != nil {
		srv.log.Trace("Failed RLPx handshake", "addr", c.fd.RemoteAddr(), "conn", c.flags, "err", err)
		return err
	}
	clog := srv.log.New("id", c.id, "addr", c.fd.RemoteAddr(), "conn", c.flags)
	// For dialed connections, check that the remote public key matches.
	// dial된 연결에 대해서 원격 공개키 매칭을 체크한다
	if dialDest != nil && c.id != dialDest.ID {
		clog.Trace("Dialed identity mismatch", "want", c, dialDest.ID)
		return DiscUnexpectedIdentity
	}
	err = srv.checkpoint(c, srv.posthandshake)
	if err != nil {
		clog.Trace("Rejected peer before protocol handshake", "err", err)
		return err
	}
	// Run the protocol handshake
	// 프로토콜 핸드쉐이크를 시작한다
	phs, err := c.doProtoHandshake(srv.ourHandshake)
	if err != nil {
		clog.Trace("Failed proto handshake", "err", err)
		return err
	}
	if phs.ID != c.id {
		clog.Trace("Wrong devp2p handshake identity", "err", phs.ID)
		return DiscUnexpectedIdentity
	}
	c.caps, c.name = phs.Caps, phs.Name
	err = srv.checkpoint(c, srv.addpeer)
	if err != nil {
		clog.Trace("Rejected peer", "err", err)
		return err
	}
	// If the checks completed successfully, runPeer has now been
	// launched by run.
	// 성공적으로 체크를 완료하면 , runPeer는 run에의해 실행되었다
	clog.Trace("connection set up", "inbound", dialDest == nil)
	return nil
}

func truncateName(s string) string {
	if len(s) > 20 {
		return s[:20] + "..."
	}
	return s
}

// checkpoint sends the conn to run, which performs the
// post-handshake checks for the stage (posthandshake, addpeer).
// checkpoint 함수는 스테이지를 위해 포스트 핸드쉐이크를 체크를 수행하기 위해 
// 실행할 커넥션을 전송한다 
func (srv *Server) checkpoint(c *conn, stage chan<- *conn) error {
	select {
	case stage <- c:
	case <-srv.quit:
		return errServerStopped
	}
	select {
	case err := <-c.cont:
		return err
	case <-srv.quit:
		return errServerStopped
	}
}

// runPeer runs in its own goroutine for each peer.
// it waits until the Peer logic returns and removes
// the peer.
// runPeer함수는 각 피어를 위해 고유의 go 루틴에서 실행된다.

func (srv *Server) runPeer(p *Peer) {
	if srv.newPeerHook != nil {
		srv.newPeerHook(p)
	}

	// broadcast peer add
	// peer의 추가를 알린다
	srv.peerFeed.Send(&PeerEvent{
		Type: PeerEventTypeAdd,
		Peer: p.ID(),
	})

	// run the protocol
	// 프로토콜을 실행한다
	remoteRequested, err := p.run()

	// broadcast peer drop
	// peer의 끊김을 알린다
	srv.peerFeed.Send(&PeerEvent{
		Type:  PeerEventTypeDrop,
		Peer:  p.ID(),
		Error: err.Error(),
	})

	// Note: run waits for existing peers to be sent on srv.delpeer
	// before returning, so this send should not select on srv.quit.
	// srv.delpeer로 보내질 존재하는 피어들을 위해 대기를 실행한다
	// 그러므로 이 전송은 srv.quit에서 선택되어서는 안된다
	srv.delpeer <- peerDrop{p, err, remoteRequested}
}

// NodeInfo represents a short summary of the information known about the host.
// NodeInfo 구조체는 호스트에 대한 알려진 정보의 짧은 요약을 나타낸다
type NodeInfo struct {
	ID    string `json:"id"`    // Unique node identifier (also the encryption key)
	// 특별한 노드 구분자(와 암호키)
	Name  string `json:"name"`  // Name of the node, including client type, version, OS, custom data
	// client ype, versin os, 임의 데이터를 포함하는 노드의 이름
	Enode string `json:"enode"` // Enode URL for adding this peer from remote peers
	// 원격 피어들이 이 피어를 추가하기 위한 EnodeURL
	IP    string `json:"ip"`    // IP address of the node
	// 노드의 ip address
	Ports struct {
		Discovery int `json:"discovery"` // UDP listening port for discovery protocol
		// dicovery프로토콜을 위한 UDP 수신대기 포트 
		Listener  int `json:"listener"`  // TCP listening port for RLPx
		// RLPx를 위한 TCP 대기 포트 
	} `json:"ports"`
	ListenAddr string                 `json:"listenAddr"`
	Protocols  map[string]interface{} `json:"protocols"`
}

// NodeInfo gathers and returns a collection of metadata known about the host.
// NodeInfo 함수는 호스트에 대해 알려진 메타데이터를 수집하고 반환한다
func (srv *Server) NodeInfo() *NodeInfo {
	node := srv.Self()

	// Gather and assemble the generic node infos
	// 일반적인 노드의 정보를 모아서 조합한다
	info := &NodeInfo{
		Name:       srv.Name,
		Enode:      node.String(),
		ID:         node.ID.String(),
		IP:         node.IP.String(),
		ListenAddr: srv.ListenAddr,
		Protocols:  make(map[string]interface{}),
	}
	info.Ports.Discovery = int(node.UDP)
	info.Ports.Listener = int(node.TCP)

	// Gather all the running protocol infos (only once per protocol type)
	// 실행중인 프로토콜의 정보를 수집한다(프로토콜 타입당하나)
	for _, proto := range srv.Protocols {
		if _, ok := info.Protocols[proto.Name]; !ok {
			nodeInfo := interface{}("unknown")
			if query := proto.NodeInfo; query != nil {
				nodeInfo = proto.NodeInfo()
			}
			info.Protocols[proto.Name] = nodeInfo
		}
	}
	return info
}

// PeersInfo returns an array of metadata objects describing connected peers.
// PeersInfo 함수는 연결된 퍼어를 나타내는 메타데이터의 어레이를 반환한다
func (srv *Server) PeersInfo() []*PeerInfo {
	// Gather all the generic and sub-protocol specific infos
	// 일반적인 내용과 서브 프로토콜의 정보를 수집한다
	infos := make([]*PeerInfo, 0, srv.PeerCount())
	for _, peer := range srv.Peers() {
		if peer != nil {
			infos = append(infos, peer.Info())
		}
	}
	// Sort the result array alphabetically by node identifier
	// node id로 알파벳순서로 결과를 정렬한다
	for i := 0; i < len(infos); i++ {
		for j := i + 1; j < len(infos); j++ {
			if infos[i].ID > infos[j].ID {
				infos[i], infos[j] = infos[j], infos[i]
			}
		}
	}
	return infos
}
