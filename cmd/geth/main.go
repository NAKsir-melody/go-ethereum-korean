// Copyright 2014 The go-ethereum Authors
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

// geth is the official command-line client for Ethereum.
package main //go 언어에서 main 패키지로 선언하면 실행파일이 생성된다고 한다

import (
	//go 시스템 패키지, 코드에서 사용시 어떤 용도로 쓰이는지 업데이트 하자.
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	//이더리움 내부 패키지
	//다른 폴더의 소스코드를 패키지화해서 사용한다
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/console"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/internal/debug"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/node"

	//gopkg.in은 stable api라는걸 지원하는데, 정확하진 않지만 github에서 버전별로 패키지를 받아 사용할수 있는것 같다
	//urfave/cli 패키지는 command line 기반의 go application을 만들기 위해 사용되는 패키지.
	//지원하는 기능은 콘솔창, 입력된 커맨드 파싱 및 콜백함수, 설정파일 읽어 변수초기화 하기 등등
	"gopkg.in/urfave/cli.v1"
)

const (
	//go에서 상수를 선언할때 괄호로 묶어서 사용한 케이스
	//타입이 명시적이지 않으면 자동으로 타입 추론
	//const clientIdentifier string = "geth" 랑 같음
	clientIdentifier = "geth" // Client identifier to advertise over the network
)

var (
	//go에서 변수를 묶어서 선언함

	// Git SHA1 commit hash of the release (set via linker flags)
	gitCommit = ""
	// The app that holds all commands and flags.
	//utils패키지의 NewApp함수를 사용한다. 해당함수는 utils/flags.go에 선언되어 있음. 리턴값은 cli.App* 이고 내부적으로 cli.NewApp을 호출함
	//cli패키지는 urfave 주석의 내용대로 커맨드라인 app을 만들기 위한 helper
	app = utils.NewApp(gitCommit, "the go-ethereum command line interface")
	// flags that configure the node
	// cli의 Flag type형태 배열을 선언함 
	// 내부적으로는 string, bool, directory 내용들이 섞여 있음
	// util의 flags.go에 실제 cli의 flag type선언 참조
	nodeFlags = []cli.Flag{
		utils.IdentityFlag,
		utils.UnlockedAccountFlag,
		utils.PasswordFileFlag,
		utils.BootnodesFlag,
		utils.BootnodesV4Flag,
		utils.BootnodesV5Flag,
		utils.DataDirFlag,
		utils.KeyStoreDirFlag,
		utils.NoUSBFlag,
		utils.DashboardEnabledFlag,
		utils.DashboardAddrFlag,
		utils.DashboardPortFlag,
		utils.DashboardRefreshFlag,
		utils.EthashCacheDirFlag,
		utils.EthashCachesInMemoryFlag,
		utils.EthashCachesOnDiskFlag,
		utils.EthashDatasetDirFlag,
		utils.EthashDatasetsInMemoryFlag,
		utils.EthashDatasetsOnDiskFlag,
		utils.TxPoolNoLocalsFlag,
		utils.TxPoolJournalFlag,
		utils.TxPoolRejournalFlag,
		utils.TxPoolPriceLimitFlag,
		utils.TxPoolPriceBumpFlag,
		utils.TxPoolAccountSlotsFlag,
		utils.TxPoolGlobalSlotsFlag,
		utils.TxPoolAccountQueueFlag,
		utils.TxPoolGlobalQueueFlag,
		utils.TxPoolLifetimeFlag,
		utils.FastSyncFlag,
		utils.LightModeFlag,
		utils.SyncModeFlag,
		utils.GCModeFlag,
		utils.LightServFlag,
		utils.LightPeersFlag,
		utils.LightKDFFlag,
		utils.CacheFlag,
		utils.CacheDatabaseFlag,
		utils.CacheGCFlag,
		utils.TrieCacheGenFlag,
		utils.ListenPortFlag,
		utils.MaxPeersFlag,
		utils.MaxPendingPeersFlag,
		utils.EtherbaseFlag,
		utils.GasPriceFlag,
		utils.MinerThreadsFlag,
		utils.MiningEnabledFlag,
		utils.TargetGasLimitFlag,
		utils.NATFlag,
		utils.NoDiscoverFlag,
		utils.DiscoveryV5Flag,
		utils.NetrestrictFlag,
		utils.NodeKeyFileFlag,
		utils.NodeKeyHexFlag,
		utils.DeveloperFlag,
		utils.DeveloperPeriodFlag,
		utils.TestnetFlag,
		utils.RinkebyFlag,
		utils.VMEnableDebugFlag,
		utils.NetworkIdFlag,
		utils.RPCCORSDomainFlag,
		utils.RPCVirtualHostsFlag,
		utils.EthStatsURLFlag,
		utils.MetricsEnabledFlag,
		utils.FakePoWFlag,
		utils.NoCompactionFlag,
		utils.GpoBlocksFlag,
		utils.GpoPercentileFlag,
		utils.ExtraDataFlag,
		configFileFlag,
	}

	rpcFlags = []cli.Flag{
		utils.RPCEnabledFlag,
		utils.RPCListenAddrFlag,
		utils.RPCPortFlag,
		utils.RPCApiFlag,
		utils.WSEnabledFlag,
		utils.WSListenAddrFlag,
		utils.WSPortFlag,
		utils.WSApiFlag,
		utils.WSAllowedOriginsFlag,
		utils.IPCDisabledFlag,
		utils.IPCPathFlag,
	}

	whisperFlags = []cli.Flag{
		utils.WhisperEnabledFlag,
		utils.WhisperMaxMessageSizeFlag,
		utils.WhisperMinPOWFlag,
	}
)

func init() {
	// Initialize the CLI app and start Geth

	app.Action = geth //app이 생성될때 불리우는 main함수.
	app.HideVersion = true // we have a command to print the version
	app.Copyright = "Copyright 2013-2018 The go-ethereum Authors"
	app.Commands = []cli.Command{ //app에서 지원할 command들
	//TODO:각 커맨드에 대한 내용 추가
		// See chaincmd.go:
		initCommand,
		importCommand,
		exportCommand,
		importPreimagesCommand,
		exportPreimagesCommand,
		copydbCommand,
		removedbCommand,
		dumpCommand,
		// See monitorcmd.go:
		monitorCommand,
		// See accountcmd.go:
		// 지갑과 계정의 생성, 불러오기, 갱신
		accountCommand,
		walletCommand,
		// See consolecmd.go:
		consoleCommand,
		attachCommand,
		javascriptCommand,
		// See misccmd.go:
		makecacheCommand,
		makedagCommand,
		versionCommand,
		bugCommand,
		licenseCommand,
		// See config.go
		dumpConfigCommand,
	}
	//TODO:정렬을 왜하지?
	sort.Sort(cli.CommandsByName(app.Commands))

	app.Flags = append(app.Flags, nodeFlags...)
	app.Flags = append(app.Flags, rpcFlags...)
	app.Flags = append(app.Flags, consoleFlags...)
	app.Flags = append(app.Flags, debug.Flags...)
	app.Flags = append(app.Flags, whisperFlags...)

	//Before는 sub command가 실행되기 전 불리우는 함수
	//모든 명령어를 실행하기전 카운터와 가스 리밋값을 설정한다.
	app.Before = func(ctx *cli.Context) error {
		//go의 멀티cpu지원 - not multi-thread
		runtime.GOMAXPROCS(runtime.NumCPU())
		if err := debug.Setup(ctx); err != nil {
			return err
		}
		// Start system runtime metrics collection
		//3초간 동작중인 프로세스의 다양한 메트릭을 수집하여
		//Counter나 Gauge형태로 사용하는것 같다
		//TODO:어떤 용도로 쓰는지는 차후확인
		//metics/metrics.go 파일을 참조
		go metrics.CollectProcessMetrics(3 * time.Second)

		//네트워크를 셋업한다
		//TargetGasLimit값을 설정함
		//utils/flags.go
		utils.SetupNetwork(ctx)
		return nil
	}
	//Before는 sub command가 실행된 후 불리우는 함수

	app.After = func(ctx *cli.Context) error {
		debug.Exit()
		console.Stdin.Close() // Resets terminal mode.
		return nil
	}
}

func main() {
	//cli app을 실행한다. 당연히 action에 해당하는 geth함수가 불릴것.
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// geth is the main entry point into the system if no special subcommand is ran.
// It creates a default node based on the command line arguments and runs it in
// blocking mode, waiting for it to be shut down.
func geth(ctx *cli.Context) error {
	//config.go에 정의됨
	//makeFullNode 에선 아래와 같은 순서로 노드를 초기화함
	//makeConfigNode
	// 각 패키지에 디폴트로 설정된 값들을 읽어 설정한다.
	// P2P설정이 완료되고 Account Manager가 설정되고, 이더리움 설정을 가진 노드를 생성함
	// p2p 노드를 만들고 프로토콜을 준비한다.
	// Account Manager 생성
	// 키스토어 folder 생성
	// 키스토어를 새로 만들고, 생성된 wallet만 우선 백엔드에 저장함
	// 현재 백엔드에는 지갑정보만 있고 이벤트 구독정보는 없는 상태로 매니저생성
	// 현재 등록된 백앤드는 지갑이고,각 백앤드들이 매니저를 구독한 상태임
	// 이더리움 관련 설정

	//RegisterEthService - 노드스택에 full 혹은 light ethereum 서비스를 등록
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
	//registerDashboardSurf - 이더리움의 data visualizer인 대시보드 서비스를 붙인다 
	//enableWhisper - 최소 1개의 whisper flag가 있다면 enable 
	// P2P설정이 완료되고 Account Manager가 설정되고, 이더리움 설정을 가진 노드를 생성함
	// 이더리움 관련 서비스를 등록한다
	node := makeFullNode(ctx)
	// 노드 시작
	// 노드가 실행되면 p2p서버를 초기화 하고 
	// 노드에 등록했던 서비스프로토콜을 실행한다
	// RPC실행(admin, debug, web3)
	// 인터럽트를 기다려 node를 종료시키는 스레드 동시실행
	// p2p 서버를 초기화 한다. Node Key와 발견DB를 생성한다
	// p2p 서버 실행, 서버 구조체는 모든 피어들과의 연결을 관리한다
	// 프로토콜들을 모아서 새롭게 모인 서버에서 실행시킴
	// 서버를 시작한다
	// TODO : RLPX, Discovery5, handshake/dial/listen
	// 노드의 서비스를 구현한다. 모니터링/레포팅 데몬을 실행시킨다
	// 데몬은 netstat서버에 접속하려고 노력하고, 체인 이벤트를 레포팅한다.
	// RPC 인터페이스 실행
	// Unlock any account specifically requested
	// keystore를 참조하여 account를 언락한다
	// 등록된 지갑들을 open한다.
	//mining시작
	startNode(ctx, node)

	// 노드종료 핸들러
	node.Wait()
	return nil
}

// startNode boots up the system node and all registered protocols, after which
// it unlocks any requested accounts, and starts the RPC/IPC interfaces and the
// miner.

// 이 함수는 요청된 어카운트들이 언락된 이후에, 
// 시스템 노드와 등록된 모든 프로토콜을 부트업 시키고 
// RPC/IPC 인터페이스와 마이너를 시작한다
func startNode(ctx *cli.Context, stack *node.Node) {
	debug.Memsize.Add("node", stack)

	// Start up the node itself
	// node/node.go
	// 노드가 실행되면 p2p서버를 초기화 하고 
	// 노드에 등록했던 서비스프로토콜을 실행한다
	// RPC실행(admin, debug, web3)
	// 인터럽트를 기다려 node를 종료시키는 스레드 동시실행
	// p2p 서버를 초기화 한다. Node Key와 발견DB를 생성한다
	// p2p 서버 실행, 서버 구조체는 모든 피어들과의 연결을 관리한다
	// 프로토콜들을 모아서 새롭게 모인 서버에서 실행시킴
	// 서버를 시작한다
	// TODO : RLPX, Discovery5, handshake/dial/listen
	// 노드의 서비스를 구현한다. 모니터링/레포팅 데몬을 실행시킨다
	// 데몬은 netstat서버에 접속하려고 노력하고, 체인 이벤트를 레포팅한다.
	// RPC 인터페이스 실행
	utils.StartNode(stack)

	// Unlock any account specifically requested
	// keystore를 참조하여 account를 언락한다
	ks := stack.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)

	passwords := utils.MakePasswordList(ctx)
	unlocks := strings.Split(ctx.GlobalString(utils.UnlockedAccountFlag.Name), ",")
	for i, account := range unlocks {
		if trimmed := strings.TrimSpace(account); trimmed != "" {
			unlockAccount(ctx, ks, trimmed, i, passwords)
		}
	}
	// Register wallet event handlers to open and auto-derive wallets
	// 키스토어 지갑의 추가/삭제에 대한 알람을 받을 채널 
	events := make(chan accounts.WalletEvent, 16)
	stack.AccountManager().Subscribe(events)

	go func() {
		// Create a chain state reader for self-derivation
		// 체인정보를 읽는 rpcClient
		rpcClient, err := stack.Attach()
		if err != nil {
			utils.Fatalf("Failed to attach to self: %v", err)
		}
		stateReader := ethclient.NewClient(rpcClient)

		// Open any wallets already attached
		// 등록된 지갑들을 open한다.
		for _, wallet := range stack.AccountManager().Wallets() {
			if err := wallet.Open(""); err != nil {
				log.Warn("Failed to open wallet", "url", wallet.URL(), "err", err)
			}
		}
		// Listen for wallet event till termination
		// 종료될때까지 지갑 이벤트 수신 대기
		// 채널을 닫지 않기 때문에 range에서 루프 효과가 난다
		for event := range events {
			switch event.Kind {
			case accounts.WalletArrived:
				if err := event.Wallet.Open(""); err != nil {
					log.Warn("New wallet appeared, failed to open", "url", event.Wallet.URL(), "err", err)
				}
			case accounts.WalletOpened:
				status, _ := event.Wallet.Status()
				log.Info("New wallet appeared", "url", event.Wallet.URL(), "status", status)

				if event.Wallet.URL().Scheme == "ledger" {
					event.Wallet.SelfDerive(accounts.DefaultLedgerBaseDerivationPath, stateReader)
				} else {
					event.Wallet.SelfDerive(accounts.DefaultBaseDerivationPath, stateReader)
				}

			case accounts.WalletDropped:
				log.Info("Old wallet dropped", "url", event.Wallet.URL())
				event.Wallet.Close()
			}
		}
	}()
	// Start auxiliary services if enabled
	if ctx.GlobalBool(utils.MiningEnabledFlag.Name) || ctx.GlobalBool(utils.DeveloperFlag.Name) {
		// Mining only makes sense if a full Ethereum node is running
		if ctx.GlobalBool(utils.LightModeFlag.Name) || ctx.GlobalString(utils.SyncModeFlag.Name) == "light" {
			utils.Fatalf("Light clients do not support mining")
		}
		var ethereum *eth.Ethereum
		//  등록되어 동작하는 이더리움 서비스를 검색한다
		if err := stack.Service(&ethereum); err != nil {
			utils.Fatalf("Ethereum service not running: %v", err)
		}
		// Use a reduced number of threads if requested
		if threads := ctx.GlobalInt(utils.MinerThreadsFlag.Name); threads > 0 {
			type threaded interface {
				SetThreads(threads int)
			}
			if th, ok := ethereum.Engine().(threaded); ok {
				th.SetThreads(threads)
			}
		}
		// Set the gas price to the limits from the CLI and start mining
		ethereum.TxPool().SetGasPrice(utils.GlobalBig(ctx, utils.GasPriceFlag.Name))
		//mining시작
		// 이벤트 처리 핸들러를 동작시켜놓는다(대기중)
		// 이후 부터는 내부적으로 commitNewWork를 호출하여 무한루프로 동작
		// 블록 시간 체크(너무 시간이 많이 가지 않도록)
		// 새로운 해더를 생성하고, 해더 번호에 부모+1
		// 헤더가 ethash 프로토콜을 따르도록 난이도 필드를 초기화 한다.
		// 현재 프로세싱이 가능한 트렌젝션을 검색하고 
		// 관련 어카운트별로 그룹핑한 후 논스로 정렬한다.
		//논스 존중 방식으로 가격정렬된 트렌젝션의 세트를 만든다
		// 트렌젝션을 적용하고 트렌젝션과 영수증을 만든다
		// 주어진 스테이트 DB에 트렌젝션을 적용하고,
		// 트렌젝션의 영수증과 가스사용량과 에러상태를 반환하며
		// 트렌젝션이 실패할경우 블록이 검증되지 않았음을 지시한다
		// 펜딩 스테이트 이벤트를 던진다
		// 합의 엔진으로 봉인하기 위한 새 블록을 만든다
		// 블록을 누적하고 엉클 리워드를 하고 최종 상태를 설정하고 블록을 조립한다
		//새로운 작업을 현재 살아있는 마이너 에이전트에게 전달한다
		if err := ethereum.StartMining(true); err != nil {
			utils.Fatalf("Failed to start mining: %v", err)
		}
	}
}
