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

// Contains all the wrappers from the node package to support client side node
// management on mobile platforms.

package geth

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/ethstats"
	"github.com/ethereum/go-ethereum/internal/debug"
	"github.com/ethereum/go-ethereum/les"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/nat"
	"github.com/ethereum/go-ethereum/params"
	whisper "github.com/ethereum/go-ethereum/whisper/whisperv6"
)

// NodeConfig represents the collection of configuration values to fine tune the Geth
// node embedded into a mobile process. The available values are a subset of the
// entire API provided by go-ethereum to reduce the maintenance surface and dev
// complexity.
// NodeConfig는 게스노드를 모바일 프로세스로 잘 넣기 위한 설정 값의 모임을 나타낸다
// 사용가능 값은 외형 유지및 개발 복잡도를 줄이기 위해 고 이더리움에 의해 재공되는 모든 api의 서브셋이다.

type NodeConfig struct {
	// Bootstrap nodes used to establish connectivity with the rest of the network.
	// 부트스트렙 노드는 네트워크상의 나머지 노드와 연결을 생성하기 위해 사용된다
	BootstrapNodes *Enodes

	// MaxPeers is the maximum number of peers that can be connected. If this is
	// set to zero, then only the configured static and trusted peers can connect.
	// MaxPeers는 연결가능한 최 대피어수이다. 만약 이것이 0으로 설정될 경우 
	// 정적으로 설정되거나, 신뢰된 피어만 연결이 가능하다
	MaxPeers int

	// EthereumEnabled specifies whether the node should run the Ethereum protocol.
	// EthereumEnabled는 노드가 이더리움 프로토콜을 돌려야 하는지를 정의한다
	EthereumEnabled bool

	// EthereumNetworkID is the network identifier used by the Ethereum protocol to
	// decide if remote peers should be accepted or not.
	// EthereumNetworkID는 이더리움 프로토콜이 원격 피어에 대한 
	// 연결여부를 결정하기 위해 사용된다
	EthereumNetworkID int64 // uint64 in truth, but Java can't handle that...

	// EthereumGenesis is the genesis JSON to use to seed the blockchain with. An
	// empty genesis state is equivalent to using the mainnet's state.
	// EthereumGenesisi는 블록체인의 시드로 사용하기 위한 genesis json이다.
	// 빈 제네시스 상태는 메인넷의 상태를 사용하는 것과 같다
	EthereumGenesis string

	// EthereumDatabaseCache is the system memory in MB to allocate for database caching.
	// A minimum of 16MB is always reserved.
	// EthereumDatabaseCache는 시스템 메모리상에 데이터베이스 캐싱을 위한 MB단위의 할당이다
	// 최소 16MB가 항상 예약되어진다
	EthereumDatabaseCache int

	// EthereumNetStats is a netstats connection string to use to report various
	// chain, transaction and node stats to a monitoring server.
	//
	// It has the form "nodename:secret@host:port"
	// EthereumnetStats는 다양한 체인, 트렌젝션, 노드 상태를 
	// 모니터링 서버에 레포팅하기 위한 네트워크 연결 상태 문자열이다 
	// nodename:비밀키@host:port의 형태를 가진다
	EthereumNetStats string

	// WhisperEnabled specifies whether the node should run the Whisper protocol.
	// WhisperEnabled는 노드가 Whisper프로토콜을 실행해야하는지 여부를 정한다
	WhisperEnabled bool

	// Listening address of pprof server.
	// pprof 서버의 수신대기 주소
	PprofAddress string
}

// defaultNodeConfig contains the default node configuration values to use if all
// or some fields are missing from the user's specified list.
// defaultNodeConfig는 만약 몇몇 필드가 유저가 특정한 리스트로부터 빠져있을 경우, 
// 기본 노드의 설정 값을 포함한다.
var defaultNodeConfig = &NodeConfig{
	BootstrapNodes:        FoundationBootnodes(),
	MaxPeers:              25,
	EthereumEnabled:       true,
	EthereumNetworkID:     1,
	EthereumDatabaseCache: 16,
}

// NewNodeConfig creates a new node option set, initialized to the default values.
// NewNodeConfig 함수는 새로운 노드 옵션 세트를 생성하고, 기본값으로 초기화 한다
func NewNodeConfig() *NodeConfig {
	config := *defaultNodeConfig
	return &config
}

// Node represents a Geth Ethereum node instance.
// Node는 게스 이더리움 노드 객체를 나타낸다
type Node struct {
	node *node.Node
}

// NewNode creates and configures a new Geth node.
// NewNode는 새로운 게스노드를 생성하고 설정한다
func NewNode(datadir string, config *NodeConfig) (stack *Node, _ error) {
	// If no or partial configurations were specified, use defaults
	// 특정한 옵션이 설정되어있지 않다면 기본값을 사용
	if config == nil {
		config = NewNodeConfig()
	}
	if config.MaxPeers == 0 {
		config.MaxPeers = defaultNodeConfig.MaxPeers
	}
	if config.BootstrapNodes == nil || config.BootstrapNodes.Size() == 0 {
		config.BootstrapNodes = defaultNodeConfig.BootstrapNodes
	}

	if config.PprofAddress != "" {
		debug.StartPProf(config.PprofAddress)
	}

	// Create the empty networking stack
	//빈 네트워크 스택을 생성한다

	nodeConf := &node.Config{
		Name:        clientIdentifier,
		Version:     params.Version,
		DataDir:     datadir,
		KeyStoreDir: filepath.Join(datadir, "keystore"), // Mobile should never use internal keystores!
	//모바일은 내부 키스토어를 사용하지 말아야한다
		P2P: p2p.Config{
			NoDiscovery:      true,
			DiscoveryV5:      true,
			BootstrapNodesV5: config.BootstrapNodes.nodes,
			ListenAddr:       ":0",
			NAT:              nat.Any(),
			MaxPeers:         config.MaxPeers,
		},
	}
	rawStack, err := node.New(nodeConf)
	if err != nil {
		return nil, err
	}

	debug.Memsize.Add("node", rawStack)

	var genesis *core.Genesis
	if config.EthereumGenesis != "" {
		// Parse the user supplied genesis spec if not mainnet
		// 메인넷이 아니라면 유저가 제공한 제네시스 스펙을 파싱한다
		genesis = new(core.Genesis)
		if err := json.Unmarshal([]byte(config.EthereumGenesis), genesis); err != nil {
			return nil, fmt.Errorf("invalid genesis spec: %v", err)
		}
		// If we have the testnet, hard code the chain configs too
		// 테스트 넷이 있다면 체인을 설정을 하드코딩한다
		if config.EthereumGenesis == TestnetGenesis() {
			genesis.Config = params.TestnetChainConfig
			if config.EthereumNetworkID == 1 {
				config.EthereumNetworkID = 3
			}
		}
	}
	// Register the Ethereum protocol if requested
	// 요구되었다면, 이더리움 프로토콜을 등록한다
	if config.EthereumEnabled {
		ethConf := eth.DefaultConfig
		ethConf.Genesis = genesis
		ethConf.SyncMode = downloader.LightSync
		ethConf.NetworkId = uint64(config.EthereumNetworkID)
		ethConf.DatabaseCache = config.EthereumDatabaseCache
		if err := rawStack.Register(func(ctx *node.ServiceContext) (node.Service, error) {
			return les.New(ctx, &ethConf)
		}); err != nil {
			return nil, fmt.Errorf("ethereum init: %v", err)
		}
		// If netstats reporting is requested, do it
		// 네트워크 상태 보고가 요구되었다면 한다
		if config.EthereumNetStats != "" {
			if err := rawStack.Register(func(ctx *node.ServiceContext) (node.Service, error) {
				var lesServ *les.LightEthereum
				ctx.Service(&lesServ)

				return ethstats.New(config.EthereumNetStats, nil, lesServ)
			}); err != nil {
				return nil, fmt.Errorf("netstats init: %v", err)
			}
		}
	}
	// Register the Whisper protocol if requested
	// whisper프로토콜이 요구되었다면 등록
	if config.WhisperEnabled {
		if err := rawStack.Register(func(*node.ServiceContext) (node.Service, error) {
			return whisper.New(&whisper.DefaultConfig), nil
		}); err != nil {
			return nil, fmt.Errorf("whisper init: %v", err)
		}
	}
	return &Node{rawStack}, nil
}

// Start creates a live P2P node and starts running it.
// 살아있는 p2p 노드를 만들고 실행시킴 
func (n *Node) Start() error {
	return n.node.Start()
}

// Stop terminates a running node along with all it's services. In the node was
// not started, an error is returned.
// Stop은 서비스를 포함한 실행중인 노드를 종료시킨다
// 노드가 실행되지 않은 상황에서는 에러를 리턴한다.
func (n *Node) Stop() error {
	return n.node.Stop()
}

// GetEthereumClient retrieves a client to access the Ethereum subsystem.
// GetEthereumClient는 이더리움 서비스스템에 접근하기 위한 클라이언트를 반환한다
func (n *Node) GetEthereumClient() (client *EthereumClient, _ error) {
	rpc, err := n.node.Attach()
	if err != nil {
		return nil, err
	}
	return &EthereumClient{ethclient.NewClient(rpc)}, nil
}

// GetNodeInfo gathers and returns a collection of metadata known about the host.
// GetNodeInfo 함수는 호스트에 대해 알려진 메타데이터의 콜렉션을 수집하고 반환한다
func (n *Node) GetNodeInfo() *NodeInfo {
	return &NodeInfo{n.node.Server().NodeInfo()}
}

// GetPeersInfo returns an array of metadata objects describing connected peers.
// GetPeersInfo 함수는 연결 피어를 표현하는 메타 오브젝트의 어레이를 반환한다
func (n *Node) GetPeersInfo() *PeerInfos {
	return &PeerInfos{n.node.Server().PeersInfo()}
}
