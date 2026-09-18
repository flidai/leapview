package personalsettings

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
)

// permissionOptionsSignal projects an already-authorized typed action-target pair into one
// picker option. The pair remains attached to the option so the browser cannot
// turn independent action and resource lists into a Cartesian product.
// Structurally invalid, non-selectable, or duplicate pairs are omitted.
func permissionOptionsSignal(permissionPairs []access.PermissionPair) []CapabilityOptionSignal {
	options := make([]CapabilityOptionSignal, 0, len(permissionPairs))
	seen := make(map[string]struct{}, len(permissionPairs))
	for _, pair := range permissionPairs {
		if err := pair.Validate(); err != nil {
			continue
		}
		definition, ok := access.Permission(pair.Action)
		if !ok || !definition.UISelectable {
			continue
		}
		key := permissionPairOptionKey(pair)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		permissions := []PermissionPairSignal{permissionPairSignal(pair)}
		target := permissionTargetOptionLabel(pair.Target)
		options = append(options, CapabilityOptionSignal{
			Value:       key,
			Label:       fmt.Sprintf("%s · %s", pair.Action, target),
			Description: fmt.Sprintf("%s This exact action-target pair is the only authority persisted for this selection.", definition.Description),
			Category:    definition.Family,
			Permissions: &permissions,
		})
	}
	sort.SliceStable(options, func(i, j int) bool { return options[i].Value < options[j].Value })
	return options
}

func permissionPairOptionKey(pair access.PermissionPair) string {
	target := pair.Target
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%t|%s", pair.Action, target.Scope, target.InstanceID, target.ProjectID, target.ResourceKind, target.ResourceID, target.IncludeFuture, pair.Profile)
}

func permissionTargetOptionLabel(target access.PermissionTarget) string {
	switch target.Scope {
	case access.PermissionScopeInstance:
		return fmt.Sprintf("Instance %s", target.InstanceID)
	case access.PermissionScopeProject:
		if target.IncludeFuture {
			return fmt.Sprintf("Project %s · future %s resources", target.ProjectID, target.ResourceKind)
		}
		return fmt.Sprintf("Project %s", target.ProjectID)
	case access.PermissionScopeResource:
		return fmt.Sprintf("%s %s", target.ResourceKind, target.ResourceID)
	default:
		return string(target.Scope)
	}
}
