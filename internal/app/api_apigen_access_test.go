package app

import (
	"testing"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
)

func TestAPIGenAccessCapabilityOwnsItsOperationSurface(t *testing.T) {
	accessContracts := accessgen.GetAPIGenOperationContracts()
	if got, want := len(accessContracts), 82; got != want {
		t.Fatalf("Access generated operations = %d, want %d", got, want)
	}
	allowedTags := map[string]bool{"Access": true, "Audit": true, "Current User": true}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	for operationID, contract := range accessContracts {
		if len(contract.Tags) != 1 || !allowedTags[contract.Tags[0]] {
			t.Errorf("Access operation %q tags = %v", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("Access operation %q is still emitted by the application package", operationID)
		}
	}
	if _, exists := accessContracts["listQueryEvents"]; exists {
		t.Fatal("Analytics-owned listQueryEvents is emitted by the Access package")
	}
	if _, exists := appContracts["listQueryEvents"]; exists {
		t.Fatal("Analytics-owned listQueryEvents is still emitted by the application package")
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}
