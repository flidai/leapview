package postgrestest

import (
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

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

func servePrivateTLSRelay(listener net.Listener, target string) {
	go func() {
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			go relayPrivateTLSConnection(incoming, target)
		}
	}()
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
