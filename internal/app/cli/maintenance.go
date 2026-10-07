package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/spf13/cobra"
)

func maintenanceCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{Use: "maintenance", Short: "Control a local managed maintenance process through its private socket"}
	for _, action := range []string{"status", "prepare", "open", "finalize", "close"} {
		var socket, revision, operation string
		var lease, timeout time.Duration
		command := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 || timeout > 24*time.Hour {
				return errors.New("finite maintenance control timeout required")
			}
			if !filepath.IsAbs(socket) {
				return errors.New("absolute private maintenance socket required")
			}
			payload, err := json.Marshal(map[string]any{"revision": revision, "operation": operation, "leaseMilliseconds": lease.Milliseconds()})
			if err != nil {
				return err
			}
			method := http.MethodPost
			if action == "status" {
				method = http.MethodGet
			}
			request, err := http.NewRequestWithContext(ctx, method, "http://maintenance/"+action, bytes.NewReader(payload))
			if err != nil {
				return err
			}
			transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: timeout}
			response, err := client.Do(request)
			if err != nil {
				return err
			}
			defer response.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(response.Body, 8192))
			if err != nil {
				return err
			}
			if response.StatusCode != 200 {
				return fmt.Errorf("maintenance control returned %d: %s", response.StatusCode, raw)
			}
			_, err = cmd.OutOrStdout().Write(raw)
			return err
		}}
		effect := "local-write"
		if action == "status" {
			effect = "read"
		}
		command.Annotations = map[string]string{documentationEffectAnnotation: effect, documentationConfirmationAnnotation: "never"}
		command.Flags().StringVar(&socket, "socket", "", "private Unix control socket")
		command.Flags().StringVar(&revision, "revision", "", "expected immutable process revision")
		command.Flags().StringVar(&operation, "operation", "", "exact managed maintenance request digest")
		command.Flags().DurationVar(&lease, "lease", 0, "provisional worker admission lease")
		command.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "bounded control request timeout")
		cmd.AddCommand(command)
	}
	return cmd
}
func maintenanceListener(path string, handler http.Handler) (net.Listener, *http.Server, error) {
	if !filepath.IsAbs(path) {
		return nil, nil, errors.New("maintenance socket must be absolute")
	}
	if handler == nil {
		return nil, nil, errors.New("application maintenance mode unavailable")
	}
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, errors.New("maintenance socket path is not a socket")
		}
		if err = os.Remove(path); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, nil, err
	}
	return listener, &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}, nil
}
