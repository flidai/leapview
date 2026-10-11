package postgrestest

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"
)

// StartPrivateTLSRelay exposes only a disposable loopback PostgreSQL process
// through the same private-interface fixture used by Database.PrivateURL. TLS
// remains end-to-end; production outbound destination policy is not changed.
func StartPrivateTLSRelay(t *testing.T, target string) (string, net.IP) {
	t.Helper()
	if err := validatePrivateTLSRelayTarget(target); err != nil {
		t.Fatal(err)
	}
	listener, privateIP := newPrivateTLSListener(t)
	t.Cleanup(servePrivateTLSRelay(listener, target))
	return listener.Addr().String(), privateIP
}

func validatePrivateTLSRelayTarget(target string) error {
	host, rawPort, err := net.SplitHostPort(target)
	ip := net.ParseIP(host)
	port, portErr := strconv.Atoi(rawPort)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || port <= 0 || port > 65535 || rawPort != strconv.Itoa(port) {
		return errors.New("private PostgreSQL fixture relay requires a numeric loopback target and nonzero port")
	}
	return nil
}

// newPrivateTLSListener binds a host-owned RFC1918 interface. The relay is
// confined to a disposable PostgreSQL container; no customer endpoint or
// external network address is used by this test fixture.
func newPrivateTLSListener(t *testing.T) (net.Listener, net.IP) {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("enumerate private test interfaces: %v", err)
	}
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ipNetwork, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNetwork.IP.To4()
			parsed, valid := netip.AddrFromSlice(ip)
			if !valid || !parsed.IsPrivate() {
				continue
			}
			listener, listenErr := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
			if listenErr != nil {
				continue
			}
			t.Cleanup(func() { _ = listener.Close() })
			return listener, ip
		}
	}
	t.Fatal("disposable PostgreSQL TLS test requires a bindable private IPv4 interface")
	return nil, nil
}

func servePrivateTLSRelay(listener net.Listener, target string) func() {
	var mu sync.Mutex
	var connections sync.WaitGroup
	active := map[net.Conn]struct{}{}
	closed := false
	go func() {
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				incoming.Close()
				return
			}
			active[incoming] = struct{}{}
			connections.Add(1)
			mu.Unlock()
			go func() {
				defer connections.Done()
				defer func() { mu.Lock(); delete(active, incoming); mu.Unlock() }()
				relayPrivateTLSConnection(incoming, target)
			}()
		}
	}()
	return func() {
		mu.Lock()
		closed = true
		_ = listener.Close()
		for connection := range active {
			_ = connection.Close()
		}
		mu.Unlock()
		connections.Wait()
	}
}

func relayPrivateTLSConnection(incoming net.Conn, target string) {
	defer incoming.Close()
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, incoming)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(incoming, upstream)
		done <- struct{}{}
	}()
	<-done
}
