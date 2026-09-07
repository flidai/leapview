// Package contractversion classifies changes between already-canonical
// leapview.contract/v1 documents and enforces authored SemVer transitions.
// It never projects, serializes, or hashes contract values.
package contractversion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

type Class string

const (
	Compatible        Class = "compatible"
	Warning           Class = "warning"
	Breaking          Class = "breaking"
	SecuritySensitive Class = "security-sensitive"
)

// Compatibility is the compatibility dimension of a contract change. It is
// deliberately independent from Class, which is retained for the historical
// security-sensitive classification surface.
type Compatibility string

const (
	CompatibilityAdditive      Compatibility = "additive"
	CompatibilityBehavioral    Compatibility = "behavioral"
	CompatibilityBreaking      Compatibility = "breaking"
	CompatibilityIndeterminate Compatibility = "indeterminate"
)

type Domain string

const (
	DomainStructural Domain = "structural"
	DomainSemantic   Domain = "semantic"
	DomainSecurity   Domain = "security"
)

type Operation string

const (
	OperationAdded    Operation = "added"
	OperationRemoved  Operation = "removed"
	OperationModified Operation = "modified"
)

type SecurityImpact string

const (
	SecurityNone          SecurityImpact = "none"
	SecurityTightening    SecurityImpact = "tightening"
	SecurityWidening      SecurityImpact = "widening"
	SecurityMixed         SecurityImpact = "mixed"
	SecurityIndeterminate SecurityImpact = "indeterminate"
)

var (
	ErrInvalidContract      = errors.New("invalid canonical contract")
	ErrIdentityMismatch     = errors.New("contract identity mismatch")
	ErrVersionReuseConflict = errors.New("published contract version reused with different content")
	ErrVersionPolicy        = errors.New("contract version policy not satisfied")
	ErrInvalidResult        = errors.New("invalid contract classification result")
	ErrIndeterminate        = errors.New("contract classification is indeterminate")
)

type Change struct {
	Path           string         `json:"path"`
	Operation      Operation      `json:"operation"`
	Domain         Domain         `json:"domain"`
	Class          Class          `json:"class"`
	Compatibility  Compatibility  `json:"compatibility"`
	SecurityImpact SecurityImpact `json:"securityImpact,omitempty"`
	RequiresMajor  bool           `json:"requiresMajor,omitempty"`
	Reason         string         `json:"reason"`
}

type Result struct {
	Class                    Class          `json:"class"`
	Compatibility            Compatibility  `json:"compatibility"`
	StructuralCompatibility  Compatibility  `json:"structuralCompatibility"`
	SemanticCompatibility    Compatibility  `json:"semanticCompatibility"`
	SecurityImpact           SecurityImpact `json:"securityImpact"`
	RequiresMajor            bool           `json:"requiresMajor,omitempty"`
	RequiresSecurityApproval bool           `json:"requiresSecurityApproval,omitempty"`
	Changes                  []Change       `json:"changes"`
}

// Validate verifies every aggregate field against the per-change evidence.
// Callers must not be able to clear an approval or major-version requirement
// after classification by editing only the summary fields.
func (r Result) Validate() error {
	if !validClass(r.Class) {
		return fmt.Errorf("%w: invalid class %q", ErrInvalidResult, r.Class)
	}
	if !validCompatibility(r.Compatibility) || !validCompatibility(r.StructuralCompatibility) || !validCompatibility(r.SemanticCompatibility) {
		return fmt.Errorf("%w: compatibility dimensions are required", ErrInvalidResult)
	}
	if !validSecurityImpact(r.SecurityImpact) {
		return fmt.Errorf("%w: invalid security impact %q", ErrInvalidResult, r.SecurityImpact)
	}
	for index, change := range r.Changes {
		if index > 0 && r.Changes[index-1].Path >= change.Path {
			return fmt.Errorf("%w: changes must be strictly sorted by path", ErrInvalidResult)
		}
		if err := validateChange(change); err != nil {
			return fmt.Errorf("%w: change %d: %v", ErrInvalidResult, index, err)
		}
	}
	expected := aggregate(r.Changes)
	if r.Class != expected.Class || r.Compatibility != expected.Compatibility ||
		r.StructuralCompatibility != expected.StructuralCompatibility ||
		r.SemanticCompatibility != expected.SemanticCompatibility ||
		r.SecurityImpact != expected.SecurityImpact ||
		r.RequiresMajor != expected.RequiresMajor ||
		r.RequiresSecurityApproval != expected.RequiresSecurityApproval {
		return fmt.Errorf("%w: aggregate fields do not match changes", ErrInvalidResult)
	}
	return nil
}

// ValidatePublication applies the result integrity checks and rejects any
// compatibility or security state that cannot be safely admitted for
// publication. Direct Classify callers may inspect an indeterminate result;
// publication and version-transition callers must fail closed.
func (r Result) ValidatePublication() error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Compatibility == CompatibilityIndeterminate || r.SecurityImpact == SecurityIndeterminate || r.SecurityImpact == SecurityMixed {
		return fmt.Errorf("%w: publication requires determinate compatibility and security impact", ErrIndeterminate)
	}
	return nil
}

type document struct {
	Profile    string
	APIVersion string
	Kind       string
	AuthoredID string
	Version    string
	Value      map[string]any
}

type rawChange struct {
	Path      string
	Operation Operation
	Before    any
	After     any
}

// Classify compares exact canonical projection documents with one unified rule
// engine. The authored version is deliberately excluded from the content diff;
// ValidateVersionTransition evaluates it after classification.
func Classify(baseline, candidate []byte, registry ...SemanticRegistryTypes) (Result, error) {
	before, err := decodeDocument(baseline)
	if err != nil {
		return Result{}, err
	}
	after, err := decodeDocument(candidate)
	if err != nil {
		return Result{}, err
	}
	if before.Profile != after.Profile || before.APIVersion != after.APIVersion || before.Kind != after.Kind || before.AuthoredID != after.AuthoredID {
		return Result{}, fmt.Errorf("%w: baseline=%s/%s/%s/%s candidate=%s/%s/%s/%s", ErrIdentityMismatch,
			before.Profile, before.APIVersion, before.Kind, before.AuthoredID,
			after.Profile, after.APIVersion, after.Kind, after.AuthoredID)
	}
	if len(registry) > 1 {
		return Result{}, fmt.Errorf("%w: multiple registry contexts", ErrInvalidContract)
	}
	if len(registry) == 1 {
		if err := registry[0].Validate(); err != nil {
			return Result{}, err
		}
		if err := bindRegisteredTypes(&before, registry[0], false); err != nil {
			return Result{}, err
		}
		if err := bindRegisteredTypes(&after, registry[0], true); err != nil {
			return Result{}, err
		}
	}

	raw := make([]rawChange, 0)
	diffObject("", before.Value, after.Value, &raw)
	changes := make([]Change, 0, len(raw))
	for _, item := range raw {
		if item.Path == "metadata.contract.version" {
			continue
		}
		changes = append(changes, classifyChange(before, after, item))
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	result := aggregate(changes)
	if err := result.Validate(); err != nil {
		return Result{}, err
	}
	return result, nil
}

// ClassifyInitial classifies an explicitly initial publication against an
// empty contract envelope carrying the candidate identity. It is separate
// from Classify so callers cannot accidentally treat a missing baseline as a
// valid comparison.
func ClassifyInitial(candidate []byte, registry ...SemanticRegistryTypes) (Result, error) {
	after, err := decodeDocument(candidate)
	if err != nil {
		return Result{}, err
	}
	contract := emptyContract(after.Kind)
	metadata := map[string]any{}
	if authoredMetadata, ok := after.Value["metadata"].(map[string]any); ok {
		for key, value := range authoredMetadata {
			metadata[key] = value
		}
	}
	metadataContract := map[string]any{"version": after.Version, "compatibility": "backward"}
	if authoredContract, ok := metadata["contract"].(map[string]any); ok {
		metadataContract = map[string]any{}
		for key, value := range authoredContract {
			metadataContract[key] = value
		}
		metadataContract["version"] = after.Version
		metadataContract["compatibility"] = "backward"
	}
	metadata["contract"] = metadataContract
	baseline, err := json.Marshal(map[string]any{
		"profile":    after.Profile,
		"apiVersion": after.APIVersion,
		"kind":       after.Kind,
		"metadata":   metadata,
		"contract":   contract,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: initial baseline: %v", ErrInvalidContract, err)
	}
	result, err := Classify(baseline, candidate, registry...)
	if err != nil {
		return Result{}, err
	}
	if err := result.ValidatePublication(); err != nil {
		return Result{}, err
	}
	return result, nil
}

func emptyContract(kind string) map[string]any {
	switch kind {
	case "Source":
		return map[string]any{"schema": map[string]any{"fields": map[string]any{}}}
	case "Model":
		return map[string]any{"fields": map[string]any{}}
	case "SemanticModel":
		return map[string]any{
			"datasets": map[string]any{}, "accessGrants": map[string]any{},
			"relationships": map[string]any{}, "dimensions": map[string]any{},
			"filters": map[string]any{}, "metrics": map[string]any{},
		}
	default:
		return map[string]any{}
	}
}

// ValidateVersionTransition applies SemVer policy to the same unified result.
// Build metadata does not create a distinct baseline.
func ValidateVersionTransition(baseline, candidate []byte, registry ...SemanticRegistryTypes) (Result, error) {
	before, err := decodeDocument(baseline)
	if err != nil {
		return Result{}, err
	}
	after, err := decodeDocument(candidate)
	if err != nil {
		return Result{}, err
	}
	result, err := Classify(baseline, candidate, registry...)
	if err != nil {
		return Result{}, err
	}
	if err := result.ValidatePublication(); err != nil {
		if errors.Is(err, ErrIndeterminate) {
			return result, fmt.Errorf("%w: %w", ErrVersionPolicy, err)
		}
		return Result{}, err
	}
	baselineVersion, err := semverBaseline(before.Version)
	if err != nil {
		return Result{}, err
	}
	candidateVersion, err := semverBaseline(after.Version)
	if err != nil {
		return Result{}, err
	}
	if baselineVersion == candidateVersion {
		if !bytes.Equal(baseline, candidate) {
			return result, fmt.Errorf("%w: %s", ErrVersionReuseConflict, before.Version)
		}
		return result, nil
	}
	if semver.Compare(candidateVersion, baselineVersion) <= 0 {
		return result, fmt.Errorf("%w: candidate %s must be newer than baseline %s", ErrVersionPolicy, after.Version, before.Version)
	}
	if len(result.Changes) == 0 {
		return result, nil
	}
	if result.RequiresMajor {
		if semver.Major(candidateVersion) == semver.Major(baselineVersion) {
			return result, fmt.Errorf("%w: %s change requires a new major version", ErrVersionPolicy, result.Class)
		}
		return result, nil
	}
	if semver.Major(candidateVersion) == semver.Major(baselineVersion) && semver.MajorMinor(candidateVersion) == semver.MajorMinor(baselineVersion) {
		return result, fmt.Errorf("%w: %s change requires at least a new minor version", ErrVersionPolicy, result.Class)
	}
	return result, nil
}

// SemverBaseline returns the immutable publication key for an authored
// version. Build metadata is intentionally not a distinct baseline.
func SemverBaseline(version string) (string, error) {
	return semverBaseline(version)
}

func semverBaseline(version string) (string, error) {
	value := "v" + version
	if !semver.IsValid(value) {
		return "", fmt.Errorf("%w: invalid semantic version %q", ErrInvalidContract, version)
	}
	if index := strings.IndexByte(value, '+'); index >= 0 {
		value = value[:index]
	}
	return value, nil
}

func decodeDocument(encoded []byte) (document, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return document{}, fmt.Errorf("%w: %v", ErrInvalidContract, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return document{}, fmt.Errorf("%w: trailing JSON value", ErrInvalidContract)
	}
	profile, _ := value["profile"].(string)
	apiVersion, _ := value["apiVersion"].(string)
	kind, _ := value["kind"].(string)
	metadata, _ := value["metadata"].(map[string]any)
	authoredID, _ := metadata["id"].(string)
	contract, _ := metadata["contract"].(map[string]any)
	version, _ := contract["version"].(string)
	compatibility, _ := contract["compatibility"].(string)
	if profile != "leapview.contract/v1" || apiVersion != "leapview.dev/v1" || !validKind(kind) || authoredID == "" || compatibility != "backward" {
		return document{}, fmt.Errorf("%w: incomplete or unsupported envelope", ErrInvalidContract)
	}
	if _, err := semverBaseline(version); err != nil {
		return document{}, err
	}
	return document{Profile: profile, APIVersion: apiVersion, Kind: kind, AuthoredID: authoredID, Version: version, Value: value}, nil
}

func validKind(kind string) bool {
	return kind == "Source" || kind == "Model" || kind == "SemanticModel"
}

func diffObject(prefix string, before, after map[string]any, result *[]rawChange) {
	keys := make([]string, 0, len(before)+len(after))
	seen := map[string]struct{}{}
	for key := range before {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range after {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		left, leftOK := before[key]
		right, rightOK := after[key]
		switch {
		case !leftOK:
			if rightObject, ok := right.(map[string]any); ok && expandCollection(path) {
				diffObject(path, map[string]any{}, rightObject, result)
				continue
			}
			*result = append(*result, rawChange{Path: path, Operation: OperationAdded, After: right})
		case !rightOK:
			if leftObject, ok := left.(map[string]any); ok && expandCollection(path) {
				diffObject(path, leftObject, map[string]any{}, result)
				continue
			}
			*result = append(*result, rawChange{Path: path, Operation: OperationRemoved, Before: left})
		case reflect.DeepEqual(left, right):
			continue
		default:
			leftObject, leftIsObject := left.(map[string]any)
			rightObject, rightIsObject := right.(map[string]any)
			if leftIsObject && rightIsObject {
				diffObject(path, leftObject, rightObject, result)
				continue
			}
			*result = append(*result, rawChange{Path: path, Operation: OperationModified, Before: left, After: right})
		}
	}
}

func classifyChange(before, after document, change rawChange) Change {
	if securityPath(change.Path) {
		return classifySecurityChange(before, after, change)
	}

	domain := DomainStructural
	if semanticPath(after.Kind, change.Path) {
		domain = DomainSemantic
	}
	classified := Change{Path: change.Path, Operation: change.Operation, Domain: domain, SecurityImpact: SecurityNone}
	switch {
	case fieldRootChange(after.Kind, change.Path):
		if change.Operation == OperationAdded && nullableField(change.After) {
			classified.Class, classified.Compatibility, classified.Reason = Compatible, CompatibilityAdditive, "optional nullable field added"
		} else if change.Operation == OperationAdded {
			classified.Class, classified.Compatibility, classified.RequiresMajor, classified.Reason = Breaking, CompatibilityBreaking, true, "required field added"
		} else {
			classified.Class, classified.Compatibility, classified.RequiresMajor, classified.Reason = Breaking, CompatibilityBreaking, true, "published field removed"
		}
	case strings.HasSuffix(change.Path, ".datatype"):
		classified.Class, classified.Compatibility, classified.RequiresMajor, classified.Reason = Breaking, CompatibilityBreaking, true, "logical datatype changed"
	case strings.HasSuffix(change.Path, ".nullable"):
		if nullabilityStrengthened(change) {
			classified.Class, classified.Compatibility, classified.Reason = Compatible, CompatibilityAdditive, "field strengthened from nullable to non-null"
		} else {
			classified.Class, classified.Compatibility, classified.RequiresMajor, classified.Reason = Breaking, CompatibilityBreaking, true, "field may now return null"
		}
	case behavioralMetadataPath(change.Path):
		classified.Class, classified.Compatibility, classified.Reason = Warning, CompatibilityBehavioral, "behavioral or governance metadata changed"
	case semanticMemberRoot(change.Path):
		if change.Operation == OperationAdded {
			classified.Class, classified.Compatibility, classified.Reason = Compatible, CompatibilityAdditive, "semantic member added"
			if unprotectedSemanticMember(after.Kind, change.Path, change.After) {
				classified.Class = SecuritySensitive
				classified.SecurityImpact = SecurityWidening
				classified.Reason = "new unprotected semantic member widens discoverable access"
			}
		} else {
			classified.Class, classified.Compatibility, classified.RequiresMajor, classified.Reason = Breaking, CompatibilityBreaking, true, "published semantic member removed"
		}
	case domain == DomainSemantic:
		classified.Class, classified.Compatibility, classified.RequiresMajor, classified.Reason = Breaking, CompatibilityBreaking, true, "published semantic behavior changed"
	case change.Operation == OperationAdded:
		classified.Class, classified.Compatibility, classified.Reason = Compatible, CompatibilityAdditive, "contract member added"
	case change.Operation == OperationRemoved:
		classified.Class, classified.Compatibility, classified.RequiresMajor, classified.Reason = Breaking, CompatibilityBreaking, true, "contract member removed"
	default:
		classified.Class, classified.Compatibility, classified.RequiresMajor, classified.Reason = Breaking, CompatibilityBreaking, true, "structural contract changed"
	}
	return classified
}

func expandCollection(path string) bool {
	switch path {
	case "contract.fields", "contract.entities", "contract.schema.fields", "contract.datasets", "contract.accessGrants",
		"contract.relationships", "contract.dimensions", "contract.filters", "contract.metrics":
		return true
	default:
		return false
	}
}

func aggregate(changes []Change) Result {
	result := Result{
		Class:                   Compatible,
		Compatibility:           CompatibilityAdditive,
		StructuralCompatibility: CompatibilityAdditive,
		SemanticCompatibility:   CompatibilityAdditive,
		SecurityImpact:          SecurityNone,
		Changes:                 changes,
	}
	for _, change := range changes {
		if classRank(change.Class) > classRank(result.Class) {
			result.Class = change.Class
		}
		result.Compatibility = mergeCompatibility(result.Compatibility, change.Compatibility)
		switch change.Domain {
		case DomainStructural:
			result.StructuralCompatibility = mergeCompatibility(result.StructuralCompatibility, change.Compatibility)
		case DomainSemantic, DomainSecurity:
			result.SemanticCompatibility = mergeCompatibility(result.SemanticCompatibility, change.Compatibility)
		}
		result.RequiresMajor = result.RequiresMajor || change.RequiresMajor
		if change.SecurityImpact != SecurityNone {
			result.RequiresSecurityApproval = result.RequiresSecurityApproval || change.SecurityImpact == SecurityWidening || change.SecurityImpact == SecurityMixed || change.SecurityImpact == SecurityIndeterminate
			result.SecurityImpact = mergeSecurityImpact(result.SecurityImpact, change.SecurityImpact)
		}
	}
	return result
}

func classifySecurityChange(before, after document, change rawChange) Change {
	classified := Change{
		Path: change.Path, Operation: change.Operation, Domain: DomainSecurity,
		Class: SecuritySensitive, SecurityImpact: SecurityNone,
		Compatibility: CompatibilityBehavioral,
	}
	if unreferencedGrantChange(before, after, change) {
		if change.Operation == OperationRemoved {
			classified.Class = Warning
			classified.Compatibility = CompatibilityBehavioral
			classified.Reason = "unreferenced access grant removal is behavioral"
		} else {
			classified.Class = Compatible
			classified.Compatibility = CompatibilityAdditive
			classified.Reason = "unreferenced access grant does not change effective access"
		}
		return classified
	}
	if wholeAccessGrantChange(change.Path) {
		switch change.Operation {
		case OperationAdded:
			classified.Compatibility = CompatibilityBreaking
			classified.SecurityImpact = SecurityTightening
			classified.RequiresMajor = true
			classified.Reason = "referenced access grant addition tightens access"
		case OperationRemoved:
			classified.Compatibility = CompatibilityBehavioral
			classified.SecurityImpact = SecurityWidening
			classified.Reason = "referenced access grant removal widens access"
		default:
			classified.Compatibility = CompatibilityIndeterminate
			classified.SecurityImpact = SecurityIndeterminate
			classified.RequiresMajor = true
			classified.Reason = securityReason(SecurityIndeterminate)
		}
		return classified
	}

	classified.SecurityImpact = securityImpact(change)
	switch {
	case securityAttributeTransition(change.Path) || accessFilterFieldTransition(change):
		classified.Compatibility = CompatibilityBreaking
		classified.RequiresMajor = true
		classified.SecurityImpact = SecurityIndeterminate
		classified.Reason = "security attribute transition cannot be classified without registry evidence"
	case strings.Contains(change.Path, "allowedValues"):
		switch classified.SecurityImpact {
		case SecurityWidening:
			classified.Compatibility = CompatibilityBehavioral
			classified.Reason = securityReason(classified.SecurityImpact)
		case SecurityTightening:
			classified.Compatibility = CompatibilityBreaking
			classified.RequiresMajor = true
			classified.Reason = securityReason(classified.SecurityImpact)
		default:
			classified.Compatibility = CompatibilityIndeterminate
			classified.SecurityImpact = SecurityIndeterminate
			classified.RequiresMajor = true
			classified.Reason = securityReason(classified.SecurityImpact)
		}
	case strings.Contains(change.Path, "requiredAccessGrants") || strings.Contains(change.Path, "accessFilters"):
		switch classified.SecurityImpact {
		case SecurityTightening:
			classified.Compatibility = CompatibilityBreaking
			classified.RequiresMajor = true
		case SecurityWidening:
			classified.Compatibility = CompatibilityBehavioral
		default:
			classified.Compatibility = CompatibilityIndeterminate
			classified.SecurityImpact = SecurityIndeterminate
			classified.RequiresMajor = true
		}
		classified.Reason = securityReason(classified.SecurityImpact)
	default:
		classified.Compatibility = CompatibilityIndeterminate
		classified.SecurityImpact = SecurityIndeterminate
		classified.RequiresMajor = true
		classified.Reason = securityReason(classified.SecurityImpact)
	}
	return classified
}

func classRank(value Class) int {
	switch value {
	case Warning:
		return 1
	case Breaking:
		return 2
	case SecuritySensitive:
		return 3
	default:
		return 0
	}
}

func validClass(value Class) bool {
	switch value {
	case Compatible, Warning, Breaking, SecuritySensitive:
		return true
	default:
		return false
	}
}

func validCompatibility(value Compatibility) bool {
	switch value {
	case CompatibilityAdditive, CompatibilityBehavioral, CompatibilityBreaking, CompatibilityIndeterminate:
		return true
	default:
		return false
	}
}

func validSecurityImpact(value SecurityImpact) bool {
	switch value {
	case SecurityNone, SecurityTightening, SecurityWidening, SecurityMixed, SecurityIndeterminate:
		return true
	default:
		return false
	}
}

func validateChange(change Change) error {
	if strings.TrimSpace(change.Path) == "" {
		return errors.New("change path is required")
	}
	if change.Operation != OperationAdded && change.Operation != OperationRemoved && change.Operation != OperationModified {
		return fmt.Errorf("invalid operation %q", change.Operation)
	}
	if change.Domain != DomainStructural && change.Domain != DomainSemantic && change.Domain != DomainSecurity {
		return fmt.Errorf("invalid domain %q", change.Domain)
	}
	if !validClass(change.Class) {
		return fmt.Errorf("invalid class %q", change.Class)
	}
	if !validCompatibility(change.Compatibility) {
		return fmt.Errorf("invalid compatibility %q", change.Compatibility)
	}
	if !validSecurityImpact(change.SecurityImpact) {
		return fmt.Errorf("invalid security impact %q", change.SecurityImpact)
	}
	if change.RequiresMajor && change.Compatibility != CompatibilityBreaking && change.Compatibility != CompatibilityIndeterminate {
		return errors.New("major-version requirement has non-breaking compatibility")
	}
	if change.Compatibility == CompatibilityBreaking && !change.RequiresMajor {
		return errors.New("breaking compatibility requires a major-version requirement")
	}
	if strings.TrimSpace(change.Reason) == "" {
		return errors.New("change reason is required")
	}
	return nil
}

func compatibilityRank(value Compatibility) int {
	switch value {
	case CompatibilityBehavioral:
		return 1
	case CompatibilityBreaking:
		return 2
	case CompatibilityIndeterminate:
		return 3
	default:
		return 0
	}
}

func mergeCompatibility(left, right Compatibility) Compatibility {
	if compatibilityRank(right) > compatibilityRank(left) {
		return right
	}
	return left
}

func mergeSecurityImpact(left, right SecurityImpact) SecurityImpact {
	if right == SecurityNone {
		return left
	}
	if left == SecurityNone || left == right {
		return right
	}
	if left == SecurityIndeterminate || right == SecurityIndeterminate {
		return SecurityIndeterminate
	}
	return SecurityMixed
}

func securityPath(path string) bool {
	return strings.Contains(path, ".requiredAccessGrants") || strings.Contains(path, ".accessFilters") || strings.HasPrefix(path, "contract.accessGrants.") || strings.HasSuffix(path, ".classification")
}

func wholeAccessGrantChange(path string) bool {
	parts := strings.Split(path, ".")
	return len(parts) == 3 && parts[0] == "contract" && parts[1] == "accessGrants"
}

func securityImpact(change rawChange) SecurityImpact {
	switch {
	case securityAttributeTransition(change.Path) || accessFilterFieldTransition(change):
		return SecurityIndeterminate
	case strings.Contains(change.Path, "allowedValues"):
		return setImpact(change, SecurityWidening, SecurityTightening)
	case strings.Contains(change.Path, "requiredAccessGrants"), strings.Contains(change.Path, "accessFilters"):
		return setImpact(change, SecurityTightening, SecurityWidening)
	case strings.HasSuffix(change.Path, ".classification"):
		return SecurityIndeterminate
	case change.Operation == OperationAdded:
		return SecurityTightening
	case change.Operation == OperationRemoved:
		return SecurityWidening
	default:
		return SecurityMixed
	}
}

func securityAttributeTransition(path string) bool {
	if strings.HasPrefix(path, "contract.accessGrants.") && strings.HasSuffix(path, ".userAttribute") {
		return true
	}
	if strings.Contains(path, ".accessFilters.") && (strings.HasSuffix(path, ".field") || strings.HasSuffix(path, ".userAttribute")) {
		return true
	}
	return false
}

func accessFilterFieldTransition(change rawChange) bool {
	if !strings.Contains(change.Path, ".accessFilters") || change.Operation != OperationModified {
		return false
	}
	before, beforeOK := change.Before.([]any)
	after, afterOK := change.After.([]any)
	if !beforeOK || !afterOK || len(before) != len(after) {
		return false
	}
	for index := range before {
		beforeFilter, beforeOK := before[index].(map[string]any)
		afterFilter, afterOK := after[index].(map[string]any)
		if !beforeOK || !afterOK {
			return true
		}
		if !reflect.DeepEqual(beforeFilter["field"], afterFilter["field"]) || !reflect.DeepEqual(beforeFilter["userAttribute"], afterFilter["userAttribute"]) {
			return true
		}
	}
	return false
}

func unprotectedSemanticMember(kind, path string, value any) bool {
	if kind != "SemanticModel" || !semanticMemberRoot(path) || strings.HasPrefix(path, "contract.relationships.") {
		return false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return true
	}
	if grants, ok := object["requiredAccessGrants"]; ok && len(valueSet(grants)) > 0 {
		return false
	}
	if filters, ok := object["accessFilters"]; ok {
		if list, ok := filters.([]any); ok && len(list) > 0 {
			return false
		}
	}
	return true
}

func setImpact(change rawChange, added, removed SecurityImpact) SecurityImpact {
	before := valueSet(change.Before)
	after := valueSet(change.After)
	additions, removals := false, false
	for key := range after {
		if _, exists := before[key]; !exists {
			additions = true
		}
	}
	for key := range before {
		if _, exists := after[key]; !exists {
			removals = true
		}
	}
	if additions && removals {
		return SecurityMixed
	}
	if additions {
		return added
	}
	if removals {
		return removed
	}
	return SecurityMixed
}

func valueSet(value any) map[string]struct{} {
	result := map[string]struct{}{}
	values, _ := value.([]any)
	for _, item := range values {
		encoded, _ := json.Marshal(item)
		result[string(encoded)] = struct{}{}
	}
	return result
}

func unreferencedGrantChange(before, after document, change rawChange) bool {
	parts := strings.Split(change.Path, ".")
	if len(parts) != 3 || parts[0] != "contract" || parts[1] != "accessGrants" {
		return false
	}
	return !grantReferenced(before.Value, parts[2]) && !grantReferenced(after.Value, parts[2])
}

func grantReferenced(value any, grant string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "requiredAccessGrants" {
				for candidate := range valueSet(item) {
					if candidate == strconv.Quote(grant) {
						return true
					}
				}
			}
			if grantReferenced(item, grant) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if grantReferenced(item, grant) {
				return true
			}
		}
	}
	return false
}

func fieldRootChange(kind, path string) bool {
	parts := strings.Split(path, ".")
	if kind == "Source" {
		return len(parts) == 4 && parts[0] == "contract" && parts[1] == "schema" && parts[2] == "fields"
	}
	return kind == "Model" && len(parts) == 3 && parts[0] == "contract" && parts[1] == "fields"
}

func nullableField(value any) bool {
	object, _ := value.(map[string]any)
	nullable, _ := object["nullable"].(bool)
	return nullable
}

func booleanValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func nullabilityStrengthened(change rawChange) bool {
	if change.Operation == OperationAdded {
		return !booleanValue(change.After)
	}
	return change.Operation == OperationModified && booleanValue(change.Before) && !booleanValue(change.After)
}

func semanticPath(kind, path string) bool {
	return (kind == "SemanticModel" && strings.HasPrefix(path, "contract.")) || strings.HasPrefix(path, "contract.checks") || strings.HasPrefix(path, "contract.freshness")
}

func semanticMemberRoot(path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) != 3 || parts[0] != "contract" {
		return false
	}
	switch parts[1] {
	case "datasets", "relationships", "dimensions", "filters", "metrics":
		return true
	default:
		return false
	}
}

func behavioralMetadataPath(path string) bool {
	return strings.HasPrefix(path, "contract.freshness") || strings.HasPrefix(path, "contract.checks") || strings.Contains(path, ".authoritativeDefinitions") || strings.Contains(path, ".deprecation") || strings.HasSuffix(path, ".criticalDataElement")
}

func securityReason(impact SecurityImpact) string {
	switch impact {
	case SecurityTightening:
		return "access policy tightens and may deny an existing consumer"
	case SecurityWidening:
		return "access policy widens and requires explicit security approval"
	case SecurityIndeterminate:
		return "security effect cannot be classified from the contract alone"
	default:
		return "security effect is mixed and requires explicit review"
	}
}
