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

//+build windows

package netutil

import (
	"net"
	"os"
	"syscall"
)

const _WSAEMSGSIZE = syscall.Errno(10040)

// isPacketTooBig reports whether err indicates that a UDP packet didn't
// fit the receive buffer. On Windows, WSARecvFrom returns
// code WSAEMSGSIZE and no data if this happens.
// isPacketTooBig함수는 UDP패킷이 수신 버퍼에 맞지 않음을 보고한다. 윈도우즈에서는
// WSARecvFrom함수가 WSAEMSGSOZE를 반환하고 발생시 아무데이터가 없다
func isPacketTooBig(err error) bool {
	if opErr, ok := err.(*net.OpError); ok {
		if scErr, ok := opErr.Err.(*os.SyscallError); ok {
			return scErr.Err == _WSAEMSGSIZE
		}
		return opErr.Err == _WSAEMSGSIZE
	}
	return false
}
