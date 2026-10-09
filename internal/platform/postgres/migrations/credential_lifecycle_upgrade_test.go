package migrations

import "testing"

func TestCredentialLifecycleUpgradePreservesReleasedRevision59(t *testing.T) {
	pool, db, provider := newDemoUpgradeDatabase(t)
	if _, err := provider.UpTo(t.Context(), 59); err != nil {
		t.Fatal(err)
	}
	history := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(t.Context(), `SELECT json_agg(row(id, version_id, is_applied, tstamp) ORDER BY id)::text
			FROM public.goose_db_version WHERE version_id <= 59`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := history()
	var epochBefore int64
	if err := pool.QueryRow(t.Context(), `SELECT epoch FROM managed_data.reachability_epoch WHERE singleton`).Scan(&epochBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("upgrade released revision 59: %v", err)
	}
	if err := VerifyGoose(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if got := history(); got != before {
		t.Fatalf("released migration history changed: before=%s after=%s", before, got)
	}
	var epochAfter int64
	var completion, agentVersion, requests, rewrap bool
	if err := pool.QueryRow(t.Context(), `SELECT epoch,
		EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='credential' AND table_name='activation_preparation' AND column_name='completed_at'),
		EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='agent' AND table_name='configuration_revisions' AND column_name='credential_version_id'),
		to_regclass('credential.activation_request') IS NOT NULL,
		EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='credential' AND p.proname='rewrap_envelope')
		FROM managed_data.reachability_epoch WHERE singleton`).Scan(&epochAfter, &completion, &agentVersion, &requests, &rewrap); err != nil {
		t.Fatal(err)
	}
	if epochAfter != epochBefore || !completion || !agentVersion || !requests || !rewrap {
		t.Fatalf("credential upgrade lost released GC state or new schema: epoch=%d/%d completion=%t agent=%t requests=%t rewrap=%t", epochBefore, epochAfter, completion, agentVersion, requests, rewrap)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("replay completed upgrade: %v", err)
	}
	if got := history(); got != before {
		t.Fatal("upgrade replay changed released migration history")
	}
}
