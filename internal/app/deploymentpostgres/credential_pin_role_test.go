package deploymentpostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/release"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The fixture applies the real control-plane baseline after provisioning the
// runtime role, so this proves the ordinary composition against production ACLs
// rather than test-specific grants.
func TestOrdinaryCredentialPublicationAdmissionRuntimeRole(t *testing.T) {
	admin, runtime := credentialPinRuntimePools(t)
	seedGenerationAdmissionBootstrap(t, admin, admissionInstanceID, "project_admission", "prod")

	delivery := deploymentnative.New(admin)
	provenance := releasepostgres.New(admin)
	input := validGenerationAdmissionInput(t)
	// Carry target-acquired provider evidence so predecessor republication
	// compares a nonempty binding fingerprint without introducing a local pin.
	input.Provenance.Plan.Bindings = []release.BindingEvidence{{
		BindingID: "provider-binding-admission", ConnectionID: "connection-admission",
		ConnectorKind: "postgres", Revision: 1, ValidatedVersion: "postgres-v1",
		EndpointConfigHash: admissionDigest('d'),
	}}
	gate := *input.Provenance.Plan.GateEvidence
	gate.BindingGeneration = release.BindingFingerprint(input.Provenance.Plan.Bindings)
	input.Provenance.Plan.GateEvidence = &gate
	input.Seal.QualificationEvidence = json.RawMessage(`{"checks":["schema"],"gates":{"bindingGeneration":"` + gate.BindingGeneration + `"}}`)

	lineage := lineagepostgres.New(admin)
	admission, err := NewGenerationAdmission(delivery, servingnative.New(admin), lineage, candidatePhysicalAdmissionStub{}, &testManagedDataBindingAdmission{}, provenance)
	if err != nil {
		t.Fatal(err)
	}
	seedGenerationAdmission(t, delivery, input)
	if _, err := admission.CompleteBuildAndAdmit(t.Context(), input); err != nil {
		t.Fatalf("admit provider-only candidate under admin seed authority: %v", err)
	}
	publication, err := delivery.CreatePublication(t.Context(), deploymentnative.PublicationInput{
		PublicationID: credentialPublicationUUID(t), TargetID: input.Generation.TargetID,
		GenerationID: input.Generation.GenerationID, CandidateID: input.Generation.CandidateID,
		SnapshotSealID: input.Generation.SnapshotSealID, ExpectedTargetRevision: 1,
		ActorID: credentialPublicationUUID(t), RequestDigest: admissionDigest('b'),
	})
	if err != nil {
		t.Fatalf("create candidate publication: %v", err)
	}
	lease, err := delivery.AcquireLease(t.Context(), deploymentnative.LeaseInput{
		LeaseID: credentialPublicationUUID(t), TargetID: publication.TargetID,
		OwnerID: "runtime-admission-operator", ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("acquire activation lease: %v", err)
	}
	activation := deploymentnative.ActivationInput{
		PublicationID: publication.PublicationID, TargetID: publication.TargetID,
		GenerationID: publication.GenerationID, ExpectedTargetRevision: publication.ExpectedTargetRevision,
		RequestDigest: publication.RequestDigest, ActorID: publication.ActorID,
		CorrelationID: credentialPublicationUUID(t), LeaseID: lease.LeaseID,
		OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch,
	}

	activationLineage, err := NewActivationLineageVerifier(lineagepostgres.New(runtime))
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := NewPersistence(runtime, Authorities{
		Access: accesspostgres.New(), Events: eventspostgres.New(),
		Jobs: jobspostgres.New(runtime), Operations: operationpostgres.New(runtime),
		Lineage: activationLineage,
		ApprovalAuthorize: deploymentnative.ApprovalAuthorizerFunc(func(context.Context, deploymentnative.ApprovalAuthorizationInput) error {
			return nil
		}),
	})
	if err != nil {
		t.Fatalf("compose deployment persistence over runtime pool: %v", err)
	}
	activated, err := persistence.Repository.Activate(t.Context(), activation)
	if err != nil {
		t.Fatalf("activate through composed ordinary admission under runtime role: %v", err)
	}
	if activated.Replay || activated.Publication.State != "committed" || activated.Pointer.TargetRevision != 2 {
		t.Fatalf("runtime-role activation = %#v", activated)
	}

	// Republish the same generation as a fresh CAS transition. This forces the
	// ordinary admission to load both successor and committed-predecessor
	// provenance through the runtime role.
	republication, err := delivery.CreatePublication(t.Context(), deploymentnative.PublicationInput{
		PublicationID: credentialPublicationUUID(t), TargetID: publication.TargetID,
		GenerationID: publication.GenerationID, CandidateID: publication.CandidateID,
		SnapshotSealID: publication.SnapshotSealID, ExpectedTargetRevision: 2,
		ActorID: publication.ActorID, RequestDigest: admissionDigest('c'),
	})
	if err != nil {
		t.Fatalf("create same-generation republication: %v", err)
	}
	repeatedActivation := activation
	repeatedActivation.PublicationID = republication.PublicationID
	repeatedActivation.ExpectedTargetRevision = republication.ExpectedTargetRevision
	repeatedActivation.RequestDigest = republication.RequestDigest
	repeatedActivation.CorrelationID = credentialPublicationUUID(t)
	repeated, err := persistence.Repository.Activate(t.Context(), repeatedActivation)
	if err != nil {
		t.Fatalf("activate same generation against committed predecessor under runtime role: %v", err)
	}
	if repeated.Replay || repeated.Publication.State != "committed" || repeated.Pointer.TargetRevision != 3 {
		t.Fatalf("runtime-role same-generation republication = %#v", repeated)
	}
}

func credentialPinRuntimePools(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Login: true, Password: "pin-runtime-migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Login: true, Password: "pin-runtime"})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Login: true, Password: "pin-maintenance"})
	readonly := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Login: true, Password: "pin-readonly"})
	backup := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup", Login: true, Password: "pin-backup"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "credential_pin_runtime_role")
	roles := []postgrestest.Role{owner, migrator, runtimeRole, maintenance, readonly, backup}
	for _, role := range roles {
		privileges := []string{"CONNECT"}
		if role.Name == owner.Name || role.Name == migrator.Name {
			privileges = append(privileges, "CREATE")
		}
		h.GrantDatabase(t, database.Name, role, privileges...)
	}
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(t.Context(), `GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatalf("grant migration role version-table authority: %v", err)
	}

	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })
	migrationPool, err := pgxpool.New(t.Context(), database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(migrationPool.Close)
	if err := postgresbaseline.ApplyWithMigrationFence(t.Context(), migrationPool, migrationDB); err != nil {
		t.Fatalf("apply canonical PostgreSQL baseline and runtime ACLs: %v", err)
	}

	runtime, err := platformpostgres.Open(t.Context(), platformpostgres.Config{
		URL: database.URL(runtimeRole), ExpectedMajor: platformpostgres.DefaultExpectedMajor,
		RuntimeRole: runtimeRole.Name, Intent: platformpostgres.IntentReadWrite,
		MinConns: 1, MaxConns: 4, AcquireTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("open production-role PostgreSQL runtime pool: %v", err)
	}
	t.Cleanup(runtime.Close)
	if err := runtime.NativePool().Ping(t.Context()); err != nil {
		t.Fatalf("ping runtime pool: %v", err)
	}
	return admin, runtime.NativePool()
}
