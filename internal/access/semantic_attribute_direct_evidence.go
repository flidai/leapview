package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/semanticvalue"
)

var ErrSemanticAttributeDirectEvidenceInvalid = errors.New("semantic attribute direct-assignment evidence is invalid")

type semanticAttributeDirectAssignmentIdentity struct {
	ID                string `json:"id"`
	AssignmentVersion int64  `json:"assignmentVersion"`
	DefinitionID      string `json:"definitionId"`
	DefinitionVersion int64  `json:"definitionVersion"`
	ValueDigest       string `json:"valueDigest"`
}

// SemanticAttributeDirectEvidence is an immutable binding between direct
// effective values, the exact FAI-637 control snapshot that authorized them,
// and the principal/group subject closure used during resolution. It exposes
// identity only and never raw values.
type SemanticAttributeDirectEvidence struct {
	instanceID  string
	principalID string
	control     SemanticAttributeControlState
	digest      string
	values      []semanticAttributeClaimValueIdentity
}

func (evidence SemanticAttributeDirectEvidence) Digest() string { return evidence.digest }

func (evidence SemanticAttributeDirectEvidence) Matches(instanceID, principalID string, control SemanticAttributeControlState, attributes []EffectiveSemanticAttribute) bool {
	if evidence.digest == "" || evidence.instanceID != instanceID || evidence.principalID != principalID || evidence.control != control {
		return false
	}
	return reflect.DeepEqual(evidence.values, semanticAttributeDirectValueIdentities(attributes))
}

// NewSemanticAttributeDirectEvidence validates and seals direct assignment
// provenance. Subjects must contain the principal and the exact group closure
// used by the resolver. Every direct-derived value must be backed by one or
// more active assignments in that closure, and conflicting assignments fail.
func NewSemanticAttributeDirectEvidence(instanceID, principalID string, subjects []SubjectRef, control SemanticAttributeControlSnapshot, attributes []EffectiveSemanticAttribute) (SemanticAttributeDirectEvidence, error) {
	if strings.TrimSpace(instanceID) != instanceID || instanceID == "" || strings.TrimSpace(principalID) != principalID || principalID == "" {
		return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: target and principal identity are required", ErrSemanticAttributeDirectEvidenceInvalid)
	}
	if err := ValidateSemanticAttributeControlSnapshot(control); err != nil {
		return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: control snapshot identity does not match its contents", ErrSemanticAttributeDirectEvidenceInvalid)
	}
	subjects = append([]SubjectRef(nil), subjects...)
	sort.Slice(subjects, func(i, j int) bool {
		if subjects[i].Kind != subjects[j].Kind {
			return subjects[i].Kind < subjects[j].Kind
		}
		return subjects[i].ID < subjects[j].ID
	})
	principalPresent := false
	allowedSubjects := make(map[SubjectRef]struct{}, len(subjects))
	for _, subject := range subjects {
		if err := subject.Validate(); err != nil {
			return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: invalid subject closure: %v", ErrSemanticAttributeDirectEvidenceInvalid, err)
		}
		if _, duplicate := allowedSubjects[subject]; duplicate {
			return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: duplicate subject in closure", ErrSemanticAttributeDirectEvidenceInvalid)
		}
		allowedSubjects[subject] = struct{}{}
		if subject.Kind == SubjectKindPrincipal && subject.ID == principalID {
			principalPresent = true
		}
	}
	if !principalPresent {
		return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: subject closure does not contain the principal", ErrSemanticAttributeDirectEvidenceInvalid)
	}

	values := semanticAttributeDirectValueIdentities(attributes)
	if len(values) == 0 {
		return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: no direct effective values", ErrSemanticAttributeDirectEvidenceInvalid)
	}
	assignments := make([]semanticAttributeDirectAssignmentIdentity, 0, len(values))
	for _, attribute := range attributes {
		if attribute.Source != "direct" && attribute.Source != "direct+trusted_claim" {
			continue
		}
		matched := false
		for _, assignment := range control.Assignments {
			if assignment.Tombstoned || assignment.DefinitionID != attribute.DefinitionID {
				continue
			}
			if _, permitted := allowedSubjects[assignment.Subject]; !permitted {
				continue
			}
			if assignment.DefinitionName != attribute.DefinitionName || assignment.DefinitionVersion <= 0 || assignment.DefinitionVersion > attribute.DefinitionVersion ||
				assignment.Type != attribute.Type || assignment.Shape != attribute.Shape || assignment.ValueDigest != attribute.ValueDigest ||
				!reflect.DeepEqual(assignment.CanonicalValues, attribute.CanonicalValues) {
				return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: assignment %q conflicts with effective attribute %q", ErrSemanticAttributeDirectEvidenceInvalid, assignment.ID, attribute.DefinitionName)
			}
			if assignment.ID == "" || assignment.AssignmentVersion <= 0 {
				return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: assignment identity is invalid", ErrSemanticAttributeDirectEvidenceInvalid)
			}
			matched = true
			assignments = append(assignments, semanticAttributeDirectAssignmentIdentity{ID: assignment.ID, AssignmentVersion: assignment.AssignmentVersion,
				DefinitionID: assignment.DefinitionID, DefinitionVersion: assignment.DefinitionVersion, ValueDigest: assignment.ValueDigest})
		}
		if !matched {
			return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: no active assignment for effective attribute %q", ErrSemanticAttributeDirectEvidenceInvalid, attribute.DefinitionName)
		}
	}
	sort.Slice(assignments, func(i, j int) bool {
		if assignments[i].ID != assignments[j].ID {
			return assignments[i].ID < assignments[j].ID
		}
		return assignments[i].AssignmentVersion < assignments[j].AssignmentVersion
	})
	wire := struct {
		Profile         string                                      `json:"profile"`
		InstanceID      string                                      `json:"instanceId"`
		PrincipalID     string                                      `json:"principalId"`
		ControlRevision int64                                       `json:"controlRevision"`
		ControlDigest   string                                      `json:"controlDigest"`
		Subjects        []SubjectRef                                `json:"subjects"`
		Assignments     []semanticAttributeDirectAssignmentIdentity `json:"assignments"`
		Values          []semanticAttributeClaimValueIdentity       `json:"values"`
	}{Profile: semanticvalue.Profile, InstanceID: instanceID, PrincipalID: principalID, ControlRevision: control.State.Revision,
		ControlDigest: control.State.Digest, Subjects: subjects, Assignments: assignments, Values: values}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return SemanticAttributeDirectEvidence{}, fmt.Errorf("%w: encode identity: %v", ErrSemanticAttributeDirectEvidenceInvalid, err)
	}
	digest, err := semanticAttributeDigestJSON(json.RawMessage(encoded), "direct evidence")
	if err != nil {
		return SemanticAttributeDirectEvidence{}, err
	}
	return SemanticAttributeDirectEvidence{instanceID: instanceID, principalID: principalID, control: control.State, digest: digest, values: values}, nil
}

func semanticAttributeDirectValueIdentities(attributes []EffectiveSemanticAttribute) []semanticAttributeClaimValueIdentity {
	values := make([]semanticAttributeClaimValueIdentity, 0, len(attributes))
	for _, attribute := range attributes {
		if attribute.Source == "direct" || attribute.Source == "direct+trusted_claim" {
			values = append(values, semanticAttributeClaimValueIdentity{DefinitionID: attribute.DefinitionID,
				DefinitionVersion: attribute.DefinitionVersion, ValueDigest: attribute.ValueDigest, Source: attribute.Source})
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].DefinitionID < values[j].DefinitionID })
	return values
}
