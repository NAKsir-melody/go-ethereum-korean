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

// Contains the NTP time drift detection via the SNTP protocol:
//   https://tools.ietf.org/html/rfc4330
// SNTP 프로토콜상의 NTP drift시간을 감지하는 것을 포함함

package discover

import (
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

const (
	ntpPool   = "pool.ntp.org" // ntpPool is the NTP server to query for the current time
	ntpChecks = 3              // Number of measurements to do against the NTP server
	// ntpPool은 현재시간을 쿼리하기 위한 ntp서버이다
	// NTP 서버에 대응하기 위한 측정횟수
)

// durationSlice attaches the methods of sort.Interface to []time.Duration,
// sorting in increasing order.
// durationSlice는 srot.Interface의 함수를 time.Duration에 붙이고 소팅된다.
type durationSlice []time.Duration

func (s durationSlice) Len() int           { return len(s) }
func (s durationSlice) Less(i, j int) bool { return s[i] < s[j] }
func (s durationSlice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// checkClockDrift queries an NTP server for clock drifts and warns the user if
// one large enough is detected.
// checkClockDrift 함수는 clock drifts를 위해 NTP서버를 쿼리하고 큰 것이 감지되면 유저에게 경고한다
func checkClockDrift() {
	drift, err := sntpDrift(ntpChecks)
	if err != nil {
		return
	}
	if drift < -driftThreshold || drift > driftThreshold {
		log.Warn(fmt.Sprintf("System clock seems off by %v, which can prevent network connectivity", drift))
		log.Warn("Please enable network time synchronisation in system settings.")
	} else {
		log.Debug("NTP sanity check done", "drift", drift)
	}
}

// sntpDrift does a naive time resolution against an NTP server and returns the
// measured drift. This method uses the simple version of NTP. It's not precise
// but should be fine for these purposes.
//
// Note, it executes two extra measurements compared to the number of requested
// ones to be able to discard the two extremes as outliers.
// sntpDrift함수는 NTP서버에 대한 고유시간 해상도로 측정된 drift를 반환한다
// 이 방법은 NTP의 단순버전을 사용한다. 정밀하진 않지만 이런 목적으로는 괜찮다
// 하나의 요청에 대해 2개의 추가 측정 비교를 실행하여 두개를 기준밖으로 취급하여 discard 하는것을 실행한다
func sntpDrift(measurements int) (time.Duration, error) {
	// Resolve the address of the NTP server
	// NTP서버의 주소를 푼다
	addr, err := net.ResolveUDPAddr("udp", ntpPool+":123")
	if err != nil {
		return 0, err
	}
	// Construct the time request (empty package with only 2 fields set):
	//   Bits 3-5: Protocol version, 3
	//   Bits 6-8: Mode of operation, client, 3
	// 시간요청을 생성한다
	request := make([]byte, 48)
	request[0] = 3<<3 | 3

	// Execute each of the measurements
	// 각 측정을 실행한다
	drifts := []time.Duration{}
	for i := 0; i < measurements+2; i++ {
		// Dial the NTP server and send the time retrieval request
		// NTP서버에 다이얼하고 시간 반환 요청을 보낸다
		conn, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			return 0, err
		}
		defer conn.Close()

		sent := time.Now()
		if _, err = conn.Write(request); err != nil {
			return 0, err
		}
		// Retrieve the reply and calculate the elapsed time
		// 요청을 반환하고 걸리시간을 계산한다
		conn.SetDeadline(time.Now().Add(5 * time.Second))

		reply := make([]byte, 48)
		if _, err = conn.Read(reply); err != nil {
			return 0, err
		}
		elapsed := time.Since(sent)

		// Reconstruct the time from the reply data
		// 응답 데이토로 부터 시간을 재생성한다
		sec := uint64(reply[43]) | uint64(reply[42])<<8 | uint64(reply[41])<<16 | uint64(reply[40])<<24
		frac := uint64(reply[47]) | uint64(reply[46])<<8 | uint64(reply[45])<<16 | uint64(reply[44])<<24

		nanosec := sec*1e9 + (frac*1e9)>>32

		t := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(nanosec)).Local()

		// Calculate the drift based on an assumed answer time of RRT/2
		// RRT/2의 응답시간으로 가정하여 drift를 계산한다
		drifts = append(drifts, sent.Sub(t)+elapsed/2)
	}
	// Calculate average drif (drop two extremities to avoid outliers)
	// 평견 drift를 계산한다
	sort.Sort(durationSlice(drifts))

	drift := time.Duration(0)
	for i := 1; i < len(drifts)-1; i++ {
		drift += drifts[i]
	}
	return drift / time.Duration(measurements), nil
}
