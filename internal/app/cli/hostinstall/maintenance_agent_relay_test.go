package hostinstall

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentReverseRelayAuthenticatesAndPreservesTLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "provider.example"}, DNSNames: []string{"provider.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, _ := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	pk, _ := x509.MarshalPKCS8PrivateKey(key)
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}))
	if err != nil {
		t.Fatal(err)
	}
	upstream, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() { defer c.Close(); c.SetDeadline(time.Now().Add(3 * time.Second)); _, _ = io.Copy(c, c) }()
		}
	}()
	app, _ := net.Listen("tcp", "127.0.0.1:0")
	control, _ := net.Listen("tcp", "127.0.0.1:0")
	cfg := agentRelayConfig{Nonce: strings.Repeat("a", 64), AppIP: "127.0.0.1", HostIP: "127.0.0.1", OperationDigest: "sha256:" + strings.Repeat("b", 64)}
	done := make(chan error, 1)
	go func() { done <- serveAgentRelay(ctx, cfg, app, control) }()
	closeChannels, err := openReverseAgentChannels(ctx, cfg, control.Addr().String(), upstream.Addr().String(), 4)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(templateFromDER(t, der))
	client, err := tls.Dial("tcp", app.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "provider.example"})
	if err != nil {
		t.Fatal(err)
	}
	client.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = client.Write([]byte("hello"))
	reply := make([]byte, 5)
	if _, err := io.ReadFull(client, reply); err != nil || string(reply) != "hello" {
		t.Fatalf("opaque TLS round-trip failed: %v", err)
	}
	client.Close()
	if c, err := tls.Dial("tcp", app.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "wrong.example"}); err == nil {
		c.Close()
		t.Fatal("wrong hostname certificate accepted")
	}
	if c, err := tls.Dial("tcp", app.Addr().String(), &tls.Config{ServerName: "provider.example"}); err == nil {
		c.Close()
		t.Fatal("untrusted certificate accepted")
	}
	before := accepted.Load()
	if err := closeChannels(); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	if accepted.Load() != before {
		t.Fatal("upstream work continued after cleanup barrier")
	}
}

func templateFromDER(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestAgentRelayRejectsWrongNonceAndPeer(t *testing.T) {
	for _, wrongPeer := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		app, _ := net.Listen("tcp", "127.0.0.1:0")
		control, _ := net.Listen("tcp", "127.0.0.1:0")
		cfg := agentRelayConfig{Nonce: strings.Repeat("a", 64), AppIP: "127.0.0.1", HostIP: "127.0.0.1"}
		if wrongPeer {
			cfg.HostIP = "192.168.1.2"
		}
		done := make(chan error, 1)
		go func() { done <- serveAgentRelay(ctx, cfg, app, control) }()
		c, err := net.Dial("tcp", control.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(c, strings.Repeat("x", 64)+"\n")
		b := make([]byte, 1)
		if _, err = c.Read(b); err == nil {
			t.Fatal("unauthorized control accepted")
		}
		c.Close()
		cancel()
		<-done
	}
}

func TestAgentRelayRejectsLateDialAndAcceptRegistration(t *testing.T) {
	for _, phase := range []string{"dial", "accept"} {
		t.Run(phase, func(t *testing.T) {
			sockets := newAgentRelaySockets(2)
			ready, proceed := make(chan struct{}), make(chan struct{})
			left, right := net.Pipe()
			defer right.Close()
			done := make(chan bool, 1)
			go func() { close(ready); <-proceed; done <- sockets.register(left) }()
			<-ready
			sockets.stop()
			close(proceed)
			if <-done {
				t.Fatal("late socket registered after stop")
			}
			_ = right.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := right.Read(make([]byte, 1)); err == nil {
				t.Fatal("late socket remained open")
			}
			sockets.mu.Lock()
			remaining := len(sockets.sockets)
			sockets.mu.Unlock()
			if remaining != 0 {
				t.Fatal("stopped registry retained socket")
			}
		})
	}
}

func TestAgentRelayBoundsActiveAndReleasesClosedSockets(t *testing.T) {
	sockets := newAgentRelaySockets(1)
	defer sockets.stop()
	a, b := net.Pipe()
	defer b.Close()
	if !sockets.register(a) {
		t.Fatal("first registration rejected")
	}
	c, d := net.Pipe()
	defer d.Close()
	if sockets.register(c) {
		t.Fatal("active bound exceeded")
	}
	sockets.release(a)
	for range 100 {
		e, f := net.Pipe()
		if !sockets.register(e) {
			t.Fatal("released slot not reusable")
		}
		sockets.release(e)
		f.Close()
	}
	sockets.mu.Lock()
	remaining := len(sockets.sockets)
	sockets.mu.Unlock()
	if remaining != 0 {
		t.Fatal("registry retained closed sockets")
	}
}
