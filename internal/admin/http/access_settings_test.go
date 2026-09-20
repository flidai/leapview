package http

import (
	"net/http/httptest"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	adminsettings "github.com/flidai/leapview/internal/admin/settings"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func TestBeginAccessSettingsInvocationUsesGeneratedCommandIdentity(t *testing.T) {
	request := httptest.NewRequest("POST", "/admin/access/command?section=access", nil)
	request.Header.Set(uicommand.HeaderOperationID, accessgen.GenCommandOperationCreateProjectRoleBinding().APIGenOperationID())
	request.Header.Set("Idempotency-Key", "idem-access-1")
	request.Header.Set("X-Request-ID", "request-access-1")

	started, operation, err := beginAccessSettingsInvocation(request, "project-active", adminsettings.AccessSettingsCommand{Action: "create"})
	if err != nil {
		t.Fatal(err)
	}
	if operation != accessgen.GenCommandOperationCreateProjectRoleBinding().APIGenOperationID() || started == request {
		t.Fatalf("operation/request = %q/%p, want generated create operation and derived request", operation, started)
	}
	request.Header.Del("Idempotency-Key")
	if _, _, err := beginAccessSettingsInvocation(request, "project-active", adminsettings.AccessSettingsCommand{Action: "create"}); err == nil {
		t.Fatal("missing idempotency key was accepted")
	}

	request.Header.Set("Idempotency-Key", "idem-access-1")
	request.Header.Set(uicommand.HeaderOperationID, accessgen.GenCommandOperationDeleteProjectRoleBinding().APIGenOperationID())
	if _, _, err := beginAccessSettingsInvocation(request, "project-active", adminsettings.AccessSettingsCommand{Action: "create"}); err == nil {
		t.Fatal("mismatched generated operation claim was accepted")
	}
}

func TestBeginAccessAdministrationInvocationUsesDedicatedCredentialRevocationCommand(t *testing.T) {
	request := httptest.NewRequest("POST", "/admin/access/command?section=principals", nil)
	request.Header.Set(uicommand.HeaderOperationID, accessgen.GenUIActionRevokeAllPrincipalCredentials().OperationID())
	request.Header.Set("X-Request-ID", "credential-revoke-ui-request")

	started, err := beginAccessAdministrationInvocation(request, adminsettings.AccessAdministrationCommand{
		Action: "revoke_all_credentials", PrincipalID: "principal-target",
	})
	if err != nil {
		t.Fatalf("begin credential revocation invocation: %v", err)
	}
	if started == request {
		t.Fatal("credential revocation invocation did not derive a guarded request")
	}
	if got, want := uicommand.OperationClaims(started), []string{accessgen.GenUIActionRevokeAllPrincipalCredentials().OperationID()}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("operation claims = %#v, want %#v", got, want)
	}
	if operationID, ok := apigencommand.OperationID(started.Context()); !ok || operationID != accessgen.GenCommandOperationRevokeAllPrincipalCredentials().APIGenOperationID() {
		t.Fatalf("generated command context operation = %q, %t", operationID, ok)
	}

	request.Header.Set(uicommand.HeaderOperationID, accessgen.GenUIActionRevokePrincipalSession().OperationID())
	if _, err := beginAccessAdministrationInvocation(request, adminsettings.AccessAdministrationCommand{Action: "revoke_all_credentials", PrincipalID: "principal-target"}); err == nil {
		t.Fatal("session-only UI operation claim was accepted for credential revocation")
	}
}
