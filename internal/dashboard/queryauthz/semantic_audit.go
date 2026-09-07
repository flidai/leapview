package authz

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// semanticDecisionObserver binds discovery/explain decisions to the exact
// snapshot already selected by the consumer. Actor provenance comes from
// authentication and the existing authorized ViewAs capability, never a query
// payload. The observer persists through the existing canonical audit port.
func (m Metrics) semanticDecisionObserver(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, modelID string, model *semanticmodel.Model, resolution access.SemanticAttributeResolution) (semanticquery.SemanticAccessDecisionObserver, error) {
	if m.auditRecorder == nil || snapshot.ValidateBound() != nil {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	control := snapshot.AuthorizationControlRevision()
	if control.Validate() != nil || control.ProjectID != snapshot.Identity().ProjectID.String() {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	index, err := newProjectResourceIndex(snapshot.Project())
	if err != nil {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	resource, ok := index.byID(modelID, projectgraph.KindSemanticModel)
	if !ok {
		resource, ok = index.byName(modelID, projectgraph.KindSemanticModel)
	}
	if !ok {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	actor, ok := m.currentPrincipal(ctx)
	if !ok || actor.ID == "" {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	metadata := dataquery.MetadataFromContext(ctx)
	if viewAs, delegated := viewAsCapabilityFromContext(ctx); delegated {
		request := dataquery.Query{ProjectID: snapshot.Identity().ProjectID, ModelID: modelID, RequestID: metadata.RequestID, CorrelationID: metadata.CorrelationID}
		governed, err := m.authorizeViewAs(ctx, actor, request, viewAs)
		if err != nil || governed.PrincipalID != resolution.Subject.ID {
			return nil, ErrSemanticConsumerAuthorityUnavailable
		}
	} else if actor.ID != resolution.Subject.ID {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	digest, err := semanticquery.SemanticModelDigest(model)
	if err != nil {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	return semanticquery.NewSemanticAuditObserver(ctx, m.auditRecorder, semanticquery.SemanticAuditBinding{
		Event: access.CanonicalAuditEvent{
			Identity: snapshot.Identity(), PrincipalID: resolution.Subject.ID,
			Resource: resource, Capability: access.CapabilityResourceUse,
			RequestID: metadata.RequestID, CorrelationID: metadata.CorrelationID,
		},
		InstanceID: control.InstanceID, ActorPrincipalID: actor.ID, SemanticModelDigest: digest,
	}, resolution)
}
