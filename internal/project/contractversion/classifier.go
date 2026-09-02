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
	SecurityNone       SecurityImpact = "none"
	SecurityTightening SecurityImpact = "tightening"
	SecurityWidening   SecurityImpact = "widening"
	SecurityMixed      SecurityImpact = "mixed"
)

var (
	ErrInvalidContract      = errors.New("invalid canonical contract")
	ErrIdentityMismatch     = errors.New("contract identity mismatch")
	ErrVersionReuseConflict = errors.New("published contract version reused with different content")
	ErrVersionPolicy        = errors.New("contract version policy not satisfied")
)

type Change struct {
	Path           string         `json:"path"`
	Operation      Operation      `json:"operation"`
	Domain         Domain         `json:"domain"`
	Class          Class          `json:"class"`
	SecurityImpact SecurityImpact `json:"securityImpact,omitempty"`
	RequiresMajor  bool           `json:"requiresMajor,omitempty"`
	Reason         string         `json:"reason"`
}

type Result struct {
	Class                    Class          `json:"class"`
	SecurityImpact           SecurityImpact `json:"securityImpact"`
	RequiresMajor            bool           `json:"requiresMajor,omitempty"`
	RequiresSecurityApproval bool           `json:"requiresSecurityApproval,omitempty"`
	Changes                  []Change       `json:"changes"`
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
func Classify(baseline, candidate []byte) (Result, error) {
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
	return aggregate(changes), nil
}

// ValidateVersionTransition applies SemVer policy to the same unified result.
// Build metadata does not create a distinct baseline.
func ValidateVersionTransition(baseline, candidate []byte) (Result, error) {
	before, err := decodeDocument(baseline)
	if err != nil {
		return Result{}, err
	}
	after, err := decodeDocument(candidate)
	if err != nil {
		return Result{}, err
	}
	result, err := Classify(baseline, candidate)
	if err != nil {
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
		if unreferencedGrantChange(before, after, change) {
			return Change{Path: change.Path, Operation: change.Operation, Domain: DomainSecurity, Class: Compatible, SecurityImpact: SecurityNone, Reason: "unreferenced access grant does not change effective access"}
		}
		impact := securityImpact(change)
		major := impact == SecurityTightening && (strings.Contains(change.Path, "requiredAccessGrants") || strings.Contains(change.Path, "accessFilters"))
		return Change{Path: change.Path, Operation: change.Operation, Domain: DomainSecurity, Class: SecuritySensitive, SecurityImpact: impact, RequiresMajor: major, Reason: securityReason(impact)}
	}

	domain := DomainStructural
	if semanticPath(after.Kind, change.Path) {
		domain = DomainSemantic
	}
	classified := Change{Path: change.Path, Operation: change.Operation, Domain: domain, SecurityImpact: SecurityNone}
	switch {
	case fieldRootChange(after.Kind, change.Path):
		if change.Operation == OperationAdded && nullableField(change.After) {
			classified.Class, classified.Reason = Compatible, "optional nullable field added"
		} else if change.Operation == OperationAdded {
			classified.Class, classified.RequiresMajor, classified.Reason = Breaking, true, "required field added"
		} else {
			classified.Class, classified.RequiresMajor, classified.Reason = Breaking, true, "published field removed"
		}
	case strings.HasSuffix(change.Path, ".datatype"):
		classified.Class, classified.RequiresMajor, classified.Reason = Breaking, true, "logical datatype changed"
	case strings.HasSuffix(change.Path, ".nullable"):
		if nullabilityStrengthened(change) {
			classified.Class, classified.Reason = Compatible, "field strengthened from nullable to non-null"
		} else {
			classified.Class, classified.RequiresMajor, classified.Reason = Breaking, true, "field may now return null"
		}
	case behavioralMetadataPath(change.Path):
		classified.Class, classified.Reason = Warning, "behavioral or governance metadata changed"
	case semanticMemberRoot(change.Path):
		if change.Operation == OperationAdded {
			classified.Class, classified.Reason = Compatible, "semantic member added"
		} else {
			classified.Class, classified.RequiresMajor, classified.Reason = Breaking, true, "published semantic member removed"
		}
	case domain == DomainSemantic:
		classified.Class, classified.RequiresMajor, classified.Reason = Breaking, true, "published semantic behavior changed"
	case change.Operation == OperationAdded:
		classified.Class, classified.Reason = Compatible, "contract member added"
	case change.Operation == OperationRemoved:
		classified.Class, classified.RequiresMajor, classified.Reason = Breaking, true, "contract member removed"
	default:
		classified.Class, classified.RequiresMajor, classified.Reason = Breaking, true, "structural contract changed"
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
	result := Result{Class: Compatible, SecurityImpact: SecurityNone, Changes: changes}
	for _, change := range changes {
		if classRank(change.Class) > classRank(result.Class) {
			result.Class = change.Class
		}
		result.RequiresMajor = result.RequiresMajor || change.RequiresMajor
		if change.Class == SecuritySensitive {
			result.RequiresSecurityApproval = result.RequiresSecurityApproval || change.SecurityImpact == SecurityWidening || change.SecurityImpact == SecurityMixed
			result.SecurityImpact = mergeSecurityImpact(result.SecurityImpact, change.SecurityImpact)
		}
	}
	return result
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

func mergeSecurityImpact(left, right SecurityImpact) SecurityImpact {
	if right == SecurityNone {
		return left
	}
	if left == SecurityNone || left == right {
		return right
	}
	return SecurityMixed
}

func securityPath(path string) bool {
	return strings.Contains(path, ".requiredAccessGrants") || strings.Contains(path, ".accessFilters") || strings.HasPrefix(path, "contract.accessGrants.") || strings.HasSuffix(path, ".classification")
}

func securityImpact(change rawChange) SecurityImpact {
	switch {
	case strings.Contains(change.Path, "allowedValues"):
		return setImpact(change, SecurityWidening, SecurityTightening)
	case strings.Contains(change.Path, "requiredAccessGrants"), strings.Contains(change.Path, "accessFilters"):
		return setImpact(change, SecurityTightening, SecurityWidening)
	case strings.HasSuffix(change.Path, ".classification"):
		return SecurityMixed
	case change.Operation == OperationAdded:
		return SecurityTightening
	case change.Operation == OperationRemoved:
		return SecurityWidening
	default:
		return SecurityMixed
	}
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
	default:
		return "security effect is mixed and requires explicit review"
	}
}
