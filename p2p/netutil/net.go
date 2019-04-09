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

// Package netutil contains extensions to the net package.
// netutil 패키지는 net패키지의 확장을 포함한다
package netutil

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

var lan4, lan6, special4, special6 Netlist

func init() {
	// Lists from RFC 5735, RFC 5156,
	// https://www.iana.org/assignments/iana-ipv4-special-registry/
	// RFC 5735, RFC 5165로부터의 리스트
	lan4.Add("0.0.0.0/8")              // "This" network
	lan4.Add("10.0.0.0/8")             // Private Use
	lan4.Add("172.16.0.0/12")          // Private Use
	lan4.Add("192.168.0.0/16")         // Private Use
	lan6.Add("fe80::/10")              // Link-Local
	lan6.Add("fc00::/7")               // Unique-Local
	special4.Add("192.0.0.0/29")       // IPv4 Service Continuity
	special4.Add("192.0.0.9/32")       // PCP Anycast
	special4.Add("192.0.0.170/32")     // NAT64/DNS64 Discovery
	special4.Add("192.0.0.171/32")     // NAT64/DNS64 Discovery
	special4.Add("192.0.2.0/24")       // TEST-NET-1
	special4.Add("192.31.196.0/24")    // AS112
	special4.Add("192.52.193.0/24")    // AMT
	special4.Add("192.88.99.0/24")     // 6to4 Relay Anycast
	special4.Add("192.175.48.0/24")    // AS112
	special4.Add("198.18.0.0/15")      // Device Benchmark Testing
	special4.Add("198.51.100.0/24")    // TEST-NET-2
	special4.Add("203.0.113.0/24")     // TEST-NET-3
	special4.Add("255.255.255.255/32") // Limited Broadcast

	// http://www.iana.org/assignments/iana-ipv6-special-registry/
	special6.Add("100::/64")
	special6.Add("2001::/32")
	special6.Add("2001:1::1/128")
	special6.Add("2001:2::/48")
	special6.Add("2001:3::/32")
	special6.Add("2001:4:112::/48")
	special6.Add("2001:5::/32")
	special6.Add("2001:10::/28")
	special6.Add("2001:20::/28")
	special6.Add("2001:db8::/32")
	special6.Add("2002::/16")
}

// Netlist is a list of IP networks.
// Netlist는 IP네트워크의 리스트이다
type Netlist []net.IPNet

// ParseNetlist parses a comma-separated list of CIDR masks.
// Whitespace and extra commas are ignored.
// ParseNEtlist 함수는 CIDR 마스크의 콤마로 분리된 리스트를 파싱한다
// 공백과 추가 콤마는 무시된다
func ParseNetlist(s string) (*Netlist, error) {
	ws := strings.NewReplacer(" ", "", "\n", "", "\t", "")
	masks := strings.Split(ws.Replace(s), ",")
	l := make(Netlist, 0)
	for _, mask := range masks {
		if mask == "" {
			continue
		}
		_, n, err := net.ParseCIDR(mask)
		if err != nil {
			return nil, err
		}
		l = append(l, *n)
	}
	return &l, nil
}

// MarshalTOML implements toml.MarshalerRec.
// Marshal TOML은 toml.MarshalerRec를 구현한다
func (l Netlist) MarshalTOML() interface{} {
	list := make([]string, 0, len(l))
	for _, net := range l {
		list = append(list, net.String())
	}
	return list
}

// UnmarshalTOML implements toml.UnmarshalerRec.
// Unmarshal TOML은 toml.UnmarshalerRec를 구현한다
func (l *Netlist) UnmarshalTOML(fn func(interface{}) error) error {
	var masks []string
	if err := fn(&masks); err != nil {
		return err
	}
	for _, mask := range masks {
		_, n, err := net.ParseCIDR(mask)
		if err != nil {
			return err
		}
		*l = append(*l, *n)
	}
	return nil
}

// Add parses a CIDR mask and appends it to the list. It panics for invalid masks and is
// intended to be used for setting up static lists.
// Add 함수는 CIDR마스크를 파싱하고 리스트에 붙인다. 
// 무효한 마스크에 대해 패닉을 리턴하고 정적인 리스트를 셋업하는데 쓰일것이다
func (l *Netlist) Add(cidr string) {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	*l = append(*l, *n)
}

// Contains reports whether the given IP is contained in the list.
// Contains 함수는 주어진 IP가 리스트에 있는지 여부를 보고한다
func (l *Netlist) Contains(ip net.IP) bool {
	if l == nil {
		return false
	}
	for _, net := range *l {
		if net.Contains(ip) {
			return true
		}
	}
	return false
}

// IsLAN reports whether an IP is a local network address.
// IsLAn함수는 IP가 로컬 어드레스인지 보고한다
func IsLAN(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return lan4.Contains(v4)
	}
	return lan6.Contains(ip)
}

// IsSpecialNetwork reports whether an IP is located in a special-use network range
// This includes broadcast, multicast and documentation addresses.
// IsSpecialNetwork함수는 IP가 특별하게 사용되는 네트워크 영역에 있는지를 보고한다
// 이것은 브로드캐스트, 멀티캐스트, 다큐멘테이션 주소를 포함한다
func IsSpecialNetwork(ip net.IP) bool {
	if ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return special4.Contains(v4)
	}
	return special6.Contains(ip)
}

var (
	errInvalid     = errors.New("invalid IP")
	errUnspecified = errors.New("zero address")
	errSpecial     = errors.New("special network")
	errLoopback    = errors.New("loopback address from non-loopback host")
	errLAN         = errors.New("LAN address from WAN host")
)

// CheckRelayIP reports whether an IP relayed from the given sender IP
// is a valid connection target.
//
// There are four rules:
//   - Special network addresses are never valid.
//   - Loopback addresses are OK if relayed by a loopback host.
//   - LAN addresses are OK if relayed by a LAN host.
//   - All other addresses are always acceptable.
// CheckRelayIP함수는 주어진 IP가 유효한 연결타겟의 전송자 IP로부터 릴레이 되었는지 보고한다
// 4개의 룰이있다
// 스페셜 네트워크의 주소는 절대 무효하다
// 루프백 주소는 루프백 호스트에서 연결되었을 경우 ok
// LAN 주소는 Lan host로 부터 릴레이 되었다면 ok
// 모든 다른 주소는 언제나 수신가능
func CheckRelayIP(sender, addr net.IP) error {
	if len(addr) != net.IPv4len && len(addr) != net.IPv6len {
		return errInvalid
	}
	if addr.IsUnspecified() {
		return errUnspecified
	}
	if IsSpecialNetwork(addr) {
		return errSpecial
	}
	if addr.IsLoopback() && !sender.IsLoopback() {
		return errLoopback
	}
	if IsLAN(addr) && !IsLAN(sender) {
		return errLAN
	}
	return nil
}

// SameNet reports whether two IP addresses have an equal prefix of the given bit length.
// SameNet함수는 두개의 IP주소가 동일한 비트길이의 접두사를 갖는지 보고한다
func SameNet(bits uint, ip, other net.IP) bool {
	ip4, other4 := ip.To4(), other.To4()
	switch {
	case (ip4 == nil) != (other4 == nil):
		return false
	case ip4 != nil:
		return sameNet(bits, ip4, other4)
	default:
		return sameNet(bits, ip.To16(), other.To16())
	}
}

func sameNet(bits uint, ip, other net.IP) bool {
	nb := int(bits / 8)
	mask := ^byte(0xFF >> (bits % 8))
	if mask != 0 && nb < len(ip) && ip[nb]&mask != other[nb]&mask {
		return false
	}
	return nb <= len(ip) && bytes.Equal(ip[:nb], other[:nb])
}

// DistinctNetSet tracks IPs, ensuring that at most N of them
// fall into the same network range.
// DistinctNetSet는 IP를 추적하고 대부분의 N이 동일 네트워크 영역 있음을 확정한다
type DistinctNetSet struct {
	Subnet uint // number of common prefix bits
	Limit  uint // maximum number of IPs in each subnet

	members map[string]uint
	buf     net.IP
	// 공통의 접두 비트
	// 각 서브넷의 최대 ip갯수
}

// Add adds an IP address to the set. It returns false (and doesn't add the IP) if the
// number of existing IPs in the defined range exceeds the limit.
// Add 함수는 설정할 IP주소 주소를 추가한다
// 이함수는 이미 존재하는 ip가 정의된 영역의 한도를 넘어서면 추가하지 않는다
func (s *DistinctNetSet) Add(ip net.IP) bool {
	key := s.key(ip)
	n := s.members[string(key)]
	if n < s.Limit {
		s.members[string(key)] = n + 1
		return true
	}
	return false
}

// Remove removes an IP from the set.
// Remove함수는 set으로부터 IP를 제거한다
func (s *DistinctNetSet) Remove(ip net.IP) {
	key := s.key(ip)
	if n, ok := s.members[string(key)]; ok {
		if n == 1 {
			delete(s.members, string(key))
		} else {
			s.members[string(key)] = n - 1
		}
	}
}

// Contains whether the given IP is contained in the set.
// Contains함수는 주어진 IP가 세트에 포함되는지 반환한다
func (s DistinctNetSet) Contains(ip net.IP) bool {
	key := s.key(ip)
	_, ok := s.members[string(key)]
	return ok
}

// Len returns the number of tracked IPs.
// Len함수는 추적중인 IP의 숫자를 반환한다
func (s DistinctNetSet) Len() int {
	n := uint(0)
	for _, i := range s.members {
		n += i
	}
	return int(n)
}

// key encodes the map key for an address into a temporary buffer.
//
// The first byte of key is '4' or '6' to distinguish IPv4/IPv6 address types.
// The remainder of the key is the IP, truncated to the number of bits.
// key함수는 임시 버퍼로 주소를 위한 맵키를 인코딩한다
// 첫번째 키의 바이트는 IPv4/IPv6를 구분하기 위해 4또는 6이다
// 키의 남은 값은 IP이고 몇개의 비트로 짤린다
func (s *DistinctNetSet) key(ip net.IP) net.IP {
	// Lazily initialize storage.
	// 여유있게 스토리지를 초기화 한다
	if s.members == nil {
		s.members = make(map[string]uint)
		s.buf = make(net.IP, 17)
	}
	// Canonicalize ip and bits.
	// ip와 bit를 합의한다
	typ := byte('6')
	if ip4 := ip.To4(); ip4 != nil {
		typ, ip = '4', ip4
	}
	bits := s.Subnet
	if bits > uint(len(ip)*8) {
		bits = uint(len(ip) * 8)
	}
	// Encode the prefix into s.buf.
	// s.buf에 접두사를 인코딩한다
	nb := int(bits / 8)
	mask := ^byte(0xFF >> (bits % 8))
	s.buf[0] = typ
	buf := append(s.buf[:1], ip[:nb]...)
	if nb < len(ip) && mask != 0 {
		buf = append(buf, ip[nb]&mask)
	}
	return buf
}

// String implements fmt.Stringer
// String함수는 fmt.Stringer를 구현한다
func (s DistinctNetSet) String() string {
	var buf bytes.Buffer
	buf.WriteString("{")
	keys := make([]string, 0, len(s.members))
	for k := range s.members {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		var ip net.IP
		if k[0] == '4' {
			ip = make(net.IP, 4)
		} else {
			ip = make(net.IP, 16)
		}
		copy(ip, k[1:])
		fmt.Fprintf(&buf, "%v×%d", ip, s.members[k])
		if i != len(keys)-1 {
			buf.WriteString(" ")
		}
	}
	buf.WriteString("}")
	return buf.String()
}
