package deployment

// This file contains the first-wave control-plane contracts for plan-driven
// project delivery.  They intentionally do not persist DuckLake table or file
// membership: the sealed catalog is the physical manifest (ADR-0009).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	"github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrDeliveryInvalid  = projectpipelineplan.ErrInvalid
	ErrDeliveryConflict = projectpipelineplan.ErrConflict
	// ErrDeliveryIdempotencyDrift is returned when a previously bound build
	// operation key is replayed with a different immutable attempt identity.
	ErrDeliveryIdempotencyDrift = errors.New("delivery idempotency key drift")
	ErrDeliveryTransition       = errors.New("invalid delivery transition")
	ErrDeliveryStale            = errors.New("delivery object is stale")
	ErrDeliveryPlanExpired      = errors.New("delivery plan has expired")
	// ErrDeliveryOutcomeUnknown is returned when durable target state proves
	// that an indeterminate publication is neither the requested commit nor a
	// proven non-commit. Callers must preserve the indeterminate row and may
	// not activate, retire, or clean the candidate as a guess.
	ErrDeliveryOutcomeUnknown = errors.New("delivery publication outcome is unknown")
)

var deliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// ValidateDeliveryID rejects aliases rather than silently normalising an
// identity. IDs are opaque, stable control-plane identities.
func ValidateDeliveryID(value string) error {
	if !deliveryIDPattern.MatchString(value) {
		return fmt.Errorf("%w: id must be 1-128 canonical identifier characters", ErrDeliveryInvalid)
	}
	return nil
}

// ValidateDeliveryDigest requires the platform's canonical sha256 identity.
func ValidateDeliveryDigest(value string) error {
	if err := digest.ValidateSHA256Identity(value); err != nil {
		return fmt.Errorf("%w: digest: %v", ErrDeliveryInvalid, err)
	}
	return nil
}

// CanonicalDeliveryDigest hashes bytes into the identity used by plans,
// artifacts, seals, and evidence. Secrets must never be supplied as bytes.
func CanonicalDeliveryDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateDeliveryText(name, value string, required bool) error {
	if value != strings.TrimSpace(value) || (required && value == "") {
		return fmt.Errorf("%w: %s must be canonical", ErrDeliveryInvalid, name)
	}
	return nil
}

// trim removes the ASCII whitespace accepted by the legacy delivery
// contracts. Keep it package-local for the lease, runtime, and GC validators
// that still canonicalize their free-form reason and object fields.
func trim(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t' || value[0] == '\n' || value[0] == '\r') {
		value = value[1:]
	}
	for len(value) > 0 && (value[len(value)-1] == ' ' || value[len(value)-1] == '\t' || value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}

func validateDeliveryTime(name string, value time.Time, required bool) error {
	if required && value.IsZero() {
		return fmt.Errorf("%w: %s is required", ErrDeliveryInvalid, name)
	}
	if !value.IsZero() && value.Location() != time.UTC {
		return fmt.Errorf("%w: %s must use the UTC location", ErrDeliveryInvalid, name)
	}
	return nil
}

func validateCatalogObjectKey(name, value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "://") {
		return fmt.Errorf("%w: %s must be a canonical relative object key", ErrDeliveryInvalid, name)
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: %s contains an unsafe path component", ErrDeliveryInvalid, name)
		}
	}
	return nil
}

func validateDeliveryScope(projectID graph.ResourceID, environment string) error {
	if err := graph.ValidateServingScope(projectID, environment); err != nil {
		return fmt.Errorf("%w: scope: %v", ErrDeliveryInvalid, err)
	}
	return nil
}

func validateOptionalDigest(name, value string) error {
	if value == "" {
		return nil
	}
	if err := validateDeliveryText(name, value, true); err != nil {
		return err
	}
	return ValidateDeliveryDigest(value)
}

// DeliveryOperationKind determines why a target-specific plan exists.
type DeliveryOperationKind string

const (
	DeliveryOperationCodeChange    DeliveryOperationKind = "code_change"
	DeliveryOperationRestatement   DeliveryOperationKind = "restatement"
	DeliveryOperationBindingChange DeliveryOperationKind = "binding_change"
	DeliveryOperationPolicyChange  DeliveryOperationKind = "policy_change"
)

func (kind DeliveryOperationKind) valid() bool {
	switch kind {
	case DeliveryOperationCodeChange, DeliveryOperationRestatement, DeliveryOperationBindingChange, DeliveryOperationPolicyChange:
		return true
	default:
		return false
	}
}

// DeliveryDataInputMode describes the reproducibility guarantee of an input.
type DeliveryDataInputMode string

const (
	DeliveryDataPinned   DeliveryDataInputMode = "pinned"
	DeliveryDataBounded  DeliveryDataInputMode = "bounded"
	DeliveryDataObserved DeliveryDataInputMode = "observed"
)

// DeliveryDataInput is a non-secret declaration attached to execution inputs.
// Pinned inputs name an immutable revision; bounded inputs name an enforced
// bound; observed inputs only carry the planning declaration and are resolved
// into candidate evidence at build time.
type DeliveryDataInput struct {
	ID          string                `json:"id"`
	Mode        DeliveryDataInputMode `json:"mode"`
	Revision    string                `json:"revision,omitempty"`
	Bound       string                `json:"bound,omitempty"`
	Explanation string                `json:"explanation,omitempty"`
}

type PipelinePlan = projectpipelineplan.Plan

// NewPipelinePlan computes the content identity after canonicalizing textual
// fields. The returned value is immutable evidence suitable for persistence.
func NewPipelinePlan(plan PipelinePlan) (PipelinePlan, error) {
	return projectpipelineplan.New(plan)
}

func (input DeliveryDataInput) canonical() DeliveryDataInput {
	input.ID, input.Revision, input.Bound, input.Explanation = strings.TrimSpace(input.ID), strings.TrimSpace(input.Revision), strings.TrimSpace(input.Bound), strings.TrimSpace(input.Explanation)
	if input.Explanation == "" {
		switch input.Mode {
		case DeliveryDataPinned:
			input.Explanation = "reads the immutable revision declared by the plan"
		case DeliveryDataBounded:
			input.Explanation = "enforces the interval or watermark declared by the plan"
		case DeliveryDataObserved:
			input.Explanation = "records the exact build-time observation; reproducibility is weaker"
		}
	}
	return input
}

func (input DeliveryDataInput) Validate() error {
	input = input.canonical()
	if err := ValidateDeliveryID(input.ID); err != nil {
		return fmt.Errorf("data input id: %w", err)
	}
	if input.Mode != DeliveryDataPinned && input.Mode != DeliveryDataBounded && input.Mode != DeliveryDataObserved {
		return fmt.Errorf("%w: unsupported data input mode %q", ErrDeliveryInvalid, input.Mode)
	}
	if input.Revision != strings.TrimSpace(input.Revision) || input.Bound != strings.TrimSpace(input.Bound) {
		return fmt.Errorf("%w: data input constraint must be canonical", ErrDeliveryInvalid)
	}
	if input.Explanation == "" {
		return fmt.Errorf("%w: data input explanation is required", ErrDeliveryInvalid)
	}
	switch input.Mode {
	case DeliveryDataPinned:
		if input.Revision == "" || input.Bound != "" {
			return fmt.Errorf("%w: pinned input requires revision only", ErrDeliveryInvalid)
		}
	case DeliveryDataBounded:
		if input.Bound == "" || input.Revision != "" {
			return fmt.Errorf("%w: bounded input requires bound only", ErrDeliveryInvalid)
		}
	case DeliveryDataObserved:
		if input.Revision != "" || input.Bound != "" {
			return fmt.Errorf("%w: observed input cannot claim a pinned or bounded value", ErrDeliveryInvalid)
		}
	}
	return nil
}

// DeliveryExecutionInputs affect computation and physical reuse. It is
// deliberately separate from provenance and governance metadata.
type DeliveryExecutionInputs struct {
	SourceArtifactDigest string `json:"sourceArtifactDigest"`
	// MaterializationDigest scopes physical execution identity to compiled
	// relation descriptors. The portable source artifact may also contain
	// dashboards or governance metadata that must not force relation rebuilds.
	MaterializationDigest string              `json:"materializationDigest,omitempty"`
	CompilerDigest        string              `json:"compilerDigest"`
	ExecutableDigest      string              `json:"executableDigest"`
	DependencyDigest      string              `json:"dependencyDigest"`
	ConfigDigest          string              `json:"configDigest"`
	BindingDigest         string              `json:"bindingDigest"`
	RuntimeDigest         string              `json:"runtimeDigest"`
	CapabilityDigest      string              `json:"capabilityDigest"`
	DataInputs            []DeliveryDataInput `json:"dataInputs,omitempty"`
}

// ContextDigest covers execution-wide inputs shared by every materialized
// relation. Binding/source/pin identities live in each relation execution
// digest, so an unrelated connection change does not invalidate unchanged
// physical references.
func (inputs DeliveryExecutionInputs) ContextDigest() (string, error) {
	return canonicalJSONDigest(struct {
		CompilerDigest   string `json:"compilerDigest"`
		ExecutableDigest string `json:"executableDigest"`
		DependencyDigest string `json:"dependencyDigest"`
		RuntimeDigest    string `json:"runtimeDigest"`
		CapabilityDigest string `json:"capabilityDigest"`
	}{inputs.CompilerDigest, inputs.ExecutableDigest, inputs.DependencyDigest, inputs.RuntimeDigest, inputs.CapabilityDigest})
}

func (inputs DeliveryExecutionInputs) Validate() error {
	digests := map[string]string{
		"source artifact": inputs.SourceArtifactDigest,
		"compiler":        inputs.CompilerDigest,
		"executable":      inputs.ExecutableDigest,
		"dependency":      inputs.DependencyDigest,
		"config":          inputs.ConfigDigest,
		"binding":         inputs.BindingDigest,
		"runtime":         inputs.RuntimeDigest,
		"capability":      inputs.CapabilityDigest,
	}
	for name, value := range digests {
		if err := ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	if inputs.MaterializationDigest != "" {
		if err := ValidateDeliveryDigest(inputs.MaterializationDigest); err != nil {
			return fmt.Errorf("materialization: %w", err)
		}
	}
	seen := make(map[string]struct{}, len(inputs.DataInputs))
	for _, input := range inputs.DataInputs {
		if err := input.Validate(); err != nil {
			return err
		}
		if _, ok := seen[input.ID]; ok {
			return fmt.Errorf("%w: duplicate data input %q", ErrDeliveryInvalid, input.ID)
		}
		seen[input.ID] = struct{}{}
	}
	return nil
}

// ExecutionDigest excludes provenance and governance, allowing safe physical
// reuse when only those concerns change.
func (inputs DeliveryExecutionInputs) ExecutionDigest() (string, error) {
	canonical := inputs
	if canonical.MaterializationDigest != "" {
		// SourceArtifactDigest remains the attested source identity for the
		// plan contract; physical reuse keys use the narrower materialization
		// identity instead.
		canonical.SourceArtifactDigest = canonical.MaterializationDigest
	}
	canonical.DataInputs = append([]DeliveryDataInput(nil), inputs.DataInputs...)
	for i := range canonical.DataInputs {
		canonical.DataInputs[i] = canonical.DataInputs[i].canonical()
	}
	sort.Slice(canonical.DataInputs, func(i, j int) bool { return canonical.DataInputs[i].ID < canonical.DataInputs[j].ID })
	if err := canonical.Validate(); err != nil {
		return "", err
	}
	return canonicalJSONDigest(canonical)
}

// DeliveryProvenance explains where portable bytes came from. It is not an
// execution identity and never contains resolved credentials.
type DeliveryProvenance struct {
	Repository        string `json:"repository,omitempty"`
	SourceRevision    string `json:"sourceRevision,omitempty"`
	Builder           string `json:"builder,omitempty"`
	BuildDefinition   string `json:"buildDefinition,omitempty"`
	AttestationDigest string `json:"attestationDigest,omitempty"`
}

func (provenance DeliveryProvenance) Validate() error {
	for name, value := range map[string]string{
		"repository": provenance.Repository, "source revision": provenance.SourceRevision,
		"builder": provenance.Builder, "build definition": provenance.BuildDefinition,
	} {
		if err := validateOptionalText(name, value); err != nil {
			return err
		}
	}
	return validateOptionalDigest("attestation digest", provenance.AttestationDigest)
}

// DeliveryGovernance controls expiry, authorization, and qualification. It
// does not alter the execution digest.
type DeliveryGovernance struct {
	PolicyDigest           string    `json:"policyDigest"`
	AuthorizationDigest    string    `json:"authorizationDigest"`
	QualificationDigest    string    `json:"qualificationDigest"`
	ExpiresAt              time.Time `json:"expiresAt"`
	RequiresApproval       bool      `json:"requiresApproval"`
	ApprovalPolicyRevision int64     `json:"approvalPolicyRevision"`
	ObservedInputsAllowed  bool      `json:"observedInputsAllowed"`
}

func (governance DeliveryGovernance) Validate() error {
	for name, value := range map[string]string{
		"policy": governance.PolicyDigest, "authorization": governance.AuthorizationDigest,
		"qualification": governance.QualificationDigest,
	} {
		if err := ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	if governance.ApprovalPolicyRevision <= 0 {
		return fmt.Errorf("%w: approval policy revision must be positive", ErrDeliveryInvalid)
	}
	return validateDeliveryTime("plan expiry", governance.ExpiresAt, true)
}

func validateOptionalText(name, value string) error {
	if value != "" && value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s must be canonical", ErrDeliveryInvalid, name)
	}
	return nil
}

func canonicalJSONDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: canonical encoding: %v", ErrDeliveryInvalid, err)
	}
	return CanonicalDeliveryDigest(encoded), nil
}
