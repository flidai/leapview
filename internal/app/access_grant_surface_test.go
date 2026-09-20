package app

import (
	"testing"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

func TestUnsupportedProjectGrantCRUDIsNotGenerated(t *testing.T) {
	contracts := accessgen.GetAPIGenOperationContracts()
	for _, operationID := range []string{"listGrants", "createGrant", "getGrant", "updateGrant", "deleteGrant"} {
		if _, exists := contracts[operationID]; exists {
			t.Errorf("unsupported project grant operation %q is still generated", operationID)
		}
	}
}
