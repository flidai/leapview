package deploymentpostgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	deploymentaudit "github.com/flidai/leapview/internal/app/deploymentaudit"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	"github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingnative "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5"
)

func ordinaryCredentialPublicationRepository(t *testing.T, fixture credentialPublicationFixture) *deploymentpostgres.Repository {
	t.Helper()
	admission, err := newOrdinaryCredentialPublicationAdmission(fixture.delivery, fixture.provenance)
	if err != nil {
		t.Fatalf("construct ordinary credential publication admission: %v", err)
	}
	lineage, err := NewActivationLineageVerifier(lineagepostgres.New(fixture.db))
	if err != nil {
		t.Fatal(err)
	}
	return deploymentpostgres.NewWithOptions(fixture.db, deploymentpostgres.Options{
		ActivationAdmission: admission, ActivationAudit: deploymentaudit.NewWithRepository(accesspostgres.New()),
		Events: postgres.New(), Lineage: lineage,
	})
}

func TestOrdinaryPublicationRequiresReceiptForFirstLocalCredentialPin(t *testing.T) {
	fixture := newCredentialPublicationFixtureWithOptions(t, credentialPublicationFixtureOptions{candidateVersionMatches: true, localPin: true})
	repository := ordinaryCredentialPublicationRepository(t, fixture)
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateTx(t.Context(), tx, fixture.activation); !errors.Is(err, deploymentpostgres.ErrConflict) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("ordinary first local pin activation = %v, want conflict", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("caller commit after denied local pin: %v", err)
	}
	assertOrdinaryDeliveryUnchanged(t, fixture)
}

func TestOrdinaryProviderOnlyPublicationRemainsAllowed(t *testing.T) {
	fixture := newCredentialPublicationFixtureWithOptions(t, credentialPublicationFixtureOptions{candidateVersionMatches: true})
	repository := ordinaryCredentialPublicationRepository(t, fixture)
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	activated, err := repository.ActivateTx(t.Context(), tx, fixture.activation)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("provider-only publication: %v", err)
	}
	if activated.Publication.State != "committed" || activated.Pointer.TargetRevision != 2 {
		_ = tx.Rollback(context.Background())
		t.Fatalf("provider-only activation result = %#v", activated)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialReceiptCannotSmuggleAnotherLocalPin(t *testing.T) {
	fixture := newCredentialPublicationFixtureWithOptions(t, credentialPublicationFixtureOptions{candidateVersionMatches: true, prepareCredential: true, localPin: true, extraLocalPin: true})
	repository, authorizeCalls := fixture.repository(t, deploymentaudit.NewWithRepository(accesspostgres.New()), credentialPublicationCurrentAuthority(fixture))
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateTx(t.Context(), tx, fixture.activation); !errors.Is(err, deploymentpostgres.ErrConflict) {
		_ = tx.Rollback(context.Background())
		t.Fatalf("receipt-backed activation with an extra local pin = %v, want conflict", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("caller commit after denied extra pin: %v", err)
	}
	if *authorizeCalls != 0 {
		t.Fatalf("credential commit authority ran before continuity denial: %d calls", *authorizeCalls)
	}
	assertCredentialPublicationUnchanged(t, fixture, false)
	assertCredentialCommitAuditCount(t, fixture, 0)
}

func TestCredentialReceiptCanReplaceOneExistingLocalPin(t *testing.T) {
	fixture := newCredentialPublicationFixtureWithOptions(t, credentialPublicationFixtureOptions{candidateVersionMatches: true, localPin: true, extraLocalPin: true})
	activateCredentialPinnedBaseForContinuityTest(t, fixture)
	basePins := fixture.generationInput.Provenance.Plan.Bindings
	if len(basePins) != 2 {
		t.Fatalf("base local pins = %d, want 2", len(basePins))
	}
	replacement := basePins[0]
	replacement.CredentialVersionID = credentialPublicationUUID(t)
	replacement.Revision++
	replacement.EndpointConfigHash = admissionDigest('d')
	bindings := []release.BindingEvidence{replacement, basePins[1]}
	publication, activation := createCredentialPublicationSuccessor(t, fixture, bindings)
	successorFixture := prepareCredentialPublicationSuccessor(t, fixture, publication, activation, replacement)
	repository, _ := successorFixture.repository(t, deploymentaudit.NewWithRepository(accesspostgres.New()), credentialPublicationCurrentAuthority(successorFixture))
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	activated, err := repository.ActivateTx(t.Context(), tx, activation)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("receipt-backed replacement of one local pin: %v", err)
	}
	if activated.Pointer.ActiveGenerationID != activation.GenerationID || activated.Pointer.TargetRevision != 3 {
		_ = tx.Rollback(context.Background())
		t.Fatalf("receipt-backed replacement result = %#v", activated)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func prepareCredentialPublicationSuccessor(
	t *testing.T,
	fixture credentialPublicationFixture,
	publication deploymentpostgres.DeliveryPublication,
	activation deploymentpostgres.ActivationInput,
	bindingEvidence release.BindingEvidence,
) credentialPublicationFixture {
	t.Helper()
	actorID := fixture.activation.ActorID
	binding := encryption.Binding{
		DeploymentID: admissionInstanceID, TargetID: admissionInstanceID, OwnerID: "customer", ScopeKind: "connection",
		ProjectID: "project_admission", Environment: "prod", ResourceID: bindingEvidence.ConnectionID,
		Purpose: "connection-authentication", Provider: "postgres", Destination: bindingEvidence.EndpointConfigHash,
		VersionID: bindingEvidence.CredentialVersionID,
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := fixture.db.Exec(t.Context(), `INSERT INTO credential.draft_version(version_id,deployment_id,owner_id,scope_kind,target_id,project_id,environment,resource_id,purpose,provider,destination,actor_id,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, binding.VersionID, binding.DeploymentID, binding.OwnerID, binding.ScopeKind, binding.TargetID, binding.ProjectID, binding.Environment, binding.ResourceID, binding.Purpose, binding.Provider, binding.Destination, actorID, now); err != nil {
		t.Fatal(err)
	}
	receipt := credential.ValidationReceipt{
		ReceiptID: credentialPublicationUUID(t), Binding: binding, ActorID: actorID,
		BindingID: bindingEvidence.BindingID, BindingRevision: bindingEvidence.Revision,
		ConfigurationDigest: admissionDigest('c'), ValidatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	}
	auditIntent, err := receipt.AuditIntent()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.credentials.SaveValidation(t.Context(), receipt, auditIntent); err != nil {
		t.Fatalf("save successor pin validation receipt: %v", err)
	}
	operationID := credentialPublicationUUID(t)
	preparation := credential.ActivationPreparation{
		OperationID: operationID, Receipt: receipt, ExpectedTargetRevision: publication.ExpectedTargetRevision,
		PredecessorGenerationID: publication.ExpectedBaseGenerationID, CandidateID: publication.CandidateID,
		GenerationID: publication.GenerationID, PublicationID: publication.PublicationID,
	}
	prepareTx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.credentials.PrepareActivationTx(t.Context(), prepareTx, preparation, credentialPreparationTargetFence(fixture.delivery)); err != nil {
		_ = prepareTx.Rollback(context.Background())
		t.Fatalf("prepare successor local pin: %v", err)
	}
	if err := prepareTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	switchTx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.credentials.BeginActivationSwitchingTx(t.Context(), switchTx, admissionInstanceID, operationID, actorID,
		func(ctx context.Context, tx pgx.Tx, prepared credential.PreparedActivation, actor string) error {
			if actor != prepared.Preparation.Receipt.ActorID {
				return credential.ErrForbidden
			}
			return credentialPreparationTargetFence(fixture.delivery)(ctx, tx, prepared.Preparation)
		})
	if err != nil {
		_ = switchTx.Rollback(context.Background())
		t.Fatalf("begin successor local pin switching: %v", err)
	}
	if err := switchTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	fixture.publication, fixture.activation, fixture.operationID, fixture.prepared = publication, activation, operationID, preparation
	return fixture
}

func TestLocalCredentialPinsMustMatchCommittedPredecessor(t *testing.T) {
	for _, test := range []struct {
		name       string
		successor  func(release.BindingEvidence) []release.BindingEvidence
		wantDenied bool
	}{
		{
			name: "same tuple succeeds",
			successor: func(base release.BindingEvidence) []release.BindingEvidence {
				return []release.BindingEvidence{base}
			},
		},
		{
			name: "credential version change denied",
			successor: func(base release.BindingEvidence) []release.BindingEvidence {
				base.CredentialVersionID = credentialPublicationUUID(t)
				return []release.BindingEvidence{base}
			},
			wantDenied: true,
		},
		{
			name: "binding identity change denied",
			successor: func(base release.BindingEvidence) []release.BindingEvidence {
				base.BindingID = "binding-replacement"
				return []release.BindingEvidence{base}
			},
			wantDenied: true,
		},
		{
			name: "binding revision change denied",
			successor: func(base release.BindingEvidence) []release.BindingEvidence {
				base.Revision++
				return []release.BindingEvidence{base}
			},
			wantDenied: true,
		},
		{
			name: "endpoint change denied",
			successor: func(base release.BindingEvidence) []release.BindingEvidence {
				base.EndpointConfigHash = admissionDigest('b')
				return []release.BindingEvidence{base}
			},
			wantDenied: true,
		},
		{
			name: "local pin removal denied",
			successor: func(base release.BindingEvidence) []release.BindingEvidence {
				base.CredentialVersionID = ""
				base.ValidatedVersion = "postgres-v1"
				return []release.BindingEvidence{base}
			},
			wantDenied: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCredentialPublicationFixtureWithOptions(t, credentialPublicationFixtureOptions{candidateVersionMatches: true, localPin: true})
			activateCredentialPinnedBaseForContinuityTest(t, fixture)
			basePin := fixture.generationInput.Provenance.Plan.Bindings[0]
			bindings := test.successor(basePin)
			publication, activation := createCredentialPublicationSuccessor(t, fixture, bindings)
			repository := ordinaryCredentialPublicationRepository(t, fixture)
			tx, err := fixture.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			activated, activationErr := repository.ActivateTx(t.Context(), tx, activation)
			if test.wantDenied {
				if !errors.Is(activationErr, deploymentpostgres.ErrConflict) {
					_ = tx.Rollback(context.Background())
					t.Fatalf("successor pin change activation = %v, want conflict", activationErr)
				}
				if err := tx.Commit(t.Context()); err != nil {
					t.Fatalf("caller commit after denied successor: %v", err)
				}
				current, err := fixture.delivery.Target(t.Context(), activation.TargetID)
				if err != nil {
					t.Fatal(err)
				}
				gotPublication, err := fixture.delivery.Publication(t.Context(), publication.PublicationID)
				if err != nil {
					t.Fatal(err)
				}
				if current.TargetRevision != 2 || current.ActiveGenerationID != fixture.publication.GenerationID || gotPublication.State != "pending" || gotPublication.ResultTargetRevision != 0 {
					t.Fatalf("denied successor changed active state: target=%#v publication=%#v", current, gotPublication)
				}
				var eventCount, auditCount, generationRootCount int
				if err := fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid`, publication.PublicationID).Scan(&eventCount); err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, publication.PublicationID).Scan(&auditCount); err != nil {
					t.Fatal(err)
				}
				if err := fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM delivery.delivery_retention_root WHERE generation_id=$1::uuid AND root_kind='generation'`, activation.GenerationID).Scan(&generationRootCount); err != nil {
					t.Fatal(err)
				}
				if eventCount != 0 || auditCount != 0 || generationRootCount != 0 {
					t.Fatalf("denied successor left activation effects: events=%d audits=%d generation_roots=%d", eventCount, auditCount, generationRootCount)
				}
				return
			}
			if activationErr != nil {
				_ = tx.Rollback(context.Background())
				t.Fatalf("same-pin successor activation: %v", activationErr)
			}
			if activated.Pointer.ActiveGenerationID != activation.GenerationID || activated.Pointer.TargetRevision != 3 {
				_ = tx.Rollback(context.Background())
				t.Fatalf("same-pin successor result = %#v", activated)
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func activateCredentialPinnedBaseForContinuityTest(t *testing.T, fixture credentialPublicationFixture) {
	t.Helper()
	lineage, err := NewActivationLineageVerifier(lineagepostgres.New(fixture.db))
	if err != nil {
		t.Fatal(err)
	}
	repository := deploymentpostgres.NewWithOptions(fixture.db, deploymentpostgres.Options{
		// This base models a previously completed credential readiness lifecycle;
		// publication admission is intentionally bypassed only for fixture setup.
		ActivationAdmission: func(context.Context, deploymentpostgres.Tx, deploymentpostgres.DeliveryPublication) error { return nil },
		ActivationAudit:     deploymentaudit.NewWithRepository(accesspostgres.New()), Events: postgres.New(), Lineage: lineage,
	})
	tx, err := fixture.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateTx(t.Context(), tx, fixture.activation); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("commit credential-pinned predecessor: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func createCredentialPublicationSuccessor(t *testing.T, fixture credentialPublicationFixture, bindings []release.BindingEvidence) (deploymentpostgres.DeliveryPublication, deploymentpostgres.ActivationInput) {
	t.Helper()
	input := admitCredentialPublicationSuccessor(t, fixture, bindings)
	plan, err := fixture.delivery.Plan(t.Context(), input.Generation.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := fixture.delivery.CreatePublication(t.Context(), deploymentpostgres.PublicationInput{
		PublicationID: credentialPublicationUUID(t), TargetID: input.Generation.TargetID, GenerationID: input.Generation.GenerationID,
		CandidateID: input.Generation.CandidateID, SnapshotSealID: input.Generation.SnapshotSealID, ExpectedTargetRevision: 2,
		ActorID: fixture.activation.ActorID, RequestDigest: admissionDigest(byte('a') + byte(plan.PlanRevision)),
	})
	if err != nil {
		t.Fatalf("create successor publication: %v", err)
	}
	lease, err := fixture.delivery.AcquireLease(t.Context(), deploymentpostgres.LeaseInput{
		LeaseID: credentialPublicationUUID(t), TargetID: input.Generation.TargetID, OwnerID: "successor-operator",
		ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("acquire successor activation lease: %v", err)
	}
	activation := deploymentpostgres.ActivationInput{
		PublicationID: publication.PublicationID, TargetID: publication.TargetID, GenerationID: publication.GenerationID,
		ExpectedTargetRevision: publication.ExpectedTargetRevision, RequestDigest: publication.RequestDigest,
		ActorID: publication.ActorID, CorrelationID: credentialPublicationUUID(t), LeaseID: lease.LeaseID,
		OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch,
	}
	return publication, activation
}

// Stop at admission so refresh finalization can own publication and its lease.
func admitCredentialPublicationSuccessor(t *testing.T, fixture credentialPublicationFixture, bindings []release.BindingEvidence) GenerationAdmissionInput {
	t.Helper()
	input := validGenerationAdmissionInput(t)
	planID, candidateID, attemptID := credentialPublicationUUID(t), credentialPublicationUUID(t), credentialPublicationUUID(t)
	generationID, sealID, leaseID := credentialPublicationUUID(t), credentialPublicationUUID(t), credentialPublicationUUID(t)
	deliveryID := credentialPublicationUUID(t)
	nextFencingEpoch := nextDeliveryCounter(t, fixture.delivery, input.Generation.TargetID, "next_fencing_epoch")
	nextPlanRevision := nextDeliveryCounter(t, fixture.delivery, input.Generation.TargetID, "next_plan_revision")
	input.Commit.DeliveryID, input.Commit.AttemptID, input.Commit.FencingEpoch = deliveryID, attemptID, nextFencingEpoch
	input.Commit.SnapshotID = 43
	input.Fence.LeaseID, input.Fence.FencingEpoch = leaseID, nextFencingEpoch
	input.Generation.GenerationID, input.Generation.CandidateID, input.Generation.SnapshotSealID, input.Generation.PlanID = generationID, candidateID, sealID, planID
	input.Seal.SealID, input.Seal.AttemptID, input.Seal.CandidateID = sealID, attemptID, candidateID
	input.Seal.DuckLakeSnapshotID = input.Commit.SnapshotID
	// The physical catalog identity belongs to the reused test pool; each
	// candidate still receives a unique snapshot and relation namespace.
	input.Seal.CatalogID = "catalog-admission"
	input.Seal.ObjectNamespace = "objects/admission/successor"
	input.Seal.ObjectRoot = "objects/admission/successor/43"
	input.Seal.ArtifactRoot = "artifacts/admission/successor"
	input.Generation.ArtifactRoot = input.Seal.ArtifactRoot
	input.Generation.ArtifactRootDigest = input.Seal.ArtifactRootDigest
	input.Bundle.GenerationID = generationID
	input.Bundle.Artifact.ServingStateID = servingstate.ID(generationID)
	input.Provenance.Plan.Identity.GenerationID = generationID
	input.Provenance.Candidate.ID = candidateID
	input.Provenance.Plan.Bindings = append([]release.BindingEvidence(nil), bindings...)
	if hasLocalCredentialPin(bindings) {
		input.Provenance.Plan.AuthoredConnections = nil
	}
	gate := *input.Provenance.Plan.GateEvidence
	gate.CandidateID = candidateID
	gate.BindingGeneration = release.BindingFingerprint(bindings)
	input.Provenance.Plan.GateEvidence = &gate
	input.Seal.QualificationEvidence = json.RawMessage(`{"checks":["schema"],"gates":{"bindingGeneration":"` + gate.BindingGeneration + `"}}`)
	input.Seal.RelationNamespace, _ = deployment.DeriveRelationNamespace(deployment.RelationNamespaceInput{CandidateID: candidateID, AttemptID: attemptID, FencingEpoch: nextFencingEpoch})
	input.Seal.RequestDigest = admissionDigest('d')
	input.QualificationDigest = admissionDigest('3')
	plan := nativePlanFixture(t, deploymentpostgres.PlanInput{
		PlanID: planID, TargetID: input.Generation.TargetID, PlanRevision: nextPlanRevision,
		CompiledGraphDigest: input.Generation.CompiledGraphDigest, CompiledConfigDigest: input.Generation.CompiledConfigDigest,
		SecurityDomainFingerprint: input.Generation.SecurityDomainFingerprint, ArtifactDigest: input.Generation.ServingArtifactDigest,
		QualificationDigest: input.QualificationDigest,
	}, input.Bundle.ProjectID.String(), input.AuthorizationPolicy)
	input.Generation.PlanDigest, input.Seal.PlanDigest = plan.PlanDigest, plan.PlanDigest
	marker := catalogartifact.CommitMarker{
		SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: deliveryID, GenerationID: generationID,
		AttemptID: attemptID, LeaseEpoch: nextFencingEpoch, RequestDigest: input.Seal.RequestDigest, PlanDigest: plan.PlanDigest,
		Project: input.Bundle.ProjectID.String(), Environment: string(input.Bundle.Environment), PhysicalPoolID: input.Seal.PhysicalPoolID,
	}
	markerJSON, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	input.Commit.CommitMarker = json.RawMessage(markerJSON)
	lineage := lineagepostgres.New(fixture.db)
	capability, err := NewGenerationAdmission(fixture.delivery, servingnative.New(fixture.db), lineage, candidatePhysicalAdmissionStub{}, &testManagedDataBindingAdmission{}, fixture.provenance)
	if err != nil {
		t.Fatal(err)
	}
	seedGenerationAdmissionWithPlan(t, fixture.delivery, input, plan)
	if _, err := capability.CompleteBuildAndAdmit(t.Context(), input); err != nil {
		t.Fatalf("admit credential publication successor: %v", err)
	}
	return input
}

func nextDeliveryCounter(t *testing.T, repo *deploymentpostgres.Repository, targetID, counter string) int64 {
	t.Helper()
	if counter != "next_fencing_epoch" && counter != "next_plan_revision" {
		t.Fatalf("unsupported delivery counter %q", counter)
	}
	tx, err := repo.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	query := `SELECT ` + counter + ` FROM delivery.delivery_target_fence WHERE target_id=$1`
	if counter == "next_plan_revision" {
		query = `SELECT next_plan_revision FROM delivery.delivery_target_revision WHERE target_id=$1`
	}
	var value int64
	if err := tx.QueryRow(t.Context(), query, targetID).Scan(&value); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if value <= 0 {
		t.Fatalf("delivery %s = %d", counter, value)
	}
	return value
}

func hasLocalCredentialPin(bindings []release.BindingEvidence) bool {
	for _, binding := range bindings {
		if binding.CredentialVersionID != "" {
			return true
		}
	}
	return false
}

func assertOrdinaryDeliveryUnchanged(t *testing.T, fixture credentialPublicationFixture) {
	t.Helper()
	target, err := fixture.delivery.Target(t.Context(), fixture.activation.TargetID)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := fixture.delivery.Publication(t.Context(), fixture.publication.PublicationID)
	if err != nil {
		t.Fatal(err)
	}
	if target.TargetRevision != 1 || target.ActiveGenerationID != "" || publication.State != "pending" || publication.ResultTargetRevision != 0 {
		t.Fatalf("ordinary publication state changed after denial: target=%#v publication=%#v", target, publication)
	}
	var eventCount, auditCount int
	if err := fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE event_id=$1::uuid`, fixture.publication.PublicationID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id=$1::uuid`, fixture.publication.PublicationID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || auditCount != 0 {
		t.Fatalf("ordinary delivery evidence survived denied publication: event=%d audit=%d", eventCount, auditCount)
	}
}
