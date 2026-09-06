package identityledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrContractPublicationInvalid  = errors.New("invalid contract publication")
	ErrContractPublicationConflict = errors.New("contract publication conflict")
	ErrContractPublicationNotFound = errors.New("contract publication not found")
)

type ValidationOutcome string

const (
	ValidationPassed  ValidationOutcome = "passed"
	ValidationWarning ValidationOutcome = "warning"
)

type ValidationCheck struct {
	Name      string            `json:"name"`
	Outcome   ValidationOutcome `json:"outcome"`
	Reference string            `json:"reference"`
	Digest    string            `json:"digest,omitempty"`
}

type ValidationEvidence struct {
	Version        int               `json:"version"`
	Checks         []ValidationCheck `json:"checks"`
	PolicyEvidence *PolicyEvidence   `json:"policyEvidence,omitempty"`
}

// ContractPublicationInput binds already-generated projection identity to the
// existing instance-qualified authored identity ledger.
type ContractPublicationInput struct {
	InstanceID    string
	Projection    contractprojection.Projection
	Validation    ValidationEvidence
	PolicyContext *PolicyContext
}

// ContractPublication is immutable exact-replay evidence. VersionBaseline is
// the SemVer key with build metadata removed; Version preserves what was
// authored and appears in canonical bytes.
type ContractPublication struct {
	InstanceID        string
	AuthoredID        projectgraph.ResourceID
	ResourceKind      projectgraph.Kind
	Version           string
	VersionBaseline   string
	ProjectionProfile string
	CanonicalBytes    []byte
	Digest            string
	PublishedAt       time.Time
	Validation        ValidationEvidence
}

// PrepareContractPublication calls the FAI-620 canonicalization and digest
// authority and derives every remaining identity field from those exact bytes.
func PrepareContractPublication(input ContractPublicationInput) (ContractPublication, error) {
	if err := validateToken("instance id", input.InstanceID); err != nil {
		return ContractPublication{}, fmt.Errorf("%w: %v", ErrContractPublicationInvalid, err)
	}
	canonical, err := contractprojection.CanonicalBytes(input.Projection)
	if err != nil {
		return ContractPublication{}, fmt.Errorf("%w: %v", ErrContractPublicationInvalid, err)
	}
	if len(canonical) == 0 || len(canonical) > 16<<20 {
		return ContractPublication{}, fmt.Errorf("%w: canonical bytes exceed publication bounds", ErrContractPublicationInvalid)
	}
	digest, err := contractprojection.Digest(input.Projection)
	if err != nil {
		return ContractPublication{}, fmt.Errorf("%w: %v", ErrContractPublicationInvalid, err)
	}
	if err := platformdigest.ValidateSHA256Identity(digest); err != nil {
		return ContractPublication{}, fmt.Errorf("%w: %v", ErrContractPublicationInvalid, err)
	}

	var envelope struct {
		Profile  string `json:"profile"`
		Kind     string `json:"kind"`
		Metadata struct {
			ID       string `json:"id"`
			Contract struct {
				Version string `json:"version"`
			} `json:"contract"`
		} `json:"metadata"`
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	if err := decoder.Decode(&envelope); err != nil {
		return ContractPublication{}, fmt.Errorf("%w: decode canonical envelope: %v", ErrContractPublicationInvalid, err)
	}
	authoredID, err := projectgraph.NewResourceID(envelope.Metadata.ID)
	if err != nil {
		return ContractPublication{}, fmt.Errorf("%w: %v", ErrContractPublicationInvalid, err)
	}
	kind, err := publicationKind(envelope.Kind)
	if err != nil {
		return ContractPublication{}, err
	}
	baseline, err := contractversion.SemverBaseline(envelope.Metadata.Contract.Version)
	if err != nil {
		return ContractPublication{}, fmt.Errorf("%w: %v", ErrContractPublicationInvalid, err)
	}
	validation, err := normalizeValidationEvidence(input.Validation)
	if err != nil {
		return ContractPublication{}, err
	}
	return ContractPublication{
		InstanceID: input.InstanceID, AuthoredID: authoredID, ResourceKind: kind,
		Version: envelope.Metadata.Contract.Version, VersionBaseline: strings.TrimPrefix(baseline, "v"),
		ProjectionProfile: envelope.Profile, CanonicalBytes: append([]byte(nil), canonical...), Digest: digest,
		Validation: validation,
	}, nil
}

func normalizeValidationEvidence(value ValidationEvidence) (ValidationEvidence, error) {
	if value.Version != 1 || len(value.Checks) == 0 || len(value.Checks) > 256 {
		return ValidationEvidence{}, fmt.Errorf("%w: validation evidence version 1 with checks is required", ErrContractPublicationInvalid)
	}
	result := ValidationEvidence{Version: 1, Checks: append([]ValidationCheck(nil), value.Checks...), PolicyEvidence: value.PolicyEvidence}
	if value.PolicyEvidence != nil {
		policy, err := normalizePolicyEvidence(*value.PolicyEvidence)
		if err != nil {
			return ValidationEvidence{}, err
		}
		result.PolicyEvidence = &policy
	}
	for index := range result.Checks {
		check := &result.Checks[index]
		if !canonicalEvidenceText(check.Name, 128) || !canonicalEvidenceText(check.Reference, 2048) {
			return ValidationEvidence{}, fmt.Errorf("%w: validation check identity is invalid", ErrContractPublicationInvalid)
		}
		if check.Outcome != ValidationPassed && check.Outcome != ValidationWarning {
			return ValidationEvidence{}, fmt.Errorf("%w: validation check %q did not pass", ErrContractPublicationInvalid, check.Name)
		}
		if check.Digest != "" && platformdigest.ValidateSHA256Identity(check.Digest) != nil {
			return ValidationEvidence{}, fmt.Errorf("%w: validation check %q digest is invalid", ErrContractPublicationInvalid, check.Name)
		}
	}
	sort.Slice(result.Checks, func(i, j int) bool {
		if result.Checks[i].Name != result.Checks[j].Name {
			return result.Checks[i].Name < result.Checks[j].Name
		}
		return result.Checks[i].Reference < result.Checks[j].Reference
	})
	for index := 1; index < len(result.Checks); index++ {
		if result.Checks[index-1].Name == result.Checks[index].Name {
			return ValidationEvidence{}, fmt.Errorf("%w: duplicate validation check %q", ErrContractPublicationInvalid, result.Checks[index].Name)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 64<<10 {
		return ValidationEvidence{}, fmt.Errorf("%w: validation evidence exceeds publication bounds", ErrContractPublicationInvalid)
	}
	return result, nil
}

// Validate verifies an immutable validation envelope without upgrading
// historical v1 rows. A v1 envelope without PolicyEvidence is valid history,
// but it is intentionally not a qualified policy decision.
func (value ValidationEvidence) Validate() error {
	_, err := normalizeValidationEvidence(value)
	return err
}

func canonicalEvidenceText(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
}

func publicationKind(value string) (projectgraph.Kind, error) {
	switch value {
	case "Source":
		return projectgraph.KindSource, nil
	case "Model":
		return projectgraph.KindModel, nil
	case "SemanticModel":
		return projectgraph.KindSemanticModel, nil
	default:
		return "", fmt.Errorf("%w: unsupported projection kind %q", ErrContractPublicationInvalid, value)
	}
}

// EqualContractPublicationContent excludes PublishedAt because exact retries
// must return the timestamp assigned to the first successful publication.
func EqualContractPublicationContent(left, right ContractPublication) bool {
	return left.InstanceID == right.InstanceID && left.AuthoredID == right.AuthoredID && left.ResourceKind == right.ResourceKind &&
		left.Version == right.Version && left.VersionBaseline == right.VersionBaseline && left.ProjectionProfile == right.ProjectionProfile &&
		bytes.Equal(left.CanonicalBytes, right.CanonicalBytes) && left.Digest == right.Digest && reflectValidation(left.Validation, right.Validation)
}

func reflectValidation(left, right ValidationEvidence) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}
