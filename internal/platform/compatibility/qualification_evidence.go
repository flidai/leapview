package compatibility

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/ociref"
)

// ReleaseIdentity is the immutable build identity shared by qualification
// evidence and release provenance. It carries normalized metadata, not mutable
// tags or runtime state.
type ReleaseIdentity struct {
	ReleaseID      string `json:"releaseId,omitempty"`
	Version        string `json:"version,omitempty"`
	SourceRevision string `json:"sourceRevision,omitempty"`
	Image          string `json:"image"`
	Distribution   string `json:"distribution,omitempty"`
	Platform       string `json:"platform"`
}

// TransitionQualificationEvidence is the owner-produced proof that an exact
// predecessor and candidate completed both transition directions without
// changing deterministic application state.
type TransitionQualificationEvidence struct {
	SchemaVersion          int                          `json:"schemaVersion"`
	PolicyVersion          string                       `json:"policyVersion"`
	RecoveryPointAt        time.Time                    `json:"recoveryPointAt"`
	Predecessor            ReleaseIdentity              `json:"predecessor"`
	Candidate              ReleaseIdentity              `json:"candidate"`
	PolicySHA256           string                       `json:"policySha256"`
	StateBeforeUpgrade     string                       `json:"stateBeforeUpgradeSha256"`
	StateAfterUpgrade      string                       `json:"stateAfterUpgradeSha256"`
	StateAfterRollback     string                       `json:"stateAfterRollbackSha256"`
	InventoryBefore        TransitionQualificationState `json:"inventoryBeforeUpgrade"`
	InventoryAfterUpgrade  TransitionQualificationState `json:"inventoryAfterUpgrade"`
	InventoryAfterRollback TransitionQualificationState `json:"inventoryAfterRollback"`
	UpgradeResult          string                       `json:"upgradeResult"`
	RollbackResult         string                       `json:"rollbackResult"`
	PreservationVerified   bool                         `json:"preservationVerified"`
}

type TransitionQualificationState struct {
	InstanceID      string `json:"instanceId"`
	Environment     string `json:"environment"`
	CanonicalOrigin string `json:"canonicalOrigin"`
	PrincipalID     string `json:"principalId"`
	PrincipalKind   string `json:"principalKind"`
	PrincipalEmail  string `json:"principalEmail"`
	PrincipalName   string `json:"principalDisplayName"`
}

type TransitionQualificationExpectation struct {
	CandidateImage string
	PolicyVersion  string
	PolicySHA256   string
	TargetScope    string
}

// ValidateTransitionQualificationEvidence is the transition owner's canonical
// validator. Consumers must use it instead of reimplementing a partial view of
// the qualification report.
func ValidateTransitionQualificationEvidence(document []byte, expected TransitionQualificationExpectation) (TransitionQualificationEvidence, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var evidence TransitionQualificationEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return TransitionQualificationEvidence{}, fmt.Errorf("decode transition qualification evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification evidence contains trailing data")
	}
	if evidence.SchemaVersion != 1 || strings.TrimSpace(evidence.PolicyVersion) == "" || evidence.RecoveryPointAt.IsZero() {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification identity is incomplete")
	}
	if evidence.UpgradeResult != "success" || evidence.RollbackResult != "success" || !evidence.PreservationVerified {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification journey is incomplete")
	}
	for label, value := range map[string]string{
		"policy": evidence.PolicySHA256, "state before upgrade": evidence.StateBeforeUpgrade,
		"state after upgrade": evidence.StateAfterUpgrade, "state after rollback": evidence.StateAfterRollback,
	} {
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 || value != strings.ToLower(value) {
			return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification %s digest is invalid", label)
		}
	}
	if evidence.StateBeforeUpgrade != evidence.StateAfterUpgrade || evidence.StateBeforeUpgrade != evidence.StateAfterRollback ||
		evidence.InventoryBefore != evidence.InventoryAfterUpgrade || evidence.InventoryBefore != evidence.InventoryAfterRollback ||
		strings.TrimSpace(evidence.InventoryBefore.InstanceID) == "" {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification did not preserve deterministic application state")
	}
	if evidence.Predecessor.Image == evidence.Candidate.Image {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification endpoints must differ")
	}
	if err := ociref.ValidateImmutable(evidence.Predecessor.Image); err != nil {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification predecessor: %w", err)
	}
	if err := ociref.ValidateImmutable(evidence.Candidate.Image); err != nil {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification candidate: %w", err)
	}
	if expected.CandidateImage != "" && evidence.Candidate.Image != expected.CandidateImage {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification candidate does not match scheduled artifact")
	}
	if expected.PolicyVersion != "" && evidence.PolicyVersion != expected.PolicyVersion {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification policy does not match scheduled policy")
	}
	if expected.PolicySHA256 != "" && evidence.PolicySHA256 != expected.PolicySHA256 {
		return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification policy digest does not match scheduled policy")
	}
	if expected.TargetScope != "" {
		validTarget := expected.TargetScope == "release:"+evidence.Candidate.ReleaseID ||
			expected.TargetScope == "instance:"+evidence.InventoryBefore.InstanceID
		if !validTarget {
			return TransitionQualificationEvidence{}, fmt.Errorf("transition qualification identity does not match scheduled target")
		}
	}
	return evidence, nil
}
