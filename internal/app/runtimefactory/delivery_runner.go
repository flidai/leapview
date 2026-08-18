package runtimefactory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/flidai/leapview/internal/analytics/candidatecatalog"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/gcadapter"
	"github.com/flidai/leapview/internal/deployment"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	"github.com/flidai/leapview/internal/release"
)

// NewCatalogObjectStore routes catalog seals through the same admitted pool
// location and namespace used by GC/runtime readers. It never accepts a
// caller-supplied bucket or prefix when the pool contract disagrees.
func NewCatalogObjectStore(ctx context.Context, contract *ducklake.PoolContract, config gcadapter.S3Config) (catalogseal.ObjectStore, error) {
	if contract == nil || contract.Validate() != nil {
		return nil, fmt.Errorf("physical-pool admission is required")
	}
	switch strings.ToLower(strings.TrimSpace(contract.Tuple.StorageImplementation)) {
	case "local", "filesystem":
		path, err := contract.Pool.DataPath()
		if err != nil {
			return nil, err
		}
		return catalogseal.NewLocalObjectStore(path)
	case "s3":
		parsed, err := url.Parse(contract.Pool.Identity.StorageLocation)
		if err != nil || parsed.Scheme != "s3" || parsed.Host == "" {
			return nil, fmt.Errorf("physical-pool S3 location is invalid")
		}
		options := []func(*awsconfig.LoadOptions) error{}
		if config.Region != "" {
			options = append(options, awsconfig.WithRegion(config.Region))
		}
		if config.AccessKeyID != "" {
			options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(config.AccessKeyID, config.SecretAccessKey, config.SessionToken)))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
		if err != nil {
			return nil, err
		}
		client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
			o.UsePathStyle = config.PathStyle
			if config.Endpoint != "" {
				o.BaseEndpoint = &config.Endpoint
			}
		})
		prefix := strings.Trim(strings.Trim(parsed.Path, "/")+"/"+strings.Trim(contract.Pool.Identity.StorageNamespace, "/"), "/")
		return catalogseal.NewS3ObjectStore(client, parsed.Host, prefix)
	default:
		return nil, fmt.Errorf("unsupported physical-pool storage implementation %q", contract.Tuple.StorageImplementation)
	}
}

// CandidateCatalogRunnerConfig binds the admitted physical pool and the
// target-owned sealing adapters. Materialize is the existing analytics
// compiler/materialization callback; it receives only a scoped,
// lease-checked WorkingCatalog and must return before detach.
type CandidateCatalogRunnerConfig struct {
	PoolContract        *ducklake.PoolContract
	StagingRoot         string
	CredentialBootstrap ducklake.CredentialBootstrap
	Base                func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error)
	Materialize         func(context.Context, *candidatecatalog.WorkingCatalog, deployment.DeliveryBuildInput, release.CandidateArtifactSet, string) error
	// Connections is acquired only around candidate catalog construction. The
	// lease registers the candidate resolver used by governed materialization;
	// it is always closed before normalization/sealing begins.
	Connections          deployment.CandidateConnectionLeaser
	Qualification        candidatecatalog.QualificationRequest
	QualificationFactory func(release.CandidateArtifactSet) candidatecatalog.QualificationRequest
	ReviewerAuthorize    func(context.Context, string) error
	ObjectStore          catalogseal.ObjectStore
	SealRepository       catalogseal.SealRepository
	RemoteVerifier       catalogseal.RemoteVerifier
	RequestTemplate      deployment.DeliveryBuildRequest
	VerifyLease          candidatecatalog.LeaseVerifier
}

// CandidateObjectStore adapts catalogseal's metadata-bearing object store to
// candidatecatalog's read-only artifact capability.
type CandidateObjectStore struct{ Store catalogseal.ObjectStore }

func (s CandidateObjectStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("candidate object store is unavailable")
	}
	object, err := s.Store.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	if object.Body == nil {
		return nil, fmt.Errorf("candidate object body is unavailable")
	}
	return object.Body, nil
}

// BootstrapTargetResolver permits the first plan to establish a target row in
// the repository's atomic CreatePlan transaction. Existing rows are always
// authoritative; a missing row is synthesized from the process-bound target
// and environment plus the durable project claim. The claim may be established
// after process startup (for example by the first candidate synchronization),
// so ProjectIDResolver is consulted when the startup snapshot is empty.
type BootstrapTargetResolver struct {
	Resolver          deployment.DeliveryTargetResolver
	TargetID          string
	ProjectID         string
	ProjectIDResolver func(context.Context) (string, error)
	Environment       string
}

func (r BootstrapTargetResolver) ResolveDeliveryTarget(ctx context.Context, targetID string) (deployment.DeliveryTarget, error) {
	if r.Resolver != nil {
		if target, err := r.Resolver.ResolveDeliveryTarget(ctx, targetID); err == nil {
			return target, nil
		} else if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, deployment.ErrNotFound) {
			return deployment.DeliveryTarget{}, err
		}
	}
	if targetID != r.TargetID || strings.TrimSpace(r.Environment) == "" {
		return deployment.DeliveryTarget{}, fmt.Errorf("delivery target is unavailable")
	}
	projectID := strings.TrimSpace(r.ProjectID)
	if projectID == "" && r.ProjectIDResolver != nil {
		resolved, err := r.ProjectIDResolver(ctx)
		if err != nil {
			return deployment.DeliveryTarget{}, err
		}
		projectID = strings.TrimSpace(resolved)
	}
	if projectID == "" {
		return deployment.DeliveryTarget{}, fmt.Errorf("delivery target is unavailable")
	}
	return deployment.DeliveryTarget{TargetID: r.TargetID, ProjectID: projectID, Environment: r.Environment}, nil
}

// SQLiteWriterLeaseVerifier adapts the durable pool fence to candidatecatalog
// without exposing the deployment repository to the analytics package.
func SQLiteWriterLeaseVerifier(repository interface {
	IsCurrentWriterFence(context.Context, deploymentsqlite.WriterFence, time.Time) (bool, error)
}) candidatecatalog.LeaseVerifier {
	return func(ctx context.Context, lease candidatecatalog.WriterLease) error {
		if repository == nil {
			return fmt.Errorf("writer lease repository is unavailable")
		}
		ok, err := repository.IsCurrentWriterFence(ctx, deploymentsqlite.WriterFence{ID: lease.ID, AttemptID: lease.AttemptID, PhysicalPoolID: lease.PhysicalPoolID, Epoch: lease.Epoch, HolderID: lease.HolderID}, time.Now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("writer lease is not current")
		}
		return nil
	}
}

// BuildRequestFactory creates a real candidatecatalog phased runner. Missing
// pool admission, materialization, or seal adapters fail closed before any
// writer lease can reach physical work.
func BuildRequestFactory(config CandidateCatalogRunnerConfig) func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryBuildRequest, error) {
	return func(ctx context.Context, input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet) (deployment.DeliveryBuildRequest, error) {
		if artifacts.Generation.DataMode == release.GenerationDataReuseSnapshotLegacy {
			return deployment.DeliveryBuildRequest{}, fmt.Errorf("candidate build requires controlled rebuild for legacy data mode: %w", release.ErrLegacyReuseSnapshot)
		}
		if config.PoolContract == nil || config.PoolContract.Validate() != nil || config.Materialize == nil || config.Connections == nil || config.ObjectStore == nil || config.SealRepository == nil || config.RemoteVerifier == nil || config.VerifyLease == nil || input.Plan == nil {
			return deployment.DeliveryBuildRequest{}, fmt.Errorf("candidate delivery physical-pool admission and materialization adapters are required")
		}
		runner := &candidateCatalogRunner{config: config, input: input, artifacts: artifacts}
		request := config.RequestTemplate
		request.PlanID = input.Plan.ID
		if request.AttemptID == "" {
			request.AttemptID = "attempt-" + input.Candidate.ID
		}
		if request.WriterLeaseID == "" {
			request.WriterLeaseID = "writer-" + input.Candidate.ID
		}
		if request.SealID == "" {
			request.SealID = "seal-" + input.Candidate.ID
		}
		request.CandidateID = input.Candidate.ID
		if request.OwnerID == "" {
			request.OwnerID = input.OwnerID
		}
		// The SQLite repository replaces this placeholder with the authoritative
		// pool-fence epoch in its lease transaction. The lifecycle validates the
		// request before reaching that allocator, so the request must still carry
		// a positive initial epoch.
		if request.Epoch < 1 {
			request.Epoch = 1
		}
		request.PhysicalPoolID = config.PoolContract.Pool.ID.String()
		if err := validateReuseEvidenceCoverage(input.Plan, artifacts, input.Candidate.ID); err != nil {
			return deployment.DeliveryBuildRequest{}, err
		}
		decision, decisionOK := deliveryReuseDecision(input.Plan, input.Candidate.ID)
		retainBase := decisionOK && (decision.Reusable || decision.RetainBase)
		if !retainBase {
			// A mismatch must not inherit caller-supplied base references from a
			// retry template; without exact physical identities it is an explicit
			// fresh materialization.
			request.BaseCatalogDigest = ""
			request.BasePhysicalPoolID = ""
		}
		if retainBase {
			if config.Base == nil {
				return deployment.DeliveryBuildRequest{}, fmt.Errorf("exact sealed base resolver is required for reusable candidate")
			}
			base, baseErr := config.Base(ctx, deployment.DeliveryBuildInput{Plan: *input.Plan})
			if baseErr != nil {
				return deployment.DeliveryBuildRequest{}, baseErr
			}
			if base == nil {
				return deployment.DeliveryBuildRequest{}, fmt.Errorf("reusable candidate has no sealed base")
			}
			request.BaseCatalogDigest = base.Digest
			request.BasePhysicalPoolID = base.PhysicalPoolID
		}
		request.PhasedRunner = runner
		request.Runner = nil
		return request, nil
	}
}

// candidateCatalogRunner is scoped to one exact source/artifact input.
type candidateCatalogRunner struct {
	config    CandidateCatalogRunnerConfig
	input     deployment.DeliveryCandidateBuildInput
	artifacts release.CandidateArtifactSet
	working   *candidatecatalog.WorkingCatalog
}

// SetCandidateArtifacts installs the exact inspected/materialized artifact
// set after the durable attempt exists. It intentionally accepts any here so
// deployment can remain independent of the release package while still
// requiring a concrete CandidateArtifactSet at the boundary.
func (r *candidateCatalogRunner) SetCandidateArtifacts(value any) error {
	artifacts, ok := value.(release.CandidateArtifactSet)
	if !ok {
		return fmt.Errorf("candidate artifact set has unexpected type %T", value)
	}
	if artifacts.Generation.Identity.GenerationID == "" || artifacts.Generation.ServingArtifactID == "" || artifacts.Generation.ArtifactDigest == "" {
		return fmt.Errorf("compiled serving artifact identity is incomplete")
	}
	r.artifacts = artifacts
	return nil
}

func candidateConnectionRequirements(values []release.CandidateConnectionRequirement) []deployment.CandidateConnectionRequirement {
	result := make([]deployment.CandidateConnectionRequirement, len(values))
	for i, value := range values {
		result[i] = deployment.CandidateConnectionRequirement{ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind}
	}
	return result
}

func candidateAuthoredConnections(values []release.CandidateAuthoredConnection) []deployment.CandidateAuthoredConnection {
	result := make([]deployment.CandidateAuthoredConnection, len(values))
	for i, value := range values {
		result[i] = deployment.CandidateAuthoredConnection{ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind}
	}
	return result
}

func deliveryReuseDecision(plan *deployment.DeliveryPlan, resourceID string) (deployment.DeliveryReuseDecision, bool) {
	if plan == nil {
		return deployment.DeliveryReuseDecision{}, false
	}
	for _, decision := range plan.Evidence.Reuse {
		if decision.ResourceID == resourceID {
			return decision, true
		}
	}
	if len(plan.Evidence.Reuse) == 1 {
		if plan.Evidence.Reuse[0].ResourceID != resourceID {
			return deployment.DeliveryReuseDecision{}, false
		}
		return plan.Evidence.Reuse[0], true
	}
	if len(plan.Evidence.Reuse) > 1 {
		aggregate := deployment.DeliveryReuseDecision{ResourceID: resourceID, Reusable: true, Reason: "all unchanged relation identities are reusable"}
		for _, decision := range plan.Evidence.Reuse {
			if !decision.Reusable {
				aggregate.Reusable = false
			}
			if decision.Reusable || decision.RetainBase {
				aggregate.RetainBase = true
			}
		}
		if aggregate.RetainBase {
			if !aggregate.Reusable {
				aggregate.Reason = "retain exact base for unchanged relations and rebuild impacted relations"
			}
			return aggregate, true
		}
	}
	return deployment.DeliveryReuseDecision{}, false
}

// validateReuseEvidenceCoverage prevents a partial or misidentified reuse
// statement from retaining a sealed base. Relation-scoped evidence must cover
// exactly the current compiled relation IDs: an unknown, duplicate, or
// missing ID is a contract error. Candidate-level evidence is intentionally a
// single exact candidate ID, never a positional fallback.
func validateReuseEvidenceCoverage(plan *deployment.DeliveryPlan, artifacts release.CandidateArtifactSet, candidateID string) error {
	if plan == nil || len(plan.Evidence.Reuse) == 0 {
		return nil
	}
	decisions := plan.Evidence.Reuse
	if len(artifacts.Compiler.RelationExecution) == 0 {
		if len(decisions) != 1 || decisions[0].ResourceID != candidateID {
			return fmt.Errorf("reuse evidence must contain exactly candidate resource %q", candidateID)
		}
		if plan.Operation != "" && plan.Operation != deployment.DeliveryOperationCodeChange && (decisions[0].Reusable || decisions[0].RetainBase) {
			return fmt.Errorf("full-refresh reuse evidence must not retain base")
		}
		return nil
	}
	// Explicit restatement/binding-style full refreshes may carry one
	// candidate-level non-reuse statement. It is never permitted to retain a
	// base and is kept distinct from code-change relation evidence below.
	if len(decisions) == 1 && decisions[0].ResourceID == candidateID && plan.Operation != "" && plan.Operation != deployment.DeliveryOperationCodeChange {
		if decisions[0].Reusable || decisions[0].RetainBase {
			return fmt.Errorf("full-refresh reuse evidence must not retain base")
		}
		return nil
	}
	if len(decisions) != len(artifacts.Compiler.RelationExecution) {
		return fmt.Errorf("reuse evidence covers %d relations, want %d", len(decisions), len(artifacts.Compiler.RelationExecution))
	}
	seen := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		if _, duplicate := seen[decision.ResourceID]; duplicate {
			return fmt.Errorf("reuse evidence contains duplicate relation %q", decision.ResourceID)
		}
		seen[decision.ResourceID] = struct{}{}
		if _, expected := artifacts.Compiler.RelationExecution[decision.ResourceID]; !expected {
			return fmt.Errorf("reuse evidence contains unknown relation %q", decision.ResourceID)
		}
	}
	for resourceID := range artifacts.Compiler.RelationExecution {
		if _, covered := seen[resourceID]; !covered {
			return fmt.Errorf("reuse evidence is missing relation %q", resourceID)
		}
	}
	return nil
}

func (r *candidateCatalogRunner) Construct(ctx context.Context, buildInput deployment.DeliveryBuildInput) (any, error) {
	if r.artifacts.Generation.DataMode == release.GenerationDataReuseSnapshotLegacy {
		return nil, fmt.Errorf("candidate build requires controlled rebuild for legacy data mode: %w", release.ErrLegacyReuseSnapshot)
	}
	var base *candidatecatalog.SealedArtifact
	var err error
	if err := validateReuseEvidenceCoverage(&buildInput.Plan, r.artifacts, r.input.Candidate.ID); err != nil {
		return nil, err
	}
	reuseDecision, hasReuseDecision := deliveryReuseDecision(&buildInput.Plan, r.input.Candidate.ID)
	useRetainedBase := hasReuseDecision && (reuseDecision.Reusable || reuseDecision.RetainBase)
	if buildInput.Plan.BaseGenerationID != "" && useRetainedBase && r.config.Base == nil {
		return nil, fmt.Errorf("exact sealed base resolver is required for non-empty active base")
	}
	if useRetainedBase && r.config.Base != nil {
		base, err = r.config.Base(ctx, buildInput)
		if err != nil {
			return nil, err
		}
		if base == nil || (buildInput.Attempt.BaseCatalogDigest != "" && base.Digest != buildInput.Attempt.BaseCatalogDigest) || (buildInput.Attempt.BasePhysicalPoolID != "" && base.PhysicalPoolID != buildInput.Attempt.BasePhysicalPoolID) {
			return nil, fmt.Errorf("sealed base does not match exact build attempt identity")
		}
	}
	if (!hasReuseDecision || !reuseDecision.Reusable) && r.artifacts.Generation.DataMode == release.GenerationDataReuseBase {
		// A reuse-key mismatch is an explicit rebuild decision. Keep the
		// candidate private, but make the materializer refresh source data
		// instead of merely checking inherited relations.
		r.artifacts.Generation.DataMode = release.GenerationDataRefreshSources
	}
	if r.config.Materialize == nil {
		return nil, fmt.Errorf("candidate materialization adapter is required")
	}
	// Candidate connection leases are deliberately scoped to materialization.
	// Qualification and sealing operate solely on the detached catalog and
	// never retain target credentials or resolver registrations.
	var connections deployment.CandidateConnectionLeases
	if r.config.Connections != nil {
		connections, err = r.config.Connections.Acquire(ctx, deployment.CandidateConnectionRequest{
			CandidateID: r.input.Candidate.ID, Actor: r.input.OwnerID, TargetID: r.input.Candidate.TargetID,
			Identity:            r.artifacts.Generation.Identity,
			Requirements:        candidateConnectionRequirements(r.artifacts.Generation.Connections),
			AuthoredConnections: candidateAuthoredConnections(r.artifacts.Generation.AuthoredConnections),
		})
		if err != nil {
			return nil, fmt.Errorf("candidate materialization connections unavailable: %w", err)
		}
		if connections == nil {
			return nil, fmt.Errorf("candidate materialization connections unavailable")
		}
		defer connections.Close()
	}
	working, err := candidatecatalog.Build(ctx, candidatecatalog.Request{AttemptID: buildInput.Attempt.ID, StagingRoot: r.config.StagingRoot, PoolContract: r.config.PoolContract, CredentialBootstrap: r.config.CredentialBootstrap, VerifyLease: r.config.VerifyLease, Lease: candidatecatalog.WriterLease{ID: buildInput.Lease.ID, AttemptID: buildInput.Attempt.ID, PhysicalPoolID: buildInput.Lease.PhysicalPoolID, HolderID: buildInput.Lease.OwnerID, Epoch: buildInput.Lease.Epoch, ExpiresAt: buildInput.Lease.ExpiresAt, Status: candidatecatalog.LeaseActive}, Base: base}, func(ctx context.Context, catalog *candidatecatalog.WorkingCatalog) error {
		return r.config.Materialize(ctx, catalog, buildInput, r.artifacts, r.input.Candidate.ID)
	})
	if err != nil {
		return nil, err
	}
	r.working = working
	return working, nil
}

func (r *candidateCatalogRunner) Normalize(ctx context.Context, _ deployment.DeliveryBuildInput, value any) error {
	working, ok := value.(*candidatecatalog.WorkingCatalog)
	if !ok || working == nil {
		return fmt.Errorf("candidate working catalog is unavailable")
	}
	normalized, err := working.Normalize(ctx)
	if err != nil {
		return err
	}
	return working.RememberNormalization(normalized)
}

func (r *candidateCatalogRunner) Qualify(ctx context.Context, buildInput deployment.DeliveryBuildInput, value any) (deployment.DeliveryBuildOutput, error) {
	working, ok := value.(*candidatecatalog.WorkingCatalog)
	if !ok || working == nil {
		return deployment.DeliveryBuildOutput{}, fmt.Errorf("candidate working catalog is unavailable")
	}
	qualification := r.config.Qualification
	if r.config.QualificationFactory != nil {
		qualification = r.config.QualificationFactory(r.artifacts)
	}
	if r.config.ReviewerAuthorize != nil {
		qualification.Checks.ReviewerAuthorization = func(ctx context.Context, _ candidatecatalog.QualificationInput) error {
			return r.config.ReviewerAuthorize(ctx, r.input.OwnerID)
		}
	}
	qualified, err := candidatecatalog.NormalizeAndQualify(ctx, working, qualification)
	if err != nil {
		return deployment.DeliveryBuildOutput{}, err
	}
	compatibilityDigest, err := r.config.PoolContract.Tuple.Digest()
	if err != nil {
		_ = qualified.Remove()
		return deployment.DeliveryBuildOutput{}, err
	}
	resolved := deployment.DeliveryResolvedBuildInputs{PolicyDigest: buildInput.Plan.Governance.PolicyDigest, Inputs: make([]deployment.DeliveryResolvedDataInput, 0, len(buildInput.Plan.Execution.DataInputs))}
	managedRevisions := make(map[string]string, len(r.artifacts.Generation.ManagedDataPins))
	for _, pin := range r.artifacts.Generation.ManagedDataPins {
		managedRevisions[pin.ConnectionID] = pin.RevisionID
	}
	for _, planned := range buildInput.Plan.Execution.DataInputs {
		item := deployment.DeliveryResolvedDataInput{ID: planned.ID, Mode: planned.Mode, PlannedRevision: planned.Revision, PlannedBound: planned.Bound, Explanation: "candidate build resolved the declared input"}
		switch planned.Mode {
		case deployment.DeliveryDataPinned:
			actual := ""
			if planned.ID == "source-artifact" {
				actual, err = materializationIdentity(r.artifacts)
				if err != nil {
					return deployment.DeliveryBuildOutput{}, err
				}
			} else {
				actual = managedRevisions[planned.ID]
			}
			if actual == "" || actual != planned.Revision {
				return deployment.DeliveryBuildOutput{}, fmt.Errorf("authoritative pinned input %q changed during build", planned.ID)
			}
			item.ActualRevision = actual
		case deployment.DeliveryDataBounded:
			return deployment.DeliveryBuildOutput{}, fmt.Errorf("bounded input %q has no authoritative build resolver", planned.ID)
		case deployment.DeliveryDataObserved:
			// Observed inputs are disallowed by the production plan policy. Keep
			// this branch explicit so a future policy cannot silently omit proof.
			return deployment.DeliveryBuildOutput{}, fmt.Errorf("observed candidate input %q has no authoritative observation adapter", planned.ID)
		}
		resolved.Inputs = append(resolved.Inputs, item)
	}
	return deployment.DeliveryBuildOutput{Catalog: catalogseal.FileCatalog{Path: qualified.CatalogPath()}, QualificationDigest: qualified.Record.Digest, ClosureDigest: qualified.Record.Closure.Digest, CompatibilityDigest: compatibilityDigest, ResolvedInputs: resolved, ObjectStore: r.config.ObjectStore, SealRepository: r.config.SealRepository, RemoteVerifier: r.config.RemoteVerifier, Cleanup: qualified.Remove}, nil
}

// ReadOnlyCatalogVerifier is a minimal remote verifier for providers whose
// runtime read adapter performs the stronger DuckLake closure checks. It still
// proves exact bytes are readable before durable ready completion.
type ReadOnlyCatalogVerifier struct {
	PoolContract *ducklake.PoolContract
	StagingRoot  string
	ObjectStore  catalogseal.ObjectStore
}

func (v ReadOnlyCatalogVerifier) Verify(ctx context.Context, verification catalogseal.RemoteVerification) error {
	return v.verify(ctx, verification)
}

func (v ReadOnlyCatalogVerifier) verify(ctx context.Context, verification catalogseal.RemoteVerification) error {
	if v.PoolContract == nil || v.PoolContract.Validate() != nil {
		return fmt.Errorf("remote verifier requires admitted pool contract")
	}
	object, err := verification.Open(ctx)
	if err != nil {
		return err
	}
	if object.Body == nil {
		return fmt.Errorf("remote catalog body is nil")
	}
	root := v.StagingRoot
	if root == "" {
		root, err = os.MkdirTemp("", "leapview-sealed-verify-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(root)
	} else if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(root, "catalog-*.ducklake")
	if err != nil {
		_ = object.Body.Close()
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	hash := sha256.New()
	bytesWritten, copyErr := io.Copy(io.MultiWriter(file, hash), object.Body)
	closeErr := errors.Join(file.Close(), object.Body.Close())
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if bytesWritten != verification.Identity.ObjectSize || object.Size != 0 && object.Size != verification.Identity.ObjectSize {
		return fmt.Errorf("remote catalog size changed: body=%d metadata=%d expected=%d", bytesWritten, object.Size, verification.Identity.ObjectSize)
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if digest != verification.Identity.CatalogDigest {
		return fmt.Errorf("remote catalog digest changed: got %s want %s", digest, verification.Identity.CatalogDigest)
	}
	metadataDigest, digestPresent := object.Metadata[catalogseal.MetadataDigest]
	if !digestPresent || metadataDigest != digest {
		return fmt.Errorf("remote catalog metadata digest changed: got %s want %s", metadataDigest, digest)
	}
	metadataSize, sizePresent := object.Metadata[catalogseal.MetadataSize]
	parsedSize, parseErr := strconv.ParseInt(metadataSize, 10, 64)
	if !sizePresent || parseErr != nil || parsedSize != verification.Identity.ObjectSize {
		return fmt.Errorf("remote catalog metadata size changed: got %q want %d", metadataSize, verification.Identity.ObjectSize)
	}
	dataPath, err := v.PoolContract.Pool.DataPath()
	if err != nil {
		return err
	}
	preview, err := candidatecatalog.OpenReadOnlyCatalog(ctx, root, path, v.PoolContract)
	if err != nil {
		return err
	}
	defer preview.Close()
	snapshots, err := preview.Snapshots(ctx)
	if err != nil {
		return err
	}
	if len(snapshots) != 1 {
		return fmt.Errorf("sealed catalog has %d snapshots, want one", len(snapshots))
	}
	policy, err := preview.DataInliningPolicy(ctx)
	if err != nil {
		return err
	}
	if err := policy.ValidateZero(); err != nil {
		return fmt.Errorf("remote catalog data inlining policy is not zero: %w", err)
	}
	inline, err := preview.LegacyInlineTables(ctx)
	if err != nil {
		return err
	}
	if err := ducklake.ValidateNoLiveInlineData(inline); err != nil {
		return fmt.Errorf("remote catalog has live inline data: %w", err)
	}
	closure, err := preview.CurrentClosure(ctx, v.PoolContract.Pool.ID.String())
	if err != nil {
		return err
	}
	if closure.Digest != verification.Identity.Closure.Digest {
		return fmt.Errorf("remote closure digest changed: got %s want %s", closure.Digest, verification.Identity.Closure.Digest)
	}
	for _, file := range closure.Files {
		if err := v.probeRemoteFile(ctx, dataPath, file.Reference); err != nil {
			return err
		}
	}
	return nil
}

func (v ReadOnlyCatalogVerifier) probeRemoteFile(ctx context.Context, dataPath, reference string) error {
	var body io.ReadCloser
	var err error
	canonical, canonicalErr := candidatecatalog.CanonicalPoolReference(dataPath, reference)
	if canonicalErr != nil {
		return fmt.Errorf("canonicalize sealed closure object %q: %w", reference, canonicalErr)
	}
	if strings.Contains(dataPath, "://") && v.ObjectStore != nil {
		base, parseErr := url.Parse(dataPath)
		ref, refErr := url.Parse(canonical)
		if parseErr != nil || refErr != nil || base.Host != ref.Host || !strings.HasPrefix(ref.Path, strings.TrimSuffix(base.Path, "/")+"/") {
			return fmt.Errorf("sealed closure object %q is outside admitted pool namespace", reference)
		}
		key := strings.TrimPrefix(strings.TrimPrefix(ref.Path, base.Path), "/")
		object, openErr := v.ObjectStore.Open(ctx, key)
		if openErr != nil {
			return fmt.Errorf("probe sealed closure object %q: %w", reference, openErr)
		}
		body = object.Body
	} else {
		body, err = candidatecatalog.LocalObjectProbe{}.Open(ctx, canonical)
		if err != nil {
			return fmt.Errorf("probe sealed closure file %q: %w", reference, err)
		}
	}
	if body == nil {
		return fmt.Errorf("probe sealed closure object %q returned nil body", reference)
	}
	var one [1]byte
	_, readErr := body.Read(one[:])
	closeErr := body.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("read sealed closure object %q: %w", reference, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close sealed closure object %q: %w", reference, closeErr)
	}
	return nil
}
