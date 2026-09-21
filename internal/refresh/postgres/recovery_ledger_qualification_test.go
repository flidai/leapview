//go:build fai1001qualification

package postgres

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFAI1001PostgresRecoveryLedgerQualification(t *testing.T) {
	pool := recoveryLedgerRuntimePool(t)
	evidence := exerciseRecoveryLedger(t, pool)
	directory := os.Getenv("LEAPVIEW_TEST_FAI1001_RECOVERY_LEDGER_EVIDENCE_DIR")
	if directory == "" {
		t.Fatal("LEAPVIEW_TEST_FAI1001_RECOVERY_LEDGER_EVIDENCE_DIR is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Qualification string                 `json:"qualification"`
		Database      string                 `json:"database"`
		Evidence      recoveryLedgerEvidence `json:"evidence"`
	}{SchemaVersion: 1, Qualification: "FAI-1001", Database: "PostgreSQL 18", Evidence: evidence}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(directory, "recovery-ledger-postgres.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("qualification evidence was not persisted: info=%v err=%v", info, err)
	}
	t.Logf("FAI-1001 PostgreSQL recovery ledger evidence: %s", path)
}
