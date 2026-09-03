package module

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/deployment/api/gen"
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
