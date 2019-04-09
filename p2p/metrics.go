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

// Contains the meters and timers used by the networking layer.
// 네트워크 레이어에서 사용될 meter와 timer를 포함한다

package p2p

import (
	"net"

	"github.com/ethereum/go-ethereum/metrics"
)

var (
	ingressConnectMeter = metrics.NewRegisteredMeter("p2p/InboundConnects", nil)
	ingressTrafficMeter = metrics.NewRegisteredMeter("p2p/InboundTraffic", nil)
	egressConnectMeter  = metrics.NewRegisteredMeter("p2p/OutboundConnects", nil)
	egressTrafficMeter  = metrics.NewRegisteredMeter("p2p/OutboundTraffic", nil)
)

// meteredConn is a wrapper around a network TCP connection that meters both the
// inbound and outbound network traffic.
// meteredConn은 내/외부트레픽을 측정을 포함하는 TCP 연결 래퍼이다
type meteredConn struct {
	*net.TCPConn // Network connection to wrap with metering
	// 측정기와 함께 둘러쌀 네트워크 연결
}

// newMeteredConn creates a new metered connection, also bumping the ingress or
// egress connection meter. If the metrics system is disabled, this function
// returns the original object.
// newMeteredConn 함수는 새로운 측정가능한 연결을 생성하고 진입 진출 측정을 증가시킨다. 
// 만약 측정시스템이 꺼져있디면 이함수는 원래의 오브젝트를 반환한다
func newMeteredConn(conn net.Conn, ingress bool) net.Conn {
	// Short circuit if metrics are disabled
	// 측정이 껴져있을때 빠른 처리
	if !metrics.Enabled {
		return conn
	}
	// Otherwise bump the connection counters and wrap the connection
	// 아니라면 연결 카운터를 증가시키고 연결을 랩핑한다
	if ingress {
		ingressConnectMeter.Mark(1)
	} else {
		egressConnectMeter.Mark(1)
	}
	return &meteredConn{conn.(*net.TCPConn)}
}

// Read delegates a network read to the underlying connection, bumping the ingress
// traffic meter along the way.
// Read는 주어진 연결을 통해 네트워크를 읽는것을 위임하고, 진입 미터기를 증가시킨다
func (c *meteredConn) Read(b []byte) (n int, err error) {
	n, err = c.TCPConn.Read(b)
	ingressTrafficMeter.Mark(int64(n))
	return
}

// Write delegates a network write to the underlying connection, bumping the
// egress traffic meter along the way.
// Write는 주어진 연결을 통해 네트워크를 쓰는것을 위임하고, 진출 미터기를 증가시킨다
func (c *meteredConn) Write(b []byte) (n int, err error) {
	n, err = c.TCPConn.Write(b)
	egressTrafficMeter.Mark(int64(n))
	return
