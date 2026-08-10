package command

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func testLookup(guarantee Guarantee) Lookup {
	return func(operationID string) (Contract, bool) {
		if operationID != "createWidget" {
			return Contract{}, false
		}
		return Contract{OperationID: operationID, Owner: "Widgets", AuditAction: "widget.created", Guarantee: guarantee}, true
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
		OperationID: "finalizeRelease", Owner: "ReleaseAPI", AuditAction: "release.validating", Guarantee: GuaranteeTransactional,
		Execution: &AsyncExecutionContract{Mode: "async", Guarantee: "transactional", JobKind: "release.finalize", ResourceKind: "release", InitialEvent: "release.validating", InitialState: "validating", StatusOperation: "getRelease", EventsOperation: "listReleaseEvents", Cancellation: "unsupported"},
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
