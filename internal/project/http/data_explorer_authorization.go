package http

import (
	"context"
	"errors"
	"strings"

	"github.com/flidai/leapview/internal/access"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

var errDataExplorerSemanticAccessUnavailable = errors.New("data explorer semantic access is unavailable")

// dataExplorerAccessPredicate binds one resolved attribute context to the
// activation-owned compiled semantic policies. It intentionally performs no
// fresh model resolution or policy compilation.
func (h *BrowserHandler) dataExplorerAccessPredicate(ctx context.Context, project projectmanifest.Project, compiledModels map[string]*semanticquery.CompiledModel) (SemanticAccessPredicate, error) {
	return h.dataExplorerAccessPredicateWithAudit(ctx, project, compiledModels, nil)
}

func (h *BrowserHandler) dataExplorerAccessPredicateWithAudit(ctx context.Context, project projectmanifest.Project, compiledModels map[string]*semanticquery.CompiledModel, auditState *dataExplorerAuditState) (SemanticAccessPredicate, error) {
	policies := make(map[string]*semanticquery.CompiledSemanticAccessPolicy)
	consumers := make(map[string]*semanticquery.SemanticAccessConsumer)
	protected := false
	for modelID, model := range project.SemanticModels {
		if model == nil {
			continue
		}
		compiled := compiledModels[modelID]
		compiledPolicy := (*semanticquery.CompiledSemanticAccessPolicy)(nil)
		if compiled != nil {
			compiledPolicy = compiled.SemanticAccessPolicy()
		}
		requiresAccess := semanticquery.ModelRequiresSemanticAccess(model)
		if compiledPolicy != nil && compiledPolicy.Protected() && !requiresAccess {
			return nil, errDataExplorerSemanticAccessUnavailable
		}
		if !requiresAccess {
			continue
		}
		protected = true
		if compiled == nil || !compiled.MatchesModel(model) || compiledPolicy == nil || !compiledPolicy.Protected() {
			return nil, errDataExplorerSemanticAccessUnavailable
		}
		policies[modelID] = compiledPolicy
	}
	if !protected {
		return nil, nil
	}
	if h.ResolveSemanticAttributes == nil {
		return nil, errDataExplorerSemanticAccessUnavailable
	}
	resolution, err := h.ResolveSemanticAttributes(ctx)
	if err != nil {
		return nil, errDataExplorerSemanticAccessUnavailable
	}
	if resolution.Subject.Kind != access.SubjectKindPrincipal || strings.TrimSpace(resolution.Subject.ID) == "" || resolution.Subject.Validate() != nil {
		return nil, errDataExplorerSemanticAccessUnavailable
	}
	attributes := make([]access.EffectiveSemanticAttribute, len(resolution.Attributes))
	for index, attribute := range resolution.Attributes {
		attribute.CanonicalValues = append([]string(nil), attribute.CanonicalValues...)
		attributes[index] = attribute
	}
	evaluation := semanticquery.SemanticAccessEvaluationContext{
		RegistryState: resolution.Registry.State,
		ControlState:  resolution.ControlState,
		Attributes:    attributes,
	}
	if auditState == nil {
		return nil, errDataExplorerSemanticAccessUnavailable
	}
	if !auditState.bound || auditState.identity.Validate() != nil || auditState.identity.ProjectID.String() != project.ID || auditState.control.Validate() != nil || auditState.control.ProjectID != auditState.identity.ProjectID.String() {
		return nil, errDataExplorerSemanticAccessUnavailable
	}
	if h.SemanticAuditRecorder == nil || h.SemanticAuditActorFromContext == nil {
		return nil, errDataExplorerSemanticAccessUnavailable
	}
	actor, err := h.SemanticAuditActorFromContext(ctx)
	if err != nil || strings.TrimSpace(actor) == "" || strings.TrimSpace(actor) != resolution.Subject.ID {
		return nil, errDataExplorerSemanticAccessUnavailable
	}
	for modelID := range policies {
		model := project.SemanticModels[modelID]
		resource, err := access.NewResourceRef(projectgraph.ResourceID(modelID), projectgraph.KindSemanticModel)
		if err != nil {
			return nil, errDataExplorerSemanticAccessUnavailable
		}
		digest, err := semanticquery.SemanticModelDigest(model)
		if err != nil {
			return nil, errDataExplorerSemanticAccessUnavailable
		}
		observer, err := semanticquery.NewSemanticAuditObserver(ctx, h.SemanticAuditRecorder, semanticquery.SemanticAuditBinding{
			Event: access.CanonicalAuditEvent{
				Identity: auditState.identity, PrincipalID: resolution.Subject.ID,
				Action: access.SemanticDecisionAuditAction, Resource: resource,
				Capability: access.CapabilityResourceRead,
				RequestID:  auditState.requestID, CorrelationID: auditState.correlationID,
			},
			InstanceID: auditState.control.InstanceID, ActorPrincipalID: actor,
			SemanticModelDigest: digest,
		}, resolution)
		if err != nil {
			return nil, errDataExplorerSemanticAccessUnavailable
		}
		planner, err := semanticquery.NewSemanticAccessPlanner(compiledModels[modelID], evaluation)
		if err != nil {
			return nil, errDataExplorerSemanticAccessUnavailable
		}
		consumer, err := semanticquery.NewSemanticAccessConsumer(planner, evaluation, resolution.Subject.ID, auditState.identity.GenerationID, observer)
		if err != nil {
			return nil, errDataExplorerSemanticAccessUnavailable
		}
		consumers[modelID] = consumer
	}
	return func(modelID string, target semanticquery.SemanticAccessTarget) bool {
		model := project.SemanticModels[modelID]
		if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
			return model != nil
		}
		if auditState.err != nil {
			return false
		}
		consumer := consumers[modelID]
		if consumer == nil {
			auditState.err = errDataExplorerSemanticAccessUnavailable
			return false
		}
		decision, err := consumer.Evaluate(target)
		if err != nil {
			auditState.err = err
			return false
		}
		return decision.Allowed
	}, nil
}
