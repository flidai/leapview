package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flidai/leapview/internal/app"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform/locking"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/spf13/cobra"
)

const defaultHTTPServerShutdownTimeout = 15 * time.Second

// ordinaryResponseWriteTimeout bounds a response that never becomes a
// stream.  SSE responses use streamResponseIdleTimeout instead, which is
// refreshed after every write/flush so a healthy long-lived stream is not
// terminated by an absolute server WriteTimeout.
const (
	ordinaryResponseWriteTimeout = 5 * time.Minute
	streamResponseIdleTimeout    = 2 * time.Minute
)

func serveCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the LeapView HTTP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.environment = serveEnvironmentFlagValue(cmd.Flags().Changed("environment"), opts.environment)
			return runServe(ctx, opts)
		},
	}
	cmd.Flags().StringVar(&opts.addr, "addr", "", "listen address; defaults to the configured address")
	cmd.Flags().StringVar(&opts.environment, "environment", "", "instance environment; overrides LEAPVIEW_ENVIRONMENT, then defaults to prod in production and dev otherwise")
	cmd.Flags().BoolVar(&opts.production, "production", false, "serve active state from the native PostgreSQL control plane")
	return cmd
}

func runServe(ctx context.Context, opts *rootOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	production := serveProductionMode(cfg, *opts)
	cfg.Production = production
	addr := opts.addr
	if addr == "" {
		addr = cfg.ListenAddr()
	}
	parsedAddr, err := config.ParseListenAddr(addr)
	if err != nil {
		return err
	}
	addr = parsedAddr.String()
	cfg.Addr = addr
	if err := cfg.Validate(config.ProfileServe); err != nil {
		return err
	}
	environment := serveEnvironment(production, opts.environment, cfg.Environment)
	cfg.Environment = string(environment)
	instanceLock, err := instancelock.Acquire(cfg.HomeDir)
	if err != nil {
		return err
	}
	defer instanceLock.Release()
	application, err := app.Build(ctx, cfg)
	if err != nil {
		return err
	}
	serveCtx, stopServe := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopServe()
	if err := application.Start(serveCtx); err != nil {
		_ = application.Shutdown(context.Background())
		return err
	}
	fatalErr := make(chan error, 1)
	go func() {
		select {
		case <-serveCtx.Done():
		case err := <-application.Fatal():
			fatalErr <- err
			stopServe()
		}
	}()
	slog.Info("LeapView listening", "url", listenURL(addr), "environment", environment)
	err = runHTTPServer(serveCtx, productionHTTPServer(addr, application.Handler()))
	stopServe()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultHTTPServerShutdownTimeout)
	defer cancel()
	if stopErr := application.Shutdown(shutdownCtx); err == nil && stopErr != nil {
		err = stopErr
	}
	select {
	case fatal := <-fatalErr:
		return fatal
	default:
	}
	return err
}

func serveProductionMode(cfg config.Config, opts rootOptions) bool {
	return opts.production || cfg.Production
}

func serveEnvironment(production bool, flagValue, configuredValue string) servingstate.Environment {
	if value := strings.TrimSpace(flagValue); value != "" {
		return servingstate.NormalizeEnvironment(servingstate.Environment(value))
	}
	if value := strings.TrimSpace(configuredValue); value != "" {
		return servingstate.NormalizeEnvironment(servingstate.Environment(value))
	}
	if production {
		return servingstate.Environment("prod")
	}
	return servingstate.DefaultEnvironment
}

func serveEnvironmentFlagValue(changed bool, value string) string {
	if !changed {
		return ""
	}
	return value
}

func listenURL(addr string) string {
	parsed, err := config.ParseListenAddr(addr)
	if err != nil {
		return ""
	}
	host := parsed.Host
	if host == "" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(parsed.Port))
}

func productionHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           withResponseLiveness(handler, ordinaryResponseWriteTimeout, streamResponseIdleTimeout),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout is intentionally zero.  It is an absolute connection
		// deadline and would terminate healthy Datastar/SSE streams.  The
		// response wrapper above applies an ordinary-response deadline and an
		// idle deadline for streams on each connection instead.
		IdleTimeout:    2 * time.Minute,
		MaxHeaderBytes: 1 << 20,
	}
}

func runHTTPServer(ctx context.Context, server *http.Server) error {
	if server == nil {
		return errors.New("http server is required")
	}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case err := <-errCh:
		return err
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultHTTPServerShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	}
}
