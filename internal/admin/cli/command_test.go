package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
)

type fakeOperations struct {
	called       string
	options      Options
	requeueEvent string
}

func (operations *fakeOperations) AuditOutbox(_ context.Context, request adminoffline.AuditOutboxRequest, _ io.Writer) error {
	operations.called, operations.requeueEvent, operations.options.Apply = "audit-outbox", request.RequeueEventID, request.Apply
	return nil
}

func (operations *fakeOperations) Initialize(context.Context, adminoffline.InitializeRequest, io.Writer) error {
	operations.called = "initialize"
	return nil
}
func (operations *fakeOperations) AcknowledgeInitialCredentials(context.Context) error {
	operations.called = "acknowledge"
	return nil
}
func (operations *fakeOperations) StorageCleanup(_ context.Context, request adminoffline.StorageCleanupRequest, _ io.Writer) error {
	operations.called, operations.options.Apply = "cleanup", request.Apply
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
func (operations *fakeOperations) Backup(_ context.Context, request adminoffline.BackupRequest, _ io.Writer) error {
	operations.called = "backup"
	operations.options.BackupOut, operations.options.DatabaseOnly = request.Out, request.DatabaseOnly
	return nil
}
func (operations *fakeOperations) Restore(_ context.Context, request adminoffline.RestoreRequest, _ io.Reader, _ io.Writer) error {
	operations.called = "restore"
	operations.options.RestoreFrom, operations.options.RestoreBefore = request.From, request.CurrentBackup
	operations.options.ConfirmRestore, operations.options.DatabaseOnly = request.Confirm, request.DatabaseOnly
	operations.options.PreflightOnly = request.PreflightOnly
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
func (operations *fakeOperations) RepairDeliveryRoot(context.Context, adminoffline.DeliveryRepairRequest, io.Writer) error {
	operations.called = "delivery-repair"
	return nil
}
func (operations *fakeOperations) AuditDeliveryRoots(context.Context, adminoffline.DeliveryAuditRequest, io.Writer) error {
	operations.called = "delivery-audit"
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

func TestCommandOwnsAuditOutboxRecoveryFlags(t *testing.T) {
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"audit-outbox", "--requeue-event", "audit-event-1", "--apply"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "audit-outbox" || operations.requeueEvent != "audit-event-1" || !operations.options.Apply {
		t.Fatalf("audit outbox call = %q %q apply=%v", operations.called, operations.requeueEvent, operations.options.Apply)
	}
}

func TestCommandRequiresOperations(t *testing.T) {
	command := Command(context.Background(), nil)
	command.SetArgs([]string{"backup", "--out", "-"})
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

func TestCommandRoutesRestorePreflightWithoutConfirmation(t *testing.T) {
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"restore", "--from", "backup.tar.gz", "--preflight-only"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "restore" || !operations.options.PreflightOnly || operations.options.ConfirmRestore {
		t.Fatalf("restore options = %#v", operations.options)
	}
}

func TestCommandRoutesBoundedDeliveryRepair(t *testing.T) {
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"delivery", "repair", "--pool-id", "pool", "--kind", "candidate", "--source-id", "candidate", "--catalog-digest", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--object-key", "catalogs/a.ducklake", "--created-at", "2026-08-17T12:00:00Z", "--apply"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "delivery-repair" {
		t.Fatalf("called = %q", operations.called)
	}
}

func TestCommandRoutesReadOnlyDeliveryAudit(t *testing.T) {
	operations := &fakeOperations{}
	command := Command(context.Background(), operations)
	command.SetArgs([]string{"delivery", "audit", "--pool-id", "pool"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.called != "delivery-audit" {
		t.Fatalf("operations called = %q", operations.called)
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
