package connectionbinding

import (
	"errors"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestNewTargetBindingRejectsConnectorAndCredentialAliases(t *testing.T) {
	base := TargetBindingInput{
		ID: "binding_prod_warehouse", TargetID: "target_prod", ConnectionID: projectgraph.ResourceID("connection_warehouse"),
		ConnectorKind: "duckdb", AuthenticationMode: AuthenticationExternalBundle,
		Scope: BindingScope{ProjectID: projectgraph.ResourceID("project_sales"), Environment: "prod"},
		CredentialReference: CredentialReference{
			ProjectID: projectgraph.ResourceID("vault_project"), Environment: "prod", SecretPath: "/leapview/sales", SecretKey: "warehouse",
		},
		Enabled: true, Now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
	}
	for name, mutate := range map[string]func(*TargetBindingInput){
		"connector":   func(input *TargetBindingInput) { input.ConnectorKind = " duckdb" },
		"secret path": func(input *TargetBindingInput) { input.CredentialReference.SecretPath = " /leapview/sales" },
		"secret key":  func(input *TargetBindingInput) { input.CredentialReference.SecretKey = "warehouse " },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			mutate(&input)
			if _, err := NewTargetBinding(input); !errors.Is(err, ErrInvalidBinding) {
				t.Fatalf("NewTargetBinding() error = %v, want invalid binding", err)
			}
		})
	}
}
