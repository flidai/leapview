package transitionpreflight

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

type phaseMaterial struct {
	SchemaVersion        int                         `json:"schemaVersion"`
	TargetIdentityDigest string                      `json:"targetIdentityDigest"`
	Phase                string                      `json:"phase"`
	Predecessor          ArtifactIdentity            `json:"predecessor"`
	Candidate            ArtifactIdentity            `json:"candidate"`
	Decision             Decision                    `json:"decision"`
	ReasonCodes          []ReasonCode                `json:"reasonCodes"`
	MigrationOwnership   MigrationOwnership          `json:"migrationOwnership"`
	Control              PostgreSQLControlProjection `json:"control"`
	River                RiverJobProjection          `json:"river"`
	DuckLake             DuckLakeProjection          `json:"ducklake"`
	RecoveryFrontier     *RecoveryFrontierRef        `json:"recoveryFrontier"`
	ReleasePolicy        ReleasePolicy               `json:"releasePolicy"`
}

func phaseIdentities(input Input, decision Decision, reasons []ReasonCode) ([]PhaseIdentity, error) {
	identities := make([]PhaseIdentity, 0, len(phaseOrder))
	for _, phase := range phaseOrder {
		material := phaseMaterial{
			SchemaVersion: input.SchemaVersion, TargetIdentityDigest: input.TargetIdentityDigest, Phase: phase,
			Predecessor: input.Predecessor, Candidate: input.Candidate,
			Decision: decision, ReasonCodes: append([]ReasonCode{}, reasons...),
			MigrationOwnership: input.MigrationOwnership, Control: input.Control,
			River: input.River, DuckLake: input.DuckLake,
			RecoveryFrontier: cloneFrontier(input.RecoveryFrontier), ReleasePolicy: input.ReleasePolicy,
		}
		encoded, err := json.Marshal(material)
		if err != nil {
			return nil, fmt.Errorf("encode %s phase identity: %w", phase, err)
		}
		hash := sha256.Sum256([]byte(DigestDomain + "phase/" + phase + "\n" + string(encoded)))
		identities = append(identities, PhaseIdentity{Phase: phase, Digest: "sha256:" + hex.EncodeToString(hash[:])})
	}
	return identities, nil
}

// Normalize returns a validated, deterministic evidence copy. It re-evaluates
// the decision, reasons, and phase identities so a structurally valid but
// inconsistent persisted document still fails closed.
func (e Evidence) Normalize() (Evidence, error) {
	if e.SchemaVersion != SchemaVersion {
		return Evidence{}, fmt.Errorf("%w: schemaVersion", ErrInvalidEvidence)
	}
	if err := platformdigest.ValidateSHA256Identity(e.TargetIdentityDigest); err != nil {
		return Evidence{}, fmt.Errorf("%w: targetIdentityDigest", ErrInvalidEvidence)
	}
	if err := e.Predecessor.validate("predecessor"); err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	if err := e.Candidate.validate("candidate"); err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	if e.Predecessor.Release.Image == e.Candidate.Release.Image {
		return Evidence{}, fmt.Errorf("%w: artifacts must differ", ErrInvalidEvidence)
	}
	if e.Decision != DecisionBinaryRollbackCompatible && e.Decision != DecisionProviderRecoveryRequired && e.Decision != DecisionUnsupported {
		return Evidence{}, fmt.Errorf("%w: decision", ErrInvalidEvidence)
	}
	var err error
	e.MigrationOwnership, err = e.MigrationOwnership.normalize()
	if err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	e.Control.PredecessorSchemaVersion, err = normalizeSchemaVersion(e.Control.PredecessorSchemaVersion, "control.predecessorSchemaVersion")
	if err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	e.Control.CandidateSchemaVersion, err = normalizeSchemaVersion(e.Control.CandidateSchemaVersion, "control.candidateSchemaVersion")
	if err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	e.Control.TargetIdentityDigest = normalizeText(e.Control.TargetIdentityDigest)
	e.River.ExistingSchemaVersion, err = normalizeSchemaVersion(e.River.ExistingSchemaVersion, "river.existingSchemaVersion")
	if err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	e.River.RequiredSchemaVersion, err = normalizeSchemaVersion(e.River.RequiredSchemaVersion, "river.requiredSchemaVersion")
	if err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	e.River.ExistingJobHistoryVersion, err = normalizeSchemaVersion(e.River.ExistingJobHistoryVersion, "river.existingJobHistoryVersion")
	if err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	e.River.RequiredJobHistoryVersion, err = normalizeSchemaVersion(e.River.RequiredJobHistoryVersion, "river.requiredJobHistoryVersion")
	if err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	e.River.TargetIdentityDigest = normalizeText(e.River.TargetIdentityDigest)
	e.DuckLake.TargetIdentityDigest = normalizeText(e.DuckLake.TargetIdentityDigest)
	for field, value := range map[string]string{
		"control.targetIdentityDigest":  e.Control.TargetIdentityDigest,
		"river.targetIdentityDigest":    e.River.TargetIdentityDigest,
		"ducklake.targetIdentityDigest": e.DuckLake.TargetIdentityDigest,
	} {
		if value != "" {
			if err := platformdigest.ValidateSHA256Identity(value); err != nil {
				return Evidence{}, fmt.Errorf("%w: %s", ErrInvalidEvidence, field)
			}
		}
	}
	for _, tuple := range []physicalpool.Compatibility{e.DuckLake.Predecessor, e.DuckLake.Candidate} {
		if tuple != (physicalpool.Compatibility{}) {
			if err := tuple.Validate(); err != nil {
				return Evidence{}, errors.Join(ErrInvalidEvidence, err)
			}
		}
	}
	if e.RecoveryFrontier != nil {
		if err := e.RecoveryFrontier.Validate(); err != nil {
			return Evidence{}, errors.Join(ErrInvalidEvidence, err)
		}
	}
	e.ReleasePolicy, err = e.ReleasePolicy.normalize()
	if err != nil {
		return Evidence{}, errors.Join(ErrInvalidEvidence, err)
	}
	seenReasons := make(map[ReasonCode]struct{}, len(e.ReasonCodes))
	for _, reason := range e.ReasonCodes {
		if reason == "" {
			return Evidence{}, fmt.Errorf("%w: empty reason code", ErrInvalidEvidence)
		}
		seenReasons[reason] = struct{}{}
	}
	e.ReasonCodes = sortedReasons(seenReasons)
	if len(e.PhaseIdentities) != len(phaseOrder) {
		return Evidence{}, fmt.Errorf("%w: phase identities", ErrInvalidEvidence)
	}
	phaseMap := make(map[string]PhaseIdentity, len(e.PhaseIdentities))
	for _, phase := range e.PhaseIdentities {
		if err := platformdigest.ValidateSHA256Identity(phase.Digest); err != nil {
			return Evidence{}, fmt.Errorf("%w: phase identity digest", ErrInvalidEvidence)
		}
		if _, exists := phaseMap[phase.Phase]; exists {
			return Evidence{}, fmt.Errorf("%w: duplicate phase identity", ErrInvalidEvidence)
		}
		phaseMap[phase.Phase] = phase
	}
	e.PhaseIdentities = make([]PhaseIdentity, 0, len(phaseOrder))
	for _, phaseName := range phaseOrder {
		phase, exists := phaseMap[phaseName]
		if !exists {
			return Evidence{}, fmt.Errorf("%w: missing phase identity", ErrInvalidEvidence)
		}
		e.PhaseIdentities = append(e.PhaseIdentities, phase)
	}
	expected, evalErr := Evaluate(Input{
		SchemaVersion:        e.SchemaVersion,
		TargetIdentityDigest: e.TargetIdentityDigest,
		Predecessor:          e.Predecessor,
		Candidate:            e.Candidate,
		MigrationOwnership:   e.MigrationOwnership,
		Control:              e.Control,
		River:                e.River,
		DuckLake:             e.DuckLake,
		RecoveryFrontier:     cloneFrontier(e.RecoveryFrontier),
		ReleasePolicy:        e.ReleasePolicy,
	})
	if evalErr != nil {
		return Evidence{}, fmt.Errorf("%w: evidence inputs are invalid: %v", ErrInvalidEvidence, evalErr)
	}
	if expected.Decision != e.Decision || !sameReasons(expected.ReasonCodes, e.ReasonCodes) || !samePhases(expected.PhaseIdentities, e.PhaseIdentities) {
		return Evidence{}, fmt.Errorf("%w: decision, reasons, or phase identities do not match evaluated inputs", ErrInvalidEvidence)
	}
	return e, nil
}

func sameReasons(left, right []ReasonCode) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func samePhases(left, right []PhaseIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Validate checks the evidence contract without performing any external
// operation.
func (e Evidence) Validate() error {
	_, err := e.Normalize()
	return err
}

// CanonicalJSON emits compact JSON in struct field order. Slices are copied,
// sorted, and normalized before encoding.
func (e Evidence) CanonicalJSON() ([]byte, error) {
	normalized, err := e.Normalize()
	if err != nil {
		return nil, err
	}
	encoded, err := marshalEvidence(normalized)
	if err != nil {
		return nil, errors.Join(ErrInvalidEvidence, err)
	}
	return encoded, nil
}

// CanonicalBytes is a convenience alias for CanonicalJSON.
func (e Evidence) CanonicalBytes() ([]byte, error) { return e.CanonicalJSON() }

// Digest computes the domain-separated SHA-256 identity of canonical evidence.
func (e Evidence) Digest() (string, error) {
	canonical, err := e.CanonicalJSON()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(append([]byte(DigestDomain), canonical...))
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

// CanonicalDigest is a convenience alias for Digest.
func (e Evidence) CanonicalDigest() (string, error) { return e.Digest() }
