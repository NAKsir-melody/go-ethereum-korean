
# Go Ethereum - Korean 

이더리움 한글 주석 프로젝트입니다.

v0.2 현재 126개 파일에 약 1000줄의 주석이 한글로 변환되었습니다. 

동작 분석 결과는 https://steemit.com/@sigmoid를 통해 업데이트 중입니다.


## Packages or binaries.
### Accounts: 계정과 키스토어, 계약계정, 지갑관련 기능
 * package accounts:높은 수준의 이더리움 계정 관리를 구현한다 
 	* package abi: 계약 컨택스트와 호출 가능한메소드에 대한 정보
 		* package bind: 이더리움 ABI를 Go Native dap으로 바인딩
 			* package backends: 테스트를 위해 가상 블록체인을 생성하여 계약 바인딩을 테스트 할수 있게 한다
 	* package keystore: secp256k1 개인키의 암호화된 저장소를 구현
 	* package usbwallet: 하드웨어 지갑 지원
 		* package trezor: trezor wallet
![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmeUEKp6onHu5ZpG3Yj44fa2g4T6T7kbmRsruxSqoUZpAa/image.png)
---
### bmt
 * package bmt: 바이너리 머클트리를 구현한다
---
### cmd: geth 포함, go-ethereum에서 지원하는 실행 가능한 바이너리들
 * abigen: abi generator
 * bootnode: 이더리움 discovery protocol을 위한 부트스트랩 노드
 * clef: geth의 계정 관리 기능을 대체 가능한 트렌젝션과 데이터에 대한 사이닝에 사용가능
 * ethkey: ethkey는 이더리움 키파일 제어를 위한 cli 툴이다
 * evm:  EVM 코드조각을 실행해 볼수있다
 * faucet: 라이트 클라이언트에 의해 지원되는 이더리움 수도꼭지
 * geth: geth
 * internal/browser: 유저의 브라우저와 상호연동 가능한 기능을 제공
 * p2psim: http api를 시뮬레이션 할수 있는 커맨드라인 클라이언트
 * puppeth: puppeth는 프라이빗 네트워크를 조합하고 유지하기 위한 명령이다
 * rlpdump: rlpdump는 RLP데이터의 예쁜 프린터이다
 * swarm: 분산저장소
 * utils
 * wnode: 단순 메신져 노드
---
### common: solidity 컴파일러 등 공통으로 쓰이는 패키지들
 * package common
 	* package compiler: 솔리디티 컴파일러
 	* package bitutil: bitwise op
 	* package fdlimit
	* package hexutil: 이더리움 RPC API가 JSON의 바이너리 데이터를 해석하기 위해 사용하는 0x prefix인코딩
	* package math: 수학적 기능 지원
	* package mclock: monotonic clock source
	* package number
---
### consensus: 합의구현부: POA, POW 마이닝, DAO & Fork
 * package consensus: implements different Ethereum consensus engines.
 	* package clique: PoA
 	* package ethash: PoW
 	* package misc: DAO & Fork
![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmRnE2veagyFNtMYWBToD7oVheh1VRBtLs3yoUA9ZkJ3mR/image.png)
---
### console
 * package console: RPC 클라이언트를 이용해 노드에 연결되는 자바스크립트 런타임 환경
---
### contract: 스마트 계약
 * package chequebook: chequeubook smartcontract
 	* package contract
 * package ens: Ethereum Name Service
 	* package contract
---
### core: 이더리움 코어. 상태, DB, EVM
 * package core: 코어패키지는 이더리움 합의 프로토콜을 구현한다
 	* package asm: lexer(sourcecode parsing), token(interperted by compiler). compiler
 	* package bloombits: 블룸 필터링
 	* package rawdb:저수준 DB 접근자 (read, write,delete)
 	* package state: 이더리움 상태 트라이의 최상위 캐싱 레이어
	* package types: 이더리움 함의와 관련된 데이터 타입들(block, bloom9, log, receipt, transaciton)
![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmcWwL9V94xtqQA3TpAJbeuusNw25C1nZ3Lbm7cPENVEcj/image.png)
 	* package vm: 이더리움 버추얼 머신, 바이트 코드 버추얼 머신. 바이트 코드 버추얼 머신은 바이트 셋을 반복하며 황서기준의 룰에 따라 실행한다
 		* package runtime: EVM코드를 실행하기 위한 기본 실행 모델

| | |
| --- | --- |
| block validator | 블록헤더와 엉클을 검증하고 스테이트를 처리 |
|blockchain     |  제네시스 블록을 가진 주어진 DB의 캐노니컬 체인과 체인의 수신/되돌리기/재구성 작업을 관리|
|blocks          | 하드포크에 의한 bad hash들을 관리함 |
|chain_indexer   | 체인 인덱서 백엔드는 체인조각를 백그라운드로 처리하고, 조각의 결과를 DB에 쓰기위해 위한 방법들을 정의한다. 블룸필터나 CHT를 생성하는데 이용될 수 있다.|
|chain_maker     | mining을 하지 않아도 블록을 생성할 수 있어 다양한 테스트 패키지에서 블록을 생성할때 사용함|
|events          |  NewTxsEvent<br>PendingLogsEvent<br>PendingStateEvent<br> NewMinedBlockEvent<br>RemovedLogsEvent<br>ChainEvent<br>ChainSideEvent<br>ChainHeadEvent 
|evm             | evm에서 사용할 체인컨텍스트와 합의 인자들을 블록체인으로 부터 생성하고 처리한다(잔고확인 및 전송) |
|gaspool         |  가스풀은 블록상의 트렌젝션이 실행되는동안 가용 가능한 가스의상태를 관찰한다 | 
|genesis         | 제네시스 블록과 하드포크 변환블록을 정의|
|state_processer | 상태 처리자는 기본 처리자로서, 한 지점에서 다른 지점으로의 state의 변환을 관리한다 |
|state_transition| 상태 전환은 현재 월드 상태에 대해 하나의 트렌젝션이 적용되었을 경우 발생하며 상태 전한 모델은 새로운 상태 루트를 만들기 위한 다음과 같은 일을 한다<br>  1. 논스 핸들링 <br> 2. 프리 가스 페이 <br> 3. 영수증에 대한 새로운 스테이트 오브젝트 <br>4. 가치 전송 <br> 만약 계약의 생성이라면  <br> 4-a) 트렌젝션을 실행하고<br>4-b) 제대로 실행되었을 경우 새로운 스테이트에 대한 코드로서 결과를 사용한다 <br> 스크립트 섹션을 실행하고 새로운 상태 루트를 유도한다|
|tx_journal      |  txJournal 구조체는 로컬에 저장하는 것을 노려 생성된 트렌젝션들중 실행되지 않은 것들이 노드의 재시작에도 살아남는 것을 허용하기 위한 순환로그 이며 로컬에서 발생한 트렌젝션이 아직 실행되지 않은 상태에서 노드를 재시작할때 정보가 손실되는것을 막는 일을 함|
|tx_list         | txList는 하나의 어카운트에 속하는 트렌젝션의 리스트이며, 어카운트 논스에 의해 정렬된다. 펜딩큐와 실행큐양쪽에서 연속된 트렌젝션을 저장하기 위해 사용된다 |
|tx_pool         |  트렌젝션 풀은 현재까지 알려진 모든 트렌젝션을 포함한다. 네트워크를 통해 수신되거나, 로컬하게 생성된 트렌젝션이 풀에 들어가게 된다. 트렌젝션이 블록체인에 포함되면, 풀에서 나가게 된다. 풀은 현재 상태에 적용가능한 처리가능 트렌젝션과 퓨처트렌젝션으로 나뉜다. |

---
### crypto: 암호화 관련
 * package crypto
 	* package b256
 	* package ecies
 	* package randentropy
 	* package secp256k1
 	* package sha3
 	
| | |
| --- | --- |
| Keccak512 | account 생성 - 주소  |
| ECDSA    | account 생성 - 공개키 |
---
### dashboard: 이더리움 대시보드
 * package dashboard: geth에 통합된 데이터 시각화 기능. 이더리움 노드의 정보를 제공한다
---
### eth: 이더리움 프로토콜
 * package eth: 이더리움 프로토콜을 구현한다 
 	* package downloader: 수동 full 체인 동기화
 	* package fetcher: 동기화를 기반으로한 블록 알람
 	* package filters: 외부 클라이언트에게 이더리움 프로토콜에 관련된 블록, 트렌젝션, 로그등 다양한 정보를 반환하기 위한 이더리움 필터링 시스템(RPC API)
 	* package gasprice: 가스 오라클 및 블록 가격
 	* package tracers: 자바스크립트 트렌젝션 추적자의 모임
 	* 
|  |  |
| --- | --- |
|config  | 이더리움 기본설정 <br> sync모드 <br> txPool <br> 가스 Oracle <br> Ethash <br> |
|sync    |   * 새로운 피어가 나타나면 현재까지 펜딩된 트렌젝션을 릴레이 한다, 네트워크 밴드위스 관리를 위해 각 피어에 트렌젝션을 쪼개서 보낸다 <br>* 주기적으로 네트워크와 동기화 하고, 해시와 블록을 다운로드한다|
|protocol | 이더리움 프로토콜의 버전과 메시지를 정의한다 |
|handler | 프로토콜 매니저 생성 <br> * 트렌젝션을 브로드 캐스팅한다<br>* 마이닝된 블럭을 브로드캐스팅한다<br>* 새로운 피어가 나타나면 현재까지 펜딩된 트렌젝션을 릴레이 한다, 네트워크 밴드위스 관리를 위해 각 피어에 트렌젝션을 쪼개서 보낸다<br>* 주기적으로 네트워크와 동기화 하고, 해시와 블록을 다운로드한다<br>* Qos 튜너는 산발적으로 피어들의 지연속도를 모아 예측시간을 업데이트 한다<br>* statefetcher는 피어 일동의 active state 동기화 및 요청 수락을 관리한다<br>* 해쉬 어나운스먼트를 베이스로 블록을 검색하는 블록패쳐를 만든다|
---
### ethclient
 * package ethclient: 이더리움 RPC API를 사용하기 위한 클라이언트 제공
---
### ethdb
 * package ethdb: LevelDB를 생성하고 db의 동작 카운터를 metrix시스템에 반환한다
---
### ethstats
 * package ethstats: 네트워크 상태보고 서비스 구현
---
### event
 * package event: 구독 기반의 실시간 이벤트 관리
 	* package filter: 이벤트의 필터
---
### internal 
 * package build
 * package cmdtest
 * package debug
 * package ethapi: 일반적인 이더리움 API함수를 구현
 * package guide
 * package jsre: 자바스크립트 실행환경을 제공
 * package web3ext: geth 특화된 web3.js 익스텐션을 제공
---
### les
 * package les: 이더리움 라이트 서브 프로토콜을 구현함
	 * package flowcontrol: 클라이언트의 flow control 매커니즘을 구현함
---
### light
 * package light: 이더리움 라이트 클라이언트의 상태 및 체인오브젝트 반환기능을 구현
---
### log
 * package log: 로그레벨 trace를 구현함
 	* package term: 각 OS 지원을 위한 터미널
 ---
### miner
 * package miner: 블록을 생성하고 합의엔진의 sealer인 ethash로 블록을 전달하여 논스를 찾도록 한다

   1. 블록 시간 체크(너무 시간이 많이 가지 않도록)
	2. 새로운 해더를 생성하고, 해더 번호에 부모+1
	3. 헤더가 ethash 프로토콜을 따르도록 난이도 필드를 초기화 한다.
	4. 현재 프로세싱이 가능한 트렌젝션을 검색하고, 관련 어카운트별로 그룹핑한 후 논스로 정렬한다.
	5.  트렌젝션을 적용하고 트렌젝션과 영수증을 만든다
주어진 스테이트 DB에 트렌젝션을 적용하고, 트렌젝션의 영수증 생성과 가스사용량 확인
	6. 엉클블록처리
	7. 합의 엔진으로 봉인하기 위해 엉클 리워드 및 최종 상태를 설정하고 블록을 조립한다
	8. 합의엔진의 sealer인 ethash로 블록을 전달하여 논스를 찾도록 한다


---
### metrics
 * package main: 메트릭스는중요한 컴포넌트의 동작을 측정하는 방법을 제공하는 툴킷이다, 게이지/meter등을 생성하도록 도와준다.
 	* package exp
 	* package influxdb
 	* package librato
---
### mobile
 * package geth: 이더리움을 위한 단순화된 모바일 api를 제공함
---
### node: 이더리움 노드
 * package node: 멀티 프로토콜 이더리움 노드를 설정함. 노드는 리소스를 RPC API들에게 공유하는 서비스들의 집합이다.
서비스들은 devp2p프로토콜을 제공할 수 있다
---
### p2p: p2p 관련
 * package p2p: 이더리움 p2p 네트워크 프로토콜을 구현한다
 	* package discover: 노드 디스커버리 프로토콜을 구현함
	* package discv5: RLPx v5 토픽 디스커버리 프로토콜을 구현한다
	* package enr: EIP-778에 따라 이더리움 노드 기록들을 제공한다. 노드는 p2p네트워크의 노드에 대한 정보를 기록한다
	* package nat: 네트워크 포트매핑 프로토콜에 대한 접근을 제공한다
	* package netutil: net 확장 패키지
	* package protocols: devp2p노드등를 생성하고 연결하여 네트워크 시뮬레이션을 수행하도록 한다
	* package simulations: p2p 익스텐션. devp2p서브 프로토콜을 사용하기 위한 쉬운 방법을 제공한다
---
### params
 * package params: 부트노드, 이더리움, gas , 네트워크, 프로토콜등 모든 설정의 모음
---
### rlp
 * package rlp: RLP 직렬화 구현
---
### rpc
 * package rpc: 네트워크상 노출된 객체의 메소드 접근이나 다른 I/O 연결에 대한 접근을 제공한다
---
### signer
* package core
 	* package rules
 		* package deps: contains the console JavaScript dependencies Go embedded.
 	* package storage
---
### swarm
 * package swarm: 컨텐츠 분산저장 시스템
 	* package api
 		* package client
 		* package http
 	* package fuse
 	* package metrics
 	* package network
 		* package kademlia
	* package storage
 * package swap: SwAP Swarm Accounting Protocol with Swift Automatic Payments a peer to peer micropayment system
---
### trie
 * package trie: 머클 패트리샤 트리의 구현
 ![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmZLrWnEnEDyy4u9Cxp1r8aPHjiQfExCpHzd7dxnhxvAtG/image.png)
---
### whisper
 * package mailserver
 * package shhclient
 * package whisperv5
 * package whisperv6
---
### vendor
 fs
 autorest
 azure
 date
 color
 memsize
 memsizeui
 termui
 ole
 oleutil
 stack
 proto
 descriptor
 lru
 simplelru
 internetgateway1
 goupnp
 httpu
 scpd
 soap
 models
 escape
 natpmp
 httprouter
 hid
 colorable
 isatty
 runewidth
 wordwrap
 stringutil
 ast
 toml
 termbox
 tablewriter
 uuid
 liner
 errors
 difflib
 flock
 notify
 otto
 dbg
 file
 parser
 registry
 token
 cors
 xhandler
 assert
 require
 cache
 comparer
 iterator
 journal
 memdb
 opt
 table
 util
 leveldb
 cast5
 curve25519
 ed25519
 edwards25519
 armor
 openpgp
 elgamal
 packet
 s2k 
 pbkdf2 
 ripemd160 
 scrypt
 terminal
 ssh
 context 
 atom
 charset 
 html 
 websocket
 syncmap
 unix
 windows
 charmap
 encoding 
 htmlindex
 identifier
 internal
 japanese
 korean
 simplifiedchinese
 traditionalchinese
 unicode
 tag 
 utf8internal
 language
 transform 
 astutil
 imports
 clia

## GETH block
![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmUYdekGgmf7jKE4T3dhuXrsofMJHLyTK6F92XDZbXwVT4/image.png) 

## GETH function flow



![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmSS9KhboUC13eutXRdD8cruNJqAsfVw6Htb5HhCvMg5s5/image.png)

![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmPrGtgbxg8fvAV8oWneto4sZhfysD5gWfyCsBnBn71vCj/image.png)

블록체인 내부 update loop: 5초마다 퓨쳐블록을 처리한다
퓨처 블록이란 블록을 체인에 넣으려고 했으나, 블록의 시간이 현재 노드의 시간보다 앞 설경우이다
이럴땐 퓨처블록으로 따로 모아두었다가 5초마다 다시  체인에 넣는것을 시도한다.

트렌젝션 풀의 내부 loop 
- 로컬 트렌젝션의 저널 로테이션: 노드 재시작때 트렌젝션이 없어지는것을 방지한다고 말씀드렸었죠?
- 오래된 트렌젝션의 삭제
- 체인헤드 이벤트 처리: 풀이 참조하는 체인의 head값을 업데이트 (트렌젝션이 실행가능한지 현재 상태를 확인 해야하니까요)

![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmQV9QmVCQ5zZNkfxrY4VQPoSwJKCsiyxXM9aEoKtVADE5/image.png)

p2p의 loop: 
- listen loop:  저수준의 네트워크 요청을 처리하기 위한 루프
- run루프: 노드간의 연결을 관리한다(노드추가, handshake 완료등등)

블룸비트 요청처리 루프: 블룸비트 DB로부터 반환된 결과를 수신하기 위한 루프

![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmTViEMDGUaXaG51UG7ze2tV3RDpgRhEUcPjCsW1qmjBS8/image.png)

txBraodcast 루프: 트렌젝션을 공유하는 루프

![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmUioaCerYaU7VPDdyFZFRCQHioshJ9HzWM8WsmzTeagLF/image.png)

mined block broadcast: 마이닝된 블록을 공유하는 루프

![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmbJ5UxC4shoWg4DjNpqYDYdAHDTgHPcVsnsaioF4S5oB9/image.png)

syncer:  블록체인 동기화 루프

![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmVfznwXtZir1izJf8uYQCUs25DBNvc7VJSPYoYLMc24Nx/image.png)

![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmebE7oGmGupQazu2LxdqaCDtZAWamskGzNVzmnqJ7CPAE/image.png)

txsyncloop: 트렌젝션 싱크루프
마이닝 루프:  논스를 찾았을때 다음 마이닝 work를 생성

## 트렌젝션 & 블록 & 블록체인

![](https://cdn.steemitimages.com/DQmT6WHwvjgxbL9qasRB4R64XzQug924P1GPz1HdoBrZGZc/image.png)

## 트랜젝션이 공유되는 형태

![](https://steemitimages.com/0x0/https://cdn.steemitimages.com/DQmWv4Nadia8oywxc3Pw2DCuihFYwJuJCge9kiwRat95C6c/image.png)


#geth 노드의 구성

![](https://cdn.steemitimages.com/DQmPge1ZTyTyGV6LCn7HX6Ar1jizptCRTHJ8L4tF3uJgsWi/image.png)

#geth 노드의 초기동작시 함수

![](https://cdn.steemitimages.com/DQmRdzfLRzzan9LMMVQHeeUX8LVwXKJd7doB9GyJgk8HGuv/image.png)
