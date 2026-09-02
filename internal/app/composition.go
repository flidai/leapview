package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	"github.com/flidai/leapview/internal/analytics/candidatecatalog"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsgates "github.com/flidai/leapview/internal/analytics/gates"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolsqlite "github.com/flidai/leapview/internal/analytics/physicalpool/sqlite"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/desktopdiscovery"
	"github.com/flidai/leapview/internal/app/gcadapter"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapiadapter "github.com/flidai/leapview/internal/deployment/apiadapter"
	"github.com/flidai/leapview/internal/deployment/gcstore"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	"github.com/flidai/leapview/internal/extension"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/platform/filesystem"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// projectCatalogLeaseProvider narrows the runtime-host provider to the
// catalog lease contract while preserving the exact active lease object. No
// graph or authorization snapshot is cached here.
type projectCatalogLeaseProvider struct {
	provider runtimehostmodule.Provider
}

type projectCatalogSubjectResolver struct {
	resolve func(context.Context, string) ([]access.SubjectRef, error)
}

type sealedDeliveryAuthorizationReader interface {
	DeliveryCandidateByID(context.Context, string) (deployment.DeliveryCandidate, error)
	PlanByID(context.Context, string) (deployment.DeliveryPlan, error)
}

type canonicalPublishReader interface {
	sealedDeliveryAuthorizationReader
	DeliveryCatalogSealByID(context.Context, string) (deployment.CatalogSeal, error)
}

type deliveryTargetReader interface {
	DeliveryTargetRevision(context.Context, string) (deployment.DeliveryTarget, error)
}

func verifyCanonicalDeliveryTarget(ctx context.Context, reader deliveryTargetReader, targetID, projectID, environment, generationID string, revision int64) error {
	if reader == nil || strings.TrimSpace(targetID) == "" || strings.TrimSpace(projectID) == "" || strings.TrimSpace(environment) == "" || strings.TrimSpace(generationID) == "" || revision <= 0 {
		return fmt.Errorf("canonical delivery target evidence is incomplete")
	}
	target, err := reader.DeliveryTargetRevision(ctx, targetID)
	if err != nil {
		return err
	}
	if target.TargetID != targetID || target.ProjectID != projectID || target.Environment != environment || target.ActiveGenerationID != generationID || target.TargetRevision != revision {
		return fmt.Errorf("%w: canonical delivery target no longer points to generation %q at revision %d", deployment.ErrDeliveryConflict, generationID, revision)
	}
	return nil
}

func sourceInputsFromManifest(artifacts release.CandidateArtifactSet, runtime any) []analyticsgates.SourceInput {
	observed := map[string]analyticsmaterialize.SourceObservation{}
	if reader, ok := runtime.(interface {
		SourceObservations() []analyticsmaterialize.SourceObservation
	}); ok {
		for _, item := range reader.SourceObservations() {
			canonicalID := canonicalSourceObservationID(artifacts.Compiler.Manifest.NameIndex.Sources, item.ID)
			item.ID = canonicalID
			observed[canonicalID] = item
		}
	}
	if len(observed) == 0 && artifacts.Generation.BaseGateEvidence != nil {
		base := artifacts.Generation.BaseGateEvidence
		for _, item := range base.Sources {
			source, ok := artifacts.Compiler.Manifest.Sources[item.ID]
			if !ok {
				continue
			}
			// Base observations are immutable evidence reused for schema/freshness
			// comparison; they are not work performed by this candidate. Do not
			// charge their historical query/row/time totals to the new gate budget.
			observation := analyticsmaterialize.SourceObservation{ID: item.ID, Schema: append([]semanticmodel.ColumnSchema(nil), item.ObservedSchema...), FreshnessObserved: item.ObservedAt}
			if source.Freshness != nil && source.Freshness.Basis == "revision" {
				observation.Revision = source.Freshness.Revision
				observation.RevisionObserved = item.ObservedAt
			}
			observation.FreshnessEmpty = item.FreshnessOutcome == release.GateEmpty
			observed[item.ID] = observation
		}
	}
	result := make([]analyticsgates.SourceInput, 0, len(artifacts.Compiler.Manifest.Sources))
	for id, source := range artifacts.Compiler.Manifest.Sources {
		item := observed[id]
		result = append(result, analyticsgates.SourceInput{ID: id, Source: source, Observed: append([]semanticmodel.ColumnSchema(nil), item.Schema...), Revision: item.Revision, RevisionObserved: item.RevisionObserved, FreshnessObserved: item.FreshnessObserved, FreshnessEmpty: item.FreshnessEmpty, SchemaFailure: item.SchemaFailure, FreshnessFailure: item.FreshnessFailure, ObservationQueries: item.ObservationQueries, ObservationRows: item.ObservationRows, ObservationMillis: item.ObservationMillis})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func canonicalSourceObservationID(sources map[string]string, observationID string) string {
	for authoredName, canonicalID := range sources {
		if projectmodule.RuntimeSourceAlias(authoredName) == observationID {
			return canonicalID
		}
	}
	return observationID
}

// deliveryMaterializationDelta maps compiler model-resource changes to the
// concrete semantic datasets that can be refreshed independently. A
// source or unknown dependency change conservatively refreshes the complete
// project; missing relation identity never silently inherits stale state.
func deliveryMaterializationDelta(artifacts release.CandidateArtifactSet, deliveryPlan deployment.DeliveryPlan) (map[string][]string, []string, bool) {
	changedByModel := make(map[string]map[string]struct{})
	removed := make(map[string]struct{})
	refreshAll := false
	resourceNames := make(map[string]string)
	for _, resource := range artifacts.Compiler.Graph.Resources() {
		resourceNames[resource.ID.String()] = resource.Name
	}
	for _, change := range artifacts.Compiler.Plan.Changes {
		if !change.MaterializationImpact {
			continue
		}
		if change.Type != string(projectgraph.KindModel) {
			refreshAll = true
			continue
		}
		name := strings.TrimSpace(change.Key)
		if name == "" {
			name = resourceNames[change.ID]
		}
		if name == "" {
			refreshAll = true
			continue
		}
		if change.Action == "remove" {
			removed[name] = struct{}{}
			continue
		}
		for modelID, model := range artifacts.Compiler.Artifact.Models() {
			if model == nil {
				continue
			}
			if _, ok := model.Tables[name]; !ok {
				continue
			}
			if changedByModel[modelID] == nil {
				changedByModel[modelID] = make(map[string]struct{})
			}
			changedByModel[modelID][name] = struct{}{}
		}
	}
	for _, dependency := range artifacts.Compiler.Plan.DependencyChanges {
		if !dependency.MaterializationImpact {
			continue
		}
		name := resourceNames[dependency.To]
		if name == "" {
			refreshAll = true
			continue
		}
		found := false
		for modelID, model := range artifacts.Compiler.Artifact.Models() {
			if model == nil {
				continue
			}
			if _, ok := model.Tables[name]; !ok {
				continue
			}
			if changedByModel[modelID] == nil {
				changedByModel[modelID] = make(map[string]struct{})
			}
			changedByModel[modelID][name] = struct{}{}
			found = true
		}
		if !found {
			refreshAll = true
		}
	}
	// A physical identity mismatch (runtime, binding, pinned input, or
	// compiler context) may not appear as a graph change. Every non-reusable
	// relation decision is therefore an explicit refresh target; unknown IDs
	// fail closed to a full refresh rather than inheriting stale files.
	for _, decision := range deliveryPlan.Evidence.Reuse {
		if decision.Reusable {
			continue
		}
		name := resourceNames[decision.ResourceID]
		if name == "" {
			refreshAll = true
			continue
		}
		found := false
		for modelID, model := range artifacts.Compiler.Artifact.Models() {
			if model == nil {
				continue
			}
			if _, ok := model.Tables[name]; !ok {
				continue
			}
			if changedByModel[modelID] == nil {
				changedByModel[modelID] = make(map[string]struct{})
			}
			changedByModel[modelID][name] = struct{}{}
			found = true
		}
		if !found {
			refreshAll = true
		}
	}
	// A generation-bound pipeline restatement is intentionally scoped even
	// when compiler materialization impact is false. Add its authored model
	// table closure as explicit refresh targets while retaining every unrelated
	// relation from the sealed base.
	if deliveryPlan.PipelinePlan != nil {
		for _, selected := range deliveryPlan.PipelinePlan.MaterializationScope {
			matched := false
			for modelID, model := range artifacts.Compiler.Artifact.Models() {
				if model == nil {
					continue
				}
				aliases := make([]string, 0, 1)
				for alias, table := range model.Tables {
					if alias == selected || table.ModelName == selected {
						aliases = append(aliases, alias)
					}
				}
				if len(aliases) == 0 {
					continue
				}
				matched = true
				if changedByModel[modelID] == nil {
					changedByModel[modelID] = make(map[string]struct{})
				}
				for _, alias := range aliases {
					changedByModel[modelID][alias] = struct{}{}
				}
			}
			if !matched {
				refreshAll = true
			}
		}
	}
	if len(changedByModel) == 0 && len(removed) == 0 && artifacts.Compiler.Plan.Summary.MaterializationImpact {
		refreshAll = true
	}
	result := make(map[string][]string, len(changedByModel))
	for modelID, names := range changedByModel {
		values := make([]string, 0, len(names))
		for name := range names {
			values = append(values, name)
		}
		sort.Strings(values)
		result[modelID] = values
	}
	removedNames := make([]string, 0, len(removed))
	for name := range removed {
		removedNames = append(removedNames, name)
	}
	sort.Strings(removedNames)
	return result, removedNames, refreshAll
}

// sealedPublicationBootstrapDecision is kept separate from the coordinator
// closure so the first-activation fence is directly testable. A durable active
// generation wins a race and returns unhandled, forcing the caller through the
// live snapshot authorizer. While the durable target is still fresh, an exact
// APIGen marker or a worker binding backed by the revalidated bootstrap policy
// may authorize the publication.
func sealedPublicationBootstrapDecision(
	ctx context.Context,
	binding sealedcontrol.SealBinding,
	marker accessmodule.BootstrapAuthorization,
	marked bool,
	active func(context.Context) (bool, error),
) (bool, error) {
	if binding.Operation != "publish" {
		return false, nil
	}
	if active == nil {
		return false, fmt.Errorf("sealed publication active-generation check is required")
	}
	isActive, err := active(ctx)
	if err != nil {
		return false, err
	}
	if isActive {
		return false, nil
	}
	if binding.Bootstrap {
		// Async activation workers do not retain the original HTTP request
		// context. The deployment worker may set Bootstrap only after the
		// durable one-shot policy has been revalidated; the active-generation
		// check above still wins any concurrent first-activation race.
		if marked && (marker.PrincipalID != binding.ActorID || marker.ProjectID.String() != binding.ProjectID || marker.Capability != access.CapabilityResourcePublish) {
			return true, fmt.Errorf("sealed publication bootstrap authorization is missing or mismatched")
		}
		return true, nil
	}
	if !marked || marker.PrincipalID != binding.ActorID || marker.ProjectID.String() != binding.ProjectID || marker.Capability != access.CapabilityResourcePublish {
		return true, fmt.Errorf("sealed publication bootstrap authorization is missing or mismatched")
	}
	return true, nil
}

// activateCanonicalServingState prepares the exact serving-state generation
// before invoking the delivery CAS callback, then cuts over the in-process
// sealed runtime only after that callback succeeds. This is the canonical
// publish/rollback equivalent of the legacy prepared-runtime activation fence:
// a prepare failure leaves the target untouched and a CAS failure aborts the
// prepared runtime without exposing it to readers.
type sealedRuntimeActivator interface {
	PrepareServingState(context.Context, string) (*runtimehost.Prepared, error)
	ActivatePreparedContext(context.Context, *runtimehost.Prepared, func() error) error
}

type sealedRuntimeActiveReader interface {
	ActiveArtifact(context.Context) (servingstate.State, servingstate.Artifact, error)
}

type sealedRuntimeLeaseReader interface {
	Acquire(context.Context) (runtimehost.Lease, error)
}

func activateCanonicalServingState(ctx context.Context, runtime sealedRuntimeActivator, generationID string, activate func() error) error {
	generationID = strings.TrimSpace(generationID)
	if runtime == nil || generationID == "" || activate == nil {
		return fmt.Errorf("canonical sealed runtime, generation, and activation callback are required")
	}
	if leaseReader, ok := runtime.(sealedRuntimeLeaseReader); ok {
		lease, err := leaseReader.Acquire(ctx)
		if err == nil && lease != nil {
			activeGenerationID := lease.Identity().GenerationID
			lease.Release()
			if activeGenerationID == generationID {
				return activate()
			}
		}
	}
	if activeReader, ok := runtime.(sealedRuntimeActiveReader); ok {
		active, _, err := activeReader.ActiveArtifact(ctx)
		switch {
		case err == nil && string(active.ID) == generationID:
			return activate()
		case err != nil && !errors.Is(err, servingstate.ErrNotFound):
			return fmt.Errorf("resolve active canonical sealed serving state: %w", err)
		}
	}
	prepared, err := runtime.PrepareServingState(ctx, generationID)
	if err != nil {
		return fmt.Errorf("prepare canonical sealed serving state %q: %w", generationID, err)
	}
	if err := runtime.ActivatePreparedContext(ctx, prepared, activate); err != nil {
		return fmt.Errorf("activate canonical sealed serving state %q: %w", generationID, err)
	}
	return nil
}

// authorizeSealedPublication is the lower control-plane authorization boundary
// used by the sealed coordinator and the generated publication route. Publish
// authorization is bound to the exact candidate -> plan tuple and every
// graph-impact resource in that immutable plan; a project role is only a
// fallback for plans that explicitly carry no impact resources.

func authorizeSealedPublication(
	ctx context.Context,
	binding sealedcontrol.SealBinding,
	targetID string,
	delivery sealedDeliveryAuthorizationReader,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
) error {
	if binding.ActorID == "" || binding.CandidateID == "" || binding.GenerationID == "" || binding.ProjectID == "" || binding.Environment == "" || binding.TargetID != targetID {
		return fmt.Errorf("sealed publication actor and root scope are required")
	}
	requestedProject, err := projectgraph.NewResourceID(binding.ProjectID)
	if err != nil {
		return fmt.Errorf("sealed publication project: %w", err)
	}
	capability := access.CapabilityProjectAdmin
	if binding.Operation == "publish" {
		capability = access.CapabilityResourcePublish
	}
	if binding.Operation == "publish" {
		if delivery == nil {
			return fmt.Errorf("sealed publication delivery reader is required")
		}
		candidate, candidateErr := delivery.DeliveryCandidateByID(ctx, binding.CandidateID)
		if candidateErr != nil || candidate.ProjectID != requestedProject || candidate.TargetID != binding.TargetID || candidate.PlanDigest != binding.PlanDigest {
			if candidateErr != nil {
				return fmt.Errorf("sealed publication candidate lookup: %w", candidateErr)
			}
			return fmt.Errorf("sealed publication candidate scope does not match reviewed plan")
		}
		plan, planErr := delivery.PlanByID(ctx, candidate.PlanID)
		if planErr != nil {
			return fmt.Errorf("sealed publication plan lookup: %w", planErr)
		}
		if plan.ProjectID != requestedProject || plan.TargetID != binding.TargetID || plan.Environment != binding.Environment || plan.Digest != binding.PlanDigest {
			return fmt.Errorf("sealed publication plan scope does not match reviewed plan")
		}
		resources, resourceErr := deliveryAuthorizationResources(plan)
		if resourceErr != nil {
			return fmt.Errorf("sealed publication graph impact: %w", resourceErr)
		}
		if requestLocalDevelopmentAuthorization(ctx, binding.ActorID) {
			return nil
		}
		var allowed bool
		if len(resources) == 0 {
			allowed, err = authorizeProjectRole(ctx, accessModule, runtimeHost, binding.ActorID, requestedProject, capability)
		} else {
			allowed, err = authorizeDeliveryProjectResources(ctx, accessModule, runtimeHost, binding.ActorID, requestedProject, resources, capability)
		}
		if err != nil {
			return fmt.Errorf("sealed publication live authorization: %w", err)
		}
		if !allowed {
			return fmt.Errorf("sealed publication live authorization denied")
		}
		return nil
	}
	resource, err := access.NewResourceRef(requestedProject, projectgraph.KindProject)
	if err != nil {
		return fmt.Errorf("sealed publication resource: %w", err)
	}
	if requestLocalDevelopmentAuthorization(ctx, binding.ActorID) {
		return nil
	}
	allowed, err := authorizeProjectResources(ctx, accessModule, runtimeHost, binding.ActorID, requestedProject, []access.ResourceRef{resource}, capability)
	if err != nil {
		return fmt.Errorf("sealed publication live authorization: %w", err)
	}
	if !allowed {
		return fmt.Errorf("sealed publication live authorization denied")
	}
	return nil
}

func requestLocalDevelopmentAuthorization(ctx context.Context, actorID string) bool {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	return ok && principal.DevBypass && strings.TrimSpace(principal.ID) == strings.TrimSpace(actorID)
}

func productionRecoveryLifecycle(cfg config.Config, _ buildinfo.Identity, _, _ string) (*refreshmodule.RecoveryLifecycle, error) {
	if !cfg.Production || !cfg.RecoveryQualificationEnabled {
		return nil, nil
	}
	executionEnvironment := strings.TrimSpace(cfg.RecoveryQualificationExecutionEnvironment)
	if executionEnvironment == "" || executionEnvironment == "host" {
		return nil, nil
	}
	return nil, fmt.Errorf("unsupported recovery qualification execution environment %q; released composition requires host", executionEnvironment)
}

// BuildProductionRecoveryLifecycle builds the owner-validated lifecycle for a
// supported execution host. The web composition delegates to the installed
// host controller by default so Docker authority is not exposed to the server.
func BuildProductionRecoveryLifecycle(cfg config.Config, build buildinfo.Identity, environment, instanceID string) (*refreshmodule.RecoveryLifecycle, error) {
	return BuildProductionRecoveryLifecycleWithContainerRuntime(cfg, build, environment, instanceID, "docker")
}

// BuildProductionRecoveryLifecycleWithContainerRuntime binds the lifecycle to
// the exact host container runtime selected by the installed controller.
func BuildProductionRecoveryLifecycleWithContainerRuntime(cfg config.Config, build buildinfo.Identity, environment, instanceID, containerRuntime string) (*refreshmodule.RecoveryLifecycle, error) {
	if build.Development || build.Dirty || build.Version == buildinfo.DevelopmentVersion || build.Revision == buildinfo.UnknownValue {
		return nil, fmt.Errorf("scheduled recovery qualification requires exact released build provenance")
	}
	const managedPolicyPath = "/run/leapview/release-transition-policy.json"
	policyPath := filepath.Join(strings.TrimSpace(cfg.RecoveryQualificationBundle), "release-transition-policy.json")
	if strings.TrimSpace(cfg.RecoveryQualificationBundle) == "" {
		policyPath = managedPolicyPath
	}
	if _, statErr := os.Stat(policyPath); os.IsNotExist(statErr) && policyPath != managedPolicyPath {
		policyPath = managedPolicyPath
	} else if statErr != nil {
		return nil, statErr
	}
	policy, policyDocument, err := compatibility.LoadPolicy(policyPath)
	if err != nil {
		return nil, fmt.Errorf("load managed recovery qualification policy: %w", err)
	}
	policyDigest := sha256.Sum256(policyDocument)
	policySHA256 := hex.EncodeToString(policyDigest[:])
	template, err := compatibility.EmbeddedCandidateTransitionTemplate()
	if err != nil {
		return nil, err
	}
	predecessor, ok := policy.ReleaseByID(template.PredecessorRelease)
	if !ok {
		return nil, fmt.Errorf("scheduled recovery qualification predecessor %q is absent from the managed policy", template.PredecessorRelease)
	}
	platformName := runtime.GOOS + "/" + runtime.GOARCH
	releaseID := "v" + strings.TrimPrefix(build.Version, "v")
	releaseIdentity := compatibility.ReleaseIdentity{
		ReleaseID: releaseID, Version: strings.TrimPrefix(build.Version, "v"), SourceRevision: build.Revision,
		Image: strings.TrimSpace(cfg.Image), Distribution: "public", Platform: platformName,
	}
	admittedRelease, ok := policy.ReleaseByID(releaseID)
	if !ok || admittedRelease.IdentityForPlatform(platformName) != releaseIdentity {
		return nil, fmt.Errorf("scheduled recovery qualification requires the managed policy bound to the running immutable release")
	}
	workRoot := strings.TrimSpace(cfg.RecoveryQualificationWorkDir)
	if workRoot == "" {
		workRoot = filepath.Join(os.TempDir(), "leapview-recovery-qualification-"+instanceID)
	}
	workRoot, err = filepath.Abs(workRoot)
	if err != nil {
		return nil, err
	}
	evidenceRoot, err := filepath.Abs(filepath.Join(cfg.ArtifactDir(), "recovery-qualification"))
	if err != nil {
		return nil, err
	}
	for _, directory := range []string{workRoot, evidenceRoot} {
		if err := securefs.EnsurePrivateDir(directory); err != nil {
			return nil, err
		}
	}
	storageEvidence := productionRecoveryStorageEvidence(cfg)
	initialStorage, err := storageEvidence(context.Background())
	if err != nil {
		return nil, err
	}
	qualification := refreshmodule.ProductionRecoveryQualificationConfig{
		HomeDir: cfg.HomeDir, DBPath: cfg.DBPath(), InstanceID: instanceID, Environment: environment,
		BuildIdentity:   build,
		ReleaseIdentity: releaseIdentity, StorageTopology: initialStorage.Topology, StorageEvidence: storageEvidence,
		TransitionPolicy: policy, PolicySHA256: policySHA256, WorkRoot: workRoot, EvidenceRoot: evidenceRoot,
		ControllerPath: cfg.RecoveryQualificationController, BundleRoot: cfg.RecoveryQualificationBundle,
		ContainerRuntime: containerRuntime,
		PredecessorImage: predecessor.IdentityForPlatform(platformName).Image,
		Cron:             cfg.RecoveryQualificationCron, Timezone: "UTC", StaleAfter: 36 * time.Hour,
	}
	return refreshmodule.NewProductionRecoveryLifecycle(qualification), nil
}

func productionRecoveryStorageEvidence(cfg config.Config) refreshmodule.RecoveryStorageEvidenceProvider {
	return func(context.Context) (refreshmodule.RecoveryStorageQualificationEvidence, error) {
		var points []adminmodule.ExternalRecoveryPoint
		evidence := map[string]string{}
		if strings.TrimSpace(cfg.ManagedDataBackend) == "s3" {
			if err := readRecoveryQualificationJSON(cfg.RecoveryQualificationExternalRecoveryPoints, &points); err != nil {
				return refreshmodule.RecoveryStorageQualificationEvidence{}, fmt.Errorf("read scheduled external recovery points: %w", err)
			}
			if err := readRecoveryQualificationJSON(cfg.RecoveryQualificationExternalEvidence, &evidence); err != nil {
				return refreshmodule.RecoveryStorageQualificationEvidence{}, fmt.Errorf("read scheduled external recovery evidence: %w", err)
			}
		}
		topology, err := adminmodule.BuildRecoveryStorageTopology(adminmodule.RecoveryStorageConfig{
			ManagedDataBackend: cfg.ManagedDataBackend, ManagedDataS3Endpoint: cfg.ManagedDataS3Endpoint,
			ManagedDataS3Region: cfg.ManagedDataS3Region, ManagedDataS3Bucket: cfg.ManagedDataS3Bucket,
			ManagedDataS3Prefix: cfg.ManagedDataS3Prefix,
		}, points, true)
		if err != nil {
			return refreshmodule.RecoveryStorageQualificationEvidence{}, err
		}
		return refreshmodule.RecoveryStorageQualificationEvidence{
			Topology: topology, ExternalEvidence: evidence,
		}, nil
	}
}

func readRecoveryQualificationJSON(path string, destination any) error {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("an absolute JSON evidence path is required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		return fmt.Errorf("JSON evidence must be a regular non-symlink file no larger than 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() > 1<<20 || !os.SameFile(info, openedInfo) {
		return fmt.Errorf("JSON evidence path changed before validation")
	}
	document, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(document) > 1<<20 {
		return fmt.Errorf("JSON evidence must be no larger than 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON evidence contains trailing data")
	}
	return nil
}

func (r projectCatalogSubjectResolver) AuthorizationSubjects(ctx context.Context, principalID string) ([]access.SubjectRef, error) {
	if r.resolve == nil {
		return nil, projectcatalog.ErrUnavailable
	}
	return r.resolve(ctx, principalID)
}

func (p projectCatalogLeaseProvider) Acquire(ctx context.Context) (projectcatalog.Lease, error) {
	if p.provider == nil {
		return nil, projectcatalog.ErrUnavailable
	}
	lease, err := p.provider.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	catalogLease, ok := lease.(projectcatalog.Lease)
	if !ok {
		lease.Release()
		return nil, fmt.Errorf("runtime lease does not expose catalog authorization snapshot")
	}
	return catalogLease, nil
}

// assemble constructs the complete process exactly once. CLI and other process
// entrypoints provide configuration but never construct capability adapters.
func assemble(ctx context.Context, cfg config.Config) (http.Handler, Lifecycle, cleanupFunc, error) {
	production := cfg.Production
	environment := servingstatemodule.NormalizeEnvironment(servingstatemodule.Environment(cfg.Environment))
	if strings.TrimSpace(cfg.Environment) == "" {
		if production {
			environment = servingstatemodule.Environment("prod")
		} else {
			environment = servingstatemodule.DefaultEnvironment
		}
	}
	return buildRuntime(ctx, cfg, production, environment)
}

func buildRuntime(ctx context.Context, cfg config.Config, production bool, environment servingstatemodule.Environment) (http.Handler, Lifecycle, cleanupFunc, error) {
	assets := applicationAssets(cfg, production)
	dashboardAssets, err := dashboardmodule.BuildAssets(ctx, cfg.MapAssetDir)
	if err != nil {
		return nil, nil, nil, err
	}
	cookieSecure, err := cfg.CookieSecure()
	if err != nil {
		return nil, nil, nil, err
	}
	var allowedHosts []string
	if production {
		allowedHosts, err = cfg.ProductionAllowedHosts()
	} else {
		allowedHosts, err = cfg.AllowedHostList()
	}
	if err != nil {
		return nil, nil, nil, err
	}
	duckLakeCatalogPath := cfg.DuckLakeCatalogPath()
	for _, dir := range []string{cfg.HomeDir, cfg.ArtifactDir(), cfg.DuckDBDirPath(), cfg.RuntimeDir(), cfg.DuckLakeDataDir(), filepath.Dir(duckLakeCatalogPath)} {
		if err := securefs.EnsurePrivateDir(dir); err != nil {
			return nil, nil, nil, err
		}
	}
	store, err := platform.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup := &cleanupStack{}
	cleanup.Push("sqlite", func(context.Context) error { return store.Close() })
	fail := func(err error) (http.Handler, Lifecycle, cleanupFunc, error) {
		cleanupErr := cleanup.Close(context.WithoutCancel(ctx))
		return nil, nil, nil, errors.Join(err, cleanupErr)
	}
	auditRuntime, err := newAuditRuntime(store.SQLDB())
	if err != nil {
		return fail(fmt.Errorf("build access audit runtime: %w", err))
	}
	if err := store.BindInstanceEnvironment(ctx, string(environment)); err != nil {
		return fail(err)
	}
	extensionSupply, err := loadExtensionSupply(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	candidateSources, err := projectmodule.NewCandidateSourceSynchronizer(
		filepath.Join(cfg.ArtifactDir(), "candidate-sources"),
	)
	if err != nil {
		return fail(err)
	}
	instanceID, err := store.InstanceID(ctx)
	if err != nil {
		return fail(err)
	}
	servingStateRepo, err := servingstatemodule.Build(ctx, servingstatemodule.Config{Database: store.SQLDB()})
	if err != nil {
		return fail(err)
	}
	projectClaimRepository, err := deploymentmodule.NewBootstrapPersistence(store.SQLDB())
	if err != nil {
		return fail(err)
	}
	// Every authorization callback reads the claim afresh. The runtime host
	// uses this same reader during startup, while bootstrap decisions must not
	// rely on a stale memoized claim after a concurrent first operation.
	readClaim := readClaimedProject(projectClaimRepository, environment)
	claimedProjectID, claimFound, err := readClaim(ctx)
	if err != nil {
		return fail(err)
	}
	activeScopes, err := servingStateRepo.ListActiveScopes(ctx)
	if err != nil {
		return fail(err)
	}
	projectID, err := resolveClaimedProjectID(activeScopes, environment, claimedProjectID, claimFound)
	if err != nil {
		return fail(err)
	}
	publicURL := firstConfigured(cfg.PublicURL, configuredListenURL(cfg.ListenAddr()))
	workloadConfig := cfg.WorkloadConfig()
	credentialMode := analyticsmodule.CredentialModeNonSecret
	if !production {
		credentialMode = analyticsmodule.CredentialModeDevelopmentEnvironment
	}
	analyticsBundle, err := buildAnalyticsCapability(ctx, analyticsCapabilityConfig{
		Database: store.SQLDB(), AuditIntentRecorder: auditRuntime.recorder, CredentialMode: credentialMode,
		CredentialTarget: instanceID, CredentialProject: projectID, Environment: string(environment),
		TargetCredentials: analyticsmodule.TargetCredentialConfig{
			InfisicalBaseURL: cfg.InfisicalBaseURL, InfisicalUniversalClientID: cfg.InfisicalUniversalClientID,
			InfisicalUniversalClientSecret: cfg.InfisicalUniversalClientSecret, InfisicalAllowedScopes: cfg.InfisicalAllowedScopes,
		},
		RootDir: cfg.DuckDBDirPath(), ExtensionSupply: extensionSupply,
		CatalogPath: duckLakeCatalogPath, DataPath: cfg.DuckLakeDataDir(),
		MaxConnections: workloadConfig.MaxRunning, MemoryMaxBytes: cfg.DuckDBNodeMemoryMaxBytes,
		TempMaxBytes: cfg.DuckDBNodeTempMaxBytes, MaxThreads: cfg.DuckDBNodeMaxThreads, TempDir: cfg.DuckDBTempDirPath(),
		DisableProcessEnv: production,
		RuntimeCacheItems: cfg.QueryCacheRuntimeMaxEntries, RuntimeCacheBytes: cfg.QueryCacheRuntimeMaxBytes,
		NodeCacheItems: cfg.QueryCacheNodeMaxEntries, NodeCacheBytes: cfg.QueryCacheNodeMaxBytes,
	})
	if err != nil {
		return fail(err)
	}
	analyticsModule := analyticsBundle.Module
	cleanup.Push("analytics", func(context.Context) error { return analyticsModule.Close() })
	avatarBlobs, err := profileImageBlobStore(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	productLogoBlobs, err := productLogoBlobStore(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	productService, err := adminmodule.NewProductService(store.SQLDB(), productLogoBlobs)
	if err != nil {
		return fail(err)
	}
	var runtimeHostModule *runtimehostmodule.Module
	currentProjectID := func(ctx context.Context) (projectgraph.ResourceID, error) {
		if runtimeHostModule == nil {
			return "", fmt.Errorf("runtime host is unavailable")
		}
		lease, err := runtimeHostModule.Acquire(ctx)
		if err != nil {
			return "", err
		}
		defer lease.Release()
		projectID := lease.Identity().ProjectID
		if err := projectID.Validate(); err != nil {
			return "", fmt.Errorf("active runtime project identity is invalid: %w", err)
		}
		return projectID, nil
	}
	accessBundle, err := buildAccessCapability(ctx, accessCapabilityConfig{
		Database: store.SQLDB(), Auth: accessAuthConfig(cfg, production, cookieSecure), Assets: assets, AvatarBlobs: avatarBlobs,
		PublicURL: publicURL, InstanceID: instanceID, MCPIssuerURL: cfg.MCPOAuthIssuerURL, CurrentProject: currentProjectID,
	})
	if err != nil {
		return fail(err)
	}
	accessModule := accessBundle.Module
	accessRepo := accessBundle.Repository
	authorizationInstaller := accessBundle.AuthorizationInstaller
	if !production {
		if err := accessModule.SeedLocalDeveloperPlatformAdmin(ctx); err != nil {
			return fail(err)
		}
	}
	workloadBundle, err := buildWorkloadCapability(ctx, workloadCapabilityConfig{Workload: workloadmodule.Config{Policy: workloadConfig}, Database: store.SQLDB(), LeaseTimeout: cfg.RefreshJobLeaseTimeout, Logger: slog.Default()})
	if err != nil {
		return fail(err)
	}
	workloadController := workloadBundle.Controller
	cleanup.Push("workload", func(context.Context) error {
		workloadController.Close()
		return nil
	})
	jobModule := workloadBundle.Jobs
	authorizationSnapshot := func(ctx context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
		if runtimeHostModule == nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("runtime host is unavailable")
		}
		lease, err := runtimeHostModule.Acquire(ctx)
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, err
		}
		defer lease.Release()
		authorizedLease, ok := lease.(interface {
			AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
		})
		if !ok {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("active runtime lease does not expose authorization snapshot")
		}
		snapshot := authorizedLease.AuthorizationSnapshot()
		if err := snapshot.ValidateBound(); err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, err
		}
		return snapshot, nil
	}
	sealedDelivery := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})
	resolveCurrentProjectID := func(ctx context.Context) (projectgraph.ResourceID, error) {
		claimed, found, err := readClaim(ctx)
		if err != nil {
			return "", fmt.Errorf("read live project claim: %w", err)
		}
		if found {
			return claimed, nil
		}
		return projectID, nil
	}
	snapshotAuthorizeConnection := accessmodule.ConnectionAuthorizerFromSnapshot(authorizationSnapshot, accessModule.AuthorizationSubjects)
	authorizeConnection := bootstrapAwareConnectionAuthorization(snapshotAuthorizeConnection, func(ctx context.Context) (bool, error) {
		currentProjectID, err := resolveCurrentProjectID(ctx)
		if err != nil {
			return false, err
		}
		return hasActiveBootstrapServingState(ctx, runtimeHostModule, servingStateRepo, string(environment), sealedDelivery, instanceID, currentProjectID.String())
	})
	managedDataModule, err := manageddatamodule.Build(ctx, manageddatamodule.Config{
		Database: store.SQLDB(), Product: managedDataProductConfig(cfg), ServingStates: servingStateRepo,
		Environment: string(environment),
		CurrentPrincipal: func(r *http.Request) (manageddatamodule.Principal, bool) {
			auth := accessModule.Auth()
			if auth == nil {
				return manageddatamodule.Principal{}, false
			}
			principal, ok := auth.Principal(r)
			return manageddatamodule.Principal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
		},
		AuthorizeConnection: manageddatamodule.ConnectionAuthorizer(authorizeConnection),
		Jobs:                jobModule, Workflow: jobModule,
		AuditIntentRecorder: auditRuntime.recorder,
		Worker: manageddatamodule.MaintenanceWorkerConfig{
			Interval: cfg.ManagedDataGCInterval,
			Acquire: func(ctx context.Context) (manageddatamodule.MaintenanceLease, error) {
				return workloadController.Acquire(ctx, workloadmodule.MaintenanceRequest("managed_data.collect"))
			},
			Logger: slog.Default(),
		},
	})
	if err != nil {
		return fail(err)
	}
	releaseModule, err := releasemodule.Build(ctx, releasemodule.Config{
		Database: store.SQLDB(), AuditIntentRecorder: auditRuntime.recorder,
		States:          servingStateRepo,
		ManagedDataPins: managedDataModule.BindingValidation(), ManagedDataHook: managedDataModule.BindingValidation(),
		ExtensionPreparation: extensionSupply,
		ArtifactDirectory:    cfg.ArtifactDir(), Environment: environment,
		API: releasemodule.APIConfig{
			CurrentPrincipal: func(r *http.Request) (releasemodule.Principal, bool) {
				auth := accessModule.Auth()
				if auth == nil {
					principal := accessmodule.LocalDeveloperPrincipal()
					return releasemodule.Principal{ID: principal.ID}, true
				}
				principal, ok := auth.Principal(r)
				return releasemodule.Principal{ID: principal.ID}, ok
			},
			AuthorizeConnection: snapshotAuthorizeConnection,
			Jobs:                jobModule, Workflow: jobModule,
		},
	})
	if err != nil {
		return fail(err)
	}
	activeRuntimeEvidence := activeConnectionEvidenceSource{
		releases: releaseModule, targetID: instanceID, environment: string(environment),
	}
	if err := analyticsModule.ConfigureActiveRuntimeBindings(activeRuntimeEvidence); err != nil {
		return fail(err)
	}
	managedDataResolution := managedDataModule.RuntimeResolution()
	if managedDataResolution == nil {
		return fail(errors.New("managed-data runtime resolver is required"))
	}
	managedDataResolver := appruntimefactory.NewManagedDataResolver(managedDataResolution)
	var sealedCoordinator *sealedcontrol.Coordinator
	var sealedPublishRequest deploymentmodule.SealedPublishRequestResolver
	var sealedRollbackRequest deploymentmodule.SealedRollbackRequestResolver
	var sealedRollbackFence func(context.Context, string) (string, int64, error)
	var sealedActiveState func(context.Context) (servingstate.ID, error)
	var deliveryStartupCheck func(context.Context) error
	{
		var beforePublicationCommit func(context.Context, deployment.PublicationIntent) error
		if string(environment) == "evaluation" {
			beforePublicationCommit = func(ctx context.Context, publication deployment.PublicationIntent) error {
				return sealedcontrol.QualificationActivationBarrier(ctx, publication.Environment)
			}
		}
		sealedCoordinator = &sealedcontrol.Coordinator{
			Publications: sealedDelivery, Rollbacks: sealedDelivery,
			BeforePublicationCommit: beforePublicationCommit,
			Authorize: func(ctx context.Context, binding sealedcontrol.SealBinding) error {
				if binding.Operation == "publish" {
					marker, marked := accessmodule.BootstrapAuthorizationFromContext(ctx)
					handled, decisionErr := sealedPublicationBootstrapDecision(ctx, binding, marker, marked, func(activeCtx context.Context) (bool, error) {
						return hasActiveBootstrapServingState(activeCtx, runtimeHostModule, servingStateRepo, string(environment), sealedDelivery, instanceID, binding.ProjectID)
					})
					if decisionErr != nil {
						return decisionErr
					}
					if handled {
						return nil
					}
				}
				return authorizeSealedPublication(ctx, binding, instanceID, sealedDelivery, accessModule, runtimeHostModule)
			},
			VerifySeal: func(ctx context.Context, binding sealedcontrol.SealBinding) error {
				slog.Default().InfoContext(ctx, "sealed publication seal verification started", "deployment", binding.DeploymentID, "candidate", binding.CandidateID, "bootstrap", binding.Bootstrap)
				candidate, err := sealedDelivery.DeliveryCandidateByID(ctx, binding.CandidateID)
				if err != nil {
					return err
				}
				seal, err := sealedDelivery.DeliveryCatalogSealByID(ctx, binding.Seal.SealID)
				if err != nil {
					return err
				}
				if candidate.Status != deployment.DeliveryCandidateReady || seal.Status != deployment.CatalogSealVerified || candidate.SealID != seal.ID || candidate.CatalogDigest != binding.Seal.CatalogDigest || candidate.CatalogObjectKey != binding.Seal.CatalogObjectKey || candidate.PhysicalPoolID != binding.Seal.PhysicalPoolID || candidate.CompatibilityDigest != binding.Seal.CompatibilityDigest || candidate.ServingArtifactID != binding.Seal.ServingArtifactID || candidate.ServingArtifactDigest != binding.Seal.ServingArtifactDigest || seal.CatalogDigest != binding.Seal.CatalogDigest || seal.CompatibilityDigest != binding.Seal.CompatibilityDigest || seal.ObjectKey != binding.Seal.CatalogObjectKey || seal.PhysicalPoolID != binding.Seal.PhysicalPoolID || seal.ServingArtifactID != binding.Seal.ServingArtifactID || seal.ServingArtifactDigest != binding.Seal.ServingArtifactDigest {
					return fmt.Errorf("sealed publication evidence is not one durable candidate/seal tuple")
				}
				pools := physicalpoolsqlite.NewRepository(store.SQLDB())
				admission, err := pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(seal.PhysicalPoolID), seal.CompatibilityDigest)
				if err != nil {
					return fmt.Errorf("load sealed pool admission: %w", err)
				}
				contract := &ducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
				objectStore, err := gcadapter.NewPoolStore(ctx, contract, gcadapter.S3Config{Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle})
				if err != nil {
					return err
				}
				object, err := objectStore.Open(ctx, binding.Seal.CatalogObjectKey)
				if err != nil || object.Body == nil {
					return fmt.Errorf("open sealed catalog object: %w", err)
				}
				defer object.Body.Close()
				slog.Default().InfoContext(ctx, "sealed publication catalog object opened", "deployment", binding.DeploymentID, "objectKey", binding.Seal.CatalogObjectKey, "size", binding.Seal.ObjectSize)
				hash := sha256.New()
				n, err := io.Copy(hash, object.Body)
				if err != nil {
					return err
				}
				if n != binding.Seal.ObjectSize || object.Size != binding.Seal.ObjectSize || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != binding.Seal.CatalogDigest {
					return fmt.Errorf("sealed catalog object bytes or metadata do not match verified seal")
				}
				slog.Default().InfoContext(ctx, "sealed publication seal verification completed", "deployment", binding.DeploymentID, "candidate", binding.CandidateID)
				return nil
			},
		}
		releases := releaseModule.DeploymentLinkage()
		sealedPublishRequest = func(ctx context.Context, pending deploymentapiadapter.Deployment, releaseID string, actor deployment.ApprovalActor, bootstrap bool) (sealedcontrol.PublishRequest, error) {
			request, err := buildSealedPublishRequest(ctx, sealedDelivery, releases, pending, releaseID, instanceID)
			request.ActorID = actor.PrincipalID
			request.Bootstrap = bootstrap
			return request, err
		}
		sealedRollbackRequest = func(ctx context.Context, pending deploymentapiadapter.Deployment, releaseID string, actor deployment.ApprovalActor, expectedBaseGenerationID string, expectedTargetRevision int64) (sealedcontrol.RollbackRequest, error) {
			request, err := buildSealedRollbackRequest(ctx, sealedDelivery, releases, pending, releaseID, instanceID, expectedBaseGenerationID, expectedTargetRevision)
			request.ActorID = actor.PrincipalID
			return request, err
		}
		sealedRollbackFence = func(ctx context.Context, targetID string) (string, int64, error) {
			target, err := sealedDelivery.DeliveryTargetRevision(ctx, targetID)
			if err != nil {
				return "", 0, err
			}
			if strings.TrimSpace(target.ActiveGenerationID) == "" {
				return "", 0, fmt.Errorf("rollback requires an active delivery generation")
			}
			return target.ActiveGenerationID, target.TargetRevision, nil
		}
		sealedActiveState = func(ctx context.Context) (servingstate.ID, error) {
			currentProjectID, err := resolveCurrentProjectID(ctx)
			if err != nil {
				return "", err
			}
			target, targetErr := sealedDelivery.DeliveryTargetRevision(ctx, instanceID)
			if targetErr != nil {
				if errors.Is(targetErr, sql.ErrNoRows) || errors.Is(targetErr, deployment.ErrNotFound) {
					return "", servingstate.ErrNotFound
				}
				return "", targetErr
			}
			if target.ProjectID != currentProjectID.String() || target.Environment != string(environment) {
				return "", fmt.Errorf("%w: target scope or active generation changed", deployment.ErrDeliveryConflict)
			}
			// A target revision is created before its first publication. An empty
			// pointer is therefore the expected fresh-target state, not a scope
			// conflict; leave the sealed runtime unbound until publication commits.
			if strings.TrimSpace(target.ActiveGenerationID) == "" {
				return "", servingstate.ErrNotFound
			}
			active, err := sealedDelivery.ActiveDeliveryGenerationForTarget(ctx, instanceID, currentProjectID.String(), string(environment))
			if err != nil {
				// A fresh production target has no delivery pointer yet. Keep the
				// administration surface bootable and let readiness/serving report
				// the absence explicitly instead of failing process construction.
				if errors.Is(err, deployment.ErrNotFound) {
					return "", servingstate.ErrNotFound
				}
				return "", err
			}
			stateID := active.ServingStateID
			if strings.TrimSpace(stateID) == "" {
				return "", fmt.Errorf("active delivery generation has no persisted serving state")
			}
			return servingstate.ID(stateID), nil
		}
	}
	if err := refreshmodule.Recover(ctx, store.SQLDB(), string(environment)); err != nil {
		return fail(err)
	}
	// Production candidate synchronization is canonical-only. The concrete
	// target-owned adapter is wired after candidate connection bindings and
	// runtime-host construction below; administration remains available when
	// physical-pool admission is absent.
	var canonicalDelivery *deploymentmodule.CanonicalDeliveryAdapter
	var canonicalDeliveryMutations *deploymentmodule.CanonicalDeliveryMutations
	candidatePreparationAdmission := candidatePreparationAdmitter(
		workloadController,
		workloadmodule.ControlRequest("candidate.prepare"),
	)
	canonicalDeliveryRequired := true
	// Sealed production serving uses delivery-owned catalog lease/GC state;
	// the legacy serving-state snapshot retention worker must not inspect or
	// delete the mutable process catalog on that path.
	var retention *servingstatemodule.Retention
	// Production serving resolves only the durable delivery pointer and exact
	// sealed catalog object. The legacy process-wide catalog remains available
	// to evaluation/tests, but is not opened in production.
	var servingFactory runtimehost.RuntimeFactory
	var gcMaintenance *gcadapter.Maintenance
	{
		var gcErr error
		gcMaintenance, gcErr = gcadapter.NewMaintenance(func(gcCtx context.Context) error {
			gcProjectID, err := resolveCurrentProjectID(gcCtx)
			if err != nil {
				return fmt.Errorf("resolve physical-pool GC project: %w", err)
			}
			return appruntimefactory.RunSQLiteProductionGC(gcCtx, appruntimefactory.ProductionGCRunConfig{
				Database: store.SQLDB(), TargetID: instanceID, ProjectID: gcProjectID.String(), Environment: string(environment), OwnerID: instanceID, HolderID: instanceID,
				StagingRoot:   filepath.Join(cfg.RuntimeDir(), "gc"),
				PoolS3:        gcadapter.S3Config{Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionSupply},
				LeaseDuration: 15 * time.Minute, BuildGrace: time.Hour, OrphanGrace: time.Hour, ReaderGrace: 30 * time.Minute,
			})
		}, cfg.ManagedDataGCInterval, slog.Default(), nil)
		if gcErr != nil {
			return fail(gcErr)
		}
	}
	{
		var factoryErr error
		servingFactory, factoryErr = appruntimefactory.NewSQLiteSealedFactory(appruntimefactory.ProductionSealedFactoryConfig{
			Database: store.SQLDB(), TargetID: instanceID, CatalogObjectRoot: cfg.ArtifactDir(),
			DuckDBDir: cfg.DuckDBDirPath(), RuntimeDir: cfg.RuntimeDir(), LeaseHolder: instanceID,
			ProjectRuntimeFactory: analyticsModule.ProjectRuntimeFactoryForEnvironment,
			DashboardMaxRows:      cfg.QueryResultMaxRows, DashboardMaxBytes: cfg.QueryResultMaxBytes,
			PoolS3:             gcadapter.S3Config{Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionSupply},
			ActivationEvidence: activeRuntimeEvidence,
			Authorize: func(ctx context.Context, evidence appruntimefactory.SealedAuthorizationInput) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				claimed, found, err := readClaim(ctx)
				if err != nil {
					return fmt.Errorf("read live serving claim: %w", err)
				}
				if !found || claimed.String() != evidence.ProjectID || evidence.TargetID != instanceID || evidence.Environment != string(environment) || evidence.ProjectID == "" || evidence.GenerationID == "" && evidence.CandidateID == "" || evidence.SealID == "" {
					return fmt.Errorf("sealed serving live authorization evidence is incomplete")
				}
				return nil
			},
		})
		if factoryErr != nil {
			return fail(factoryErr)
		}
	}
	runtimeHostModule, err = runtimehostmodule.Build(ctx, runtimehostmodule.Config{
		States:             servingStateRepo,
		ProjectID:          projectID,
		Environment:        environment,
		ReadClaimedProject: readClaim,
		ManagedData:        managedDataResolver,
		Authorization:      authorizationInstaller,
		OnDrained: func(_ servingstatemodule.ID, _ int64) {
			if retention == nil {
				return
			}
			go func() {
				if err := retention.Run(context.Background(), false); err != nil {
					slog.Default().Warn("storage retention cleanup failed after runtime drain", "error", err)
				}
			}()
		},
		Factory:                  servingFactory,
		RequireSealedCatalog:     true,
		ResolveSealedActiveState: sealedActiveState,
	})
	if err != nil {
		return fail(err)
	}
	projectCatalog, err := projectcatalog.NewService(
		projectCatalogLeaseProvider{provider: runtimeHostModule.Provider()},
		projectCatalogSubjectResolver{resolve: accessModule.AuthorizationSubjects},
	)
	if err != nil {
		return fail(fmt.Errorf("build project catalog: %w", err))
	}
	releaseModule.SetProjectSearchCatalog(projectCatalog)
	accessModule.SetCurrentEffectiveCapabilities(func(ctx context.Context, principalID string) ([]access.Capability, error) {
		subjects, err := accessModule.AuthorizationSubjects(ctx, principalID)
		if err != nil {
			return nil, err
		}
		snapshot, err := authorizationSnapshot(ctx)
		if err != nil {
			return nil, err
		}
		return snapshot.EffectiveCapabilities(subjects)
	})
	projectIDResolver := currentProjectID
	accessModule.SetCurrentProjectID(projectIDResolver)
	servingSnapshotResolver := func(ctx context.Context) (string, error) {
		lease, err := runtimeHostModule.Acquire(ctx)
		if err != nil {
			return "", err
		}
		defer lease.Release()
		identity := lease.Identity()
		if err := identity.Validate(); err != nil {
			return "", fmt.Errorf("active runtime serving identity is invalid: %w", err)
		}
		return identity.GenerationID, nil
	}
	cleanup.Push("runtime-host", func(context.Context) error { return runtimeHostModule.Close() })
	authoringAcquireRuntime := func(ctx context.Context) (runtimehostmodule.Lease, error) {
		return runtimeHostModule.Acquire(ctx)
	}
	authoringApplication, err := dashboardmodule.BuildAuthoring(dashboardmodule.AuthoringConfig{
		Database: store.SQLDB(), AuditIntentRecorder: auditRuntime.recorder,
		AuthorizeResource: func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
			if authoringDevelopmentBypass(ctx, principalID) {
				return true, nil
			}
			return authorizeProjectResources(ctx, accessModule, runtimeHostModule, principalID, projectID, []access.ResourceRef{resource}, capability)
		},
		AuthorizeProjectCapability: func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, capability access.Capability) (bool, error) {
			if authoringDevelopmentBypass(ctx, principalID) {
				return true, nil
			}
			return authorizeProjectRole(ctx, accessModule, runtimeHostModule, principalID, projectID, capability)
		},
		AcquireRuntime: authoringAcquireRuntime,
	})
	if err != nil {
		return fail(fmt.Errorf("build dashboard authoring module: %w", err))
	}
	generationRevalidator, err := authoringApplication.NewGenerationRevalidator(time.Now)
	if err != nil {
		return fail(fmt.Errorf("build dashboard generation revalidator: %w", err))
	}
	deploymentRuntime, err := deploymentmodule.NewRuntime(runtimeHostModule)
	if err != nil {
		return fail(err)
	}
	candidateBindings, err := analyticsModule.NewRuntimeBindingLeaser(
		analyticsmodule.RuntimeBindingLeaserConfig{
			Authorize: func(
				ctx context.Context,
				principalID string,
				binding analyticsmodule.ConnectionTargetBinding,
			) error {
				resource, err := access.NewResourceRef(binding.ConnectionID, projectgraph.KindConnection)
				if err != nil {
					return err
				}
				allowed, err := authorizeProjectResources(
					ctx, accessModule, runtimeHostModule, principalID,
					binding.Scope.ProjectID, []access.ResourceRef{resource}, access.CapabilityResourceUse,
				)
				if err != nil {
					return err
				}
				if !allowed {
					return analyticsmodule.ErrConnectionBindingUnauthorized
				}
				return nil
			},
			Now: time.Now,
			Audit: connectionRotationAuditRecorder{
				record: accessAuditRecorder(accessModule),
			},
		},
	)
	if err != nil {
		return fail(err)
	}
	identity := buildinfo.Current()
	recoveryLifecycle, err := productionRecoveryLifecycle(cfg, identity, string(environment), instanceID)
	if err != nil {
		return fail(err)
	}
	{
		// The canonical adapter is assembled only after the runtime binding
		// leaser exists. Missing or unadmitted pool configuration leaves the
		// process available for administration but makes candidate sync fail
		// closed with ErrCandidateUnavailable.
		deliveryRepository := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})
		deliveryLifecycle, lifecycleErr := deployment.NewDeliveryLifecycle(appruntimefactory.BootstrapTargetResolver{
			Resolver: deliveryRepository, TargetID: instanceID, ProjectID: projectID.String(), Environment: string(environment),
			ProjectIDResolver: func(resolveCtx context.Context) (string, error) {
				claimed, found, err := readClaim(resolveCtx)
				if err != nil {
					return "", err
				}
				if !found {
					return "", nil
				}
				return claimed.String(), nil
			},
		}, deliveryRepository)
		if lifecycleErr != nil {
			return fail(lifecycleErr)
		}
		var poolContract *ducklake.PoolContract
		var poolStore catalogseal.ObjectStore
		var poolCredentialBootstrap ducklake.CredentialBootstrap
		var poolErr error
		deliveryPhysicalPoolID := strings.TrimSpace(cfg.DeliveryPhysicalPoolID)
		deliveryCompatibilityDigest := strings.TrimSpace(cfg.DeliveryPhysicalPoolCompatibilityDigest)
		// The disposable loopback-only evaluation profile deliberately uses the
		// production serving runtime, but it retains the development contract of
		// owning and qualifying an isolated local pool. Ordinary production
		// targets must still use reviewed offline admission evidence.
		if allowsLocalEvaluationRuntime(production, cfg.EvaluationMode) && deliveryPhysicalPoolID == "" {
			tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:" + identity.Version + ":" + identity.Revision, DuckLakeExtension: "ducklake:managed", CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
			deliveryStorageLocation, storageLocationErr := filepath.Abs(cfg.DuckLakeDataDir())
			if storageLocationErr != nil {
				poolErr = fmt.Errorf("resolve local physical-pool storage location: %w", storageLocationErr)
			} else {
				pool, newPoolErr := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: deliveryStorageLocation, StorageNamespace: "delivery", EncryptionDomain: instanceID, IsolationBoundary: instanceID, RetentionAuthority: instanceID, RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 1800, OrphanGracePeriodSeconds: 3600, BuildGracePeriodSeconds: 3600}, Compatibility: tuple})
				if newPoolErr != nil {
					poolErr = newPoolErr
				} else {
					pools := physicalpoolsqlite.NewRepository(store.SQLDB())
					admission, loadErr := pools.LoadAdmissionContract(ctx, pool.ID)
					if loadErr == nil && admission.Pool.Compatibility == tuple {
						deliveryPhysicalPoolID = admission.Pool.ID.String()
						deliveryCompatibilityDigest = admission.Admission.CompatibilityDigest
					} else if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) && !errors.Is(loadErr, deployment.ErrNotFound) && !errors.Is(loadErr, physicalpool.ErrPoolNotAdmitted) {
						poolErr = fmt.Errorf("load local physical-pool admission: %w", loadErr)
					} else {
						evidence, evidenceErr := ducklake.RunLocalPoolConformance(ctx, filepath.Join(cfg.RuntimeDir(), "delivery-conformance"), tuple, extensionSupply)
						if evidenceErr != nil {
							poolErr = fmt.Errorf("run local physical-pool conformance: %w", evidenceErr)
						} else if dataPath, dataPathErr := pool.DataPath(); dataPathErr != nil {
							poolErr = dataPathErr
						} else if err := securefs.EnsurePrivateDir(dataPath); err != nil {
							poolErr = err
						} else if marker, markerErr := gcstore.NewLocal(dataPath); markerErr != nil {
							poolErr = markerErr
						} else if admitted, admission, admitErr := pools.CreateAndAdmitWithOwnership(ctx, pool, evidence, instanceID, marker); admitErr != nil {
							poolErr = fmt.Errorf("admit local physical pool: %w", admitErr)
						} else {
							deliveryPhysicalPoolID = admitted.ID.String()
							deliveryCompatibilityDigest = admission.CompatibilityDigest
						}
					}
				}
			}
		}
		if deliveryPhysicalPoolID != "" {
			pools := physicalpoolsqlite.NewRepository(store.SQLDB())
			poolID := physicalpool.PoolID(deliveryPhysicalPoolID)
			var admission physicalpoolsqlite.AdmissionContract
			var err error
			if deliveryCompatibilityDigest != "" {
				admission, err = pools.LoadAdmissionContractByCompatibilityDigest(ctx, poolID, deliveryCompatibilityDigest)
			} else {
				// A pool with append-only upgrades is ambiguous without the exact
				// tuple. The repository intentionally rejects that case; never
				// select the newest admission by timestamp.
				admission, err = pools.LoadAdmissionContract(ctx, poolID)
			}
			if err != nil {
				poolErr = fmt.Errorf("load configured delivery physical-pool admission: %w", err)
			} else {
				poolContract = &ducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
				poolS3 := gcadapter.S3Config{Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionSupply}
				poolStore, poolErr = appruntimefactory.NewCatalogObjectStore(ctx, poolContract, poolS3)
				if poolErr == nil {
					poolCredentialBootstrap, poolErr = gcadapter.NewPoolCredentialBootstrap(poolContract, poolS3)
				}
			}
		}
		// A production process may still serve the administration surface on a
		// fresh target, but readiness must expose missing pool admission,
		// missing target revision, or legacy rows without serving identity. The
		// callback reads durable state each time; configuration never synthesizes
		// an admission or serving pointer.
		deliveryStartupCheck = func(ctx context.Context) error {
			startupProjectID, err := resolveDeliveryStartupProjectID(ctx, projectID, readClaim)
			if err != nil {
				return fmt.Errorf("delivery startup project claim: %w", err)
			}
			state := deployment.DeliveryStartupState{
				Production:               production,
				TargetID:                 instanceID,
				ProjectID:                startupProjectID.String(),
				Environment:              string(environment),
				ConfiguredPhysicalPoolID: deliveryPhysicalPoolID,
				PhysicalPoolExists:       poolContract != nil,
				PhysicalPoolAdmitted:     poolContract != nil && poolErr == nil,
				LegacyServingPathEnabled: false,
			}
			if indeterminate, err := deliveryRepository.HasIndeterminateDeliveryPublication(ctx, instanceID); err != nil {
				return fmt.Errorf("delivery startup publication reconciliation: %w", err)
			} else {
				state.IndeterminatePublication = indeterminate
			}
			target, targetErr := deliveryRepository.DeliveryTargetRevision(ctx, instanceID)
			if targetErr == nil {
				state.TargetRevisionExists = true
				if target.ProjectID != startupProjectID.String() || target.Environment != string(environment) {
					return fmt.Errorf("delivery startup target scope changed")
				}
			} else if !errors.Is(targetErr, sql.ErrNoRows) && !errors.Is(targetErr, deployment.ErrNotFound) {
				return fmt.Errorf("delivery startup target revision: %w", targetErr)
			}
			if targetErr == nil && strings.TrimSpace(target.ActiveGenerationID) != "" {
				active, err := deliveryRepository.ActiveDeliveryGenerationForTarget(ctx, instanceID, startupProjectID.String(), string(environment))
				if err == nil {
					state.ActiveServingGeneration = true
					state.ActiveServingStateIdentity = active.ServingStateID
					if strings.TrimSpace(state.ActiveServingStateIdentity) == "" {
						state.MigratedRowsWithoutServingID = 1
					}
				} else if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, deployment.ErrNotFound) {
					return fmt.Errorf("delivery startup active generation: %w", err)
				}
			}
			return deployment.ValidateDeliveryStartup(state)
		}
		canonicalRuntime, runtimeErr := deployment.NewCandidateRuntimeService(deployment.CandidateRuntimeServiceConfig{Connections: candidateConnectionLeaser{leaser: candidateBindings, module: analyticsModule}, Runtime: runtimeHostModule, RuntimeVersion: identity.Version + ":" + identity.Revision})
		if runtimeErr != nil {
			return fail(runtimeErr)
		}
		materialize := func(matCtx context.Context, working *candidatecatalog.WorkingCatalog, buildInput deployment.DeliveryBuildInput, artifacts release.CandidateArtifactSet, candidateID string) ([]analyticsgates.SourceInput, error) {
			observationBounds := analyticsgates.Bounds{MaxRows: 10000, MaxQueries: 128, MaxMillis: 5000}
			matCtx = analyticsmaterialize.WithObservationBudget(matCtx, analyticsmaterialize.ObservationBudget{MaxQueries: observationBounds.MaxQueries, MaxMillis: observationBounds.MaxMillis})
			models := artifacts.Compiler.Artifact.Models()
			if len(models) == 0 {
				return nil, fmt.Errorf("compiled project contains no semantic models")
			}
			// Candidate artifacts intentionally keep managed-data locations out of
			// the portable project artifact. Resolve the exact pins that were
			// installed by ValidateWithManagedDataRevisions and bind their leased
			// runtime roots to this detached model copy for physical refresh.
			managedResolution, resolveErr := managedDataResolver.ResolveManagedDataForIdentity(matCtx, artifacts.Generation.Identity)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolve candidate managed-data roots: %w", resolveErr)
			}
			if managedResolution.Lifetime != nil {
				defer managedResolution.Lifetime.Release()
			}
			if err := analyticsmodule.BindCandidateManagedDataRoots(models, artifacts.Compiler.Artifact.Manifest().NameIndex.Connections, managedResolution.Roots); err != nil {
				return nil, err
			}
			pipelineScoped := buildInput.Plan.PipelinePlan != nil && buildInput.Plan.Operation == deployment.DeliveryOperationRestatement
			if artifacts.Generation.DataMode == release.GenerationDataReuseBase && !pipelineScoped {
				actual, err := working.VisibleTables(matCtx)
				if err != nil {
					return nil, err
				}
				expected := appruntimefactory.ExpectedRelations(artifacts)
				if err := appruntimefactory.VerifyExpectedRelations(actual, expected); err != nil {
					return nil, err
				}
				return sourceInputsFromManifest(artifacts, nil), nil
			}
			var observations []analyticsgates.SourceInput
			err := working.WithEnvironment(matCtx, func(environment *ducklake.Environment) error {
				baseRetained := buildInput.Attempt.BaseCatalogDigest != ""
				changedByModel, removed, refreshAll := deliveryMaterializationDelta(artifacts, buildInput.Plan)
				if baseRetained {
					for _, table := range removed {
						if err := environment.Exec(matCtx, `DROP TABLE IF EXISTS "model"."`+strings.ReplaceAll(table, `"`, `""`)+`"`); err != nil {
							return fmt.Errorf("remove retired model relation %q: %w", table, err)
						}
					}
				}
				factory := analyticsModule.ProjectRuntimeFactoryForEnvironment(environment)
				runtime, err := factory.OpenProject(matCtx, analyticsruntime.ProjectRequest{Models: models, ServingStateID: artifacts.Generation.Identity.GenerationID, ProjectID: artifacts.Generation.Identity.ProjectID, Environment: artifacts.Generation.Identity.Environment, SemanticDigest: artifacts.Artifact.ProjectDigest, ArtifactDigest: artifacts.Generation.ArtifactDigest, SourceDataDigest: artifacts.Generation.DataRevision, CandidateID: candidateID, AuthorizationFingerprint: artifacts.AuthorizationFingerprint, BindingFingerprint: buildInput.Plan.Execution.BindingDigest, SkipInitialRefresh: baseRetained && !refreshAll})
				if err != nil {
					return err
				}
				defer runtime.Close()
				if baseRetained && !refreshAll {
					for modelID, tables := range changedByModel {
						if len(tables) == 0 {
							continue
						}
						if err := runtime.RefreshModelTables(matCtx, modelID, tables); err != nil {
							return fmt.Errorf("refresh impacted model %q: %w", modelID, err)
						}
					}
					observations = sourceInputsFromManifest(artifacts, runtime)
					return nil
				}
				if err := runtime.Refresh(matCtx); err != nil {
					return err
				}
				observations = sourceInputsFromManifest(artifacts, runtime)
				return nil
			})
			if err != nil {
				return nil, err
			}
			return observations, nil
		}
		var baseResolver func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error)
		if poolContract != nil && poolStore != nil {
			baseResolver = func(baseCtx context.Context, buildInput deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
				if buildInput.Plan.BaseGenerationID == "" {
					return nil, nil
				}
				generation, err := deliveryRepository.DeliveryGenerationByID(baseCtx, buildInput.Plan.BaseGenerationID)
				if err != nil {
					return nil, err
				}
				baseCandidate, err := deliveryRepository.DeliveryCandidateByID(baseCtx, generation.CandidateID)
				if err != nil {
					return nil, err
				}
				seal, err := deliveryRepository.DeliveryCatalogSealByID(baseCtx, baseCandidate.SealID)
				if err != nil {
					return nil, err
				}
				if generation.CatalogDigest != seal.CatalogDigest || generation.PhysicalPoolID != poolContract.Pool.ID.String() || generation.CompatibilityDigest != seal.CompatibilityDigest || baseCandidate.Status != deployment.DeliveryCandidateReady || baseCandidate.ServingStateID != generation.ServingStateID || baseCandidate.ServingArtifactID != generation.ServingArtifactID || baseCandidate.ServingArtifactDigest != generation.ServingArtifactDigest {
					return nil, fmt.Errorf("active sealed base does not match configured physical pool")
				}
				return &candidatecatalog.SealedArtifact{ObjectKey: seal.ObjectKey, Digest: seal.CatalogDigest, SizeBytes: seal.ObjectSize, PhysicalPoolID: seal.PhysicalPoolID, Compatibility: poolContract.Tuple, Reader: candidatecatalog.ObjectReader{Store: appruntimefactory.CandidateObjectStore{Store: poolStore}, Key: seal.ObjectKey}}, nil
			}
		}
		var verifyLease candidatecatalog.LeaseVerifier
		if poolErr == nil && poolContract != nil {
			verifyLease = appruntimefactory.SQLiteWriterLeaseVerifier(deliveryRepository)
		}
		buildFactory := appruntimefactory.BuildRequestFactory(appruntimefactory.CandidateCatalogRunnerConfig{PoolContract: poolContract, StagingRoot: cfg.DeliveryStagingDir, ExtensionAdmission: extensionSupply, CredentialBootstrap: poolCredentialBootstrap, Base: baseResolver, Materialize: materialize, Connections: candidateConnectionLeaser{leaser: candidateBindings, module: analyticsModule}, QualificationFactory: appruntimefactory.QualificationRequestForCandidate, ObjectStore: poolStore, SealRepository: deliveryRepository, RemoteVerifier: appruntimefactory.ReadOnlyCatalogVerifier{PoolContract: poolContract, StagingRoot: cfg.DeliveryStagingDir, ObjectStore: poolStore, ExtensionAdmission: extensionSupply, CredentialBootstrap: poolCredentialBootstrap}, VerifyLease: verifyLease, RuntimeVersion: identity.Version + ":" + identity.Revision})
		planCandidate := func(planCtx context.Context, input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet) (deployment.DeliveryPlan, error) {
			var reuse *deployment.DeliveryReuseInput
			if input.Candidate.Scope.BaseGenerationID != "" {
				generation, generationErr := deliveryRepository.DeliveryGenerationByID(planCtx, input.Candidate.Scope.BaseGenerationID)
				if generationErr != nil {
					return deployment.DeliveryPlan{}, generationErr
				}
				baseCandidate, candidateErr := deliveryRepository.DeliveryCandidateByID(planCtx, generation.CandidateID)
				if candidateErr != nil {
					return deployment.DeliveryPlan{}, candidateErr
				}
				basePlan, planErr := deliveryRepository.PlanByID(planCtx, baseCandidate.PlanID)
				if planErr != nil {
					return deployment.DeliveryPlan{}, planErr
				}
				baseContextDigest, contextErr := basePlan.Execution.ContextDigest()
				if contextErr != nil {
					return deployment.DeliveryPlan{}, contextErr
				}
				reuse = &deployment.DeliveryReuseInput{
					BaseExecutionDigest: baseCandidate.ExecutionDigest, CatalogDigest: generation.CatalogDigest, BaseCatalogDigest: generation.CatalogDigest,
					PhysicalPoolID: generation.PhysicalPoolID, BasePhysicalPoolID: generation.PhysicalPoolID, BaseContextDigest: baseContextDigest,
					CompatibilityDigest: generation.CompatibilityDigest, BaseCompatibilityDigest: generation.CompatibilityDigest, Deterministic: artifacts.Generation.Deterministic,
				}
			}
			return appruntimefactory.PreviewCandidatePlanWithPolicyAndReuse(planCtx, deliveryLifecycle, input, artifacts, identity.Version+":"+identity.Revision, appruntimefactory.CandidateDeliveryPolicy{RequiresApproval: requiresDeliveryApproval(production, cfg.EvaluationMode, input.Operation), RollbackClass: deployment.DeliveryServingSafe, RetentionWindow: cfg.DeliveryRollbackRetention().String()}, reuse)
		}
		publishCanonicalCandidate := func(publishCtx context.Context, project, candidate, actor string, refreshFence *deployment.RefreshPublicationFence) (deployment.DeliveryPublication, error) {
			candidateRecord, candidateErr := sealedDelivery.DeliveryCandidateByID(publishCtx, candidate)
			if candidateErr != nil {
				return deployment.DeliveryPublication{}, candidateErr
			}
			if candidateRecord.ProjectID.String() != project {
				return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication project scope changed", deployment.ErrDeliveryConflict)
			}
			request, err := buildCanonicalPublishRequest(publishCtx, sealedDelivery, candidate, instanceID)
			if err != nil {
				return deployment.DeliveryPublication{}, err
			}
			if refreshFence != nil {
				if err := refreshFence.Validate(); err != nil {
					return deployment.DeliveryPublication{}, err
				}
				request.Publication.RefreshRunID = refreshFence.RunID
				request.Publication.RefreshLeaseOwner = refreshFence.LeaseOwner
				request.Publication.RefreshLeaseRevision = refreshFence.LeaseRevision
				request.Publication.RefreshTargetRevision = refreshFence.TargetRevision
			}
			request.ActorID = actor
			if _, err := sealedCoordinator.PublishWithActivation(publishCtx, request, func(activationCtx context.Context, commit func() error) error {
				commitAndVerify := func() error {
					if err := commit(); err != nil {
						return err
					}
					return verifyCanonicalDeliveryTarget(activationCtx, sealedDelivery, instanceID, request.Publication.ProjectID.String(), request.Publication.Environment, request.Generation.ServingStateID, request.Publication.ExpectedTargetRevision+1)
				}
				return activateCanonicalServingState(activationCtx, runtimeHostModule, request.Generation.ServingStateID, commitAndVerify)
			}); err != nil {
				if pending, readErr := sealedDelivery.DeliveryPublicationByID(publishCtx, request.Publication.ID); readErr == nil {
					return pending, err
				}
				return deployment.DeliveryPublication{}, err
			}
			activated := deployment.Deployment{
				ServingIdentity: projectgraph.ServingIdentity{
					ProjectID: request.Publication.ProjectID, Environment: request.Publication.Environment,
					GenerationID: request.Generation.ServingStateID,
				},
				ActivationPrincipal: actor,
			}
			if err := reconcileActivatedDashboardPublications(publishCtx, store.SQLDB(), servingStateRepo, activated); err != nil {
				logDashboardPublicationReconciliationFailure(slog.Default(), err, request.Generation.ServingStateID)
			}
			return sealedDelivery.DeliveryPublicationByID(publishCtx, request.Publication.ID)
		}
		canonicalDelivery = appruntimefactory.NewCanonicalDeliveryAdapter(appruntimefactory.CanonicalDeliveryConfig{Lifecycle: deliveryLifecycle, Artifacts: releaseModule, Publish: func(publishCtx context.Context, project, candidate, actor, _ string) (deployment.DeliveryPublication, error) {
			return publishCanonicalCandidate(publishCtx, project, candidate, actor, nil)
		}, Rollback: func(rollbackCtx context.Context, project, generation, actor, key string) (deployment.DeliveryPublication, error) {
			generationRecord, generationErr := sealedDelivery.DeliveryGenerationByID(rollbackCtx, generation)
			if generationErr != nil {
				return deployment.DeliveryPublication{}, generationErr
			}
			if generationRecord.ProjectID.String() != project {
				return deployment.DeliveryPublication{}, fmt.Errorf("%w: rollback project scope changed", deployment.ErrDeliveryConflict)
			}
			request, err := buildCanonicalRollbackRequest(rollbackCtx, sealedDelivery, generation, key, instanceID)
			if err != nil {
				return deployment.DeliveryPublication{}, err
			}
			request.ActorID = actor
			if _, err := sealedCoordinator.RollbackWithActivation(rollbackCtx, request, func(activationCtx context.Context, commit func() error) error {
				commitAndVerify := func() error {
					if err := commit(); err != nil {
						return err
					}
					return verifyCanonicalDeliveryTarget(activationCtx, sealedDelivery, instanceID, request.Request.ProjectID.String(), request.Request.Environment, request.Request.GenerationID, request.Request.ExpectedTargetRevision+1)
				}
				return activateCanonicalServingState(activationCtx, runtimeHostModule, request.Request.GenerationID, commitAndVerify)
			}); err != nil {
				return deployment.DeliveryPublication{}, err
			}
			activated := deployment.Deployment{
				ServingIdentity: projectgraph.ServingIdentity{
					ProjectID: request.Request.ProjectID, Environment: request.Request.Environment,
					GenerationID: request.Request.GenerationID,
				},
				ActivationPrincipal: actor,
			}
			if err := reconcileActivatedDashboardPublications(rollbackCtx, store.SQLDB(), servingStateRepo, activated); err != nil {
				logDashboardPublicationReconciliationFailure(slog.Default(), err, request.Request.GenerationID)
			}
			return sealedDelivery.DeliveryPublicationByID(rollbackCtx, request.Request.ID)
		}, Plan: planCandidate, PlanPreview: planCandidate, BuildRequest: func(buildCtx context.Context, input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet) (deployment.DeliveryBuildRequest, error) {
			if poolErr != nil || poolContract == nil {
				return deployment.DeliveryBuildRequest{}, fmt.Errorf("%w: candidate physical-pool admission unavailable", deployment.ErrCandidateUnavailable)
			}
			request, err := buildFactory(buildCtx, input, artifacts)
			if err != nil {
				return deployment.DeliveryBuildRequest{}, err
			}
			return request, nil
		}, ReadyCandidate: func(readyCtx context.Context, input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet, build deployment.DeliveryBuildResult) (deployment.Candidate, error) {
			if build.GateEvidence == nil {
				return deployment.Candidate{}, fmt.Errorf("%w: candidate gate evidence is required", deployment.ErrCandidateInvalid)
			}
			bindingFingerprint := ""
			if build.GateEvidence != nil {
				bindingFingerprint = build.GateEvidence.BindingGeneration
			}
			receipt, err := canonicalRuntime.Prepare(readyCtx, deployment.CandidateRuntimeRequest{Candidate: input.Candidate, AuthorizationFingerprint: artifacts.AuthorizationFingerprint, Generation: deployment.CandidateGenerationRuntime{Identity: artifacts.Generation.Identity, ArtifactDigest: artifacts.Generation.ArtifactDigest, DataRevision: artifacts.Generation.DataRevision, DataMode: deployment.CandidateDataMode(artifacts.Generation.DataMode), Connections: candidateConnectionRequirements(artifacts.Generation.Connections), AuthoredConnections: candidateReleaseAuthoredConnections(artifacts.Generation.AuthoredConnections), ManagedDataConnections: candidateManagedDataConnections(artifacts.Generation.ManagedDataPins), Extensions: append([]extension.Evidence(nil), artifacts.Extensions...), Restrictions: candidateRuntimeRestrictions(artifacts.Generation.Restrictions), BindingFingerprint: bindingFingerprint, GateEvidence: build.GateEvidence}})
			if err != nil {
				return deployment.Candidate{}, err
			}
			provenance, err := deploymentmodule.CandidateProvenance(input.Candidate, artifacts, receipt, input.Source.SourceRevision)
			if err != nil {
				return deployment.Candidate{}, err
			}
			retained, err := releaseModule.RetainCandidateProvenance(readyCtx, input.ProjectID, provenance)
			if err != nil {
				return deployment.Candidate{}, err
			}
			if retained.Digest != provenance.Digest {
				return deployment.Candidate{}, fmt.Errorf("retained candidate provenance changed")
			}
			input.Candidate.Status = deployment.CandidateReady
			input.Candidate.ProvenanceDigest = retained.Digest
			return input.Candidate, nil
		}})
		canonicalDeliveryMutations = &deploymentmodule.CanonicalDeliveryMutations{
			Lifecycle:    canonicalDelivery.Lifecycle,
			Sources:      candidateSources,
			Artifacts:    releaseModule,
			Admission:    candidatePreparationAdmission,
			Plan:         canonicalDelivery.Plan,
			PlanPreview:  canonicalDelivery.PlanPreview,
			BuildRequest: canonicalDelivery.BuildRequest,
			Adapter:      canonicalDelivery,
			Publish:      canonicalDelivery.Publish,
			PublishFenced: func(publishCtx context.Context, project, candidate, actor, _ string, fence deployment.RefreshPublicationFence) (deployment.DeliveryPublication, error) {
				return publishCanonicalCandidate(publishCtx, project, candidate, actor, &fence)
			},
			Rollback: canonicalDelivery.Rollback,
		}
	}
	deploymentConfig := deploymentmodule.Config{
		Database: store.SQLDB(), AuditIntentRecorder: auditRuntime.recorder, States: servingStateRepo, Runtime: deploymentRuntime,
		DeliveryReader:     sealedDelivery,
		ManagedData:        managedDataResolver,
		BootstrapPolicies:  projectClaimRepository,
		BindClaimedProject: bindClaimedProject(runtimeHostModule, environment),
		Protected: protectedPublishingTarget(
			production,
			cfg.EvaluationMode,
		),
		CurrentApprovalActor: func(r *http.Request) (deploymentmodule.ApprovalActor, bool) {
			evidence, ok := accessModule.CurrentCredentialEvidence(r)
			if !ok {
				return deploymentmodule.ApprovalActor{}, false
			}
			return deploymentmodule.ApprovalActor{
				PrincipalID:         evidence.PrincipalID,
				CredentialClass:     deploymentmodule.CredentialClass(evidence.Class),
				CredentialID:        evidence.ID,
				CredentialExpiresAt: evidence.ExpiresAt,
			}, true
		},
		AuthorizeApproval: func(
			ctx context.Context,
			actor deploymentmodule.ApprovalActor,
			projectID string,
			environment string,
		) error {
			requestedProject, err := projectgraph.NewResourceID(projectID)
			if err != nil {
				return err
			}
			if requestedProject.String() != projectID {
				return deploymentmodule.ErrApprovalForbidden
			}
			project, err := access.NewResourceRef(requestedProject, projectgraph.KindProject)
			if err != nil {
				return err
			}
			allowed, err := authorizeProjectResources(ctx, accessModule, runtimeHostModule, actor.PrincipalID, requestedProject, []access.ResourceRef{project}, access.CapabilityProjectAdmin)
			if err != nil {
				return err
			}
			if !allowed {
				return deploymentmodule.ErrApprovalForbidden
			}
			return nil
		},
		AuthorizeActivation: func(
			ctx context.Context,
			actor deploymentmodule.ApprovalActor,
			projectID string,
			environment string,
		) error {
			requestedProject, err := projectgraph.NewResourceID(projectID)
			if err != nil {
				return err
			}
			if requestedProject.String() != projectID {
				return deploymentmodule.ErrActivationForbidden
			}
			project, err := access.NewResourceRef(requestedProject, projectgraph.KindProject)
			if err != nil {
				return err
			}
			allowed, err := authorizeProjectResources(ctx, accessModule, runtimeHostModule, actor.PrincipalID, requestedProject, []access.ResourceRef{project}, access.CapabilityProjectAdmin)
			if err != nil {
				return err
			}
			if !allowed {
				return deploymentmodule.ErrActivationForbidden
			}
			return nil
		},
		AuthorizeBootstrap: func(ctx context.Context, policy deployment.BootstrapActivationPolicy) error {
			if err := policy.Validate(); err != nil {
				return err
			}
			claimedProject, found, err := readClaim(ctx)
			if err != nil {
				return fmt.Errorf("bootstrap claim authorization: %w", err)
			}
			if !found || claimedProject != policy.ProjectID || policy.Environment != environment {
				return deployment.ErrBootstrapPolicyConflict
			}
			admin, err := accessModule.IsPlatformAdmin(ctx, policy.ActorID)
			if err != nil {
				return fmt.Errorf("bootstrap platform role authorization: %w", err)
			}
			if !admin {
				return deployment.ErrBootstrapPolicyConflict
			}
			if err := accessModule.AuthorizeBootstrapCredential(ctx, policy.ActorID, policy.CredentialID, policy.CredentialExpiresAt, time.Now().UTC()); err != nil {
				return fmt.Errorf("bootstrap credential authorization: %w", err)
			}
			// The process may have started before candidate synchronization
			// established the durable project claim, so the startup projectID
			// can legitimately be empty here. The bootstrap policy has already
			// been validated against the fresh claim above; use that canonical
			// request scope for the active-target check instead of the stale
			// startup snapshot.
			active, activeErr := hasActiveBootstrapServingState(ctx, runtimeHostModule, servingStateRepo, string(environment), sealedDelivery, instanceID, policy.ProjectID.String())
			if activeErr != nil {
				return fmt.Errorf("%w: %v", deployment.ErrBootstrapPolicyConflict, activeErr)
			}
			if active {
				return deployment.ErrBootstrapPolicyConflict
			}
			return nil
		},
		CandidateConnections: candidateConnectionLeaser{
			leaser: candidateBindings, module: analyticsModule,
		},
		CandidateRuntime:          runtimeHostModule,
		CandidateRuntimeLifecycle: runtimeHostModule,
		CandidateAdmission:        candidatePreparationAdmission,
		CandidateSources:          candidateSources,
		CandidateArtifacts:        releaseModule,
		CanonicalDeliveryAdapter:  canonicalDelivery,
		DeliveryMutations:         canonicalDeliveryMutations,
		RequireCanonicalDelivery:  canonicalDeliveryRequired,
		CandidateSourceAudit:      candidateSourceAuditRecorder(accessModule),
		CandidateSourceBlobAudit:  candidateSourceAuditRecorder(accessModule),
		RuntimeVersion:            identity.Version + ":" + identity.Revision,
		AfterActivated: func(ctx context.Context, activated deployment.Deployment) {
			generation, generationErr := activatedRevalidationGeneration(
				ctx, servingStateRepo, runtimeHostModule, activated.ServingIdentity, activated.PriorGenerationID,
			)
			if generationErr != nil {
				slog.Default().Warn("dashboard generation revalidation could not load activated generation", "project", activated.ServingIdentity.ProjectID, "generation", activated.ServingIdentity.GenerationID, "error", generationErr)
				return
			}
			results, revalidationErr := generationRevalidator.GenerationActivated(ctx, generation)
			for _, result := range results {
				if result.Err != nil {
					slog.Default().Warn("dashboard generation revalidation failed", "project", generation.Identity.ProjectID, "generation", generation.Identity.GenerationID, "dashboard", result.DashboardID, "error", result.Err)
				}
			}
			if revalidationErr != nil {
				slog.Default().Warn("dashboard generation revalidation failed", "project", generation.Identity.ProjectID, "generation", generation.Identity.GenerationID, "error", revalidationErr)
			}
		},
		ActivationHooks:   deploymentmodule.ActivationHooks{},
		SealedCoordinator: sealedCoordinator, SealedPublishRequest: sealedPublishRequest,
		SealedRollbackRequest: sealedRollbackRequest, SealedRollbackFence: sealedRollbackFence, RequireSealedCoordinator: true,
		SealedReconcile: func(ctx context.Context, generationID string) error {
			return runtimeHostModule.ReconcileSealed(ctx, servingstatemodule.ID(generationID))
		},
	}
	runtimeMetrics := dashboardmodule.NewRuntimeMetrics(dashboardmodule.RuntimeMetricsOptions{
		Provider: runtimeHostModule.Provider(), ProjectID: projectID,
		PublishedCompilationReader: authoringApplication.PublishedCompilationReader(),
	})
	auth := accessModule.Auth()
	rateLimits := apihttpmiddleware.ProductionRateLimitConfig()
	rateLimits.Enabled = production && cfg.RateLimitingEnabled()
	rateLimits.UseRealIP = cfg.RateLimitingUsesRealIP()
	routes, runtime, platformServices, policy, err := buildApplicationSurfaces(ctx, runtimeMetrics,
		dataAssemblyInputs{
			Database: store.SQLDB(), AuditRuntime: auditRuntime, PlatformHealth: store, AdminDatabase: store.SQLDB(),
			ServingStateRepo: servingStateRepo, StorageRetention: retention,
			AccessRepo: accessRepo,
		},
		capabilityAssemblyInputs{
			AnalyticsModule: analyticsModule, DashboardAssets: dashboardAssets,
			ReleaseModule: releaseModule, JobModule: jobModule,
			AccessModule: accessModule, ManagedDataModule: managedDataModule,
			ProjectCatalog: projectCatalog,
			// Browser graph reads are pinned to the exact active runtime lease;
			// canonical sealed publication no longer updates the legacy active
			// serving-state scope pointer.
			ProjectGraph: projectmodule.NewActiveServingStateGraphReader(runtimeHostModule.Provider(), servingStateRepo),
			Authoring:    authoringApplication,
			Product:      productService, ProductStatus: productAdministrationStatus(cfg, instanceID, publicURL, string(environment), identity),
		},
		workflowAssemblyInputs{
			AgentSettings: store,
			AgentConfig:   agentmodule.ModelConfig{APIKey: cfg.AgentAPIKey, BaseURL: cfg.AgentBaseURL, Model: cfg.AgentModel},
			Auth:          auth, Reloader: runtimeHostModule, Workload: workloadController,
			ManagedDataValidation: managedDataModule.BindingValidation(),
			ManagedDataResolver:   managedDataResolver,
			RefreshSourceDigest:   canonicalRefreshSourceDigest(sealedDelivery, instanceID),
			CanonicalRefreshExecutor: canonicalRefreshExecutor(
				canonicalDeliveryMutations, sealedDelivery, instanceID, auth != nil && auth.DevBypass(),
			),
			PublishedVersion:        canonicalPublishedDataVersion(sealedDelivery, instanceID),
			EnableRefreshDispatcher: true,
			RecoveryLifecycle:       recoveryLifecycle,
			RecoveryInterval:        time.Minute,
			DeploymentConfig:        deploymentConfig,
		},
		runtimeAssemblyInputs{
			RuntimeHost:          runtimeHostModule,
			DeliveryTargetReader: sealedDelivery,
			ProjectID:            projectID, ProjectIDResolver: projectIDResolver, ServingSnapshotResolver: servingSnapshotResolver,
			DuckLakeCatalogPath: duckLakeCatalogPath, DuckLakeDataPath: cfg.DuckLakeDataDir(),
			DefaultEnvironment: string(environment), SCIMBearerToken: cfg.SCIMBearerToken,
			MetricsBearerToken: cfg.MetricsBearerToken, AllowedHosts: allowedHosts, Assets: assets,
			InstanceID: instanceID, RequireActiveDeployment: cfg.EvaluationMode, SealedServing: true,
			RequireQueryAuthorization: production, AllowDevAuthBypass: !production,
			DeliveryStartup: deliveryStartupCheck,
		},
		httpAssemblyInputs{
			PublicURL: publicURL,
			DesktopDiscovery: desktopdiscovery.Config{
				CanonicalOrigin:   publicURL,
				InstanceID:        instanceID,
				DisplayName:       "LeapView",
				ServerVersion:     assets.Version(),
				AllowLoopbackHTTP: allowsLocalEvaluationRuntime(production, cfg.EvaluationMode),
			},
			RateLimits:      rateLimits,
			SecurityHeaders: apihttpmiddleware.SecurityHeaders(production && cfg.HSTSEnabled(cookieSecure)),
			RequestLogging:  production && cfg.RequestLoggingEnabled(), Logger: slog.Default(),
			JobLeaseTimeout: cfg.RefreshJobLeaseTimeout, ManagedDataTus: managedDataModule.TusHandler(),
		},
	)
	if err != nil {
		return fail(err)
	}
	if sealedCoordinator != nil && routes.deploymentModule != nil {
		durableApproval := routes.deploymentModule.SealedApprovalVerifier()
		sealedCoordinator.ApprovalVerifier = func(approvalCtx context.Context, binding sealedcontrol.SealBinding, publication deployment.PublicationIntent) error {
			slog.Default().InfoContext(approvalCtx, "sealed publication approval verification started", "deployment", binding.DeploymentID, "bootstrap", binding.Bootstrap)
			if binding.Bootstrap {
				// The activation worker has already revalidated the durable
				// one-shot bootstrap policy. Recheck the active-generation fence
				// here because Authorize and ApprovalVerifier are separate control
				// boundaries; if a generation appeared in between, fall through to
				// ordinary durable approval rather than bypassing it.
				active, activeErr := hasActiveBootstrapServingState(
					approvalCtx,
					runtimeHostModule,
					servingStateRepo,
					string(environment),
					sealedDelivery,
					instanceID,
					binding.ProjectID,
				)
				if activeErr != nil {
					return activeErr
				}
				slog.Default().InfoContext(approvalCtx, "sealed publication bootstrap fence checked", "deployment", binding.DeploymentID, "active", active)
				if !active {
					return nil
				}
			}
			plan, planErr := sealedDelivery.PlanByID(approvalCtx, publication.PlanID)
			if planErr != nil {
				return planErr
			}
			if !plan.Governance.RequiresApproval {
				slog.Default().InfoContext(approvalCtx, "sealed publication approval not required", "deployment", binding.DeploymentID)
				return nil
			}
			err := durableApproval(approvalCtx, binding, publication)
			if err != nil {
				slog.Default().ErrorContext(approvalCtx, "sealed publication approval verification failed", "deployment", binding.DeploymentID, "error", err)
			}
			return err
		}
	}
	runtime.runtimeHostModule = runtimeHostModule
	handler := Routes(routes, runtime, platformServices, policy)
	lifecycle := newRuntimeLifecycle(platformServices.workers, runtime.analyticsModule, runtime.workloads, gcMaintenance)
	return handler, lifecycle, cleanup.Close, nil
}

func allowsLocalEvaluationRuntime(production, evaluation bool) bool {
	return !production || evaluation
}

func protectedPublishingTarget(production, evaluation bool) bool {
	return production && !evaluation
}

func requiresDeliveryApproval(production, evaluation bool, operation deployment.DeliveryOperationKind) bool {
	// Restatement is the sealed implementation of an authorized operational
	// refresh. Requiring a separate deployment approval for every scheduled or
	// manually requested refresh would leave the refresh dispatcher unable to
	// complete. Publication still crosses the live RBAC authorization boundary
	// and the exact target/seal CAS; only the code/policy change approval is not
	// applicable to this data-only operation.
	return protectedPublishingTarget(production, evaluation) && operation != deployment.DeliveryOperationRestatement
}

func readClaimedProject(repository deploymentmodule.ProjectClaimReader, environment servingstatemodule.Environment) func(context.Context) (projectgraph.ResourceID, bool, error) {
	return func(ctx context.Context) (projectgraph.ResourceID, bool, error) {
		if repository == nil {
			return "", false, errors.New("project claim repository is required")
		}
		claim, err := repository.GetProjectClaim(ctx)
		if errors.Is(err, deployment.ErrProjectClaimNotFound) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("read claimed project: %w", err)
		}
		if claim.Environment != environment {
			return "", false, fmt.Errorf("claimed project environment %q does not match configured environment %q", claim.Environment, environment)
		}
		return claim.ProjectID, true, nil
	}
}

func resolveDeliveryStartupProjectID(ctx context.Context, initial projectgraph.ResourceID, readClaim func(context.Context) (projectgraph.ResourceID, bool, error)) (projectgraph.ResourceID, error) {
	if readClaim == nil {
		return "", errors.New("project claim reader is required")
	}
	claimed, found, err := readClaim(ctx)
	if err != nil {
		return "", err
	}
	if !found {
		return initial, nil
	}
	if initial != "" && claimed != initial {
		return "", fmt.Errorf("project claim changed from %q to %q", initial, claimed)
	}
	return claimed, nil
}

type claimedProjectBinder interface {
	BindClaimedProject(projectgraph.ResourceID, servingstatemodule.Environment) error
}

func bindClaimedProject(runtimeHost claimedProjectBinder, environment servingstatemodule.Environment) func(context.Context, projectgraph.ResourceID, servingstatemodule.Environment) error {
	return func(_ context.Context, projectID projectgraph.ResourceID, claimedEnvironment servingstatemodule.Environment) error {
		if claimedEnvironment != environment {
			return fmt.Errorf("claimed project environment %q does not match configured environment %q", claimedEnvironment, environment)
		}
		if runtimeHost == nil {
			return errors.New("runtime host is unavailable")
		}
		return runtimeHost.BindClaimedProject(projectID, claimedEnvironment)
	}
}

func firstConfigured(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func singletonProjectID(scopes []servingstatemodule.ActiveScope, environment servingstatemodule.Environment) (projectgraph.ResourceID, error) {
	var projectID projectgraph.ResourceID
	for _, scope := range scopes {
		if scope.Environment != environment {
			continue
		}
		if err := scope.ProjectID.Validate(); err != nil {
			return "", fmt.Errorf("active serving project identity is invalid: %w", err)
		}
		if projectID == "" {
			projectID = scope.ProjectID
			continue
		}
		if scope.ProjectID != projectID {
			return "", fmt.Errorf("active serving scopes span multiple projects: %q and %q", projectID, scope.ProjectID)
		}
	}
	return projectID, nil
}

func resolveClaimedProjectID(scopes []servingstatemodule.ActiveScope, environment servingstatemodule.Environment, claimedProjectID projectgraph.ResourceID, claimFound bool) (projectgraph.ResourceID, error) {
	projectID, err := singletonProjectID(scopes, environment)
	if err != nil {
		return "", err
	}
	if !claimFound {
		if projectID != "" {
			return "", errors.New("active serving scopes require a durable project claim")
		}
		return "", nil
	}
	if projectID != "" && projectID != claimedProjectID {
		return "", fmt.Errorf("active serving project %q does not match durable project claim %q", projectID, claimedProjectID)
	}
	return claimedProjectID, nil
}

func configuredListenURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func accessAuthConfig(cfg config.Config, production, cookieSecure bool) accessmodule.AuthConfig {
	if !production {
		return accessmodule.AuthConfig{DevBypass: true, DevAPIToken: cfg.DevAPIToken, CSRFKey: cfg.CSRFKey}
	}
	providers := []accessmodule.OIDCProviderConfig{}
	if cfg.OIDCConfigured() {
		providers = append(providers, accessmodule.OIDCProviderConfig{
			ID: cfg.OIDCProviderID, IssuerURL: cfg.OIDCIssuerURL, ClientID: cfg.OIDCClientID,
			ClientSecret: cfg.OIDCSecret, RedirectURL: cfg.OIDCCallbackURL, Scopes: cfg.OIDCScopesList(),
		})
	}
	return accessmodule.AuthConfig{
		DevBypass: cfg.DevAuthBypass, DevAPIToken: cfg.DevAPIToken, APITokenOnly: cfg.APITokenOnlyAuth,
		LocalAuth: cfg.LocalAuth, AzureClientID: cfg.AzureClientID, AzureSecret: cfg.AzureSecret,
		AzureCallback: cfg.AzureCallbackURL, AzureTenant: cfg.AzureTenant, CSRFKey: cfg.CSRFKey,
		CookieSecure: cookieSecure, BootstrapTenant: cfg.AzureTenant, OIDCProviders: providers,
	}
}

func buildSealedPublishRequest(ctx context.Context, delivery *deploymentsqlite.Repository, releases release.DeploymentLinkage, pending deploymentapiadapter.Deployment, releaseID, targetID string) (sealedcontrol.PublishRequest, error) {
	if delivery == nil || releases == nil {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("sealed delivery and release repositories are required")
	}
	released, err := releases.Get(ctx, projectgraph.ResourceID(pending.Project), releaseID)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	if released.Provenance == nil || released.ServingIdentity.GenerationID != pending.GenerationID || released.ServingIdentity.Environment != pending.Environment || released.ServingIdentity.ProjectID.String() != pending.Project {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("release is not bound to pending deployment")
	}
	candidate, err := delivery.DeliveryCandidateByID(ctx, released.Provenance.Candidate.ID)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	seal, err := delivery.DeliveryCatalogSealByID(ctx, candidate.SealID)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	if candidate.Status != deployment.DeliveryCandidateReady || seal.Status != deployment.CatalogSealVerified || candidate.TargetID != targetID || candidate.ProjectID.String() != pending.Project || candidate.Environment != pending.Environment || candidate.CatalogDigest != seal.CatalogDigest || candidate.CatalogObjectKey != seal.ObjectKey || candidate.PhysicalPoolID != seal.PhysicalPoolID || candidate.ServingArtifactID != seal.ServingArtifactID || candidate.ServingArtifactDigest != seal.ServingArtifactDigest {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("candidate and seal are not one verified publication tuple")
	}
	servingStateID := candidate.ServingStateID
	if strings.TrimSpace(servingStateID) == "" {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("candidate has no persisted serving state")
	}
	if servingStateID != pending.GenerationID {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("candidate serving state %q does not match pending generation %q", servingStateID, pending.GenerationID)
	}
	plan, err := delivery.PlanByID(ctx, candidate.PlanID)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	if plan.Digest != candidate.PlanDigest || plan.TargetID != targetID || plan.ProjectID != candidate.ProjectID || plan.Environment != candidate.Environment {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("candidate is not bound to its durable delivery plan")
	}
	createdAt, err := parseDeploymentTime(pending.CreatedAt)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	rollbackClass, rollbackUntil, rollbackEffects, err := sealedRollbackEvidence(plan, createdAt)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	generation, err := deployment.NewCatalogRoot(deployment.CatalogRoot{
		ID: pending.GenerationID, CandidateID: candidate.ID, PlanID: candidate.PlanID, PlanDigest: candidate.PlanDigest,
		TargetID: targetID, ProjectID: candidate.ProjectID, Environment: candidate.Environment,
		CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: seal.PhysicalPoolID,
		ServingStateID: servingStateID, CompatibilityDigest: candidate.CompatibilityDigest, ServingArtifactID: candidate.ServingArtifactID, ServingArtifactDigest: candidate.ServingArtifactDigest,
		RollbackClass: rollbackClass, RollbackUntil: rollbackUntil, RollbackExternalEffects: rollbackEffects, CreatedAt: createdAt,
	})
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	publication, err := deployment.NewPublicationIntent(deployment.PublicationIntent{
		ID: pending.ID, RequestDigest: pending.RequestDigest, TargetID: targetID, ProjectID: candidate.ProjectID,
		Environment: candidate.Environment, PlanID: candidate.PlanID, PlanDigest: candidate.PlanDigest,
		CandidateID: candidate.ID, GenerationID: generation.ID, ExpectedBaseGenerationID: candidate.BaseGenerationID,
		ExpectedTargetRevision: candidate.BaseTargetRevision, CreatedAt: createdAt,
	})
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	return sealedcontrol.PublishRequest{Publication: publication, Generation: generation, Seal: sealedVerifiedSeal(seal), ApprovalReleaseID: releaseID}, nil
}

// buildCanonicalPublishRequest resolves the direct plan/build/publish API's
// exact durable tuple. It deliberately does not recapture source or create a
// new serving artifact; publication is a control-plane operation over the
// ready candidate's persisted generation and verified seal.
func buildCanonicalPublishRequest(ctx context.Context, delivery canonicalPublishReader, candidateID, targetID string) (sealedcontrol.PublishRequest, error) {
	if delivery == nil || strings.TrimSpace(candidateID) == "" {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("canonical publication inputs are incomplete")
	}
	candidate, err := delivery.DeliveryCandidateByID(ctx, candidateID)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	if candidate.TargetID != targetID || candidate.Status != deployment.DeliveryCandidateReady {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("candidate is not ready on this target")
	}
	seal, err := delivery.DeliveryCatalogSealByID(ctx, candidate.SealID)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	plan, err := delivery.PlanByID(ctx, candidate.PlanID)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	if plan.Digest != candidate.PlanDigest || plan.TargetID != candidate.TargetID || plan.ProjectID != candidate.ProjectID || plan.Environment != candidate.Environment ||
		seal.Status != deployment.CatalogSealVerified || candidate.SealID != seal.ID || candidate.CatalogDigest != seal.CatalogDigest || candidate.CatalogObjectKey != seal.ObjectKey || candidate.PhysicalPoolID != seal.PhysicalPoolID || candidate.CompatibilityDigest != seal.CompatibilityDigest || candidate.ServingArtifactID != seal.ServingArtifactID || candidate.ServingArtifactDigest != seal.ServingArtifactDigest || candidate.QualificationDigest != seal.QualificationDigest || strings.TrimSpace(candidate.ServingStateID) == "" {
		return sealedcontrol.PublishRequest{}, fmt.Errorf("canonical publication tuple is not durably sealed")
	}
	createdAt := candidate.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	rollbackClass, rollbackUntil, rollbackEffects, err := sealedRollbackEvidence(plan, createdAt)
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	root, err := deployment.NewCatalogRoot(deployment.CatalogRoot{ID: candidate.ServingStateID, CandidateID: candidate.ID, PlanID: candidate.PlanID, PlanDigest: candidate.PlanDigest, TargetID: candidate.TargetID, ProjectID: candidate.ProjectID, Environment: candidate.Environment, CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: seal.PhysicalPoolID, ServingStateID: candidate.ServingStateID, CompatibilityDigest: candidate.CompatibilityDigest, ServingArtifactID: candidate.ServingArtifactID, ServingArtifactDigest: candidate.ServingArtifactDigest, RollbackClass: rollbackClass, RollbackUntil: rollbackUntil, RollbackExternalEffects: rollbackEffects, CreatedAt: createdAt})
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	// HTTP idempotency identifies one transport attempt. The durable
	// publication identity belongs to the immutable candidate so a later CLI
	// attempt can recover the exact approval and activation request after an
	// indeterminate transport outcome.
	publicationID := "publication-" + strings.TrimPrefix(
		deployment.CanonicalDeliveryDigest([]byte("candidate-publication:"+candidate.ID)),
		"sha256:",
	)
	publication, err := deployment.NewPublicationIntent(deployment.PublicationIntent{ID: publicationID, RequestDigest: deployment.CanonicalDeliveryDigest([]byte("publication:" + publicationID)), TargetID: candidate.TargetID, ProjectID: candidate.ProjectID, Environment: candidate.Environment, PlanID: candidate.PlanID, PlanDigest: candidate.PlanDigest, CandidateID: candidate.ID, GenerationID: candidate.ServingStateID, ExpectedBaseGenerationID: candidate.BaseGenerationID, ExpectedTargetRevision: candidate.BaseTargetRevision, CreatedAt: createdAt})
	if err != nil {
		return sealedcontrol.PublishRequest{}, err
	}
	return sealedcontrol.PublishRequest{Publication: publication, Generation: root, Seal: sealedVerifiedSeal(seal), ApprovalReleaseID: candidate.ServingArtifactID}, nil
}

func buildCanonicalRollbackRequest(ctx context.Context, delivery *deploymentsqlite.Repository, generationID, idempotencyKey, targetID string) (sealedcontrol.RollbackRequest, error) {
	if delivery == nil || strings.TrimSpace(generationID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return sealedcontrol.RollbackRequest{}, fmt.Errorf("canonical rollback inputs are incomplete")
	}
	generation, err := delivery.DeliveryGenerationByID(ctx, generationID)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	candidate, err := delivery.DeliveryCandidateByID(ctx, generation.CandidateID)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	seal, err := delivery.DeliveryCatalogSealByID(ctx, candidate.SealID)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	if generation.TargetID != targetID || generation.ServingStateID != generation.ID || candidate.Status != deployment.DeliveryCandidateReady || seal.Status != deployment.CatalogSealVerified {
		return sealedcontrol.RollbackRequest{}, fmt.Errorf("canonical rollback tuple is not durably sealed")
	}
	target, err := delivery.DeliveryTargetRevision(ctx, targetID)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	if strings.TrimSpace(target.ActiveGenerationID) == "" || target.TargetRevision < 0 {
		return sealedcontrol.RollbackRequest{}, fmt.Errorf("canonical rollback target fence is unavailable")
	}
	requestID := "rollback-" + strings.TrimPrefix(deployment.CanonicalDeliveryDigest([]byte("rollback:"+generationID+":"+idempotencyKey)), "sha256:")
	createdAt := time.Now().UTC()
	request := deployment.RollbackRequest{ID: requestID, RequestDigest: deployment.CanonicalDeliveryDigest([]byte("rollback-request:" + requestID)), TargetID: targetID, ProjectID: generation.ProjectID, Environment: generation.Environment, GenerationID: generation.ID, CandidateID: candidate.ID, ExpectedBaseGenerationID: target.ActiveGenerationID, ExpectedTargetRevision: target.TargetRevision, VerifiedSeal: sealedVerifiedSeal(seal), CreatedAt: createdAt}
	return sealedcontrol.RollbackRequest{Request: request}, nil
}

// sealedRollbackEvidence carries the reviewed plan's rollback contract into
// the immutable generation. Publication must never invent a rollback class or
// retention window at activation time. RetentionWindow is intentionally a
// duration string in review evidence so it remains human-readable while the
// generation stores the exact resulting deadline.
func sealedRollbackEvidence(plan deployment.DeliveryPlan, createdAt time.Time) (deployment.DeliveryRollbackClass, time.Time, []string, error) {
	evidence := plan.Evidence.Rollback
	if evidence.Class == "" {
		return "", time.Time{}, nil, fmt.Errorf("delivery plan rollback class is missing")
	}
	var until time.Time
	if strings.TrimSpace(evidence.RetentionWindow) != "" {
		window, err := time.ParseDuration(strings.TrimSpace(evidence.RetentionWindow))
		if err != nil || window <= 0 {
			return "", time.Time{}, nil, fmt.Errorf("delivery plan rollback retention window is invalid: %q", evidence.RetentionWindow)
		}
		until = createdAt.Add(window)
	}
	return evidence.Class, until, append([]string(nil), evidence.ExternalEffects...), nil
}

func buildSealedRollbackRequest(ctx context.Context, delivery *deploymentsqlite.Repository, releases release.DeploymentLinkage, pending deploymentapiadapter.Deployment, releaseID, targetID, expectedBaseGenerationID string, expectedTargetRevision int64) (sealedcontrol.RollbackRequest, error) {
	if delivery == nil || releases == nil {
		return sealedcontrol.RollbackRequest{}, fmt.Errorf("sealed delivery and release repositories are required")
	}
	released, err := releases.Get(ctx, projectgraph.ResourceID(pending.Project), releaseID)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	if released.Provenance == nil || released.ServingIdentity.GenerationID != pending.GenerationID {
		return sealedcontrol.RollbackRequest{}, fmt.Errorf("rollback release is not bound to pending deployment")
	}
	generation, err := delivery.DeliveryGenerationByID(ctx, pending.GenerationID)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	candidate, err := delivery.DeliveryCandidateByID(ctx, generation.CandidateID)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	seal, err := delivery.DeliveryCatalogSealByID(ctx, candidate.SealID)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	servingStateID := generation.ServingStateID
	if strings.TrimSpace(servingStateID) == "" {
		return sealedcontrol.RollbackRequest{}, fmt.Errorf("rollback generation has no persisted serving state")
	}
	if servingStateID != generation.ID {
		return sealedcontrol.RollbackRequest{}, fmt.Errorf("rollback generation is not bound to persisted serving state")
	}
	if expectedTargetRevision < 0 || strings.TrimSpace(expectedBaseGenerationID) == "" {
		return sealedcontrol.RollbackRequest{}, fmt.Errorf("rollback target fence was not persisted at enqueue")
	}
	createdAt, err := parseDeploymentTime(pending.CreatedAt)
	if err != nil {
		return sealedcontrol.RollbackRequest{}, err
	}
	request := deployment.RollbackRequest{ID: pending.ID, RequestDigest: pending.RequestDigest, TargetID: targetID, ProjectID: candidate.ProjectID, Environment: candidate.Environment, GenerationID: generation.ID, CandidateID: candidate.ID, ExpectedBaseGenerationID: expectedBaseGenerationID, ExpectedTargetRevision: expectedTargetRevision, VerifiedSeal: sealedVerifiedSeal(seal), CreatedAt: createdAt}
	return sealedcontrol.RollbackRequest{Request: request}, nil
}

func sealedVerifiedSeal(seal deployment.CatalogSeal) deployment.VerifiedSeal {
	return deployment.VerifiedSeal{SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.ObjectKey, ObjectSize: seal.ObjectSize, PhysicalPoolID: seal.PhysicalPoolID, CompatibilityDigest: seal.CompatibilityDigest, ClosureDigest: seal.ClosureDigest, QualificationDigest: seal.QualificationDigest, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest}
}

func parseDeploymentTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("deployment timestamp %q is not canonical", value)
}
