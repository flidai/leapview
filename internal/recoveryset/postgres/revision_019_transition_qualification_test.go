//go:build fai518qualification

package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	releasetransitionapp "github.com/flidai/leapview/internal/app/releasetransitionpreflightproduction"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/release/migrationcapability"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionoperation"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestFAI518Revision019To020TransitionQualification(t *testing.T) {
	pool, migratorDB := revision019PreflightDB(t)
	var before int64
	if err := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&before); err != nil || before != 19 {
		t.Fatalf("predecessor revision = %d, error = %v; want 19", before, err)
	}
	var operationTable *string
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('release.release_transition_operation')::text`).Scan(&operationTable); err != nil || operationTable != nil {
		t.Fatalf("revision 019 transition operation table = %v, error = %v; want absent", operationTable, err)
	}
	releases := releasepostgres.New(pool)
	targets := deploymentpostgres.New(pool)
	recoverySets := postgres.New(pool)
	capabilities, err := releasepostgres.NewMigrationCapabilityAuthority(releases, productionOwnerRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := targets.CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: "target", ProjectID: "project", Environment: "production", TargetRevision: 1}); err != nil {
		t.Fatal(err)
	}
	targetDigest, err := (transitionpreflight.TargetIdentity{TargetID: "target", TargetRevision: 1}).Digest()
	if err != nil {
		t.Fatal(err)
	}
	predecessor := productionAdmission("predecessor", 'a', '1')
	candidate := productionAdmission("candidate", 'b', '2')
	predecessorIdentity := publishProductionAdmission(t, releases, predecessor)
	candidateIdentity := publishProductionAdmission(t, releases, candidate)
	publishRevision019Capabilities(t, capabilities, predecessorIdentity.ArtifactAdmissionDigest, targetDigest, false)
	publishRevision019Capabilities(t, capabilities, candidateIdentity.ArtifactAdmissionDigest, targetDigest, true)
	publishProductionPolicy(t, releases, predecessorIdentity, candidateIdentity)
	frontier := productionPublishedFrontier(t, recoverySets, productionRecoverySetFixture(t))
	resolver, err := releasetransitionapp.NewProductionResolver(releasetransitionapp.ProductionDependencies{Releases: releases, Targets: targets, MigrationCapabilities: capabilities, RecoverySets: recoverySets})
	if err != nil {
		t.Fatal(err)
	}
	preflightRequest := transitionpreflight.ResolutionRequest{PredecessorRef: predecessor.Release.Image, CandidateRef: candidate.Release.Image, TargetRef: "target", RecoveryFrontier: transitionpreflight.RecoveryFrontierRef{SetID: frontier.ID, Digest: frontier.FrontierDigest}}
	baseline, err := representativeStateDigest(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	transitions := releasepostgres.NewTransitionRepositoryWithBootstrap(pool, func(ctx context.Context) error {
		return migrations.BootstrapTransitionOperation(ctx, pool, migratorDB)
	})
	rejected, err := transitionrunner.New(transitionrunner.Options{
		Operations: transitions,
		Preflight:  &rejectedForwardPreflight{cause: errors.New("qualification rejected before bootstrap")},
		Fences:     transitions,
		Effects:    transitionrunner.EffectFuncs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rejected.Run(t.Context(), transitionrunner.Request{OwnerID: "revision-019-rejected-owner", IdempotencyKey: "revision-019-rejected", Preflight: preflightRequest}); !errors.Is(err, transitionrunner.ErrStalePreflight) {
		t.Fatalf("rejected preflight error = %v, want stale preflight", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('release.release_transition_operation')::text`).Scan(&operationTable); err != nil || operationTable != nil {
		t.Fatalf("rejected preflight created transition schema = %v, error = %v", operationTable, err)
	}
	failedRunner, err := transitionrunner.New(transitionrunner.Options{
		Operations: transitions,
		Preflight:  resolver,
		Fences:     transitions,
		Effects: transitionrunner.EffectFuncs{MigrationsFunc: func(context.Context, transitionrunner.EffectInput) (transitionrunner.EffectResult, error) {
			return transitionrunner.EffectResult{}, errors.New("qualification migration failure before Goose 020")
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `REVOKE leapview_control_owner FROM leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	if _, err := failedRunner.Run(t.Context(), transitionrunner.Request{OwnerID: "revision-019-bootstrap-owner", IdempotencyKey: "revision-019-bootstrap-denied", Preflight: preflightRequest}); err == nil {
		t.Fatal("runner created an operation without migration ownership")
	}
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('release.release_transition_operation')::text`).Scan(&operationTable); err != nil || operationTable != nil {
		t.Fatalf("failed runner bootstrap left transition schema = %v, error = %v", operationTable, err)
	}
	if _, err := pool.Exec(t.Context(), `GRANT leapview_control_owner TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	failed, err := failedRunner.Run(t.Context(), transitionrunner.Request{OwnerID: "revision-019-failure-owner", IdempotencyKey: "revision-019-migration-failure", Preflight: preflightRequest})
	if !errors.Is(err, transitionrunner.ErrPhaseFailure) || failed.Operation.Status != transitionoperation.StatusIndeterminate {
		t.Fatalf("failed migration result = %#v, error = %v; want indeterminate phase failure", failed.Operation, err)
	}
	if got := readForwardTransitionAfterRepositoryRestart(t, pool, failed.Operation.OperationID); got.Status != transitionoperation.StatusIndeterminate {
		t.Fatalf("durable failed migration status = %q, want indeterminate", got.Status)
	}
	if err := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&before); err != nil || before != 19 {
		t.Fatalf("failed migration revision = %d, error = %v; want 19", before, err)
	}
	for _, table := range []string{"release.release_transition_operation", "release.release_transition_phase_result", "release.release_transition_fence"} {
		var exists bool
		if err := pool.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("runner bootstrap table %s exists = %t, error = %v", table, exists, err)
		}
	}
	effects := newForwardQualificationEffects(t, pool, predecessor.Release.Image, resolver, preflightRequest)
	t.Cleanup(effects.cleanup)
	runner := newForwardQualificationRunner(t, transitions, resolver, effects)
	result, err := runner.Run(t.Context(), transitionrunner.Request{OwnerID: "revision-019-owner", IdempotencyKey: "revision-019-to-020", Preflight: preflightRequest})
	if err != nil {
		var after int64
		if readErr := pool.QueryRow(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&after); readErr != nil {
			t.Fatal(readErr)
		}
		afterState, readErr := representativeStateDigest(t.Context(), pool)
		if readErr != nil {
			t.Fatal(readErr)
		}
		t.Fatalf("forward transition from revision 019: %v (revision after=%d, migration effect entered=%t, state preserved=%t)", err, after, effects.migrationsApplied, afterState == baseline)
	}
	if !effects.migrationsApplied {
		t.Fatal("migration 020 did not execute")
	}
	assertForwardPhaseResults(t, result.Operation)
	assertAppliedMigrationRevision(t, pool)
	var truncateGuards int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_trigger WHERE tgname IN ('release_transition_operation_no_truncate', 'release_transition_phase_no_truncate', 'release_transition_fence_no_truncate') AND NOT tgisinternal`).Scan(&truncateGuards); err != nil || truncateGuards != 3 {
		t.Fatalf("migration 020 truncate guards = %d, error = %v; want 3", truncateGuards, err)
	}
	afterState, err := representativeStateDigest(t.Context(), pool)
	if err != nil || afterState != baseline {
		t.Fatalf("representative state after migration = %q, error = %v; want %q", afterState, err, baseline)
	}
	readback := readForwardTransitionAfterRepositoryRestart(t, pool, result.Operation.OperationID)
	assertForwardPhaseResults(t, readback)
	if readback.Status != result.Operation.Status {
		t.Fatalf("durable result status = %q, want %q", readback.Status, result.Operation.Status)
	}
	var generation int64
	var fenceOperation *string
	var fenceOwner string
	if err := pool.QueryRow(t.Context(), `SELECT fencing_generation, operation_id::text, owner_id FROM release.release_transition_fence WHERE target_identity_digest=$1`, targetDigest).Scan(&generation, &fenceOperation, &fenceOwner); err != nil || generation < 1 || fenceOperation != nil || fenceOwner != "" {
		t.Fatalf("released durable fence: generation=%d operation=%v owner=%q error=%v", generation, fenceOperation, fenceOwner, err)
	}
}

func revision019PreflightDB(t *testing.T) (*pgxpool.Pool, *sql.DB) {
	t.Helper()
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "revision-019", Login: true})
	for _, role := range []string{"leapview_control_runtime", "leapview_control_maintenance", "leapview_control_readonly", "leapview_control_backup"} {
		harness.EnsureRole(t, postgrestest.Role{Name: role})
	}
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "revision_019_transition")
	harness.GrantDatabase(t, database.Name, owner, "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	pool, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `ALTER DATABASE `+database.Name+` OWNER TO leapview_control_owner; REVOKE ALL ON SCHEMA public FROM PUBLIC; GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	control, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	previous := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			t.Fatalf("unnumbered migration %s", entry.Name())
		}
		revision, err := strconv.Atoi(prefix)
		if err != nil {
			t.Fatal(err)
		}
		if revision > 19 {
			continue
		}
		contents, err := fs.ReadFile(migrations.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		previous[entry.Name()] = &fstest.MapFile{Data: contents}
	}
	if len(previous) != 19 {
		t.Fatalf("revision 019 fixture contains %d migrations, want 19", len(previous))
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, control, previous)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply predecessor migrations through 019: %v", err)
	}
	return pool, control
}

func publishRevision019Capabilities(t *testing.T, authority *releasepostgres.MigrationCapabilityAuthority, artifactDigest, targetDigest string, candidate bool) {
	t.Helper()
	for _, capability := range productionCapabilities(t, artifactDigest, targetDigest, candidate) {
		if capability.Subsystem == migrationcapability.SubsystemGoose {
			capability.Goose.SchemaVersion = "goose/v19"
			if candidate {
				capability.Goose.SchemaVersion = "goose/v20"
			}
			capability.Goose.RunnableSchemaVersions = []string{"goose/v19", "goose/v20"}
		}
		evidence, err := migrationcapability.SignOwnerEvidence(capability, productionOwnerKeyID, productionOwnerPrivateKeys()[capability.Subsystem])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authority.Publish(t.Context(), evidence); err != nil {
			t.Fatal(fmt.Errorf("publish %s capability: %w", capability.Subsystem, err))
		}
	}
}
