package authz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const maxSemanticAccessAuditEvidenceBytes = 64 << 10
const maxSemanticAccessAuditMetadataTextBytes = 256
const maxSemanticAccessAuditMetadataBytes = 64 << 10

// semanticAccessAuditMetadata is deliberately a closed projection. Do not
// add request filters, predicate literals, SQL, or arbitrary error strings to
// this envelope: canonical audit is the durable authorization evidence path.
type semanticAccessAuditMetadata struct {
	Source            string                             `json:"source"`
	Operation         string                             `json:"operation"`
	Target            semanticquery.SemanticAccessTarget `json:"target"`
	Datasets          []string                           `json:"datasets,omitempty"`
	Allowed           bool                               `json:"allowed"`
	Reason            string                             `json:"reason,omitempty"`
	DecisionAvailable bool                               `json:"decisionAvailable"`
	PrincipalID       string                             `json:"principalId"`
	ActorID           string                             `json:"actorId,omitempty"`
	Surface           string                             `json:"surface,omitempty"`
	RequestOp         string                             `json:"requestOperation,omitempty"`
	PolicyDigest      string                             `json:"policyDigest,omitempty"`
	DecisionDigest    string                             `json:"decisionDigest,omitempty"`
	Evidence          json.RawMessage                    `json:"evidence,omitempty"`
}

func semanticAuditResource(project projectgraph.ProjectGraph, modelID string) (access.ResourceRef, bool) {
	if resource, ok := project.Resource(projectgraph.ResourceID(modelID)); ok && resource.Kind == projectgraph.KindSemanticModel {
		ref, err := access.NewResourceRef(resource.ID, resource.Kind)
		return ref, err == nil
	}
	// A few legacy/library adapters identify a compiled model by its declared
	// name. Resolve that name only to the one exact graph resource; the audit
	// event still carries the canonical graph ID, never the route/name alias.
	var match *projectgraph.Resource
	for _, resource := range project.Resources() {
		if resource.Kind != projectgraph.KindSemanticModel || resource.Name != modelID {
			continue
		}
		if match != nil {
			return access.ResourceRef{}, false
		}
		copy := resource
		match = &copy
	}
	if match == nil {
		return access.ResourceRef{}, false
	}
	ref, err := access.NewResourceRef(match.ID, match.Kind)
	return ref, err == nil
}

func validateSemanticAuditText(label, value string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("semantic access audit %s is required", label)
	}
	if value != strings.TrimSpace(value) || len(value) > maxSemanticAccessAuditMetadataTextBytes {
		return fmt.Errorf("semantic access audit %s is not bounded and canonical", label)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("semantic access audit %s contains a control character", label)
		}
	}
	return nil
}

func (m Metrics) semanticAuditObserver(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, modelID, principalID string) (semanticquery.SemanticAccessObserver, error) {
	if m.auditRecorder == nil {
		return nil, fmt.Errorf("protected semantic access requires canonical audit recorder")
	}
	if err := snapshot.ValidateBound(); err != nil {
		return nil, fmt.Errorf("semantic access audit snapshot: %w", err)
	}
	resource, ok := semanticAuditResource(snapshot.Project(), modelID)
	if !ok {
		return nil, fmt.Errorf("semantic access audit resource %q is unavailable", modelID)
	}
	metadata := dataquery.MetadataFromContext(ctx)
	return func(observation semanticquery.SemanticAccessAuditObservation) error {
		if strings.TrimSpace(observation.PrincipalID) == "" {
			observation.PrincipalID = principalID
		}
		if observation.PrincipalID != principalID {
			return fmt.Errorf("semantic access audit principal does not match consumer")
		}
		if observation.ActorID == "" {
			observation.ActorID = observation.PrincipalID
		}
		for _, field := range []struct {
			label      string
			value      string
			allowEmpty bool
		}{
			{label: "operation", value: observation.Operation},
			{label: "reason", value: observation.Reason, allowEmpty: true},
			{label: "principal id", value: observation.PrincipalID},
			{label: "actor id", value: observation.ActorID},
			{label: "policy digest", value: observation.PolicyDigest, allowEmpty: true},
			{label: "decision digest", value: observation.DecisionDigest, allowEmpty: true},
			{label: "surface", value: metadata.Surface, allowEmpty: true},
			{label: "request operation", value: metadata.Operation, allowEmpty: true},
		} {
			if err := validateSemanticAuditText(field.label, field.value, field.allowEmpty); err != nil {
				return err
			}
		}
		for _, field := range []struct {
			label string
			value string
		}{
			{label: "target dataset", value: observation.Target.Dataset},
			{label: "target dimension", value: observation.Target.Dimension},
			{label: "target metric", value: observation.Target.Metric},
		} {
			if err := validateSemanticAuditText(field.label, field.value, true); err != nil {
				return err
			}
		}
		for _, dataset := range observation.Datasets {
			if err := validateSemanticAuditText("plan dataset", dataset, false); err != nil {
				return err
			}
		}
		for _, field := range []struct {
			label string
			value string
		}{
			{label: "request id", value: metadata.RequestID},
			{label: "correlation id", value: metadata.CorrelationID},
		} {
			if err := validateSemanticAuditText(field.label, field.value, true); err != nil {
				return err
			}
		}
		if len(metadata.Surface) > maxSemanticAccessAuditMetadataTextBytes || len(metadata.Operation) > maxSemanticAccessAuditMetadataTextBytes {
			return fmt.Errorf("semantic access audit request metadata is unbounded")
		}
		if observation.DecisionAvailable {
			if observation.Allowed && (observation.PolicyDigest == "" || observation.DecisionDigest == "") {
				return fmt.Errorf("semantic access audit allowed decision identities are required")
			}
			if len(observation.EvidenceJSON) == 0 || len(observation.EvidenceJSON) > maxSemanticAccessAuditEvidenceBytes || !json.Valid(observation.EvidenceJSON) {
				return fmt.Errorf("semantic access audit evidence is invalid or unbounded")
			}
			var evidence map[string]json.RawMessage
			if err := json.Unmarshal(observation.EvidenceJSON, &evidence); err != nil || evidence == nil {
				return fmt.Errorf("semantic access audit evidence must be a JSON object")
			}
		} else if observation.Allowed || len(observation.EvidenceJSON) != 0 || observation.PolicyDigest != "" || observation.DecisionDigest != "" {
			return fmt.Errorf("semantic access audit invalid decision cannot carry evidence or digests")
		}
		payload, err := json.Marshal(semanticAccessAuditMetadata{
			Source: "semantic_access_consumer", Operation: observation.Operation, Target: observation.Target,
			Datasets: append([]string(nil), observation.Datasets...), Allowed: observation.Allowed,
			Reason: observation.Reason, DecisionAvailable: observation.DecisionAvailable, PrincipalID: observation.PrincipalID,
			ActorID: observation.ActorID, Surface: metadata.Surface, RequestOp: metadata.Operation,
			PolicyDigest: observation.PolicyDigest, DecisionDigest: observation.DecisionDigest,
			Evidence: json.RawMessage(append([]byte(nil), observation.EvidenceJSON...)),
		})
		if err != nil {
			return fmt.Errorf("encode semantic access audit metadata: %w", err)
		}
		if len(payload) > maxSemanticAccessAuditMetadataBytes {
			return fmt.Errorf("semantic access audit metadata exceeds bounded size")
		}
		status := "denied"
		if observation.Allowed {
			status = "success"
		}
		event := access.CanonicalAuditEvent{
			Identity: snapshot.Identity(), PrincipalID: observation.PrincipalID,
			Action: observation.Operation, Resource: resource, Capability: access.CapabilityResourceUse,
			Status: status, RequestID: metadata.RequestID, CorrelationID: metadata.CorrelationID,
			MetadataJSON: string(payload),
		}
		return access.PersistCanonicalAuditEvent(ctx, m.auditRecorder, event)
	}, nil
}
