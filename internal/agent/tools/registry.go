package tools

import (
	"sort"

	agenttool "github.com/Yacobolo/toolbelt/apigen/runtime/agenttool"
)

const QueryVisualToolName = "query_visual"

type OperationContract struct {
	OperationID string
	Method      string
	Path        string
	Protected   bool
	AuthzMode   string
	Manual      bool
	Extensions  map[string]any
}

type APIGenOperation struct {
	Contract OperationContract
	Tool     agenttool.Contract
}

func BuildAPIGenOperations(operationContracts map[string]OperationContract, toolContracts map[string]agenttool.Contract) []APIGenOperation {
	operations := make([]APIGenOperation, 0, len(toolContracts))
	for _, tool := range toolContracts {
		contract, ok := operationContracts[tool.OperationID]
		if !ok || !operationAllowed(contract, tool) {
			continue
		}
		operations = append(operations, APIGenOperation{Contract: contract, Tool: tool})
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].Tool.Name < operations[j].Tool.Name
	})
	return operations
}

func APIGenToolNames(operations []APIGenOperation) []string {
	names := make([]string, 0, len(operations))
	for _, operation := range operations {
		names = append(names, operation.Tool.Name)
	}
	return names
}

func ManualToolNames() []string {
	return []string{
		AddDashboardPageToolName,
		AddDashboardVisualToolName,
		AssignDashboardFieldToolName,
		CatalogGetToolName,
		CatalogListToolName,
		CatalogSearchToolName,
		CreateDashboardDraftToolName,
		DocsReadToolName,
		DocsSearchToolName,
		EditDashboardSourceToolName,
		ExecuteDashboardCommandToolName,
		ExportDashboardYAMLToolName,
		ForkDashboardToolName,
		GetDashboardDraftToolName,
		GetDashboardToolName,
		ListDashboardsToolName,
		PreviewDashboardDraftToolName,
		QueryVisualToolName,
		ReadDashboardSourceToolName,
		SetDashboardVisibilityToolName,
	}
}

func ToolNames(operations []APIGenOperation) []string {
	names := append([]string{}, APIGenToolNames(operations)...)
	names = append(names, ManualToolNames()...)
	sort.Strings(names)
	return names
}

func IsKnownTool(operations []APIGenOperation, name string) bool {
	for _, tool := range ToolNames(operations) {
		if tool == name {
			return true
		}
	}
	return false
}

func operationAllowed(contract OperationContract, tool agenttool.Contract) bool {
	if tool.Effect != agenttool.EffectRead || contract.Manual {
		return false
	}
	if tool.Name != "query_semantic_model" && tool.Name != "query_dashboard_visual" {
		return false
	}
	if contract.Method != "GET" && contract.Method != "POST" {
		return false
	}
	switch operationPrivilege(contract) {
	case "RESOURCE_USE", "RESOURCE_READ", "RESOURCE_EDIT", "RESOURCE_MANAGE", "RESOURCE_PUBLISH":
		return true
	default:
		return false
	}
}

func operationPrivilege(contract OperationContract) string {
	raw, _ := contract.Extensions["x-authz"].(map[string]any)
	value, _ := raw["privilege"].(string)
	return value
}
