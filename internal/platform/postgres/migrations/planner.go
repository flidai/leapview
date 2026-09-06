package migrations

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
)

// A transaction advisory lock is scoped by PostgreSQL to the current database.
// The fixed key names this migration authority, not an environment or resource.
const migrationLockSQL = `SELECT pg_advisory_xact_lock(1279612496, 1)`

//go:embed 002_supersession.json
var supersessionJSON string

type revisionRecord struct {
	Revision int64  `json:"revision"`
	ID       string `json:"migration_id"`
	Checksum string `json:"checksum"`
}

type supersession struct {
	Original    revisionRecord `json:"original"`
	Replacement revisionRecord `json:"replacement"`
	Reason      string         `json:"reason"`
}

func identitySupersession() (supersession, error) {
	var declaration supersession
	if err := json.Unmarshal([]byte(supersessionJSON), &declaration); err != nil {
		return declaration, fmt.Errorf("decode migration supersession: %w", err)
	}
	if declaration.Original != (revisionRecord{2, IdentityLedgerMigrationID, IdentityLedgerChecksum()}) ||
		declaration.Replacement != (revisionRecord{2, IdentityLedgerReplacementMigrationID, IdentityLedgerReplacementChecksum()}) ||
		declaration.Reason == "" {
		return declaration, errors.New("migration supersession artifact/metadata mismatch")
	}
	return declaration, nil
}

// inspectHistory reads a complete ordered ledger in one statement. Unknown
// revisions cannot hide beyond the last expected revision. A missing ledger is
// bootstrap only when there are no user schemas or user relations to adopt.
func inspectHistory(ctx context.Context, reader RevisionReader) ([]revisionRecord, error) {
	var exists bool
	if err := reader.QueryRow(ctx, `SELECT to_regclass('platform.schema_revision') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("inspect migration ledger: %w", err)
	}
	if !exists {
		var occupied bool
		if err := reader.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM pg_namespace WHERE nspname <> 'public'
				AND nspname <> 'information_schema' AND nspname !~ '^pg_')
			OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','S','f'))`).Scan(&occupied); err != nil {
			return nil, fmt.Errorf("inspect bootstrap database: %w", err)
		}
		if occupied {
			return nil, errors.New("migration ledger missing in non-empty database; adoption is unsupported")
		}
		return nil, nil
	}
	var encoded []byte
	if err := reader.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object(
		'revision', revision, 'migration_id', migration_id, 'checksum', checksum)
		ORDER BY revision), '[]'::jsonb) FROM platform.schema_revision`).Scan(&encoded); err != nil {
		return nil, fmt.Errorf("read migration history: %w", err)
	}
	var records []revisionRecord
	if err := json.Unmarshal(encoded, &records); err != nil {
		return nil, fmt.Errorf("decode migration history: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("empty existing migration ledger; partial bootstrap is unsupported")
	}
	return records, nil
}

func validateHistory(records []revisionRecord, declaration supersession) error {
	wants := ordered()
	if len(records) > len(wants) {
		return errors.New("unknown future PostgreSQL migration history")
	}
	for i, got := range records {
		want := wants[i]
		expected := revisionRecord{want.revision, want.id, want.checksum}
		if got != expected && !(want.revision == 2 && got == declaration.Original) {
			return fmt.Errorf("PostgreSQL schema revision mismatch or non-contiguous history at revision %d: got revision=%d migration=%q checksum=%q", want.revision, got.Revision, got.ID, got.Checksum)
		}
	}
	return nil
}
