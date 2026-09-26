package module

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/semanticvalue"
)

const (
	af09SemanticActor     = "60000000-0000-0000-0000-000000000001"
	af09SemanticPrincipal = "60000000-0000-0000-0000-000000000002"
)

// TestNativeCoordinatorPostgresAF09SemanticMutationSerializesWithRollback
// uses the real semantic activation authority lock in the native activation
// transaction. A committed assignment mutation cannot cross that lock: the
// rollback commits first, then the mutation commits against the next control
// revision. This is the allowed linearization order for a current restriction
// arriving concurrently with cutover.
func TestNativeCoordinatorPostgresAF09SemanticMutationSerializesWithRollback(t *testing.T) {
	f := newNativePGFixture(t)
	authority, assignment := af09SemanticAuthority(t, f)
	initial := af09SemanticControlState(t, authority)
	if initial.State.Revision <= 0 || initial.State.Digest == "" {
		t.Fatalf("initial semantic control authority = %#v", initial.State)
	}

	if _, err := f.repo.CreateRetentionRoot(t.Context(), deploymentpostgres.DeliveryRetentionRoot{
		RootID: f.generation, TargetID: f.targetID, CandidateID: f.candidate,
		GenerationID: f.generation, SnapshotSealID: f.seal, RootKind: "generation", State: "live",
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := f.coordinator.RollbackGeneration(t.Context(), nativeRollbackRequest(f, "af09-serial-rollback"))
	if err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	f.coordinator.beforeActivationCommit = af09SemanticFence(authority, initial.State.Revision, locked, release)
	activationDone := make(chan error, 1)
	go func() {
		_, activationErr := f.coordinator.Activate(context.Background(), nativeActivationRequest(pending.ID.String(), "af09-serial-activate"))
		activationDone <- activationErr
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("rollback activation did not acquire semantic authority lock")
	}

	mutationStarted := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		close(mutationStarted)
		_, mutationErr := authority.SetSemanticAttributeAssignment(context.Background(), access.SemanticAttributeAssignmentInput{
			AssignmentID: assignment.ID, DefinitionID: assignment.DefinitionID, Subject: assignment.Subject,
			Values: []string{"EMEA"}, ExpectedVersion: assignment.AssignmentVersion,
			Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: af09SemanticActor},
		})
		mutationDone <- mutationErr
	}()
	<-mutationStarted
	// The mutation uses the same control row lock as the activation fence. Keep
	// the fence held long enough to prove it cannot complete before release.
	select {
	case err := <-mutationDone:
		t.Fatalf("semantic restriction crossed activation lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	if err := <-activationDone; err != nil {
		t.Fatalf("rollback activation: %v", err)
	}
	if err := <-mutationDone; err != nil {
		t.Fatalf("committed semantic restriction: %v", err)
	}
	active, err := f.repo.Target(t.Context(), f.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if active.TargetRevision != 2 || active.ActiveGenerationID != f.generation {
		t.Fatalf("rollback pointer after serialized mutation = %#v", active)
	}
	current := af09SemanticControlState(t, authority)
	if current.State.Revision <= initial.State.Revision || current.State.Digest == initial.State.Digest {
		t.Fatalf("semantic restriction did not advance current authority: initial=%#v current=%#v", initial.State, current.State)
	}
	assertAF09ActivationEvidence(t, f, f.generation)
	// An exact retry is reconciliation of the already committed activation. It
	// must replay the durable outcome even though current semantic authority is
	// now different; no new cutover or semantic permission is granted by it.
	replay, err := f.coordinator.Activate(t.Context(), nativeActivationRequest(pending.ID.String(), "af09-serial-activate"))
	if err != nil || replay.Status != apiadapter.StatusActive || replay.ID != pending.ID.String() {
		t.Fatalf("committed rollback activation replay = %#v, %v", replay, err)
	}
	assertAF09ActivationEvidence(t, f, f.generation)
}

// TestNativeCoordinatorPostgresAF09StaleRollbackHasNoActivationEvidence
// commits a semantic restriction before rollback activation. The activation
// transaction locks and re-reads the current authority, rejects the retained
// generation as stale, and rolls back its pointer/event/audit consequences.
func TestNativeCoordinatorPostgresAF09StaleRollbackHasNoActivationEvidence(t *testing.T) {
	f := newNativePGFixture(t)
	authority, assignment := af09SemanticAuthority(t, f)
	initial := af09SemanticControlState(t, authority)
	if _, err := authority.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		AssignmentID: assignment.ID, DefinitionID: assignment.DefinitionID, Subject: assignment.Subject,
		Values: []string{"EMEA"}, ExpectedVersion: assignment.AssignmentVersion,
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: af09SemanticActor},
	}); err != nil {
		t.Fatal(err)
	}
	current := af09SemanticControlState(t, authority)
	if current.State.Revision <= initial.State.Revision || current.State.Digest == initial.State.Digest {
		t.Fatalf("semantic restriction did not commit: initial=%#v current=%#v", initial.State, current.State)
	}

	if _, err := f.repo.CreateRetentionRoot(t.Context(), deploymentpostgres.DeliveryRetentionRoot{
		RootID: f.generation, TargetID: f.targetID, CandidateID: f.candidate,
		GenerationID: f.generation, SnapshotSealID: f.seal, RootKind: "generation", State: "live",
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := f.coordinator.RollbackGeneration(t.Context(), nativeRollbackRequest(f, "af09-stale-rollback"))
	if err != nil {
		t.Fatal(err)
	}
	f.coordinator.beforeActivationCommit = af09SemanticFence(authority, initial.State.Revision, nil, nil)
	if _, err := f.coordinator.Activate(t.Context(), nativeActivationRequest(pending.ID.String(), "af09-stale-activate")); !errors.Is(err, deployment.ErrConflict) {
		t.Fatalf("stale rollback activation = %v, want deployment conflict", err)
	}

	active, err := f.repo.Target(t.Context(), f.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if active.TargetRevision != 1 || active.ActiveGenerationID != "" || active.ActivePublicationID != "" {
		t.Fatalf("stale rollback changed pointer = %#v", active)
	}
	var activationEvents, activationAudits int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE aggregate_id=$1 AND event_type='activation_committed'`, f.targetID).Scan(&activationEvents); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='activate' AND resource_kind='generation' AND resource_id=$1`, f.generation).Scan(&activationAudits); err != nil {
		t.Fatal(err)
	}
	if activationEvents != 0 || activationAudits != 0 {
		t.Fatalf("stale rollback left activation evidence: events=%d audits=%d", activationEvents, activationAudits)
	}
	stored, err := f.repo.Publication(t.Context(), pending.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "pending" || stored.ResultTargetRevision != 0 {
		t.Fatalf("stale rollback publication = %#v", stored)
	}
}

func TestNativeCoordinatorPostgresAF09UnavailableEligibilityFailsClosed(t *testing.T) {
	f := newNativePGFixture(t)
	if _, err := f.repo.CreateRetentionRoot(t.Context(), deploymentpostgres.DeliveryRetentionRoot{
		RootID: f.generation, TargetID: f.targetID, CandidateID: f.candidate,
		GenerationID: f.generation, SnapshotSealID: f.seal, RootKind: "generation", State: "live",
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := f.coordinator.RollbackGeneration(t.Context(), nativeRollbackRequest(f, "af09-unavailable-rollback"))
	if err != nil {
		t.Fatal(err)
	}
	f.coordinator.beforeActivationCommit = af09SemanticFence(nil, 1, nil, nil)
	if _, err := f.coordinator.Activate(t.Context(), nativeActivationRequest(pending.ID.String(), "af09-unavailable-activate")); !errors.Is(err, deployment.ErrConflict) {
		t.Fatalf("unavailable semantic eligibility = %v, want deployment conflict", err)
	}
	active, err := f.repo.Target(t.Context(), f.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if active.TargetRevision != 1 || active.ActiveGenerationID != "" || active.ActivePublicationID != "" {
		t.Fatalf("unavailable eligibility changed pointer = %#v", active)
	}
	var events, audits int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE aggregate_id=$1 AND event_type='activation_committed'`, f.targetID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='activate' AND resource_kind='generation' AND resource_id=$1`, f.generation).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if events != 0 || audits != 0 {
		t.Fatalf("unavailable eligibility left activation evidence: events=%d audits=%d", events, audits)
	}
}

func af09SemanticAuthority(t *testing.T, f *nativePGFixture) (*accesspostgres.Repository, access.SemanticAttributeAssignment) {
	t.Helper()
	if _, err := f.db.Exec(t.Context(), `INSERT INTO access.principal (id, principal_type, status) VALUES ($1::uuid, 'user', 'active'), ($2::uuid, 'user', 'active') ON CONFLICT (id) DO NOTHING`, af09SemanticActor, af09SemanticPrincipal); err != nil {
		t.Fatal(err)
	}
	authority, err := accesspostgres.NewAccess(f.db, accesspostgres.FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := authority.RegisterSemanticAttribute(t.Context(), access.RegisterSemanticAttributeInput{
		Name: "af09_regions", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeList,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}},
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: af09SemanticActor},
	})
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := authority.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: af09SemanticPrincipal},
		Values: []string{"AMER", "EMEA"}, Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: af09SemanticActor},
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority, assignment
}

func af09SemanticControlState(t *testing.T, authority *accesspostgres.Repository) access.SemanticAttributeControlSnapshot {
	t.Helper()
	state, err := authority.SemanticAttributeControl(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func af09SemanticFence(authority *accesspostgres.Repository, expectedRevision int64, locked chan<- struct{}, release <-chan struct{}) deploymentpostgres.ActivationPreCommitHook {
	return func(ctx context.Context, tx deploymentpostgres.Tx, _ deploymentpostgres.DeliveryPublication) error {
		if authority == nil {
			return fmt.Errorf("%w: semantic eligibility authority is unavailable", deploymentpostgres.ErrNotQualified)
		}
		_, current, err := authority.SemanticAttributeActivationAuthorityTx(ctx, tx)
		if err != nil {
			return err
		}
		if locked != nil {
			close(locked)
		}
		if release != nil {
			<-release
		}
		if current.State.Revision != expectedRevision {
			return fmt.Errorf("%w: semantic eligibility revision changed from %d to %d", deploymentpostgres.ErrNotQualified, expectedRevision, current.State.Revision)
		}
		return nil
	}
}

func nativeActivationRequest(publicationID, key string) apiadapter.ActivateRequest {
	return apiadapter.ActivateRequest{Scope: apiadapter.Scope{Project: "project_sales", DeploymentID: publicationID}, Actor: "operator", IdempotencyKey: key}
}

func assertAF09ActivationEvidence(t *testing.T, f *nativePGFixture, generationID string) {
	t.Helper()
	var events, audits int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE aggregate_id=$1 AND event_type='activation_committed'`, f.targetID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE action='activate' AND resource_kind='generation' AND resource_id=$1`, generationID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if events != 1 || audits != 1 {
		t.Fatalf("serialized rollback activation evidence = events %d audits %d, want one each", events, audits)
	}
}
