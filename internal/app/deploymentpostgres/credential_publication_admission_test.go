package deploymentpostgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	credentialpostgres "github.com/flidai/leapview/internal/credential/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	"github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/release"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type credentialPublicationFixture struct {
	db              *pgxpool.Pool
	delivery        *deploymentpostgres.Repository
	provenance      *releasepostgres.Repository
	publication     deploymentpostgres.DeliveryPublication
	activation      deploymentpostgres.ActivationInput
	generationInput GenerationAdmissionInput
}

func credentialPublicationUUID(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

type credentialPublicationFixtureOptions struct {
	localPin  bool
	editInput func(*GenerationAdmissionInput)
}

func newCredentialPublicationFixtureWithOptions(t *testing.T, options credentialPublicationFixtureOptions) credentialPublicationFixture {
	t.Helper()
	localPin := options.localPin
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
	provenance := releasepostgres.New(db)
	input := validGenerationAdmissionInput(t)
	actorID := credentialPublicationUUID(t)
	candidateVersionID := credentialPublicationUUID(t)
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
	publicationID := credentialPublicationUUID(t)
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
		LeaseID: credentialPublicationUUID(t), TargetID: input.Generation.TargetID, OwnerID: "activation-operator",
		ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("acquire target lease: %v", err)
	}
	activation := deploymentpostgres.ActivationInput{
		PublicationID: publication.PublicationID, TargetID: publication.TargetID, GenerationID: publication.GenerationID,
		ExpectedTargetRevision: publication.ExpectedTargetRevision, RequestDigest: publication.RequestDigest,
		ActorID: publication.ActorID, CorrelationID: credentialPublicationUUID(t), LeaseID: lease.LeaseID,
		OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch,
	}
	return credentialPublicationFixture{
		db: db, delivery: delivery, provenance: provenance,
		publication: publication, activation: activation, generationInput: input,
	}
}
