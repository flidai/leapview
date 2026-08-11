package uicommand

import (
	"errors"
	"net/http/httptest"
	"testing"

	apigenaudit "github.com/Yacobolo/toolbelt/apigen/runtime/audit"
	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
)

type testOperationID string

func (id testOperationID) APIGenOperationID() string { return string(id) }

func TestBeginInvocationVerifiesClaimAndGeneratedContract(t *testing.T) {
	binding := Must("widget.create", testOperationID("createWidget"))
	contract := testContract("createWidget")
	request := httptest.NewRequest("POST", "/widgets", nil)
	request.Header.Set(HeaderOperationID, "createWidget")

	ctx, _, err := BeginInvocation(request, binding, contract, apigencommand.Invocation{IdempotencyKey: "request-1"})
	if err != nil {
		t.Fatalf("begin UI invocation: %v", err)
	}
	if operationID, ok := apigencommand.OperationID(ctx); !ok || operationID != "createWidget" {
		t.Fatalf("operation ID = %q/%v", operationID, ok)
	}
}

func TestBeginInvocationRejectsMissingMismatchedAndUnexposedClaims(t *testing.T) {
	binding := Must("widget.create", testOperationID("createWidget"))
	contract := testContract("createWidget")

	missing := httptest.NewRequest("POST", "/widgets", nil)
	if _, _, err := BeginInvocation(missing, binding, contract, apigencommand.Invocation{IdempotencyKey: "request-1"}); !errors.Is(err, ErrOperationMissing) {
		t.Fatalf("missing claim error = %v", err)
	}

	mismatch := httptest.NewRequest("POST", "/widgets", nil)
	mismatch.Header.Set(HeaderOperationID, "deleteWidget")
	if _, _, err := BeginInvocation(mismatch, binding, contract, apigencommand.Invocation{IdempotencyKey: "request-1"}); !errors.Is(err, ErrOperationMismatch) {
		t.Fatalf("mismatched claim error = %v", err)
	}

	unexposed := httptest.NewRequest("POST", "/widgets", nil)
	unexposed.Header.Set(HeaderOperationID, "createWidget")
	contract.AdditionalExposures = nil
	if _, _, err := BeginInvocation(unexposed, binding, contract, apigencommand.Invocation{IdempotencyKey: "request-1"}); !errors.Is(err, apigencommand.ErrSurfaceNotExposed) {
		t.Fatalf("unexposed command error = %v", err)
	}
}

func TestComposedClaimsStillVerifyEachIndividualCommand(t *testing.T) {
	request := httptest.NewRequest("POST", "/workflow", nil)
	request.Header.Set(HeaderOperationID, "createWidget,runWidget")
	claims := OperationClaims(request)
	workflow := []Binding{
		Must("widget.create", testOperationID("createWidget")),
		Must("widget.run", testOperationID("runWidget")),
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

func testContract(operationID string) apigencommand.Contract {
	return apigencommand.Contract{
		OperationID:         operationID,
		Owner:               "Test.Widgets",
		Method:              "POST",
		Path:                "/widgets",
		Idempotency:         apigencommand.IdempotencyRequired,
		AuthzMode:           "none",
		AdditionalExposures: []apigencommand.Surface{apigencommand.SurfaceUI},
		AuditAction:         "widget.created",
		Guarantee:           apigencommand.GuaranteeBestEffort,
		AuditPayload: &apigenaudit.Contract{
			Schema: "WidgetAuditPayload", SchemaVersion: 1, Retention: apigenaudit.RetentionSecurity,
			Fields: []apigenaudit.FieldContract{{Name: "widget", Sensitivity: apigenaudit.SensitivityInternal}},
		},
	}
}
