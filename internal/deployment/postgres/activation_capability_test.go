package postgres

import (
	"context"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgresActivationCapabilityRuntimeConformance exercises the real
// non-owner role against the serving-state boundary. Direct target,
// publication, and pointer writes are rejected by the database triggers; the
// guarded repository path still activates successfully through the narrow
// SECURITY DEFINER transition. Forged and replayed capability tuples fail
// closed without advancing the target a second time.
func TestPostgresActivationCapabilityRuntimeConformance(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "activation-runtime", Login: true})
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, runtimeRole, "CONNECT")

	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	applyActivationSchemas(t, admin)
	grantActivationRuntime(t, admin, runtimeRole.Name)

	runtime, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	if err := runtime.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}

	adminRepository := New(admin)
	input, ids := prepareLostAckActivation(t, adminRepository)
	lineage := &testActivationLineage{expected: ActivationLineageInput{TargetID: ids.target, ProjectID: "project_lost_ack", GenerationID: ids.generation}}
	runtimeRepository := NewWithOptions(runtime, Options{ActivationAudit: testActivationAudit{audit: accesspostgres.New()}, Lineage: lineage})

	// A caller cannot substitute another target for the publication tuple. The
	// capability rejects this before any serving row is mutated.
	if err := runtime.QueryRow(t.Context(), `
		SELECT delivery.commit_activation_transition($1::uuid, $2, $3::uuid, $4, $5)`,
		ids.publication, ids.target+"-forged", ids.generation, int64(1), int64(2)).Scan(new(bool)); err == nil {
		t.Fatal("forged activation tuple unexpectedly succeeded")
	}
	var pendingState string
	var pendingRevision int64
	if err := admin.QueryRow(t.Context(), `SELECT state FROM delivery.delivery_publication WHERE publication_id=$1::uuid`, ids.publication).Scan(&pendingState); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(t.Context(), `SELECT target_revision FROM delivery.delivery_target WHERE target_id=$1`, ids.target).Scan(&pendingRevision); err != nil {
		t.Fatal(err)
	}
	if pendingState != "pending" || pendingRevision != 1 {
		t.Fatalf("forged tuple changed pending state: publication=%q revision=%d", pendingState, pendingRevision)
	}

	// The normal repository activation remains valid under the real runtime
	// role because all three serving writes cross the SECURITY DEFINER boundary.
	activated, err := runtimeRepository.Activate(t.Context(), input)
	if err != nil {
		t.Fatalf("runtime-role repository activation: %v", err)
	}
	if activated.Replay || activated.Pointer.TargetRevision != 2 || activated.Publication.State != "committed" {
		t.Fatalf("runtime-role activation result = %#v", activated)
	}

	// Broad table privileges are intentionally granted in this conformance
	// fixture so trigger enforcement, rather than a permission error alone, is
	// what proves each direct mutation is forbidden.
	for name, statement := range map[string]string{
		"target revision":       `UPDATE delivery.delivery_target SET target_revision=3 WHERE target_id=$1`,
		"publication commit":    `UPDATE delivery.delivery_publication SET state='committed', result_target_revision=3, committed_at=clock_timestamp() WHERE publication_id=$1::uuid`,
		"active pointer update": `UPDATE delivery.delivery_active_pointer SET changed_at=clock_timestamp() WHERE target_id=$1`,
		"active pointer delete": `DELETE FROM delivery.delivery_active_pointer WHERE target_id=$1`,
	} {
		if _, err := runtime.Exec(t.Context(), statement, ids.target); err == nil {
			t.Fatalf("runtime direct %s unexpectedly succeeded", name)
		}
	}

	// Reusing the exact tuple after the publication is committed is a restart
	// rejection, not a second revision allocation.
	if err := runtime.QueryRow(t.Context(), `
		SELECT delivery.commit_activation_transition($1::uuid, $2, $3::uuid, $4, $5)`,
		ids.publication, ids.target, ids.generation, int64(1), int64(2)).Scan(new(bool)); err == nil {
		t.Fatal("replayed activation capability unexpectedly succeeded")
	}
	if err := admin.QueryRow(t.Context(), `SELECT target_revision FROM delivery.delivery_target WHERE target_id=$1`, ids.target).Scan(&pendingRevision); err != nil {
		t.Fatal(err)
	}
	if pendingRevision != 2 {
		t.Fatalf("replayed activation changed target revision to %d", pendingRevision)
	}
}

func applyActivationSchemas(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for name, schema := range map[string]string{
		"access":   accesspostgres.SchemaSQL(),
		"event":    eventspostgres.SchemaSQL(),
		"delivery": SchemaSQL(),
	} {
		if _, err := tx.Exec(t.Context(), schema); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatalf("apply %s schema: %v", name, err)
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func grantActivationRuntime(t *testing.T, admin *pgxpool.Pool, role string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	// The fixture grants broad DML on the three protected tables to ensure the
	// role-aware trigger, rather than missing table privilege, is exercised.
	statement := `
		GRANT USAGE ON SCHEMA delivery, event, audit TO ` + role + `;
		GRANT SELECT ON ALL TABLES IN SCHEMA delivery TO ` + role + `;
		GRANT INSERT, UPDATE, DELETE ON delivery.delivery_target, delivery.delivery_publication, delivery.delivery_active_pointer TO ` + role + `;
		GRANT UPDATE ON delivery.delivery_lease TO ` + role + `;
		GRANT INSERT ON delivery.delivery_retention_root TO ` + role + `;
		GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA event TO ` + role + `;
		GRANT SELECT, INSERT ON audit.audit_event TO ` + role + `;
	`
	if _, err := admin.Exec(ctx, statement); err != nil {
		t.Fatal(err)
	}
}
