// Package transitionpreflight evaluates whether an exact PostgreSQL-era
// release transition may use a binary rollback or needs provider recovery.
//
// The package is deliberately a pure value evaluator. It does not connect to
// PostgreSQL, inspect migration tables, acquire locks, or mutate a release,
// migration, job, or recovery record. Callers supply immutable projections
// obtained from their owning authorities.
package transitionpreflight

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/platform/compatibility"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/ociref"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

const (
	// SchemaVersion is the version of the preflight evidence contract.
	SchemaVersion = 1

	// DigestDomain is prepended to canonical evidence before hashing. Keeping
	// the newline in the domain makes this identity unambiguous when the
	// canonical JSON begins with a JSON string or number in a future version.
	DigestDomain = "leapview/release-transition-preflight/v1\n"

	// ArchitecturePostgreSQL is the architecture marker admitted by this
	// bounded transition contract. Earlier SQLite-era artifacts are not an
	// upgrade predecessor for this evaluator.
	ArchitecturePostgreSQL = "postgresql"

	// The phase names and order are part of the evidence contract. They are
	// returned as hashes, rather than timestamps, so repeated evaluations are
	// comparable and replayable.
	PhasePreflight          = "preflight"
	PhaseMigration          = "migration"
	PhaseCandidateStartup   = "candidate-startup"
	PhaseRollbackOrRecovery = "rollback-or-recovery"
)

var phaseOrder = []string{
	PhasePreflight,
	PhaseMigration,
	PhaseCandidateStartup,
	PhaseRollbackOrRecovery,
}

// Decision is the only outcome vocabulary emitted by the evaluator.
type Decision string

const (
	DecisionBinaryRollbackCompatible = Decision("binary-rollback-compatible")
	DecisionProviderRecoveryRequired = Decision("provider-recovery-required")
	DecisionUnsupported              = Decision("unsupported")
)

// CompatibilityState is an explicit, caller-owned assessment of one
// persistent domain. Unknown is intentionally distinct from incompatible:
// neither permits the evaluator to infer a migration or rollback outcome.
type CompatibilityState string

const (
	CompatibilityBackwardCompatible = CompatibilityState("backward-compatible")
	CompatibilityIncompatible       = CompatibilityState("incompatible")
	CompatibilityUnknown            = CompatibilityState("unknown")
)

// ReasonCode is stable evidence vocabulary. Values do not contain release
// identifiers, SQL, paths, or provider details.
type ReasonCode string

const (
	ReasonLegacyPredecessor            ReasonCode = "legacy_predecessor"
	ReasonPredecessorNotPostgreSQL     ReasonCode = "predecessor_not_postgresql_era"
	ReasonCandidateNotPostgreSQL       ReasonCode = "candidate_not_postgresql_era"
	ReasonMissingMigrationOwnership    ReasonCode = "missing_migration_ownership"
	ReasonGooseOwnershipMismatch       ReasonCode = "goose_ownership_mismatch"
	ReasonRiverSchemaOwnershipMismatch ReasonCode = "river_schema_ownership_mismatch"
	ReasonRiverJobHistoryOwnership     ReasonCode = "river_job_history_ownership_mismatch"
	ReasonUnknownControlCompatibility  ReasonCode = "unknown_control_compatibility"
	ReasonControlIncompatible          ReasonCode = "control_incompatible"
	ReasonUnknownRiverCompatibility    ReasonCode = "unknown_river_compatibility"
	ReasonRiverIncompatible            ReasonCode = "river_incompatible"
	ReasonUnknownDuckLakeCompatibility ReasonCode = "unknown_ducklake_compatibility"
	ReasonDuckLakeIncompatible         ReasonCode = "ducklake_incompatible"
	ReasonMissingReleasePolicy         ReasonCode = "missing_release_policy"
	ReasonReleasePolicyDigestMismatch  ReasonCode = "release_policy_digest_mismatch"
	ReasonMultiplePolicyRules          ReasonCode = "multiple_policy_rules"
	ReasonPolicyMismatch               ReasonCode = "policy_mismatch"
	ReasonUnknownPolicyDecision        ReasonCode = "unknown_policy_decision"
	ReasonMissingRecoveryFrontier      ReasonCode = "missing_recovery_frontier"
	ReasonIncompleteControlProjection  ReasonCode = "incomplete_control_projection"
	ReasonIncompleteRiverProjection    ReasonCode = "incomplete_river_projection"
	ReasonIncompleteDuckLakeProjection ReasonCode = "incomplete_ducklake_projection"
	ReasonUnknownArchitecture          ReasonCode = "unknown_architecture"
	ReasonMissingTargetIdentityBinding ReasonCode = "missing_target_identity_binding"
	ReasonTargetIdentityMismatch       ReasonCode = "target_identity_mismatch"
)

var (
	// ErrInvalidInput means the supplied value cannot be a structurally valid
	// preflight request. A complete but unsafe request returns evidence with an
	// unsupported decision instead.
	ErrInvalidInput    = errors.New("transition preflight input is malformed")
	ErrInvalidEvidence = errors.New("transition preflight evidence is malformed")
)

// StructuralError identifies a malformed field without including its value.
type StructuralError struct {
	Cause error
	Field string
}

func (e *StructuralError) Error() string {
	if e == nil {
		return ErrInvalidInput.Error()
	}
	if e.Field == "" {
		return ErrInvalidInput.Error()
	}
	return fmt.Sprintf("%s: %s", ErrInvalidInput, e.Field)
}

func (e *StructuralError) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Cause != nil {
		return errors.Join(ErrInvalidInput, e.Cause)
	}
	return ErrInvalidInput
}

// ArtifactIdentity is the exact immutable artifact projection used by the
// transition contract. ReleaseIdentity remains owned by compatibility; the
// architecture marker is transition metadata and is deliberately separate.
type ArtifactIdentity struct {
	Release                 compatibility.ReleaseIdentity `json:"release"`
	ArchitectureMarker      string                        `json:"architectureMarker"`
	ArtifactAdmissionDigest string                        `json:"artifactAdmissionDigest"`
}

// Digest computes the immutable artifact identity over all release metadata,
// including its admission digest. The digest is domain-separated so it cannot
// be confused with preflight or policy identities.
func (a ArtifactIdentity) Digest() (string, error) {
	if err := a.validate("artifact"); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("encode artifact identity: %w", err)
	}
	hash := sha256.Sum256([]byte("leapview/release-artifact/v1\n" + string(encoded)))
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

// PostgreSQLControlProjection is the immutable control-schema comparison
// supplied by the PostgreSQL authority.
type PostgreSQLControlProjection struct {
	Compatibility            CompatibilityState `json:"compatibility"`
	PredecessorSchemaVersion string             `json:"predecessorSchemaVersion"`
	CandidateSchemaVersion   string             `json:"candidateSchemaVersion"`
	TargetIdentityDigest     string             `json:"targetIdentityDigest"`
}

// RiverJobProjection separately represents River's upstream operational
// schema and LeapView's product-owned job history. They may not be collapsed
// into a generic "database compatible" flag because ownership differs.
type RiverJobProjection struct {
	SchemaCompatibility       CompatibilityState `json:"schemaCompatibility"`
	JobHistoryCompatibility   CompatibilityState `json:"jobHistoryCompatibility"`
	ExistingSchemaVersion     string             `json:"existingSchemaVersion"`
	RequiredSchemaVersion     string             `json:"requiredSchemaVersion"`
	ExistingJobHistoryVersion string             `json:"existingJobHistoryVersion"`
	RequiredJobHistoryVersion string             `json:"requiredJobHistoryVersion"`
	TargetIdentityDigest      string             `json:"targetIdentityDigest"`
}

// DuckLakeProjection retains the exact five-field tuple owned by physicalpool
// alongside the caller's explicit comparison result.
type DuckLakeProjection struct {
	Compatibility        CompatibilityState         `json:"compatibility"`
	Predecessor          physicalpool.Compatibility `json:"predecessor"`
	Candidate            physicalpool.Compatibility `json:"candidate"`
	TargetIdentityDigest string                     `json:"targetIdentityDigest"`
}

// RecoveryFrontierRef is intentionally narrower than recoveryset.RecoverySet:
// preflight needs only the canonical set identity and its content digest.
type RecoveryFrontierRef struct {
	SetID                string `json:"setId"`
	Digest               string `json:"digest"`
	TargetIdentityDigest string `json:"targetIdentityDigest"`
}

// MigrationOwnership names the owners of each migration/job boundary. The
// expected values are exported so adapters can normalize their own source
// representation without copying string literals.
type MigrationOwnership struct {
	GooseControlSchemaOwner     string `json:"gooseControlSchemaOwner"`
	RiverOperationalSchemaOwner string `json:"riverOperationalSchemaOwner"`
	RiverJobHistoryOwner        string `json:"riverJobHistoryOwner"`
}

const (
	OwnerLeapView = "leapview"
	OwnerRiver    = "river"

	GooseControlSchemaOwnerLeapView  = OwnerLeapView
	RiverOperationalSchemaOwnerRiver = OwnerRiver
	RiverJobHistoryOwnerLeapView     = OwnerLeapView
)

// ReleasePolicyRule is one exact artifact-pair policy. A rule's decision is
// checked against the persistent-domain projections; it is never treated as
// permission to ignore contradictory evidence.
type ReleasePolicyRule struct {
	PredecessorArtifactDigest  string   `json:"predecessorArtifactDigest"`
	CandidateArtifactDigest    string   `json:"candidateArtifactDigest"`
	RollbackFromArtifactDigest string   `json:"rollbackFromArtifactDigest"`
	RollbackToArtifactDigest   string   `json:"rollbackToArtifactDigest"`
	Decision                   Decision `json:"decision"`
}

// ReleasePolicy is immutable release transition metadata. Rules are sorted
// by Normalize so source map/loader order cannot alter evidence identity.
type ReleasePolicy struct {
	Version string              `json:"version"`
	Digest  string              `json:"digest"`
	Rules   []ReleasePolicyRule `json:"rules"`
}

type releasePolicyContent struct {
	Version string              `json:"version"`
	Rules   []ReleasePolicyRule `json:"rules"`
}

// ContentDigest computes the immutable policy identity over policy metadata
// without including the supplied Digest field itself.
func (p ReleasePolicy) ContentDigest() (string, error) {
	normalized, err := p.normalizeWithoutDigest()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(releasePolicyContent{Version: normalized.Version, Rules: normalized.Rules})
	if err != nil {
		return "", fmt.Errorf("encode release policy: %w", err)
	}
	hash := sha256.Sum256([]byte("leapview/release-policy/v1\n" + string(encoded)))
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

// Input is the complete immutable request for one PostgreSQL-era transition.
// It contains projections only; no field is a database handle or operation
// request.
type Input struct {
	SchemaVersion        int                         `json:"schemaVersion"`
	TargetIdentityDigest string                      `json:"targetIdentityDigest"`
	Predecessor          ArtifactIdentity            `json:"predecessor"`
	Candidate            ArtifactIdentity            `json:"candidate"`
	MigrationOwnership   MigrationOwnership          `json:"migrationOwnership"`
	Control              PostgreSQLControlProjection `json:"control"`
	River                RiverJobProjection          `json:"river"`
	DuckLake             DuckLakeProjection          `json:"ducklake"`
	RecoveryFrontier     *RecoveryFrontierRef        `json:"recoveryFrontier"`
	ReleasePolicy        ReleasePolicy               `json:"releasePolicy"`
}

// PhaseIdentity binds one fixed phase name to a deterministic identity.
type PhaseIdentity struct {
	Phase  string `json:"phase"`
	Digest string `json:"digest"`
}

// Evidence is the complete immutable result. It intentionally has no Digest
// field: Digest() hashes canonical bytes that cannot contain their own digest.
type Evidence struct {
	SchemaVersion        int                         `json:"schemaVersion"`
	TargetIdentityDigest string                      `json:"targetIdentityDigest"`
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
	PhaseIdentities      []PhaseIdentity             `json:"phaseIdentities"`
}

// PhaseNames returns the fixed phase order as a defensive copy.
func PhaseNames() []string {
	return append([]string(nil), phaseOrder...)
}

// Evaluate normalizes immutable inputs and returns one deterministic decision.
// Structural errors return no evidence; safety uncertainty returns an
// unsupported evidence document with stable reason codes.
func Evaluate(input Input) (Evidence, error) {
	normalized, err := input.Normalize()
	if err != nil {
		return Evidence{}, err
	}

	reasons := make(map[ReasonCode]struct{})
	if normalized.Predecessor.ArchitectureMarker != ArchitecturePostgreSQL {
		reasons[ReasonPredecessorNotPostgreSQL] = struct{}{}
		reasons[ReasonLegacyPredecessor] = struct{}{}
	}
	if normalized.Candidate.ArchitectureMarker != ArchitecturePostgreSQL {
		reasons[ReasonCandidateNotPostgreSQL] = struct{}{}
		reasons[ReasonUnknownArchitecture] = struct{}{}
	}

	evaluateOwnership(normalized.MigrationOwnership, reasons)
	if normalized.RecoveryFrontier == nil {
		reasons[ReasonMissingRecoveryFrontier] = struct{}{}
	}
	for _, targetDigest := range []string{
		normalized.Control.TargetIdentityDigest,
		normalized.River.TargetIdentityDigest,
		normalized.DuckLake.TargetIdentityDigest,
	} {
		if targetDigest == "" {
			reasons[ReasonMissingTargetIdentityBinding] = struct{}{}
		} else if targetDigest != normalized.TargetIdentityDigest {
			reasons[ReasonTargetIdentityMismatch] = struct{}{}
		}
	}
	if normalized.RecoveryFrontier != nil && normalized.RecoveryFrontier.TargetIdentityDigest != normalized.TargetIdentityDigest {
		reasons[ReasonTargetIdentityMismatch] = struct{}{}
	}
	controlIncompatible, controlUnknown := evaluateControl(normalized.Control, reasons)
	riverIncompatible, riverUnknown := evaluateRiver(normalized.River, reasons)
	ducklakeIncompatible, ducklakeUnknown := evaluateDuckLake(normalized.DuckLake, reasons)

	if normalized.DuckLake.Compatibility != CompatibilityUnknown && (normalized.DuckLake.Predecessor == (physicalpool.Compatibility{}) || normalized.DuckLake.Candidate == (physicalpool.Compatibility{})) {
		reasons[ReasonIncompleteDuckLakeProjection] = struct{}{}
	}
	if normalized.Control.Compatibility != CompatibilityUnknown && (strings.TrimSpace(normalized.Control.PredecessorSchemaVersion) == "" || strings.TrimSpace(normalized.Control.CandidateSchemaVersion) == "") {
		reasons[ReasonIncompleteControlProjection] = struct{}{}
	}
	if normalized.River.SchemaCompatibility != CompatibilityUnknown && normalized.River.JobHistoryCompatibility != CompatibilityUnknown && (strings.TrimSpace(normalized.River.ExistingSchemaVersion) == "" || strings.TrimSpace(normalized.River.RequiredSchemaVersion) == "" || strings.TrimSpace(normalized.River.ExistingJobHistoryVersion) == "" || strings.TrimSpace(normalized.River.RequiredJobHistoryVersion) == "") {
		reasons[ReasonIncompleteRiverProjection] = struct{}{}
	}

	predecessorDigest, _ := normalized.Predecessor.Digest()
	candidateDigest, _ := normalized.Candidate.Digest()
	rule, ruleCount, policyProblem := matchingPolicyRule(normalized.ReleasePolicy, predecessorDigest, candidateDigest)
	if policyProblem != "" {
		switch policyProblem {
		case "missing":
			reasons[ReasonMissingReleasePolicy] = struct{}{}
		case "multiple":
			reasons[ReasonMultiplePolicyRules] = struct{}{}
		case "mismatch":
			reasons[ReasonPolicyMismatch] = struct{}{}
		}
	}
	if ruleCount == 1 && rule.Decision != DecisionBinaryRollbackCompatible && rule.Decision != DecisionProviderRecoveryRequired {
		reasons[ReasonUnknownPolicyDecision] = struct{}{}
	}
	if normalized.ReleasePolicy.Digest == "" {
		reasons[ReasonMissingReleasePolicy] = struct{}{}
	} else if expectedDigest, digestErr := normalized.ReleasePolicy.ContentDigest(); digestErr != nil || expectedDigest != normalized.ReleasePolicy.Digest {
		reasons[ReasonReleasePolicyDigestMismatch] = struct{}{}
	}

	unknown := controlUnknown || riverUnknown || ducklakeUnknown
	incompatible := controlIncompatible || riverIncompatible || ducklakeIncompatible
	decision := DecisionUnsupported
	// Domain incompatibility is an expected provider-recovery input, not a
	// blocker by itself. All other reason codes are gates that must be absent
	// before the policy can select a decision.
	blockingReasons := len(reasons)
	for _, reason := range []ReasonCode{ReasonControlIncompatible, ReasonRiverIncompatible, ReasonDuckLakeIncompatible} {
		if _, exists := reasons[reason]; exists {
			blockingReasons--
		}
	}
	if blockingReasons == 0 && !unknown {
		expected := DecisionBinaryRollbackCompatible
		frontierMissing := false
		if incompatible {
			expected = DecisionProviderRecoveryRequired
			if normalized.RecoveryFrontier == nil {
				reasons[ReasonMissingRecoveryFrontier] = struct{}{}
				frontierMissing = true
			}
		} else if normalized.RecoveryFrontier == nil {
			frontierMissing = true
		}
		if ruleCount != 1 || rule.Decision != expected {
			reasons[ReasonPolicyMismatch] = struct{}{}
		} else if !frontierMissing && (len(reasons) == 0 || (incompatible && blockingReasons == 0)) {
			decision = expected
		}
	}
	if unknown || (incompatible && normalized.RecoveryFrontier == nil) {
		decision = DecisionUnsupported
	}
	if incompatible && normalized.RecoveryFrontier == nil {
		// Keep this explicit in case a future evaluator changes the ordering
		// above or gains another provider-recovery gate.
		reasons[ReasonMissingRecoveryFrontier] = struct{}{}
	}

	evidence := Evidence{
		SchemaVersion:        normalized.SchemaVersion,
		TargetIdentityDigest: normalized.TargetIdentityDigest,
		Predecessor:          normalized.Predecessor,
		Candidate:            normalized.Candidate,
		Decision:             decision,
		ReasonCodes:          sortedReasons(reasons),
		MigrationOwnership:   normalized.MigrationOwnership,
		Control:              normalized.Control,
		River:                normalized.River,
		DuckLake:             normalized.DuckLake,
		RecoveryFrontier:     cloneFrontier(normalized.RecoveryFrontier),
		ReleasePolicy:        normalized.ReleasePolicy,
	}
	evidence.PhaseIdentities, err = phaseIdentities(normalized, evidence.Decision, evidence.ReasonCodes)
	if err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

// EvaluatePreflight is an explicit spelling for call sites at the release
// admission boundary.
func EvaluatePreflight(input Input) (Evidence, error) { return Evaluate(input) }

// EvaluateTransition is an explicit spelling for callers that own transition
// orchestration.
func EvaluateTransition(input Input) (Evidence, error) { return Evaluate(input) }

// ParseEvidence decodes one bounded evidence document through the repository's
// strict JSON owner, rejects unknown/duplicate/trailing fields, and returns its
// normalized value. Parsing remains pure and never verifies live state.
func ParseEvidence(document []byte) (Evidence, error) {
	var evidence Evidence
	if err := strictjson.DecodeWithOptions(document, &evidence, strictjson.Options{MaxBytes: 1 << 20, MaxDepth: 32, DuplicateKeys: strictjson.CaseFoldedKeys}); err != nil {
		return Evidence{}, fmt.Errorf("%w: decode evidence: %v", ErrInvalidEvidence, err)
	}
	// Keep this explicit even though strictjson currently rejects trailing
	// values; it documents that exactly one JSON value is part of the contract.
	decoder := json.NewDecoder(strings.NewReader(string(document)))
	var trailing any
	if err := decoder.Decode(&evidence); err != nil {
		return Evidence{}, fmt.Errorf("%w: decode evidence: %v", ErrInvalidEvidence, err)
	}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Evidence{}, fmt.Errorf("%w: trailing evidence data", ErrInvalidEvidence)
	}
	normalized, err := evidence.Normalize()
	if err != nil {
		return Evidence{}, err
	}
	return normalized, nil
}

// Normalize validates structural identity and returns a deep-normalized copy.
func (input Input) Normalize() (Input, error) {
	if input.SchemaVersion != SchemaVersion {
		return Input{}, malformed("schemaVersion", fmt.Errorf("unsupported version"))
	}
	if err := platformdigest.ValidateSHA256Identity(input.TargetIdentityDigest); err != nil {
		return Input{}, malformed("targetIdentityDigest", err)
	}
	if err := input.Predecessor.validate("predecessor"); err != nil {
		return Input{}, err
	}
	if err := input.Candidate.validate("candidate"); err != nil {
		return Input{}, err
	}
	if input.Predecessor.Release.Image == input.Candidate.Release.Image {
		return Input{}, malformed("candidate", fmt.Errorf("predecessor and candidate artifacts must differ"))
	}
	var err error
	input.MigrationOwnership, err = input.MigrationOwnership.normalize()
	if err != nil {
		return Input{}, err
	}
	input.Control.PredecessorSchemaVersion = normalizeText(input.Control.PredecessorSchemaVersion)
	input.Control.CandidateSchemaVersion = normalizeText(input.Control.CandidateSchemaVersion)
	input.Control.TargetIdentityDigest = normalizeText(input.Control.TargetIdentityDigest)
	input.River.ExistingSchemaVersion = normalizeText(input.River.ExistingSchemaVersion)
	input.River.RequiredSchemaVersion = normalizeText(input.River.RequiredSchemaVersion)
	input.River.ExistingJobHistoryVersion = normalizeText(input.River.ExistingJobHistoryVersion)
	input.River.RequiredJobHistoryVersion = normalizeText(input.River.RequiredJobHistoryVersion)
	input.River.TargetIdentityDigest = normalizeText(input.River.TargetIdentityDigest)
	input.DuckLake.TargetIdentityDigest = normalizeText(input.DuckLake.TargetIdentityDigest)
	for field, value := range map[string]string{
		"control.targetIdentityDigest":  input.Control.TargetIdentityDigest,
		"river.targetIdentityDigest":    input.River.TargetIdentityDigest,
		"ducklake.targetIdentityDigest": input.DuckLake.TargetIdentityDigest,
	} {
		if value != "" {
			if err := platformdigest.ValidateSHA256Identity(value); err != nil {
				return Input{}, malformed(field, err)
			}
		}
	}
	for field, tuple := range map[string]physicalpool.Compatibility{"ducklake.predecessor": input.DuckLake.Predecessor, "ducklake.candidate": input.DuckLake.Candidate} {
		if tuple != (physicalpool.Compatibility{}) {
			if err := tuple.Validate(); err != nil {
				return Input{}, malformed(field, err)
			}
		}
	}
	input.RecoveryFrontier = cloneFrontier(input.RecoveryFrontier)
	if input.RecoveryFrontier != nil {
		if err := input.RecoveryFrontier.Validate(); err != nil {
			return Input{}, malformed("recoveryFrontier", err)
		}
	}
	input.ReleasePolicy, err = input.ReleasePolicy.normalize()
	if err != nil {
		return Input{}, err
	}
	return input, nil
}

func malformed(field string, cause error) error {
	return &StructuralError{Field: field, Cause: cause}
}

func (a ArtifactIdentity) validate(field string) error {
	identity := a.Release
	for name, value := range map[string]string{
		"releaseId":               identity.ReleaseID,
		"version":                 identity.Version,
		"sourceRevision":          identity.SourceRevision,
		"distribution":            identity.Distribution,
		"platform":                identity.Platform,
		"architectureMarker":      a.ArchitectureMarker,
		"artifactAdmissionDigest": a.ArtifactAdmissionDigest,
	} {
		if err := canonicalText(value); err != nil {
			return malformed(field+"."+name, err)
		}
	}
	if err := ociref.ValidateImmutable(identity.Image); err != nil {
		return malformed(field+".release.image", err)
	}
	if err := platformdigest.ValidateSHA256Identity(a.ArtifactAdmissionDigest); err != nil {
		return malformed(field+".artifactAdmissionDigest", err)
	}
	return nil
}

func canonicalText(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 1024 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("text is not canonical")
	}
	return nil
}

func normalizeText(value string) string { return strings.TrimSpace(value) }

func (f RecoveryFrontierRef) Validate() error {
	u, err := uuid.Parse(f.SetID)
	if err != nil || u.String() != f.SetID {
		return fmt.Errorf("setId must be a canonical UUID")
	}
	if err := platformdigest.ValidateSHA256Identity(f.Digest); err != nil {
		return fmt.Errorf("digest must be a canonical SHA-256 identity")
	}
	if err := platformdigest.ValidateSHA256Identity(f.TargetIdentityDigest); err != nil {
		return fmt.Errorf("targetIdentityDigest must be a canonical SHA-256 identity")
	}
	return nil
}

func cloneFrontier(frontier *RecoveryFrontierRef) *RecoveryFrontierRef {
	if frontier == nil {
		return nil
	}
	copy := *frontier
	return &copy
}

func (m MigrationOwnership) normalize() (MigrationOwnership, error) {
	for field, value := range map[string]string{
		"gooseControlSchemaOwner":     m.GooseControlSchemaOwner,
		"riverOperationalSchemaOwner": m.RiverOperationalSchemaOwner,
		"riverJobHistoryOwner":        m.RiverJobHistoryOwner,
	} {
		if len(value) > 128 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return MigrationOwnership{}, malformed("migrationOwnership."+field, fmt.Errorf("owner is not canonical"))
		}
	}
	m.GooseControlSchemaOwner = normalizeText(m.GooseControlSchemaOwner)
	m.RiverOperationalSchemaOwner = normalizeText(m.RiverOperationalSchemaOwner)
	m.RiverJobHistoryOwner = normalizeText(m.RiverJobHistoryOwner)
	return m, nil
}

func (p ReleasePolicy) normalize() (ReleasePolicy, error) {
	p.Version = normalizeText(p.Version)
	p.Digest = normalizeText(p.Digest)
	if p.Digest != "" {
		if err := platformdigest.ValidateSHA256Identity(p.Digest); err != nil {
			return ReleasePolicy{}, malformed("releasePolicy.digest", err)
		}
	}
	p.Rules = append([]ReleasePolicyRule(nil), p.Rules...)
	for i := range p.Rules {
		rule := &p.Rules[i]
		for field, value := range map[string]string{
			"predecessorArtifactDigest":  rule.PredecessorArtifactDigest,
			"candidateArtifactDigest":    rule.CandidateArtifactDigest,
			"rollbackFromArtifactDigest": rule.RollbackFromArtifactDigest,
			"rollbackToArtifactDigest":   rule.RollbackToArtifactDigest,
		} {
			if err := platformdigest.ValidateSHA256Identity(value); err != nil {
				return ReleasePolicy{}, malformed(fmt.Sprintf("releasePolicy.rules[%d].%s", i, field), err)
			}
		}
		rule.PredecessorArtifactDigest = normalizeText(rule.PredecessorArtifactDigest)
		rule.CandidateArtifactDigest = normalizeText(rule.CandidateArtifactDigest)
		rule.RollbackFromArtifactDigest = normalizeText(rule.RollbackFromArtifactDigest)
		rule.RollbackToArtifactDigest = normalizeText(rule.RollbackToArtifactDigest)
		rule.Decision = Decision(normalizeText(string(rule.Decision)))
	}
	sort.Slice(p.Rules, func(i, j int) bool {
		left, right := p.Rules[i], p.Rules[j]
		if left.PredecessorArtifactDigest != right.PredecessorArtifactDigest {
			return left.PredecessorArtifactDigest < right.PredecessorArtifactDigest
		}
		if left.CandidateArtifactDigest != right.CandidateArtifactDigest {
			return left.CandidateArtifactDigest < right.CandidateArtifactDigest
		}
		if left.RollbackFromArtifactDigest != right.RollbackFromArtifactDigest {
			return left.RollbackFromArtifactDigest < right.RollbackFromArtifactDigest
		}
		if left.RollbackToArtifactDigest != right.RollbackToArtifactDigest {
			return left.RollbackToArtifactDigest < right.RollbackToArtifactDigest
		}
		return left.Decision < right.Decision
	})
	return p, nil
}

func (p ReleasePolicy) normalizeWithoutDigest() (ReleasePolicy, error) {
	p.Digest = ""
	return p.normalize()
}

func matchingPolicyRule(policy ReleasePolicy, predecessor, candidate string) (ReleasePolicyRule, int, string) {
	if policy.Version == "" || len(policy.Rules) == 0 {
		return ReleasePolicyRule{}, 0, "missing"
	}
	var matching []ReleasePolicyRule
	for _, rule := range policy.Rules {
		if rule.PredecessorArtifactDigest == predecessor &&
			rule.CandidateArtifactDigest == candidate &&
			rule.RollbackFromArtifactDigest == candidate &&
			rule.RollbackToArtifactDigest == predecessor {
			matching = append(matching, rule)
		}
	}
	if len(matching) == 0 {
		return ReleasePolicyRule{}, 0, "mismatch"
	}
	if len(matching) > 1 {
		return matching[0], len(matching), "multiple"
	}
	return matching[0], 1, ""
}

func evaluateOwnership(ownership MigrationOwnership, reasons map[ReasonCode]struct{}) {
	if ownership.GooseControlSchemaOwner == "" || ownership.RiverOperationalSchemaOwner == "" || ownership.RiverJobHistoryOwner == "" {
		reasons[ReasonMissingMigrationOwnership] = struct{}{}
	}
	if ownership.GooseControlSchemaOwner != "" && ownership.GooseControlSchemaOwner != GooseControlSchemaOwnerLeapView {
		reasons[ReasonGooseOwnershipMismatch] = struct{}{}
	}
	if ownership.RiverOperationalSchemaOwner != "" && ownership.RiverOperationalSchemaOwner != RiverOperationalSchemaOwnerRiver {
		reasons[ReasonRiverSchemaOwnershipMismatch] = struct{}{}
	}
	if ownership.RiverJobHistoryOwner != "" && ownership.RiverJobHistoryOwner != RiverJobHistoryOwnerLeapView {
		reasons[ReasonRiverJobHistoryOwnership] = struct{}{}
	}
}

func evaluateControl(projection PostgreSQLControlProjection, reasons map[ReasonCode]struct{}) (incompatible, unknown bool) {
	switch projection.Compatibility {
	case CompatibilityBackwardCompatible:
		return false, false
	case CompatibilityIncompatible:
		reasons[ReasonControlIncompatible] = struct{}{}
		return true, false
	case CompatibilityUnknown, "":
		reasons[ReasonUnknownControlCompatibility] = struct{}{}
		return false, true
	default:
		reasons[ReasonUnknownControlCompatibility] = struct{}{}
		return false, true
	}
}

func evaluateRiver(projection RiverJobProjection, reasons map[ReasonCode]struct{}) (incompatible, unknown bool) {
	if projection.SchemaCompatibility == CompatibilityIncompatible || projection.JobHistoryCompatibility == CompatibilityIncompatible {
		reasons[ReasonRiverIncompatible] = struct{}{}
		incompatible = true
	}
	if projection.SchemaCompatibility == CompatibilityUnknown || projection.SchemaCompatibility == "" || projection.JobHistoryCompatibility == CompatibilityUnknown || projection.JobHistoryCompatibility == "" || (projection.SchemaCompatibility != CompatibilityBackwardCompatible && projection.SchemaCompatibility != CompatibilityIncompatible) || (projection.JobHistoryCompatibility != CompatibilityBackwardCompatible && projection.JobHistoryCompatibility != CompatibilityIncompatible) {
		reasons[ReasonUnknownRiverCompatibility] = struct{}{}
		unknown = true
	}
	return incompatible, unknown
}

func evaluateDuckLake(projection DuckLakeProjection, reasons map[ReasonCode]struct{}) (incompatible, unknown bool) {
	switch projection.Compatibility {
	case CompatibilityBackwardCompatible:
		return false, false
	case CompatibilityIncompatible:
		reasons[ReasonDuckLakeIncompatible] = struct{}{}
		return true, false
	case CompatibilityUnknown, "":
		reasons[ReasonUnknownDuckLakeCompatibility] = struct{}{}
		return false, true
	default:
		reasons[ReasonUnknownDuckLakeCompatibility] = struct{}{}
		return false, true
	}
}

func sortedReasons(reasons map[ReasonCode]struct{}) []ReasonCode {
	values := make([]ReasonCode, 0, len(reasons))
	for reason := range reasons {
		values = append(values, reason)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}
