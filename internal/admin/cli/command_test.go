package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
)

type fakeOperations struct {
	called  string
	options Options
}

func (operations *fakeOperations) Initialize(context.Context, adminoffline.InitializeRequest, io.Writer) error {
	operations.called = "initialize"
	return nil
}
func (operations *fakeOperations) AcknowledgeInitialCredentials(context.Context) error {
	operations.called = "acknowledge"
	return nil
}
func (operations *fakeOperations) Maintenance(_ context.Context, request adminoffline.MaintenanceRequest, _ io.Writer) error {
	operations.called = "maintenance"
	operations.options = Options{
		Apply: request.Apply, AuditDays: request.AuditDays, QueryDays: request.QueryDays,
		ArchivedAgentDays: request.ArchivedAgentDays, AuthStateDays: request.AuthStateDays,
	}
	return nil
}
func (operations *fakeOperations) BootstrapPhysicalPool(context.Context, adminoffline.PhysicalPoolBootstrapRequest, io.Writer) error {
	operations.called = "pool-bootstrap"
	return nil
}
func (operations *fakeOperations) BootstrapQualificationLocalPhysicalPool(context.Context, io.Writer) error {
	operations.called = "qualification-local-pool-bootstrap"
	return nil
}

func TestCommandOwnsMaintenanceFlags(t *testing.T) {
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"maintenance", "--apply", "--audit-days", "10", "--query-days", "11", "--archived-agent-days", "12", "--auth-state-days", "13"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "maintenance" {
		t.Fatalf("called = %q", operations.called)
	}
	if !operations.options.Apply || operations.options.AuditDays != 10 || operations.options.QueryDays != 11 ||
		operations.options.ArchivedAgentDays != 12 || operations.options.AuthStateDays != 13 {
		t.Fatalf("options = %#v", operations.options)
	}
}

func TestCommandRequiresOperations(t *testing.T) {
	command := Command(context.Background(), nil)
	command.SetArgs([]string{"initialize"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "operations are required") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommandRejectsInvalidNestedSubcommandsWithoutUsage(t *testing.T) {
	command := Command(context.Background(), &fakeOperations{})
	var output strings.Builder
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"delivery", "not-a-command"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(output.String(), "Usage:") {
		t.Fatalf("invalid command emitted usage: %q", output.String())
	}
}

func TestCommandRoutesQualificationLocalPoolBootstrapOnlyWithApply(t *testing.T) {
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"delivery", "pool", "qualify"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--apply is required") {
		t.Fatalf("missing confirmation error = %v", err)
	}
	command = Command(context.Background(), operations)
	command.SetArgs([]string{"delivery", "pool", "qualify", "--apply"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "qualification-local-pool-bootstrap" {
		t.Fatalf("called = %q", operations.called)
	}
}
