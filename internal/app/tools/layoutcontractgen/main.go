package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	sourcePath = "internal/dashboard/layoutcontract/contracts.json"
	targetPath = "web/generated/dashboard-layout/contracts.json"
)

func main() {
	if err := generate(sourcePath, targetPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(source, target string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read layout contract: %w", err)
	}
	var document struct {
		Version int                        `json:"version"`
		Widgets map[string]json.RawMessage `json:"widgets"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode layout contract: %w", err)
	}
	if document.Version != 1 || len(document.Widgets) == 0 {
		return errors.New("layout contract requires version 1 and at least one widget")
	}

	formatted := append([]byte(nil), bytes.TrimSpace(raw)...)
	formatted = append(formatted, '\n')

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create layout contract output directory: %w", err)
	}
	if err := os.WriteFile(target, formatted, 0o644); err != nil {
		return fmt.Errorf("write browser layout contract: %w", err)
	}
	return nil
}
