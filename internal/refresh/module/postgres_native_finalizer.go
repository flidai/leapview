package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/google/uuid"
)

const defaultNativeRefreshLeaseTTL = 5 * time.Minute

// PostgresNativeRefreshFinalizerAdapter composes the native delivery
// publication and activation authority with a canonical refresh completion.
// Both repositories are deliberately used through the transaction supplied
// by CompleteCanonicalRefresh; this adapter never begins, commits, or rolls
// back a transaction.
//
// Refresh and Deployment must point at the same PostgreSQL database. A
// resolver-derived target (or an explicitly single-target binding) is never
// accepted from browser or job payload input. The publication, lease, and
// activation-correlation identities are deterministic from refresh evidence,
// making a lost acknowledgement an exact replay rather than a second target
// advance.
type PostgresNativeRefreshFinalizerAdapter struct {
	Refresh    *refreshpostgres.Repository
	Deployment *deploymentpostgres.Repository
	// TargetResolver is the preferred multi-scope seam. It must derive a
	// target from durable job scope through tx; no browser/payload target is
	// accepted. TargetID remains an explicit single-target binding for
	// deployments whose process serves exactly one target.
	TargetResolver PostgresNativeRefreshTargetResolver
	TargetID       string
	LeaseTTL       time.Duration
}

// NativeRefreshFinalizer is a concise alias for callers that do not need the
// adapter suffix.
type NativeRefreshFinalizer = PostgresNativeRefreshFinalizerAdapter

// PostgresNativeRefreshTargetResolver resolves the deployment target from
// durable refresh job scope while the caller-owned transaction is open.
type PostgresNativeRefreshTargetResolver interface {
	ResolveNativeRefreshTargetTx(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) (string, error)
}

type PostgresNativeRefreshTargetResolverFunc func(context.Context, refreshpostgres.Tx, refreshrun.JobRecord) (string, error)

func (f PostgresNativeRefreshTargetResolverFunc) ResolveNativeRefreshTargetTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord) (string, error) {
	if f == nil {
		return "", errors.New("native refresh target resolver is unavailable")
	}
	return f(ctx, tx, job)
}

// NewPostgresNativeRefreshFinalizer constructs a target-bound native refresh
// finalizer for a process that serves exactly one deployment target. Callers
// with multi-scope routing should use the resolver-backed constructor below.
func NewPostgresNativeRefreshFinalizer(refresh *refreshpostgres.Repository, deployment *deploymentpostgres.Repository, targetID string) (*PostgresNativeRefreshFinalizerAdapter, error) {
	finalizer := &PostgresNativeRefreshFinalizerAdapter{Refresh: refresh, Deployment: deployment, TargetID: targetID}
	if err := finalizer.validate(); err != nil {
		return nil, err
	}
	return finalizer, nil
}

// NewNativeRefreshFinalizer is kept as a short constructor alias for module
// composition code.
func NewNativeRefreshFinalizer(refresh *refreshpostgres.Repository, deployment *deploymentpostgres.Repository, targetID string) (*PostgresNativeRefreshFinalizerAdapter, error) {
	return NewPostgresNativeRefreshFinalizer(refresh, deployment, targetID)
}

// NewPostgresNativeRefreshFinalizerWithResolver constructs the preferred
// multi-project target-resolving adapter.
func NewPostgresNativeRefreshFinalizerWithResolver(refresh *refreshpostgres.Repository, deployment *deploymentpostgres.Repository, resolver PostgresNativeRefreshTargetResolver) (*PostgresNativeRefreshFinalizerAdapter, error) {
	finalizer := &PostgresNativeRefreshFinalizerAdapter{Refresh: refresh, Deployment: deployment, TargetResolver: resolver}
	if err := finalizer.validate(); err != nil {
		return nil, err
	}
	return finalizer, nil
}

func (f *PostgresNativeRefreshFinalizerAdapter) validate() error {
	if f == nil || f.Refresh == nil {
		return errors.New("refresh PostgreSQL repository is required")
	}
	if f.Deployment == nil {
		return errors.New("deployment PostgreSQL repository is required")
	}
	if f.TargetResolver == nil && f.TargetID == "" {
		return errors.New("native refresh target resolver is required")
	}
	if f.TargetResolver == nil && (f.TargetID != strings.TrimSpace(f.TargetID) || len(f.TargetID) > 255) {
		return errors.New("native refresh target id must be canonical")
	}
	if f.LeaseTTL == 0 {
		f.LeaseTTL = defaultNativeRefreshLeaseTTL
	}
	if f.LeaseTTL <= 0 || f.LeaseTTL > 24*time.Hour {
		return errors.New("native refresh lease TTL is outside the allowed bound")
	}
	return nil
}

// FinalizeCanonicalRefreshTx creates or exactly replays a native publication,
// fences a target lease, and advances the native target pointer with CAS. All
// operations stay in tx so a later refresh data-version/run/job failure rolls
// the native writes back as well.
func (f *PostgresNativeRefreshFinalizerAdapter) FinalizeCanonicalRefreshTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult, evidence refreshpostgres.PublicationInput) error {
	if err := f.validate(); err != nil {
		return err
	}
	if tx == nil {
		return errors.New("native refresh transaction is required")
	}
	if err := job.Validate(); err != nil {
		return fmt.Errorf("native refresh job: %w", err)
	}
	if job.LeaseOwner == "" || job.LeaseRevision <= 0 || result.PlanID == "" || result.NativeGenerationID == "" || result.SnapshotID <= 0 {
		return refreshrun.ErrLeaseLost
	}
	if result.ServingStateID != result.NativeGenerationID {
		return fmt.Errorf("%w: serving-state and native generation identities differ", refreshpostgres.ErrConflict)
	}
	if evidence.RunID != job.RunID || evidence.BaseGenerationID != job.Identity.GenerationID || evidence.ResultGenerationID != result.NativeGenerationID || evidence.ExpectedTargetRevision != job.TargetRevision || evidence.ResultTargetRevision != evidence.ExpectedTargetRevision+1 {
		return fmt.Errorf("%w: native refresh publication evidence differs from job", refreshpostgres.ErrConflict)
	}

	targetID, err := f.resolveTarget(ctx, tx, job)
	if err != nil {
		return err
	}
	publicationID, leaseID, correlationID, requestDigest := nativeRefreshIdentities(job, result, evidence)
	// This first check intentionally locks the refresh run row before any
	// native row is created. A worker takeover cannot race publication admission.
	// A committed native row is durable outcome evidence, however, and may be
	// replayed after the refresh worker lease has expired (the lost-ack path).
	if err := f.requireLiveRun(ctx, tx, job); err != nil {
		committed, publicationErr := f.Deployment.PublicationTx(ctx, tx, publicationID)
		if publicationErr != nil || committed.State != "committed" {
			return err
		}
	}

	generation, err := f.Deployment.GenerationTx(ctx, tx, result.NativeGenerationID)
	if err != nil {
		return fmt.Errorf("load native refresh generation: %w", err)
	}
	target, err := f.Deployment.TargetForShareTx(ctx, tx, targetID)
	if err != nil {
		return fmt.Errorf("load native refresh target: %w", err)
	}
	if target.ProjectID != job.Identity.ProjectID.String() || target.Environment != job.Identity.Environment || generation.TargetID != targetID || generation.GenerationID != result.NativeGenerationID || generation.PlanID != result.PlanID {
		return fmt.Errorf("%w: native refresh generation or target scope differs", refreshpostgres.ErrConflict)
	}
	candidate, err := f.Deployment.CandidateTx(ctx, tx, generation.CandidateID)
	if err != nil {
		return fmt.Errorf("load native refresh candidate: %w", err)
	}
	if candidate.CandidateID != generation.CandidateID || candidate.TargetID != targetID || candidate.PlanID != generation.PlanID || candidate.SnapshotSealID != generation.SnapshotSealID || candidate.ArtifactDigest != generation.ServingArtifactDigest || candidate.Status != "qualified" && candidate.Status != "ready" && candidate.Status != "admitted" {
		return fmt.Errorf("%w: native refresh candidate evidence differs", refreshpostgres.ErrConflict)
	}
	plan, err := f.Deployment.PlanTx(ctx, tx, generation.PlanID)
	if err != nil {
		return fmt.Errorf("load native refresh delivery plan: %w", err)
	}
	if plan.TargetID != targetID || plan.PlanDigest != generation.PlanDigest || plan.ArtifactDigest != generation.ServingArtifactDigest {
		return fmt.Errorf("%w: native refresh delivery plan evidence differs", refreshpostgres.ErrConflict)
	}
	// The native generation's PlanDigest identifies the delivery plan, while
	// the refresh job carries the embedded pipeline-plan digest. The verifier
	// binds those two immutable documents; this adapter only needs to ensure
	// the serving artifact hand-off agrees with the refresh job.
	if job.PipelinePlan == nil || generation.ServingArtifactDigest != job.PipelinePlan.ArtifactDigest {
		return fmt.Errorf("%w: native refresh generation plan evidence differs", refreshpostgres.ErrConflict)
	}
	seal, err := f.Deployment.SnapshotSealTx(ctx, tx, generation.SnapshotSealID)
	if err != nil {
		return fmt.Errorf("load native refresh snapshot seal: %w", err)
	}
	if seal.SealID != generation.SnapshotSealID || seal.CandidateID != generation.CandidateID || seal.PlanDigest != generation.PlanDigest || seal.ArtifactRoot != generation.ArtifactRoot || seal.ArtifactRootDigest != generation.ArtifactRootDigest || seal.CompiledGraphDigest != generation.CompiledGraphDigest || seal.CompiledConfigDigest != generation.CompiledConfigDigest || seal.SecurityDomainFingerprint != generation.SecurityDomainFingerprint || seal.ServingArtifactDigest != generation.ServingArtifactDigest || seal.DuckLakeSnapshotID != result.SnapshotID || seal.PhysicalPoolID != evidence.PhysicalPoolID || seal.CatalogID != evidence.CatalogID {
		return fmt.Errorf("%w: native refresh snapshot seal evidence differs", refreshpostgres.ErrConflict)
	}

	publication, err := f.Deployment.CreatePublicationTx(ctx, tx, deploymentpostgres.PublicationInput{
		PublicationID: publicationID, TargetID: targetID, GenerationID: result.NativeGenerationID,
		ExpectedBaseGenerationID: job.Identity.GenerationID, CandidateID: generation.CandidateID,
		SnapshotSealID: generation.SnapshotSealID, ExpectedTargetRevision: evidence.ExpectedTargetRevision,
		ActorID: job.PrincipalID, RequestDigest: requestDigest,
	})
	if err != nil {
		return fmt.Errorf("create native refresh publication: %w", err)
	}
	if publication.PublicationID != publicationID || publication.TargetID != targetID || publication.GenerationID != result.NativeGenerationID || publication.ExpectedBaseGenerationID != job.Identity.GenerationID || publication.CandidateID != generation.CandidateID || publication.SnapshotSealID != generation.SnapshotSealID || publication.ExpectedTargetRevision != evidence.ExpectedTargetRevision || publication.ActorID != job.PrincipalID || publication.RequestDigest != requestDigest {
		return fmt.Errorf("%w: native refresh publication identity differs", refreshpostgres.ErrConflict)
	}

	// A committed publication is durable native outcome evidence. Activation's
	// replay path verifies pointer/event/audit identity and deliberately does
	// not require a still-live lease, which is the lost-ack recovery boundary.
	activation := deploymentpostgres.ActivationInput{
		PublicationID: publicationID, TargetID: targetID, GenerationID: result.NativeGenerationID,
		ExpectedTargetRevision: evidence.ExpectedTargetRevision, RequestDigest: requestDigest,
		ActorID: job.PrincipalID, CorrelationID: correlationID,
	}
	if publication.State != "committed" {
		if publication.State != "pending" {
			return fmt.Errorf("%w: native refresh publication state=%s", refreshpostgres.ErrConflict, publication.State)
		}
		lease, leaseErr := f.leaseForPublication(ctx, tx, leaseID, targetID, job)
		if leaseErr != nil {
			return leaseErr
		}
		activation.LeaseID, activation.OwnerID, activation.FencingEpoch = lease.LeaseID, lease.OwnerID, lease.FencingEpoch
		// Lease acquisition can itself advance the target fence. Recheck the
		// refresh worker lease immediately before native target CAS.
		if err := f.requireLiveRun(ctx, tx, job); err != nil {
			return err
		}
	}
	if _, err := f.Deployment.ActivateTx(ctx, tx, activation); err != nil {
		return fmt.Errorf("activate native refresh publication: %w", err)
	}
	return nil
}

func (f *PostgresNativeRefreshFinalizerAdapter) requireLiveRun(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord) error {
	ok, err := f.Refresh.RunMayPublishTx(ctx, tx, job.RunID, job.LeaseOwner, job.LeaseRevision)
	if err != nil {
		return fmt.Errorf("check native refresh run fence: %w", err)
	}
	if !ok {
		return refreshpostgres.ErrStaleFence
	}
	return nil
}

func (f *PostgresNativeRefreshFinalizerAdapter) resolveTarget(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord) (string, error) {
	targetID := f.TargetID
	if f.TargetResolver != nil {
		resolved, err := f.TargetResolver.ResolveNativeRefreshTargetTx(ctx, tx, job)
		if err != nil {
			return "", fmt.Errorf("resolve native refresh target: %w", err)
		}
		targetID = resolved
	}
	if targetID == "" || targetID != strings.TrimSpace(targetID) || len(targetID) > 255 {
		return "", errors.New("native refresh target resolver returned a non-canonical target")
	}
	return targetID, nil
}

func (f *PostgresNativeRefreshFinalizerAdapter) leaseForPublication(ctx context.Context, tx refreshpostgres.Tx, leaseID, targetID string, job refreshrun.JobRecord) (deploymentpostgres.DeliveryLease, error) {
	now, err := f.Deployment.DatabaseNowTx(ctx, tx)
	if err != nil {
		return deploymentpostgres.DeliveryLease{}, fmt.Errorf("read native refresh authority time: %w", err)
	}
	if lease, err := f.Deployment.LeaseTx(ctx, tx, leaseID); err == nil {
		if lease.TargetID != targetID || lease.OwnerID != job.LeaseOwner || lease.State != "active" || !lease.ExpiresAt.After(now) {
			return deploymentpostgres.DeliveryLease{}, deploymentpostgres.ErrStaleFence
		}
		return lease, nil
	} else if !errors.Is(err, deploymentpostgres.ErrNotFound) {
		return deploymentpostgres.DeliveryLease{}, fmt.Errorf("read native refresh lease: %w", err)
	}
	lease, err := f.Deployment.AcquireLeaseTx(ctx, tx, deploymentpostgres.LeaseInput{
		LeaseID: leaseID, TargetID: targetID, OwnerID: job.LeaseOwner,
		ExpiresAt: now.Add(f.LeaseTTL),
	})
	if err != nil {
		return deploymentpostgres.DeliveryLease{}, fmt.Errorf("acquire native refresh lease: %w", err)
	}
	return lease, nil
}

func nativeRefreshIdentities(job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult, evidence refreshpostgres.PublicationInput) (publicationID, leaseID, correlationID, requestDigest string) {
	seed := strings.Join([]string{
		job.RunID, job.Identity.ProjectID.String(), job.Identity.Environment, job.Identity.GenerationID,
		result.PlanID, result.NativeGenerationID, fmt.Sprintf("%d", result.SnapshotID),
		fmt.Sprintf("%d", evidence.ExpectedTargetRevision), evidence.PhysicalPoolID, evidence.CatalogID,
	}, "\x00")
	publicationID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/native-refresh/publication\x00"+seed)).String()
	leaseID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/native-refresh/lease\x00"+seed)).String()
	correlationID = uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/native-refresh/correlation\x00"+seed)).String()
	requestDigest = deployment.CanonicalDeliveryDigest([]byte("leapview/native-refresh/request\x00" + seed))
	return publicationID, leaseID, correlationID, requestDigest
}

var _ PostgresNativeRefreshFinalizer = (*PostgresNativeRefreshFinalizerAdapter)(nil)
