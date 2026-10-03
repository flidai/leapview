package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// metricsListener has a separate listener and lifecycle so the public app
// router cannot expose metrics through the customer-facing reverse proxy.
// Operators must restrict the configured listener in their network/container.
type metricsListener struct {
	address  string
	handler  http.Handler
	listener net.Listener
	server   *http.Server
	fatal    chan error
}

func newMetricsListener(address string, handler http.Handler) *metricsListener {
	return &metricsListener{address: address, handler: handler, fatal: make(chan error, 1)}
}

func (m *metricsListener) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.server != nil {
		return errors.New("metrics listener already started")
	}
	listener, err := net.Listen("tcp", m.address)
	if err != nil {
		return fmt.Errorf("listen for private metrics: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.handler)
	m.listener = listener
	m.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := m.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.fatal <- fmt.Errorf("private metrics listener: %w", err)
		}
	}()
	return nil
}

func (m *metricsListener) Stop(ctx context.Context) error {
	if m.server == nil {
		return nil
	}
	if err := m.server.Shutdown(ctx); err != nil {
		return errors.Join(err, m.server.Close())
	}
	return nil
}

func (m *metricsListener) Fatal() <-chan error { return m.fatal }
