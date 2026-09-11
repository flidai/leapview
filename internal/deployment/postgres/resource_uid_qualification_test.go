package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	project "github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const resourceUIDQualificationInstance = "lvinst_0123456789abcdefghijklmnopqrstuv"

// TestPostgresResourceUIDAdmissionAndActivationQualification exercises the
// complete PostgreSQL lifecycle against the canonical instance target. The
// inventory is staged with the generation and serving bundle, while the
// registry remains empty until the database-owned activation transition.
func TestPostgresResourceUIDAdmissionAndActivationQualification(t *testing.T) {
	db := deliveryTestDB(t)
	installResourceUIDQualificationDependencies(t, db)
	seedResourceUIDQualificationBootstrap(t, db)

	adminRepository := New(db)
	first := prepareResourceUIDQualificationGeneration(t, adminRepository, resourceUIDQualificationGenerationSpec{
		Number: 1, IncludeDashboard: true,
	})
	lineage := &testActivationLineage{expected: ActivationLineageInput{
		TargetID: resourceUIDQualificationInstance, ProjectID: first.projectID,
		GenerationID: first.generationID, CompiledGraphDigest: first.graph.Digest(),
	}}
	repository := NewWithOptions(db, Options{
		ActivationAudit: testActivationAudit{audit: accesspostgres.New()},
		Lineage:         lineage,
	})

	t.Run("admission stages inventory without allocating UIDs", func(t *testing.T) {
		var inventoryRows, registryRows, bundles, generations int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_inventory WHERE generation_id=$1::uuid`, first.generationID).Scan(&inventoryRows); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_registry WHERE instance_id=$1 AND project_id=$2`, resourceUIDQualificationInstance, first.projectID).Scan(&registryRows); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM serving_state.bundle WHERE generation_id=$1::uuid`, first.generationID).Scan(&bundles); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_generation WHERE generation_id=$1::uuid`, first.generationID).Scan(&generations); err != nil {
			t.Fatal(err)
		}
		if inventoryRows != 1 || registryRows != 0 || bundles != 1 || generations != 1 {
			t.Fatalf("admission evidence = inventory %d registry %d bundle %d generation %d; want 1, 0, 1, 1", inventoryRows, registryRows, bundles, generations)
		}
	})

	t.Run("failed activation rolls back binder writes", func(t *testing.T) {
		failing := NewWithOptions(db, Options{
			ActivationAudit: failingResourceUIDActivationAudit{err: errors.New("qualification audit interruption")},
			Lineage:         lineage,
		})
		if _, err := failing.Activate(t.Context(), first.activation); err == nil {
			t.Fatal("failed activation unexpectedly succeeded")
		}
		var registryRows, generationBindings int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_registry WHERE instance_id=$1 AND project_id=$2`, resourceUIDQualificationInstance, first.projectID).Scan(&registryRows); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_generation WHERE instance_id=$1 AND project_id=$2`, resourceUIDQualificationInstance, first.projectID).Scan(&generationBindings); err != nil {
			t.Fatal(err)
		}
		if registryRows != 0 || generationBindings != 0 {
			t.Fatalf("failed activation left registry evidence: registry=%d bindings=%d", registryRows, generationBindings)
		}
		var revision int64
		if err := db.QueryRow(t.Context(), `SELECT target_revision FROM delivery.delivery_target WHERE target_id=$1`, resourceUIDQualificationInstance).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != 1 {
			t.Fatalf("failed activation advanced target revision to %d", revision)
		}
	})

	const initialActivationCallers = 2
	initialStart := make(chan struct{})
	initialResults := make(chan struct {
		result ActivationResult
		err    error
	}, initialActivationCallers)
	var initialWG sync.WaitGroup
	for range initialActivationCallers {
		initialWG.Add(1)
		go func() {
			defer initialWG.Done()
			<-initialStart
			result, activateErr := repository.Activate(t.Context(), first.activation)
			initialResults <- struct {
				result ActivationResult
				err    error
			}{result: result, err: activateErr}
		}()
	}
	close(initialStart)
	initialWG.Wait()
	close(initialResults)
	var freshActivations, replayActivations int
	for call := range initialResults {
		if call.err != nil {
			t.Fatalf("concurrent first activation: %v", call.err)
		}
		if call.result.Publication.State != "committed" || call.result.Pointer.ActiveGenerationID != first.generationID || call.result.Pointer.TargetRevision != 2 {
			t.Fatalf("concurrent first activation result = %#v", call.result)
		}
		if call.result.Replay {
			replayActivations++
		} else {
			freshActivations++
		}
	}
	if freshActivations != 1 || replayActivations != 1 {
		t.Fatalf("concurrent first activation outcomes = fresh %d replay %d, want one each", freshActivations, replayActivations)
	}

	t.Run("successful activation allocates stable UIDs for every kind", func(t *testing.T) {
		bindings, err := projectpostgres.New(db).ListGenerationResourceUIDs(t.Context(), resourceUIDQualificationInstance, first.projectID, first.generationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(bindings) != 6 {
			t.Fatalf("first generation bindings = %d, want six authored kinds", len(bindings))
		}
		seenKinds := make(map[projectgraph.Kind]bool, len(bindings))
		for _, binding := range bindings {
			seenKinds[binding.Kind] = true
			if binding.InstanceID != resourceUIDQualificationInstance || binding.TargetID != resourceUIDQualificationInstance || binding.ProjectID != first.projectID || binding.GenerationID != first.generationID {
				t.Fatalf("binding scope = %#v", binding)
			}
		}
		for _, kind := range []projectgraph.Kind{projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard} {
			if !seenKinds[kind] {
				t.Fatalf("binding kinds missing %q: %#v", kind, seenKinds)
			}
		}
	})

	firstUIDs := resourceUIDsByAuthoredID(t, db, first.projectID)
	t.Run("exact activation replay and concurrent replay preserve UIDs", func(t *testing.T) {
		replay, err := repository.Activate(t.Context(), first.activation)
		if err != nil {
			t.Fatalf("activation replay: %v", err)
		}
		if !replay.Replay || replay.Pointer.TargetRevision != 2 {
			t.Fatalf("activation replay = %#v", replay)
		}
		const callers = 2
		errs := make(chan error, callers)
		results := make(chan ActivationResult, callers)
		var wg sync.WaitGroup
		for range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result, activateErr := repository.Activate(t.Context(), first.activation)
				results <- result
				errs <- activateErr
			}()
		}
		wg.Wait()
		close(errs)
		close(results)
		for activateErr := range errs {
			if activateErr != nil {
				t.Fatalf("concurrent exact replay: %v", activateErr)
			}
		}
		for result := range results {
			if !result.Replay || result.Pointer.TargetRevision != 2 {
				t.Fatalf("concurrent replay = %#v", result)
			}
		}
		if got := resourceUIDsByAuthoredID(t, db, first.projectID); !sameResourceUIDMap(got, firstUIDs) {
			t.Fatalf("UIDs changed on replay: got=%v want=%v", got, firstUIDs)
		}
	})

	second := prepareResourceUIDQualificationGeneration(t, adminRepository, resourceUIDQualificationGenerationSpec{
		Number: 2, IncludeDashboard: false, BaseGenerationID: first.generationID,
	})
	lineage.expected = ActivationLineageInput{
		TargetID: resourceUIDQualificationInstance, ProjectID: second.projectID,
		GenerationID: second.generationID, CompiledGraphDigest: second.graph.Digest(),
	}
	if _, err := repository.Activate(t.Context(), second.activation); err != nil {
		t.Fatalf("activate generation removing dashboard: %v", err)
	}

	t.Run("tombstone records exact generation and preserves UID", func(t *testing.T) {
		dashboardUID, ok := firstUIDs["dashboard:sales"]
		if !ok {
			t.Fatal("first dashboard UID missing")
		}
		dashboard, err := projectpostgres.New(db).ResolveResourceUID(t.Context(), resourceUIDQualificationInstance, first.projectID, "dashboard:sales")
		if err != nil {
			t.Fatal(err)
		}
		if dashboard.UID != dashboardUID || dashboard.State != project.ResourceUIDTombstoned || dashboard.RemovedInGeneration != second.generationID {
			t.Fatalf("dashboard tombstone = %#v", dashboard)
		}
		var tombstones int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_tombstone WHERE instance_id=$1 AND project_id=$2 AND authored_resource_id='dashboard:sales' AND removed_in_generation_id=$3::uuid`, resourceUIDQualificationInstance, first.projectID, second.generationID).Scan(&tombstones); err != nil {
			t.Fatal(err)
		}
		if tombstones != 1 {
			t.Fatalf("dashboard tombstones = %d, want one", tombstones)
		}
	})

	t.Run("tombstone evidence rejects direct SQL mutation", func(t *testing.T) {
		mutation := `UPDATE project.resource_uid_tombstone
			SET removed_in_generation_id = removed_in_generation_id
			WHERE instance_id=$1 AND project_id=$2 AND authored_resource_id='dashboard:sales' AND removed_in_generation_id=$3::uuid`
		if _, err := db.Exec(t.Context(), mutation, resourceUIDQualificationInstance, first.projectID, second.generationID); err == nil {
			t.Fatal("direct tombstone UPDATE unexpectedly succeeded")
		}
		deletion := `DELETE FROM project.resource_uid_tombstone
			WHERE instance_id=$1 AND project_id=$2 AND authored_resource_id='dashboard:sales' AND removed_in_generation_id=$3::uuid`
		if _, err := db.Exec(t.Context(), deletion, resourceUIDQualificationInstance, first.projectID, second.generationID); err == nil {
			t.Fatal("direct tombstone DELETE unexpectedly succeeded")
		}
		var tombstones int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_tombstone WHERE instance_id=$1 AND project_id=$2 AND authored_resource_id='dashboard:sales' AND removed_in_generation_id=$3::uuid`, resourceUIDQualificationInstance, first.projectID, second.generationID).Scan(&tombstones); err != nil {
			t.Fatal(err)
		}
		if tombstones != 1 {
			t.Fatalf("direct tombstone mutations changed evidence count to %d", tombstones)
		}
	})

	third := prepareResourceUIDQualificationGeneration(t, adminRepository, resourceUIDQualificationGenerationSpec{
		Number: 3, IncludeDashboard: true, BaseGenerationID: second.generationID,
	})
	lineage.expected = ActivationLineageInput{
		TargetID: resourceUIDQualificationInstance, ProjectID: third.projectID,
		GenerationID: third.generationID, CompiledGraphDigest: third.graph.Digest(),
	}
	t.Run("restore authorization rejects unadmitted generations and wrong scopes", func(t *testing.T) {
		dashboardUID := firstUIDs["dashboard:sales"]
		if _, err := projectpostgres.New(db).AuthorizeResourceUIDRestore(t.Context(), projectpostgres.ResourceUIDRestoreInput{
			ResourceUIDActivation: projectpostgres.ResourceUIDActivation{
				InstanceID: resourceUIDQualificationInstance, TargetID: resourceUIDQualificationInstance,
				ProjectID: first.projectID, Environment: "prod", GenerationID: "0198f2c0-7c7a-7f00-8a11-000000009999",
			},
			ResourceUID: dashboardUID, AuthoredID: "dashboard:sales", Kind: projectgraph.KindDashboard,
			ActorID: "restore-operator", RequestDigest: testDigest('b'),
		}); err == nil {
			t.Fatal("restore authorization for random unadmitted generation unexpectedly succeeded")
		}
		if _, err := projectpostgres.New(db).AuthorizeResourceUIDRestore(t.Context(), projectpostgres.ResourceUIDRestoreInput{
			ResourceUIDActivation: projectpostgres.ResourceUIDActivation{
				InstanceID: resourceUIDQualificationInstance, TargetID: resourceUIDQualificationInstance,
				ProjectID: "project-other", Environment: "prod", GenerationID: third.generationID,
			},
			ResourceUID: dashboardUID, AuthoredID: "dashboard:sales", Kind: projectgraph.KindDashboard,
			ActorID: "restore-operator", RequestDigest: testDigest('c'),
		}); err == nil {
			t.Fatal("restore authorization for wrong project unexpectedly succeeded")
		}
		var authorizations int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_restore_authorization WHERE resource_uid=$1::uuid`, dashboardUID).Scan(&authorizations); err != nil {
			t.Fatal(err)
		}
		if authorizations != 0 {
			t.Fatalf("rejected restore requests left %d authorization rows", authorizations)
		}
	})
	t.Run("new-generation tombstone restore requires and consumes exact authorization", func(t *testing.T) {
		if _, err := repository.Activate(t.Context(), third.activation); err == nil || !strings.Contains(strings.ToLower(err.Error()), "restore") {
			t.Fatalf("unauthorized new-generation restore error = %v, want restore authorization failure", err)
		}
		var revision int64
		if err := db.QueryRow(t.Context(), `SELECT target_revision FROM delivery.delivery_target WHERE target_id=$1`, resourceUIDQualificationInstance).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != 3 {
			t.Fatalf("unauthorized restore advanced target revision to %d", revision)
		}
		dashboard, err := projectpostgres.New(db).ResolveResourceUID(t.Context(), resourceUIDQualificationInstance, first.projectID, "dashboard:sales")
		if err != nil {
			t.Fatal(err)
		}
		if dashboard.State != project.ResourceUIDTombstoned || dashboard.CurrentGeneration != "" {
			t.Fatalf("unauthorized restore changed dashboard = %#v", dashboard)
		}

		dashboardUID := firstUIDs["dashboard:sales"]
		restore, err := projectpostgres.New(db).AuthorizeResourceUIDRestore(t.Context(), projectpostgres.ResourceUIDRestoreInput{
			ResourceUIDActivation: projectpostgres.ResourceUIDActivation{
				InstanceID: resourceUIDQualificationInstance, TargetID: resourceUIDQualificationInstance,
				ProjectID: first.projectID, Environment: "prod", GenerationID: third.generationID,
			},
			ResourceUID: dashboardUID, AuthoredID: "dashboard:sales", Kind: projectgraph.KindDashboard,
			ActorID: "restore-operator", RequestDigest: testDigest('a'),
		})
		if err != nil {
			t.Fatalf("authorize dashboard restore: %v", err)
		}
		if restore.Status != "pending" || restore.ResourceUID != dashboardUID || restore.Kind != projectgraph.KindDashboard {
			t.Fatalf("restore authorization = %#v", restore)
		}
		activated, err := repository.Activate(t.Context(), third.activation)
		if err != nil {
			t.Fatalf("activate authorized new generation restore: %v", err)
		}
		if activated.Replay || activated.Pointer.ActiveGenerationID != third.generationID {
			t.Fatalf("authorized restore activation = %#v", activated)
		}
		resolved, err := projectpostgres.New(db).ResolveResourceUID(t.Context(), resourceUIDQualificationInstance, first.projectID, "dashboard:sales")
		if err != nil {
			t.Fatal(err)
		}
		if resolved.State != project.ResourceUIDActive || resolved.CurrentGeneration != third.generationID || resolved.UID != dashboardUID {
			t.Fatalf("restored dashboard = %#v", resolved)
		}
		var status string
		var consumed bool
		if err := db.QueryRow(t.Context(), `SELECT status, consumed_at IS NOT NULL FROM project.resource_uid_restore_authorization WHERE generation_id=$1::uuid AND resource_uid=$2::uuid`, third.generationID, dashboardUID).Scan(&status, &consumed); err != nil {
			t.Fatal(err)
		}
		if status != "consumed" || !consumed {
			t.Fatalf("restore authorization consumption = status %q consumed_at_set=%v", status, consumed)
		}
	})

	fourth := prepareResourceUIDQualificationGeneration(t, adminRepository, resourceUIDQualificationGenerationSpec{
		Number: 4, IncludeDashboard: false, BaseGenerationID: third.generationID, KindMismatch: true,
	})
	lineage.expected = ActivationLineageInput{
		TargetID: resourceUIDQualificationInstance, ProjectID: fourth.projectID,
		GenerationID: fourth.generationID, CompiledGraphDigest: fourth.graph.Digest(),
	}
	t.Run("kind mismatch rolls back an earlier fresh allocation", func(t *testing.T) {
		before := resourceUIDsByAuthoredID(t, db, first.projectID)
		if _, err := repository.Activate(t.Context(), fourth.activation); err == nil || !strings.Contains(strings.ToLower(err.Error()), "kind conflict") {
			t.Fatalf("kind mismatch activation error = %v, want kind conflict", err)
		}
		if got := resourceUIDsByAuthoredID(t, db, first.projectID); !sameResourceUIDMap(got, before) {
			t.Fatalf("kind mismatch changed registry UIDs: got=%v before=%v", got, before)
		}
		var freshRows int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_registry WHERE instance_id=$1 AND project_id=$2 AND authored_resource_id='a_new'`, resourceUIDQualificationInstance, first.projectID).Scan(&freshRows); err != nil {
			t.Fatal(err)
		}
		if freshRows != 0 {
			t.Fatalf("kind mismatch leaked fresh earlier allocation: %d rows", freshRows)
		}
		var revision int64
		if err := db.QueryRow(t.Context(), `SELECT target_revision FROM delivery.delivery_target WHERE target_id=$1`, resourceUIDQualificationInstance).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != 4 {
			t.Fatalf("kind mismatch advanced target revision to %d", revision)
		}
	})

	rollbackSecond := prepareHistoricalResourceUIDRollback(t, adminRepository, second, 4, third.generationID)
	lineage.expected = ActivationLineageInput{
		TargetID: resourceUIDQualificationInstance, ProjectID: second.projectID,
		GenerationID: second.generationID, CompiledGraphDigest: second.graph.Digest(),
	}
	t.Run("historical generation rollback reuses UIDs without new authorization", func(t *testing.T) {
		result, err := repository.Activate(t.Context(), rollbackSecond.activation)
		if err != nil {
			t.Fatalf("rollback to previously bound generation: %v", err)
		}
		if result.Replay || result.Pointer.ActiveGenerationID != second.generationID {
			t.Fatalf("historical rollback result = %#v", result)
		}
		got := resourceUIDsByAuthoredID(t, db, first.projectID)
		if !sameResourceUIDMap(got, firstUIDs) {
			t.Fatalf("historical rollback changed UIDs: got=%v want=%v", got, firstUIDs)
		}
		dashboard, err := projectpostgres.New(db).ResolveResourceUID(t.Context(), resourceUIDQualificationInstance, first.projectID, "dashboard:sales")
		if err != nil {
			t.Fatal(err)
		}
		if dashboard.State != project.ResourceUIDTombstoned || dashboard.RemovedInGeneration != second.generationID {
			t.Fatalf("rollback dashboard state = %#v", dashboard)
		}
	})

	rollbackFirst := prepareHistoricalResourceUIDRollback(t, adminRepository, first, 5, second.generationID)
	lineage.expected = ActivationLineageInput{
		TargetID: resourceUIDQualificationInstance, ProjectID: first.projectID,
		GenerationID: first.generationID, CompiledGraphDigest: first.graph.Digest(),
	}
	t.Run("historical first generation restores tombstone without authorization", func(t *testing.T) {
		result, err := repository.Activate(t.Context(), rollbackFirst.activation)
		if err != nil {
			t.Fatalf("rollback to first generation: %v", err)
		}
		if result.Replay || result.Pointer.ActiveGenerationID != first.generationID {
			t.Fatalf("first historical rollback result = %#v", result)
		}
		dashboard, err := projectpostgres.New(db).ResolveResourceUID(t.Context(), resourceUIDQualificationInstance, first.projectID, "dashboard:sales")
		if err != nil {
			t.Fatal(err)
		}
		if dashboard.State != project.ResourceUIDActive || dashboard.CurrentGeneration != first.generationID || dashboard.UID != firstUIDs["dashboard:sales"] {
			t.Fatalf("first rollback dashboard = %#v", dashboard)
		}
		var pending int
		if err := db.QueryRow(t.Context(), `SELECT count(*) FROM project.resource_uid_restore_authorization WHERE instance_id=$1 AND project_id=$2 AND status='pending'`, resourceUIDQualificationInstance, first.projectID).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending != 0 {
			t.Fatalf("historical rollback left pending restore authorizations: %d", pending)
		}
	})

	t.Run("independent database instances allocate distinct UIDs", func(t *testing.T) {
		isolatedDB := deliveryTestDB(t)
		installResourceUIDQualificationDependencies(t, isolatedDB)
		seedResourceUIDQualificationBootstrap(t, isolatedDB)
		isolatedAdmin := New(isolatedDB)
		isolatedFirst := prepareResourceUIDQualificationGeneration(t, isolatedAdmin, resourceUIDQualificationGenerationSpec{
			Number: 1, IncludeDashboard: true,
		})
		isolatedLineage := &testActivationLineage{expected: ActivationLineageInput{
			TargetID: resourceUIDQualificationInstance, ProjectID: isolatedFirst.projectID,
			GenerationID: isolatedFirst.generationID, CompiledGraphDigest: isolatedFirst.graph.Digest(),
		}}
		isolatedRepository := NewWithOptions(isolatedDB, Options{
			ActivationAudit: testActivationAudit{audit: accesspostgres.New()},
			Lineage:         isolatedLineage,
		})
		if _, err := isolatedRepository.Activate(t.Context(), isolatedFirst.activation); err != nil {
			t.Fatalf("activate isolated instance: %v", err)
		}
		isolatedUIDs := resourceUIDsByAuthoredID(t, isolatedDB, isolatedFirst.projectID)
		if isolatedUIDs["connection:warehouse"] == firstUIDs["connection:warehouse"] {
			t.Fatalf("independent database instances reused connection UID %q", isolatedUIDs["connection:warehouse"])
		}
		if isolatedUIDs["dashboard:sales"] == firstUIDs["dashboard:sales"] {
			t.Fatalf("independent database instances reused dashboard UID %q", isolatedUIDs["dashboard:sales"])
		}
	})

	t.Run("wrong project or instance cannot resolve the UID", func(t *testing.T) {
		if _, err := projectpostgres.New(db).ResolveResourceUID(t.Context(), resourceUIDQualificationInstance, "project-other", "connection:warehouse"); !errors.Is(err, project.ErrResourceUIDNotFound) {
			t.Fatalf("wrong project resolution = %v, want not found", err)
		}
		if _, err := projectpostgres.New(db).ResolveResourceUIDByUID(t.Context(), "lvinst_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", first.projectID, firstUIDs["connection:warehouse"]); !errors.Is(err, project.ErrResourceUIDNotFound) {
			t.Fatalf("wrong instance resolution = %v, want not found", err)
		}
	})
}

type failingResourceUIDActivationAudit struct{ err error }

func (a failingResourceUIDActivationAudit) AppendActivationAudit(_ context.Context, _ Tx, _ ActivationAuditInput) (AuditEvent, error) {
	return AuditEvent{}, a.err
}

func (a failingResourceUIDActivationAudit) GetActivationAudit(_ context.Context, _ Tx, _ ActivationAuditInput) (AuditEvent, error) {
	return AuditEvent{}, a.err
}

type resourceUIDQualificationGenerationSpec struct {
	Number           int
	IncludeDashboard bool
	BaseGenerationID string
	KindMismatch     bool
}

type resourceUIDQualificationGeneration struct {
	projectID, generationID, publicationID string
	candidateID, sealID                    string
	graph                                  projectgraph.ProjectGraph
	activation                             ActivationInput
}

func installResourceUIDQualificationDependencies(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := platformbootstrappostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply bootstrap schema: %v", err)
	}
	if err := projectpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply project schema: %v", err)
	}
	if _, err := tx.Exec(t.Context(), projectpostgres.ResourceUIDSchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("apply resource UID schema after dependencies: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func seedResourceUIDQualificationBootstrap(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	bootstrap := platformbootstrappostgres.New(db)
	if err := bootstrap.EnsureInstanceID(t.Context(), resourceUIDQualificationInstance); err != nil {
		t.Fatalf("ensure instance ID: %v", err)
	}
	if err := bootstrap.BindInstanceEnvironment(t.Context(), "prod"); err != nil {
		t.Fatalf("bind instance environment: %v", err)
	}
	if _, err := projectpostgres.New(db).Ensure(t.Context(), projectpostgres.EnsureInput{ID: "project_uid_qualification", Title: "Resource UID qualification"}); err != nil {
		t.Fatalf("ensure project identity: %v", err)
	}
	if _, err := bootstrap.ClaimProject(t.Context(), platformbootstrappostgres.ProjectClaimInput{
		ProjectID: "project_uid_qualification", Environment: "prod", ClaimedBy: "qualification", ClaimedAt: time.Now().UTC().Truncate(time.Microsecond),
	}); err != nil {
		t.Fatalf("claim project: %v", err)
	}
}

func prepareResourceUIDQualificationGeneration(t *testing.T, r *Repository, spec resourceUIDQualificationGenerationSpec) resourceUIDQualificationGeneration {
	t.Helper()
	ctx := t.Context()
	projectID := "project_uid_qualification"
	ids := fmt.Sprintf("%d", spec.Number)
	planID := "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "01"
	candidateID := "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "02"
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "03"
	sealID := "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "04"
	generationID := "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "05"
	publicationID := "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "06"
	leaseID := "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "07"
	correlationID := "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "08"
	poolID := "uid-pool-" + ids
	catalogID := "uid-catalog-" + ids
	artifactDigest := testDigest('e')
	bundle, inventory, graph := resourceUIDQualificationBundle(t, spec.IncludeDashboard)
	if spec.KindMismatch {
		bundle, inventory, graph = resourceUIDQualificationKindMismatchBundle(t)
	}

	if spec.Number == 1 {
		if _, err := r.CreateTarget(ctx, TargetInput{TargetID: resourceUIDQualificationInstance, ProjectID: projectID, Environment: "prod"}); err != nil {
			t.Fatalf("create canonical instance target: %v", err)
		}
	}
	rich, document := richPlanDocumentFixture(t, planID, resourceUIDQualificationInstance, projectID)
	rich.SourceDigest = graph.Digest()
	rich.Execution.SourceArtifactDigest = graph.Digest()
	rich.ServingArtifactDigest = artifactDigest
	rich.Governance.RequiresApproval = false
	rich.Governance.ExpiresAt = time.Now().UTC().Add(2 * time.Hour)
	rich, err := deploymentdomain.NewDeliveryPlan(rich)
	if err != nil {
		t.Fatalf("new resource UID plan: %v", err)
	}
	document, err = json.Marshal(rich)
	if err != nil {
		t.Fatal(err)
	}
	planInput := planDocumentProjectionFixture(t, rich, document)
	planInput.PlanRevision = int64(spec.Number)
	planInput.CompiledGraphDigest = graph.Digest()
	planInput.ArtifactDigest = artifactDigest
	planInput.Evidence = []byte(`{"qualification":"resource-uid"}`)
	plan, err := r.CreatePlan(ctx, planInput)
	if err != nil {
		t.Fatalf("create resource UID plan: %v", err)
	}
	if _, err := r.CreateCandidate(ctx, CandidateInput{CandidateID: candidateID, TargetID: resourceUIDQualificationInstance, PlanID: planID, CandidateRevision: int64(spec.Number), ArtifactDigest: artifactDigest}); err != nil {
		t.Fatalf("create resource UID candidate: %v", err)
	}
	requestDigest := testDigest([]byte("fabc")[spec.Number-1])
	if _, err := r.BeginBuildAttempt(ctx, BuildAttemptInput{AttemptID: attemptID, PlanID: planID, CandidateID: candidateID, OwnerID: "uid-builder-" + ids, PhysicalPoolID: poolID, CatalogID: catalogID, FencingEpoch: 1, RequestDigest: requestDigest, PlanDigest: plan.PlanDigest, Namespace: "candidate/uid-" + ids, SessionIdentity: "session/uid-" + ids, LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("begin resource UID build attempt: %v", err)
	}
	if _, err := r.BindBuildArtifact(ctx, BuildArtifactBindingInput{AttemptID: attemptID, ServingArtifactID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ServingArtifactDigest: artifactDigest, ServingStateID: "generation-test", OwnerID: "uid-builder-" + ids, FencingEpoch: 1}); err != nil {
		t.Fatalf("bind resource UID artifact: %v", err)
	}
	marker := testCommitMarker(attemptID, poolID, requestDigest, plan.PlanDigest)
	if _, err := r.CommitBuildAttempt(ctx, CommitAttemptInput{AttemptID: attemptID, OwnerID: "uid-builder-" + ids, FencingEpoch: 1, SnapshotID: int64(100 + spec.Number), CommitMarker: marker}); err != nil {
		t.Fatalf("commit resource UID build: %v", err)
	}
	seal := SnapshotSealInput{
		SealID: sealID, AttemptID: attemptID, CandidateID: candidateID, PhysicalPoolID: poolID, TenantDomain: "uid-tenant", Region: "us-east", EncryptionDomain: "uid-encryption", ObjectNamespace: "objects/uid-" + ids, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000007" + ids + "09", CatalogVersion: 1, DuckLakeSnapshotID: int64(100 + spec.Number), RelationNamespace: "candidate/uid-" + ids, RelationManifestDigest: testDigest('1'), ClosureDigest: testDigest('8'), ObjectRoot: "objects/uid-" + ids + "/snapshot", ObjectRootDigest: testDigest('6'), ArtifactRoot: "artifacts/uid-" + ids, ArtifactRootDigest: testDigest('7'), CompiledGraphDigest: graph.Digest(), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), RequestDigest: requestDigest, PlanDigest: plan.PlanDigest, CompatibilityDigest: testDigest('2'), ServingArtifactID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ServingArtifactDigest: artifactDigest, DuckDBVersion: "1", RuntimeVersion: "runtime-v1", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1", QualificationEvidence: []byte(`{"checks":["resource-uid"]}`),
	}
	if _, err := r.CreateSnapshotSeal(ctx, seal); err != nil {
		t.Fatalf("create resource UID seal: %v", err)
	}
	if _, err := r.QualifyCandidate(ctx, candidateID, sealID, testDigest('3')); err != nil {
		t.Fatalf("qualify resource UID candidate: %v", err)
	}
	if _, err := r.CreateGeneration(ctx, GenerationInput{GenerationID: generationID, TargetID: resourceUIDQualificationInstance, CandidateID: candidateID, SnapshotSealID: sealID, PlanID: planID, PlanDigest: plan.PlanDigest, ArtifactRoot: seal.ArtifactRoot, ArtifactRootDigest: seal.ArtifactRootDigest, ServingArtifactDigest: artifactDigest, CompiledGraphDigest: graph.Digest(), CompiledConfigDigest: testDigest('c'), SecurityDomainFingerprint: testDigest('d'), GenerationRevision: int64(spec.Number)}); err != nil {
		t.Fatalf("create resource UID generation: %v", err)
	}
	seedPhysicalRetentionFixture(t, r.DB(), seal)
	stageResourceUIDQualificationBundle(t, r.DB(), bundle, inventory, graph, generationID, projectID, artifactDigest)
	if _, err := r.CreatePublication(ctx, PublicationInput{PublicationID: publicationID, TargetID: resourceUIDQualificationInstance, GenerationID: generationID, ExpectedBaseGenerationID: spec.BaseGenerationID, CandidateID: candidateID, SnapshotSealID: sealID, ExpectedTargetRevision: int64(spec.Number), ActorID: "operator", RequestDigest: testDigest(byte('4' + byte(spec.Number)))}); err != nil {
		t.Fatalf("create resource UID publication: %v", err)
	}
	lease, err := r.AcquireLease(ctx, LeaseInput{LeaseID: leaseID, TargetID: resourceUIDQualificationInstance, OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatalf("acquire resource UID activation lease: %v", err)
	}
	return resourceUIDQualificationGeneration{projectID: projectID, generationID: generationID, publicationID: publicationID, candidateID: candidateID, sealID: sealID, graph: graph, activation: ActivationInput{PublicationID: publicationID, TargetID: resourceUIDQualificationInstance, GenerationID: generationID, ExpectedTargetRevision: int64(spec.Number), RequestDigest: testDigest(byte('4' + byte(spec.Number))), ActorID: "operator", CorrelationID: correlationID, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch}}
}

func prepareHistoricalResourceUIDRollback(t *testing.T, r *Repository, generation resourceUIDQualificationGeneration, revision int, baseGenerationID string) resourceUIDQualificationGeneration {
	t.Helper()
	ids := fmt.Sprintf("rollback-%d", revision)
	publicationID := "0198f2c0-7c7a-7f00-8a11-00000000" + fmt.Sprintf("%04d", 900+revision)
	leaseID := "0198f2c0-7c7a-7f00-8a11-00000000" + fmt.Sprintf("%04d", 910+revision)
	correlationID := "0198f2c0-7c7a-7f00-8a11-00000000" + fmt.Sprintf("%04d", 920+revision)
	requestDigest := testDigest([]byte("9a")[revision-4])
	if _, err := r.CreatePublication(t.Context(), PublicationInput{PublicationID: publicationID, TargetID: resourceUIDQualificationInstance, GenerationID: generation.generationID, ExpectedBaseGenerationID: baseGenerationID, CandidateID: generation.candidateID, SnapshotSealID: generation.sealID, ExpectedTargetRevision: int64(revision), ActorID: "operator", RequestDigest: requestDigest}); err != nil {
		t.Fatalf("create historical rollback publication %s: %v", ids, err)
	}
	lease, err := r.AcquireLease(t.Context(), LeaseInput{LeaseID: leaseID, TargetID: resourceUIDQualificationInstance, OwnerID: "operator", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatalf("acquire historical rollback lease %s: %v", ids, err)
	}
	return resourceUIDQualificationGeneration{
		projectID: generation.projectID, generationID: generation.generationID, publicationID: publicationID,
		candidateID: generation.candidateID, sealID: generation.sealID, graph: generation.graph,
		activation: ActivationInput{PublicationID: publicationID, TargetID: resourceUIDQualificationInstance, GenerationID: generation.generationID, ExpectedTargetRevision: int64(revision), RequestDigest: requestDigest, ActorID: "operator", CorrelationID: correlationID, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch},
	}
}

func stageResourceUIDQualificationBundle(t *testing.T, db DBTX, bundle projectartifact.SourceBundle, inventory project.ResourceUIDInventory, graph projectgraph.ProjectGraph, generationID, projectID, artifactDigest string) {
	t.Helper()
	tx, ok := db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		t.Fatal("resource UID bundle staging requires a PostgreSQL pool")
	}
	transaction, err := tx.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = transaction.Rollback(t.Context())
		}
	}()
	if _, err := servingnative.AdmitGenerationBundleTx(t.Context(), transaction, servingnative.GenerationBundleInput{GenerationID: generationID, ProjectID: projectgraph.ResourceID(projectID), Environment: "prod", Artifact: servingstate.Artifact{ID: "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"), ServingStateID: servingstate.ID(generationID), Digest: artifactDigest, Format: servingstate.ArtifactBundleFormat, ManifestJSON: "{}", SizeBytes: 1}, ArtifactLocator: "serving-artifacts/" + strings.TrimPrefix(artifactDigest, "sha256:") + ".tar.gz", StorageSecurityDomain: "uid-storage", ArtifactContentType: servingstate.ArtifactBundleContentType, ArtifactMetadataDigest: testDigest('9'), ProjectDigest: bundle.Digest(), AccessPolicyJSON: "{}", DashboardPublicationsJSON: "{}", DashboardAppearancesJSON: "{}", CreatedBy: "uid-qualification"}, graph); err != nil {
		t.Fatalf("admit serving bundle: %v", err)
	}
	if err := projectpostgres.New(transaction).AdmitResourceUIDInventoryTx(t.Context(), transaction, resourceUIDQualificationInstance, projectID, "prod", generationID, inventory); err != nil {
		t.Fatalf("admit resource UID inventory: %v", err)
	}
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	rollback = false
}

func resourceUIDQualificationBundle(t *testing.T, includeDashboard bool) (projectartifact.SourceBundle, project.ResourceUIDInventory, projectgraph.ProjectGraph) {
	t.Helper()
	resources := []projectgraph.Resource{{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"}, {ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"}, {ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"}, {ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"}, {ID: "pipeline:sales", Kind: projectgraph.KindPipeline, Name: "sales_refresh"}}
	edges := []projectgraph.Edge{{From: "source:orders", To: "connection:warehouse", Relation: "depends_on"}, {From: "model:orders", To: "source:orders", Relation: "depends_on"}, {From: "semantic:sales", To: "model:orders", Relation: "depends_on"}, {From: "pipeline:sales", To: "semantic:sales", Relation: "depends_on"}}
	if includeDashboard {
		resources = append(resources, projectgraph.Resource{ID: "dashboard:sales", Kind: projectgraph.KindDashboard, Name: "sales_dashboard"})
		edges = append(edges, projectgraph.Edge{From: "dashboard:sales", To: "semantic:sales", Relation: "depends_on"})
	}
	graph, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	manifest := projectmanifest.ResourceManifest{Title: "Resource UID qualification", Connections: map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed"}}, Sources: map[string]semanticmodel.Source{"source:orders": {Connection: "connection:warehouse"}}, Models: map[string]semanticmodel.Table{"model:orders": {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}, SourceDependencies: []string{"source:orders"}}}, SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": {Name: "sales", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders_model"}}}}, RefreshPipelines: map[string]refreshschedule.Definition{"pipeline:sales": {ID: "pipeline:sales", Name: "sales_refresh", SemanticModelID: "semantic:sales"}}, AuthoredResourceSources: resourceUIDQualificationAuthoredSources()}
	manifest.DashboardDefinitions = map[string]dashboarddefinition.Definition{}
	manifest.DashboardSources = map[string]projectmanifest.DashboardSource{}
	if includeDashboard {
		manifest.DashboardDefinitions["dashboard:sales"] = dashboarddefinition.Definition{ID: "dashboard:sales", SemanticModel: "semantic:sales"}
		manifest.DashboardSources["dashboard:sales"] = projectmanifest.DashboardSource{Document: dashboarddocument.DashboardDocument{
			APIVersion: "leapview.dev/v1", Kind: dashboarddocument.DashboardResourceKindDashboard,
			Metadata: dashboarddocument.DashboardMetadata{ID: "dashboard:sales", Name: "sales_dashboard"},
			Spec:     dashboarddocument.DashboardSpec{SemanticModel: "semantic:sales"},
		}}
	}
	bundle, err := projectartifact.NewSourceBundle(graph, manifest)
	if err != nil {
		t.Fatalf("create resource UID source bundle: %v", err)
	}
	inventory, err := project.NewResourceUIDInventory(bundle)
	if err != nil {
		t.Fatalf("seal resource UID inventory: %v", err)
	}
	return bundle, inventory, graph
}

func resourceUIDQualificationKindMismatchBundle(t *testing.T) (projectartifact.SourceBundle, project.ResourceUIDInventory, projectgraph.ProjectGraph) {
	t.Helper()
	resources := []projectgraph.Resource{
		{ID: "a_new", Kind: projectgraph.KindConnection, Name: "new_connection"},
		// The registry already contains connection:warehouse, but this
		// generation intentionally presents that authored ID as a source. The
		// inventory order allocates a_new first, then must fail on the conflict.
		{ID: "connection:warehouse", Kind: projectgraph.KindSource, Name: "warehouse"},
	}
	edges := []projectgraph.Edge{{From: "connection:warehouse", To: "a_new", Relation: "depends_on"}}
	graph, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		t.Fatal(err)
	}
	manifest := projectmanifest.ResourceManifest{
		Title:       "Resource UID kind mismatch qualification",
		Connections: map[string]semanticmodel.Connection{"a_new": {Kind: "managed"}},
		Sources:     map[string]semanticmodel.Source{"connection:warehouse": {Connection: "a_new"}},
		AuthoredResourceSources: map[string]string{
			"connection:warehouse": "apiVersion: leapview.dev/v1\nkind: Source\nmetadata:\n  id: connection:warehouse\n  name: warehouse\nspec:\n  connection: a_new\n  location:\n    type: path\n    path: orders.csv\n    format: csv\n",
		},
	}
	bundle, err := projectartifact.NewSourceBundle(graph, manifest)
	if err != nil {
		t.Fatalf("create kind mismatch source bundle: %v", err)
	}
	inventory, err := project.NewResourceUIDInventory(bundle)
	if err != nil {
		t.Fatalf("seal kind mismatch inventory: %v", err)
	}
	return bundle, inventory, graph
}

func resourceUIDQualificationAuthoredSources() map[string]string {
	return map[string]string{
		"source:orders":  "apiVersion: leapview.dev/v1\nkind: Source\nmetadata:\n  id: source:orders\n  name: orders\nspec:\n  connection: connection:warehouse\n  location:\n    type: path\n    path: orders.csv\n    format: csv\n",
		"model:orders":   "apiVersion: leapview.dev/v1\nkind: Model\nmetadata:\n  id: model:orders\n  name: orders_model\nspec:\n  definition:\n    type: sql\n    sql: SELECT 1 AS id\n  entities: {}\n  grain:\n    entity: id\n",
		"semantic:sales": "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata:\n  id: semantic:sales\n  name: sales\nspec:\n  datasets:\n    orders:\n      model: orders_model\n  metrics: {}\n",
	}
}

func resourceUIDsByAuthoredID(t *testing.T, db DBTX, projectID string) map[string]project.ResourceUID {
	t.Helper()
	rows, err := db.Query(t.Context(), `SELECT authored_resource_id, resource_uid FROM project.resource_uid_registry WHERE instance_id=$1 AND project_id=$2`, resourceUIDQualificationInstance, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := make(map[string]project.ResourceUID)
	for rows.Next() {
		var authored, uid string
		if err := rows.Scan(&authored, &uid); err != nil {
			t.Fatal(err)
		}
		result[authored] = project.ResourceUID(uid)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func sameResourceUIDMap(left, right map[string]project.ResourceUID) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range right {
		if left[key] != value {
			return false
		}
	}
	return true
}
