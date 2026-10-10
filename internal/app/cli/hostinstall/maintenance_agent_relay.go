package hostinstall

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/platform/security/secret"
)

const agentRelayStreamBudget = 4
const agentRelayAcceptBudget = 16
const agentRelayByteBudget = 2 << 20
const agentRelayLifetime = 2 * time.Minute

type agentRelayConfig struct {
	OperationDigest string `json:"operationDigest"`
	Nonce           string `json:"nonce"`
	AppIP           string `json:"appIp"`
	HostIP          string `json:"hostIp"`
	SidecarIP       string `json:"sidecarIp"`
}

func (c agentRelayConfig) validate() error {
	nonce, err := hex.DecodeString(c.Nonce)
	if err != nil || len(nonce) != 32 || !digestPattern.MatchString(c.OperationDigest) {
		return errAgentTransition
	}
	for _, raw := range []string{c.AppIP, c.HostIP, c.SidecarIP} {
		ip, err := netip.ParseAddr(raw)
		if err != nil || !ip.Is4() || !ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return errAgentTransition
		}
	}
	if c.AppIP == c.HostIP || c.AppIP == c.SidecarIP || c.HostIP == c.SidecarIP {
		return errAgentTransition
	}
	return nil
}

func agentRelayPeer(c net.Conn, expected string) bool {
	host, _, err := net.SplitHostPort(c.RemoteAddr().String())
	return err == nil && host == expected
}
func agentRelayToken(c agentRelayConfig) string { return c.OperationDigest + " " + c.Nonce + "\n" }

// Registration and stopping share one lock. A socket arriving after the stop
// sweep is closed immediately, including a late successful Dial or Accept.
type agentRelaySockets struct {
	mu      sync.Mutex
	stopped bool
	limit   int
	sockets map[net.Conn]struct{}
}

func newAgentRelaySockets(limit int) *agentRelaySockets {
	return &agentRelaySockets{limit: limit, sockets: make(map[net.Conn]struct{})}
}
func (s *agentRelaySockets) register(c net.Conn) bool {
	s.mu.Lock()
	if s.stopped || len(s.sockets) >= s.limit {
		s.mu.Unlock()
		_ = c.Close()
		return false
	}
	s.sockets[c] = struct{}{}
	s.mu.Unlock()
	return true
}
func (s *agentRelaySockets) release(c net.Conn) {
	s.mu.Lock()
	delete(s.sockets, c)
	s.mu.Unlock()
	_ = c.Close()
}
func (s *agentRelaySockets) stop() {
	s.mu.Lock()
	s.stopped = true
	sockets := s.sockets
	s.sockets = make(map[net.Conn]struct{})
	s.mu.Unlock()
	for c := range sockets {
		_ = c.Close()
	}
}

// Host-initiated channels carry opaque end-to-end TLS to one admitted public
// destination. The isolated bridge receives neither a default route nor a
// firewall exception, and client frames cannot select a destination.
func startReverseAgentChannels(parent context.Context, config agentRelayConfig, controlAddress, destination string, budget int) (func() error, error) {
	host, port, err := net.SplitHostPort(destination)
	if config.validate() != nil || controlAddress != net.JoinHostPort(config.SidecarIP, "4443") || err != nil || port != "443" || !agentPublicAddress(host) {
		return nil, errAgentTransition
	}
	return openReverseAgentChannels(parent, config, controlAddress, destination, budget)
}

func openReverseAgentChannels(parent context.Context, config agentRelayConfig, controlAddress, destination string, budget int) (func() error, error) {
	if budget < 1 || budget > agentRelayStreamBudget {
		return nil, errAgentTransition
	}
	ctx, cancel := context.WithTimeout(parent, agentRelayLifetime)
	sockets := newAgentRelaySockets(2 * budget)
	var workers sync.WaitGroup
	var once sync.Once
	closeChannels := func() error { once.Do(func() { sockets.stop(); cancel(); workers.Wait() }); return nil }
	watcherDone := make(chan struct{})
	go func() { defer close(watcherDone); <-ctx.Done(); sockets.stop() }()
	for range budget {
		c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", controlAddress)
		if err != nil {
			_ = closeChannels()
			<-watcherDone
			return nil, errAgentTransition
		}
		if !sockets.register(c) {
			_ = closeChannels()
			<-watcherDone
			return nil, errAgentTransition
		}
		host, _, _ := net.SplitHostPort(controlAddress)
		if !agentRelayPeer(c, host) {
			_ = closeChannels()
			<-watcherDone
			return nil, errAgentTransition
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err = io.WriteString(c, agentRelayToken(config)); err != nil {
			_ = closeChannels()
			<-watcherDone
			return nil, errAgentTransition
		}
		reader := bufio.NewReader(io.LimitReader(c, 256))
		line, err := reader.ReadString('\n')
		if err != nil || line != "READY "+config.OperationDigest+"\n" {
			_ = closeChannels()
			<-watcherDone
			return nil, errAgentTransition
		}
		_ = c.SetDeadline(time.Now().Add(agentRelayLifetime))
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer sockets.release(c)
			line, err := reader.ReadString('\n')
			if err != nil || line != "STREAM "+config.OperationDigest+"\n" || ctx.Err() != nil {
				return
			}
			u, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", destination)
			if err != nil {
				return
			}
			if !sockets.register(u) {
				return
			}
			defer sockets.release(u)
			_ = u.SetDeadline(time.Now().Add(agentRelayLifetime))
			if buffered := reader.Buffered(); buffered > 0 {
				raw, _ := reader.Peek(buffered)
				if _, err = u.Write(raw); err != nil {
					return
				}
			}
			copyAgentRelay(c, u)
		}()
	}
	return func() error { err := closeChannels(); <-watcherDone; return err }, nil
}

func copyAgentRelay(left, right net.Conn) {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(right, io.LimitReader(left, agentRelayByteBudget))
		_ = right.Close()
		close(done)
	}()
	_, _ = io.Copy(left, io.LimitReader(right, agentRelayByteBudget))
	_ = left.Close()
	_ = right.Close()
	<-done
}

func serveAgentRelay(parent context.Context, config agentRelayConfig, app, control net.Listener) error {
	ctx, cancel := context.WithTimeout(parent, agentRelayLifetime)
	defer cancel()
	sockets := newAgentRelaySockets(2 * agentRelayStreamBudget)
	queue := make(chan net.Conn, agentRelayStreamBudget)
	var workers sync.WaitGroup
	closed := make(chan struct{})
	go func() { defer close(closed); <-ctx.Done(); sockets.stop(); _ = app.Close(); _ = control.Close() }()
	// This accept-loop worker remains counted while registering child workers.
	workers.Add(1)
	go func() {
		defer workers.Done()
		for accepted := 0; ; {
			c, err := control.Accept()
			if err != nil {
				return
			}
			accepted++
			if accepted > agentRelayAcceptBudget {
				_ = c.Close()
				cancel()
				return
			}
			if !sockets.register(c) {
				continue
			}
			if !agentRelayPeer(c, config.HostIP) {
				sockets.release(c)
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				handedOff := false
				defer func() {
					if !handedOff {
						sockets.release(c)
					}
				}()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(io.LimitReader(c, 256))
				token, err := reader.ReadString('\n')
				if err != nil || !secret.EqualFixedBytes([]byte(token), []byte(agentRelayToken(config))) {
					return
				}
				_ = c.SetDeadline(time.Now().Add(agentRelayLifetime))
				if _, err = io.WriteString(c, "READY "+config.OperationDigest+"\n"); err != nil {
					return
				}
				select {
				case queue <- c:
					handedOff = true
				case <-ctx.Done():
				}
			}()
		}
	}()
	streams := 0
	for accepted := 0; ; {
		c, err := app.Accept()
		if err != nil {
			break
		}
		accepted++
		if accepted > agentRelayAcceptBudget {
			_ = c.Close()
			cancel()
			break
		}
		if !sockets.register(c) {
			continue
		}
		if !agentRelayPeer(c, config.AppIP) || streams >= agentRelayStreamBudget {
			sockets.release(c)
			continue
		}
		streams++
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer sockets.release(c)
			_ = c.SetDeadline(time.Now().Add(agentRelayLifetime))
			var upstream net.Conn
			select {
			case upstream = <-queue:
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				return
			}
			defer sockets.release(upstream)
			if _, err := io.WriteString(upstream, "STREAM "+config.OperationDigest+"\n"); err != nil {
				return
			}
			copyAgentRelay(c, upstream)
		}()
	}
	cancel()
	<-closed
	workers.Wait()
	if err := parent.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return errAgentTransition
	}
	return nil
}
