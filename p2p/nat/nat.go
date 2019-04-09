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

// Package nat provides access to common network port mapping protocols.
// nat패키지는 일반적인 네트워크 포트 매핑 프로토콜에 접근하기 위해 제공된다
package nat

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/jackpal/go-nat-pmp"
)

// An implementation of nat.Interface can map local ports to ports
// accessible from the Internet.
// nat.Interface 구현은 로컬 포트를 인터넷에 접근가능한 포트들로 맵할수 있다
type Interface interface {
	// These methods manage a mapping between a port on the local
	// machine to a port that can be connected to from the internet.
	//
	// protocol is "UDP" or "TCP". Some implementations allow setting
	// a display name for the mapping. The mapping may be removed by
	// the gateway when its lifetime ends.
	// 이 메소드들은 로컬머신의 포트를 인터넷으로 부터 연결되어질 포트로 맵핑한다
	// 프로토콜은 udp/tcp이다. 어떤 구현은 매핑에 대한 이름을 허용한다. 
	// 맵핑은 생명주기가 끝날 경우 게이트 웨이에 의해 제거될 수 있다
	AddMapping(protocol string, extport, intport int, name string, lifetime time.Duration) error
	DeleteMapping(protocol string, extport, intport int) error

	// This method should return the external (Internet-facing)
	// address of the gateway device.
	// 이 메소드는 게이트웨이 장비의 외부 주소를 반드시 반환해야한다
	ExternalIP() (net.IP, error)

	// Should return name of the method. This is used for logging.
	// 메소드의 이름을 반환해야 한다. 로깅에 사용된다
	String() string
}

// Parse parses a NAT interface description.
// The following formats are currently accepted.
// Note that mechanism names are not case-sensitive.
//
//     "" or "none"         return nil
//     "extip:77.12.33.4"   will assume the local machine is reachable on the given IP
//     "any"                uses the first auto-detected mechanism
//     "upnp"               uses the Universal Plug and Play protocol
//     "pmp"                uses NAT-PMP with an auto-detected gateway address
//     "pmp:192.168.0.1"    uses NAT-PMP with the given gateway address
// Parse함수는 NAT 인터페이스 표현을 분석한다
// 아래의 포멧만 현재 허용된다
// 대소문자는 구분하지 않는다
func Parse(spec string) (Interface, error) {
	var (
		parts = strings.SplitN(spec, ":", 2)
		mech  = strings.ToLower(parts[0])
		ip    net.IP
	)
	if len(parts) > 1 {
		ip = net.ParseIP(parts[1])
		if ip == nil {
			return nil, errors.New("invalid IP address")
		}
	}
	switch mech {
	case "", "none", "off":
		return nil, nil
	case "any", "auto", "on":
		return Any(), nil
	case "extip", "ip":
		if ip == nil {
			return nil, errors.New("missing IP address")
		}
		return ExtIP(ip), nil
	case "upnp":
		return UPnP(), nil
	case "pmp", "natpmp", "nat-pmp":
		return PMP(ip), nil
	default:
		return nil, fmt.Errorf("unknown mechanism %q", parts[0])
	}
}

const (
	mapTimeout        = 20 * time.Minute
	mapUpdateInterval = 15 * time.Minute
)

// Map adds a port mapping on m and keeps it alive until c is closed.
// This function is typically invoked in its own goroutine.
// Map 함수는 m 인터페이스에 포트 맵핑을 추가하고 c가 닫힐때 까지 저장한다
// 이 함수는 자신의 고루틴안에서 싫행된다
func Map(m Interface, c chan struct{}, protocol string, extport, intport int, name string) {
	log := log.New("proto", protocol, "extport", extport, "intport", intport, "interface", m)
	refresh := time.NewTimer(mapUpdateInterval)
	defer func() {
		refresh.Stop()
		log.Debug("Deleting port mapping")
		m.DeleteMapping(protocol, extport, intport)
	}()
	if err := m.AddMapping(protocol, extport, intport, name, mapTimeout); err != nil {
		log.Debug("Couldn't add port mapping", "err", err)
	} else {
		log.Info("Mapped network port")
	}
	for {
		select {
		case _, ok := <-c:
			if !ok {
				return
			}
		case <-refresh.C:
			log.Trace("Refreshing port mapping")
			if err := m.AddMapping(protocol, extport, intport, name, mapTimeout); err != nil {
				log.Debug("Couldn't add port mapping", "err", err)
			}
			refresh.Reset(mapUpdateInterval)
		}
	}
}

// ExtIP assumes that the local machine is reachable on the given
// external IP address, and that any required ports were mapped manually.
// Mapping operations will not return an error but won't actually do anything.
// ExtIP 인터페이스는 로컬 머신이 주어진 외부 ip에 접근가능하다고 가정하고
// 요구되는모든 포트는 매뉴얼하게 맾핑된다
// 맵핑 동작은 에러를 리턴하지 않지만 실제로 아무것도 안할것이다
func ExtIP(ip net.IP) Interface {
	if ip == nil {
		panic("IP must not be nil")
	}
	return extIP(ip)
}

type extIP net.IP

func (n extIP) ExternalIP() (net.IP, error) { return net.IP(n), nil }
func (n extIP) String() string              { return fmt.Sprintf("ExtIP(%v)", net.IP(n)) }

// These do nothing.
func (extIP) AddMapping(string, int, int, string, time.Duration) error { return nil }
func (extIP) DeleteMapping(string, int, int) error                     { return nil }

// Any returns a port mapper that tries to discover any supported
// mechanism on the local network.
// 이 함수는 로컬 네트워크의 지원가능한 메카니즘(UPNP or NAT-pmp) 을 발견하려 노력하는 포트 맵퍼를 리턴한다
func Any() Interface {
	// TODO: attempt to discover whether the local machine has an
	// Internet-class address. Return ExtIP in this case.
	return startautodisc("UPnP or NAT-PMP", func() Interface {
		found := make(chan Interface, 2)
		go func() { found <- discoverUPnP() }()
		go func() { found <- discoverPMP() }()
		for i := 0; i < cap(found); i++ {
			if c := <-found; c != nil {
				return c
			}
		}
		return nil
	})
}

// UPnP returns a port mapper that uses UPnP. It will attempt to
// discover the address of your router using UDP broadcasts.
// UPnP인터페이스는 uPnP를 사용하는 포트맵퍼를 반환한다
// 이 인터페이슨는 udp broad캐스팅을 통해 라우터 어드레스를 발견하려 할것이다
func UPnP() Interface {
	return startautodisc("UPnP", discoverUPnP)
}

// PMP returns a port mapper that uses NAT-PMP. The provided gateway
// address should be the IP of your router. If the given gateway
// address is nil, PMP will attempt to auto-discover the router.
// PMP 함수는 NAT-PMP를 이용하는 포트맵퍼를 반환한다
// 제공된 게이트웨이 주소는 라우터 IP여야 한다
// 주어진 주소가 nil일 경우 PMP는 라우터를 자동 발견 하려 할것이다
func PMP(gateway net.IP) Interface {
	if gateway != nil {
		return &pmp{gw: gateway, c: natpmp.NewClient(gateway)}
	}
	return startautodisc("NAT-PMP", discoverPMP)
}

// autodisc represents a port mapping mechanism that is still being
// auto-discovered. Calls to the Interface methods on this type will
// wait until the discovery is done and then call the method on the
// discovered mechanism.
//
// This type is useful because discovery can take a while but we
// want return an Interface value from UPnP, PMP and Auto immediately.
// autodisc 구조체는 자동으로 발견될 포트 매핑 메카니즘을 나타낸다
// 이 타입에 대한 인터페이스 메소드 호출들은 발견이 끝날때까지 대기하고
// 발견된 매캐니즘상의 메소드를 호출할것이다
// 이 타입은 발견은 시간이 걸리지만 우리는 인터페이스값을 반환받기 원하기 때문에
// 유용하다
type autodisc struct {
	what string // type of interface being autodiscovered
	// 자동으로 발견될 인터페이스의 타입
	once sync.Once
	doit func() Interface

	mu    sync.Mutex
	found Interface
}

func startautodisc(what string, doit func() Interface) Interface {
	// TODO: monitor network configuration and rerun doit when it changes.
	return &autodisc{what: what, doit: doit}
}

func (n *autodisc) AddMapping(protocol string, extport, intport int, name string, lifetime time.Duration) error {
	if err := n.wait(); err != nil {
		return err
	}
	return n.found.AddMapping(protocol, extport, intport, name, lifetime)
}

func (n *autodisc) DeleteMapping(protocol string, extport, intport int) error {
	if err := n.wait(); err != nil {
		return err
	}
	return n.found.DeleteMapping(protocol, extport, intport)
}

func (n *autodisc) ExternalIP() (net.IP, error) {
	if err := n.wait(); err != nil {
		return nil, err
	}
	return n.found.ExternalIP()
}

func (n *autodisc) String() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.found == nil {
		return n.what
	} else {
		return n.found.String()
	}
}

// wait blocks until auto-discovery has been performed.
// wait 함수는 자동 발견이 수행될때까지 대기한다
func (n *autodisc) wait() error {
	n.once.Do(func() {
		n.mu.Lock()
		n.found = n.doit()
		n.mu.Unlock()
	})
	if n.found == nil {
		return fmt.Errorf("no %s router discovered", n.what)
	}
	return nil
}
