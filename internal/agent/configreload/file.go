package configreload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/agent"
)

const (
	maxConfigBytes       = 64 << 10
	DefaultCheckInterval = 2 * time.Second
)

// FileConfig is the complete deployment-owned agent configuration. APIKey is
// deliberately confined to this server-side loader and agent.Config.
type FileConfig struct {
	Enabled         bool   `json:"enabled"`
	APIKey          string `json:"apiKey"`
	Model           string `json:"model"`
	BaseURL         string `json:"baseUrl,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

func (c FileConfig) agentConfig() agent.Config {
	return agent.Config{APIKey: strings.TrimSpace(c.APIKey), Model: strings.TrimSpace(c.Model), BaseURL: strings.TrimSpace(c.BaseURL), ReasoningEffort: strings.TrimSpace(c.ReasoningEffort)}
}

type FileReloader struct {
	path     string
	service  *agent.Service
	logger   *slog.Logger
	interval time.Duration
	mu       sync.Mutex
	digest   [sha256.Size]byte
	loaded   bool
}

func NewFileReloader(path string, service *agent.Service, logger *slog.Logger, interval time.Duration) (*FileReloader, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("agent configuration file path is required")
	}
	if service == nil {
		return nil, fmt.Errorf("agent service is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = DefaultCheckInterval
	}
	return &FileReloader{path: path, service: service, logger: logger, interval: interval}, nil
}

// Reload reads, validates, and atomically applies one complete file revision.
// Invalid revisions never replace the service's last known-good snapshot.
func (r *FileReloader) Reload() (bool, error) {
	if r.service.AdminManaged() {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := readSecureFile(r.path)
	if err != nil {
		r.service.ReportDeploymentConfigError()
		return false, err
	}
	digest := sha256.Sum256(raw)
	if r.loaded && digest == r.digest {
		return false, nil
	}
	r.digest, r.loaded = digest, true
	config, err := decode(raw)
	if err != nil {
		r.service.ReportDeploymentConfigError()
		return false, err
	}
	if err := r.service.ApplyRuntimeConfig(config.agentConfig(), config.Enabled); err != nil {
		r.service.ReportDeploymentConfigError()
		return false, err
	}
	return true, nil
}

func (r *FileReloader) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			changed, err := r.Reload()
			if err != nil {
				r.logger.ErrorContext(ctx, "agent configuration reload rejected; retaining last known-good configuration", "path", r.path, "error", err)
			} else if changed {
				status := r.service.RuntimeStatus()
				r.logger.InfoContext(ctx, "agent configuration reloaded", "path", r.path, "state", status.State, "model", status.Model, "reasoning_effort", status.ReasoningEffort)
			}
		}
	}
}

func readSecureFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read agent configuration: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect agent configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("agent configuration must be a regular file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("agent configuration must not be group- or world-writable")
	}
	if info.Size() > maxConfigBytes {
		return nil, fmt.Errorf("agent configuration exceeds %d bytes", maxConfigBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read agent configuration: %w", err)
	}
	if len(raw) > maxConfigBytes {
		return nil, fmt.Errorf("agent configuration exceeds %d bytes", maxConfigBytes)
	}
	return raw, nil
}

func decode(raw []byte) (FileConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config FileConfig
	if err := decoder.Decode(&config); err != nil {
		return FileConfig{}, fmt.Errorf("decode agent configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return FileConfig{}, fmt.Errorf("decode agent configuration: multiple JSON values")
	}
	return config, nil
}
