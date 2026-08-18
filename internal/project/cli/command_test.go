package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestValidateCommandOwnsProjectArgumentRules(t *testing.T) {
	command := ValidateCommand(context.Background())
	command.SetArgs([]string{"project.yaml", "--project", "other.yaml"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "choose either --project or positional project") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlanCommandHasNoWorkspaceSelector(t *testing.T) {
	command := PlanCommand(context.Background())
	if command.Flags().Lookup("workspace") != nil || command.Flags().Lookup("target") != nil {
		t.Fatal("project plan exposes a removed workspace/target selector")
	}
}

func TestDeliveryPlanTextOutputIncludesReviewEvidence(t *testing.T) {
	result := DeliveryPlanResult{
		PlanID: "plan-1", ProjectID: "finance", TargetID: "target-1", Environment: "prod", Operation: "code_change",
		SourceDigest: "sha256:source", PlanDigest: "sha256:plan", ExecutionDigest: "sha256:execution", ProvenanceDigest: "sha256:provenance", GovernanceDigest: "sha256:governance", EvidenceDigest: "sha256:evidence", Status: "planned",
		Evidence: DeliveryPlanEvidenceResult{
			Digest: "sha256:evidence", ImpactStatement: "graph impact is bounded", PhysicalWorkStatement: "one qualification step", ReuseStatement: "unchanged nodes are reusable", QualificationPolicy: "required", RollbackClass: "rollback_safe",
			StalePolicy: DeliveryStalePolicyResult{Mode: "reject"},
		},
	}
	var output bytes.Buffer
	if err := writeDeliveryPlanResult(&output, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"impact graph impact is bounded", "physical-work one qualification step", "reuse unchanged nodes are reusable", "qualification-policy required", "stale-policy reject", "rollback-class rollback_safe"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("plan output missing %q:\n%s", want, output.String())
		}
	}
}

func TestSchemaCommandRejectsUnknownFormats(t *testing.T) {
	command := SchemaCommand()
	command.SetArgs([]string{"export", "--format", "yaml"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), `unsupported schema format "yaml"`) {
		t.Fatalf("error = %v", err)
	}
}
