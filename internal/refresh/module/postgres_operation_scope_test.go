package module

import (
	"encoding/json"
	"strings"
	"testing"

	refreshoperation "github.com/flidai/leapview/internal/refresh/operation"
)

func TestRefreshOperationScopeIsBoundedAndSeparated(t *testing.T) {
	project := strings.Repeat("p", 255)
	environment := strings.Repeat("e", 128)
	scope := refreshOperationScope(project, environment)
	if len(scope) > 255 || scope == refreshOperationScope(project, environment+"x") || scope == refreshOperationScope(project+"x", environment) {
		t.Fatalf("scope is not bounded/separated: %q", scope)
	}
}

func TestOperationRunIDRequiresExactTerminalEvidence(t *testing.T) {
	valid := refreshoperation.Record{State: "completed", Outcome: json.RawMessage(`{"runId":"run-1"}`)}
	if got, ok := operationRunID(valid); !ok || got != "run-1" {
		t.Fatalf("valid operation evidence = %q, %v", got, ok)
	}
	for _, raw := range []string{`{"runId":"run-1","extra":true}`, `{"runId":"run-1","runId":"run-2"}`, `{"RunId":"run-1"}`} {
		if got, ok := operationRunID(refreshoperation.Record{State: "completed", Outcome: json.RawMessage(raw)}); ok {
			t.Fatalf("ambiguous operation evidence %q accepted as %q", raw, got)
		}
	}
}
