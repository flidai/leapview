package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestValidateCommandOwnsProjectArgumentRules(t *testing.T) {
	command := ValidateCommand(context.Background())
	command.SetArgs([]string{"source-root-a", "--source-root", "source-root-b"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "choose either --source-root or positional source root") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlanCommandHasNoWorkspaceSelector(t *testing.T) {
	command := DeliveryPlanCommand(context.Background(), nil)
	if command.Flags().Lookup("workspace") != nil {
		t.Fatal("project plan exposes a removed workspace selector")
	}
}

type firstSourcePlanFlagOperations struct{ got DeliveryPlanOptions }

func (o *firstSourcePlanFlagOperations) Create(_ context.Context, options DeliveryPlanOptions) (DeliveryPlanResult, error) {
	o.got = options
	return DeliveryPlanResult{}, nil
}

func TestPlanCommandPropagatesExplicitFirstSourcePreparationFlag(t *testing.T) {
	operations := &firstSourcePlanFlagOperations{}
	command := DeliveryPlanCommand(t.Context(), operations)
	command.SetOut(&bytes.Buffer{})
	preparation := "0198f2c0-7c7a-7f00-8a11-000000000301"
	command.SetArgs([]string{"--first-source-preparation-id", preparation, "--idempotency-key", "prepared-plan-key"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if operations.got.FirstSourcePreparationID != preparation || operations.got.IdempotencyKey != "prepared-plan-key" {
		t.Fatalf("options = %#v", operations.got)
	}
}

func TestPlanCommandRejectsPreparationWithoutExplicitOperationKey(t *testing.T) {
	for _, args := range [][]string{{"--first-source-preparation-id", "0198f2c0-7c7a-7f00-8a11-000000000301"}, {"--first-source-preparation-id", "0198f2c0-7c7a-7f00-8a11-000000000301", "--idempotency-key", " "}, {"--first-source-preparation-id", "", "--idempotency-key", "prepared-plan-key"}} {
		operations := &firstSourcePlanFlagOperations{}
		command := DeliveryPlanCommand(t.Context(), operations)
		command.SetArgs(args)
		if err := command.Execute(); err == nil || operations.got.Operation != "" {
			t.Fatalf("prepared plan intent reached operations: err=%v, options=%#v", err, operations.got)
		}
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
