// Package contractprojection owns the explicit leapview.contract/v1 boundary.
//
// Every wire field below comes from TypeSpec-generated projection DTOs. The
// three local defined types only seal those generated roots to this package so
// graph, artifact, release, and runtime values cannot enter canonicalization.
package contractprojection

import projectcontracts "github.com/flidai/leapview/internal/project/contracts"

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

type Source projectcontracts.SourceContractProjection

func (Source) projection() {}

type ModelDefinition = projectcontracts.ContractProjectionModelDefinition
type ModelEntity = projectcontracts.ContractProjectionModelEntity
type ModelGrain = projectcontracts.ContractProjectionModelGrain
type ModelCheck = projectcontracts.ContractProjectionModelCheck
type ModelContract = projectcontracts.ContractProjectionModelBody

type Model projectcontracts.ModelContractProjection

func (Model) projection() {}

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

type SemanticModel projectcontracts.SemanticModelContractProjection

func (SemanticModel) projection() {}

// Projection is sealed to this package so canonicalization cannot be applied
// accidentally to graph, artifact, release, or runtime values.
type Projection interface {
	projection()
}
