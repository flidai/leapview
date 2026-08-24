package module

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform"
)

func TestBuildDeploymentAuditIntentUsesGeneratedLifecyclePayloads(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		status    string
		want      map[string]any
		sequence  int64
	}{
		{
			name:      "create keeps explicit initial sequence",
			operation: string(gen.GenOperationCreateDeployment),
			status:    "queued",
			want:      map[string]any{"deploymentId": "deployment-1", "projectId": "project-1", "releaseId": "release-1", "status": "queued"},
			sequence:  1,
		},
		{
			name:      "activation uses queued payload",
			operation: string(gen.GenOperationActivateDeployment),
			status:    "queued",
			want:      map[string]any{"deploymentId": "deployment-1", "projectId": "project-1", "releaseId": "release-1", "status": "queued"},
			sequence:  0,
		},
		{
			name:      "cancellation uses cancelled payload",
			operation: string(gen.GenOperationCancelDeployment),
			status:    "cancelled",
			want:      map[string]any{"deploymentId": "deployment-1", "status": "cancelled"},
			sequence:  0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent, err := buildDeploymentAuditIntent(deploymentAuditCommandInput{
				OperationID: test.operation, ProjectID: "project-1", DeploymentID: "deployment-1", ReleaseID: "release-1",
				IdempotencyKey: test.name, PrincipalID: "principal-1", Status: test.status,
			})
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(intent.MetadataJSON), &got); err != nil {
				t.Fatal(err)
			}
			payload, ok := got["payload"].(map[string]any)
			if !ok || !reflect.DeepEqual(payload, test.want) {
				t.Fatalf("metadata payload = %#v, want %#v (metadata=%#v)", payload, test.want, got)
			}
			if intent.AggregateSequence != test.sequence {
				t.Fatalf("aggregate sequence = %d, want %d", intent.AggregateSequence, test.sequence)
			}
		})
	}
}

func TestDeploymentLifecycleAuditSequencesAreAllocatedInOrder(t *testing.T) {
	ctx := t.Context()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	audit := accesssqlite.NewRepository(store.SQLDB())

	for _, operation := range []string{
		string(gen.GenOperationCreateDeployment),
		string(gen.GenOperationActivateDeployment),
		string(gen.GenOperationCancelDeployment),
	} {
		status := "queued"
		if operation == string(gen.GenOperationCancelDeployment) {
			status = "cancelled"
		}
		intent, err := buildDeploymentAuditIntent(deploymentAuditCommandInput{
			OperationID: operation, ProjectID: "project-1", DeploymentID: "deployment-1", ReleaseID: "release-1",
			IdempotencyKey: operation, PrincipalID: "principal-1", Status: status,
		})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := store.SQLDB().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := audit.RecordAuditIntent(ctx, tx, intent); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := store.SQLDB().QueryContext(ctx, `
		SELECT operation, aggregate_sequence
		FROM audit_outbox
		WHERE aggregate_key = ?
		ORDER BY aggregate_sequence`, "deployment:deployment-1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct {
		operation string
		sequence  int64
	}{
		{string(gen.GenOperationCreateDeployment), 1},
		{string(gen.GenOperationActivateDeployment), 2},
		{string(gen.GenOperationCancelDeployment), 3},
	}
	for _, expected := range want {
		if !rows.Next() {
			t.Fatalf("missing audit row for %s", expected.operation)
		}
		var operation string
		var sequence int64
		if err := rows.Scan(&operation, &sequence); err != nil {
			t.Fatal(err)
		}
		if operation != expected.operation || sequence != expected.sequence {
			t.Fatalf("audit row = %s/%d, want %s/%d", operation, sequence, expected.operation, expected.sequence)
		}
	}
	if rows.Next() {
		t.Fatal("unexpected additional lifecycle audit row")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildDeploymentAuditIntentUsesApprovalIdentity(t *testing.T) {
	intent, err := buildDeploymentAuditIntent(deploymentAuditCommandInput{
		OperationID: string(gen.GenOperationApproveDeployment), ProjectID: "project-1", DeploymentID: "deployment-1",
		ApprovalID: "approval-2", ApprovalRev: 2, IdempotencyKey: "approve-2", PrincipalID: "reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if intent.AggregateKey != "deployment:deployment-1:approval:approval-2" {
		t.Fatalf("aggregate key = %q", intent.AggregateKey)
	}
	if intent.AggregateSequence != 1 {
		t.Fatalf("decision aggregate sequence before repository allocation = %d, want 1", intent.AggregateSequence)
	}
}
