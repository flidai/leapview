package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	"github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/release"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errCredentialPublicationLateAudit = errors.New("injected delivery activation audit failure")

type activationPublicationFailingAudit struct {
	delegate deploymentpostgres.ActivationAuditPort
}

func (audit activationPublicationFailingAudit) AppendActivationAudit(ctx context.Context, tx deploymentpostgres.Tx, input deploymentpostgres.ActivationAuditInput) (deploymentpostgres.AuditEvent, error) {
	if _, err := audit.delegate.AppendActivationAudit(ctx, tx, input); err != nil {
		return deploymentpostgres.AuditEvent{}, err
	}
	return deploymentpostgres.AuditEvent{}, errCredentialPublicationLateAudit
}

func (audit activationPublicationFailingAudit) GetActivationAudit(ctx context.Context, tx deploymentpostgres.Tx, input deploymentpostgres.ActivationAuditInput) (deploymentpostgres.AuditEvent, error) {
	return audit.delegate.GetActivationAudit(ctx, tx, input)
}

type activationPublicationFixture struct {
	db              *pgxpool.Pool
	delivery        *deploymentpostgres.Repository
	credentials     *credentialpostgres.Repository
	provenance      *releasepostgres.Repository
	publication     deploymentpostgres.DeliveryPublication
	activation      deploymentpostgres.ActivationInput
	operationID     string
	prepared        credential.ActivationPreparation
	generationInput GenerationAdmissionInput
}

func activationPublicationUUID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

func newActivationPublicationFixture(t *testing.T, candidateVersionMatches bool) activationPublicationFixture {
	return newActivationPublicationFixtureWithOptions(t, activationPublicationFixtureOptions{
		candidateVersionMatches: candidateVersionMatches, prepareCredential: true, localPin: true,
	})
}

type activationPublicationFixtureOptions struct {
	candidateVersionMatches bool
	prepareCredential       bool
	localPin                bool
	extraLocalPin           bool
	editInput               func(*GenerationAdmissionInput)
}

func newActivationPublicationFixtureWithOptions(t *testing.T, options activationPublicationFixtureOptions) activationPublicationFixture {
	t.Helper()
	candidateVersionMatches, prepareCredential, localPin, extraLocalPin := options.candidateVersionMatches, options.prepareCredential, options.localPin, options.extraLocalPin
	db := generationAdmissionDB(t)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := credentialpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), postgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	delivery := deploymentpostgres.New(db)
	credentials, err := credentialpostgres.New(db, credentialFenceAudit{})
	if err != nil {
		t.Fatal(err)
	}
	provenance := releasepostgres.New(db)
	input := validGenerationAdmissionInput(t)
	actorID := activationPublicationUUID(t)
	versionID := activationPublicationUUID(t)
	candidateVersionID := versionID
	if !candidateVersionMatches {
		candidateVersionID = activationPublicationUUID(t)
	}
	const bindingID = "binding-admission"
	const resourceID = "connection-admission"
	const bindingRevision int64 = 1
	const configHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bindings := []release.BindingEvidence{}
	if localPin {
		bindings = append(bindings, release.BindingEvidence{
			BindingID: bindingID, ConnectionID: resourceID, ConnectorKind: "postgres", Revision: bindingRevision,
			CredentialVersionID: candidateVersionID, EndpointConfigHash: configHash,
		})
		// A credential-pinned local resource is represented by binding evidence,
		// never by the separate authored-connection projection.
		input.Provenance.Plan.AuthoredConnections = nil
	} else {
		bindings = append(bindings, release.BindingEvidence{
			BindingID: bindingID, ConnectionID: resourceID, ConnectorKind: "postgres", Revision: bindingRevision,
			ValidatedVersion: "postgres-v1", EndpointConfigHash: configHash,
		})
	}
	if extraLocalPin {
		bindings = append(bindings, release.BindingEvidence{
			BindingID: "binding-extra", ConnectionID: "connection-extra", ConnectorKind: "postgres", Revision: 1,
			CredentialVersionID: activationPublicationUUID(t), EndpointConfigHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		})
	}
	input.Provenance.Plan.Bindings = bindings
	gate := *input.Provenance.Plan.GateEvidence
	gate.BindingGeneration = release.BindingFingerprint(bindings)
	input.Provenance.Plan.GateEvidence = &gate
	input.Seal.QualificationEvidence = json.RawMessage(`{"checks":["schema"],"gates":{"bindingGeneration":"` + gate.BindingGeneration + `"}}`)
	if options.editInput != nil {
		options.editInput(&input)
	}

	physical := candidatePhysicalAdmissionStub{}
	lineage := lineagepostgres.New(db)
	capability, err := NewGenerationAdmission(delivery, servingnative.New(db), lineage, physical, &testManagedDataBindingAdmission{}, provenance)
	if err != nil {
		t.Fatal(err)
	}
	seedGenerationAdmission(t, delivery, input)
	if _, err := capability.CompleteBuildAndAdmit(t.Context(), input); err != nil {
		t.Fatalf("complete candidate generation admission: %v", err)
	}
	publicationID := activationPublicationUUID(t)
	requestDigest := admissionDigest('b')
	publication, err := delivery.CreatePublication(t.Context(), deploymentpostgres.PublicationInput{
		PublicationID: publicationID, TargetID: input.Generation.TargetID, GenerationID: input.Generation.GenerationID,
		CandidateID: input.Generation.CandidateID, SnapshotSealID: input.Generation.SnapshotSealID,
		ExpectedTargetRevision: 1, ActorID: actorID, RequestDigest: requestDigest,
	})
	if err != nil {
		t.Fatalf("create delivery publication: %v", err)
	}
	lease, err := delivery.AcquireLease(t.Context(), deploymentpostgres.LeaseInput{
		LeaseID: activationPublicationUUID(t), TargetID: input.Generation.TargetID, OwnerID: "activation-operator",
		ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("acquire target lease: %v", err)
	}
	activation := deploymentpostgres.ActivationInput{
		PublicationID: publication.PublicationID, TargetID: publication.TargetID, GenerationID: publication.GenerationID,
		ExpectedTargetRevision: publication.ExpectedTargetRevision, RequestDigest: publication.RequestDigest,
		ActorID: publication.ActorID, CorrelationID: activationPublicationUUID(t), LeaseID: lease.LeaseID,
		OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch,
	}
	if !prepareCredential {
		return activationPublicationFixture{
			db: db, delivery: delivery, credentials: credentials, provenance: provenance,
			publication: publication, activation: activation, generationInput: input,
		}
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	binding := encryption.Binding{
		DeploymentID: admissionInstanceID, TargetID: admissionInstanceID, OwnerID: "customer", ScopeKind: "connection",
		ProjectID: "project_admission", Environment: "prod", ResourceID: resourceID,
		Purpose: "connection-authentication", Provider: "postgres", Destination: configHash, VersionID: versionID,
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO credential.draft_version(version_id,deployment_id,owner_id,scope_kind,target_id,project_id,environment,resource_id,purpose,provider,destination,actor_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, binding.VersionID, binding.DeploymentID, binding.OwnerID, binding.ScopeKind, binding.TargetID, binding.ProjectID, binding.Environment, binding.ResourceID, binding.Purpose, binding.Provider, binding.Destination, actorID, now); err != nil {
		t.Fatal(err)
	}
	receipt := credential.ValidationReceipt{
		ReceiptID: activationPublicationUUID(t), Binding: binding, ActorID: actorID, BindingID: bindingID,
		BindingRevision: bindingRevision, ConfigurationDigest: admissionDigest('c'),
		ValidatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	auditIntent, err := receipt.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err := credentials.SaveValidation(t.Context(), receipt, auditIntent); err != nil {
		t.Fatalf("save exact validation receipt: %v", err)
	}
	operationID := activationPublicationUUID(t)
	preparation := credential.ActivationPreparation{
		OperationID: operationID, Receipt: receipt, ExpectedTargetRevision: publication.ExpectedTargetRevision,
		PredecessorGenerationID: publication.ExpectedBaseGenerationID, CandidateID: publication.CandidateID,
		GenerationID: publication.GenerationID, PublicationID: publication.PublicationID,
	}
	prepareTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.PrepareActivationTx(t.Context(), prepareTx, preparation, credentialPreparationTargetFence(delivery)); err != nil {
		_ = prepareTx.Rollback(context.Background())
		t.Fatalf("prepare credential activation: %v", err)
	}
	if err := prepareTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	switchTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = credentials.BeginActivationSwitchingTx(t.Context(), switchTx, admissionInstanceID, operationID, actorID,
		func(ctx context.Context, tx pgx.Tx, prepared credential.PreparedActivation, actor string) error {
			if actor != prepared.Preparation.Receipt.ActorID {
				return credential.ErrForbidden
			}
			return credentialPreparationTargetFence(delivery)(ctx, tx, prepared.Preparation)
		})
	if err != nil {
		_ = switchTx.Rollback(context.Background())
		t.Fatalf("begin credential activation switching: %v", err)
	}
	if err := switchTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return activationPublicationFixture{
		db: db, delivery: delivery, credentials: credentials, provenance: provenance,
		publication: publication, activation: activation, operationID: operationID, prepared: preparation, generationInput: input,
	}
}

func (fixture activationPublicationFixture) repository(t *testing.T, audit deploymentpostgres.ActivationAuditPort, authorize credentialpostgres.ActivationCommitAuthorizer) (*deploymentpostgres.Repository, *int) {
	t.Helper()
	lineage, err := NewActivationLineageVerifier(lineagepostgres.New(fixture.db))
	if err != nil {
		t.Fatal(err)
	}
	called := new(int)
	admission, err := newCredentialPublicationAdmission(fixture.delivery, fixture.provenance, fixture.credentials, fixture.operationID,
		func(ctx context.Context, tx credentialpostgres.Tx, prepared credential.PreparedActivation) error {
			*called++
			return authorize(ctx, tx, prepared)
		})
	if err != nil {
		t.Fatal(err)
	}
	return deploymentpostgres.NewWithOptions(fixture.db, deploymentpostgres.Options{
		ActivationAdmission: admission, ActivationAudit: audit, Events: postgres.New(), Lineage: lineage,
	}), called
}

func activationPublicationCurrentAuthority(fixture activationPublicationFixture) credentialpostgres.ActivationCommitAuthorizer {
	return func(ctx context.Context, tx credentialpostgres.Tx, prepared credential.PreparedActivation) error {
		if prepared.Preparation.Receipt.ActorID != fixture.activation.ActorID || prepared.Preparation.Receipt.Binding.OwnerID != "customer" {
			return credential.ErrForbidden
		}
		return credentialPreparationTargetFence(fixture.delivery)(ctx, tx, prepared.Preparation)
	}
}

func TestCredentialPublicationActivationIsAtomicAndReplaySafe(t *testing.T) {
	fixture := newActivationPublicationFixture(t, true)
	audit := deploymentaudit.NewWithRepository(accesspostgres.New())
	failedRepository, commitAuthorityCalls := fixture.repository(t, activationPublicationFailingAudit{delegate: audit}, activationPublicationCurrentAuthority(fixture))
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failedRepository.ActivateTx(t.Context(), tx, fixture.activation); !errors.Is(err, errCredentialPublicationLateAudit) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("activation with late delivery audit failure = %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit caller transaction after activation failure: %v", err)
	}
	assertActivationPublicationUnchanged(t, fixture, false)
	assertActivationCommitAuditCount(t, fixture, 0)
	if *commitAuthorityCalls != 1 {
		t.Fatalf("current authority calls after failed activation = %d, want 1", *commitAuthorityCalls)
	}

	workingRepository, commitAuthorityCalls := fixture.repository(t, audit, activationPublicationCurrentAuthority(fixture))
	tx, err = fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	activated, err := workingRepository.ActivateTx(t.Context(), tx, fixture.activation)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("credential-backed activation: %v", err)
	}
	if activated.Publication.State != "committed" || activated.Pointer.TargetRevision != 2 || activated.Pointer.ActiveGenerationID != fixture.publication.GenerationID || activated.Replay {
		_ = tx.Rollback(context.Background())
		t.Fatalf("activation result = %#v", activated)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	committed, err := fixture.credentials.GetPendingActivation(t.Context(), fixture.activation.TargetID)
	if err != nil || committed.CommittedAt.IsZero() {
		t.Fatalf("credential publication commit missing: %#v, %v", committed, err)
	}
	firstCommittedAt := committed.CommittedAt
	assertActivationCommitAuditCount(t, fixture, 1)
	if *commitAuthorityCalls != 1 {
		t.Fatalf("current authority calls after activation = %d, want 1", *commitAuthorityCalls)
	}

	tx, err = fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := workingRepository.ActivateTx(t.Context(), tx, fixture.activation)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("exact delivery replay: %v", err)
	}
	if !replayed.Replay || replayed.Publication != activated.Publication || replayed.Pointer != activated.Pointer {
		_ = tx.Rollback(context.Background())
		t.Fatalf("exact replay changed publication outcome: first=%#v replay=%#v", activated, replayed)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if *commitAuthorityCalls != 1 {
		t.Fatalf("replay reran credential commit authority: calls=%d", *commitAuthorityCalls)
	}
	committed, err = fixture.credentials.GetPendingActivation(t.Context(), fixture.activation.TargetID)
	if err != nil || committed.CommittedAt.IsZero() || !committed.CommittedAt.Equal(firstCommittedAt) {
		t.Fatalf("exact replay changed committed credential operation: %#v, %v", committed, err)
	}
	assertActivationCommitAuditCount(t, fixture, 1)
}

func TestCredentialPublicationAdmissionRejectsCandidatePinMismatch(t *testing.T) {
	fixture := newActivationPublicationFixture(t, false)
	audit := deploymentaudit.NewWithRepository(accesspostgres.New())
	repository, authorizeCalls := fixture.repository(t, audit, activationPublicationCurrentAuthority(fixture))
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateTx(t.Context(), tx, fixture.activation); !errors.Is(err, credential.ErrConflict) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("candidate/receipt version mismatch activation = %v, want credential conflict", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("caller commit after denied activation: %v", err)
	}
	if *authorizeCalls != 0 {
		t.Fatalf("current authority ran for mismatched immutable pin: %d calls", *authorizeCalls)
	}
	assertActivationPublicationUnchanged(t, fixture, false)
	assertActivationCommitAuditCount(t, fixture, 0)
}

func assertActivationPublicationUnchanged(t *testing.T, fixture activationPublicationFixture, wantCommitted bool) {
	t.Helper()
	target, err := fixture.delivery.Target(t.Context(), fixture.activation.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := fixture.delivery.Publication(t.Context(), fixture.publication.PublicationID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := fixture.credentials.GetPendingActivation(t.Context(), fixture.activation.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetRevision != 1 || target.ActiveGenerationID != "" || target.ActivePublicationID != "" ||
		publication.State != "pending" || publication.ResultTargetRevision != 0 || (!operation.CommittedAt.IsZero()) != wantCommitted {
		t.Fatalf("delivery/credential state after failed admission: target=%#v publication=%#v operation=%#v", target, publication, operation)
	}
	var eventCount, auditCount int
	if err := fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid`, fixture.publication.PublicationID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, fixture.publication.PublicationID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || auditCount != 0 {
		t.Fatalf("delivery evidence survived failed activation: event=%d audit=%d", eventCount, auditCount)
	}
}

func assertActivationCommitAuditCount(t *testing.T, fixture activationPublicationFixture, want int) {
	t.Helper()
	var got int
	if err := fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE aggregate_key=$1 AND action='credential.activation.committed'`, "credential-activation:"+fixture.operationID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("credential commit audit count = %d, want %d", got, want)
	}
}

type credentialFenceAudit struct{}

func (credentialFenceAudit) RecordAuditEvent(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
	_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	return err
}
func credentialPreparationTargetFence(delivery *deploymentpostgres.Repository) func(context.Context, pgx.Tx, credential.ActivationPreparation) error {
	return func(ctx context.Context, tx pgx.Tx, input credential.ActivationPreparation) error {
		target, err := delivery.TargetForUpdateTx(ctx, tx, input.Receipt.Binding.TargetID)
		if err != nil {
			return err
		}
		if target.TargetID != input.Receipt.Binding.DeploymentID || target.ProjectID != input.Receipt.Binding.ProjectID || target.Environment != input.Receipt.Binding.Environment || target.TargetRevision != input.ExpectedTargetRevision || target.ActiveGenerationID != input.PredecessorGenerationID {
			return credential.ErrConflict
		}
		return nil
	}
}

func TestCredentialPublicationCannotIntroduceAnotherConnectionsLocalPin(t *testing.T) {
	fixture := newActivationPublicationFixtureWithOptions(t, activationPublicationFixtureOptions{candidateVersionMatches: true, prepareCredential: true, localPin: true, extraLocalPin: true})
	repository, _ := fixture.repository(t, deploymentaudit.NewWithRepository(accesspostgres.New()), activationPublicationCurrentAuthority(fixture))
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repository.ActivateTx(t.Context(), tx, fixture.activation); !errors.Is(err, deploymentpostgres.ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("activation changed an unrelated connection pin: %v", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertActivationPublicationUnchanged(t, fixture, false)
	assertActivationCommitAuditCount(t, fixture, 0)
}
