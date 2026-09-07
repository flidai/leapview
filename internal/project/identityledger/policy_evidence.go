package identityledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"unicode"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	ocidigest "github.com/opencontainers/go-digest"
)

const (
	// PolicyEvidenceVersion identifies the nested typed evidence payload. The
	// outer ValidationEvidence envelope remains version one because the
	// existing immutable PostgreSQL check constrains that field to "1".
	PolicyEvidenceVersion = 2
	// RegistryPolicyEvidenceVersion adds retained Access-owned type evidence.
	// Version two remains readable with its original digest preimage.
	RegistryPolicyEvidenceVersion = 3

	PolicyBaselineGenesis  PolicyBaselineKind = "genesis"
	PolicyBaselineExisting PolicyBaselineKind = "existing"

	PolicyApprovalRequired    PolicyApprovalState = "required"
	PolicyApprovalNotRequired PolicyApprovalState = "not-required"

	PolicyAffectedResourceScope = "published-resource-only"
)

var (
	ErrPolicyEvidenceInvalid  = errors.New("invalid contract policy evidence")
	ErrPolicyEvidenceConflict = errors.New("contract policy evidence conflict")
)

// PolicyBaselineKind makes the initial publication case explicit. A missing
// baseline is not inferred to mean genesis because doing so would let a stale
// update masquerade as a first publication.
type PolicyBaselineKind string

// PolicyApprovalState is intentionally a two-state input to the future
// approval workflow. There is no approved state in immutable publication
// evidence; approval is a later control-plane decision.
type PolicyApprovalState string

// PolicyPublicationIdentity is the exact identity of a publication row. It
// is a reference, not a new resource identity or digest authority.
type PolicyPublicationIdentity struct {
	InstanceID        string                  `json:"instanceId"`
	AuthoredID        projectgraph.ResourceID `json:"authoredId"`
	ResourceKind      projectgraph.Kind       `json:"resourceKind"`
	Version           string                  `json:"version"`
	VersionBaseline   string                  `json:"versionBaseline"`
	ProjectionProfile string                  `json:"projectionProfile"`
	Digest            string                  `json:"digest"`
}

// PolicyContext is supplied by the publication caller and checked against
// live identity/publication rows under the publication transaction. The
// baseline bytes themselves are always read from the immutable ledger.
type PolicyContext struct {
	BaselineKind              PolicyBaselineKind
	Baseline                  *PolicyPublicationIdentity
	ExpectedLifecycleSequence int64
	ExpectedRegistry          *PolicyRegistryReference
}

// PolicyRegistryReference is an expected revision reference, not registry
// authority. The publication adapter resolves definitions under its transaction.
type PolicyRegistryReference struct {
	InstanceID      string                  `json:"instanceId"`
	ProjectID       projectgraph.ResourceID `json:"projectId"`
	ControlRevision int64                   `json:"controlRevision"`
	Profile         string                  `json:"profile"`
	Revision        int64                   `json:"revision"`
	Digest          string                  `json:"digest"`
}

func (r PolicyRegistryReference) Validate() error {
	return (contractversion.SemanticRegistryTypes{
		InstanceID: r.InstanceID, ProjectID: r.ProjectID.String(), ControlRevision: r.ControlRevision,
		Profile: r.Profile, Revision: r.Revision, Digest: r.Digest,
	}).Validate()
}

func (r PolicyRegistryReference) Matches(types contractversion.SemanticRegistryTypes) bool {
	return r.InstanceID == types.InstanceID && r.ProjectID.String() == types.ProjectID &&
		r.ControlRevision == types.ControlRevision && r.Profile == types.Profile &&
		r.Revision == types.Revision && r.Digest == types.Digest
}

// PolicyAffectedResource is deliberately scoped to the directly published
// resource. Project-level dependency impact planning belongs to the graph
// consumer and must not be reconstructed in this ledger.
type PolicyAffectedResource struct {
	InstanceID   string                  `json:"instanceId"`
	AuthoredID   projectgraph.ResourceID `json:"authoredId"`
	ResourceKind projectgraph.Kind       `json:"resourceKind"`
	Scope        string                  `json:"scope"`
}

// PolicyEvidence is immutable, server-derived evidence attached to a
// contract publication. Candidate and Publication are both retained so a
// consumer can distinguish caller intent from the identity actually written,
// even though they must be equal for a valid record.
type PolicyEvidence struct {
	Version           int                                    `json:"version"`
	BaselineKind      PolicyBaselineKind                     `json:"baselineKind"`
	Baseline          PolicyPublicationIdentity              `json:"baseline"`
	Candidate         PolicyPublicationIdentity              `json:"candidate"`
	Publication       PolicyPublicationIdentity              `json:"publication"`
	LifecycleSequence int64                                  `json:"lifecycleSequence"`
	ActiveBundleID    string                                 `json:"activeBundleId"`
	Classification    contractversion.Result                 `json:"classification"`
	ApprovalState     PolicyApprovalState                    `json:"approvalState"`
	AffectedResources []PolicyAffectedResource               `json:"affectedResources"`
	ChangedDimensions []contractversion.Domain               `json:"changedDimensions"`
	EvidenceDigest    string                                 `json:"evidenceDigest"`
	RegistryTypes     *contractversion.SemanticRegistryTypes `json:"registryTypes,omitempty"`
}

// PolicyDecision is the safe read view used by planning. It contains no
// evaluator and cannot turn missing historical evidence into approval.
type PolicyDecision struct {
	BaselineKind      PolicyBaselineKind                     `json:"baselineKind"`
	Baseline          PolicyPublicationIdentity              `json:"baseline"`
	Candidate         PolicyPublicationIdentity              `json:"candidate"`
	Publication       PolicyPublicationIdentity              `json:"publication"`
	LifecycleSequence int64                                  `json:"lifecycleSequence"`
	ActiveBundleID    string                                 `json:"activeBundleId"`
	EvidenceDigest    string                                 `json:"evidenceDigest"`
	ApprovalRequired  bool                                   `json:"approvalRequired"`
	ApprovalState     PolicyApprovalState                    `json:"approvalState"`
	Classification    contractversion.Result                 `json:"classification"`
	ChangedDimensions []contractversion.Domain               `json:"changedDimensions"`
	AffectedResources []PolicyAffectedResource               `json:"affectedResources"`
	RegistryTypes     *contractversion.SemanticRegistryTypes `json:"registryTypes,omitempty"`
}

func (i PolicyPublicationIdentity) Validate() error {
	if !validPolicyToken(i.InstanceID, 255) {
		return fmt.Errorf("%w: publication instance id", ErrPolicyEvidenceInvalid)
	}
	if err := i.AuthoredID.Validate(); err != nil {
		return fmt.Errorf("%w: publication authored id: %v", ErrPolicyEvidenceInvalid, err)
	}
	if !validPolicyKind(i.ResourceKind) {
		return fmt.Errorf("%w: publication resource kind", ErrPolicyEvidenceInvalid)
	}
	if i.Version == "" || i.VersionBaseline == "" {
		return fmt.Errorf("%w: publication version identity is incomplete", ErrPolicyEvidenceInvalid)
	}
	baseline, err := contractversion.SemverBaseline(i.Version)
	if err != nil || strings.TrimPrefix(baseline, "v") != i.VersionBaseline {
		return fmt.Errorf("%w: publication version baseline mismatch", ErrPolicyEvidenceInvalid)
	}
	if !validPolicyText(i.ProjectionProfile, 255) {
		return fmt.Errorf("%w: publication projection profile", ErrPolicyEvidenceInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(i.Digest); err != nil {
		return fmt.Errorf("%w: publication digest: %v", ErrPolicyEvidenceInvalid, err)
	}
	return nil
}

func (i PolicyPublicationIdentity) equal(other PolicyPublicationIdentity) bool {
	return i.InstanceID == other.InstanceID && i.AuthoredID == other.AuthoredID &&
		i.ResourceKind == other.ResourceKind && i.Version == other.Version &&
		i.VersionBaseline == other.VersionBaseline && i.ProjectionProfile == other.ProjectionProfile &&
		i.Digest == other.Digest
}

// EqualPolicyPublicationIdentity compares all immutable publication identity
// fields. It is useful to adapters without exposing a second identity model.
func EqualPolicyPublicationIdentity(left, right PolicyPublicationIdentity) bool {
	return left.equal(right)
}

func (c PolicyContext) validate(instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind) error {
	if c.ExpectedRegistry != nil {
		if err := c.ExpectedRegistry.Validate(); err != nil {
			return fmt.Errorf("%w: expected registry: %v", ErrPolicyEvidenceInvalid, err)
		}
		if kind != projectgraph.KindSemanticModel || c.ExpectedRegistry.InstanceID != instanceID {
			return fmt.Errorf("%w: expected registry resource scope", ErrPolicyEvidenceInvalid)
		}
	}
	if c.ExpectedLifecycleSequence <= 0 {
		return fmt.Errorf("%w: expected lifecycle sequence is required", ErrPolicyEvidenceInvalid)
	}
	if c.BaselineKind != PolicyBaselineGenesis && c.BaselineKind != PolicyBaselineExisting {
		return fmt.Errorf("%w: baseline kind is required", ErrPolicyEvidenceInvalid)
	}
	if c.BaselineKind == PolicyBaselineGenesis {
		if c.Baseline != nil {
			return fmt.Errorf("%w: genesis cannot carry a baseline publication", ErrPolicyEvidenceInvalid)
		}
		return nil
	}
	if c.Baseline == nil {
		return fmt.Errorf("%w: existing publication baseline is required", ErrPolicyEvidenceInvalid)
	}
	if err := c.Baseline.Validate(); err != nil {
		return err
	}
	if c.Baseline.InstanceID != instanceID || c.Baseline.AuthoredID != authoredID || c.Baseline.ResourceKind != kind {
		return fmt.Errorf("%w: baseline publication identity does not match candidate", ErrPolicyEvidenceInvalid)
	}
	return nil
}

// ValidateForPublication checks only the caller-bound identity/context shape.
// Baseline bytes and current lifecycle state are resolved transactionally by
// the PostgreSQL repository.
func (c PolicyContext) ValidateForPublication(instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind) error {
	return c.validate(instanceID, authoredID, kind)
}

// Validate enforces all policy evidence signals, including the digest over
// the typed standard-JSON preimage. It intentionally does not mutate or infer
// omitted values.
func (e PolicyEvidence) Validate() error {
	if e.Version != PolicyEvidenceVersion && e.Version != RegistryPolicyEvidenceVersion {
		return fmt.Errorf("%w: unsupported policy evidence version", ErrPolicyEvidenceInvalid)
	}
	if (e.Version == RegistryPolicyEvidenceVersion) != (e.RegistryTypes != nil) {
		return fmt.Errorf("%w: registry evidence/version mismatch", ErrPolicyEvidenceInvalid)
	}
	if e.RegistryTypes != nil {
		if err := e.RegistryTypes.Validate(); err != nil {
			return fmt.Errorf("%w: registry types: %v", ErrPolicyEvidenceInvalid, err)
		}
		if e.RegistryTypes.InstanceID != e.Candidate.InstanceID || e.Candidate.ResourceKind != projectgraph.KindSemanticModel {
			return fmt.Errorf("%w: registry evidence scope mismatch", ErrPolicyEvidenceInvalid)
		}
	}
	if e.BaselineKind != PolicyBaselineGenesis && e.BaselineKind != PolicyBaselineExisting {
		return fmt.Errorf("%w: baseline kind", ErrPolicyEvidenceInvalid)
	}
	if e.BaselineKind == PolicyBaselineExisting {
		if err := e.Baseline.Validate(); err != nil {
			return err
		}
	} else if e.Baseline != (PolicyPublicationIdentity{}) {
		return fmt.Errorf("%w: genesis baseline must be empty", ErrPolicyEvidenceInvalid)
	}
	if err := e.Candidate.Validate(); err != nil {
		return err
	}
	if err := e.Publication.Validate(); err != nil {
		return err
	}
	if !e.Candidate.equal(e.Publication) {
		return fmt.Errorf("%w: candidate/publication identity mismatch", ErrPolicyEvidenceInvalid)
	}
	if e.BaselineKind == PolicyBaselineExisting && (e.Baseline.InstanceID != e.Candidate.InstanceID || e.Baseline.AuthoredID != e.Candidate.AuthoredID || e.Baseline.ResourceKind != e.Candidate.ResourceKind) {
		return fmt.Errorf("%w: baseline resource scope mismatch", ErrPolicyEvidenceInvalid)
	}
	if e.LifecycleSequence <= 0 || !validPolicyToken(e.ActiveBundleID, 255) {
		return fmt.Errorf("%w: lifecycle binding is incomplete", ErrPolicyEvidenceInvalid)
	}
	if err := e.Classification.ValidatePublication(); err != nil {
		return fmt.Errorf("%w: classifier result: %w", ErrPolicyEvidenceInvalid, err)
	}
	if e.ApprovalState != PolicyApprovalRequired && e.ApprovalState != PolicyApprovalNotRequired {
		return fmt.Errorf("%w: unsupported approval state", ErrPolicyEvidenceInvalid)
	}
	if (e.ApprovalState == PolicyApprovalRequired) != e.Classification.RequiresSecurityApproval {
		return fmt.Errorf("%w: approval state is not derived from classifier result", ErrPolicyEvidenceInvalid)
	}
	if err := validateAffectedResources(e.AffectedResources, e.Candidate); err != nil {
		return err
	}
	derivedDimensions := policyChangedDimensions(e.Classification)
	if !equalDomains(derivedDimensions, e.ChangedDimensions) {
		return fmt.Errorf("%w: changed dimensions are not derived from classifier changes", ErrPolicyEvidenceInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(e.EvidenceDigest); err != nil {
		return fmt.Errorf("%w: evidence digest: %v", ErrPolicyEvidenceInvalid, err)
	}
	expected, err := e.computeDigest()
	if err != nil || expected != e.EvidenceDigest {
		return fmt.Errorf("%w: evidence digest mismatch", ErrPolicyEvidenceInvalid)
	}
	encoded, err := json.Marshal(e)
	if err != nil || len(encoded) > 16<<10 {
		return fmt.Errorf("%w: policy evidence exceeds 16KiB", ErrPolicyEvidenceInvalid)
	}
	return nil
}

func (e PolicyEvidence) Decision() (PolicyDecision, error) {
	if err := e.Validate(); err != nil {
		return PolicyDecision{}, err
	}
	return PolicyDecision{
		BaselineKind: e.BaselineKind, Baseline: e.Baseline, Candidate: e.Candidate,
		Publication: e.Publication, LifecycleSequence: e.LifecycleSequence,
		ActiveBundleID: e.ActiveBundleID,
		EvidenceDigest: e.EvidenceDigest, ApprovalRequired: e.ApprovalState == PolicyApprovalRequired,
		ApprovalState: e.ApprovalState, Classification: clonePolicyResult(e.Classification),
		ChangedDimensions: slices.Clone(e.ChangedDimensions),
		AffectedResources: slices.Clone(e.AffectedResources),
		RegistryTypes:     cloneRegistryTypes(e.RegistryTypes),
	}, nil
}

func clonePolicyResult(result contractversion.Result) contractversion.Result {
	result.Changes = slices.Clone(result.Changes)
	return result
}

func (e PolicyEvidence) computeDigest() (string, error) {
	payload := policyEvidenceDigestPayload{
		Version: e.Version, BaselineKind: e.BaselineKind, Baseline: e.Baseline, Candidate: e.Candidate,
		Publication: e.Publication, LifecycleSequence: e.LifecycleSequence,
		ActiveBundleID: e.ActiveBundleID, Classification: e.Classification,
		ApprovalState: e.ApprovalState, AffectedResources: e.AffectedResources,
		ChangedDimensions: e.ChangedDimensions,
		RegistryTypes:     e.RegistryTypes,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return ocidigest.FromBytes(encoded).String(), nil
}

type policyEvidenceDigestPayload struct {
	Version           int                                    `json:"version"`
	BaselineKind      PolicyBaselineKind                     `json:"baselineKind"`
	Baseline          PolicyPublicationIdentity              `json:"baseline"`
	Candidate         PolicyPublicationIdentity              `json:"candidate"`
	Publication       PolicyPublicationIdentity              `json:"publication"`
	LifecycleSequence int64                                  `json:"lifecycleSequence"`
	ActiveBundleID    string                                 `json:"activeBundleId"`
	Classification    contractversion.Result                 `json:"classification"`
	ApprovalState     PolicyApprovalState                    `json:"approvalState"`
	AffectedResources []PolicyAffectedResource               `json:"affectedResources"`
	ChangedDimensions []contractversion.Domain               `json:"changedDimensions"`
	RegistryTypes     *contractversion.SemanticRegistryTypes `json:"registryTypes,omitempty"`
}

func cloneRegistryTypes(value *contractversion.SemanticRegistryTypes) *contractversion.SemanticRegistryTypes {
	if value == nil {
		return nil
	}
	clone := value.Clone()
	return &clone
}

// EqualPolicyContextEvidence compares retry intent with retained evidence. It
// does not consult current registry state or reclassify immutable publications.
func EqualPolicyContextEvidence(context PolicyContext, evidence *PolicyEvidence) bool {
	if evidence == nil || evidence.Validate() != nil || context.validate(evidence.Candidate.InstanceID, evidence.Candidate.AuthoredID, evidence.Candidate.ResourceKind) != nil ||
		context.ExpectedLifecycleSequence != evidence.LifecycleSequence || context.BaselineKind != evidence.BaselineKind {
		return false
	}
	if context.BaselineKind == PolicyBaselineExisting && (context.Baseline == nil || !context.Baseline.equal(evidence.Baseline)) {
		return false
	}
	if context.ExpectedRegistry == nil || evidence.RegistryTypes == nil {
		return context.ExpectedRegistry == nil && evidence.RegistryTypes == nil
	}
	return context.ExpectedRegistry.Matches(*evidence.RegistryTypes)
}

func validateAffectedResources(resources []PolicyAffectedResource, candidate PolicyPublicationIdentity) error {
	if len(resources) != 1 {
		return fmt.Errorf("%w: exactly one published-resource-only affected resource is required", ErrPolicyEvidenceInvalid)
	}
	foundDirect := false
	for _, resource := range resources {
		if !validPolicyToken(resource.InstanceID, 255) || resource.AuthoredID.Validate() != nil || !validPolicyKind(resource.ResourceKind) || resource.Scope != PolicyAffectedResourceScope {
			return fmt.Errorf("%w: affected resource identity or scope", ErrPolicyEvidenceInvalid)
		}
		if resource.InstanceID == candidate.InstanceID && resource.AuthoredID == candidate.AuthoredID && resource.ResourceKind == candidate.ResourceKind {
			foundDirect = true
		}
	}
	if !foundDirect {
		return fmt.Errorf("%w: published resource is not affected", ErrPolicyEvidenceInvalid)
	}
	return nil
}

func policyChangedDimensions(result contractversion.Result) []contractversion.Domain {
	seen := make(map[contractversion.Domain]struct{})
	for _, change := range result.Changes {
		seen[change.Domain] = struct{}{}
	}
	resultDomains := make([]contractversion.Domain, 0, len(seen))
	for domain := range seen {
		resultDomains = append(resultDomains, domain)
	}
	sort.Slice(resultDomains, func(i, j int) bool { return resultDomains[i] < resultDomains[j] })
	return resultDomains
}

func equalDomains(left, right []contractversion.Domain) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validPolicyKind(kind projectgraph.Kind) bool {
	return kind == projectgraph.KindSource || kind == projectgraph.KindModel || kind == projectgraph.KindSemanticModel
}

func validPolicyToken(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validPolicyText(value string, limit int) bool { return validPolicyToken(value, limit) }

// normalizePolicyEvidence copies and validates evidence before persistence so
// callers cannot mutate the value after it has been used for a digest.
func normalizePolicyEvidence(value PolicyEvidence) (PolicyEvidence, error) {
	if err := value.Validate(); err != nil {
		return PolicyEvidence{}, err
	}
	result := value
	result.AffectedResources = slices.Clone(value.AffectedResources)
	result.ChangedDimensions = slices.Clone(value.ChangedDimensions)
	result.Classification.Changes = slices.Clone(value.Classification.Changes)
	result.RegistryTypes = cloneRegistryTypes(value.RegistryTypes)
	return result, nil
}

// NewPolicyEvidence is a pure evidence derivation helper, not publication
// admission. Its legacy mode exists to reproduce historical v2 evidence.
// The PostgreSQL publication adapter alone admits new publications and requires
// trusted registered types for protected contracts before calling this helper.
// It builds server-owned evidence from the classifier result
// and exact publication/lifecycle observations. Callers cannot supply the
// approval state, affected-resource scope, changed dimensions, or digest.
func NewPolicyEvidence(context PolicyContext, baseline *ContractPublication, candidate ContractPublication, sequence int64, activeBundleID string, result contractversion.Result, registry ...contractversion.SemanticRegistryTypes) (PolicyEvidence, error) {
	if err := result.ValidatePublication(); err != nil {
		return PolicyEvidence{}, fmt.Errorf("%w: classifier result: %w", ErrPolicyEvidenceInvalid, err)
	}
	derived, err := DerivePolicyEvidence(context, baseline, candidate, sequence, activeBundleID, registry...)
	if err != nil {
		return PolicyEvidence{}, err
	}
	if !reflect.DeepEqual(derived.Classification, result) {
		return PolicyEvidence{}, fmt.Errorf("%w: supplied classifier result does not match canonical baseline and candidate", ErrPolicyEvidenceConflict)
	}
	return derived, nil
}

// DerivePolicyEvidence classifies the exact baseline/candidate pair and seals
// the result into policy evidence. It is a pure helper, not a registry or
// publication trust boundary: only the PostgreSQL adapter may use it to publish.
// Omitted types reproduce historical v2 evidence; new protected publication is
// admitted only after the adapter resolves trusted types transactionally.
// Callers cannot provide summary flags or an evidence digest.
func DerivePolicyEvidence(context PolicyContext, baseline *ContractPublication, candidate ContractPublication, sequence int64, activeBundleID string, registry ...contractversion.SemanticRegistryTypes) (PolicyEvidence, error) {
	if err := context.validate(candidate.InstanceID, candidate.AuthoredID, candidate.ResourceKind); err != nil {
		return PolicyEvidence{}, err
	}
	if len(registry) > 1 || (context.ExpectedRegistry != nil) != (len(registry) == 1) {
		return PolicyEvidence{}, fmt.Errorf("%w: expected and resolved registry evidence are required together", ErrPolicyEvidenceInvalid)
	}
	if len(registry) == 1 {
		if err := registry[0].Validate(); err != nil {
			return PolicyEvidence{}, fmt.Errorf("%w: registry types: %v", ErrPolicyEvidenceInvalid, err)
		}
		if !context.ExpectedRegistry.Matches(registry[0]) {
			return PolicyEvidence{}, fmt.Errorf("%w: resolved registry differs from expected revision", ErrPolicyEvidenceConflict)
		}
	}
	if baseline != nil {
		identity := policyIdentityFromPublication(*baseline)
		if err := identity.Validate(); err != nil {
			return PolicyEvidence{}, err
		}
		if context.Baseline == nil || !context.Baseline.equal(identity) {
			return PolicyEvidence{}, fmt.Errorf("%w: baseline publication identity does not match context", ErrPolicyEvidenceConflict)
		}
	} else if context.BaselineKind != PolicyBaselineGenesis || context.Baseline != nil {
		return PolicyEvidence{}, fmt.Errorf("%w: missing genesis baseline", ErrPolicyEvidenceConflict)
	}
	if err := validatePublicationContent(candidate); err != nil {
		return PolicyEvidence{}, err
	}
	if baseline != nil {
		if err := validatePublicationContent(*baseline); err != nil {
			return PolicyEvidence{}, err
		}
	}
	if baseline != nil && len(registry) == 1 {
		if err := validateRetainedRegistryTypes(*baseline, registry[0]); err != nil {
			return PolicyEvidence{}, err
		}
	}
	if len(registry) == 1 {
		if err := validateRegistryTypeSelection(baseline, candidate, registry[0]); err != nil {
			return PolicyEvidence{}, err
		}
	}
	result, err := policyClassification(baseline, candidate, registry...)
	if err != nil {
		return PolicyEvidence{}, err
	}
	return newPolicyEvidence(context, baseline, candidate, sequence, activeBundleID, result, registry...)
}

func validateRegistryTypeSelection(baseline *ContractPublication, candidate ContractPublication, types contractversion.SemanticRegistryTypes) error {
	names := map[string]bool{}
	publications := []ContractPublication{candidate}
	if baseline != nil {
		publications = append(publications, *baseline)
	}
	for _, publication := range publications {
		referenced, err := contractversion.ReferencedSemanticAttributes(publication.CanonicalBytes)
		if err != nil {
			return err
		}
		for _, name := range referenced {
			names[name] = true
		}
	}
	if len(names) != len(types.Definitions) {
		return fmt.Errorf("%w: retained type evidence must match referenced attribute union", ErrPolicyEvidenceInvalid)
	}
	for _, definition := range types.Definitions {
		if !names[definition.Name] {
			return fmt.Errorf("%w: unreferenced registered type evidence", ErrPolicyEvidenceInvalid)
		}
	}
	return nil
}

// The registry owns immutable type identity. A retained typed baseline must
// not be silently reinterpreted if a supplied authority violates that identity.
// Metadata revisions may advance; referenced types may not be reassigned.
func validateRetainedRegistryTypes(baseline ContractPublication, current contractversion.SemanticRegistryTypes) error {
	if baseline.Validation.PolicyEvidence == nil || baseline.Validation.PolicyEvidence.RegistryTypes == nil {
		return nil // Historical evidence did not retain registered types.
	}
	decision, err := baseline.PolicyDecision()
	if err != nil {
		return err
	}
	previous := decision.RegistryTypes
	if previous.InstanceID != current.InstanceID || previous.ProjectID != current.ProjectID || previous.Profile != current.Profile || previous.Revision > current.Revision || previous.ControlRevision > current.ControlRevision || (previous.Revision == current.Revision && previous.Digest != current.Digest) {
		return fmt.Errorf("%w: retained registry authority scope/revision differs", ErrPolicyEvidenceConflict)
	}
	names, err := contractversion.ReferencedSemanticAttributes(baseline.CanonicalBytes)
	if err != nil {
		return err
	}
	oldDefinitions := make(map[string]contractversion.RegisteredSemanticType, len(previous.Definitions))
	for _, definition := range previous.Definitions {
		oldDefinitions[definition.Name] = definition
	}
	newDefinitions := make(map[string]contractversion.RegisteredSemanticType, len(current.Definitions))
	for _, definition := range current.Definitions {
		newDefinitions[definition.Name] = definition
	}
	for _, name := range names {
		old, existed := oldDefinitions[name]
		next, exists := newDefinitions[name]
		if !existed || !exists || old.ID != next.ID || old.Type != next.Type || old.Shape != next.Shape || next.Version < old.Version {
			return fmt.Errorf("%w: retained registered type identity changed for %q", ErrPolicyEvidenceConflict, name)
		}
	}
	return nil
}

func policyClassification(baseline *ContractPublication, candidate ContractPublication, registry ...contractversion.SemanticRegistryTypes) (contractversion.Result, error) {
	var result contractversion.Result
	var err error
	if baseline == nil {
		result, err = contractversion.ClassifyInitial(candidate.CanonicalBytes, registry...)
	} else {
		result, err = contractversion.ValidateVersionTransition(baseline.CanonicalBytes, candidate.CanonicalBytes, registry...)
	}
	if err != nil {
		return contractversion.Result{}, fmt.Errorf("%w: classify publication policy: %w", ErrPolicyEvidenceConflict, err)
	}
	return result, nil
}

func newPolicyEvidence(context PolicyContext, baseline *ContractPublication, candidate ContractPublication, sequence int64, activeBundleID string, result contractversion.Result, registry ...contractversion.SemanticRegistryTypes) (PolicyEvidence, error) {
	if err := result.ValidatePublication(); err != nil {
		return PolicyEvidence{}, fmt.Errorf("%w: classifier result: %w", ErrPolicyEvidenceInvalid, err)
	}
	if sequence <= 0 || !validPolicyToken(activeBundleID, 255) {
		return PolicyEvidence{}, fmt.Errorf("%w: lifecycle binding is incomplete", ErrPolicyEvidenceInvalid)
	}
	if sequence != context.ExpectedLifecycleSequence {
		return PolicyEvidence{}, fmt.Errorf("%w: lifecycle sequence does not match context", ErrPolicyEvidenceConflict)
	}
	candidateIdentity := policyIdentityFromPublication(candidate)
	if err := candidateIdentity.Validate(); err != nil {
		return PolicyEvidence{}, err
	}
	if err := validatePublicationContent(candidate); err != nil {
		return PolicyEvidence{}, err
	}
	if baseline != nil {
		if err := validatePublicationContent(*baseline); err != nil {
			return PolicyEvidence{}, err
		}
	}
	evidence := PolicyEvidence{
		Version: PolicyEvidenceVersion, BaselineKind: context.BaselineKind,
		Candidate: candidateIdentity, Publication: candidateIdentity,
		LifecycleSequence: sequence, ActiveBundleID: activeBundleID,
		Classification: clonePolicyResult(result), ApprovalState: PolicyApprovalNotRequired,
		AffectedResources: []PolicyAffectedResource{{InstanceID: candidate.InstanceID, AuthoredID: candidate.AuthoredID, ResourceKind: candidate.ResourceKind, Scope: PolicyAffectedResourceScope}},
		ChangedDimensions: policyChangedDimensions(result),
	}
	if baseline != nil {
		evidence.Baseline = policyIdentityFromPublication(*baseline)
	}
	if len(registry) == 1 {
		evidence.Version = RegistryPolicyEvidenceVersion
		evidence.RegistryTypes = cloneRegistryTypes(&registry[0])
	}
	if result.RequiresSecurityApproval {
		evidence.ApprovalState = PolicyApprovalRequired
	}
	digest, err := evidence.computeDigest()
	if err != nil {
		return PolicyEvidence{}, fmt.Errorf("%w: evidence digest: %v", ErrPolicyEvidenceInvalid, err)
	}
	evidence.EvidenceDigest = digest
	if err := evidence.Validate(); err != nil {
		return PolicyEvidence{}, err
	}
	return evidence, nil
}

func policyIdentityFromPublication(publication ContractPublication) PolicyPublicationIdentity {
	return PolicyPublicationIdentity{
		InstanceID: publication.InstanceID, AuthoredID: publication.AuthoredID,
		ResourceKind: publication.ResourceKind, Version: publication.Version,
		VersionBaseline: publication.VersionBaseline, ProjectionProfile: publication.ProjectionProfile,
		Digest: publication.Digest,
	}
}

// PolicyDecision validates and returns the server-owned policy read view.
// Historical v1 publication rows intentionally return an error here: their
// checks remain replayable history but cannot be treated as policy evidence.
func (p ContractPublication) PolicyDecision() (PolicyDecision, error) {
	if err := p.Validation.Validate(); err != nil {
		return PolicyDecision{}, err
	}
	if p.Validation.Version != 1 || p.Validation.PolicyEvidence == nil {
		return PolicyDecision{}, fmt.Errorf("%w: publication has no policy evidence", ErrPolicyEvidenceInvalid)
	}
	if err := validatePublicationContent(p); err != nil {
		return PolicyDecision{}, err
	}
	evidence := p.Validation.PolicyEvidence
	if !evidence.Publication.equal(policyIdentityFromPublication(p)) {
		return PolicyDecision{}, fmt.Errorf("%w: evidence is bound to a different publication", ErrPolicyEvidenceInvalid)
	}
	decision, err := evidence.Decision()
	if err != nil {
		return PolicyDecision{}, err
	}
	decision.Classification.Changes = slices.Clone(evidence.Classification.Changes)
	return decision, nil
}

func validatePublicationContent(publication ContractPublication) error {
	var digest string
	var err error
	switch publication.ResourceKind {
	case projectgraph.KindSource:
		digest, err = contractprojection.DigestSourcePublication(publication.CanonicalBytes)
	case projectgraph.KindModel:
		digest, err = contractprojection.DigestModelPublication(publication.CanonicalBytes)
	case projectgraph.KindSemanticModel:
		digest, err = contractprojection.DigestSemanticModelPublication(publication.CanonicalBytes)
	default:
		return fmt.Errorf("%w: unsupported publication kind", ErrPolicyEvidenceInvalid)
	}
	if err != nil || digest != publication.Digest {
		return fmt.Errorf("%w: publication canonical bytes/digest mismatch", ErrPolicyEvidenceInvalid)
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
	if err := json.Unmarshal(publication.CanonicalBytes, &envelope); err != nil {
		return fmt.Errorf("%w: publication envelope: %v", ErrPolicyEvidenceInvalid, err)
	}
	kind, err := publicationKind(envelope.Kind)
	if err != nil || envelope.Profile != publication.ProjectionProfile || kind != publication.ResourceKind || envelope.Metadata.ID != publication.AuthoredID.String() || envelope.Metadata.Contract.Version != publication.Version {
		return fmt.Errorf("%w: publication envelope identity mismatch", ErrPolicyEvidenceInvalid)
	}
	baseline, err := contractversion.SemverBaseline(envelope.Metadata.Contract.Version)
	if err != nil || strings.TrimPrefix(baseline, "v") != publication.VersionBaseline {
		return fmt.Errorf("%w: publication envelope baseline mismatch", ErrPolicyEvidenceInvalid)
	}
	return nil
}
