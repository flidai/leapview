package resultidentity

import (
	"fmt"
	"sort"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// SemanticLifecycle projects existing authorities; Sequence is the ledger's
// append-only resource history sequence, never a cache-generated identifier.
// AuthorizationRevision is the instance role/grant revision used to construct
// the activation's authorization snapshot, not a fresh revision blessed at lookup.
type SemanticLifecycle struct {
	InstanceID            string                  `json:"instanceId"`
	ProjectID             projectgraph.ResourceID `json:"projectId"`
	AuthoredID            projectgraph.ResourceID `json:"authoredId"`
	ResourceKind          projectgraph.Kind       `json:"resourceKind"`
	Sequence              int64                   `json:"sequence"`
	ActiveBundleID        string                  `json:"activeBundleId"`
	PublicationVersion    string                  `json:"publicationVersion"`
	ProjectionProfile     string                  `json:"projectionProfile"`
	PublicationDigest     string                  `json:"publicationDigest"`
	AuthorizationRevision int64                   `json:"authorizationRevision"`
}

func (value SemanticLifecycle) Validate() error {
	if err := value.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: semantic project scope: %v", ErrInvalidDependency, err)
	}
	if err := validateOpaqueText(value.InstanceID); err != nil {
		return fmt.Errorf("%w: semantic instance: %v", ErrInvalidDependency, err)
	}
	if err := value.AuthoredID.Validate(); err != nil {
		return fmt.Errorf("%w: semantic authored identity: %v", ErrInvalidDependency, err)
	}
	if value.ResourceKind != projectgraph.KindSemanticModel || value.Sequence <= 0 || value.AuthorizationRevision <= 0 {
		return fmt.Errorf("%w: semantic kind and positive lifecycle/control revisions are required", ErrInvalidDependency)
	}
	if err := validateOpaqueText(value.ActiveBundleID); err != nil {
		return fmt.Errorf("%w: active bundle: %v", ErrInvalidDependency, err)
	}
	// Publication syntax/profile admission remains owned by the Project
	// adapter. Result identity retains that evidence opaquely, including
	// historical profiles, without importing a second capability's authority.
	for _, text := range []string{value.PublicationVersion, value.ProjectionProfile} {
		if err := validateOpaqueText(text); err != nil {
			return fmt.Errorf("%w: publication identity: %v", ErrInvalidDependency, err)
		}
	}
	return validateDigest("publication digest", value.PublicationDigest)
}

// SemanticAttributeIdentity retains digests, not raw attribute or claim values.
type SemanticAttributeIdentity struct {
	DefinitionID      string `json:"definitionId"`
	DefinitionName    string `json:"definitionName"`
	DefinitionVersion int64  `json:"definitionVersion"`
	Type              string `json:"type"`
	Shape             string `json:"shape"`
	Source            string `json:"source"`
	ValueDigest       string `json:"valueDigest"`
}

// SemanticAccessIdentity is an optional extension of the existing dependency
// contract. NewDependency remains the only serialization/digest authority.
// The dependency's semantic-model digest binds authored grants and filters;
// the existing query policy fingerprint separately binds actor/credential mode.
type SemanticAccessIdentity struct {
	Lifecycle        SemanticLifecycle           `json:"lifecycle"`
	ServingStateID   string                      `json:"servingStateId"`
	PrincipalID      string                      `json:"principalId"`
	PolicyProfile    string                      `json:"policyProfile"`
	RegistryRevision int64                       `json:"registryRevision"`
	RegistryDigest   string                      `json:"registryDigest"`
	ControlRevision  int64                       `json:"controlRevision"`
	ControlDigest    string                      `json:"controlDigest"`
	Attributes       []SemanticAttributeIdentity `json:"attributes"`
}

func normalizeSemanticAccess(value *SemanticAccessIdentity) (*SemanticAccessIdentity, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.Lifecycle.Validate(); err != nil {
		return nil, err
	}
	for _, text := range []string{value.ServingStateID, value.PrincipalID} {
		if err := validateOpaqueText(text); err != nil {
			return nil, fmt.Errorf("%w: semantic consumer identity: %v", ErrInvalidDependency, err)
		}
	}
	if value.PolicyProfile != semanticvalue.Profile || value.RegistryRevision <= 0 || value.ControlRevision <= 0 {
		return nil, fmt.Errorf("%w: semantic profile and positive registry/control revisions are required", ErrInvalidDependency)
	}
	for _, digest := range []string{value.RegistryDigest, value.ControlDigest} {
		if err := validateDigest("semantic control digest", digest); err != nil {
			return nil, err
		}
	}
	clone := *value
	clone.Attributes = append([]SemanticAttributeIdentity{}, value.Attributes...)
	sort.Slice(clone.Attributes, func(i, j int) bool { return clone.Attributes[i].DefinitionID < clone.Attributes[j].DefinitionID })
	for index, attribute := range clone.Attributes {
		for _, text := range []string{attribute.DefinitionID, attribute.DefinitionName} {
			if err := validateOpaqueText(text); err != nil {
				return nil, fmt.Errorf("%w: semantic attribute identity: %v", ErrInvalidDependency, err)
			}
		}
		if attribute.DefinitionVersion <= 0 || (index > 0 && clone.Attributes[index-1].DefinitionID == attribute.DefinitionID) {
			return nil, fmt.Errorf("%w: invalid or duplicate semantic attribute definition", ErrInvalidDependency)
		}
		if attribute.Source != "direct" && attribute.Source != "group" {
			return nil, fmt.Errorf("%w: semantic source has no live cache authority", ErrInvalidDependency)
		}
		if attribute.Shape != "scalar" && attribute.Shape != "list" {
			return nil, fmt.Errorf("%w: invalid semantic attribute shape", ErrInvalidDependency)
		}
		switch semanticvalue.Type(attribute.Type) {
		case semanticvalue.TypeString, semanticvalue.TypeBoolean, semanticvalue.TypeInteger, semanticvalue.TypeDecimal, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		default:
			return nil, fmt.Errorf("%w: invalid semantic attribute type", ErrInvalidDependency)
		}
		if err := validateDigest("semantic value digest", attribute.ValueDigest); err != nil {
			return nil, err
		}
	}
	return &clone, nil
}
