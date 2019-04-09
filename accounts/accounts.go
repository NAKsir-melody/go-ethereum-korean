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

// Package accounts implements high level Ethereum account management.
// accounts 패키지는 높은수준의 이더리움 어카운트 관리를 구현한다
package accounts

import (
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// Account represents an Ethereum account located at a specific location defined
// by the optional URL field.
// 어카운트 구조체는 URL필드에 정의된 특정위치에 존재하는 이더리움 계정을 나타낸다
type Account struct {
	Address common.Address `json:"address"` // Ethereum account address derived from the key
	URL     URL            `json:"url"`     // Optional resource locator within a backend
}

// Wallet represents a software or hardware wallet that might contain one or more
// accounts (derived from the same seed).
// 지갑인터페이스는 동일한 시드에서 유도된 1개 이상의 어카운트를 포함하는 소프트웨어 또는 하드웨어 지갑을 나타낸다
type Wallet interface {
	// URL retrieves the canonical path under which this wallet is reachable. It is
	// user by upper layers to define a sorting order over all wallets from multiple
	// backends.
	// URL함수는 이 지갑이 접근 가능한 캐노니컬 패스를 반환한다.
	// 이함수는 상위레이어에서 다양한 백엔드들로 부터 온 모든 지갑들의 소팅순서를 결정하기위해 사용된다
	URL() URL

	// Status returns a textual status to aid the user in the current state of the
	// wallet. It also returns an error indicating any failure the wallet might have
	// encountered.
	// 자갑의 현재 상태를 원하는 유저에게 현재 상태를 반환한다
	// 또한 어떤 지갑의 실패상태를 알려주는 에러를 반환하기도 한다
	Status() (string, error)

	// Open initializes access to a wallet instance. It is not meant to unlock or
	// decrypt account keys, rather simply to establish a connection to hardware
	// wallets and/or to access derivation seeds.
	//
	// The passphrase parameter may or may not be used by the implementation of a
	// particular wallet instance. The reason there is no passwordless open method
	// is to strive towards a uniform wallet handling, oblivious to the different
	// backend providers.
	//
	// Please note, if you open a wallet, you must close it to release any allocated
	// resources (especially important when working with hardware wallets).

	// Open 함수는 지갑객체를 접근하기 위한 초기화를 진행한다.
	// 실제로 지갑을 언락하거나 계정키를 비암호화 하는것을 뜻하는것이아니라
	// 단순히 하드웨어 지갑에 접근하는 연결을 생성하거나, 시드에 접근하기 위한 것이다.

	// 암호구문 인자는 특정지갑에서는 사용되지 않을수 있다.
	// 다른 백엔드 제공자들임을 고려할필요 없는 획일적인 지갑 관리를 위해 암호없는 open함수가 없기 때문이다

	// 지갑을 열었을때는 리소스의 해제를 위해 반드시 닫아야 한다
	// (특히 하드웨어 지갑을 이용할 경우 중요하다)
	Open(passphrase string) error

	// Close releases any resources held by an open wallet instance.
	// close는 열린 지갑객체에 의해 잡혀진 모든 리소스를 헤제한다
	Close() error

	// Accounts retrieves the list of signing accounts the wallet is currently aware
	// of. For hierarchical deterministic wallets, the list will not be exhaustive,
	// rather only contain the accounts explicitly pinned during account derivation.
	// 계정 함수는 현재 고려되는 사이닝 어카운트의 리스트를 반환한다
	// 계층적으로 결정되는 지갑들을 위해, 반환되는 리스트는 완벽하지 않은, 
	// 계정유도과정동안 명백해게 고정된 계정만을 포함한다
	Accounts() []Account

	// Contains returns whether an account is part of this particular wallet or not.
	// Contains 함수는 계정이 특정지갑에 포함되는지 여부를 반환한다
	Contains(account Account) bool

	// Derive attempts to explicitly derive a hierarchical deterministic account at
	// the specified derivation path. If requested, the derived account will be added
	// to the wallet's tracked account list.
	// Derive함수는 명백하게 구조적으로 결정된 계정을 특정 패스에 유도한다.
	// 요구된다면, 유도 계정은 지갑의 계정리스트에 추가될수있다
	Derive(path DerivationPath, pin bool) (Account, error)

	// SelfDerive sets a base account derivation path from which the wallet attempts
	// to discover non zero accounts and automatically add them to list of tracked
	// accounts.
	//
	// Note, self derivaton will increment the last component of the specified path
	// opposed to decending into a child path to allow discovering accounts starting
	// from non zero components.
	//
	// You can disable automatic account discovery by calling SelfDerive with a nil
	// chain state reader.
	// SelfDerive 함수는 계정을 찾으려하는 지갑으로 부터 기본 계정 유도주소를 얻어 리스트에 추가하고 관찰한다
	// 자동 계정 발견을 이함수를 nil인자로 호출함으로서 비활성 시킬수 있다
	// 자기주도적 유도는 0이 아닌 부분으로 시작하는것 계정을 발견하는것을 허용하기 위해
	// 자식경로와는 반대로 특정된 경로의 마지막 부분을 증가시킬것이다 
	// 체인리더에 nil 값을 전달함으로서, 자동 계정발견기능을 중지시킬 수 있다

	SelfDerive(base DerivationPath, chain ethereum.ChainStateReader)

	// SignHash requests the wallet to sign the given hash.
	//
	// It looks up the account specified either solely via its address contained within,
	// or optionally with the aid of any location metadata from the embedded URL field.
	//
	// If the wallet requires additional authentication to sign the request (e.g.
	// a password to decrypt the account, or a PIN code o verify the transaction),
	// an AuthNeededError instance will be returned, containing infos for the user
	// about which fields or actions are needed. The user may retry by providing
	// the needed details via SignHashWithPassphrase, or by other means (e.g. unlock
	// the account in a keystore).
	// SignHash함수는 주어진 해시에 사인하도록 지갑에 요청한다.
	// 이 함수는 선택적으로 포함된 url 필드의 메타데이터를 이용하여, 
	// 특정된 어드레스에 대한 유일한 계정을 찾는다
	// 만약 서명요구에 대해 지갑이 추가적인 인증(걔정 해독 암호나 트렌젝션 검증 PIN)을 요구한다면 
	// 관련 추가 동작에 대한 정보를 포함하는 인증에러 객체가 반환될것이다
	// 사용자는 반환된 에러를 활용하여 SighnHashWithPassphrase함수나 
	// 다른 방법(키스토어 파일의 계정 잠금 해제)을 통해 시도 할수 있다
	SignHash(account Account, hash []byte) ([]byte, error)

	// SignTx requests the wallet to sign the given transaction.
	//
	// It looks up the account specified either solely via its address contained within,
	// or optionally with the aid of any location metadata from the embedded URL field.
	//
	// If the wallet requires additional authentication to sign the request (e.g.
	// a password to decrypt the account, or a PIN code o verify the transaction),
	// an AuthNeededError instance will be returned, containing infos for the user
	// about which fields or actions are needed. The user may retry by providing
	// the needed details via SignTxWithPassphrase, or by other means (e.g. unlock
	// the account in a keystore).
	// SignTx 함수는 주어진 트렌젝션의 사인을 지갑에 요구한다
	// 이 함수는 선택적으로 포함된 url 필드의 메타데이터를 이용하여, 
	// 특정된 어드레스에 대한 유일한 계정을 찾는다
	// 만약 서명요구에 대해 지갑이 추가적인 인증(걔정 해독 암호나 트렌젝션 검증 PIN)을 요구한다면 
	// 관련 추가 동작에 대한 정보를 포함하는 인증에러 객체가 반환될것이다
	// 사용자는 반환된 에러를 활용하여 SighnHashWithPassphrase함수나 
	// 다른 방법(키스토어 파일의 계정 잠금 해제)을 통해 시도 할수 있다
	SignTx(account Account, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)

	// SignHashWithPassphrase requests the wallet to sign the given hash with the
	// given passphrase as extra authentication information.
	//
	// It looks up the account specified either solely via its address contained within,
	// or optionally with the aid of any location metadata from the embedded URL field.
	// SignHashWithPassphrase 함수는 지갑에게 주어진 해시를 
	// 추가 인증을 위해 주어진 암호정보를 이용하여 서명하도록 한다
	SignHashWithPassphrase(account Account, passphrase string, hash []byte) ([]byte, error)

	// SignTxWithPassphrase requests the wallet to sign the given transaction, with the
	// given passphrase as extra authentication information.
	//
	// It looks up the account specified either solely via its address contained within,
	// or optionally with the aid of any location metadata from the embedded URL field.
	// SignTxWithPassphrase함수는 지갑에게 주어진 추가 암호정보를 이용하여 서명하도록 요청한다
	SignTxWithPassphrase(account Account, passphrase string, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error)
}

// Backend is a "wallet provider" that may contain a batch of accounts they can
// sign transactions with and upon request, do so.
// 백엔드 인터페이스는 지갑 제공자이며 트렌젝션에 사인이 가능한 많은 어카운트들을 포함할 수 있다
// 지갑과 지갑이벤트 구독기능을 구현한 인터페이스
type Backend interface {
	// Wallets retrieves the list of wallets the backend is currently aware of.
	//
	// The returned wallets are not opened by default. For software HD wallets this
	// means that no base seeds are decrypted, and for hardware wallets that no actual
	// connection is established.
	//
	// The resulting wallet list will be sorted alphabetically based on its internal
	// URL assigned by the backend. Since wallets (especially hardware) may come and
	// go, the same wallet might appear at a different positions in the list during
	// subsequent retrievals.
	// Wallets 함수는 현재 이 인터페이스가 관리하는 지갑리스트를 반환한다
	// 반환된 지갑은 기본적으로 닫힌상태이다. 소프트웨어 지갑에서의 이것이 이미하는 것은
	// 베이스 시드들이 아무것도 해독되지 않은것이고, 
	// 하드웨어 지갑들은 실제 연결이 일어나지 않았다는 것이다
	// 반환된 지갑의 리스트는 backend에 의해 지정된 내부 URL의 알파벳순서로 정렬된다.

	Wallets() []Wallet

	// Subscribe creates an async subscription to receive notifications when the
	// backend detects the arrival or departure of a wallet.
	// 구독함수는 백엔드가 지갑의 도착/떠남을 감지하기 위한 비동기 구독을 생성한다
	Subscribe(sink chan<- WalletEvent) event.Subscription
}

// WalletEventType represents the different event types that can be fired by
// the wallet subscription subsystem.
// 지갑이벤트 타입은 지갑 구독 서브시스템에서 발생할수 있는 이벤트이다
type WalletEventType int

const (
	// WalletArrived is fired when a new wallet is detected either via USB or via
	// a filesystem event in the keystore.
	// 지갑 도착 이벤트는 usb나 키스토어의 파일시스템에 새로운 지갑이 감지되었을때 발생한다
	WalletArrived WalletEventType = iota

	// WalletOpened is fired when a wallet is successfully opened with the purpose
	// of starting any background processes such as automatic key derivation.
	// 지갑 오픈은 자동 유도키 같은 백그라운드 프로세스가 
	// 시작되기위해 성공적으로 열렸을 경우 발생한다
	WalletOpened

	// WalletDropped
	// 지갑이 분리되었음을 알리는 이벤트
	WalletDropped
)

// WalletEvent is an event fired by an account backend when a wallet arrival or
// departure is detected.
// 지갑 이벤트는 지갑이 도착//떠남이 감지되었을때 어카운트 백엔드에 의해 발생한다
type WalletEvent struct {
	Wallet Wallet          // Wallet instance arrived or departed
	Kind   WalletEventType // Event type that happened in the system
}
