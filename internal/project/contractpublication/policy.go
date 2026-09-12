package contractpublication

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractversion"
)

// BaselineKind makes genesis and update admissions explicit.  A missing
// baseline is never silently interpreted as genesis.
type BaselineKind string

const (
	BaselineGenesis  BaselineKind = "genesis"
	BaselineExisting BaselineKind = "existing"
)

// PolicyContext is the caller-owned binding supplied to a derivation.  The
// adapter must obtain Existing from its immutable history under its own
// transaction; this package only validates the value it receives.
type PolicyContext struct {
	BaselineKind BaselineKind
	Existing     *ContractPublication
	// Baseline is an alias for Existing for adapters that use the terminology
	// of the persisted evidence. Set at most one of Baseline and Existing.
	Baseline *ContractPublication
}

// PolicyEvidence is deterministic, immutable classification evidence.  It
// intentionally carries no lifecycle sequence, active bundle, audit event, or
// approval state beyond the derived requirement.  Those belong to later
// capabilities and are outside this publication domain.
type PolicyEvidence struct {
	Version                  int                      `json:"version"`
	BaselineKind             BaselineKind             `json:"baselineKind"`
	Baseline                 PublicationIdentity      `json:"baseline"`
	Candidate                PublicationIdentity      `json:"candidate"`
	Classification           contractversion.Result   `json:"classification"`
	RequiresSecurityApproval bool                     `json:"requiresSecurityApproval"`
	AffectedResources        []PolicyAffectedResource `json:"affectedResources"`
	ChangedDimensions        []contractversion.Domain `json:"changedDimensions"`
	EvidenceDigest           string                   `json:"evidenceDigest"`
}

// DerivePolicyEvidence classifies either an explicit first publication or an
// explicit update.  Genesis and existing calls use different classifier
// entrypoints so a missing baseline cannot become a first publication by
// accident.
func DerivePolicyEvidence(context PolicyContext, candidate ContractPublication) (PolicyEvidence, error) {
	if err := candidate.Validate(); err != nil {
		return PolicyEvidence{}, err
	}
	if context.BaselineKind != BaselineGenesis && context.BaselineKind != BaselineExisting {
		return PolicyEvidence{}, fmt.Errorf("%w: baseline kind is required", ErrInvalidPolicy)
	}
	var (
		baselineIdentity PublicationIdentity
		classification   contractversion.Result
		err              error
	)
	switch context.BaselineKind {
	case BaselineGenesis:
		if context.Existing != nil || context.Baseline != nil {
			return PolicyEvidence{}, fmt.Errorf("%w: genesis cannot carry an existing baseline", ErrInvalidPolicy)
		}
		classification, err = contractversion.ClassifyInitial(candidate.CanonicalBytes)
	case BaselineExisting:
		baseline := context.Existing
		if baseline != nil && context.Baseline != nil {
			return PolicyEvidence{}, fmt.Errorf("%w: baseline supplied twice", ErrInvalidPolicy)
		}
		if baseline == nil {
			baseline = context.Baseline
		}
		if baseline == nil {
			return PolicyEvidence{}, fmt.Errorf("%w: existing baseline is required", ErrInvalidPolicy)
		}
		if err := baseline.Validate(); err != nil {
			return PolicyEvidence{}, fmt.Errorf("%w: invalid existing baseline: %v", ErrInvalidPolicy, err)
		}
		baselineIdentity = baseline.Identity()
		if baselineIdentity.InstanceID != candidate.InstanceID || baselineIdentity.AuthoredID != candidate.AuthoredID || baselineIdentity.ResourceKind != candidate.ResourceKind {
			return PolicyEvidence{}, fmt.Errorf("%w: baseline identity does not match candidate", ErrInvalidPolicy)
		}
		classification, err = contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes)
	}
	if err != nil {
		return PolicyEvidence{}, fmt.Errorf("%w: classify publication: %v", ErrInvalidPolicy, err)
	}
	if err := classification.ValidatePublication(); err != nil {
		return PolicyEvidence{}, fmt.Errorf("%w: classifier result: %v", ErrInvalidPolicy, err)
	}
	evidence := PolicyEvidence{
		Version: PolicyEvidenceVersion, BaselineKind: context.BaselineKind,
		Baseline: baselineIdentity, Candidate: candidate.Identity(),
		Classification:           cloneClassification(classification),
		RequiresSecurityApproval: classification.SecurityImpact == contractversion.SecurityWidening,
		AffectedResources:        directAffectedResources(candidate.Identity()),
		ChangedDimensions:        changedDimensions(classification),
	}
	if classification.RequiresSecurityApproval != evidence.RequiresSecurityApproval {
		return PolicyEvidence{}, fmt.Errorf("%w: classifier approval requirement is not security-widening derived", ErrInvalidPolicy)
	}
	digest, err := evidence.computeDigest()
	if err != nil {
		return PolicyEvidence{}, fmt.Errorf("%w: evidence digest: %v", ErrInvalidPolicy, err)
	}
	evidence.EvidenceDigest = digest
	if err := evidence.Validate(); err != nil {
		return PolicyEvidence{}, err
	}
	return evidence, nil
}

// DeriveGenesisPolicyEvidence is an explicit first-publication helper.
func DeriveGenesisPolicyEvidence(candidate ContractPublication) (PolicyEvidence, error) {
	return DerivePolicyEvidence(PolicyContext{BaselineKind: BaselineGenesis}, candidate)
}

// DeriveUpdatePolicyEvidence is an explicit update helper.
func DeriveUpdatePolicyEvidence(baseline, candidate ContractPublication) (PolicyEvidence, error) {
	return DerivePolicyEvidence(PolicyContext{BaselineKind: BaselineExisting, Existing: &baseline}, candidate)
}

// Validate checks the historical evidence itself.  It deliberately does not
// compare against current time: expired approval evidence remains verifiable
// historical evidence.  Admission-time expiry is checked separately.
func (e PolicyEvidence) Validate() error {
	if e.Version != PolicyEvidenceVersion {
		return fmt.Errorf("%w: unsupported policy evidence version", ErrInvalidPolicy)
	}
	if e.BaselineKind != BaselineGenesis && e.BaselineKind != BaselineExisting {
		return fmt.Errorf("%w: baseline kind", ErrInvalidPolicy)
	}
	if e.BaselineKind == BaselineGenesis {
		if e.Baseline != (PublicationIdentity{}) {
			return fmt.Errorf("%w: genesis baseline must be empty", ErrInvalidPolicy)
		}
	} else {
		if err := e.Baseline.Validate(); err != nil {
			return err
		}
	}
	if err := e.Candidate.Validate(); err != nil {
		return err
	}
	if e.BaselineKind == BaselineExisting && !sameScope(e.Baseline, e.Candidate) {
		return fmt.Errorf("%w: baseline and candidate scope differ", ErrInvalidPolicy)
	}
	if err := e.Classification.ValidatePublication(); err != nil {
		return fmt.Errorf("%w: classifier result: %v", ErrInvalidPolicy, err)
	}
	wantApproval := e.Classification.SecurityImpact == contractversion.SecurityWidening
	if e.RequiresSecurityApproval != wantApproval || e.Classification.RequiresSecurityApproval != wantApproval {
		return fmt.Errorf("%w: security approval requirement is not derived", ErrInvalidPolicy)
	}
	if !equalAffectedResources(e.AffectedResources, directAffectedResources(e.Candidate)) {
		return fmt.Errorf("%w: affected resources are not the exact published resource", ErrInvalidPolicy)
	}
	wantDimensions := changedDimensions(e.Classification)
	if !equalDomains(wantDimensions, e.ChangedDimensions) {
		return fmt.Errorf("%w: changed dimensions are not derived", ErrInvalidPolicy)
	}
	if err := validateDigest(e.EvidenceDigest); err != nil {
		return fmt.Errorf("%w: evidence digest: %v", ErrInvalidPolicy, err)
	}
	wantDigest, err := e.computeDigest()
	if err != nil || wantDigest != e.EvidenceDigest {
		return fmt.Errorf("%w: evidence digest mismatch", ErrInvalidPolicy)
	}
	encoded, err := json.Marshal(e)
	if err != nil || len(encoded) > MaxPolicyEvidenceBytes {
		return fmt.Errorf("%w: evidence exceeds bounds", ErrInvalidPolicy)
	}
	return nil
}

// ValidatePolicyEvidenceAgainst re-runs the sole contractversion classifier
// against exact immutable bytes and compares every derived field.  It is the
// adapter-facing check that closes baseline/candidate substitution and
// classification tampering.
func ValidatePolicyEvidenceAgainst(context PolicyContext, candidate ContractPublication, evidence PolicyEvidence) error {
	want, err := DerivePolicyEvidence(context, candidate)
	if err != nil {
		return err
	}
	if !EqualPolicyEvidence(want, evidence) {
		return fmt.Errorf("%w: policy evidence does not match exact baseline/candidate", ErrInvalidPolicy)
	}
	return nil
}

func (e PolicyEvidence) computeDigest() (string, error) {
	return digestJSON(policyEvidenceDigestPayload{
		Version: e.Version, BaselineKind: e.BaselineKind, Baseline: e.Baseline,
		Candidate: e.Candidate, Classification: e.Classification,
		RequiresSecurityApproval: e.RequiresSecurityApproval,
		AffectedResources:        e.AffectedResources,
		ChangedDimensions:        e.ChangedDimensions,
	})
}

type policyEvidenceDigestPayload struct {
	Version                  int                      `json:"version"`
	BaselineKind             BaselineKind             `json:"baselineKind"`
	Baseline                 PublicationIdentity      `json:"baseline"`
	Candidate                PublicationIdentity      `json:"candidate"`
	Classification           contractversion.Result   `json:"classification"`
	RequiresSecurityApproval bool                     `json:"requiresSecurityApproval"`
	AffectedResources        []PolicyAffectedResource `json:"affectedResources"`
	ChangedDimensions        []contractversion.Domain `json:"changedDimensions"`
}

// ChangedDimensions returns a defensive copy of classifier-derived domains.
func (e PolicyEvidence) Dimensions() []contractversion.Domain {
	return append([]contractversion.Domain(nil), e.ChangedDimensions...)
}

// Affected returns the immutable direct-resource seed consumed by graph-owned
// dependency planning. It never claims to be an exhaustive consumer graph.
func (e PolicyEvidence) Affected() []PolicyAffectedResource {
	return append([]PolicyAffectedResource(nil), e.AffectedResources...)
}

// Clone returns a defensive copy of policy evidence.
func (e PolicyEvidence) Clone() PolicyEvidence {
	e.Classification = cloneClassification(e.Classification)
	if e.AffectedResources != nil {
		e.AffectedResources = append(make([]PolicyAffectedResource, 0, len(e.AffectedResources)), e.AffectedResources...)
	}
	if e.ChangedDimensions != nil {
		e.ChangedDimensions = append(make([]contractversion.Domain, 0, len(e.ChangedDimensions)), e.ChangedDimensions...)
	}
	return e
}

// EqualPolicyEvidence compares deterministic policy evidence including its
// digest.  It excludes no policy fields.
func EqualPolicyEvidence(left, right PolicyEvidence) bool {
	return equalJSON(left, right)
}

// WideningApprovalEvidence is a historical approval assertion, not an
// approval workflow.  It is valid as history after expiry but cannot satisfy
// admission once its expiration instant has passed.
type WideningApprovalEvidence struct {
	Baseline       PublicationIdentity `json:"baseline"`
	Candidate      PublicationIdentity `json:"candidate"`
	PolicyDigest   string              `json:"policyDigest"`
	Actor          string              `json:"actor"`
	ApprovedAt     time.Time           `json:"approvedAt"`
	ExpiresAt      time.Time           `json:"expiresAt"`
	EvidenceDigest string              `json:"evidenceDigest"`
}

// ApprovalEvidence is an adapter-friendly name for the same immutable value.
type ApprovalEvidence = WideningApprovalEvidence

// PrepareWideningApproval derives an approval evidence value bound to exact
// policy evidence.  It does not persist or authorize anything.
func PrepareWideningApproval(policy PolicyEvidence, actor string, approvedAt, expiresAt time.Time) (WideningApprovalEvidence, error) {
	if err := policy.Validate(); err != nil {
		return WideningApprovalEvidence{}, err
	}
	if !policy.RequiresSecurityApproval || policy.Classification.SecurityImpact != contractversion.SecurityWidening {
		return WideningApprovalEvidence{}, fmt.Errorf("%w: approval is only valid for security widening", ErrApprovalMismatch)
	}
	if !validToken(actor, 255) {
		return WideningApprovalEvidence{}, fmt.Errorf("%w: actor", ErrApprovalMismatch)
	}
	approvedAt, expiresAt, err := normalizeApprovalTimes(approvedAt, expiresAt)
	if err != nil {
		return WideningApprovalEvidence{}, err
	}
	evidence := WideningApprovalEvidence{
		Baseline: policy.Baseline, Candidate: policy.Candidate,
		PolicyDigest: policy.EvidenceDigest, Actor: actor,
		ApprovedAt: approvedAt, ExpiresAt: expiresAt,
	}
	digest, err := evidence.computeDigest()
	if err != nil {
		return WideningApprovalEvidence{}, fmt.Errorf("%w: evidence digest: %v", ErrApprovalMismatch, err)
	}
	evidence.EvidenceDigest = digest
	if err := evidence.Validate(); err != nil {
		return WideningApprovalEvidence{}, err
	}
	return evidence, nil
}

// Validate checks approval evidence without checking current time.
func (a WideningApprovalEvidence) Validate() error {
	if err := a.Baseline.ValidateOptional(); err != nil {
		return err
	}
	if err := a.Candidate.Validate(); err != nil {
		return err
	}
	if !validDigest(a.PolicyDigest) || !validToken(a.Actor, 255) {
		return fmt.Errorf("%w: approval identity", ErrApprovalMismatch)
	}
	if a.ApprovedAt.IsZero() || a.ExpiresAt.IsZero() || a.ApprovedAt.Location() != time.UTC || a.ExpiresAt.Location() != time.UTC || !a.ExpiresAt.After(a.ApprovedAt) {
		return fmt.Errorf("%w: approval time interval", ErrApprovalMismatch)
	}
	if !validDigest(a.EvidenceDigest) {
		return fmt.Errorf("%w: approval evidence digest", ErrApprovalMismatch)
	}
	want, err := a.computeDigest()
	if err != nil || want != a.EvidenceDigest {
		return fmt.Errorf("%w: approval evidence digest mismatch", ErrApprovalMismatch)
	}
	return nil
}

// ValidAt verifies the historical evidence and then applies the live expiry
// interval for an admission instant.
func (a WideningApprovalEvidence) ValidAt(now time.Time) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if now.IsZero() {
		return ErrApprovalExpired
	}
	instant := now.UTC()
	if instant.Before(a.ApprovedAt) || !instant.Before(a.ExpiresAt) {
		return ErrApprovalExpired
	}
	return nil
}

// ValidateAdmission checks all exact bindings and the current-time expiry
// rule.  It is the only helper that treats expiry as a live admission fact.
func ValidateAdmission(context PolicyContext, candidate ContractPublication, policy PolicyEvidence, approval *WideningApprovalEvidence, now time.Time) error {
	if err := ValidatePolicyEvidenceAgainst(context, candidate, policy); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: admission time is required", ErrInvalidPolicy)
	}
	if policy.RequiresSecurityApproval {
		if approval == nil {
			return ErrApprovalRequired
		}
		if err := approval.Validate(); err != nil {
			return err
		}
		if !EqualPublicationIdentity(approval.Baseline, policy.Baseline) || !EqualPublicationIdentity(approval.Candidate, policy.Candidate) || approval.PolicyDigest != policy.EvidenceDigest {
			return ErrApprovalMismatch
		}
		return approval.ValidAt(now)
	}
	if approval != nil {
		return ErrApprovalUnexpected
	}
	return nil
}

// ValidateQualifiedPublication verifies a persisted publication's nested
// policy and approval evidence against its exact canonical bytes and current
// admission time.  It is the adapter/read-path helper; it never rewrites
// historical evidence.  A publication without policy evidence is a valid
// historical validation-only record but is not qualified for admission.
func ValidateQualifiedPublication(context PolicyContext, publication ContractPublication, now time.Time) error {
	if err := ValidateHistoricalPublication(context, publication); err != nil {
		return err
	}
	policy := *publication.Validation.PolicyEvidence
	if policy.RequiresSecurityApproval {
		return publication.Validation.ApprovalEvidence.ValidAt(now)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: admission time is required", ErrInvalidPolicy)
	}
	return nil
}

// ValidateHistoricalPublication verifies a persisted qualified record without
// applying wall-clock expiry.  This keeps expired approval evidence
// internally verifiable while still requiring its exact immutable bindings.
func ValidateHistoricalPublication(context PolicyContext, publication ContractPublication) error {
	if err := publication.Validate(); err != nil {
		return err
	}
	if publication.Validation.PolicyEvidence == nil {
		return fmt.Errorf("%w: qualified publication requires policy evidence", ErrInvalidPolicy)
	}
	policy := *publication.Validation.PolicyEvidence
	if err := ValidatePolicyEvidenceAgainst(context, publication, policy); err != nil {
		return err
	}
	approval := publication.Validation.ApprovalEvidence
	if policy.RequiresSecurityApproval {
		if approval == nil {
			return ErrApprovalRequired
		}
		if err := approval.Validate(); err != nil {
			return err
		}
		if !EqualPublicationIdentity(approval.Baseline, policy.Baseline) || !EqualPublicationIdentity(approval.Candidate, policy.Candidate) || approval.PolicyDigest != policy.EvidenceDigest {
			return ErrApprovalMismatch
		}
		return nil
	}
	if approval != nil {
		return ErrApprovalUnexpected
	}
	return nil
}

// AttachPolicyEvidence returns a cloned publication with normalized policy and
// optional approval evidence. Exact baseline/candidate derivation is still
// required through ValidateAdmission before persistence.
func AttachPolicyEvidence(context PolicyContext, publication ContractPublication, policy PolicyEvidence, approval *WideningApprovalEvidence) (ContractPublication, error) {
	if err := publication.Validate(); err != nil {
		return ContractPublication{}, err
	}
	if err := ValidatePolicyEvidenceAgainst(context, publication, policy); err != nil {
		return ContractPublication{}, err
	}
	if !EqualPublicationIdentity(policy.Candidate, publication.Identity()) {
		return ContractPublication{}, fmt.Errorf("%w: policy candidate does not match publication", ErrInvalidPolicy)
	}
	result := publication.Clone()
	result.Validation.PolicyEvidence = ptrPolicy(policy.Clone())
	result.Validation.ApprovalEvidence = nil
	if policy.RequiresSecurityApproval && approval == nil {
		return ContractPublication{}, ErrApprovalRequired
	}
	if approval != nil {
		if !policy.RequiresSecurityApproval {
			return ContractPublication{}, ErrApprovalUnexpected
		}
		if err := approval.Validate(); err != nil {
			return ContractPublication{}, err
		}
		if !EqualPublicationIdentity(approval.Baseline, policy.Baseline) || !EqualPublicationIdentity(approval.Candidate, policy.Candidate) || approval.PolicyDigest != policy.EvidenceDigest {
			return ContractPublication{}, ErrApprovalMismatch
		}
		copyApproval := approval.Clone()
		result.Validation.ApprovalEvidence = &copyApproval
	}
	normalized, err := normalizeValidationEvidence(result.Validation, true)
	if err != nil {
		return ContractPublication{}, err
	}
	result.Validation = normalized
	return result, nil
}

// ValidateOptional validates the zero identity used for a genesis approval
// baseline, while rejecting malformed non-zero identities.
func (i PublicationIdentity) ValidateOptional() error {
	if i == (PublicationIdentity{}) {
		return nil
	}
	return i.Validate()
}

func (i PublicationIdentity) Validate() error {
	if !validToken(i.InstanceID, 255) {
		return fmt.Errorf("%w: publication instance id", ErrInvalidPolicy)
	}
	if err := i.AuthoredID.Validate(); err != nil {
		return fmt.Errorf("%w: publication authored id: %v", ErrInvalidPolicy, err)
	}
	if !validPublicationKind(i.ResourceKind) || !validToken(i.Version, 255) || !validToken(i.VersionBaseline, 255) || i.ProjectionProfile != contractprojection.Profile || !validDigest(i.Digest) {
		return fmt.Errorf("%w: publication identity", ErrInvalidPolicy)
	}
	baseline, err := contractversion.SemverBaseline(i.Version)
	if err != nil || strings.TrimPrefix(baseline, "v") != i.VersionBaseline {
		return fmt.Errorf("%w: publication version baseline mismatch", ErrInvalidPolicy)
	}
	return nil
}

func sameScope(left, right PublicationIdentity) bool {
	return left.InstanceID == right.InstanceID && left.AuthoredID == right.AuthoredID && left.ResourceKind == right.ResourceKind
}

func changedDimensions(result contractversion.Result) []contractversion.Domain {
	seen := make(map[contractversion.Domain]struct{}, len(result.Changes))
	for _, change := range result.Changes {
		seen[change.Domain] = struct{}{}
	}
	domains := make([]contractversion.Domain, 0, len(seen))
	for domain := range seen {
		domains = append(domains, domain)
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i] < domains[j] })
	return domains
}

func equalDomains(left, right []contractversion.Domain) bool {
	return equalJSON(left, right)
}

func cloneClassification(result contractversion.Result) contractversion.Result {
	if result.Changes != nil {
		result.Changes = append(make([]contractversion.Change, 0, len(result.Changes)), result.Changes...)
	}
	return result
}

func (a WideningApprovalEvidence) Clone() WideningApprovalEvidence { return a }

func (a WideningApprovalEvidence) computeDigest() (string, error) {
	return digestJSON(approvalDigestPayload{
		Baseline: a.Baseline, Candidate: a.Candidate, PolicyDigest: a.PolicyDigest,
		Actor: a.Actor, ApprovedAt: a.ApprovedAt.UTC(), ExpiresAt: a.ExpiresAt.UTC(),
	})
}

type approvalDigestPayload struct {
	Baseline     PublicationIdentity `json:"baseline"`
	Candidate    PublicationIdentity `json:"candidate"`
	PolicyDigest string              `json:"policyDigest"`
	Actor        string              `json:"actor"`
	ApprovedAt   time.Time           `json:"approvedAt"`
	ExpiresAt    time.Time           `json:"expiresAt"`
}

func normalizeApprovalTimes(approvedAt, expiresAt time.Time) (time.Time, time.Time, error) {
	if approvedAt.IsZero() || expiresAt.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: approval times are required", ErrApprovalMismatch)
	}
	approvedAt, expiresAt = approvedAt.UTC(), expiresAt.UTC()
	if !expiresAt.After(approvedAt) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: expiry must be after approval", ErrApprovalMismatch)
	}
	return approvedAt, expiresAt, nil
}

func ptrPolicy(value PolicyEvidence) *PolicyEvidence { return &value }

func validateDigest(value string) error {
	if !validDigest(value) {
		return fmt.Errorf("invalid SHA-256 digest %q", value)
	}
	return nil
}

func validDigest(value string) bool { return platformdigest.ValidateSHA256Identity(value) == nil }
