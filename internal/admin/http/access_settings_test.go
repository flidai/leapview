package http

import (
	"net/http/httptest"
	"testing"

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
