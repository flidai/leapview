package module

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

// semanticCatalogRuntime is the definition projection retained by a serving
// runtime. It is asserted from the runtime held by the catalog lease, so the
// authored definition and compiled policy cannot be mixed across generations.
type semanticCatalogRuntime interface {
	ProjectManifest() projectmanifest.Project
	CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
}

// SemanticCatalogAuditConfig supplies the trusted actor and existing Access
// recorder for protected semantic-model discovery. It is optional for
// ordinary (unprotected) models; protected discovery fails closed when it is
// absent or incomplete.
type SemanticCatalogAuditConfig struct {
	Recorder         access.CanonicalAuditRecorder
	ActorFromContext func(context.Context) (string, error)
}

// SemanticCatalogVisibility returns the protected semantic-model visibility
// gate used by project/catalog. The callback evaluates the authenticated
// principal's durable attributes; no request-provided identity or claims are
// accepted here.
func SemanticCatalogVisibility(resolve func(context.Context) (access.SemanticAttributeResolution, error), auditConfigs ...SemanticCatalogAuditConfig) projectcatalog.SemanticModelVisibility {
	auditConfig := SemanticCatalogAuditConfig{}
	if len(auditConfigs) == 1 {
		auditConfig = auditConfigs[0]
	} else if len(auditConfigs) > 1 {
		auditConfig.Recorder = nil
	}
	return func(ctx context.Context, lease projectcatalog.Lease, modelID projectgraph.ResourceID) (bool, error) {
		runtimePort, ok := lease.(interface{ Runtime() projectruntime.Runtime })
		if !ok || runtimePort.Runtime() == nil {
			return false, fmt.Errorf("semantic catalog runtime is unavailable")
		}
		definitionPort, ok := runtimePort.Runtime().(semanticCatalogRuntime)
		if !ok {
			return false, fmt.Errorf("semantic catalog runtime definition is unavailable")
		}
		definition := definitionPort.ProjectManifest()
		if definition.ID != lease.Identity().ProjectID.String() {
			return false, fmt.Errorf("semantic catalog snapshot is stale")
		}
		model := definition.SemanticModels[modelID.String()]
		if model == nil {
			return false, nil
		}
		compiled, ok := definitionPort.CompiledSemanticModel(modelID.String())
		if !ok || compiled == nil || !compiled.MatchesModel(model) {
			return false, nil
		}
		policy := compiled.SemanticAccessPolicy()
		protected := policy != nil && policy.Protected()
		authoredProtected := semanticquery.ModelRequiresSemanticAccess(model)
		// A protection mismatch indicates a stale or malformed activation
		// projection. Never use the less restrictive side as a fallback.
		if protected != authoredProtected {
			return false, nil
		}
		if !protected {
			return true, nil
		}
		if resolve == nil {
			return false, nil
		}
		resolution, err := resolve(ctx)
		if err != nil || resolution.Subject.Kind != access.SubjectKindPrincipal || strings.TrimSpace(resolution.Subject.ID) == "" {
			return false, nil
		}
		evaluation := semanticquery.SemanticAccessEvaluationContext{
			RegistryState: resolution.Registry.State,
			ControlState:  resolution.ControlState,
			Attributes:    resolution.Attributes,
		}
		if auditConfig.Recorder == nil || auditConfig.ActorFromContext == nil {
			return false, nil
		}
		actor, err := auditConfig.ActorFromContext(ctx)
		if err != nil || strings.TrimSpace(actor) == "" || strings.TrimSpace(actor) != resolution.Subject.ID {
			return false, nil
		}
		identity := lease.Identity()
		snapshot := lease.AuthorizationSnapshot()
		if identity.Validate() != nil || snapshot.Identity() != identity {
			return false, fmt.Errorf("semantic catalog authorization snapshot is stale")
		}
		control := snapshot.AuthorizationControlRevision()
		if control.Validate() != nil || control.ProjectID != identity.ProjectID.String() {
			return false, fmt.Errorf("semantic catalog authorization control is unavailable")
		}
		resource, err := access.NewResourceRef(modelID, projectgraph.KindSemanticModel)
		if err != nil {
			return false, fmt.Errorf("semantic catalog model resource is invalid")
		}
		digest, err := semanticquery.SemanticModelDigest(model)
		if err != nil {
			return false, fmt.Errorf("semantic catalog semantic model identity is unavailable")
		}
		metadata := dataquery.MetadataFromContext(ctx)
		observer, err := semanticquery.NewSemanticAuditObserver(ctx, auditConfig.Recorder, semanticquery.SemanticAuditBinding{
			Event: access.CanonicalAuditEvent{
				Identity: identity, PrincipalID: resolution.Subject.ID,
				Action: access.SemanticDecisionAuditAction, Resource: resource,
				Capability: access.CapabilityResourceRead,
				RequestID:  metadata.RequestID, CorrelationID: metadata.CorrelationID,
			},
			InstanceID: control.InstanceID, ActorPrincipalID: actor,
			SemanticModelDigest: digest,
		}, resolution)
		if err != nil {
			return false, err
		}
		planner, err := semanticquery.NewSemanticAccessPlanner(compiled, evaluation)
		if err != nil {
			return false, err
		}
		consumer, err := semanticquery.NewSemanticAccessConsumer(planner, evaluation, resolution.Subject.ID, identity.GenerationID, observer)
		if err != nil {
			return false, err
		}
		for _, dataset := range compiled.DatasetNames() {
			decision, err := consumer.Evaluate(semanticquery.SemanticAccessTarget{Dataset: dataset})
			if err != nil {
				return false, err
			}
			if decision.Allowed {
				return true, nil
			}
		}
		return false, nil
	}
}
