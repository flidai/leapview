// Package pipelineplan owns the immutable generation-bound execution
// selection shared by refresh orchestration and canonical delivery.
package pipelineplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/platform/digest"
)

var (
	ErrInvalid  = errors.New("invalid delivery contract")
	ErrConflict = errors.New("delivery transition conflict")
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// Plan is the complete immutable pipeline closure carried from refresh
// admission through delivery publication. MaterializationScope and
// ModelExecutionOrder are dependency ordered and are never sorted.
type Plan struct {
	ID                   string   `json:"id"`
	PipelineID           string   `json:"pipelineId"`
	ProjectID            string   `json:"projectId"`
	Environment          string   `json:"environment"`
	SemanticModelID      string   `json:"semanticModelId"`
	SelectedResourceType string   `json:"selectedResourceType"`
	SelectedResourceID   string   `json:"selectedResourceId"`
	ServingGenerationID  string   `json:"servingGenerationId"`
	ArtifactDigest       string   `json:"artifactDigest"`
	SelectionDigest      string   `json:"selectionDigest"`
	MaterializationScope []string `json:"materializationScope"`
	ModelExecutionOrder  []string `json:"modelExecutionOrder"`
	SourceInputs         []string `json:"sourceInputs,omitempty"`
	QualificationChecks  []string `json:"qualificationChecks"`
	TriggerType          string   `json:"triggerType,omitempty"`
	TriggerID            string   `json:"triggerId,omitempty"`
	MissedOccurrences    string   `json:"missedOccurrences,omitempty"`
	Overlap              string   `json:"overlap,omitempty"`
	RequestedStart       string   `json:"requestedStart,omitempty"`
	RequestedEnd         string   `json:"requestedEnd,omitempty"`
	RequestedWatermark   string   `json:"requestedWatermark,omitempty"`
	EffectiveStart       string   `json:"effectiveStart,omitempty"`
	EffectiveEnd         string   `json:"effectiveEnd,omitempty"`
	EffectiveWatermark   string   `json:"effectiveWatermark,omitempty"`
	ExecutionDigest      string   `json:"executionDigest"`
	ProvenanceDigest     string   `json:"provenanceDigest"`
	GovernanceDigest     string   `json:"governanceDigest"`
	EvidenceDigest       string   `json:"evidenceDigest"`
	Digest               string   `json:"digest"`
}

// Canonical returns a detached canonical copy suitable for validation and
// persistence.
func (p Plan) Canonical() Plan {
	p.ID = strings.TrimSpace(p.ID)
	p.PipelineID = strings.TrimSpace(p.PipelineID)
	p.ProjectID = strings.TrimSpace(p.ProjectID)
	p.Environment = strings.TrimSpace(p.Environment)
	p.SemanticModelID = strings.TrimSpace(p.SemanticModelID)
	p.SelectedResourceType = strings.TrimSpace(p.SelectedResourceType)
	p.SelectedResourceID = strings.TrimSpace(p.SelectedResourceID)
	p.ServingGenerationID = strings.TrimSpace(p.ServingGenerationID)
	p.ArtifactDigest = strings.TrimSpace(p.ArtifactDigest)
	p.SelectionDigest = strings.TrimSpace(p.SelectionDigest)
	p.TriggerType = strings.TrimSpace(p.TriggerType)
	p.TriggerID = strings.TrimSpace(p.TriggerID)
	p.MissedOccurrences = strings.TrimSpace(p.MissedOccurrences)
	p.Overlap = strings.TrimSpace(p.Overlap)
	p.RequestedStart = strings.TrimSpace(p.RequestedStart)
	p.RequestedEnd = strings.TrimSpace(p.RequestedEnd)
	p.RequestedWatermark = strings.TrimSpace(p.RequestedWatermark)
	p.EffectiveStart = strings.TrimSpace(p.EffectiveStart)
	p.EffectiveEnd = strings.TrimSpace(p.EffectiveEnd)
	p.EffectiveWatermark = strings.TrimSpace(p.EffectiveWatermark)
	p.ExecutionDigest = strings.TrimSpace(p.ExecutionDigest)
	p.ProvenanceDigest = strings.TrimSpace(p.ProvenanceDigest)
	p.GovernanceDigest = strings.TrimSpace(p.GovernanceDigest)
	p.EvidenceDigest = strings.TrimSpace(p.EvidenceDigest)
	p.Digest = strings.TrimSpace(p.Digest)
	p.MaterializationScope = canonicalOrderedList(p.MaterializationScope)
	p.ModelExecutionOrder = canonicalOrderedList(p.ModelExecutionOrder)
	p.SourceInputs = canonicalSet(p.SourceInputs)
	p.QualificationChecks = canonicalSet(p.QualificationChecks)
	return p
}

func (p Plan) Validate() error {
	p = p.Canonical()
	if err := p.ValidateWithoutDigest(); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"execution": p.ExecutionDigest, "provenance": p.ProvenanceDigest,
		"governance": p.GovernanceDigest, "evidence": p.EvidenceDigest, "plan": p.Digest,
	} {
		if err := validateDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	execution, provenance, governance, evidence, err := componentDigests(p)
	if err != nil || execution != p.ExecutionDigest || provenance != p.ProvenanceDigest || governance != p.GovernanceDigest || evidence != p.EvidenceDigest {
		return fmt.Errorf("%w: pipeline plan component digests do not match canonical inputs", ErrConflict)
	}
	expected, err := canonicalDigest(p)
	if err != nil || expected != p.Digest {
		return fmt.Errorf("%w: pipeline plan digest does not match canonical inputs", ErrConflict)
	}
	return nil
}

func (p Plan) ValidateWithoutDigest() error {
	p = p.Canonical()
	for name, value := range map[string]string{
		"pipeline plan": p.ID, "pipeline": p.PipelineID, "project": p.ProjectID,
		"semantic model": p.SemanticModelID, "selected resource": p.SelectedResourceID,
		"serving generation": p.ServingGenerationID,
	} {
		if err := validateID(value); err != nil {
			return fmt.Errorf("%s id: %w", name, err)
		}
	}
	if p.Environment == "" || p.Environment != strings.TrimSpace(p.Environment) {
		return fmt.Errorf("%w: environment is required and must be canonical", ErrInvalid)
	}
	if p.SelectedResourceType != "semanticModel" || p.SelectedResourceID != p.SemanticModelID {
		return fmt.Errorf("%w: selected resource must be the semantic model", ErrInvalid)
	}
	for name, value := range map[string]string{"artifact": p.ArtifactDigest, "selection": p.SelectionDigest} {
		if err := validateDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	if len(p.MaterializationScope) == 0 {
		return fmt.Errorf("%w: pipeline materialization scope is empty", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(p.MaterializationScope))
	for _, relation := range p.MaterializationScope {
		if err := validateID(relation); err != nil {
			return fmt.Errorf("materialization scope relation: %w", err)
		}
		if _, ok := seen[relation]; ok {
			return fmt.Errorf("%w: duplicate materialization scope relation %q", ErrInvalid, relation)
		}
		seen[relation] = struct{}{}
	}
	if len(p.ModelExecutionOrder) != len(p.MaterializationScope) {
		return fmt.Errorf("%w: model execution order must equal materialization scope", ErrInvalid)
	}
	for i := range p.MaterializationScope {
		if p.ModelExecutionOrder[i] != p.MaterializationScope[i] {
			return fmt.Errorf("%w: model execution order differs from materialization scope", ErrInvalid)
		}
	}
	for _, source := range p.SourceInputs {
		if err := validateID(source); err != nil {
			return fmt.Errorf("source input: %w", err)
		}
	}
	if len(p.QualificationChecks) == 0 {
		return fmt.Errorf("%w: qualification checks are required", ErrInvalid)
	}
	for _, check := range p.QualificationChecks {
		if err := validateID(check); err != nil {
			return fmt.Errorf("qualification check: %w", err)
		}
	}
	if p.TriggerType != "" || p.TriggerID != "" || p.Overlap != "" {
		if p.TriggerType != "manual" && p.TriggerType != "schedule" && p.TriggerType != "retry" {
			return fmt.Errorf("%w: unsupported effective trigger type %q", ErrInvalid, p.TriggerType)
		}
		if err := validateID(p.TriggerID); err != nil {
			return fmt.Errorf("effective trigger: %w", err)
		}
		if p.Overlap != "forbid" && p.Overlap != "replace" {
			return fmt.Errorf("%w: overlap must be forbid or replace", ErrInvalid)
		}
		if p.TriggerType == "schedule" && p.MissedOccurrences != "skip" && p.MissedOccurrences != "latest" {
			return fmt.Errorf("%w: scheduled plans require missed-occurrence policy", ErrInvalid)
		}
		if p.TriggerType != "schedule" && p.MissedOccurrences != "" {
			return fmt.Errorf("%w: missed-occurrence policy belongs to schedule triggers", ErrInvalid)
		}
	}
	return nil
}

// New canonicalizes a plan and assigns every digest family. Qualification
// defaults mirror canonical delivery's mandatory checks; invocation policy is
// supplied by refresh admission when the plan is attached to a run.
func New(plan Plan) (Plan, error) {
	plan = plan.Canonical()
	if plan.Digest != "" || plan.ExecutionDigest != "" || plan.ProvenanceDigest != "" || plan.GovernanceDigest != "" || plan.EvidenceDigest != "" {
		return Plan{}, fmt.Errorf("%w: pipeline plan digests are assigned by constructor", ErrInvalid)
	}
	if plan.SelectedResourceType == "" {
		plan.SelectedResourceType = "semanticModel"
	}
	if plan.SelectedResourceID == "" {
		plan.SelectedResourceID = plan.SemanticModelID
	}
	if len(plan.ModelExecutionOrder) == 0 {
		plan.ModelExecutionOrder = append([]string(nil), plan.MaterializationScope...)
	}
	if len(plan.QualificationChecks) == 0 {
		plan.QualificationChecks = []string{"compatibility", "schema-closure"}
	}
	plan = plan.Canonical()
	if err := plan.ValidateWithoutDigest(); err != nil {
		return Plan{}, err
	}
	var err error
	plan.ExecutionDigest, plan.ProvenanceDigest, plan.GovernanceDigest, plan.EvidenceDigest, err = componentDigests(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.Digest, err = canonicalDigest(plan)
	if err != nil {
		return Plan{}, err
	}
	return plan, plan.Validate()
}

func componentDigests(plan Plan) (string, string, string, string, error) {
	execution, err := digestJSON(struct {
		PipelineID, ProjectID, Environment, SemanticModelID, SelectedResourceType, SelectedResourceID, ServingGenerationID, SelectionDigest string
		MaterializationScope, ModelExecutionOrder, SourceInputs                                                                             []string
		RequestedStart, RequestedEnd, RequestedWatermark, EffectiveStart, EffectiveEnd, EffectiveWatermark                                  string
	}{plan.PipelineID, plan.ProjectID, plan.Environment, plan.SemanticModelID, plan.SelectedResourceType, plan.SelectedResourceID, plan.ServingGenerationID, plan.SelectionDigest, plan.MaterializationScope, plan.ModelExecutionOrder, plan.SourceInputs, plan.RequestedStart, plan.RequestedEnd, plan.RequestedWatermark, plan.EffectiveStart, plan.EffectiveEnd, plan.EffectiveWatermark})
	if err != nil {
		return "", "", "", "", err
	}
	provenance, err := digestJSON(struct {
		ArtifactDigest string
		SourceInputs   []string
	}{plan.ArtifactDigest, plan.SourceInputs})
	if err != nil {
		return "", "", "", "", err
	}
	governance, err := digestJSON(struct{ TriggerType, TriggerID, MissedOccurrences, Overlap string }{plan.TriggerType, plan.TriggerID, plan.MissedOccurrences, plan.Overlap})
	if err != nil {
		return "", "", "", "", err
	}
	evidence, err := digestJSON(struct{ QualificationChecks []string }{plan.QualificationChecks})
	return execution, provenance, governance, evidence, err
}

func canonicalDigest(plan Plan) (string, error) {
	return digestJSON(struct {
		ID               string
		ExecutionDigest  string
		ProvenanceDigest string
		GovernanceDigest string
		EvidenceDigest   string
	}{plan.ID, plan.ExecutionDigest, plan.ProvenanceDigest, plan.GovernanceDigest, plan.EvidenceDigest})
}

func digestJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: encode pipeline plan component: %v", ErrInvalid, err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateID(value string) error {
	if !idPattern.MatchString(value) {
		return fmt.Errorf("%w: id must be 1-128 canonical identifier characters", ErrInvalid)
	}
	return nil
}

func validateDigest(value string) error {
	if err := digest.ValidateSHA256Identity(value); err != nil {
		return fmt.Errorf("%w: digest: %v", ErrInvalid, err)
	}
	return nil
}

func canonicalOrderedList(values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.TrimSpace(value)
	}
	return result
}

func canonicalSet(values []string) []string {
	result := canonicalOrderedList(values)
	sort.Strings(result)
	return result
}
