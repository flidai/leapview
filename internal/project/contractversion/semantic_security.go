package contractversion

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

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
	case SecurityNone, SecurityTightening, SecurityWidening, SecurityIndeterminate:
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
	if change.SecurityImpact != SecurityNone {
		semanticWidening := change.Domain == DomainSemantic && change.Class == SecuritySensitive &&
			change.Operation == OperationAdded && semanticMemberRoot(change.Path)
		if change.Class != SecuritySensitive || change.Domain != DomainSecurity && !semanticWidening {
			return errors.New("security impact is inconsistent with change domain or class")
		}
	}
	if change.Class == SecuritySensitive && change.SecurityImpact == SecurityNone {
		return errors.New("security-sensitive change requires a security impact")
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
	return SecurityIndeterminate
}

func securityPath(path string) bool {
	return strings.Contains(path, ".requiredAccessGrants") || strings.Contains(path, ".accessFilters") || strings.HasPrefix(path, "contract.accessGrants.")
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
	case change.Operation == OperationAdded:
		return SecurityTightening
	case change.Operation == OperationRemoved:
		return SecurityWidening
	default:
		return SecurityIndeterminate
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
		return SecurityIndeterminate
	}
	if additions {
		return added
	}
	if removals {
		return removed
	}
	return SecurityIndeterminate
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
	return strings.HasPrefix(path, "contract.freshness") || strings.HasPrefix(path, "contract.checks") || strings.Contains(path, ".authoritativeDefinitions") || strings.Contains(path, ".deprecation") || strings.HasSuffix(path, ".criticalDataElement") || strings.HasSuffix(path, ".classification")
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
		return "security effect cannot be classified from the contract alone"
	}
}
