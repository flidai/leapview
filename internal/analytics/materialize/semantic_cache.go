package materialize

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

var (
	errSemanticCacheUnavailable = errors.New("semantic cache qualification is unavailable")
	errSemanticCacheMismatch    = errors.New("semantic cache authority is stale")
)

// SemanticCacheConfig binds protected result reuse to the activation-owned
// lifecycle ledger and a live authority reader. The binding is supplied by
// activation; it is never inferred from the first ReadCurrent result.
type SemanticCacheConfig struct {
	Binding     resultidentity.SemanticLifecycle
	ReadCurrent func(context.Context) (resultidentity.SemanticLifecycle, error)
}

func cloneSemanticCacheConfig(config *SemanticCacheConfig) *SemanticCacheConfig {
	if config == nil {
		return nil
	}
	clone := *config
	return &clone
}

func validateSemanticCacheConfig(config *SemanticCacheConfig) error {
	if config == nil {
		return nil
	}
	if err := config.Binding.Validate(); err != nil {
		return fmt.Errorf("semantic cache binding: %w", err)
	}
	if config.ReadCurrent == nil {
		return fmt.Errorf("semantic cache current lifecycle reader is required")
	}
	return nil
}

// semanticCacheIdentity qualifies one admitted protected request for result
// reuse. A source outside direct/group is an explicit cache bypass: no live
// trusted-claim envelope exists at this boundary, so it must not be invented.
func (r *Runtime) semanticCacheIdentity(ctx context.Context, request dataquery.Query) (*resultidentity.SemanticAccessIdentity, bool, error) {
	if r == nil || r.semanticConsumer == nil || r.semanticCache == nil {
		return nil, false, nil
	}
	config := r.semanticCache
	if err := validateSemanticCacheConfig(config); err != nil {
		return nil, false, err
	}
	binding := config.Binding
	if binding.AuthoredID != projectgraph.ResourceID(r.modelID) || binding.ResourceKind != projectgraph.KindSemanticModel {
		return nil, false, fmt.Errorf("%w: lifecycle model binding does not match runtime", errSemanticCacheUnavailable)
	}
	if strings.TrimSpace(r.servingStateID) == "" || r.resultPartition.Version() != resultidentity.PartitionVersion {
		return nil, false, fmt.Errorf("%w: serving identity is unavailable", errSemanticCacheUnavailable)
	}
	if binding.ProjectID != r.resultPartition.ProjectID() {
		return nil, false, fmt.Errorf("%w: lifecycle project does not match runtime partition", errSemanticCacheUnavailable)
	}
	if request.ModelID != "" && request.ModelID != r.modelID {
		return nil, false, fmt.Errorf("%w: request model does not match runtime", errSemanticCacheUnavailable)
	}
	if request.ProjectID != "" && request.ProjectID != r.resultPartition.ProjectID() {
		return nil, false, fmt.Errorf("%w: request partition does not match runtime", errSemanticCacheUnavailable)
	}
	modelDigest, err := semanticquery.SemanticModelDigest(r.model)
	if err != nil {
		return nil, false, fmt.Errorf("%w: semantic model identity is unavailable", errSemanticCacheUnavailable)
	}
	if !r.dependencyEvidence.MatchesSemanticModel(projectgraph.ResourceID(r.modelID), modelDigest) {
		return nil, false, fmt.Errorf("%w: activation evidence does not match semantic model", errSemanticCacheUnavailable)
	}
	if err := r.validateSemanticResolution(ctx); err != nil {
		return nil, false, err
	}
	if r.semanticResolution.Subject.Kind != access.SubjectKindPrincipal || strings.TrimSpace(r.semanticResolution.Subject.ID) == "" {
		return nil, false, fmt.Errorf("%w: authenticated principal is unavailable", errSemanticCacheUnavailable)
	}
	if r.semanticResolution.Registry.State.Profile != semanticvalue.Profile || r.semanticResolution.ControlState.Profile != semanticvalue.Profile {
		return nil, false, fmt.Errorf("%w: semantic authority profile is invalid", errSemanticCacheUnavailable)
	}
	attributes := make([]resultidentity.SemanticAttributeIdentity, 0, len(r.semanticResolution.Attributes))
	for _, attribute := range r.semanticResolution.Attributes {
		if attribute.Source != "direct" && attribute.Source != "group" {
			return nil, false, nil
		}
		attributes = append(attributes, resultidentity.SemanticAttributeIdentity{
			DefinitionID: attribute.DefinitionID, DefinitionName: attribute.DefinitionName,
			DefinitionVersion: attribute.DefinitionVersion, Type: string(attribute.Type), Shape: string(attribute.Shape),
			Source: attribute.Source, ValueDigest: attribute.ValueDigest,
		})
	}
	identity := &resultidentity.SemanticAccessIdentity{
		Lifecycle: binding, ServingStateID: r.servingStateID,
		PrincipalID: r.semanticResolution.Subject.ID, PolicyProfile: r.semanticResolution.Registry.State.Profile,
		RegistryRevision: r.semanticResolution.Registry.State.Revision, RegistryDigest: r.semanticResolution.Registry.State.Digest,
		ControlRevision: r.semanticResolution.ControlState.Revision, ControlDigest: r.semanticResolution.ControlState.Digest,
		Attributes: attributes,
	}
	return identity, true, nil
}

func (r *Runtime) validateSemanticCacheLifecycle(ctx context.Context) error {
	if r == nil || r.semanticCache == nil {
		return nil
	}
	if err := validateSemanticCacheConfig(r.semanticCache); err != nil {
		return err
	}
	current, err := r.semanticCache.ReadCurrent(ctx)
	if err != nil {
		return fmt.Errorf("%w: read current lifecycle: %v", errSemanticCacheMismatch, err)
	}
	if current != r.semanticCache.Binding {
		return fmt.Errorf("%w: current lifecycle differs from activation binding", errSemanticCacheMismatch)
	}
	return nil
}

func (r *Runtime) validateSemanticCache(ctx context.Context, request dataquery.Query) error {
	_, reusable, err := r.semanticCacheIdentity(ctx, request)
	if err != nil {
		return err
	}
	if !reusable {
		return errSemanticCacheUnavailable
	}
	return nil
}
