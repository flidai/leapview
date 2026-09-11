package module

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

const maxSemanticCatalogAuditMetadataBytes = 64 << 10

type semanticCatalogAuditMetadata struct {
	Source            string                             `json:"source"`
	Operation         string                             `json:"operation"`
	Target            semanticquery.SemanticAccessTarget `json:"target"`
	Allowed           bool                               `json:"allowed"`
	Reason            string                             `json:"reason,omitempty"`
	DecisionAvailable bool                               `json:"decisionAvailable"`
	PrincipalID       string                             `json:"principalId"`
	ActorID           string                             `json:"actorId,omitempty"`
	PolicyDigest      string                             `json:"policyDigest,omitempty"`
	DecisionDigest    string                             `json:"decisionDigest,omitempty"`
	Evidence          json.RawMessage                    `json:"evidence,omitempty"`
}

type semanticCatalogRuntime interface {
	ProjectManifest() projectmanifest.ResourceManifest
	CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
}

// SemanticCatalogVisibility uses the exact catalog lease, not a second active
// generation lookup. Resource RBAC is still required by catalog.Service.
func SemanticCatalogVisibility(instanceID string, resolve func(context.Context) (access.SemanticAttributeResolution, error), auditRecorder access.CanonicalAuditRecorder) projectcatalog.SemanticModelVisibility {
	return func(ctx context.Context, lease projectcatalog.Lease, principalID string, modelID projectgraph.ResourceID) (bool, error) {
		port, ok := lease.(interface{ Runtime() projectruntime.Runtime })
		if !ok || port.Runtime() == nil {
			return false, projectcatalog.ErrUnavailable
		}
		runtime, ok := port.Runtime().(semanticCatalogRuntime)
		if !ok {
			return false, projectcatalog.ErrUnavailable
		}
		identity := lease.Identity()
		if err := identity.Validate(); err != nil {
			return false, err
		}
		definition := runtime.ProjectManifest()
		if lease.AuthorizationSnapshot().Identity() != identity || lease.AuthorizationSnapshot().ValidateBound() != nil {
			return false, projectcatalog.ErrSnapshotChanged
		}
		model := definition.SemanticModels[modelID.String()]
		compiled, available := runtime.CompiledSemanticModel(modelID.String())
		if model == nil || !available || compiled == nil || !compiled.MatchesModel(model) {
			return false, projectcatalog.ErrUnavailable
		}
		if model.AccessPolicy.Empty() {
			return true, nil
		}
		if resolve == nil {
			return false, projectcatalog.ErrUnavailable
		}
		if auditRecorder == nil {
			return false, projectcatalog.ErrUnavailable
		}
		resource, err := access.NewResourceRef(modelID, projectgraph.KindSemanticModel)
		if err != nil {
			return false, projectcatalog.ErrUnavailable
		}
		persistObservation := semanticCatalogAuditObserver(ctx, auditRecorder, identity, resource, principalID)
		var auditFailure error
		observer := func(observation semanticquery.SemanticAccessAuditObservation) error {
			err := persistObservation(observation)
			if err != nil {
				auditFailure = err
			}
			return err
		}
		initial, err := resolve(ctx)
		if err != nil {
			return false, err
		}
		if initial.Subject.Kind != access.SubjectKindPrincipal || initial.Subject.ID != principalID {
			return false, projectcatalog.ErrUnavailable
		}
		provider := func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
			if lease.Identity() != identity {
				return semanticquery.SemanticAccessAttributeSnapshot{}, semanticquery.SemanticAccessAuthority{}, projectcatalog.ErrSnapshotChanged
			}
			resolved, err := resolve(ctx)
			if err != nil {
				return semanticquery.SemanticAccessAttributeSnapshot{}, semanticquery.SemanticAccessAuthority{}, err
			}
			return semanticquery.SemanticAccessResolutionSnapshot(instanceID, initial.Subject.ID, resolved)
		}
		consumer, err := semanticquery.NewSemanticAccessDiscovery(compiled, semanticquery.SemanticAccessConsumerConfig{
			ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
			InstanceID: instanceID, ModelID: modelID.String(), Generation: identity.GenerationID,
			PrincipalID: initial.Subject.ID, Authority: provider, Observer: observer,
		})
		if err != nil {
			return false, fmt.Errorf("semantic catalog authority: %w", err)
		}
		for _, dataset := range compiled.DatasetNames() {
			if consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: dataset}) == nil {
				return true, nil
			}
			if auditFailure != nil {
				return false, auditFailure
			}
		}
		return false, nil
	}
}

func semanticCatalogAuditObserver(ctx context.Context, recorder access.CanonicalAuditRecorder, identity projectgraph.ServingIdentity, resource access.ResourceRef, principalID string) semanticquery.SemanticAccessObserver {
	return func(observation semanticquery.SemanticAccessAuditObservation) error {
		if observation.Operation != semanticquery.SemanticAccessAuditAuthorization {
			return fmt.Errorf("semantic catalog audit operation %q is unsupported", observation.Operation)
		}
		if observation.PrincipalID == "" {
			observation.PrincipalID = principalID
		}
		if observation.PrincipalID != principalID {
			return fmt.Errorf("semantic catalog audit principal does not match request")
		}
		if observation.ActorID == "" {
			observation.ActorID = observation.PrincipalID
		}
		if !observation.DecisionAvailable || observation.PolicyDigest == "" || observation.DecisionDigest == "" || len(observation.EvidenceJSON) == 0 || !json.Valid(observation.EvidenceJSON) {
			return fmt.Errorf("semantic catalog audit decision evidence is unavailable")
		}
		var evidence map[string]json.RawMessage
		if err := json.Unmarshal(observation.EvidenceJSON, &evidence); err != nil || evidence == nil {
			return fmt.Errorf("semantic catalog audit decision evidence must be a JSON object")
		}
		metadata, err := json.Marshal(semanticCatalogAuditMetadata{
			Source: "semantic_catalog", Operation: observation.Operation, Target: observation.Target,
			Allowed: observation.Allowed, Reason: observation.Reason, DecisionAvailable: observation.DecisionAvailable,
			PrincipalID: observation.PrincipalID, ActorID: observation.ActorID,
			PolicyDigest: observation.PolicyDigest, DecisionDigest: observation.DecisionDigest,
			Evidence: json.RawMessage(append([]byte(nil), observation.EvidenceJSON...)),
		})
		if err != nil {
			return fmt.Errorf("encode semantic catalog audit metadata: %w", err)
		}
		if len(metadata) > maxSemanticCatalogAuditMetadataBytes {
			return fmt.Errorf("semantic catalog audit metadata exceeds bounded size")
		}
		status := "denied"
		if observation.Allowed {
			status = "success"
		}
		return access.PersistCanonicalAuditEvent(ctx, recorder, access.CanonicalAuditEvent{
			Identity: identity, PrincipalID: observation.PrincipalID, Action: observation.Operation,
			Resource: resource, Capability: access.CapabilityResourceUse, Status: status,
			MetadataJSON: string(metadata),
		})
	}
}
