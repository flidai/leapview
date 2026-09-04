// Package contractprojection owns the explicit leapview.contract/v1 boundary.
//
// Every wire field below comes from TypeSpec-generated projection DTOs. The
// local sealed roots keep those payloads private so graph, artifact, release,
// runtime, and publication-read values cannot enter canonicalization.
package contractprojection

import (
	"encoding/json"
	"errors"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

const Profile = "leapview.contract/v1"

type Contract = projectcontracts.ContractProjectionContract
type Metadata = projectcontracts.ContractProjectionMetadata
type AuthoritativeDefinition = projectcontracts.ContractProjectionAuthoritativeDefinition
type Deprecation = projectcontracts.ContractProjectionDeprecation
type Field = projectcontracts.ContractProjectionField
type Duration = projectcontracts.ContractProjectionDuration
type SourceFreshness = projectcontracts.ContractProjectionSourceFreshness
type SourceSchema = projectcontracts.ContractProjectionSourceSchema
type SourceContract = projectcontracts.ContractProjectionSourceBody

// Source is a sealed, projected contract. Its generated payload is private so
// callers cannot manufacture a Projection with a composite literal. Use one
// of the Project* functions to obtain a value suitable for canonicalization.
type Source struct {
	payload projectcontracts.SourceContractProjection
}

func (Source) projection() {}

// MarshalJSON preserves the generated contract wire shape while keeping the
// payload inaccessible to callers.
func (value Source) MarshalJSON() ([]byte, error) {
	return json.Marshal(value.payload)
}

// UnmarshalJSON deliberately does not provide a publication bypass. Published
// bytes are decoded into SourceView by DecodeSourcePublication instead.
func (*Source) UnmarshalJSON([]byte) error {
	return errors.New("contractprojection.Source is sealed; use ProjectSource or DecodeSourcePublication")
}

type ModelDefinition = projectcontracts.ContractProjectionModelDefinition
type ModelEntity = projectcontracts.ContractProjectionModelEntity
type ModelGrain = projectcontracts.ContractProjectionModelGrain
type ModelCheck = projectcontracts.ContractProjectionModelCheck
type ModelContract = projectcontracts.ContractProjectionModelBody

// Model is the sealed projected form of a generated Model resource.
type Model struct {
	payload projectcontracts.ModelContractProjection
}

func (Model) projection() {}

func (value Model) MarshalJSON() ([]byte, error) {
	return json.Marshal(value.payload)
}

func (*Model) UnmarshalJSON([]byte) error {
	return errors.New("contractprojection.Model is sealed; use ProjectModel or DecodeModelPublication")
}

type SemanticAccessFilter = projectcontracts.ContractProjectionSemanticAccessFilter
type SemanticDataset = projectcontracts.ContractProjectionSemanticDataset
type SemanticAccessGrant = projectcontracts.ContractProjectionSemanticAccessGrant
type RelationshipEndpoint = projectcontracts.ContractProjectionRelationshipEndpoint
type SemanticRelationship = projectcontracts.ContractProjectionSemanticRelationship
type SemanticTime = projectcontracts.ContractProjectionSemanticTime
type SemanticBinding = projectcontracts.ContractProjectionSemanticBinding
type SemanticDimension = projectcontracts.ContractProjectionSemanticDimension
type CanonicalValue = projectcontracts.ContractProjectionCanonicalValue
type SemanticFilter = projectcontracts.ContractProjectionSemanticFilter
type SemanticMetric = projectcontracts.ContractProjectionSemanticMetric
type SemanticModelContract = projectcontracts.ContractProjectionSemanticModelBody

// SemanticModel is the sealed projected form of a generated SemanticModel
// resource.
type SemanticModel struct {
	payload projectcontracts.SemanticModelContractProjection
}

func (SemanticModel) projection() {}

func (value SemanticModel) MarshalJSON() ([]byte, error) {
	return json.Marshal(value.payload)
}

func (*SemanticModel) UnmarshalJSON([]byte) error {
	return errors.New("contractprojection.SemanticModel is sealed; use ProjectSemanticModel or DecodeSemanticModelPublication")
}

// The read views intentionally remain generated DTOs. They expose no
// package-local projection marker and therefore cannot satisfy Projection.
// Consumers of already-published bytes should use these views instead of the
// sealed roots above.
type SourceView = projectcontracts.SourceContractProjection
type ModelView = projectcontracts.ModelContractProjection
type SemanticModelView = projectcontracts.SemanticModelContractProjection

// Projection is sealed to this package so canonicalization cannot be applied
// accidentally to graph, artifact, release, or runtime values.
type Projection interface {
	projection()
}

var _ Projection = Source{}
var _ Projection = Model{}
var _ Projection = SemanticModel{}
