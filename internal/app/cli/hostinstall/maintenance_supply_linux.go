//go:build linux

package hostinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The extension supply is built into each admitted image. Equal source module
// versions alone cannot prove equal DuckLake extensions downloaded at build time.
func imageSupply(ctx context.Context, image string) ([]byte, error) {
	create := exec.CommandContext(ctx, "docker", "create", image)
	raw, err := create.Output()
	if err != nil {
		return nil, errors.New("cannot inspect admitted image supply")
	}
	id := strings.TrimSpace(string(raw))
	if !dockerSelector.MatchString(id) {
		return nil, errors.New("invalid image container identity")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-v", id).Run()
	}()
	directory, err := os.MkdirTemp("", "host-supply-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "supply.json")
	cp := exec.CommandContext(ctx, "docker", "cp", id+":/usr/local/share/leapview/extensions/extension-supply.json", path)
	cp.Stderr = io.Discard
	if cp.Run() != nil {
		return nil, errors.New("image lacks immutable extension supply")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest map[string]any
	if len(data) > 4<<20 || json.Unmarshal(data, &manifest) != nil || manifest["duckdbVersion"] == nil || manifest["artifacts"] == nil {
		return nil, errors.New("invalid immutable extension supply")
	}
	return json.Marshal(manifest)
}

func validateImageSupplies(ctx context.Context, r NativeRequest) error {
	before, err := imageSupply(ctx, r.PredecessorImage)
	if err != nil {
		return err
	}
	after, err := imageSupply(ctx, r.CandidateImage)
	if err != nil {
		return err
	}
	if !bytes.Equal(before, after) {
		return errors.New("DuckDB/extension supply changed; engine upgrade requires separate qualification")
	}
	return nil
}

func requireLocalDocker(ctx context.Context) error {
	if os.Getenv("DOCKER_HOST") != "" || (os.Getenv("DOCKER_CONTEXT") != "" && os.Getenv("DOCKER_CONTEXT") != "default") {
		return errors.New("remote or overridden Docker endpoints are unsupported")
	}
	raw, err := exec.CommandContext(ctx, "docker", "context", "show").Output()
	if err != nil || strings.TrimSpace(string(raw)) != "default" {
		return errors.New("maintenance requires the local default Docker context")
	}
	return nil
}
