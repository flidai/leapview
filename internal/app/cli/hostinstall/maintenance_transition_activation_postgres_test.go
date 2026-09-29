//go:build linux

package hostinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
)

// This qualifies the activation predicate against PostgreSQL itself. The
// docker execution seam only routes the waiter's read-only SQL into a
// disposable database; it does not synthesize the query result.
func TestTransitionActivationRequiresBothPersistedPolicyDigests(t *testing.T) {
	db := postgrestest.Open(t, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
CREATE SCHEMA delivery;
CREATE SCHEMA access;
CREATE TABLE delivery.delivery_target (
  target_id text PRIMARY KEY,
  project_id text NOT NULL,
  environment text NOT NULL
);
CREATE TABLE delivery.delivery_active_pointer (
  target_id text NOT NULL,
  generation_id uuid NOT NULL,
  publication_id uuid NOT NULL
);
CREATE TABLE delivery.delivery_publication (
  publication_id uuid PRIMARY KEY,
  target_id text NOT NULL,
  generation_id uuid NOT NULL,
  state text NOT NULL
);
CREATE TABLE delivery.delivery_generation (
  generation_id uuid PRIMARY KEY,
  target_id text NOT NULL,
  plan_id uuid NOT NULL
);
CREATE TABLE delivery.delivery_plan (
  plan_id uuid PRIMARY KEY,
  target_id text NOT NULL,
  plan_document jsonb NOT NULL
);
CREATE TABLE access.authorization_snapshot (
  project_id text NOT NULL,
  environment text NOT NULL,
  generation_id text NOT NULL,
  digest text NOT NULL
);`)
		return err
	})

	e, _, _ := transitionEffectsFixture(t)
	intent := e.request.AccessTransition
	plan, err := intent.Plan()
	if err != nil {
		t.Fatal(err)
	}
	const (
		generationID  = "11111111-1111-4111-8111-111111111111"
		publicationID = "22222222-2222-4222-8222-222222222222"
		planID        = "33333333-3333-4333-8333-333333333333"
	)
	planDigest := "sha256:" + hex64('a')
	servingDigest := "sha256:" + hex64('b')
	result, err := json.Marshal(transitionActivation{
		OperationID:                 "access-transition:" + strings.TrimPrefix(e.id.ArtifactAdmissionDigest, "sha256:"),
		IntentDigest:                plan.IntentDigest,
		PlanID:                      planID,
		GenerationID:                generationID,
		PublicationID:               publicationID,
		PlanPolicySnapshotDigest:    planDigest,
		ServingPolicySnapshotDigest: servingDigest,
		Status:                      "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.recordTransitionResult(string(result), true); err != nil {
		t.Fatal(err)
	}

	type persistedPolicy struct {
		name                string
		activeGeneration    string
		snapshotGeneration  string
		snapshotProject     string
		snapshotEnvironment string
		planDigest          string
		servingDigest       string
		wantActive          bool
	}
	cases := []persistedPolicy{
		{name: "exact generation and both digests", activeGeneration: generationID, snapshotGeneration: generationID, planDigest: planDigest, servingDigest: servingDigest, wantActive: true},
		{name: "wrong plan digest", activeGeneration: generationID, snapshotGeneration: generationID, planDigest: "sha256:" + hex64('c'), servingDigest: servingDigest},
		{name: "wrong serving snapshot digest", activeGeneration: generationID, snapshotGeneration: generationID, planDigest: planDigest, servingDigest: "sha256:" + hex64('d')},
		{name: "snapshot exists only for another generation", activeGeneration: generationID, snapshotGeneration: "44444444-4444-4444-8444-444444444444", planDigest: planDigest, servingDigest: servingDigest},
		{name: "snapshot belongs to another project", activeGeneration: generationID, snapshotGeneration: generationID, snapshotProject: "project:other", planDigest: planDigest, servingDigest: servingDigest},
		{name: "snapshot belongs to another environment", activeGeneration: generationID, snapshotGeneration: generationID, snapshotEnvironment: "other", planDigest: planDigest, servingDigest: servingDigest},
		{name: "active pointer selects another generation", activeGeneration: "44444444-4444-4444-8444-444444444444", snapshotGeneration: generationID, planDigest: planDigest, servingDigest: servingDigest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshotProject := tc.snapshotProject
			if snapshotProject == "" {
				snapshotProject = intent.ProjectID
			}
			snapshotEnvironment := tc.snapshotEnvironment
			if snapshotEnvironment == "" {
				snapshotEnvironment = intent.Environment
			}
			_, err := db.Exec(t.Context(), `TRUNCATE delivery.delivery_active_pointer, delivery.delivery_publication,
delivery.delivery_generation, delivery.delivery_plan, delivery.delivery_target, access.authorization_snapshot`)
			if err != nil {
				t.Fatal(err)
			}
			inserts := []struct {
				query string
				args  []any
			}{
				{`INSERT INTO delivery.delivery_target(target_id, project_id, environment) VALUES($1,$2,$3)`, []any{intent.TargetID, intent.ProjectID, intent.Environment}},
				{`INSERT INTO delivery.delivery_plan(plan_id,target_id,plan_document) VALUES($1,$2,jsonb_build_object('authorization',jsonb_build_object('snapshotDigest',$3::text)))`, []any{planID, intent.TargetID, tc.planDigest}},
				{`INSERT INTO delivery.delivery_generation(generation_id,target_id,plan_id) VALUES($1,$2,$3)`, []any{generationID, intent.TargetID, planID}},
				{`INSERT INTO delivery.delivery_publication(publication_id,target_id,generation_id,state) VALUES($1,$2,$3,'committed')`, []any{publicationID, intent.TargetID, generationID}},
				{`INSERT INTO delivery.delivery_active_pointer(target_id,generation_id,publication_id) VALUES($1,$2,$3)`, []any{intent.TargetID, tc.activeGeneration, publicationID}},
				{`INSERT INTO access.authorization_snapshot(project_id,environment,generation_id,digest) VALUES($1,$2,$3,$4)`, []any{snapshotProject, snapshotEnvironment, tc.snapshotGeneration, tc.servingDigest}},
			}
			for _, insert := range inserts {
				if _, err := db.Exec(t.Context(), insert.query, insert.args...); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			queries := 0
			e.execute = func(_ context.Context, args ...string) (string, error) {
				queries++
				if len(args) < 2 || args[0] != "exec" || args[1] != "activation-postgres-test" {
					return "", fmt.Errorf("unexpected activation query command: %v", args)
				}
				query := args[len(args)-1]
				var active bool
				if err := db.QueryRow(t.Context(), query).Scan(&active); err != nil {
					return "", err
				}
				if !active {
					cancel()
				}
				if active {
					return "t", nil
				}
				return "f", nil
			}

			err = e.waitTransitionActivation(ctx, "activation-postgres-test", true)
			if tc.wantActive && err != nil {
				t.Fatalf("exact persisted activation did not pass: %v", err)
			}
			if !tc.wantActive && err == nil {
				t.Fatal("mismatched persisted policy state passed activation")
			}
			if queries != 1 {
				t.Fatalf("executed %d activation queries, want exactly one", queries)
			}
		})
	}
}
