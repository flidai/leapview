package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWatermillConsumerAdmissionContract keeps the target decision aligned
// with the completed architecture audit. The adapter and Router are selected,
// but they must not acquire a production consumer or its operational burden
// until a capability owns a concrete bounded idempotent effect.
func TestWatermillConsumerAdmissionContract(t *testing.T) {
	root := repoRoot(t)
	documents := map[string][]string{
		"adr/0018-adopt-a-postgresql-centered-target-data-architecture.md": {
			"canonical PostgreSQL event log/delivery authority",
			"no real production consumer is admitted",
			"projections (including lineage, cache, audit, and product histories)",
			"No production event runtime is started",
			"restore exercises, and operator runbooks",
		},
		"adr/specifications/watermill-router-runtime.md": {
			"Production enrollment is conditional",
			"no real",
			"placeholder to justify enrollment",
			"qualification boundary only",
			"Those obligations become release gates only for the first explicitly admitted",
		},
		"adr/specifications/watermill-canonical-envelope.md": {
			"only for explicitly admitted consumers",
			"No real consumer is admitted",
			"Owner projections remain synchronous",
			"no Watermill SQL table",
		},
		"adr/specifications/watermill-postgresql-proof.md": {
			"custom PostgreSQL Subscriber adapter are selected",
			"No real consumer is admitted",
			"does not admit a production consumer or event runtime",
			"Watermill jobs",
		},
		"adr/specifications/fai-594-product-histories-and-canonical-events.md": {
			"Delivery rows are created only for an explicitly admitted consumer",
			"Owner projections remain synchronous",
			"no placeholder read model or export is admitted",
			"consumer currently exists",
		},
	}
	for relative, required := range documents {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		text := string(body)
		for _, fragment := range required {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s missing conditional Watermill admission contract %q", relative, fragment)
			}
		}
	}

	// No production composition may enroll a consumer or register a Router
	// handler. The event repository and adapter packages define those APIs, but
	// all current enrollment/runtime uses are qualification fixtures.
	for _, file := range productionGoFiles(t) {
		if file.path == "internal/platform/events/postgres/repository.go" {
			continue
		}
		if strings.Contains(file.body, ".EnrollConsumer(") {
			t.Errorf("%s enrolls a production Watermill consumer before admission", file.path)
		}
		if strings.Contains(file.body, "HandlerRegistration{") {
			t.Errorf("%s registers a production Watermill handler before admission", file.path)
		}
	}
}
