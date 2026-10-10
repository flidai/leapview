package postgrestest

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestPrivateTLSRelayTargetRequiresNumericLoopback(t *testing.T) {
	for _, target := range []string{"127.0.0.1:5432", "127.0.0.2:1", "[::1]:65535"} {
		if err := validatePrivateTLSRelayTarget(target); err != nil {
			t.Fatalf("valid loopback target rejected: %v", err)
		}
	}
	for _, target := range []string{"localhost:5432", "10.0.0.1:5432", "0.0.0.0:5432", "127.0.0.1:0", "127.0.0.1:-1", "127.0.0.1:65536", "127.0.0.1:05432", "127.0.0.1:postgres", "[::]:5432", "[::ffff:192.168.1.1]:5432", "127.0.0.1"} {
		if validatePrivateTLSRelayTarget(target) == nil {
			t.Fatalf("unsafe or ambiguous target accepted: %q", target)
		}
	}
}

func TestPrivateTLSRelayCleanupClosesLiveConnections(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		connection, err := upstream.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = io.Copy(connection, connection)
	}()
	var client net.Conn
	var address string
	t.Run("scoped relay", func(t *testing.T) {
		address, _ = StartPrivateTLSRelay(t, upstream.Addr().String())
		client, err = net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = client.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := client.Write([]byte("TLS remains opaque")); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, len("TLS remains opaque"))
		if _, err := io.ReadFull(client, buffer); err != nil || string(buffer) != "TLS remains opaque" {
			t.Fatal("relay did not preserve transport bytes")
		}
	})
	if client == nil {
		return
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("scoped cleanup did not close client: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("scoped cleanup retained upstream connection")
	}
	if connection, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		connection.Close()
		t.Fatal("scoped listener remained open")
	}
}
