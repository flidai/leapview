package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
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
