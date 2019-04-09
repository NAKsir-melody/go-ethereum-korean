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

package core

import "errors"

var (
	// ErrKnownBlock is returned when a block to import is already known locally.
	// ErrKnownBlock은 입수할 블록이 이미 로컬에 존재할때 반환된다
	ErrKnownBlock = errors.New("block already known")

	// ErrGasLimitReached is returned by the gas pool if the amount of gas required
	// by a transaction is higher than what's left in the block.
	// ErrGasLimitReached는 트렌젝션에 요구되는 가스의 양이 
	// 블록에 남은 것보다 높을 경우 가스풀에의해 반환된다
	ErrGasLimitReached = errors.New("gas limit reached")

	// ErrBlacklistedHash is returned if a block to import is on the blacklist.
	// ErrBlacklistedHahs는 입수할 블록이 블랙리스트에 있을 경우 반환된다
	ErrBlacklistedHash = errors.New("blacklisted hash")

	// ErrNonceTooHigh is returned if the nonce of a transaction is higher than the
	// next one expected based on the local chain.
	// ErrNonceTooHigh는 트렌젝션의 논스가 로컬 체인의 기대값보다 높을때 반환된다
	ErrNonceTooHigh = errors.New("nonce too high")
)
