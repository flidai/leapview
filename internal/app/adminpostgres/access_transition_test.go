package adminpostgres

import (
	"bytes"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	admincli "github.com/flidai/leapview/internal/admin/cli"
)

func TestApplyAccessTransitionRequiresHostMaintenanceVerifier(t *testing.T) {
	request := admincli.StageAccessTransitionRequest{
		Intent: admincli.AccessTransitionIntent{
			TargetID: "target_demo", Environment: "production",
			ProjectID: "project_demo", ExpectedPolicyRevision: 32, ExpectedPolicyDigest: "sha256:" + strings.Repeat("b", 64),
			ExpectedServingGeneration: "generation_legacy", ExpectedServingPolicyDigest: "sha256:" + strings.Repeat("c", 64),
			PublisherPrincipalID: "principal_release", ReviewerPrincipalID: "principal_reviewer",
			RoleBindings: []admincli.AccessTransitionRoleIntent{{BindingID: "viewer", Principal: "principal_cfo", Role: string(access.PermissionRoleViewer)}},
		},
		MaintenanceOperationID:     "upgrade-42",
		MaintenanceOperationDigest: "sha256:" + strings.Repeat("a", 64),
		OperationID:                "access-transition-42",
		Apply:                      true,
	}
	var out bytes.Buffer
	ops := Operations{Dependencies: Dependencies{}}
	if callErr := ops.StageAccessTransition(t.Context(), request, &out); callErr == nil || !strings.Contains(callErr.Error(), "maintenance access-transition verifier") {
		t.Fatalf("Apply without host admission verifier = %v", callErr)
	}
	if out.Len() != 0 {
		t.Fatal("unverified transition wrote output")
	}
}
