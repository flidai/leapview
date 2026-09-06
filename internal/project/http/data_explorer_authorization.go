package http

import (
	"context"
	"errors"
	"strings"

	"github.com/flidai/leapview/internal/access"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

var errDataExplorerSemanticAccessUnavailable = errors.New("data explorer semantic access is unavailable")

// dataExplorerAccessPredicate binds one resolved attribute context to the
// activation-owned compiled semantic policies. It intentionally performs no
// fresh model resolution or policy compilation.
func (h *BrowserHandler) dataExplorerAccessPredicate(ctx context.Context, project projectmanifest.Project, compiledModels map[string]*semanticquery.CompiledModel) (SemanticAccessPredicate, error) {
	policies := make(map[string]*semanticquery.CompiledSemanticAccessPolicy)
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
	return func(modelID string, target semanticquery.SemanticAccessTarget) bool {
		model := project.SemanticModels[modelID]
		if model == nil || !semanticquery.ModelRequiresSemanticAccess(model) {
			return model != nil
		}
		policy := policies[modelID]
		return policy != nil && policy.Allows(target, evaluation)
	}, nil
}
