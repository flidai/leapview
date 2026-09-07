package query

import (
	"context"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/project/graph"
)

// SemanticAuditBinding is supplied by the existing consumer's trusted serving
// and principal boundary. Event carries the canonical resource/request scope;
// it is not populated from a browser's claimed authorization evidence.
type SemanticAuditBinding struct {
	Event               access.CanonicalAuditEvent
	InstanceID          string
	ActorPrincipalID    string
	SemanticModelDigest string
}

// NewSemanticAuditObserver projects already-evaluated decisions into Access's
// canonical audit authority. It neither evaluates policy nor computes a digest.
// Each observation is appended before its consumer can admit a protected plan
// or discovery result. An audit failure is a consumer failure, not an allow.
func NewSemanticAuditObserver(ctx context.Context, recorder access.CanonicalAuditRecorder, binding SemanticAuditBinding, resolution access.SemanticAttributeResolution) (SemanticAccessDecisionObserver, error) {
	if ctx == nil || recorder == nil {
		return nil, fmt.Errorf("semantic audit authority is unavailable")
	}
	if err := binding.Event.Identity.Validate(); err != nil {
		return nil, fmt.Errorf("semantic audit serving identity is invalid")
	}
	if err := binding.Event.Resource.Validate(); err != nil || binding.Event.Resource.Kind() != graph.KindSemanticModel {
		return nil, fmt.Errorf("semantic audit model identity is invalid")
	}
	if resolution.Subject.Kind != access.SubjectKindPrincipal || resolution.Subject.ID == "" || binding.Event.PrincipalID != resolution.Subject.ID {
		return nil, fmt.Errorf("semantic audit principal is not bound to resolved authority")
	}
	base := access.SemanticDecisionEvidence{
		Version: 1, InstanceID: binding.InstanceID, ActorPrincipalID: binding.ActorPrincipalID,
		SemanticModelDigest: binding.SemanticModelDigest,
		Registry:            access.SemanticAuditRevision{Profile: resolution.Registry.State.Profile, Revision: resolution.Registry.State.Revision, Digest: resolution.Registry.State.Digest},
		Control:             access.SemanticAuditRevision{Profile: resolution.ControlState.Profile, Revision: resolution.ControlState.Revision, Digest: resolution.ControlState.Digest},
	}
	for _, attribute := range resolution.Attributes {
		base.Attributes = append(base.Attributes, access.SemanticAuditAttribute{
			DefinitionID: attribute.DefinitionID, DefinitionName: attribute.DefinitionName,
			DefinitionVersion: attribute.DefinitionVersion, Type: string(attribute.Type), Shape: string(attribute.Shape),
			Source: attribute.Source, ValueDigest: attribute.ValueDigest,
		})
	}
	sort.Slice(base.Attributes, func(i, j int) bool { return base.Attributes[i].DefinitionID < base.Attributes[j].DefinitionID })
	if err := base.ValidateBinding(); err != nil {
		return nil, fmt.Errorf("semantic audit authority evidence is invalid")
	}
	return func(observation SemanticAccessObservation) error {
		evidence := base
		evidence.Target = access.SemanticAuditTarget{Dataset: observation.Target.Dataset, Dimension: observation.Target.Dimension, Metric: observation.Target.Metric}
		evidence.Allowed, evidence.Reason = observation.Allowed, observation.Reason
		for _, grant := range observation.GrantOutcomes {
			evidence.Grants = append(evidence.Grants, access.SemanticAuditGrant{
				Grant: grant.Grant, UserAttribute: grant.UserAttribute, AttributeDefinitionID: grant.AttributeDefinitionID,
				AttributeDefinitionVersion: grant.AttributeDefinitionVersion, Satisfied: grant.Satisfied,
			})
		}
		for _, filter := range observation.AppliedFilters {
			evidence.Filters = append(evidence.Filters, access.SemanticAuditFilter{
				Dataset: filter.Dataset, Dimension: filter.Dimension, UserAttribute: filter.UserAttribute,
				Identity: filter.Identity, AttributeDefinitionID: filter.AttributeDefinitionID,
				AttributeDefinitionVersion: filter.AttributeDefinitionVersion, Applied: filter.Applied,
			})
		}
		sort.Slice(evidence.Grants, func(i, j int) bool { return evidence.Grants[i].Grant < evidence.Grants[j].Grant })
		sort.Slice(evidence.Filters, func(i, j int) bool { return evidence.Filters[i].Identity < evidence.Filters[j].Identity })
		metadata, err := evidence.MetadataJSON()
		if err != nil {
			return fmt.Errorf("semantic audit evidence is invalid")
		}
		event := binding.Event
		event.Action, event.MetadataJSON = access.SemanticDecisionAuditAction, metadata
		event.Status = "denied"
		if observation.Allowed {
			event.Status = "success"
		}
		if err := access.PersistCanonicalAuditEvent(ctx, recorder, event); err != nil {
			// Recorder errors can include storage details. Do not expose them to
			// semantic consumers or copy them into a subsequent audit payload.
			return fmt.Errorf("semantic audit persistence failed")
		}
		return nil
	}, nil
}
