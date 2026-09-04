package deploymentpostgres

// Native BuildPlan orchestration.  This coordinator is intentionally kept
// separate from the existing plan coordinator: the build hand-off crosses an
// external DuckLake writer and therefore has a distinct reservation, attempt,
// qualification, and replay contract.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/gates"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	manageddataresolver "github.com/flidai/leapview/internal/manageddata/resolver"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	project "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

const (
	nativeBuildDefaultLease         = 30 * time.Minute
	nativeBuildDefaultSessionPrefix = "native-build-"
	nativeBuildHeartbeatMaxInterval = 24 * time.Hour
	nativeBuildSettlementTimeout    = 30 * time.Second
)

// NativeBuildArtifactPhases is the release boundary needed by BuildPlan. The
// coordinator invokes inspection before the durable attempt transaction and
// materialization/hydration with the deterministic serving-generation ID.
type NativeBuildArtifactPhases interface {
	NativeReleaseArtifactInspector
	MaterializeCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet) (release.CandidateArtifactSet, error)
	HydrateCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet, release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error)
}

// NativeBuildContractResolver is deliberately value-only. Production uses
// *NativeBuildContractAuthority, while tests may inject a narrow exact reader.
type NativeBuildContractResolver interface {
	Resolve(context.Context, NativeBuildContractRequest) (NativeBuildContract, error)
}

// NativeCandidateManagedDataResolver materializes exact immutable revision
// pins before a native serving-state row exists. Activated runtimes use the
// separate serving-state binding resolver after generation admission.
type NativeCandidateManagedDataResolver interface {
	ResolveCandidateManagedData(context.Context, projectgraph.ResourceID, map[projectgraph.ResourceID]string) (manageddataresolver.Resolution, error)
}

// NativeBuildHeartbeatRunner renews every lease protecting an admitted build.
// NativeBuildHeartbeat is the production implementation; tests and embedders
// may provide a value-only runner with the same atomic renewal contract.
type NativeBuildHeartbeatRunner interface {
	Renew(context.Context, NativeBuildHeartbeatInput) (NativeBuildHeartbeatResult, error)
}

// NativeBuildConfig contains all authorities needed for one native build. No
// authority is inferred from process globals or a latest-row lookup.
type NativeBuildConfig struct {
	Repository          *deploymentnative.Repository
	Sources             project.CandidateSourceAttestationReader
	Artifacts           NativeBuildArtifactPhases
	ArtifactRecovery    release.CandidateArtifactRecovery
	BindingEvidence     deploymentdomain.CandidateConnectionEvidenceResolver
	Connections         deploymentdomain.CandidateConnectionLeaser
	ManagedData         NativeCandidateManagedDataResolver
	Contract            NativeBuildContractResolver
	ContractAuthority   *NativeBuildContractAuthority
	PhysicalPoolID      string
	CompatibilityDigest string

	Operations            deploymentmodule.NativeBuildOperationAuthority
	Heartbeat             NativeBuildHeartbeatRunner
	AttemptAdmission      CandidateBuildAttemptAdmission
	AttemptTermination    AttemptTermination
	GenerationAdmission   GenerationAdmission
	PhysicalFactory       NativePhysicalBuildEnvironmentFactory
	ObservationWriter     ducklakepostgres.SourceObservationWriter
	MarkerResolverFactory NativePhysicalMarkerResolverFactory
	MarkerQuarantine      NativeMarkerQuarantineWriter
	ObservationReader     NativeSourceObservationReader
	SnapshotFactory       NativePhysicalSnapshotInspectorFactory
	QualificationFactory  NativeQualificationEnvironmentFactory
	RuntimeVersion        string
	Bounds                gates.Bounds
	SessionIdentity       string
	LeaseDuration         time.Duration
	HeartbeatInterval     time.Duration
	Clock                 func() time.Time
	Events                deploymentmodule.NativeDeliveryEventAppender
	Audit                 deploymentmodule.NativeDeliveryAuditAppender
	Workflow              deploymentmodule.NativeDeliveryWorkflowRecorder
}

// NativeBuildCoordinatorConfig is an expressive alias used by callers that
// name this object by its orchestration role.
type NativeBuildCoordinatorConfig = NativeBuildConfig

// NativeBuildCoordinator implements the clean-slate delivery mutation port.
type NativeBuildCoordinator struct {
	repository                          *deploymentnative.Repository
	sources                             project.CandidateSourceAttestationReader
	artifacts                           NativeBuildArtifactPhases
	artifactRecovery                    release.CandidateArtifactRecovery
	bindingEvidence                     deploymentdomain.CandidateConnectionEvidenceResolver
	connections                         deploymentdomain.CandidateConnectionLeaser
	managedData                         NativeCandidateManagedDataResolver
	contract                            NativeBuildContractResolver
	physicalPoolID, compatibilityDigest string
	operations                          deploymentmodule.NativeBuildOperationAuthority
	heartbeat                           NativeBuildHeartbeatRunner
	attemptAdmission                    CandidateBuildAttemptAdmission
	attemptTermination                  AttemptTermination
	generationAdmission                 GenerationAdmission
	physicalFactory                     NativePhysicalBuildEnvironmentFactory
	observationWriter                   ducklakepostgres.SourceObservationWriter
	markerResolverFactory               NativePhysicalMarkerResolverFactory
	markerQuarantine                    NativeMarkerQuarantineWriter
	observationReader                   NativeSourceObservationReader
	snapshotFactory                     NativePhysicalSnapshotInspectorFactory
	qualificationFactory                NativeQualificationEnvironmentFactory
	runtimeVersion, sessionIdentity     string
	bounds                              gates.Bounds
	leaseDuration, heartbeatInterval    time.Duration
	clock                               func() time.Time
	events                              deploymentmodule.NativeDeliveryEventAppender
	eventReader                         nativeDeliveryEventReader
	audit                               deploymentmodule.NativeDeliveryAuditAppender
	auditReader                         nativeDeliveryAuditReader
	workflow                            deploymentmodule.NativeDeliveryWorkflowRecorder
	operationLookup                     nativeOperationLookup
}

// nativeBuildPlan keeps the rich execution contract together with the
// persisted serving-artifact digest selected during planning. The latter is a
// PostgreSQL projection field, not part of deployment.DeliveryPlan.
type nativeBuildPlan struct {
	deploymentdomain.DeliveryPlan
	ArtifactDigest string
}

var _ deploymentmodule.NativeDeliveryMutationPort = (*NativeBuildCoordinator)(nil)

func NewNativeBuildCoordinator(config NativeBuildConfig) (*NativeBuildCoordinator, error) {
	if config.Repository == nil || !config.Repository.Configured() || !config.Repository.TransactionCapable() {
		return nil, errors.New("native build requires a configured transaction-capable PostgreSQL repository")
	}
	if nativeBuildAuthorityNil(config.Sources) || nativeBuildAuthorityNil(config.ManagedData) || nativeBuildAuthorityNil(config.Operations) || nativeBuildAuthorityNil(config.Heartbeat) || nativeBuildAuthorityNil(config.AttemptAdmission) || nativeBuildAuthorityNil(config.AttemptTermination) || nativeBuildAuthorityNil(config.GenerationAdmission) || nativeBuildAuthorityNil(config.PhysicalFactory) || nativeBuildAuthorityNil(config.QualificationFactory) {
		return nil, errors.New("native build source, operation, admission, and execution authorities are required")
	}
	if nativeBuildAuthorityNil(config.ObservationWriter) {
		return nil, errors.New("native build source observation writer is required")
	}
	if nativeBuildAuthorityNil(config.ArtifactRecovery) {
		return nil, errors.New("native build artifact recovery authority is required")
	}
	if nativeBuildAuthorityNil(config.MarkerResolverFactory) || nativeBuildAuthorityNil(config.MarkerQuarantine) || nativeBuildAuthorityNil(config.ObservationReader) || nativeBuildAuthorityNil(config.SnapshotFactory) {
		return nil, errors.New("native build physical recovery authorities are required")
	}
	artifacts := config.Artifacts
	if nativeBuildAuthorityNil(artifacts) {
		return nil, errors.New("native build artifact phases are required")
	}
	contract := config.Contract
	if nativeBuildAuthorityNil(contract) {
		contract = config.ContractAuthority
	}
	if nativeBuildAuthorityNil(contract) {
		return nil, errors.New("native build contract authority is required")
	}
	if _, ok := config.Operations.(nativeOperationLookup); !ok {
		return nil, errors.New("native build operation replay reader is required")
	}
	if nativeBuildAuthorityNil(config.Events) || nativeBuildAuthorityNil(config.Audit) {
		return nil, errors.New("native build event and audit authorities are required")
	}
	if config.Workflow != nil && nativeBuildAuthorityNil(config.Workflow) {
		return nil, errors.New("native build workflow authority is typed nil")
	}
	eventReader, ok := config.Events.(nativeDeliveryEventReader)
	if !ok {
		return nil, errors.New("native build durable event reader is required")
	}
	auditReader, ok := config.Audit.(nativeDeliveryAuditReader)
	if !ok {
		return nil, errors.New("native build durable audit reader is required")
	}
	if err := validateText(config.PhysicalPoolID, "physical pool id", 255); err != nil {
		return nil, fmt.Errorf("native build physical-pool identity: %w", err)
	}
	if err := platformdigest.ValidateSHA256Identity(config.CompatibilityDigest); err != nil {
		return nil, fmt.Errorf("native build compatibility identity: %w", err)
	}
	if err := validateText(config.RuntimeVersion, "runtime version", 255); err != nil {
		return nil, fmt.Errorf("native build runtime identity: %w", err)
	}
	leaseDuration := config.LeaseDuration
	if leaseDuration == 0 {
		leaseDuration = nativeBuildDefaultLease
	}
	if leaseDuration < time.Microsecond || leaseDuration > 24*time.Hour {
		return nil, errors.New("native build lease duration is outside bounds")
	}
	heartbeatInterval := config.HeartbeatInterval
	if heartbeatInterval == 0 {
		heartbeatInterval = leaseDuration / 3
		if heartbeatInterval < time.Microsecond {
			heartbeatInterval = time.Microsecond
		}
	}
	if heartbeatInterval < time.Microsecond || heartbeatInterval > nativeBuildHeartbeatMaxInterval || heartbeatInterval > leaseDuration/2 {
		return nil, errors.New("native build heartbeat interval is outside bounds")
	}
	clock := config.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	bounds := normalizeNativeBuildBounds(config.Bounds)
	session := config.SessionIdentity
	if session == "" {
		session = nativeBuildDefaultSessionPrefix
	} else if err := validateText(session, "session identity", 255); err != nil {
		return nil, fmt.Errorf("native build session identity: %w", err)
	}
	return &NativeBuildCoordinator{
		repository: config.Repository, sources: config.Sources, artifacts: artifacts, artifactRecovery: config.ArtifactRecovery, bindingEvidence: config.BindingEvidence, connections: config.Connections, managedData: config.ManagedData, contract: contract,
		physicalPoolID: config.PhysicalPoolID, compatibilityDigest: config.CompatibilityDigest,
		operations: config.Operations, heartbeat: config.Heartbeat, heartbeatInterval: heartbeatInterval, attemptAdmission: config.AttemptAdmission, attemptTermination: config.AttemptTermination, generationAdmission: config.GenerationAdmission,
		physicalFactory: config.PhysicalFactory, observationWriter: config.ObservationWriter, markerResolverFactory: config.MarkerResolverFactory, markerQuarantine: config.MarkerQuarantine, observationReader: config.ObservationReader, snapshotFactory: config.SnapshotFactory, qualificationFactory: config.QualificationFactory,
		runtimeVersion: config.RuntimeVersion, sessionIdentity: session, bounds: bounds,
		leaseDuration: leaseDuration, clock: clock, events: config.Events, eventReader: eventReader, audit: config.Audit,
		auditReader: auditReader, workflow: config.Workflow,
		operationLookup: config.Operations.(nativeOperationLookup),
	}, nil
}

func normalizeNativeBuildBounds(bounds gates.Bounds) gates.Bounds {
	if bounds.MaxRows <= 0 {
		bounds.MaxRows = 10000
	}
	if bounds.MaxQueries <= 0 {
		bounds.MaxQueries = 128
	}
	if bounds.MaxMillis <= 0 {
		bounds.MaxMillis = 5000
	}
	return bounds
}

// CreatePlan is intentionally unavailable on the build-only coordinator. The
// native plan coordinator owns plan authoring; callers should compose both
// ports when exposing the complete mutation surface.
func (c *NativeBuildCoordinator) CreatePlan(context.Context, deploymentmodule.NativeDeliveryPlanRequest) (deploymentmodule.NativeDeliveryPlan, error) {
	return deploymentmodule.NativeDeliveryPlan{}, deploymentmodule.ErrDeliveryInputUnavailable
}

// NewNativeBuild is a concise constructor alias.
func NewNativeBuild(config NativeBuildConfig) (*NativeBuildCoordinator, error) {
	return NewNativeBuildCoordinator(config)
}

func (c *NativeBuildCoordinator) BuildPlan(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (_ deploymentmodule.NativeDeliveryBuild, resultErr error) {
	if c == nil || c.repository == nil || nativeBuildAuthorityNil(c.sources) || nativeBuildAuthorityNil(c.artifacts) || nativeBuildAuthorityNil(c.artifactRecovery) || nativeBuildAuthorityNil(c.managedData) || nativeBuildAuthorityNil(c.contract) || nativeBuildAuthorityNil(c.operations) || nativeBuildAuthorityNil(c.heartbeat) || nativeBuildAuthorityNil(c.attemptAdmission) || nativeBuildAuthorityNil(c.attemptTermination) || nativeBuildAuthorityNil(c.generationAdmission) || nativeBuildAuthorityNil(c.physicalFactory) || nativeBuildAuthorityNil(c.observationWriter) || nativeBuildAuthorityNil(c.markerResolverFactory) || nativeBuildAuthorityNil(c.observationReader) || nativeBuildAuthorityNil(c.snapshotFactory) || nativeBuildAuthorityNil(c.qualificationFactory) || nativeBuildAuthorityNil(c.events) || nativeBuildAuthorityNil(c.audit) {
		return deploymentmodule.NativeDeliveryBuild{}, deploymentmodule.ErrDeliveryInputUnavailable
	}
	normalized, err := normalizeNativeBuildRequest(request)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	requestDigest, err := nativeBuildRequestDigest(normalized)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	plan, err := c.loadBuildPlan(ctx, normalized)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	owner, err := uuid.NewV7()
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("allocate native build owner: %w", err)
	}
	reservation, err := ReserveNativeBuildOperation(ctx, c.repository, c.operations, NativeBuildOperationReservationInput{
		Request: normalized, RequestDigest: requestDigest, OwnerID: owner.String(), LeaseDuration: c.leaseDuration,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	var preflightLease deploymentmodule.NativeOperationLease
	switch reservation.Disposition {
	case deploymentmodule.NativeOperationReplay:
		return c.replayBuild(ctx, normalized, requestDigest, reservation.Operation)
	case deploymentmodule.NativeOperationBusy:
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native build operation is %s", deploymentdomain.ErrDeliveryConflict, reservation.Disposition)
	case deploymentmodule.NativeOperationIndeterminate:
		return c.recoverIndeterminateNativeBuild(ctx, normalized, requestDigest, reservation, plan)
	case deploymentmodule.NativeOperationAcquired:
		// Continue with the executable lease below.
		// Until the external attempt transaction commits, every subsequent
		// error is a deterministic preflight failure. Terminalize the reserved
		// operation so an invalid plan cannot remain busy for a full lease.
		preflightLease = reservation.Lease
		defer func() {
			if resultErr != nil && preflightLease.OperationID != "" && nativeBuildPreflightFailureIsDeterministic(resultErr) {
				resultErr = c.settleNativeBuildPreflightFailure(ctx, preflightLease, requestDigest, plan.Digest, resultErr)
			}
		}()
		now := c.clock().UTC()
		if now.IsZero() || !now.Before(plan.Governance.ExpiresAt.UTC()) {
			return deploymentmodule.NativeDeliveryBuild{}, deploymentdomain.ErrDeliveryPlanExpired
		}
	default:
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: unknown native build operation disposition %q", deploymentdomain.ErrDeliveryConflict, reservation.Disposition)
	}
	if reservation.Lease.OperationID != reservation.Operation.OperationID {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: reserved operation lease identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	contract, err := c.contract.Resolve(ctx, NativeBuildContractRequest{PhysicalPoolID: c.physicalPoolID, CompatibilityDigest: c.compatibilityDigest})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if contract.PhysicalPoolID != c.physicalPoolID || contract.CompatibilityDigest != c.compatibilityDigest || contract.PoolContract == nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: resolved native build contract identity differs", deploymentnative.ErrConflict)
	}
	sourceOwner := strings.TrimSpace(plan.SourceOwnerID)
	if sourceOwner == "" {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: persisted plan source owner is missing", deploymentdomain.ErrDeliveryConflict)
	}
	source, err := c.sources.SnapshotAttestation(ctx, project.CandidateSourceScope{ProjectID: normalized.ProjectID, OwnerID: sourceOwner}, plan.SourceDigest, plan.Provenance.AttestationDigest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("verify retained source attestation: %w", err)
	}
	if source.ProjectID != normalized.ProjectID || source.ArtifactDigest != plan.SourceDigest || source.SourceAttestationDigest != plan.Provenance.AttestationDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: retained source attestation identity changed", deploymentdomain.ErrDeliveryConflict)
	}
	opID := reservation.Operation.OperationID
	candidateID, err := nativeBuildConsequenceID(opID, "candidate")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	generationID, err := nativeBuildConsequenceID(opID, "generation")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	artifactRequest := release.CandidateArtifactRequest{CandidateID: candidateID, GenerationID: generationID, OwnerID: sourceOwner, ArtifactDigest: plan.SourceDigest, Source: source,
		Scope: projectgraph.CandidateScope{ProjectID: normalized.ProjectID, Environment: normalized.Environment, BaseGenerationID: plan.BaseGenerationID}}
	inspected, err := c.artifacts.InspectCandidateArtifacts(ctx, artifactRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("inspect candidate artifacts: %w", err)
	}
	effectiveInspection, err := deploymentmodule.EffectiveCandidateArtifacts(plan.DeliveryPlan, candidateID, inspected)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("resolve effective candidate artifacts: %w", err)
	}
	if effectiveInspection.Generation.DataMode == release.GenerationDataReuseBase {
		// Native base reuse needs an exact persisted base snapshot, closure, and
		// relation-namespace binding. None of those identities may be inferred
		// from gate evidence. Exact whole-candidate reuse therefore remains
		// fail-closed; restatements and partial-reuse plans are projected to a
		// fresh source materialization by EffectiveCandidateArtifacts above.
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native base-snapshot reuse admission is not configured", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	if effectiveInspection.Generation.ArtifactDigest == "" || effectiveInspection.Generation.ArtifactDigest != plan.ArtifactDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: inspected serving artifact differs from planned artifact", deploymentdomain.ErrDeliveryConflict)
	}
	if err := validateNativeBuildArtifacts(effectiveInspection, normalized, plan.DeliveryPlan); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	// Inspection is read-only evidence. Even when it reports a planned
	// serving identity, acquired execution must materialize through the
	// authority so the object and bundle bytes are freshly bound to this
	// deterministic generation.
	effective, err := c.artifacts.MaterializeCandidateArtifacts(ctx, artifactRequest, effectiveInspection)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("materialize candidate artifacts: %w", err)
	}
	if err := validateNativeBuildArtifacts(effective, normalized, plan.DeliveryPlan); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if effective.Generation.Identity.GenerationID != generationID || effective.Generation.ServingArtifactID == "" || effective.Generation.ArtifactDigest == "" || platformdigest.ValidateSHA256Identity(effective.Generation.ArtifactDigest) != nil {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: candidate artifact generation identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	// The plan's artifact digest is the immutable serving-bundle identity
	// selected during planning. Materialization may only reproduce that exact
	// bundle; a source digest is not a valid substitute here.
	if effective.Generation.ArtifactDigest != plan.ArtifactDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: candidate serving artifact differs from planned artifact", deploymentdomain.ErrDeliveryConflict)
	}
	bindingRequest := nativeCandidateConnectionRequest(
		candidateID, normalized.PrincipalID, normalized.TargetID, effective,
	)
	bindingDigest, err := resolveNativeCandidateBindingDigest(ctx, c.bindingEvidence, bindingRequest)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if bindingDigest != plan.Execution.BindingDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: candidate connection evidence differs from planned binding identity", deploymentdomain.ErrDeliveryConflict)
	}
	materializationRequest, managedDataLifetime, err := prepareNativeMaterializationRequest(ctx, c.managedData, effective, normalized, generationID, candidateID, "", plan.DeliveryPlan)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	managedDataReleased := false
	releaseManagedData := func() error {
		if managedDataReleased || managedDataLifetime == nil {
			return nil
		}
		managedDataReleased = true
		return managedDataLifetime.Release()
	}
	defer func() { _ = releaseManagedData() }()

	attemptID, err := nativeBuildConsequenceID(opID, "attempt")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	leaseID, err := nativeBuildConsequenceID(opID, "lease")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	session := c.sessionIdentity
	if session == nativeBuildDefaultSessionPrefix {
		session += opID
	}
	attemptIdentity := "native-build/" + opID
	if len(attemptIdentity) > 512 {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: attempt identity is oversized", deploymentdomain.ErrDeliveryInvalid)
	}
	marker := nativeBuildMarker(opID, generationID, attemptID, requestDigest, normalized, plan.Digest, c.physicalPoolID, 1)
	// Admission supplies the target lease epoch; the marker is rebuilt after
	// admission so no caller-selected fencing value crosses the boundary.
	firstTx, err := c.repository.Begin(ctx)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	firstCommitted := false
	defer func() {
		if !firstCommitted {
			_ = firstTx.Rollback(context.Background())
		}
	}()
	bound, err := c.operations.BeginAttemptTx(ctx, firstTx, deploymentmodule.NativeOperationBeginAttemptInput{Lease: reservation.Lease, AttemptID: attemptID, AttemptIdentity: attemptIdentity})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if bound.AttemptID != attemptID || bound.Lease.AttemptID != attemptID {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: operation attempt identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if _, err := c.repository.CreateCandidateAllocatedTx(ctx, firstTx, deploymentnative.CandidateInput{CandidateID: candidateID, TargetID: normalized.TargetID, PlanID: plan.ID, ArtifactDigest: effective.Generation.ArtifactDigest}); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	attemptAdmission, err := c.attemptAdmission.AdmitCandidateBuildAttemptTx(ctx, firstTx, CandidateBuildAttemptAdmissionInput{
		Lease:     deploymentnative.LeaseInput{LeaseID: leaseID, TargetID: normalized.TargetID, OwnerID: normalized.PrincipalID, ExpiresAt: reservation.Lease.LeaseExpiresAt},
		Attempt:   deploymentnative.BuildAttemptInput{AttemptID: attemptID, PlanID: plan.ID, CandidateID: candidateID, OwnerID: normalized.PrincipalID, PhysicalPoolID: c.physicalPoolID, RequestDigest: requestDigest, PlanDigest: plan.Digest, SessionIdentity: session},
		Artifact:  CandidateBuildArtifactInput{ServingArtifactID: effective.Generation.ServingArtifactID, ServingArtifactDigest: effective.Generation.ArtifactDigest, ServingStateID: generationID},
		CatalogID: contract.Catalog.CatalogID,
	})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	// Once commit is attempted, its result can be ambiguous to this process:
	// PostgreSQL may have durably bound the external attempt before a network
	// error is returned. Disable preflight failure settlement before crossing
	// that boundary so we never mark an operation failed while the delivery
	// attempt is running. Lease expiry/recovery will fence an uncertain commit.
	preflightLease = deploymentmodule.NativeOperationLease{}
	if err := firstTx.Commit(ctx); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	firstCommitted = true
	marker.LeaseEpoch = attemptAdmission.Attempt.FencingEpoch
	marker.FencingToken = fmt.Sprintf("%d", marker.LeaseEpoch)
	buildCtx, buildCancel := context.WithCancel(ctx)
	heartbeatGuard := newNativeBuildHeartbeatGuard(ctx, c.heartbeat, c.heartbeatInterval, NativeBuildHeartbeatInput{
		OperationLease: bound.Lease, TargetLease: deploymentnative.LeaseFence{LeaseID: attemptAdmission.Lease.LeaseID, TargetID: attemptAdmission.Lease.TargetID, OwnerID: attemptAdmission.Lease.OwnerID, FencingEpoch: attemptAdmission.Lease.FencingEpoch},
		AttemptID: attemptAdmission.Attempt.AttemptID, AttemptOwnerID: attemptAdmission.Attempt.OwnerID, AttemptFencingEpoch: attemptAdmission.Attempt.FencingEpoch, Duration: c.leaseDuration,
	}, buildCancel)
	heartbeatStopped := false
	stopHeartbeat := func() (deploymentmodule.NativeOperationLease, error) {
		if heartbeatStopped {
			return bound.Lease, nil
		}
		heartbeatStopped = true
		latest, heartbeatErr := heartbeatGuard.Stop()
		if latest.OperationLease.OperationID != "" {
			bound.Lease = latest.OperationLease
		}
		return bound.Lease, heartbeatErr
	}
	settle := func(buildErr error, classification NativePhysicalFailureClassification, phase NativePhysicalBuildPhase, physical *NativePhysicalBuildEvidence) error {
		operationLease, heartbeatErr := stopHeartbeat()
		if heartbeatErr != nil {
			buildErr = errors.Join(buildErr, heartbeatErr)
			classification = NativePhysicalFailureIndeterminate
		}
		return c.settleNativeBuildFailure(ctx, operationLease, attemptAdmission, buildErr, classification, phase, physical)
	}
	defer func() {
		if !heartbeatStopped {
			_, _ = stopHeartbeat()
		}
		buildCancel()
	}()
	physicalRoot, err := contract.PoolContract.Pool.DataPath()
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, settle(err, NativePhysicalFailureDeterministic, NativePhysicalBuildPhaseValidation, nil)
	}
	materializationRequest.RelationNamespace = attemptAdmission.Attempt.Namespace
	physicalInput := NativePhysicalBuildInput{Attempt: attemptAdmission.Attempt, Marker: marker, CatalogID: contract.Catalog.CatalogID, ObjectRoot: physicalRoot, ObservationWriter: c.observationWriter, CaptureClock: c.clock, Request: materializationRequest}
	physicalContext := analyticsmaterialize.WithObservationBudget(buildCtx, analyticsmaterialize.ObservationBudget{MaxQueries: c.bounds.MaxQueries, MaxMillis: c.bounds.MaxMillis})
	physical, bindingEvidence, err := buildNativePhysicalWithCandidateBindingsEvidence(physicalContext, c.connections, bindingRequest, plan.Execution.BindingDigest, physicalInput, c.physicalFactory)
	if releaseErr := releaseManagedData(); releaseErr != nil {
		err = nativePhysicalBuildIndeterminateFailure(NativePhysicalBuildPhaseEvidence, errors.Join(err, fmt.Errorf("release native candidate managed-data roots: %w", releaseErr)))
	}
	if err != nil {
		classification, phase := NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseMaterialize
		if failure, ok := NativePhysicalBuildFailureOf(err); ok {
			classification, phase = failure.Classification, failure.Phase
		}
		var physicalEvidence *NativePhysicalBuildEvidence
		if physical.SnapshotID > 0 {
			physicalEvidence = &physical
		}
		return deploymentmodule.NativeDeliveryBuild{}, settle(err, classification, phase, physicalEvidence)
	}
	sources, models, err := nativeQualificationInputs(effective, physical.SourceObservations)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, settle(err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	qualification, err := QualifyNativeSnapshot(buildCtx, NativeQualificationRequest{Build: physical, CandidateID: candidateID, SourceDigest: plan.SourceDigest, BindingGeneration: plan.Execution.BindingDigest, RuntimeVersion: c.runtimeVersion, Compatibility: contract.Compatibility, Sources: sources, Models: models, Bounds: c.bounds, Now: c.clock().UTC()}, c.qualificationFactory)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, settle(err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	bindingDigest, err = deploymentdomain.BindingFingerprint(bindingEvidence)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, settle(fmt.Errorf("fingerprint acquired candidate connection evidence: %w", err), NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	if bindingDigest != plan.Execution.BindingDigest {
		return deploymentmodule.NativeDeliveryBuild{}, settle(fmt.Errorf("%w: acquired candidate connection evidence differs from planned binding identity", deploymentdomain.ErrDeliveryConflict), NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	if _, heartbeatErr := stopHeartbeat(); heartbeatErr != nil {
		return deploymentmodule.NativeDeliveryBuild{}, c.settleNativeBuildFailure(ctx, bound.Lease, attemptAdmission, heartbeatErr, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	sealID, err := nativeBuildConsequenceID(opID, "seal")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, c.settleNativeBuildFailure(ctx, bound.Lease, attemptAdmission, err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	assembled, err := AssembleNativeGenerationAdmissionInput(NativeSealEvidenceAssemblerInput{Build: physical, AttemptAdmission: attemptAdmission, PoolContract: contract.PoolContract, CatalogIdentity: contract.Catalog, Compatibility: contract.Compatibility, Plan: plan.DeliveryPlan, Artifacts: effective, Bindings: bindingEvidence, SourceRevision: source.SourceRevision, RuntimeVersion: c.runtimeVersion, Qualification: qualification, SealID: sealID, GenerationID: generationID, TenantDomain: contract.TenantDomain, EncryptionDomain: contract.EncryptionDomain, ObjectNamespace: contract.ObjectNamespace})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, c.settleNativeBuildFailure(ctx, bound.Lease, attemptAdmission, err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	// Completion must use the attempt-bound, heartbeat-renewed operation fence,
	// not the pre-admission reservation projection.
	reservation.Lease = bound.Lease
	reservation.Operation.AttemptID = bound.Lease.AttemptID
	reservation.Operation.AttemptIdentity = bound.Lease.AttemptIdentity
	reservation.Operation.FencingGeneration = bound.Lease.FencingGeneration
	reservation.Operation.LeaseExpiresAt = bound.Lease.LeaseExpiresAt
	built, err := c.completeNativeBuild(ctx, normalized, requestDigest, reservation, plan.DeliveryPlan, assembled, effective, attemptAdmission, sealID, generationID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, c.settleNativeBuildFailure(ctx, bound.Lease, attemptAdmission, err, NativePhysicalFailureIndeterminate, NativePhysicalBuildPhaseEvidence, &physical)
	}
	return built, nil
}

// nativeBuildTerminationEvidence is deliberately free of raw error text.
// The digest gives operators a stable correlation key while the exact attempt
// and physical identities make recovery/reconciliation unambiguous.
type nativeBuildTerminationEvidence struct {
	SchemaVersion   int                                 `json:"schemaVersion"`
	AttemptID       string                              `json:"attemptId"`
	OwnerID         string                              `json:"ownerId"`
	FencingEpoch    int64                               `json:"fencingEpoch"`
	RequestDigest   string                              `json:"requestDigest"`
	PlanDigest      string                              `json:"planDigest"`
	PhysicalPoolID  string                              `json:"physicalPoolId"`
	Namespace       string                              `json:"namespace"`
	SessionIdentity string                              `json:"sessionIdentity"`
	Phase           NativePhysicalBuildPhase            `json:"phase"`
	Classification  NativePhysicalFailureClassification `json:"classification"`
	ErrorDigest     string                              `json:"errorDigest"`
	SnapshotID      int64                               `json:"snapshotId,omitempty"`
	CommitMarker    json.RawMessage                     `json:"commitMarker,omitempty"`
}

// nativeBuildPreflightFailureEvidence is the replay-safe terminal document
// for failures before an external attempt is durably bound. It deliberately
// contains no raw error text and cannot be mistaken for post-attempt evidence.
type nativeBuildPreflightFailureEvidence struct {
	SchemaVersion  int                                 `json:"schemaVersion"`
	RequestDigest  string                              `json:"requestDigest"`
	PlanDigest     string                              `json:"planDigest"`
	Phase          NativePhysicalBuildPhase            `json:"phase"`
	Classification NativePhysicalFailureClassification `json:"classification"`
	ErrorDigest    string                              `json:"errorDigest"`
}

func (c *NativeBuildCoordinator) settleNativeBuildPreflightFailure(ctx context.Context, operationLease deploymentmodule.NativeOperationLease, requestDigest, planDigest string, buildErr error) error {
	if c == nil || c.repository == nil || c.operations == nil {
		return errors.Join(buildErr, fmt.Errorf("%w: native build preflight settlement is unavailable", deploymentmodule.ErrDeliveryInputUnavailable))
	}
	errorDigest := sha256.Sum256([]byte(errorString(buildErr)))
	evidenceJSON, err := json.Marshal(nativeBuildPreflightFailureEvidence{
		SchemaVersion: 1, RequestDigest: requestDigest, PlanDigest: planDigest,
		Phase: NativePhysicalBuildPhaseValidation, Classification: NativePhysicalFailureDeterministic,
		ErrorDigest: "sha256:" + hex.EncodeToString(errorDigest[:]),
	})
	if err != nil {
		return errors.Join(buildErr, err)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), nativeBuildSettlementTimeout)
	defer cleanupCancel()
	tx, err := c.repository.Begin(cleanupCtx)
	if err != nil {
		return errors.Join(buildErr, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := c.operations.FailTx(cleanupCtx, tx, operationLease, evidenceJSON); err != nil {
		return errors.Join(buildErr, err)
	}
	if err := tx.Commit(cleanupCtx); err != nil {
		return errors.Join(buildErr, err)
	}
	committed = true
	return buildErr
}

func nativeBuildPreflightFailureIsDeterministic(err error) bool {
	return errors.Is(err, deploymentdomain.ErrDeliveryInvalid) ||
		errors.Is(err, deploymentdomain.ErrDeliveryConflict) ||
		errors.Is(err, deploymentdomain.ErrDeliveryPlanExpired) ||
		errors.Is(err, deploymentnative.ErrInvalid) ||
		errors.Is(err, deploymentnative.ErrConflict) ||
		errors.Is(err, release.ErrCandidateArtifactInvalid) ||
		errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable)
}

// settleNativeBuildFailure closes the delivery attempt and corresponding
// operation state after an error once the first attempt transaction has
// committed. The injected AttemptTermination authority updates the delivery
// ledger on this same PostgreSQL transaction. The operation transition and
// target lease release share that transaction as well.
func (c *NativeBuildCoordinator) settleNativeBuildFailure(ctx context.Context, operationLease deploymentmodule.NativeOperationLease, admission CandidateBuildAttemptAdmissionResult, buildErr error, classification NativePhysicalFailureClassification, phase NativePhysicalBuildPhase, physical *NativePhysicalBuildEvidence) error {
	if c == nil || c.repository == nil || c.attemptTermination == nil {
		return errors.Join(buildErr, fmt.Errorf("%w: native build attempt termination is unavailable", deploymentmodule.ErrDeliveryInputUnavailable))
	}
	if classification != NativePhysicalFailureDeterministic && classification != NativePhysicalFailureIndeterminate {
		classification = NativePhysicalFailureIndeterminate
	}
	if phase == "" {
		phase = NativePhysicalBuildPhaseEvidence
	}
	errorDigest := sha256.Sum256([]byte(errorString(buildErr)))
	evidence := nativeBuildTerminationEvidence{
		SchemaVersion: 1, AttemptID: admission.Attempt.AttemptID, OwnerID: admission.Attempt.OwnerID,
		FencingEpoch: admission.Attempt.FencingEpoch, RequestDigest: admission.Attempt.RequestDigest,
		PlanDigest: admission.Attempt.PlanDigest, PhysicalPoolID: admission.Attempt.PhysicalPoolID,
		Namespace: admission.Attempt.Namespace, SessionIdentity: admission.Attempt.SessionIdentity,
		Phase: phase, Classification: classification, ErrorDigest: "sha256:" + hex.EncodeToString(errorDigest[:]),
	}
	if physical != nil && physical.SnapshotID > 0 {
		evidence.SnapshotID = physical.SnapshotID
		evidence.CommitMarker = append(json.RawMessage(nil), physical.CanonicalMarkerJSON...)
		if len(evidence.CommitMarker) == 0 {
			canonical, err := physical.Marker.CanonicalJSON()
			if err != nil {
				return errors.Join(buildErr, err)
			}
			evidence.CommitMarker = json.RawMessage(canonical)
		}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return errors.Join(buildErr, err)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), nativeBuildSettlementTimeout)
	defer cleanupCancel()
	tx, err := c.repository.Begin(cleanupCtx)
	if err != nil {
		return errors.Join(buildErr, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	lockedOperation, err := lockNativeBuildSettlementOperationTx(cleanupCtx, tx, c.operations, operationLease, admission.Attempt.RequestDigest)
	if err != nil {
		return errors.Join(buildErr, err)
	}
	lockedLease, err := lockNativeBuildLeaseTx(cleanupCtx, tx, c.repository, admission.Lease, "active", "released")
	if err != nil {
		return errors.Join(buildErr, err)
	}
	if !lockedLease.ExpiresAt.Equal(lockedOperation.LeaseExpiresAt) {
		return errors.Join(buildErr, fmt.Errorf("%w: settlement operation and target lease deadlines differ", deploymentdomain.ErrDeliveryConflict))
	}
	terminationEvidence := json.RawMessage(evidenceJSON)
	if classification == NativePhysicalFailureIndeterminate && lockedOperation.State == deploymentmodule.NativeOperationStateIndeterminate {
		// Expiry takeover owns the operation's uncertainty evidence. Preserve it
		// into the delivery attempt so a later marker-based recovery has one exact
		// canonical evidence identity rather than an unrecoverable split.
		terminationEvidence = append(json.RawMessage(nil), lockedOperation.AttemptEvidence...)
	}
	terminationInput := AttemptTerminationInput{AttemptID: admission.Attempt.AttemptID, OwnerID: admission.Attempt.OwnerID, FencingEpoch: admission.Attempt.FencingEpoch, Evidence: terminationEvidence}
	if classification == NativePhysicalFailureDeterministic {
		_, err = c.attemptTermination.AbortAttemptTx(cleanupCtx, tx, terminationInput)
	} else {
		_, err = c.attemptTermination.MarkAttemptIndeterminateTx(cleanupCtx, tx, terminationInput)
	}
	if err == nil && classification == NativePhysicalFailureDeterministic {
		_, err = c.repository.RejectCandidateTx(cleanupCtx, tx, admission.Attempt.CandidateID)
	}
	if err == nil {
		reconcileExpiredDeterministic := false
		if classification == NativePhysicalFailureDeterministic {
			err = c.operations.FailTx(cleanupCtx, tx, operationLease, evidenceJSON)
		} else {
			err = c.operations.MarkIndeterminateTx(cleanupCtx, tx, operationLease, evidenceJSON)
		}
		if errors.Is(err, deploymentmodule.ErrNativeOperationLeaseExpired) {
			// The attempt termination above is positive evidence, so an expired
			// operation lease must not roll the cross-ledger transaction back.
			// Fence the exact expired attempt to indeterminate first; a proven
			// deterministic abort can then be reconciled to failed in this same
			// caller-owned transaction.
			err = c.operations.ExpireAttemptTx(cleanupCtx, tx, operationLease, evidenceJSON)
			if err == nil {
				reconcileExpiredDeterministic = true
			}
		}
		if errors.Is(err, deploymentmodule.ErrNativeOperationStaleFence) || errors.Is(err, deploymentmodule.ErrNativeOperationAlreadyTerminal) {
			// Expiry recovery may have won the operation row immediately before
			// this settlement transaction. Accept only the exact same attempt in
			// the expiry-fenced indeterminate state, locked on this transaction;
			// completed/failed or later-fenced records remain hard failures.
			if operationLease.FencingGeneration == 1<<63-1 {
				err = deploymentmodule.ErrNativeOperationInvalid
			} else {
				_, err = c.operations.ConfirmExpiredAttemptTx(cleanupCtx, tx, operationLease, operationLease.FencingGeneration+1)
				if err == nil {
					reconcileExpiredDeterministic = true
				}
			}
		}
		if err == nil && classification == NativePhysicalFailureDeterministic && reconcileExpiredDeterministic {
			_, err = c.operations.ReconcileAttemptTx(cleanupCtx, tx, deploymentmodule.NativeOperationReconcileAttemptInput{
				Scope: operationLease.Scope, IdempotencyKey: operationLease.IdempotencyKey,
				AttemptID: operationLease.AttemptID, AttemptIdentity: operationLease.AttemptIdentity,
				State: deploymentmodule.NativeOperationStateFailed, Outcome: evidenceJSON, Evidence: evidenceJSON,
			})
		}
	}
	if err == nil {
		err = c.repository.ReleaseLeaseAfterAttemptTerminationTx(cleanupCtx, tx, deploymentnative.LeaseFence{LeaseID: admission.Lease.LeaseID, TargetID: admission.Lease.TargetID, OwnerID: admission.Lease.OwnerID, FencingEpoch: admission.Lease.FencingEpoch})
	}
	if err == nil {
		err = tx.Commit(cleanupCtx)
	}
	if err != nil {
		return errors.Join(buildErr, err)
	}
	committed = true
	return buildErr
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (c *NativeBuildCoordinator) loadBuildPlan(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest) (nativeBuildPlan, error) {
	stored, err := c.repository.Plan(ctx, request.PlanID.String())
	if err != nil {
		return nativeBuildPlan{}, err
	}
	plan, err := stored.RichPlan()
	if err != nil {
		return nativeBuildPlan{}, err
	}
	if plan.ID != request.PlanID.String() || plan.ProjectID != request.ProjectID || plan.TargetID != request.TargetID || plan.Environment != request.Environment || plan.Status != deploymentdomain.DeliveryPlanPlanned {
		return nativeBuildPlan{}, fmt.Errorf("%w: persisted native plan identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if err := plan.Validate(); err != nil {
		return nativeBuildPlan{}, err
	}
	if plan.Provenance.AttestationDigest == "" || platformdigest.ValidateSHA256Identity(plan.SourceDigest) != nil || platformdigest.ValidateSHA256Identity(plan.Digest) != nil || platformdigest.ValidateSHA256Identity(stored.ArtifactDigest) != nil {
		return nativeBuildPlan{}, fmt.Errorf("%w: persisted native plan evidence is incomplete", deploymentdomain.ErrDeliveryConflict)
	}
	return nativeBuildPlan{DeliveryPlan: plan, ArtifactDigest: stored.ArtifactDigest}, nil
}

// nativeSourceRevision resolves the immutable source attestation used by a
// build. Recovery and successor paths must re-read this exact attestation;
// lossy reconstruction from durable delivery metadata is not admissible.
func (c *NativeBuildCoordinator) nativeSourceRevision(ctx context.Context, plan nativeBuildPlan, request deploymentmodule.NativeDeliveryBuildRequest) (*project.CandidateSourceRevision, error) {
	if c == nil || nativeBuildAuthorityNil(c.sources) {
		return nil, deploymentmodule.ErrDeliveryInputUnavailable
	}
	if strings.TrimSpace(plan.SourceOwnerID) == "" || strings.TrimSpace(plan.Provenance.AttestationDigest) == "" {
		return nil, fmt.Errorf("%w: persisted native plan source attestation evidence is incomplete", deploymentdomain.ErrDeliveryInvalid)
	}
	snapshot, err := c.sources.SnapshotAttestation(ctx, project.CandidateSourceScope{ProjectID: request.ProjectID, OwnerID: plan.SourceOwnerID}, plan.SourceDigest, plan.Provenance.AttestationDigest)
	if err != nil {
		return nil, fmt.Errorf("resolve retained source attestation for provenance: %w", err)
	}
	if snapshot.ProjectID != request.ProjectID || snapshot.ArtifactDigest != plan.SourceDigest || snapshot.SourceAttestationDigest != plan.Provenance.AttestationDigest {
		return nil, fmt.Errorf("%w: retained source attestation identity changed", deploymentdomain.ErrDeliveryConflict)
	}
	return snapshot.SourceRevision, nil
}

func validateNativeBuildArtifacts(artifacts release.CandidateArtifactSet, request deploymentmodule.NativeDeliveryBuildRequest, plan deploymentdomain.DeliveryPlan) error {
	if artifacts.Artifact.SourceDigest != plan.SourceDigest || artifacts.Compiler.Graph.ProjectID() != request.ProjectID || artifacts.Compiler.Graph.Validate() != nil || artifacts.Compiler.Artifact.ProjectID() != request.ProjectID || artifacts.Compiler.Artifact.Digest() != artifacts.Artifact.ProjectDigest {
		return fmt.Errorf("%w: candidate artifact compiler identity differs from plan", deploymentdomain.ErrDeliveryConflict)
	}
	if platformdigest.ValidateSHA256Identity(artifacts.Artifact.ProjectDigest) != nil {
		return fmt.Errorf("%w: candidate artifact project digest is invalid", deploymentdomain.ErrDeliveryInvalid)
	}
	return nil
}

func nativeBuildMarker(deliveryID, generationID, attemptID, requestDigest string, request deploymentmodule.NativeDeliveryBuildRequest, planDigest, physicalPool string, epoch int64) catalogartifact.CommitMarker {
	return catalogartifact.CommitMarker{SchemaVersion: catalogartifact.CommitMarkerSchemaVersion, DeliveryID: deliveryID, GenerationID: generationID, AttemptID: attemptID, LeaseEpoch: epoch, RequestDigest: requestDigest, PlanDigest: planDigest, Project: request.ProjectID.String(), Environment: request.Environment, PhysicalPoolID: physicalPool}
}

func nativeMaterializationRequest(artifacts release.CandidateArtifactSet, request deploymentmodule.NativeDeliveryBuildRequest, generationID, candidateID, namespace string, plan deploymentdomain.DeliveryPlan) analyticsmaterialization.Request {
	refreshDefinition := artifacts.Compiler.Artifact.RefreshDefinition()
	models := refreshDefinition.Models
	modelTables := refreshDefinition.ModelTables
	tables := make([]string, 0, len(modelTables))
	for name := range modelTables {
		tables = append(tables, name)
	}
	sort.Strings(tables)
	return analyticsmaterialization.Request{Models: models, ModelTables: modelTables, Identity: projectgraph.ServingIdentity{ProjectID: request.ProjectID, Environment: request.Environment, GenerationID: generationID}, CandidateID: candidateID, RelationNamespace: namespace, Environment: servingstate.Environment(request.Environment), TargetType: "deployment", TargetID: projectgraph.ResourceID(request.TargetID), SemanticDigest: plan.Execution.BindingDigest, ArtifactDigest: plan.SourceDigest, Tables: tables}
}

func prepareNativeMaterializationRequest(
	ctx context.Context,
	resolver NativeCandidateManagedDataResolver,
	artifacts release.CandidateArtifactSet,
	request deploymentmodule.NativeDeliveryBuildRequest,
	generationID, candidateID, namespace string,
	plan deploymentdomain.DeliveryPlan,
) (analyticsmaterialization.Request, manageddataresolver.Lifetime, error) {
	if nativeBuildAuthorityNil(resolver) {
		return analyticsmaterialization.Request{}, nil, deploymentmodule.ErrDeliveryInputUnavailable
	}
	materialization := nativeMaterializationRequest(artifacts, request, generationID, candidateID, namespace, plan)
	pins := make(map[projectgraph.ResourceID]string, len(artifacts.Generation.ManagedDataPins))
	for _, pin := range artifacts.Generation.ManagedDataPins {
		connectionID, err := projectgraph.NewResourceID(pin.ConnectionID)
		if err != nil || strings.TrimSpace(pin.RevisionID) == "" || strings.TrimSpace(pin.RevisionID) != pin.RevisionID {
			return analyticsmaterialization.Request{}, nil, fmt.Errorf("%w: native candidate managed-data pin is invalid", deploymentdomain.ErrDeliveryConflict)
		}
		if _, duplicate := pins[connectionID]; duplicate {
			return analyticsmaterialization.Request{}, nil, fmt.Errorf("%w: native candidate managed-data pin %q is duplicated", deploymentdomain.ErrDeliveryConflict, connectionID)
		}
		pins[connectionID] = pin.RevisionID
	}
	resolution, err := resolver.ResolveCandidateManagedData(ctx, request.ProjectID, pins)
	if err != nil {
		if errors.Is(err, manageddataresolver.ErrInvalidMetadata) || errors.Is(err, manageddataresolver.ErrRevisionNotReady) || errors.Is(err, manageddataresolver.ErrAmbiguousConnection) {
			err = errors.Join(deploymentdomain.ErrDeliveryConflict, err)
		}
		return analyticsmaterialization.Request{}, nil, fmt.Errorf("resolve native candidate managed-data roots: %w", err)
	}
	revisions := make(map[string]string, len(resolution.Revisions))
	for connectionID, revision := range resolution.Revisions {
		revisions[connectionID.String()] = revision
	}
	roots := make(map[string]string, len(resolution.Roots))
	for connectionID, root := range resolution.Roots {
		roots[connectionID.String()] = root
	}
	if err := validateNativeManagedDataResolution(artifacts.Generation.ManagedDataPins, revisions, roots); err != nil {
		if resolution.Lifetime != nil {
			_ = resolution.Lifetime.Release()
		}
		return analyticsmaterialization.Request{}, nil, err
	}
	if err := analyticsmodule.BindCandidateManagedDataRoots(materialization.Models, artifacts.Compiler.Artifact.Manifest().NameIndex.Connections, roots); err != nil {
		if resolution.Lifetime != nil {
			_ = resolution.Lifetime.Release()
		}
		return analyticsmaterialization.Request{}, nil, errors.Join(deploymentdomain.ErrDeliveryConflict, err)
	}
	return materialization, resolution.Lifetime, nil
}

func validateNativeManagedDataResolution(pins []release.ManagedDataPin, revisions, roots map[string]string) error {
	expected := make(map[string]string, len(pins))
	for _, pin := range pins {
		connectionID, revisionID := strings.TrimSpace(pin.ConnectionID), strings.TrimSpace(pin.RevisionID)
		if connectionID == "" || revisionID == "" || connectionID != pin.ConnectionID || revisionID != pin.RevisionID {
			return fmt.Errorf("%w: native candidate managed-data pin is invalid", deploymentdomain.ErrDeliveryConflict)
		}
		if _, duplicate := expected[connectionID]; duplicate {
			return fmt.Errorf("%w: native candidate managed-data pin %q is duplicated", deploymentdomain.ErrDeliveryConflict, connectionID)
		}
		expected[connectionID] = revisionID
	}
	if len(revisions) != len(expected) {
		return fmt.Errorf("%w: resolved native candidate managed-data revision set differs from immutable pins", deploymentdomain.ErrDeliveryConflict)
	}
	if len(roots) != len(expected) {
		return fmt.Errorf("%w: resolved native candidate managed-data root set differs from immutable pins", deploymentdomain.ErrDeliveryConflict)
	}
	for connectionID, revisionID := range revisions {
		if strings.TrimSpace(connectionID) != connectionID || strings.TrimSpace(revisionID) != revisionID || expected[connectionID] != revisionID {
			return fmt.Errorf("%w: resolved native candidate managed-data revision for %q differs from immutable pin", deploymentdomain.ErrDeliveryConflict, connectionID)
		}
	}
	for connectionID, root := range roots {
		if strings.TrimSpace(connectionID) != connectionID || strings.TrimSpace(root) == "" {
			return fmt.Errorf("%w: resolved native candidate managed-data root for %q is invalid", deploymentdomain.ErrDeliveryConflict, connectionID)
		}
		if _, ok := expected[connectionID]; !ok {
			return fmt.Errorf("%w: resolved native candidate managed-data root for %q has no immutable pin", deploymentdomain.ErrDeliveryConflict, connectionID)
		}
	}
	return nil
}

func (c *NativeBuildCoordinator) completeNativeBuild(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest, requestDigest string, reservation NativeBuildOperationReservationResult, plan deploymentdomain.DeliveryPlan, assembled GenerationAdmissionInput, artifacts release.CandidateArtifactSet, admission CandidateBuildAttemptAdmissionResult, sealID, generationID string) (deploymentmodule.NativeDeliveryBuild, error) {
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := c.generationAdmission.ValidatePhysicalAdmissionTx(ctx, tx, assembled); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	lockedOperation, err := lockNativeBuildOperationTx(ctx, tx, c.operations, reservation.Operation, deploymentmodule.NativeOperationStatePending)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if len(lockedOperation.AttemptEvidence) != 0 {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: pending completion operation carries attempt evidence", deploymentdomain.ErrDeliveryConflict)
	}
	lockedLease, err := lockNativeBuildLeaseTx(ctx, tx, c.repository, admission.Lease, "active")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if !lockedLease.ExpiresAt.Equal(reservation.Lease.LeaseExpiresAt) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: completion operation and target lease deadlines differ", deploymentdomain.ErrDeliveryConflict)
	}
	generation, err := c.generationAdmission.CompleteBuildAndAdmitTx(ctx, tx, assembled)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	eventID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "event")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	auditID, err := nativeBuildConsequenceID(reservation.Operation.OperationID, "audit")
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	payload, err := json.Marshal(nativeBuildEventPayload{OperationID: reservation.Operation.OperationID, ProjectID: request.ProjectID.String(), ResourceID: generationID, Status: "sealed"})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	event, err := c.events.AppendDeliveryEvent(ctx, tx, deploymentmodule.NativeDeliveryEventInput{EventID: eventID, ScopeID: request.TargetID, AggregateType: "delivery_build", AggregateID: reservation.Operation.OperationID, EventType: "delivery.build.sealed", SchemaVersion: 1, CorrelationID: reservation.Operation.OperationID, Payload: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if event.EventID != eventID || event.ScopeID != request.TargetID || event.AggregateType != "delivery_build" || event.AggregateID != reservation.Operation.OperationID || event.EventType != "delivery.build.sealed" || event.SchemaVersion != 1 || event.CorrelationID != reservation.Operation.OperationID || event.AggregateVersion <= 0 || event.OccurredAt.IsZero() || !sameNativeJSON(event.Payload, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native build event identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	audit, err := c.audit.AppendMutationAudit(ctx, tx, deploymentmodule.NativeDeliveryAuditInput{AuditID: auditID, DomainEventID: eventID, ScopeID: request.TargetID, ActorID: request.PrincipalID, Action: "delivery.build.sealed", ResourceKind: "build", ResourceID: reservation.Operation.OperationID, Outcome: "accepted", Operation: "build", RequestDigest: requestDigest, CorrelationID: reservation.Operation.OperationID, AggregateKey: event.AggregateID, AggregateSequence: event.AggregateVersion, Metadata: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if audit.AuditID != auditID || audit.EventID != eventID || audit.ScopeID != request.TargetID || audit.ActorID != request.PrincipalID || audit.Action != "delivery.build.sealed" || audit.ResourceKind != "build" || audit.ResourceID != reservation.Operation.OperationID || audit.Outcome != "accepted" || audit.RequestDigest != requestDigest || audit.OccurredAt.IsZero() || !sameNativeJSON(audit.Metadata, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native build audit identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if c.workflow != nil {
		if err := c.workflow.RecordWorkflow(ctx, tx, jobs.WorkflowIntent{Event: jobs.EventInput{Key: "delivery.build.sealed/" + reservation.Operation.OperationID, ResourceKind: "build", ResourceID: reservation.Operation.OperationID, EventType: "delivery.build.sealed", Data: payload}}); err != nil {
			return deploymentmodule.NativeDeliveryBuild{}, err
		}
	}
	outcome := nativeBuildOutcome{OperationID: reservation.Operation.OperationID, OperationOwnerID: reservation.Operation.OwnerID, PlanID: plan.ID, CandidateID: admission.Attempt.CandidateID, AttemptID: admission.Attempt.AttemptID, LeaseID: admission.Lease.LeaseID, GenerationID: generationID, SealID: sealID, EventID: eventID, AuditID: auditID, ServingArtifactID: artifacts.Generation.ServingArtifactID, ProjectID: request.ProjectID.String(), TargetID: request.TargetID, Environment: request.Environment, ActorID: request.PrincipalID, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, QualificationDigest: assembled.QualificationDigest, ServingArtifactDigest: artifacts.Generation.ArtifactDigest, Status: "sealed"}
	outcomeJSON, err := encodeNativeBuildOutcome(outcome, request, deploymentmodule.NativeOperationAcquireInput{Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, OwnerID: reservation.Operation.OwnerID})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if err := c.operations.CompleteTx(ctx, tx, reservation.Lease, outcomeJSON); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	completedAttempt, err := c.repository.BuildAttemptTx(ctx, tx, admission.Attempt.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if completedAttempt.AttemptID != assembled.Commit.AttemptID || completedAttempt.PlanID != assembled.Generation.PlanID || completedAttempt.CandidateID != assembled.Generation.CandidateID || completedAttempt.OwnerID != assembled.Commit.OwnerID || completedAttempt.PhysicalPoolID != assembled.Seal.PhysicalPoolID || completedAttempt.FencingEpoch != assembled.Commit.FencingEpoch || completedAttempt.RequestDigest != assembled.Seal.RequestDigest || completedAttempt.PlanDigest != assembled.Generation.PlanDigest || completedAttempt.Namespace != assembled.Seal.RelationNamespace || completedAttempt.State != deploymentnative.AttemptCommitted || completedAttempt.SnapshotID != assembled.Commit.SnapshotID || completedAttempt.SessionIdentity == "" || completedAttempt.LeaseExpiresAt.IsZero() || completedAttempt.FinishedAt.IsZero() || completedAttempt.UpdatedAt.IsZero() || completedAttempt.FinishedAt.Before(completedAttempt.CreatedAt) || len(completedAttempt.TerminationEvidence) != 0 || !sameCommitMarker(completedAttempt.CommitMarker, assembled.Commit.CommitMarker) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: completed native build attempt evidence is incomplete", deploymentdomain.ErrDeliveryConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	committed = true
	return nativeBuildProjection(outcome, plan.BaseGenerationID, completedAttempt, admission.Lease, generation.CandidateRevision)
}

type nativeBuildEventPayload struct{ OperationID, ProjectID, ResourceID, Status string }

func nativeBuildProjection(outcome nativeBuildOutcome, baseGenerationID string, attempt deploymentnative.DeliveryBuildAttempt, lease deploymentnative.DeliveryLease, candidateRevision int64) (deploymentmodule.NativeDeliveryBuild, error) {
	if candidateRevision <= 0 {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native candidate revision is invalid", deploymentdomain.ErrDeliveryConflict)
	}
	base := uuid.Nil
	if baseGenerationID != "" {
		parsed, err := uuid.Parse(baseGenerationID)
		if err != nil || parsed.String() != baseGenerationID {
			return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native build base generation identity is not canonical", deploymentdomain.ErrDeliveryConflict)
		}
		base = parsed
	}
	parseID := func(label, value string) (uuid.UUID, error) {
		canonical, err := canonicalUUIDv7(value)
		if err != nil {
			return uuid.Nil, fmt.Errorf("%w: native build %s identity is invalid: %v", deploymentdomain.ErrDeliveryConflict, label, err)
		}
		parsed, err := uuid.Parse(canonical)
		if err != nil {
			return uuid.Nil, fmt.Errorf("%w: native build %s identity is invalid: %v", deploymentdomain.ErrDeliveryConflict, label, err)
		}
		return parsed, nil
	}
	operationID, err := parseID("operation", outcome.OperationID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	eventID, err := parseID("event", outcome.EventID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	auditID, err := parseID("audit", outcome.AuditID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	attemptID, err := parseID("attempt", outcome.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	planID, err := parseID("plan", outcome.PlanID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	writerLeaseID, err := parseID("writer lease", lease.LeaseID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	servingStateID, err := parseID("serving state", outcome.GenerationID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	sealID, err := parseID("seal", outcome.SealID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	candidateID, err := parseID("candidate", outcome.CandidateID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	return deploymentmodule.NativeDeliveryBuild{ActorID: outcome.ActorID, OperationOwnerID: outcome.OperationOwnerID, IdempotencyKey: outcome.IdempotencyKey, RequestDigest: outcome.RequestDigest, OperationID: operationID, EventID: eventID, AuditID: auditID, ID: attemptID, PlanID: planID, PlanDigest: outcome.PlanDigest, SourceDigest: outcome.SourceDigest, ExecutionDigest: outcome.ExecutionDigest, BaseGenerationID: base, PhysicalPoolID: attempt.PhysicalPoolID, WriterLeaseID: writerLeaseID, ServingArtifactID: outcome.ServingArtifactID, ServingArtifactDigest: outcome.ServingArtifactDigest, ServingStateID: servingStateID, Status: "sealed", SealID: sealID, CandidateID: candidateID, CreatedAt: attempt.CreatedAt.UTC(), UpdatedAt: attempt.UpdatedAt.UTC(), TerminalAt: attempt.FinishedAt.UTC(), Revision: attempt.FencingEpoch, CandidateRevision: candidateRevision}, nil
}

func (c *NativeBuildCoordinator) replayBuild(ctx context.Context, request deploymentmodule.NativeDeliveryBuildRequest, requestDigest string, operation deploymentmodule.NativeOperationRecord) (deploymentmodule.NativeDeliveryBuild, error) {
	if operation.State == deploymentmodule.NativeOperationStateFailed {
		return deploymentmodule.NativeDeliveryBuild{}, replayFailedNativeBuild(operation, requestDigest)
	}
	if operation.State != deploymentmodule.NativeOperationStateCompleted {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: native build replay is not completed", deploymentdomain.ErrDeliveryConflict)
	}
	input := deploymentmodule.NativeOperationAcquireInput{Scope: request.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, OwnerID: operation.OwnerID}
	outcome, err := decodeNativeBuildOutcome(operation.Outcome, request, input)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	planRow, err := c.repository.Plan(ctx, outcome.PlanID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	plan, err := planRow.RichPlan()
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if plan.ID != outcome.PlanID || plan.Digest != outcome.PlanDigest || plan.SourceDigest != outcome.SourceDigest || plan.ExecutionDigest != outcome.ExecutionDigest || planRow.ArtifactDigest != outcome.ServingArtifactDigest || plan.ProjectID != request.ProjectID || plan.TargetID != request.TargetID || plan.Environment != request.Environment {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay plan identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	candidate, err := c.repository.Candidate(ctx, outcome.CandidateID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if candidate.CandidateID != outcome.CandidateID || candidate.TargetID != request.TargetID || candidate.PlanID != outcome.PlanID || candidate.AttemptID != outcome.AttemptID || candidate.SnapshotSealID != outcome.SealID || candidate.Status != "qualified" || candidate.ArtifactDigest != outcome.ServingArtifactDigest || candidate.QualificationDigest != outcome.QualificationDigest || candidate.CandidateRevision <= 0 || candidate.CreatedAt.IsZero() || candidate.QualifiedAt.IsZero() || !candidate.RetiredAt.IsZero() {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay candidate identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	attempt, err := c.repository.BuildAttempt(ctx, outcome.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if attempt.AttemptID != outcome.AttemptID || attempt.PlanID != outcome.PlanID || attempt.CandidateID != outcome.CandidateID || attempt.OwnerID != request.PrincipalID || attempt.State != deploymentnative.AttemptCommitted || attempt.RequestDigest != requestDigest || attempt.PlanDigest != outcome.PlanDigest || attempt.PhysicalPoolID == "" || attempt.FencingEpoch <= 0 || attempt.Namespace == "" || attempt.SessionIdentity == "" || attempt.SnapshotID <= 0 || attempt.CreatedAt.IsZero() || attempt.UpdatedAt.IsZero() || attempt.FinishedAt.IsZero() || attempt.FinishedAt.Before(attempt.CreatedAt) || attempt.LeaseExpiresAt.IsZero() || len(attempt.TerminationEvidence) != 0 {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay build attempt identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	marker, err := catalogartifact.DecodeCommitMarker(attempt.CommitMarker)
	if err != nil || marker.DeliveryID != outcome.OperationID || marker.GenerationID != outcome.GenerationID || marker.AttemptID != outcome.AttemptID || marker.LeaseEpoch != attempt.FencingEpoch || marker.RequestDigest != requestDigest || marker.PlanDigest != outcome.PlanDigest || marker.Project != request.ProjectID.String() || marker.Environment != request.Environment || marker.PhysicalPoolID != attempt.PhysicalPoolID {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay commit marker identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	lease, err := c.repository.Lease(ctx, outcome.LeaseID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	binding, err := c.repository.BuildArtifactBinding(ctx, outcome.AttemptID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if binding.AttemptID != outcome.AttemptID || binding.ServingStateID != outcome.GenerationID || binding.ServingArtifactDigest != outcome.ServingArtifactDigest || binding.ServingArtifactID != outcome.ServingArtifactID || binding.BoundAt.IsZero() {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay artifact identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	seal, err := c.repository.SnapshotSeal(ctx, outcome.SealID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if seal.SealID != outcome.SealID || seal.AttemptID != outcome.AttemptID || seal.CandidateID != outcome.CandidateID || seal.PhysicalPoolID != attempt.PhysicalPoolID || seal.DuckLakeSnapshotID != attempt.SnapshotID || seal.RelationNamespace != attempt.Namespace || seal.PlanDigest != outcome.PlanDigest || seal.RequestDigest != outcome.RequestDigest || seal.ServingArtifactID != outcome.ServingArtifactID || seal.ServingArtifactDigest != outcome.ServingArtifactDigest || seal.CatalogID == "" || seal.CatalogUUID == "" || seal.CatalogVersion <= 0 || seal.ObjectRoot == "" || seal.ArtifactRoot == "" || seal.RelationManifestDigest == "" || seal.ClosureDigest == "" || seal.CompatibilityDigest == "" || len(seal.QualificationEvidence) == 0 || seal.QualifiedAt.IsZero() {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay seal identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	var qualification NativeQualificationEvidence
	if err := strictjson.DecodeWithOptions(seal.QualificationEvidence, &qualification, strictjson.Options{MaxBytes: NativeQualificationMaxBytes}); err != nil || validateNativeQualificationEvidence(qualification) != nil || qualification.Digest != outcome.QualificationDigest || qualification.CandidateID != outcome.CandidateID || qualification.AttemptID != outcome.AttemptID || qualification.PhysicalPoolID != attempt.PhysicalPoolID || qualification.CatalogID != seal.CatalogID || qualification.SnapshotID != attempt.SnapshotID || qualification.ObjectRoot != seal.ObjectRoot || qualification.RelationNamespace != attempt.Namespace || qualification.RelationManifestDigest != seal.RelationManifestDigest || qualification.ClosureDigest != seal.ClosureDigest || qualification.Runtime.CompatibilityDigest != seal.CompatibilityDigest {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay qualification identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	generation, err := c.repository.Generation(ctx, outcome.GenerationID)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if generation.GenerationID != outcome.GenerationID || generation.TargetID != request.TargetID || generation.PlanID != outcome.PlanID || generation.CandidateID != outcome.CandidateID || generation.SnapshotSealID != outcome.SealID || generation.PlanDigest != outcome.PlanDigest || generation.ArtifactRoot != seal.ArtifactRoot || generation.ArtifactRootDigest != seal.ArtifactRootDigest || generation.CompiledGraphDigest != seal.CompiledGraphDigest || generation.CompiledConfigDigest != seal.CompiledConfigDigest || generation.SecurityDomainFingerprint != seal.SecurityDomainFingerprint || generation.ServingArtifactDigest != outcome.ServingArtifactDigest || generation.GenerationRevision <= 0 || generation.CreatedAt.IsZero() {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay generation identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	defer tx.Rollback(context.Background())
	payload, err := json.Marshal(nativeBuildEventPayload{OperationID: outcome.OperationID, ProjectID: outcome.ProjectID, ResourceID: outcome.GenerationID, Status: "sealed"})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	event, err := c.eventReader.GetDeliveryEvent(ctx, tx, deploymentmodule.NativeDeliveryEventInput{EventID: outcome.EventID, ScopeID: request.TargetID, AggregateType: "delivery_build", AggregateID: outcome.OperationID, EventType: "delivery.build.sealed", SchemaVersion: 1, CorrelationID: outcome.OperationID, Payload: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if event.EventID != outcome.EventID || event.ScopeID != request.TargetID || event.AggregateType != "delivery_build" || event.AggregateID != outcome.OperationID || event.EventType != "delivery.build.sealed" || event.SchemaVersion != 1 || event.CorrelationID != outcome.OperationID || event.AggregateVersion <= 0 || event.OccurredAt.IsZero() || !sameNativeJSON(event.Payload, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay event identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	audit, err := c.auditReader.GetMutationAudit(ctx, tx, deploymentmodule.NativeDeliveryAuditInput{AuditID: outcome.AuditID, DomainEventID: outcome.EventID, ScopeID: request.TargetID, ActorID: request.PrincipalID, Action: "delivery.build.sealed", ResourceKind: "build", ResourceID: outcome.OperationID, Outcome: "accepted", Operation: "build", RequestDigest: requestDigest, CorrelationID: outcome.OperationID, AggregateKey: event.AggregateID, AggregateSequence: event.AggregateVersion, Metadata: payload})
	if err != nil {
		return deploymentmodule.NativeDeliveryBuild{}, err
	}
	if audit.AuditID != outcome.AuditID || audit.EventID != outcome.EventID || audit.ScopeID != request.TargetID || audit.ActorID != request.PrincipalID || audit.Action != "delivery.build.sealed" || audit.ResourceKind != "build" || audit.ResourceID != outcome.OperationID || audit.Outcome != "accepted" || audit.RequestDigest != requestDigest || audit.OccurredAt.IsZero() || !sameNativeJSON(audit.Metadata, payload) {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay audit identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if lease.LeaseID != outcome.LeaseID || lease.TargetID != request.TargetID || lease.OwnerID != request.PrincipalID || lease.State != "released" || lease.FencingEpoch != attempt.FencingEpoch || lease.ExpiresAt.IsZero() || lease.AcquiredAt.IsZero() || lease.ReleasedAt.IsZero() {
		return deploymentmodule.NativeDeliveryBuild{}, fmt.Errorf("%w: replay lease identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	return nativeBuildProjection(outcome, plan.BaseGenerationID, attempt, lease, candidate.CandidateRevision)
}

func replayFailedNativeBuild(operation deploymentmodule.NativeOperationRecord, requestDigest string) error {
	if operation.AttemptID == "" {
		var evidence nativeBuildPreflightFailureEvidence
		if err := strictjson.DecodeWithOptions(operation.Outcome, &evidence, strictjson.Options{MaxBytes: 64 << 10}); err != nil {
			return fmt.Errorf("%w: failed native build preflight evidence is invalid", deploymentdomain.ErrDeliveryConflict)
		}
		if evidence.SchemaVersion != 1 || evidence.RequestDigest != requestDigest || evidence.Classification != NativePhysicalFailureDeterministic || evidence.Phase != NativePhysicalBuildPhaseValidation || platformdigest.ValidateSHA256Identity(evidence.PlanDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.ErrorDigest) != nil {
			return fmt.Errorf("%w: failed native build preflight evidence identity differs", deploymentdomain.ErrDeliveryConflict)
		}
		return fmt.Errorf("%w: native build preflight previously failed (%s)", deploymentdomain.ErrDeliveryConflict, evidence.ErrorDigest)
	}
	var evidence nativeBuildTerminationEvidence
	if err := strictjson.DecodeWithOptions(operation.Outcome, &evidence, strictjson.Options{MaxBytes: 64 << 10}); err != nil {
		return fmt.Errorf("%w: failed native build evidence is invalid", deploymentdomain.ErrDeliveryConflict)
	}
	if evidence.SchemaVersion != 1 || evidence.AttemptID != operation.AttemptID || evidence.RequestDigest != requestDigest || evidence.Classification != NativePhysicalFailureDeterministic || evidence.FencingEpoch <= 0 {
		return fmt.Errorf("%w: failed native build evidence identity differs", deploymentdomain.ErrDeliveryConflict)
	}
	if _, err := canonicalUUIDv7(evidence.AttemptID); err != nil || validateText(evidence.OwnerID, "attempt owner", 255) != nil || validateText(evidence.PhysicalPoolID, "physical pool", 255) != nil || validateText(evidence.Namespace, "namespace", 255) != nil || validateText(evidence.SessionIdentity, "session identity", 255) != nil || platformdigest.ValidateSHA256Identity(evidence.PlanDigest) != nil || platformdigest.ValidateSHA256Identity(evidence.ErrorDigest) != nil {
		return fmt.Errorf("%w: failed native build evidence is incomplete", deploymentdomain.ErrDeliveryConflict)
	}
	return fmt.Errorf("%w: native build previously failed (%s)", deploymentdomain.ErrDeliveryConflict, evidence.ErrorDigest)
}

// CompleteNativeBuildCommand rereads all durable consequences for generated
// command guards. It never invokes physical execution.
func (c *NativeBuildCoordinator) CompleteNativeBuildCommand(ctx context.Context, build deploymentmodule.NativeDeliveryBuild) error {
	if c == nil || c.repository == nil || c.operationLookup == nil {
		return deploymentmodule.ErrDeliveryInputUnavailable
	}
	if build.ID == uuid.Nil || build.PlanID == uuid.Nil || build.EventID == uuid.Nil || build.AuditID == uuid.Nil {
		return fmt.Errorf("%w: native build completion evidence is incomplete", deploymentdomain.ErrDeliveryConflict)
	}
	planRow, err := c.repository.Plan(ctx, build.PlanID.String())
	if err != nil {
		return err
	}
	plan, err := planRow.RichPlan()
	if err != nil {
		return err
	}
	request := deploymentmodule.NativeDeliveryBuildRequest{ProjectID: projectgraph.ResourceID(plan.ProjectID), TargetID: plan.TargetID, Environment: plan.Environment, PlanID: build.PlanID, PrincipalID: build.ActorID, IdempotencyKey: build.IdempotencyKey}
	requestDigest, err := nativeBuildRequestDigest(request)
	if err != nil || requestDigest != build.RequestDigest {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: native build request digest differs", deploymentdomain.ErrDeliveryConflict)
	}
	if build.OperationOwnerID == "" {
		return fmt.Errorf("%w: native build operation owner is missing", deploymentdomain.ErrDeliveryConflict)
	}
	if _, err := canonicalUUIDv7(build.OperationOwnerID); err != nil {
		return fmt.Errorf("%w: native build operation owner is invalid", deploymentdomain.ErrDeliveryConflict)
	}
	operation, found, err := c.operationLookup.Lookup(ctx, deploymentmodule.NativeOperationAcquireInput{Scope: plan.TargetID, OperationType: nativeBuildOperationType, IdempotencyKey: build.IdempotencyKey, RequestDigest: build.RequestDigest, OwnerID: build.OperationOwnerID})
	if err != nil || !found || operation.OperationID != build.OperationID.String() || operation.AttemptID != build.ID.String() || operation.OwnerID != build.OperationOwnerID {
		return fmt.Errorf("%w: persisted native build operation is unavailable", deploymentdomain.ErrDeliveryConflict)
	}
	replayed, err := c.replayBuild(ctx, request, build.RequestDigest, operation)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(replayed, build) {
		return fmt.Errorf("%w: native build completion projection differs from durable evidence", deploymentdomain.ErrDeliveryConflict)
	}
	return nil
}
