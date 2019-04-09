// Copyright 2017 The go-ethereum Authors
// This file is part of go-ethereum.
//
// go-ethereum is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// go-ethereum is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with go-ethereum. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"unicode"

	cli "gopkg.in/urfave/cli.v1"

	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/dashboard"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/params"
	whisper "github.com/ethereum/go-ethereum/whisper/whisperv6"
	"github.com/naoina/toml"
)

var (
	dumpConfigCommand = cli.Command{
		Action:      utils.MigrateFlags(dumpConfig),
		Name:        "dumpconfig",
		Usage:       "Show configuration values",
		ArgsUsage:   "",
		Flags:       append(append(nodeFlags, rpcFlags...), whisperFlags...),
		Category:    "MISCELLANEOUS COMMANDS",
		Description: `The dumpconfig command shows configuration values.`,
	}

	configFileFlag = cli.StringFlag{
		Name:  "config",
		Usage: "TOML configuration file",
	}
)

// These settings ensure that TOML keys use the same names as Go struct fields.
var tomlSettings = toml.Config{
	NormFieldName: func(rt reflect.Type, key string) string {
		return key
	},
	FieldToKey: func(rt reflect.Type, field string) string {
		return field
	},
	MissingField: func(rt reflect.Type, field string) error {
		link := ""
		if unicode.IsUpper(rune(rt.Name()[0])) && rt.PkgPath() != "main" {
			link = fmt.Sprintf(", see https://godoc.org/%s#%s for available fields", rt.PkgPath(), rt.Name())
		}
		return fmt.Errorf("field '%s' is not defined in %s%s", field, rt.String(), link)
	},
}

type ethstatsConfig struct {
	URL string `toml:",omitempty"`
}

type gethConfig struct {
	Eth       eth.Config
	Shh       whisper.Config
	Node      node.Config
	Ethstats  ethstatsConfig
	Dashboard dashboard.Config
}

func loadConfig(file string, cfg *gethConfig) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	err = tomlSettings.NewDecoder(bufio.NewReader(f)).Decode(cfg)
	// Add file name to errors that have a line number.
	if _, ok := err.(*toml.LineError); ok {
		err = errors.New(file + ", " + err.Error())
	}
	return err
}

// client 식별자
// http 모듈에 eth/shh 추가
// Websocket 모듈에 eth/shh 추가
// DefaultConfig contains reasonable default settings.
// data 위치설정
// http 포트 설정
// http 모듈에 net, web3 설정
// websocket 모듈에 net,web3설정
// p2p 설정: max peer는 25개
// any 함수는 로컬 네트워크의 지원가능한 메카니즘(UPNP or NAT-pmp) 을 발견하려 노력하는 포트 맵퍼를 리턴한다
func defaultNodeConfig() node.Config {
	cfg := node.DefaultConfig
	cfg.Name = clientIdentifier
	cfg.Version = params.VersionWithCommit(gitCommit)
	cfg.HTTPModules = append(cfg.HTTPModules, "eth", "shh")
	cfg.WSModules = append(cfg.WSModules, "eth", "shh")
	cfg.IPCPath = "geth.ipc"
	return cfg
}

// account manager 만 설정된 노드가 리턴
func makeConfigNode(ctx *cli.Context) (*node.Node, gethConfig) {
	// Load defaults.
	cfg := gethConfig{
// fast sync모드
// txPool도 기본설정사용
// 가스 신탁 설정
		Eth:       eth.DefaultConfig,
		Shh:       whisper.DefaultConfig,
		//바로 위 함수
		Node:      defaultNodeConfig(),
		Dashboard: dashboard.DefaultConfig,
	}

	// Load config file.
	//설정파일이 있다면 읽어서 각 설정을 업데이트한다
	if file := ctx.GlobalString(configFileFlag.Name); file != "" {
		if err := loadConfig(file, &cfg); err != nil {
			utils.Fatalf("%v", err)
		}
	}

	// Apply flags.
	utils.SetNodeConfig(ctx, &cfg.Node)
	// p2p 노드를 만들고 프로토콜을 준비한다.
	// account manager 만 설정된 노드가 리턴
	stack, err := node.New(&cfg.Node)
	if err != nil {
		utils.Fatalf("Failed to create the protocol stack: %v", err)
	}
	//이더리움 관련 설정
	utils.SetEthConfig(ctx, stack, &cfg.Eth)
	if ctx.GlobalIsSet(utils.EthStatsURLFlag.Name) {
		cfg.Ethstats.URL = ctx.GlobalString(utils.EthStatsURLFlag.Name)
	}

	utils.SetShhConfig(ctx, stack, &cfg.Shh)
	utils.SetDashboardConfig(ctx, &cfg.Dashboard)

	return stack, cfg
}

// enableWhisper returns true in case one of the whisper flags is set.
func enableWhisper(ctx *cli.Context) bool {
	for _, flag := range whisperFlags {
		if ctx.GlobalIsSet(flag.GetName()) {
			return true
		}
	}
	return false
}

func makeFullNode(ctx *cli.Context) *node.Node {
	// 각 패키지에 디폴트로 설정된 값들을 읽어 설정한다.
	// P2P설정이 완료되고 Account Manager가 설정되고, 이더리움 설정을 가진 노드를 생성함
	// p2p 노드를 만들고 프로토콜을 준비한다.
	// Account Manager 생성
	//키스토어 folder 생성
	//키스토어를 새로 만들고, 생성된 wallet만 우선 백엔드에 저장함
	// 현재 백엔드에는 지갑정보만 있고 이벤트 구독정보는 없는 상태로 매니저생성
	// 현재 등록된 백앤드는 지갑이고,각 백앤드들이 매니저를 구독한 상태임
	//이더리움 관련 설정

	stack, cfg := makeConfigNode(ctx)

	// 이더리움 관련 서비스를 등록한다
	/*
	geth 바이너리가 실행되면서 노드를 생성후 초기화 하는데,
	노드객체는 기본적으로 서비스들이 등록되는 컨테이너 객체이기 때문에
	ethereum 프로토콜도 역시 서비스 추가 된다.
	초기화가 완료된 노드가 start하면서 본인에게 등록된 모든 서비스를 시작하게 된다.
	이때 config파일의 sync 모드에 따라 Light Sync인지 아닌지(Full sync)를 구별한다.
	*/
	// 이함수는 새로운 이더리움 오브젝트를 생성하고 초기화한다
	// 체인DB(Level DB)를 생성하거나 이미 존재한다면 접속, 메인넷 genesis 블럭을 사용 
	// DB의 정보를 이용해서 완전히 초기화된 블록체인을 리턴한다.
	// 이더리움의 기본 검증자와 처리자를 초기화한다
	// DB로부터 마지막으로 알려진 state를 읽어온다.
	// 메인 계정 trie를 오픈하고 stateDB를 생성한다
	// 현재 블록과 현재 블록헤더를 설정하고 tota difficulty를 계산한다
	// 5초마다 퓨처블록들을 체인에 추가하는 루틴 실행
	// TX pool 생성 & 체인 헤드 이벤트 구독 	
	// 이더리움 서브프로토콜: 네트워크에서 동작가능한 피어들을 관리  
	// 해쉬나 블록을 원격피어로 부터 가져오는 다운로더를 만든다
	// Qos 튜너는 산발적으로 피어들의 지연속도를 모아 예측시간을 업데이트 한다
	// statefetcher는 피어 일동의 active state 동기화 및 요청 수락을 관리한다
	// 해쉬 어나운스먼트를 베이스로 블록을 검색하는 블록패쳐를 만든다
	// 다운로더 이벤트를 트랙킹한다. (한번짜리)
	// 체인이 sync되었는지, 실패했는지 확인한다.
	// 가스 오라클 생성
	utils.RegisterEthService(stack, &cfg.Eth)

	if ctx.GlobalBool(utils.DashboardEnabledFlag.Name) {
		utils.RegisterDashboardService(stack, &cfg.Dashboard, gitCommit)
	}
	// Whisper must be explicitly enabled by specifying at least 1 whisper flag or in dev mode
	shhEnabled := enableWhisper(ctx)
	shhAutoEnabled := !ctx.GlobalIsSet(utils.WhisperEnabledFlag.Name) && ctx.GlobalIsSet(utils.DeveloperFlag.Name)
	if shhEnabled || shhAutoEnabled {
		if ctx.GlobalIsSet(utils.WhisperMaxMessageSizeFlag.Name) {
			cfg.Shh.MaxMessageSize = uint32(ctx.Int(utils.WhisperMaxMessageSizeFlag.Name))
		}
		if ctx.GlobalIsSet(utils.WhisperMinPOWFlag.Name) {
			cfg.Shh.MinimumAcceptedPOW = ctx.Float64(utils.WhisperMinPOWFlag.Name)
		}
		utils.RegisterShhService(stack, &cfg.Shh)
	}

	// Add the Ethereum Stats daemon if requested.
	if cfg.Ethstats.URL != "" {
		utils.RegisterEthStatsService(stack, cfg.Ethstats.URL)
	}
	return stack
}

// dumpConfig is the dumpconfig command.
func dumpConfig(ctx *cli.Context) error {
	_, cfg := makeConfigNode(ctx)
	comment := ""

	if cfg.Eth.Genesis != nil {
		cfg.Eth.Genesis = nil
		comment += "# Note: this config doesn't contain the genesis block.\n\n"
	}

	out, err := tomlSettings.Marshal(&cfg)
	if err != nil {
		return err
	}
	io.WriteString(os.Stdout, comment)
	os.Stdout.Write(out)
	return nil
}
