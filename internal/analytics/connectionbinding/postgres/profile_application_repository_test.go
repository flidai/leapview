package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func profileApplicationDigest(marker byte) string {
	return "sha256:" + strings.Repeat(string(marker), 64)
}

func profileApplicationRecord(t *testing.T, status connectionbinding.ProfileApplicationStatus, evidence bool) connectionbinding.ProfileApplicationRecord {
	t.Helper()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	record := connectionbinding.ProfileApplicationRecord{
		ID: "application-local-1", CheckoutID: "checkout-1", RuntimeID: "runtime-1",
		TargetID: "target-local", ProjectID: projectgraph.ResourceID("project-1"), Environment: "dev",
		ProfileName: "local", GraphDigest: profileApplicationDigest('a'), SourceDigest: profileApplicationDigest('c'), ProfileDigest: profileApplicationDigest('b'),
		RequiredConnections: []connectionbinding.ProfileApplicationRequiredConnection{{ConnectionID: "connection-a", ConnectorKind: "postgres"}},
		ExpectedConnections: []connectionbinding.ProfileApplicationConnection{{BindingID: "binding-a", ConnectionID: "connection-a", ConnectorKind: "postgres", AuthenticationMode: connectionbinding.AuthenticationExternalBundle, Endpoint: connectionbinding.EndpointConfig{Host: "warehouse.internal", Port: 5432, Database: "analytics", TLSMode: "verify-full", Options: map[string]string{"application_name": "profile-test"}}, CredentialReference: connectionbinding.CredentialReference{ProjectID: "project-1", Environment: "dev", SecretPath: "/profiles/local", SecretKey: "warehouse"}, BindingRevision: 3, ProviderVersion: "provider-a"}},
		Status:              status, CreatedAt: now, UpdatedAt: now,
	}
	if evidence {
		applied := record.ExpectedConnections[0]
		applied.BindingRevision = 4
		record.AppliedConnections = []connectionbinding.ProfileApplicationConnection{applied}
	}
	if _, err := connectionbinding.NewProfileApplication(record); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	return record
}

func TestProfileApplicationRepositoryNewRetryAndRestartReadback(t *testing.T) {
	db := connectionBindingDB(t)
	repository := NewProfileApplicationRepository(db)
	initial := profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false)
	saved, err := repository.Save(t.Context(), initial, 0)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Revision != 1 || len(saved.RequiredConnections) != 1 || len(saved.AppliedConnections) != 0 {
		t.Fatalf("saved applying record = %#v", saved)
	}
	retry, err := repository.Save(t.Context(), saved, saved.Revision)
	if err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if retry.Revision != saved.Revision {
		t.Fatalf("retry revision = %d, want %d", retry.Revision, saved.Revision)
	}
	loaded, err := NewProfileApplicationRepository(db).Application(t.Context(), connectionbinding.ProfileApplicationScope{
		CheckoutID: saved.CheckoutID, RuntimeID: saved.RuntimeID, ProjectID: saved.ProjectID, Environment: saved.Environment,
	}, saved.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != saved.ID || loaded.GraphDigest != saved.GraphDigest || loaded.SourceDigest != saved.SourceDigest || loaded.ProfileDigest != saved.ProfileDigest || len(loaded.RequiredConnections) != 1 || len(loaded.AppliedConnections) != 0 {
		t.Fatalf("restart readback = %#v", loaded)
	}
}

func TestProfileApplicationRepositoryApplyingIncompleteAppliedAndEvidence(t *testing.T) {
	db, runtimeDB := connectionBindingDatabases(t)
	repository := NewProfileApplicationRepository(runtimeDB)
	saved, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	incomplete := saved
	incomplete.Status = connectionbinding.ProfileApplicationIncomplete
	incomplete.UpdatedAt = saved.UpdatedAt.Add(time.Minute)
	incomplete, err = repository.Save(t.Context(), incomplete, saved.Revision)
	if err != nil {
		t.Fatalf("applying to incomplete: %v", err)
	}
	if incomplete.Revision != saved.Revision+1 || incomplete.Status != connectionbinding.ProfileApplicationIncomplete {
		t.Fatalf("incomplete = %#v", incomplete)
	}
	applied := incomplete
	applied.Status = connectionbinding.ProfileApplicationApplied
	evidence := applied.ExpectedConnections[0]
	evidence.BindingRevision = 7
	seedProfileBindingEvidence(t, db, applied, evidence)
	applied.AppliedConnections = []connectionbinding.ProfileApplicationConnection{evidence}
	applied.UpdatedAt = incomplete.UpdatedAt.Add(time.Minute)
	applied, err = repository.Save(t.Context(), applied, incomplete.Revision)
	if err != nil {
		t.Fatalf("incomplete to applied: %v", err)
	}
	if applied.Status != connectionbinding.ProfileApplicationApplied || applied.Revision != incomplete.Revision+1 || len(applied.AppliedConnections) != 1 || applied.AppliedConnections[0].BindingRevision != 7 || applied.AppliedConnections[0].ProviderVersion != "provider-a" {
		t.Fatalf("applied evidence = %#v", applied)
	}
	loaded, err := repository.Application(t.Context(), recordScope(applied), applied.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != connectionbinding.ProfileApplicationApplied || !reflect.DeepEqual(loaded.AppliedConnections[0], applied.AppliedConnections[0]) {
		t.Fatalf("applied evidence readback = %#v", loaded)
	}
	if loaded.LastCompletedApplicationID != applied.ID || !loaded.LastCompletedAt.Equal(applied.UpdatedAt) {
		t.Fatalf("last completion readback = id %q at %s", loaded.LastCompletedApplicationID, loaded.LastCompletedAt)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE connection_binding.profile_application SET updated_at = updated_at + interval '1 second', revision = revision + 1 WHERE id = $1`, applied.ID.String()); err == nil {
		t.Fatal("runtime role mutated a terminal parent")
	}
	if _, err := runtimeDB.Exec(t.Context(), `INSERT INTO connection_binding.profile_application_required_connection (application_id, connection_id, connector_kind) VALUES ($1, 'connection-b', 'postgres')`, applied.ID.String()); err == nil {
		t.Fatal("runtime role appended required intent to a terminal application")
	}
	const destinationID = "application-local-destination"
	if _, err := runtimeDB.Exec(t.Context(), `
INSERT INTO connection_binding.profile_application (
    id, checkout_id, runtime_id, target_id, project_id, environment,
    profile_name, graph_digest, source_digest, profile_digest,
    retired_connections, status, revision, created_at, updated_at
)
SELECT $2, checkout_id, 'runtime-destination', target_id, project_id, environment,
       profile_name, graph_digest, source_digest, profile_digest,
       retired_connections, 'applying', 1, created_at, updated_at
  FROM connection_binding.profile_application WHERE id = $1`, applied.ID.String(), destinationID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `
INSERT INTO connection_binding.profile_application_required_connection
SELECT $2, connection_id, connector_kind
  FROM connection_binding.profile_application_required_connection WHERE application_id = $1`, applied.ID.String(), destinationID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `
INSERT INTO connection_binding.profile_application_expected_connection
SELECT $2, binding_id, connection_id, connector_kind, authentication_mode, endpoint_json,
       credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
       binding_revision, provider_version
  FROM connection_binding.profile_application_expected_connection WHERE application_id = $1`, applied.ID.String(), destinationID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE connection_binding.profile_application_applied_connection SET application_id = $2 WHERE application_id = $1`, applied.ID.String(), destinationID); err == nil {
		t.Fatal("runtime role moved evidence away from a terminal application")
	}
}

func TestProfileApplicationCompletionRevalidatesCurrentBinding(t *testing.T) {
	db, runtimeDB := connectionBindingDatabases(t)
	repository := NewProfileApplicationRepository(runtimeDB)
	saved, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	evidence := saved.ExpectedConnections[0]
	evidence.BindingRevision = 7
	seedProfileBindingEvidence(t, db, saved, evidence)
	incomplete := saved
	incomplete.Status = connectionbinding.ProfileApplicationIncomplete
	incomplete.AppliedConnections = []connectionbinding.ProfileApplicationConnection{evidence}
	incomplete.UpdatedAt = saved.UpdatedAt.Add(time.Minute)
	incomplete, err = repository.Save(t.Context(), incomplete, saved.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `UPDATE connection_binding.target_connection_binding SET enabled = false, health = 'disabled', updated_at = updated_at + interval '1 second', revision = revision + 1 WHERE id = $1`, evidence.BindingID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := completeProfileApplication(t.Context(), runtimeDB, incomplete, incomplete.UpdatedAt.Add(time.Minute)); err == nil {
		t.Fatal("completion accepted stale disabled binding evidence")
	}
	loaded, err := repository.Application(t.Context(), recordScope(incomplete), incomplete.TargetID)
	if err != nil || loaded.Status != connectionbinding.ProfileApplicationIncomplete {
		t.Fatalf("failed completion changed checkpoint: loaded=%#v err=%v", loaded, err)
	}
}

func TestProfileApplicationCompletionFailsClosedAgainstConcurrentEvidenceDeletion(t *testing.T) {
	db, runtimeDB := connectionBindingDatabases(t)
	repository := NewProfileApplicationRepository(runtimeDB)
	saved, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	evidence := saved.ExpectedConnections[0]
	evidence.BindingRevision = 7
	seedProfileBindingEvidence(t, db, saved, evidence)
	incomplete := saved
	incomplete.Status = connectionbinding.ProfileApplicationIncomplete
	incomplete.AppliedConnections = []connectionbinding.ProfileApplicationConnection{evidence}
	incomplete.UpdatedAt = saved.UpdatedAt.Add(time.Minute)
	incomplete, err = repository.Save(t.Context(), incomplete, saved.Revision)
	if err != nil {
		t.Fatal(err)
	}

	mutation, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := mutation.Exec(t.Context(), `DELETE FROM connection_binding.profile_application_applied_connection WHERE application_id = $1`, incomplete.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if deleted.RowsAffected() != 1 {
		t.Fatalf("deleted evidence rows = %d", deleted.RowsAffected())
	}
	completion := make(chan error, 1)
	go func() {
		_, completeErr := completeProfileApplication(context.Background(), runtimeDB, incomplete, incomplete.UpdatedAt.Add(time.Minute))
		completion <- completeErr
	}()
	select {
	case err := <-completion:
		if err == nil {
			t.Fatal("completion succeeded while evidence mutation held the parent fence")
		}
	case <-time.After(time.Second):
		t.Fatal("completion waited on concurrent evidence instead of failing closed")
	}
	if err := mutation.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Application(t.Context(), recordScope(incomplete), incomplete.TargetID)
	if err != nil || loaded.Status != connectionbinding.ProfileApplicationIncomplete || len(loaded.AppliedConnections) != 0 {
		t.Fatalf("concurrent deletion result status=%s applied=%d err=%v", loaded.Status, len(loaded.AppliedConnections), err)
	}
}

func completeProfileApplication(ctx context.Context, db DBTX, record connectionbinding.ProfileApplicationRecord, updatedAt time.Time) (int64, error) {
	var affected int64
	err := db.QueryRow(ctx, `SELECT connection_binding.complete_profile_application($1,$2,$3,$4,$5,$6,$7,$8)`,
		record.ID.String(), record.Revision, record.CheckoutID, record.RuntimeID, record.TargetID.String(), record.ProjectID.String(), record.Environment, updatedAt).Scan(&affected)
	return affected, err
}

func TestProfileApplicationRepositoryRequiresControlledTerminalCompletion(t *testing.T) {
	db, runtimeDB := connectionBindingDatabases(t)
	repository := NewProfileApplicationRepository(runtimeDB)
	empty := profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false)
	empty.ID = "application-empty"
	empty.RuntimeID = "runtime-empty"
	empty.RequiredConnections = nil
	empty.ExpectedConnections = nil
	saved, err := repository.Save(t.Context(), empty, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE connection_binding.profile_application SET status = 'applied', revision = revision + 1 WHERE id = $1`, saved.ID.String()); err == nil {
		t.Fatal("runtime role completed a checkpoint without the bounded completion function")
	}
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "SELECT set_config('connection_binding.profile_application_completion', 'on', true)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE connection_binding.profile_application SET status = 'applied', revision = revision + 1 WHERE id = $1`, saved.ID.String()); err == nil {
		t.Fatal("runtime role forged the bounded completion marker")
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `
INSERT INTO connection_binding.profile_application (
    id, checkout_id, runtime_id, target_id, project_id, environment,
    profile_name, graph_digest, source_digest, profile_digest,
    retired_connections, status, revision, created_at, updated_at
)
SELECT 'application-forged-terminal', checkout_id, 'runtime-forged-terminal', target_id, project_id, environment,
       profile_name, graph_digest, source_digest, profile_digest,
       retired_connections, 'applied', 1, created_at, updated_at
  FROM connection_binding.profile_application WHERE id = $1`, saved.ID.String()); err == nil {
		t.Fatal("runtime role inserted a terminal checkpoint directly")
	}
	applied := saved
	applied.Status = connectionbinding.ProfileApplicationApplied
	applied.UpdatedAt = saved.UpdatedAt.Add(time.Minute)
	completed, err := repository.Save(t.Context(), applied, saved.Revision)
	if err != nil {
		t.Fatalf("bounded completion: %v", err)
	}
	if completed.Status != connectionbinding.ProfileApplicationApplied || completed.Revision != saved.Revision+1 {
		t.Fatalf("completed checkpoint = %#v", completed)
	}
	replacement := empty
	replacement.ID = "application-empty-replacement"
	replacement.SourceDigest = profileApplicationDigest('d')
	replacement.UpdatedAt = completed.UpdatedAt.Add(time.Minute)
	replaced, err := NewProfileApplicationRepository(db).Replace(t.Context(), replacement, completed.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.LastCompletedApplicationID != completed.ID || !replaced.LastCompletedAt.Equal(completed.UpdatedAt) {
		t.Fatalf("replacement lost last completion: %#v", replaced)
	}
	replaced.Status = connectionbinding.ProfileApplicationIncomplete
	replaced.UpdatedAt = replaced.UpdatedAt.Add(time.Minute)
	replaced, err = NewProfileApplicationRepository(db).Save(t.Context(), replaced, replaced.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.LastCompletedApplicationID != completed.ID || !replaced.LastCompletedAt.Equal(completed.UpdatedAt) {
		t.Fatalf("incomplete replacement lost last completion: %#v", replaced)
	}
}

func TestProfileApplicationRepositoryRejectsRetiredIntentMutationInDatabase(t *testing.T) {
	_, runtimeDB := connectionBindingDatabases(t)
	repository := NewProfileApplicationRepository(runtimeDB)
	saved, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE connection_binding.profile_application SET retired_connections = '["forged"]'::jsonb, revision = revision + 1 WHERE id = $1`, saved.ID.String()); err == nil {
		t.Fatal("runtime role mutated immutable retired-connection intent")
	}
}

func TestProfileApplicationRepositoryRejectsAppliedEvidenceWithoutCurrentHealthyBinding(t *testing.T) {
	_, runtimeDB := connectionBindingDatabases(t)
	repository := NewProfileApplicationRepository(runtimeDB)
	saved, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	forged := saved
	forged.Status = connectionbinding.ProfileApplicationApplied
	evidence := forged.ExpectedConnections[0]
	evidence.BindingRevision = 7
	forged.AppliedConnections = []connectionbinding.ProfileApplicationConnection{evidence}
	forged.UpdatedAt = saved.UpdatedAt.Add(time.Minute)
	if _, err := repository.Save(t.Context(), forged, saved.Revision); err == nil {
		t.Fatal("profile admission checkpoint accepted evidence without a current healthy binding")
	}
	loaded, err := repository.Application(t.Context(), recordScope(saved), saved.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != connectionbinding.ProfileApplicationApplying || len(loaded.AppliedConnections) != 0 {
		t.Fatalf("forged evidence changed checkpoint: %#v", loaded)
	}
}

func seedProfileBindingEvidence(t *testing.T, db DBTX, application connectionbinding.ProfileApplicationRecord, evidence connectionbinding.ProfileApplicationConnection) {
	t.Helper()
	endpoint, err := json.Marshal(evidence.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(t.Context(), `
INSERT INTO connection_binding.target_connection_binding (
    id, target_id, connection_id, connector_kind, authentication_mode,
    project_id, environment, endpoint_json,
    credential_project_id, credential_environment, credential_secret_path, credential_secret_key,
    enabled, validated_version, health, health_reason, last_validated_at,
    created_at, updated_at, revision
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,true,$13,'healthy','',$14,$14,$14,$15)
`, evidence.BindingID.String(), application.TargetID.String(), evidence.ConnectionID.String(), evidence.ConnectorKind, string(evidence.AuthenticationMode),
		application.ProjectID.String(), application.Environment, endpoint,
		evidence.CredentialReference.ProjectID.String(), evidence.CredentialReference.Environment, evidence.CredentialReference.SecretPath, evidence.CredentialReference.SecretKey,
		evidence.ProviderVersion, application.UpdatedAt, evidence.BindingRevision)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProfileApplicationRepositoryRejectsStaleRevisionAndImmutableDrift(t *testing.T) {
	db := connectionBindingDB(t)
	repository := NewProfileApplicationRepository(db)
	saved, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	first := saved
	first.Status = connectionbinding.ProfileApplicationIncomplete
	first.UpdatedAt = saved.UpdatedAt.Add(time.Minute)
	if _, err := repository.Save(t.Context(), first, saved.Revision); err != nil {
		t.Fatal(err)
	}
	second := saved
	second.Status = connectionbinding.ProfileApplicationIncomplete
	second.UpdatedAt = saved.UpdatedAt.Add(2 * time.Minute)
	if _, err := repository.Save(t.Context(), second, saved.Revision); !errors.Is(err, connectionbinding.ErrProfileApplicationConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	for name, mutate := range map[string]func(*connectionbinding.ProfileApplicationRecord){
		"graph digest": func(record *connectionbinding.ProfileApplicationRecord) {
			record.GraphDigest = profileApplicationDigest('d')
		},
		"source digest": func(record *connectionbinding.ProfileApplicationRecord) {
			record.SourceDigest = profileApplicationDigest('e')
		},
		"profile digest": func(record *connectionbinding.ProfileApplicationRecord) {
			record.ProfileDigest = profileApplicationDigest('f')
		},
		"required kind": func(record *connectionbinding.ProfileApplicationRecord) {
			record.RequiredConnections[0].ConnectorKind = "mysql"
			record.ExpectedConnections[0].ConnectorKind = "mysql"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := first
			mutate(&candidate)
			if _, err := repository.Save(t.Context(), candidate, first.Revision); !errors.Is(err, connectionbinding.ErrProfileApplicationReplacement) {
				t.Fatalf("immutable drift error = %v", err)
			}
		})
	}
}

func TestProfileApplicationRepositoryUsesExactLookupScope(t *testing.T) {
	db := connectionBindingDB(t)
	repository := NewProfileApplicationRepository(db)
	saved, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	wrong := recordScope(saved)
	wrong.Environment = "other"
	if _, err := repository.Application(context.Background(), wrong, saved.TargetID); !errors.Is(err, connectionbinding.ErrProfileApplicationNotFound) {
		t.Fatalf("wrong environment lookup = %v", err)
	}
	if _, err := repository.Application(context.Background(), recordScope(saved), "other-target"); !errors.Is(err, connectionbinding.ErrProfileApplicationNotFound) {
		t.Fatalf("wrong target lookup = %v", err)
	}
}

func TestProfileApplicationRepositoryRequiresExplicitCASReplacement(t *testing.T) {
	db := connectionBindingDB(t)
	repository := NewProfileApplicationRepository(db)
	current, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	replacement := profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false)
	replacement.ID = "application-local-2"
	replacement.SourceDigest = profileApplicationDigest('d')
	replacement.GraphDigest = profileApplicationDigest('e')
	replacement.ProfileDigest = profileApplicationDigest('f')
	replacement.UpdatedAt = current.UpdatedAt.Add(time.Minute)

	if _, err := repository.Save(t.Context(), replacement, current.Revision); !errors.Is(err, connectionbinding.ErrProfileApplicationReplacement) {
		t.Fatalf("ordinary replacement error = %v", err)
	}
	if _, err := repository.Replace(t.Context(), replacement, current.Revision+1); !errors.Is(err, connectionbinding.ErrProfileApplicationConflict) {
		t.Fatalf("stale replacement error = %v", err)
	}
	sameID := replacement
	sameID.ID = current.ID
	if _, err := repository.Replace(t.Context(), sameID, current.Revision); !errors.Is(err, connectionbinding.ErrProfileApplicationReplacement) {
		t.Fatalf("same-ID repository replacement error = %v", err)
	}
	var affected int64
	if err := db.QueryRow(t.Context(), `SELECT connection_binding.delete_profile_application_for_replacement($1,$2,$3,$4,$5,$6,$7,$8)`,
		current.ID.String(), current.Revision, current.CheckoutID, current.RuntimeID, current.TargetID.String(), current.ProjectID.String(), current.Environment, current.ID.String()).Scan(&affected); err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("same-ID database replacement affected %d rows", affected)
	}
	saved, err := repository.Replace(t.Context(), replacement, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != replacement.ID || saved.Revision != current.Revision+1 || saved.CreatedAt != current.CreatedAt {
		t.Fatalf("replacement = %#v", saved)
	}
	loaded, err := repository.Application(t.Context(), recordScope(saved), saved.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != saved.ID || loaded.SourceDigest != replacement.SourceDigest || loaded.Revision != saved.Revision {
		t.Fatalf("replacement readback = %#v", loaded)
	}
}

func TestProfileApplicationRepositoryCanonicalizesEmptyEndpointOptions(t *testing.T) {
	db := connectionBindingDB(t)
	repository := NewProfileApplicationRepository(db)
	record := profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false)
	record.ExpectedConnections[0].Endpoint.Options = map[string]string{}
	saved, err := repository.Save(t.Context(), record, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved.Status = connectionbinding.ProfileApplicationIncomplete
	saved.UpdatedAt = saved.UpdatedAt.Add(time.Minute)
	if _, err := repository.Save(t.Context(), saved, saved.Revision); err != nil {
		t.Fatalf("resume after empty options readback: %v", err)
	}
}

func TestProfileApplicationRepositoryRejectsForgedReplacementMarker(t *testing.T) {
	db, runtimeDB := connectionBindingDatabases(t)
	repository := NewProfileApplicationRepository(db)
	saved, err := repository.Save(t.Context(), profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false), 0)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), "SELECT set_config('connection_binding.profile_application_replacement', 'on', true)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE connection_binding.profile_application SET source_digest = $1, revision = revision + 1 WHERE id = $2`, profileApplicationDigest('d'), saved.ID.String()); err == nil {
		t.Fatal("direct replacement marker bypassed the owner-executed capability")
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacement := profileApplicationRecord(t, connectionbinding.ProfileApplicationApplying, false)
	replacement.ID = "application-local-runtime-replacement"
	replacement.SourceDigest = profileApplicationDigest('d')
	replacement.UpdatedAt = saved.UpdatedAt.Add(time.Minute)
	if _, err := NewProfileApplicationRepository(runtimeDB).Replace(t.Context(), replacement, saved.Revision); err == nil {
		t.Fatal("runtime role invoked the owner-only replacement capability")
	}
	loaded, err := repository.Application(t.Context(), recordScope(saved), saved.TargetID)
	if err != nil || loaded.ID != saved.ID {
		t.Fatalf("runtime replacement changed retained checkpoint: loaded=%#v err=%v", loaded, err)
	}
}

func recordScope(record connectionbinding.ProfileApplicationRecord) connectionbinding.ProfileApplicationScope {
	return connectionbinding.ProfileApplicationScope{CheckoutID: record.CheckoutID, RuntimeID: record.RuntimeID, ProjectID: record.ProjectID, Environment: record.Environment}
}
