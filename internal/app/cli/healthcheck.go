package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/config/spec"
	"github.com/spf13/cobra"
)

const defaultHealthcheckURL = "http://127.0.0.1:8080/readyz"

func healthcheckCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Check the local LeapView readiness endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runHealthcheck(ctx, opts, cmd.OutOrStdout())
		},
	}
	timeout := opts.healthcheckTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cmd.Flags().StringVar(&opts.healthcheckURL, "url", opts.healthcheckURL, "readiness URL to check")
	cmd.Flags().DurationVar(&opts.healthcheckTimeout, "timeout", timeout, "healthcheck HTTP timeout")
	return cmd
}

func runHealthcheck(ctx context.Context, opts *rootOptions, out io.Writer) error {
	url := healthcheckURL(opts)
	timeout := opts.healthcheckTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("readiness endpoint returned status %d", resp.StatusCode)
	}
	_, err = fmt.Fprintln(out, "ready")
	return err
}

func healthcheckURL(opts *rootOptions) string {
	if opts != nil {
		if url := strings.TrimSpace(opts.healthcheckURL); url != "" {
			return url
		}
	}
	if url := strings.TrimSpace(os.Getenv(configspec.EnvLEAPVIEW_HEALTHCHECK_URL)); url != "" {
		return url
	}
	if url := healthcheckURLForListenAddr(os.Getenv(configspec.EnvLEAPVIEW_ADDR)); url != "" {
		return url
	}
	return defaultHealthcheckURL
}

func healthcheckURLForListenAddr(addr string) string {
	host, port := healthcheckListenHostPort(addr)
	if strings.TrimSpace(port) == "" {
		return ""
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/readyz"
}

func healthcheckListenHostPort(addr string) (string, string) {
	if strings.TrimSpace(addr) == "" {
		return "", ""
	}
	parsed, err := config.ParseListenAddr(addr)
	if err != nil {
		return "", ""
	}
	return parsed.Host, strconv.Itoa(parsed.Port)
}
