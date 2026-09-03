package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsgates "github.com/flidai/leapview/internal/analytics/gates"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapiadapter "github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
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

type sealedDeliveryGenerationReader interface {
	DeliveryGenerationByID(context.Context, string) (deployment.DeliveryGeneration, error)
	DeliveryCandidateByID(context.Context, string) (deployment.DeliveryCandidate, error)
	DeliveryCatalogSealByID(context.Context, string) (deployment.CatalogSeal, error)
}

type canonicalRollbackReader interface {
	sealedDeliveryGenerationReader
	deliveryTargetReader
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

func requiresDeliveryApproval(operation deployment.DeliveryOperationKind) bool {
	// Restatement is the sealed implementation of an authorized operational
	// refresh. Requiring a separate deployment approval for every scheduled or
	// manually requested refresh would leave the refresh dispatcher unable to
	// complete. Publication still crosses the live RBAC authorization boundary
	// and the exact target/seal CAS; only the code/policy change approval is not
	// applicable to this data-only operation.
	return operation != deployment.DeliveryOperationRestatement
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

func buildSealedPublishRequest(ctx context.Context, delivery canonicalPublishReader, releases release.DeploymentLinkage, pending deploymentapiadapter.Deployment, releaseID, targetID string) (sealedcontrol.PublishRequest, error) {
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

func buildCanonicalRollbackRequest(ctx context.Context, delivery canonicalRollbackReader, generationID, idempotencyKey, targetID string) (sealedcontrol.RollbackRequest, error) {
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

func buildSealedRollbackRequest(ctx context.Context, delivery sealedDeliveryGenerationReader, releases release.DeploymentLinkage, pending deploymentapiadapter.Deployment, releaseID, targetID, expectedBaseGenerationID string, expectedTargetRevision int64) (sealedcontrol.RollbackRequest, error) {
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
