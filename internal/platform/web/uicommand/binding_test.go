package uicommand

import (
	"errors"
	"net/http/httptest"
	"testing"

	apigenui "github.com/Yacobolo/toolbelt/apigen/runtime/ui"
)

func TestComposedClaimsStillVerifyEachIndividualCommand(t *testing.T) {
	request := httptest.NewRequest("POST", "/workflow", nil)
	request.Header.Set(HeaderOperationID, "createWidget,runWidget")
	claims := OperationClaims(request)
	workflow := []Binding{
		apigenui.MustAction("widget.create", "createWidget"),
		apigenui.MustAction("widget.run", "runWidget"),
	}
	if err := VerifyWorkflowClaims(claims, workflow); err != nil {
		t.Fatalf("verify workflow: %v", err)
	}
	if err := VerifyClaim(claims, "runWidget"); !errors.Is(err, ErrOperationMismatch) {
		t.Fatalf("ordinary command accepted a composed claim: %v", err)
	}
	request.Header.Set(HeaderOperationID, "createWidget,deleteWidget,runWidget")
	if err := VerifyWorkflowClaims(OperationClaims(request), workflow); !errors.Is(err, ErrOperationMismatch) {
		t.Fatalf("workflow accepted an extra operation: %v", err)
	}
}
