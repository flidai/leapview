package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/extension"
)

func TestReadSupplyDocumentIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "supply.json")
	payload := bytes.Repeat([]byte{'x'}, maxExtensionSupplyDocumentBytes+1)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSupplyDocument(path); err == nil {
		t.Fatal("readSupplyDocument accepted an oversized supply document")
	}
}

func TestLoadExtensionSupplyFailsClosedWithoutPackageConfig(t *testing.T) {
	if _, err := loadExtensionSupply(nil, config.Config{}); !errors.Is(err, extension.ErrExtensionConfiguration) {
		t.Fatalf("loadExtensionSupply() error = %v, want extension configuration error", err)
	}
}
