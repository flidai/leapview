package migrations

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCredentialRetirementAcceptsProvenanceWithoutBindingReferences(t *testing.T) {
	pool, _, provider := newDemoUpgradeDatabase(t)
	_, err := provider.Up(t.Context())
	require.NoError(t, err)
	for _, provenance := range []string{`{"plan":{}}`, `{"plan":{"bindings":null}}`, `{"plan":{"bindings":[]}}`} {
		t.Run(provenance, func(t *testing.T) {
			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)
			defer tx.Rollback(context.Background())
			_, err = tx.Exec(t.Context(), `INSERT INTO release.candidate_provenance(project_id,candidate_id,candidate_revision,provenance_digest,provenance) VALUES('project',$1,1,'sha256:'||repeat('b',64),$2::jsonb)`, uuid.NewString(), provenance)
			require.NoError(t, err)
			_, err = tx.Exec(t.Context(), `INSERT INTO release.release_record(release_id,project_id,environment,generation_id,project_digest,artifact_digest,request_digest,idempotency_key,created_by,provenance) VALUES($1,'project','prod',$1,'sha256:'||repeat('a',64),'sha256:'||repeat('b',64),'sha256:'||repeat('c',64),$1,'actor',$2::jsonb)`, uuid.NewString(), provenance)
			require.NoError(t, err)
		})
	}
}
