package servergo

import (
	"fmt"
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/ir"
)

// exportedName is the single naming policy used by every generated Go
// declaration. Keeping identifier normalization here prevents handlers,
// request models, and operation registries from drifting apart.
func exportedName(operationID string) string {
	parts := splitIdentifier(operationID)
	if len(parts) == 0 {
		return "Operation"
	}
	for i := range parts {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func splitIdentifier(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	replacer := strings.NewReplacer("-", " ", "_", " ", ".", " ", "/", " ")
	value = replacer.Replace(value)
	chunks := strings.Fields(value)
	if len(chunks) > 0 {
		return chunks
	}
	return []string{value}
}

func lowerCamelName(value string) string {
	parts := splitIdentifier(value)
	if len(parts) == 0 {
		return "value"
	}
	parts[0] = strings.ToLower(parts[0][:1]) + parts[0][1:]
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func packageName(opts Options) string {
	if strings.TrimSpace(opts.PackageName) == "" {
		return "api"
	}
	return opts.PackageName
}

// validateOperationIDsByName checks for exported handler name collisions.
func validateOperationIDsByName(doc ir.Document) error {
	seen := make(map[string]string, len(doc.Endpoints))
	for _, endpoint := range doc.Endpoints {
		exported := exportedName(endpoint.OperationID)
		if previous, exists := seen[exported]; exists {
			return fmt.Errorf("operation name collision %q for %q and %q", exported, previous, endpoint.OperationID)
		}
		seen[exported] = endpoint.OperationID
	}
	return nil
}
