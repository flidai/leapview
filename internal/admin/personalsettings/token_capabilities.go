package personalsettings

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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
		label, description, category := permissionOptionPresentation(definition, pair)
		options = append(options, CapabilityOptionSignal{
			Value:       key,
			Label:       label,
			Description: description,
			Category:    category,
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

func permissionOptionPresentation(definition access.PermissionDefinition, pair access.PermissionPair) (string, string, string) {
	actionLabel := permissionActionLabel(pair.Action)
	switch pair.Target.Scope {
	case access.PermissionScopeResource:
		kind := permissionResourceKindLabel(pair.Target.ResourceKind)
		return permissionResourceName(pair.Target.ResourceID, kind), kind + " · " + actionLabel, permissionResourceKindCategory(pair.Target.ResourceKind)
	case access.PermissionScopeProject:
		if pair.Target.IncludeFuture {
			return actionLabel, "Current and future " + strings.ToLower(permissionResourceKindCategory(pair.Target.ResourceKind)), permissionResourceKindCategory(pair.Target.ResourceKind)
		}
		if len(definition.ResourceKinds) == 1 && definition.ResourceKinds[0] != projectgraph.KindProjectNamespace {
			return actionLabel, "Current project", permissionResourceKindCategory(definition.ResourceKinds[0])
		}
		return actionLabel, "Current project", definition.Family
	case access.PermissionScopeInstance:
		return actionLabel, "This instance", definition.Family
	default:
		return actionLabel, definition.Description, definition.Family
	}
}

func permissionResourceName(resourceID projectgraph.ResourceID, fallback string) string {
	value := string(resourceID)
	if separator := strings.LastIndexAny(value, ":/"); separator >= 0 {
		value = value[separator+1:]
	}
	value = strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(value)), " ")
	if value == "" {
		return fallback
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func permissionResourceKindLabel(kind projectgraph.Kind) string {
	switch kind {
	case projectgraph.KindSemanticModel:
		return "Semantic model"
	case projectgraph.KindConnection:
		return "Connection"
	case projectgraph.KindSource:
		return "Source"
	case projectgraph.KindModel:
		return "Model"
	case projectgraph.KindPipeline:
		return "Pipeline"
	case projectgraph.KindDashboard:
		return "Dashboard"
	default:
		return "Resource"
	}
}

func permissionResourceKindCategory(kind projectgraph.Kind) string {
	if kind == projectgraph.KindSemanticModel {
		return "Semantic models"
	}
	return permissionResourceKindLabel(kind) + "s"
}

var permissionActionLabels = map[access.Action]string{
	access.ActionDashboardRead:          "View dashboard",
	access.ActionDashboardCreate:        "Create dashboards",
	access.ActionDashboardUpdate:        "Edit dashboard",
	access.ActionDashboardDelete:        "Delete dashboard",
	access.ActionDashboardPublish:       "Publish dashboard",
	access.ActionSemanticRead:           "Discover metadata",
	access.ActionSemanticQuery:          "Build queries",
	access.ActionSemanticConsume:        "Use governed data",
	access.ActionSemanticCreate:         "Create semantic models",
	access.ActionSemanticUpdate:         "Edit semantic model",
	access.ActionSemanticDelete:         "Delete semantic model",
	access.ActionSourceRead:             "View source",
	access.ActionSourceCreate:           "Create sources",
	access.ActionSourceUpdate:           "Edit source",
	access.ActionSourceDelete:           "Delete source",
	access.ActionModelRead:              "View model",
	access.ActionModelCreate:            "Create models",
	access.ActionModelUpdate:            "Edit model",
	access.ActionModelDelete:            "Delete model",
	access.ActionPipelineRead:           "View pipeline",
	access.ActionPipelineCreate:         "Create pipelines",
	access.ActionPipelineRun:            "Run pipeline",
	access.ActionPipelineUpdate:         "Edit pipeline",
	access.ActionPipelineDelete:         "Delete pipeline",
	access.ActionConnectionRead:         "View connection details",
	access.ActionConnectionCreate:       "Create connections",
	access.ActionConnectionUse:          "Use connection",
	access.ActionConnectionManage:       "Manage connection",
	access.ActionResourceShare:          "Share resource",
	access.ActionDeliveryRead:           "View releases",
	access.ActionDeliveryPlan:           "Plan releases",
	access.ActionDeliveryBuild:          "Build releases",
	access.ActionDeliveryPublish:        "Publish releases",
	access.ActionDeliveryApprove:        "Approve releases",
	access.ActionDeliveryActivate:       "Activate releases",
	access.ActionDeliveryRollback:       "Roll back releases",
	access.ActionProjectSettingsRead:    "View project settings",
	access.ActionProjectSettingsUpdate:  "Update project settings",
	access.ActionProjectAccessRead:      "View project access",
	access.ActionProjectAccessManage:    "Manage project access",
	access.ActionProjectAccessDelegate:  "Delegate permissions",
	access.ActionAuditRead:              "View audit log",
	access.ActionWorkloadDelegate:       "Delegate workload",
	access.ActionPlatformSettingsRead:   "View platform settings",
	access.ActionPlatformSettingsUpdate: "Update platform settings",
	access.ActionPlatformAccessRead:     "View platform access",
	access.ActionPlatformAccessManage:   "Manage platform access",
	access.ActionPlatformAuditRead:      "View platform audit log",
}

func permissionActionLabel(action access.Action) string {
	if label := permissionActionLabels[action]; label != "" {
		return label
	}
	return "Use permission"
}
