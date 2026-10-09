package migrations

import (
	"io/fs"
	"strings"
	"testing"

	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	"github.com/stretchr/testify/require"
)

func TestCredentialRetirementUpgradeFencesEveryRetainedReference(t *testing.T) {
	pool, db, provider := newDemoUpgradeDatabase(t)
	_, err := provider.UpTo(t.Context(), 65)
	require.NoError(t, err)
	var before string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT json_agg(row(id,version_id,is_applied,tstamp) ORDER BY id)::text FROM goose_db_version`).Scan(&before))
	_, err = provider.Up(t.Context())
	require.NoError(t, err)
	require.NoError(t, VerifyGoose(t.Context(), db))
	var history string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT json_agg(row(id,version_id,is_applied,tstamp) ORDER BY id)::text FROM goose_db_version WHERE version_id<=65`).Scan(&history))
	require.Equal(t, before, history)
	for table, trigger := range map[string]string{
		"credential.validation_receipt": "validation_receipt_version_available",
		"credential.activation_request": "activation_request_version_available",
		"release.candidate_provenance":  "candidate_credential_version_available",
		"release.release_record":        "release_credential_version_available",
		"agent.configuration_revisions": "agent_credential_version_available",
		"credential.version_retirement": "version_retirement_retained_references",
	} {
		var enabled bool
		require.NoError(t, pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=$1::regclass AND tgname=$2 AND tgenabled='O')`, table, trigger).Scan(&enabled))
		require.True(t, enabled, table)
	}
	var canRead, canInsert, canUpdate, canDelete, canBackup bool
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT has_table_privilege('leapview_control_runtime','credential.version_retirement','SELECT'),has_table_privilege('leapview_control_runtime','credential.version_retirement','INSERT'),has_table_privilege('leapview_control_runtime','credential.version_retirement','UPDATE'),has_table_privilege('leapview_control_runtime','credential.version_retirement','DELETE'),has_table_privilege('leapview_control_backup','credential.version_retirement','SELECT')`).Scan(&canRead, &canInsert, &canUpdate, &canDelete, &canBackup))
	require.True(t, canRead && canInsert && canBackup)
	require.False(t, canUpdate || canDelete)
	_, err = provider.Down(t.Context())
	require.ErrorContains(t, err, "retirement is forward-only")
	require.NoError(t, VerifyGoose(t.Context(), db))
	body, err := fs.ReadFile(MigrationFS(), "066_credential_version_retirement.sql")
	require.NoError(t, err)
	start := strings.Index(string(body), "-- Retirement preserves encrypted history")
	end := strings.Index(string(body), "-- Application composition")
	require.Greater(t, end, start)
	require.Contains(t, credentialpostgres.SchemaSQL(), strings.TrimSpace(string(body)[start:end]))
}
