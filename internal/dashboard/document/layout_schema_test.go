package document

import (
	"fmt"
	"strings"
	"testing"
)

// Agent-side structural validation must reject the same scalar layout bounds
// as the compiler, before a generated document reaches contextual compilation.
func TestDashboardDocumentSchemaLayoutBounds(t *testing.T) {
	schema := loadDashboardDocumentSchema(t)
	bounds := map[string]int{"columns": 1, "rowHeight": 1, "gap": 0, "padding": 0}
	for _, scope := range []string{"defaults", "override", "placement"} {
		fields := bounds
		if scope == "placement" {
			fields = map[string]int{"column": 1, "row": 1, "columnSpan": 1, "rowSpan": 1}
		}
		for field, minimum := range fields {
			for _, value := range []any{minimum - 1, minimum, nil} {
				t.Run(fmt.Sprintf("%s/%s/%v", scope, field, value), func(t *testing.T) {
					doc := loadDashboardDocumentFixture(t)
					spec := doc["spec"].(map[string]any)
					page := spec["pages"].([]any)[0].(map[string]any)
					target := map[string]any{"columns": 12, "rowHeight": 48, "gap": 16, "padding": 16}
					path := "/spec/layout/" + field
					switch scope {
					case "defaults":
						spec["layout"] = target
					case "override":
						page["layout"] = target
						path = "/spec/pages/0/layout/" + field
					case "placement":
						target = page["components"].([]any)[0].(map[string]any)["placement"].(map[string]any)
						path = "/spec/pages/0/components/0/placement/" + field
					}
					target[field] = value
					err := schema.Validate(doc)
					if value == minimum {
						if err != nil {
							t.Fatalf("valid minimum rejected: %v", err)
						}
						return
					}
					if err == nil || !strings.Contains(err.Error(), path) {
						t.Fatalf("want rejection at %s, got %v", path, err)
					}
				})
			}
		}
	}
	t.Run("omitted layout and empty override inherit", func(t *testing.T) {
		doc := loadDashboardDocumentFixture(t)
		spec := doc["spec"].(map[string]any)
		spec["pages"].([]any)[0].(map[string]any)["layout"] = map[string]any{}
		if err := schema.Validate(doc); err != nil {
			t.Fatalf("inherited defaults rejected: %v", err)
		}
	})
}
