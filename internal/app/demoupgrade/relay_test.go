package demoupgrade

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestPrivateRelayPreservesBytesAndClosesConnectionsOnCancellation(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		c, err := upstream.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	address, closeRelay, err := startTCPRelay(context.Background(), "127.0.0.1:0", upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelay()
	client, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = client.Write([]byte("opaque TLS bytes")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("opaque TLS bytes"))
	if _, err = io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "opaque TLS bytes" {
		t.Fatal(string(got))
	}
	closeRelay()
	if _, err = client.Read(make([]byte, 1)); err == nil {
		t.Fatal("cancelled relay kept the connection open")
	}
	if c, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		c.Close()
		t.Fatal("closed relay still accepts")
	}
}
