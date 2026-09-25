package outbound

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/platform/security/secret"
)

// Proxy is a loopback-only application egress gateway for embedded native
// clients that cannot accept a Go DialContext (notably DuckDB httpfs). The
// loopback listener is an internal transport detail; every external socket is
// still created through Policy.DialContext.
type Proxy struct {
	listener net.Listener
	server   *http.Server
	policy   *Policy
	client   *http.Client
	username string
	password string
	once     sync.Once
}

func StartProxy(policy *Policy) (*Proxy, error) {
	if policy == nil {
		return nil, errors.New("outbound proxy requires a destination policy")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start outbound policy proxy: %w", err)
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		listener.Close()
		return nil, fmt.Errorf("create outbound proxy credential: %w", err)
	}
	proxy := &Proxy{
		listener: listener, policy: policy, username: "leapview",
		password: base64.RawURLEncoding.EncodeToString(token),
	}
	proxy.client = policy.HTTPClient(&http.Client{Timeout: 30 * time.Second}, HTTPConfig{
		AllowedSchemes: []string{"http", "https"}, MaxRedirects: 0,
	})
	proxy.server = &http.Server{
		Handler:           proxy,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		_ = proxy.server.Serve(listener)
	}()
	return proxy, nil
}

func (p *Proxy) URL() string {
	if p == nil || p.listener == nil {
		return ""
	}
	return "http://" + p.listener.Addr().String()
}

func (p *Proxy) Credentials() (string, string) {
	if p == nil {
		return "", ""
	}
	return p.username, p.password
}

func (p *Proxy) Close() error {
	if p == nil {
		return nil
	}
	var closeErr error
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closeErr = p.server.Shutdown(ctx)
		p.client.CloseIdleConnections()
	})
	return closeErr
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.authorized(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="LeapView outbound"`)
		http.Error(w, http.StatusText(http.StatusProxyAuthRequired), http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL == nil || !r.URL.IsAbs() {
		http.Error(w, "absolute outbound URL required", http.StatusBadRequest)
		return
	}
	request := r.Clone(r.Context())
	request.RequestURI = ""
	request.Host = r.URL.Host
	removeHopHeaders(request.Header)
	request.Header.Del("Proxy-Authorization")
	response, err := p.client.Do(request)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrDestinationDenied) || errors.Is(err, ErrUnsupportedScheme) {
			status = http.StatusForbidden
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	defer response.Body.Close()
	removeHopHeaders(response.Header)
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (p *Proxy) authorized(request *http.Request) bool {
	const prefix = "Basic "
	raw := request.Header.Get("Proxy-Authorization")
	if !strings.HasPrefix(raw, prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(raw, prefix)))
	if err != nil {
		return false
	}
	want := p.username + ":" + p.password
	return secret.EqualFixedBytes(decoded, []byte(want))
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if !strings.Contains(target, ":") {
		target = net.JoinHostPort(target, "443")
	}
	upstream, err := p.policy.DialContext(r.Context(), "tcp", target)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, ErrDestinationDenied) {
			status = http.StatusForbidden
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "proxy tunneling unavailable", http.StatusInternalServerError)
		return
	}
	downstream, buffer, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		downstream.Close()
		upstream.Close()
		return
	}
	if err := buffer.Flush(); err != nil {
		downstream.Close()
		upstream.Close()
		return
	}
	go tunnel(downstream, upstream)
}

func tunnel(left, right net.Conn) {
	defer left.Close()
	defer right.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(left, right)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(right, left)
		done <- struct{}{}
	}()
	<-done
}

func removeHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
}
