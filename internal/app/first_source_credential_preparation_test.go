package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/project"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type firstSourcePreparationServiceFixture struct {
	*firstSourceAuthorityFixture
	service  firstSourceCredentialPreparationService
	services *credentialmodule.Services
	request  firstSourceCredentialPreparationRequest
	probe    *testCredentialAnalyticsProbe
	pool     *pgxpool.Pool
}

func newFirstSourcePreparationServiceFixture(t *testing.T) *firstSourcePreparationServiceFixture {
	t.Helper()
	f := newFirstSourceAuthorityFixture(t)
	// The native unpublished fence owns a separate connection. Restrict the
	// authority/credential pool to one to catch nested pool use in callbacks;
	// this proves the callback discipline, not a total single-connection pool.
	config := f.pool.Config().Copy()
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	authority := f.authority
	authority.pool = pool
	scope := firstSourceServiceScope(f)
	scope.authority = authority
	bindings := f.authority.bindings.(credentialConnectionBindingLookup)
	probe := &testCredentialAnalyticsProbe{policy: "production-first-source-test-policy"}
	keyring := filepath.Join(t.TempDir(), "keyring.json")
	document := fmt.Sprintf(`{"format":"credential-keyring-v1","deployment_id":%q,"active_write_key_id":"first-source-key","keys":[{"key_id":"first-source-key","key_base64":%q,"state":"active_write"}]}`, f.resource.TargetID, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	require.NoError(t, os.WriteFile(keyring, []byte(document), 0600))
	services, err := credentialmodule.Build(t.Context(), credentialmodule.Config{Pool: pool, Audit: firstSourceAuthorityAudit{}.RecordAuditEvent, KeyringPath: keyring,
		InstanceID: f.resource.TargetID, Environment: f.resource.Environment, CustomerOwner: platformbootstrap.New(f.pool), Bindings: newCredentialTargetBindingReader(bindings),
		CurrentProject: scope.CurrentProject, AuthorizeConnection: scope.AuthorizeConnection,
		ValidationProbe: newCredentialValidationProbe(bindings, probe), RecheckCredential: accessmodule.CredentialAuthorityRechecker(f.repository, f.repository)})
	require.NoError(t, err)
	require.NotNil(t, services)
	metadata, err := services.Drafts.SaveDraft(f.ctx, f.actor, f.resource, map[string]string{"password": "saved-first-source-test-value"})
	require.NoError(t, err)
	receipt, err := services.Validation.ValidateDraft(f.ctx, f.actor, f.resource, metadata.Binding.VersionID, 1)
	require.NoError(t, err)
	claim, err := platformbootstrap.New(f.pool).GetProjectClaim(t.Context())
	require.NoError(t, err)
	retained := retainFirstSourcePreparationFixture(t, f.resource.ProjectID, claim.ClaimedBy)
	journal, err := credentialmodule.NewFirstSourcePreparationMutations(pool, firstSourceAuthorityAudit{}.RecordAuditEvent)
	require.NoError(t, err)
	service := firstSourceCredentialPreparationService{authority: authority, receipts: services.ActivationRepository(), preparations: journal, targets: deploymentpostgres.New(f.pool), probePolicy: probe.CredentialProbePolicyIdentity,
		retainedSource: func(ctx context.Context, projectID, ownerID, digest, attestation string) (firstSourceRetainedSource, error) {
			source, err := retained.reader.SnapshotAttestation(ctx, project.CandidateSourceScope{ProjectID: projectgraph.ResourceID(projectID), OwnerID: ownerID}, digest, attestation)
			return firstSourceRetainedSource{ProjectID: source.ProjectID.String(), SourceDigest: source.ArtifactDigest, SourceAttestationDigest: source.SourceAttestationDigest}, err
		}}
	return &firstSourcePreparationServiceFixture{firstSourceAuthorityFixture: f, service: service, services: services, pool: pool, probe: probe,
		request: firstSourceCredentialPreparationRequest{PreparationID: uuid.NewString(), VersionID: metadata.Binding.VersionID, ReceiptID: receipt.ReceiptID,
			PublisherID: claim.ClaimedBy, SourceOwnerID: claim.ClaimedBy, SourceDigest: retained.source.ArtifactDigest, SourceAttestationDigest: retained.source.SourceAttestationDigest, PlanIdempotencyKey: "first-source-explicit-plan", ExpectedTargetRevision: 1}}
}

type firstSourceRetainedFixture struct {
	reader project.CandidateSourceAttestationReader
	source project.CandidateSourceSnapshot
}

func retainFirstSourcePreparationFixture(t *testing.T, projectID, ownerID string) firstSourceRetainedFixture {
	t.Helper()
	snapshot, err := (projectdevloop.FilesystemBuilder{SourceRoot: filepath.Join("..", "..", "examples", "dbt-warehouse-boundary", "leapview"), ProjectID: projectgraph.ResourceID(projectID)}).Build(t.Context())
	require.NoError(t, err)
	synchronizer, err := projectmodule.NewCandidateSourceSynchronizer(t.TempDir())
	require.NoError(t, err)
	scope := project.CandidateSourceScope{ProjectID: snapshot.ProjectID, OwnerID: ownerID}
	request := project.CandidateSynchronizationRequest{ArtifactDigest: snapshot.Digest, IdempotencyKey: "first-source-retention"}
	byDigest := make(map[string]projectdevloop.Artifact)
	for _, artifact := range snapshot.Artifacts {
		request.Artifacts = append(request.Artifacts, project.CandidateSourceArtifact{Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes})
		byDigest[artifact.Digest] = artifact
	}
	plan, err := synchronizer.Plan(t.Context(), scope, request)
	require.NoError(t, err)
	for _, identity := range plan.MissingDigests {
		require.NoError(t, synchronizer.Upload(t.Context(), scope, plan.PlanID, identity, bytes.NewReader(byDigest[identity].Content)))
	}
	request.PlanID = plan.PlanID
	source, err := synchronizer.Commit(t.Context(), scope, request)
	require.NoError(t, err)
	return firstSourceRetainedFixture{reader: synchronizer.(project.CandidateSourceAttestationReader), source: source}
}

func TestFirstSourceCredentialPreparationPersistsExactIntentAndRetry(t *testing.T) {
	f := newFirstSourcePreparationServiceFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	result, err := f.service.Prepare(ctx, f.actor, f.resource, f.request)
	require.NoError(t, err)
	require.Equal(t, f.request.PreparationID, result.PreparationID)
	require.NotEmpty(t, result.IntentDigest)
	require.NotEmpty(t, result.PlanRequestDigest)
	require.False(t, result.CreatedAt.IsZero())
	retry, err := f.service.Prepare(ctx, f.actor, f.resource, f.request)
	require.NoError(t, err)
	require.Equal(t, result, retry)
	stored, err := f.service.preparations.Preparation(t.Context(), f.resource.TargetID, result.PreparationID)
	require.NoError(t, err)
	require.Equal(t, f.request.PublisherID, stored.Intent.PublisherID)
	require.NotEqual(t, f.actor, stored.Intent.PublisherID, "operator never becomes publisher implicitly")
	require.Equal(t, f.request.SourceOwnerID, stored.Intent.SourceOwnerID)
	require.Equal(t, f.request.PlanIdempotencyKey, stored.Intent.PlanIdempotencyKey)
	require.Equal(t, f.request.VersionID, stored.Intent.Receipt.Binding.VersionID)
	expectedDigest, err := deploymentmodule.NativeDeliveryPlanRequestDigest(deploymentmodule.NativeDeliveryPlanRequest{ProjectID: projectgraph.ResourceID(f.resource.ProjectID), TargetID: f.resource.TargetID, Environment: f.resource.Environment, PrincipalID: f.request.PublisherID, SourceOwnerID: f.request.SourceOwnerID, Operation: "code_change", SourceDigest: f.request.SourceDigest, SourceAttestationDigest: f.request.SourceAttestationDigest, IdempotencyKey: f.request.PlanIdempotencyKey, FirstSourcePreparationID: f.request.PreparationID})
	require.NoError(t, err)
	require.Equal(t, expectedDigest, result.PlanRequestDigest)
	changed := f.request
	changed.PublisherID = f.actor
	_, err = f.service.Prepare(ctx, f.actor, f.resource, changed)
	require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
	changed = f.request
	changed.SourceOwnerID = uuid.NewString()
	_, err = f.service.Prepare(ctx, f.actor, f.resource, changed)
	require.ErrorIs(t, err, credentialmodule.ErrValidationConflict, "explicit source owner is immutable even when a retained reader shares project bytes")
	require.Equal(t, 1, f.probe.calls, "preparation never probes while authority locks are held")
}

type firstSourceReceiptReaderFunc func(context.Context, string, string) (credentialmodule.ValidationReceipt, error)

func (r firstSourceReceiptReaderFunc) ReadValidationReceipt(ctx context.Context, target, id string) (credentialmodule.ValidationReceipt, error) {
	return r(ctx, target, id)
}

type firstSourceTargetReaderFunc func(context.Context, pgx.Tx, string) (deploymentpostgres.DeliveryTarget, error)

func (r firstSourceTargetReaderFunc) TargetTx(ctx context.Context, tx pgx.Tx, target string) (deploymentpostgres.DeliveryTarget, error) {
	return r(ctx, tx, target)
}

func TestFirstSourceCredentialPreparationRejectsUnprovenIntent(t *testing.T) {
	f := newFirstSourcePreparationServiceFixture(t)
	for _, scenario := range []string{"foreign-version", "foreign-actor", "foreign-resource", "missing-key", "wrong-initial-revision", "foreign-attestation", "api-token", "background", "retained-read-error", "retained-mismatch", "probe-policy-drift", "receipt-read-tampering", "target-read-error", "cancel-after-retained"} {
		t.Run(scenario, func(t *testing.T) {
			service, request, resource, actor, ctx := f.service, f.request, f.resource, f.actor, f.ctx
			switch scenario {
			case "foreign-version":
				request.VersionID = uuid.NewString()
			case "foreign-actor":
				actor = f.request.PublisherID
			case "foreign-resource":
				resource.ResourceID = "connection:other"
			case "missing-key":
				request.PlanIdempotencyKey = ""
			case "wrong-initial-revision":
				request.ExpectedTargetRevision = 2
			case "foreign-attestation":
				request.SourceAttestationDigest = "sha256:" + fmt.Sprintf("%064d", 3)
			case "api-token":
				ctx = accessmodule.WithAPICredential(ctx, access.APICredential{Principal: access.Principal{ID: actor}})
			case "background":
				ctx = t.Context()
			case "retained-read-error":
				service.retainedSource = func(context.Context, string, string, string, string) (firstSourceRetainedSource, error) {
					return firstSourceRetainedSource{}, errors.New("retained source unavailable")
				}
			case "retained-mismatch":
				service.retainedSource = func(context.Context, string, string, string, string) (firstSourceRetainedSource, error) {
					return firstSourceRetainedSource{ProjectID: resource.ProjectID, SourceDigest: request.SourceDigest, SourceAttestationDigest: "sha256:" + fmt.Sprintf("%064d", 3)}, nil
				}
			case "probe-policy-drift":
				service.probePolicy = func() string { return "different-production-probe-policy" }
			case "target-read-error":
				service.targets = firstSourceTargetReaderFunc(func(context.Context, pgx.Tx, string) (deploymentpostgres.DeliveryTarget, error) {
					return deploymentpostgres.DeliveryTarget{}, errors.New("durable target read failed")
				})
			case "cancel-after-retained":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				read := service.retainedSource
				service.retainedSource = func(ctx context.Context, project, owner, digest, attestation string) (firstSourceRetainedSource, error) {
					source, err := read(ctx, project, owner, digest, attestation)
					cancel()
					return source, err
				}
			case "receipt-read-tampering":
				service.receipts = firstSourceReceiptReaderFunc(func(ctx context.Context, target, id string) (credentialmodule.ValidationReceipt, error) {
					receipt, err := f.service.receipts.ReadValidationReceipt(ctx, target, id)
					receipt.ValidatedAt = receipt.ValidatedAt.Add(time.Microsecond)
					receipt.ExpiresAt = receipt.ExpiresAt.Add(time.Microsecond)
					return receipt, err
				})
			}
			result, err := service.Prepare(ctx, actor, resource, request)
			require.Error(t, err)
			require.Empty(t, result.PreparationID)
			var count int
			require.NoError(t, f.pool.QueryRow(t.Context(), "SELECT count(*) FROM credential.activation_request WHERE operation_id=$1", request.PreparationID).Scan(&count))
			require.Zero(t, count, "failed proof must not reserve any version or receipt")
		})
	}
}

func TestFirstSourceCredentialPreparationChecksExistingUnpublishedRevision(t *testing.T) {
	f := newFirstSourcePreparationServiceFixture(t)
	_, err := deploymentpostgres.New(f.firstSourceAuthorityFixture.pool).CreateTarget(t.Context(), deploymentpostgres.TargetInput{TargetID: f.resource.TargetID, ProjectID: f.resource.ProjectID, Environment: f.resource.Environment, TargetRevision: 3})
	require.NoError(t, err)
	_, err = f.service.Prepare(f.ctx, f.actor, f.resource, f.request)
	require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
	f.request.ExpectedTargetRevision = 3
	_, err = f.service.Prepare(f.ctx, f.actor, f.resource, f.request)
	require.NoError(t, err)
}

func TestFirstSourceCredentialPreparationRechecksSessionAfterExternalReads(t *testing.T) {
	f := newFirstSourcePreparationServiceFixture(t)
	read := f.service.retainedSource
	f.service.retainedSource = func(ctx context.Context, project, owner, digest, attestation string) (firstSourceRetainedSource, error) {
		retained, err := read(ctx, project, owner, digest, attestation)
		require.NoError(t, f.repository.DeleteSession(t.Context(), f.session))
		return retained, err
	}
	_, err := f.service.Prepare(f.ctx, f.actor, f.resource, f.request)
	require.Error(t, err)
	_, err = f.service.preparations.Preparation(t.Context(), f.resource.TargetID, f.request.PreparationID)
	require.ErrorIs(t, err, credentialmodule.ErrValidationNotFound)
}

func TestFirstSourceCredentialPreparationChecksReceiptFreshnessInsideTransaction(t *testing.T) {
	f := newFirstSourcePreparationServiceFixture(t)
	receipt, err := f.service.receipts.ReadValidationReceipt(t.Context(), f.resource.TargetID, f.request.ReceiptID)
	require.NoError(t, err)
	receipt.ReceiptID = uuid.NewString()
	receipt.ExpiresAt = time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	receipt.ValidatedAt = receipt.ExpiresAt.Add(-5 * time.Minute)
	audit, err := receipt.AuditIntent()
	require.NoError(t, err)
	require.NoError(t, f.services.ActivationRepository().SaveValidation(t.Context(), receipt, audit))
	f.request.ReceiptID = receipt.ReceiptID
	read := f.service.retainedSource
	f.service.retainedSource = func(ctx context.Context, project, owner, digest, attestation string) (firstSourceRetainedSource, error) {
		retained, err := read(ctx, project, owner, digest, attestation)
		time.Sleep(max(0, time.Until(receipt.ExpiresAt.Add(time.Millisecond))))
		return retained, err
	}
	_, err = f.service.Prepare(f.ctx, f.actor, f.resource, f.request)
	require.Error(t, err, "receipt expired after preload must fail reservation under the live fence")
	_, err = f.service.preparations.Preparation(t.Context(), f.resource.TargetID, f.request.PreparationID)
	require.ErrorIs(t, err, credentialmodule.ErrValidationNotFound)
}

func TestFirstSourceCredentialPreparationAuditFailureRollsBackAllMutation(t *testing.T) {
	f := newFirstSourcePreparationServiceFixture(t)
	_, err := f.pool.Exec(t.Context(), "CREATE TABLE public.first_source_rollback_marker(value integer)")
	require.NoError(t, err)
	failure := errors.New("first-source preparation audit failed")
	f.service.preparations, err = credentialmodule.NewFirstSourcePreparationMutations(f.pool, func(ctx context.Context, tx pgx.Tx, intent access.AuditIntent) error {
		if intent.Action == "credential.first_source.prepared" {
			_, err := tx.Exec(ctx, "INSERT INTO public.first_source_rollback_marker(value) VALUES(1)")
			if err != nil {
				return err
			}
			return failure
		}
		return firstSourceAuthorityAudit{}.RecordAuditEvent(ctx, tx, intent)
	})
	require.NoError(t, err)
	_, err = f.service.Prepare(f.ctx, f.actor, f.resource, f.request)
	require.ErrorIs(t, err, failure)
	var count int
	require.NoError(t, f.pool.QueryRow(t.Context(), "SELECT count(*) FROM public.first_source_rollback_marker").Scan(&count))
	require.Zero(t, count)
	require.NoError(t, f.pool.QueryRow(t.Context(), "SELECT count(*) FROM credential.activation_request WHERE operation_id=$1", f.request.PreparationID).Scan(&count))
	require.Zero(t, count)
	_, err = f.service.preparations.Preparation(t.Context(), f.resource.TargetID, f.request.PreparationID)
	require.ErrorIs(t, err, credentialmodule.ErrValidationNotFound)
}

func TestFirstSourceCredentialPreparationRenewKeepsOriginalIntentAndCannotReviveTerminal(t *testing.T) {
	f := newFirstSourcePreparationServiceFixture(t)
	first, err := f.service.Prepare(f.ctx, f.actor, f.resource, f.request)
	require.NoError(t, err)
	fresh, err := f.services.Validation.ValidateDraft(f.ctx, f.actor, f.resource, f.request.VersionID, 1)
	require.NoError(t, err)
	for range 2 {
		renewed, err := f.service.Renew(f.ctx, f.actor, f.resource, f.request.PreparationID, fresh.ReceiptID)
		require.NoError(t, err)
		require.Equal(t, first, renewed)
	}
	stored, err := f.service.preparations.Preparation(t.Context(), f.resource.TargetID, f.request.PreparationID)
	require.NoError(t, err)
	require.Equal(t, f.request.ReceiptID, stored.Intent.Receipt.ReceiptID, "renewal appends evidence without rewriting immutable intent")
	second, err := f.services.Drafts.SaveDraft(f.ctx, f.actor, f.resource, map[string]string{"password": "different-saved-first-source-test-value"})
	require.NoError(t, err)
	foreign, err := f.services.Validation.ValidateDraft(f.ctx, f.actor, f.resource, second.Binding.VersionID, 1)
	require.NoError(t, err)
	_, err = f.service.Renew(f.ctx, f.actor, f.resource, f.request.PreparationID, foreign.ReceiptID)
	require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
	row, err := f.services.ActivationRepository().GetActivationRequest(t.Context(), f.resource.TargetID, f.request.PreparationID)
	require.NoError(t, err)
	err = f.service.authority.WithAuthorization(f.ctx, f.actor, f.resource, access.ActionConnectionManage, func(ctx context.Context, tx pgx.Tx, admission credentialmodule.FirstSourceAdmission) error {
		aborted := row
		aborted.State = "aborted"
		_, err := f.services.ActivationRepository().TransitionActivationRequestTx(ctx, tx, row, aborted, f.actor, "credential.activation.aborted", func(ctx context.Context, tx pgx.Tx) error {
			return f.service.checkIntentTx(ctx, tx, admission, stored.Intent)
		})
		return err
	})
	require.NoError(t, err)
	_, err = f.service.Renew(f.ctx, f.actor, f.resource, f.request.PreparationID, fresh.ReceiptID)
	require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
	_, err = f.service.Prepare(f.ctx, f.actor, f.resource, f.request)
	require.ErrorIs(t, err, credentialmodule.ErrValidationConflict)
}
