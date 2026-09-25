package hostinstall

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

// startTCPRelay forwards opaque TLS bytes. Docker internal networks intentionally
// do not publish ports, but the host can reach their bridge addresses. The
// listener stays on loopback; TLS identity remains checked by the remote browser.
func startTCPRelay(ctx context.Context, listen, target string) (string, func(), error) {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var clients sync.WaitGroup
	done := make(chan struct{})
	go func() { <-ctx.Done(); _ = listener.Close() }()
	go func() {
		defer close(done)
		for {
			incoming, err := listener.Accept()
			if err != nil {
				return
			}
			clients.Add(1)
			go func() {
				defer clients.Done()
				defer incoming.Close()
				outgoing, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", target)
				if err != nil {
					return
				}
				defer outgoing.Close()
				connectionDone := make(chan struct{})
				go func() {
					select {
					case <-ctx.Done():
						_ = incoming.Close()
						_ = outgoing.Close()
					case <-connectionDone:
					}
				}()
				copied := make(chan struct{})
				go func() { _, _ = io.Copy(outgoing, incoming); _ = outgoing.Close(); close(copied) }()
				_, _ = io.Copy(incoming, outgoing)
				_ = incoming.Close()
				<-copied
				close(connectionDone)
			}()
		}
	}()
	closeRelay := func() { cancel(); <-done; clients.Wait() }
	return listener.Addr().String(), closeRelay, nil
}
