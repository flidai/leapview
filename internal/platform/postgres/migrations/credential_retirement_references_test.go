package migrations

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/credential"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type retirementMigrationAudit struct{}

func (retirementMigrationAudit) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	return err
}
func TestCredentialRetirementDatabaseGuardsRetainedReferencesAndRacingWriters(t *testing.T) {
	pool, _, provider := newDemoUpgradeDatabase(t)
	_, err := provider.Up(t.Context())
	require.NoError(t, err)
	repository, err := credentialpostgres.New(pool, retirementMigrationAudit{})
	require.NoError(t, err)
	resource := credential.Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "prod", ResourceID: "connection"}
	save := func() string {
		version := uuid.NewString()
		_, err := pool.Exec(t.Context(), `INSERT INTO credential.draft_version(version_id,deployment_id,owner_id,scope_kind,target_id,project_id,environment,resource_id,purpose,provider,destination,actor_id,created_at) VALUES($1,'deployment','owner','connection','target','project','prod','connection','connection','postgres','sha256:'||repeat('a',64),'actor',clock_timestamp())`, version)
		require.NoError(t, err)
		return version
	}
	noRefs := func(context.Context, pgx.Tx, credential.Metadata) ([]credential.VersionDependency, error) {
		return nil, nil
	}
	retire := func(tx pgx.Tx, version string) error {
		_, err := repository.RetireVersionTx(t.Context(), tx, "deployment", "owner", "actor", resource, version, noRefs)
		return err
	}
	candidates := `INSERT INTO release.candidate_provenance(project_id,candidate_id,candidate_revision,provenance_digest,provenance) VALUES('project',$1,1,'sha256:'||repeat('b',64),$2::jsonb)`
	releases := `INSERT INTO release.release_record(release_id,project_id,environment,generation_id,project_digest,artifact_digest,request_digest,idempotency_key,created_by,provenance) VALUES($1,'project','prod',$1,'sha256:'||repeat('a',64),'sha256:'||repeat('b',64),'sha256:'||repeat('c',64),$1,'actor',$2::jsonb)`
	provenance := func(version string) []byte {
		body, err := json.Marshal(map[string]any{"plan": map[string]any{"bindings": []map[string]string{{"credentialVersionId": version}}}})
		require.NoError(t, err)
		return body
	}
	for index, kind := range []string{"candidate", "release", "agent"} {
		t.Run(kind, func(t *testing.T) {
			insert := func(tx pgx.Tx, version string) error {
				if kind == "agent" {
					_, err := tx.Exec(t.Context(), `INSERT INTO agent.configuration_revisions(revision,enabled,config_json,credential,credential_version_id,actor_id) VALUES($1,false,'{}','',$2,'actor')`, index+1, version)
					return err
				}
				sql := candidates
				if kind == "release" {
					sql = releases
				}
				_, err := tx.Exec(t.Context(), sql, uuid.NewString(), provenance(version))
				return err
			}
			referenced := save()
			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)
			defer tx.Rollback(context.Background())
			require.NoError(t, insert(tx, referenced))
			require.NoError(t, tx.Commit(t.Context()))
			tx, err = pool.Begin(t.Context())
			require.NoError(t, err)
			defer tx.Rollback(context.Background())
			require.ErrorIs(t, retire(tx, referenced), credential.ErrUnavailable)
			require.NoError(t, tx.Commit(t.Context()))
			retired := save()
			tx, err = pool.Begin(t.Context())
			require.NoError(t, err)
			defer tx.Rollback(context.Background())
			require.NoError(t, retire(tx, retired))
			require.NoError(t, tx.Commit(t.Context()))
			tx, err = pool.Begin(t.Context())
			require.NoError(t, err)
			defer tx.Rollback(context.Background())
			require.ErrorContains(t, insert(tx, retired), "retired locally")
			_ = tx.Rollback(t.Context())
		})
	}
	version := save()
	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	require.NoError(t, retire(tx, version))
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := pool.Exec(ctx, candidates, uuid.NewString(), provenance(version))
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("reference writer escaped uncommitted retirement fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(t.Context()))
	select {
	case err := <-result:
		require.ErrorContains(t, err, "retired locally")
	case <-time.After(5 * time.Second):
		t.Fatal("reference writer did not finish")
	}
}
