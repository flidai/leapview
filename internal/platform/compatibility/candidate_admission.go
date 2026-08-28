package compatibility

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/platform/ociref"
)

const candidateAdmissionMaxBytes = 64 * 1024

// CandidateAdmissionEvidence is the release workflow's immutable OCI
// admission output. Transition-policy binding and downstream qualification
// share this owner validator so neither can accept a weaker projection.
type CandidateAdmissionEvidence struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Image          string `json:"image"`
	Digest         string `json:"digest"`
	RegistryDigest string `json:"registryDigest"`
	Attestation    struct {
		Verified       bool   `json:"verified"`
		Repository     string `json:"repository"`
		Workflow       string `json:"workflow"`
		SourceRevision string `json:"sourceRevision"`
	} `json:"attestation"`
	SBOM struct {
		Discoverable bool `json:"discoverable"`
	} `json:"sbom"`
	VulnerabilityPolicy struct {
		Passed bool `json:"passed"`
	} `json:"vulnerabilityPolicy"`
}

// ValidateCandidateAdmissionEvidence binds the exact admitted registry digest
// and verified release provenance to the policy candidate identity.
func ValidateCandidateAdmissionEvidence(document []byte, expected ReleaseIdentity) (CandidateAdmissionEvidence, error) {
	if err := ociref.ValidateImmutable(expected.Image); err != nil {
		return CandidateAdmissionEvidence{}, fmt.Errorf("candidate admission expected identity: %w", err)
	}
	if len(expected.SourceRevision) != 40 || !isLowerHex(expected.SourceRevision) {
		return CandidateAdmissionEvidence{}, fmt.Errorf("candidate admission expected source revision is invalid")
	}
	if len(document) == 0 || len(document) > candidateAdmissionMaxBytes {
		return CandidateAdmissionEvidence{}, fmt.Errorf("candidate admission evidence size must be between 1 and %d bytes", candidateAdmissionMaxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var record CandidateAdmissionEvidence
	if err := decoder.Decode(&record); err != nil {
		return CandidateAdmissionEvidence{}, fmt.Errorf("decode candidate admission evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CandidateAdmissionEvidence{}, fmt.Errorf("candidate admission evidence contains trailing data")
	}
	digest := ""
	if index := strings.LastIndex(expected.Image, "@"); index >= 0 {
		digest = expected.Image[index+1:]
	}
	if record.SchemaVersion != CurrentSchemaVersion || !record.Attestation.Verified ||
		record.Attestation.Repository != "flidai/leapview" ||
		record.Attestation.Workflow != "flidai/leapview/.github/workflows/release.yml" ||
		!record.SBOM.Discoverable || !record.VulnerabilityPolicy.Passed ||
		record.Image != expected.Image || record.Digest != digest || record.RegistryDigest != digest ||
		record.Attestation.SourceRevision != expected.SourceRevision {
		return CandidateAdmissionEvidence{}, fmt.Errorf(
			"candidate admission does not authorize release identity %s at revision %s",
			expected.Image, expected.SourceRevision,
		)
	}
	return record, nil
}
