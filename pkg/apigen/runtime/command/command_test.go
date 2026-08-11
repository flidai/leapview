package command

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	apigenaudit "github.com/Yacobolo/toolbelt/apigen/runtime/audit"
)

func testLookup(guarantee Guarantee) Lookup {
	return func(operationID string) (Contract, bool) {
		if operationID != "createWidget" {
			return Contract{}, false
		}
		return Contract{OperationID: operationID, Owner: "Widgets", Method: "POST", Path: "/widgets", Idempotency: IdempotencyRequired, AuthzMode: "privilege", Privilege: "CREATE_WIDGET", AuditAction: "widget.created", Guarantee: guarantee, AuditPayload: testAuditPayload()}, true
	}
}

func testAuditPayload() *apigenaudit.Contract {
	return &apigenaudit.Contract{
		Schema: "WidgetAuditPayload", SchemaVersion: 1, Retention: apigenaudit.RetentionSecurity,
		Fields: []apigenaudit.FieldContract{{Name: "operationId", Sensitivity: apigenaudit.SensitivityInternal}},
	}
}

func TestExecutorSelectsBestEffortFromGeneratedContract(t *testing.T) {
	var logs bytes.Buffer
	executor, err := NewExecutor(testLookup(GuaranteeBestEffort), slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	contract, _ := testLookup(GuaranteeBestEffort)("createWidget")
	ctx, guard, err := Begin(t.Context(), contract)
	if err != nil {
		t.Fatal(err)
	}
	transactionalCalled := false
	err = executor.Execute(ctx, "createWidget", Execution{
		BestEffortAudit: func(context.Context, Contract) error { return errors.New("sink unavailable") },
		Transactional: func(context.Context, Contract) error {
			transactionalCalled = true
			return nil
		},
		LogAttributes: []slog.Attr{slog.String("target_id", "w-1")},
	})
	if err != nil {
		t.Fatalf("best-effort failure changed command result: %v", err)
	}
	if transactionalCalled || !guard.Completed() {
		t.Fatalf("transactionalCalled=%v completed=%v", transactionalCalled, guard.Completed())
	}
	for _, value := range []string{"best-effort command audit failed", "createWidget", "widget.created", "target_id=w-1", "sink unavailable"} {
		if !strings.Contains(logs.String(), value) {
			t.Fatalf("log %q does not contain %q", logs.String(), value)
		}
	}
}

func TestExecutorSelectsTransactionalFromGeneratedContract(t *testing.T) {
	executor, err := NewExecutor(testLookup(GuaranteeTransactional), nil)
	if err != nil {
		t.Fatal(err)
	}
	contract, _ := testLookup(GuaranteeTransactional)("createWidget")
	ctx, guard, err := Begin(t.Context(), contract)
	if err != nil {
		t.Fatal(err)
	}
	bestEffortCalled := false
	err = executor.Execute(ctx, "", Execution{
		BestEffortAudit: func(context.Context, Contract) error { bestEffortCalled = true; return nil },
		Transactional: func(_ context.Context, received Contract) error {
			if received.AuditAction != "widget.created" {
				t.Fatalf("contract = %#v", received)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bestEffortCalled || !guard.Completed() {
		t.Fatalf("bestEffortCalled=%v completed=%v", bestEffortCalled, guard.Completed())
	}
}

func TestExecutorRejectsMissingGuaranteeCapability(t *testing.T) {
	executor, err := NewExecutor(testLookup(GuaranteeTransactional), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = executor.Execute(t.Context(), "createWidget", Execution{BestEffortAudit: func(context.Context, Contract) error { return nil }})
	if !errors.Is(err, ErrExecutionUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestContractValidatesAsyncExecution(t *testing.T) {
	contract := Contract{
		OperationID: "finalizeRelease", Owner: "ReleaseAPI", Method: "POST", Path: "/releases/{release}/finalize", Idempotency: IdempotencyRequired, AuthzMode: "privilege", Privilege: "PUBLISH_RELEASE", AuditAction: "release.validating", Guarantee: GuaranteeTransactional,
		AuditPayload: testAuditPayload(),
		Execution:    &AsyncExecutionContract{Mode: "async", Guarantee: "transactional", JobKind: "release.finalize", ResourceKind: "release", InitialEvent: "release.validating", InitialState: "validating", StatusOperation: "getRelease", EventsOperation: "listReleaseEvents", Cancellation: "unsupported"},
	}
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
	contract.Execution.InitialEvent = "release.started"
	contract.Guarantee = GuaranteeBestEffort
	if err := contract.Validate(); err != nil {
		t.Fatalf("error = %v", err)
	}
	contract.Execution.Guarantee = "best-effort"
	if err := contract.Validate(); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("error = %v", err)
	}
}

func TestContractValidatesAuditPayload(t *testing.T) {
	contract := Contract{
		OperationID: "createWidget", Owner: "Widgets", Method: "POST", Path: "/widgets", Idempotency: IdempotencyRequired, AuthzMode: "authenticated", AuditAction: "widget.created", Guarantee: GuaranteeTransactional,
		AuditPayload: &apigenaudit.Contract{
			Schema: "WidgetCreatedAuditPayload", SchemaVersion: 1, Retention: apigenaudit.RetentionSecurity,
			Fields: []apigenaudit.FieldContract{{Name: "widgetId", Sensitivity: apigenaudit.SensitivityInternal}},
		},
	}
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
	contract.AuditPayload.Fields[0].Sensitivity = "unknown"
	if err := contract.Validate(); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("error = %v", err)
	}
}

func TestGeneratedPolicyDrivesDependenciesAndSurfaces(t *testing.T) {
	contract, _ := testLookup(GuaranteeTransactional)("createWidget")
	contract.AdditionalExposures = []Surface{SurfaceUI}
	if err := contract.Validate(); err != nil {
		t.Fatal(err)
	}
	if !contract.Exposes(SurfaceAPI) || !contract.Exposes(SurfaceCLI) || !contract.Exposes(SurfaceUI) || contract.Exposes(SurfaceAgent) {
		t.Fatalf("unexpected surface policy: %#v", contract.AdditionalExposures)
	}
	want := []Dependency{DependencyAuthorization, DependencyIdempotency, DependencyAudit}
	if got := contract.Dependencies(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("dependencies = %#v, want %#v", got, want)
	}
}

func TestExecutorAppliesGeneratedIfMatchPolicy(t *testing.T) {
	lookup := func(operationID string) (Contract, bool) {
		contract, ok := testLookup(GuaranteeTransactional)("createWidget")
		contract.OperationID = operationID
		contract.Method = "PATCH"
		contract.Idempotency = IdempotencyNone
		contract.Concurrency = ConcurrencyIfMatch
		return contract, ok
	}
	executor, err := NewExecutor(lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	contract, _ := lookup("updateWidget")
	ctx, guard, err := Begin(t.Context(), contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.CheckConcurrency(ctx, "updateWidget", `"old", "current"`, `"current"`); err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(ctx, "updateWidget", Execution{Transactional: func(context.Context, Contract) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if !guard.Completed() {
		t.Fatal("guard did not record concurrency and audit completion")
	}
	if err := executor.CheckConcurrency(t.Context(), "updateWidget", `W/"current"`, `"current"`); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("weak match error = %v", err)
	}
	if err := executor.CheckConcurrency(t.Context(), "updateWidget", "", `"current"`); !errors.Is(err, ErrPreconditionRequired) {
		t.Fatalf("missing match error = %v", err)
	}
}

func TestMatchPathUsesGeneratedRouteTemplate(t *testing.T) {
	if !MatchPath("/api/v1/workspaces/{workspace}/role-bindings", "/api/v1/workspaces/sales/role-bindings") {
		t.Fatal("generated route did not match")
	}
	if MatchPath("/api/v1/workspaces/{workspace}/role-bindings", "/api/v1/workspaces/sales/role-bindings/extra") {
		t.Fatal("route with extra segment matched")
	}
}

func TestBeginInvocationRejectsMissingGeneratedPolicyInputs(t *testing.T) {
	contract, _ := testLookup(GuaranteeTransactional)("createWidget")
	contract.Target = &TargetContract{Parameter: "workspace", Type: "workspace"}
	contract.AdditionalExposures = []Surface{SurfaceUI}
	for _, tc := range []struct {
		name       string
		invocation Invocation
		want       error
	}{
		{name: "surface", invocation: Invocation{Surface: SurfaceAgent}, want: ErrSurfaceNotExposed},
		{name: "operation", invocation: Invocation{OperationID: "deleteWidget", Surface: SurfaceUI}, want: ErrOperationMismatch},
		{name: "target", invocation: Invocation{Surface: SurfaceUI, IdempotencyKey: "key"}, want: ErrTargetRequired},
		{name: "idempotency", invocation: Invocation{Surface: SurfaceUI, TargetValues: map[string]string{"workspace": "sales"}}, want: ErrIdempotencyRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := BeginInvocation(t.Context(), contract, tc.invocation)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	ctx, guard, err := BeginInvocation(t.Context(), contract, Invocation{
		Surface: SurfaceUI, TargetValues: map[string]string{"workspace": "sales"}, IdempotencyKey: "key",
	})
	if err != nil || guard == nil {
		t.Fatalf("begin invocation: guard=%#v err=%v", guard, err)
	}
	if operationID, ok := OperationID(ctx); !ok || operationID != contract.OperationID {
		t.Fatalf("operation = %q, %t", operationID, ok)
	}
}

func TestValidateDependenciesFailsClosed(t *testing.T) {
	contract, _ := testLookup(GuaranteeTransactional)("createWidget")
	contracts := map[string]Contract{contract.OperationID: contract}
	available := map[Dependency]bool{DependencyAuthorization: true, DependencyAudit: true}
	if err := ValidateDependencies(contracts, available); !errors.Is(err, ErrExecutionUnavailable) {
		t.Fatalf("missing idempotency dependency error = %v", err)
	}
	available[DependencyIdempotency] = true
	if err := ValidateDependencies(contracts, available); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorEmitsGeneratedLowCardinalityObservation(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	executor, err := NewExecutor(testLookup(GuaranteeTransactional), logger)
	if err != nil {
		t.Fatal(err)
	}
	contract, _ := testLookup(GuaranteeTransactional)("createWidget")
	ctx, _, err := BeginInvocation(t.Context(), contract, Invocation{Surface: SurfaceCLI, IdempotencyKey: "secret-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(ctx, contract.OperationID, Execution{Transactional: func(context.Context, Contract) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"span_name=command.createWidget", "operation_id=createWidget", "surface=cli", "outcome=succeeded", "idempotency_policy=required"} {
		if !strings.Contains(logs.String(), value) {
			t.Fatalf("observation %q does not contain %q", logs.String(), value)
		}
	}
	if strings.Contains(logs.String(), "secret-key") {
		t.Fatalf("observation leaked idempotency key: %s", logs.String())
	}
}
