package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

// DeliveryError is the stable public CLI error envelope for target-owned
// delivery failures. Code and status are the server's typed problem identity;
// Kind groups approval, stale/conflict, forbidden, and other public classes.
type DeliveryError struct {
	Operation string
	Kind      string
	Code      string
	Status    int
	Detail    string
	Cause     error
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return "delivery operation failed"
	}
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = "delivery operation failed"
	}
	if e.Code == "" {
		return fmt.Sprintf("%s: %s", e.Operation, detail)
	}
	return fmt.Sprintf("%s failed (%s): %s", e.Operation, e.Code, detail)
}

func (e *DeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// DeliveryPlanOptions is the portable authoring input for a target-owned plan.
// The source digest is deliberately returned by the remote source snapshot
// synchronizer; callers must not invent a digest from arbitrary local bytes.
type DeliveryPlanOptions struct {
	ProjectPath             string
	Credentials             cliapi.Credentials
	TargetID                string
	Operation               string
	CandidateKey            string
	UploadConcurrency       int
	Format                  string
	CandidateID             string
	ProjectID               string
	SourceDigest            string
	SourceAttestationDigest string
	Environment             string
}

// DeliveryPlanResult is the redacted durable plan identity printed by plan,
// dev, and deploy. It contains no credentials, locations, or raw evidence.
type DeliveryPlanResult struct {
	SchemaVersion           int                        `json:"schemaVersion"`
	PlanID                  string                     `json:"planId"`
	ProjectID               string                     `json:"projectId"`
	TargetID                string                     `json:"targetId"`
	Environment             string                     `json:"environment"`
	Operation               string                     `json:"operation"`
	SourceDigest            string                     `json:"sourceDigest"`
	SourceAttestationDigest string                     `json:"sourceAttestationDigest,omitempty"`
	PlanDigest              string                     `json:"planDigest"`
	ExecutionDigest         string                     `json:"executionDigest"`
	ProvenanceDigest        string                     `json:"provenanceDigest"`
	GovernanceDigest        string                     `json:"governanceDigest"`
	EvidenceDigest          string                     `json:"evidenceDigest"`
	Status                  string                     `json:"status"`
	BaseGenerationID        string                     `json:"baseGenerationId,omitempty"`
	BaseTargetRevision      int64                      `json:"baseTargetRevision"`
	Evidence                DeliveryPlanEvidenceResult `json:"evidence"`
}

type DeliveryPlanEvidenceResult struct {
	Digest                  string                            `json:"digest"`
	CompatibilityBreaking   bool                              `json:"compatibilityBreaking"`
	AddedCount              int32                             `json:"addedCount"`
	RemovedCount            int32                             `json:"removedCount"`
	DirectlyModifiedCount   int32                             `json:"directlyModifiedCount"`
	IndirectlyAffectedCount int32                             `json:"indirectlyAffectedCount"`
	ReuseCount              int32                             `json:"reuseCount"`
	QualificationStepCount  int32                             `json:"qualificationStepCount"`
	ImpactStatement         string                            `json:"impactStatement,omitempty"`
	PhysicalWorkStatement   string                            `json:"physicalWorkStatement,omitempty"`
	ReuseStatement          string                            `json:"reuseStatement,omitempty"`
	RollbackClass           string                            `json:"rollbackClass,omitempty"`
	PlannedInputs           []DeliveryPlannedInputResult      `json:"plannedInputs"`
	QualificationPolicy     string                            `json:"qualificationPolicy"`
	QualificationSteps      []DeliveryQualificationStepResult `json:"qualificationSteps"`
	StalePolicy             DeliveryStalePolicyResult         `json:"stalePolicy"`
	ReuseDecisions          []DeliveryReuseDecisionResult     `json:"reuseDecisions"`
}

type DeliveryPlannedInputResult struct {
	ID       string `json:"id"`
	Mode     string `json:"mode"`
	Revision string `json:"revision,omitempty"`
	Bound    string `json:"bound,omitempty"`
}

type DeliveryQualificationStepResult struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Blocking    bool   `json:"blocking"`
}

type DeliveryStalePolicyResult struct {
	Mode              string `json:"mode"`
	AllowRetainedBase bool   `json:"allowRetainedBase"`
	Description       string `json:"description,omitempty"`
}

type DeliveryReuseDecisionResult struct {
	ResourceID     string `json:"resourceId"`
	Reusable       bool   `json:"reusable"`
	RetainBase     bool   `json:"retainBase"`
	Reason         string `json:"reason"`
	ReuseKeyDigest string `json:"reuseKeyDigest,omitempty"`
}

// DeliveryPlanOperations is implemented by the application adapter. The
// adapter must synchronize an exact source snapshot before creating the plan.
type DeliveryPlanOperations interface {
	Create(context.Context, DeliveryPlanOptions) (DeliveryPlanResult, error)
}

// DeliveryPlanCommand constructs the canonical target-owned plan command.
func DeliveryPlanCommand(ctx context.Context, operations DeliveryPlanOperations) *cobra.Command {
	values := DeliveryPlanOptions{
		ProjectPath:       filepath.Join("dashboards", "leapview.yaml"),
		TargetID:          "",
		Operation:         "code_change",
		CandidateKey:      "plan",
		UploadConcurrency: 4,
		Format:            "text",
	}
	command := &cobra.Command{
		Use:   "plan [project]",
		Short: "Capture an exact source snapshot and create a target-owned delivery plan",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if operations == nil {
				return fmt.Errorf("delivery plan operations are required")
			}
			if len(args) == 1 {
				if command.Flags().Changed("project") {
					return fmt.Errorf("choose either --project or positional project, not both")
				}
				values.ProjectPath = args[0]
			}
			if values.Format != "text" && values.Format != "json" {
				return fmt.Errorf("plan format must be text or json")
			}
			result, err := operations.Create(ctx, values)
			if err != nil {
				return err
			}
			return writeDeliveryPlanResult(command.OutOrStdout(), values.Format, result)
		},
	}
	command.Flags().StringVar(&values.ProjectPath, "project", values.ProjectPath, "project manifest path")
	command.Flags().StringVar(&values.Credentials.Target, "target", "", "LeapView target profile or URL (bound into this plan)")
	command.Flags().StringVar(&values.Credentials.Token, "token", "", "ephemeral API token for one-shot automation")
	command.Flags().StringVar(&values.Operation, "operation", values.Operation, "delivery operation: code_change, restatement, binding_change, or policy_change")
	command.Flags().StringVar(&values.CandidateKey, "candidate-key", values.CandidateKey, "stable source synchronization key")
	command.Flags().IntVar(&values.UploadConcurrency, "upload-concurrency", values.UploadConcurrency, "maximum parallel source uploads (1-16)")
	command.Flags().StringVar(&values.Format, "format", values.Format, "output format: text or json")
	return command
}

func writeDeliveryPlanResult(out io.Writer, format string, result DeliveryPlanResult) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	fmt.Fprintf(out, "plan %s target %s environment %s operation %s\n", result.PlanID, result.TargetID, result.Environment, result.Operation)
	fmt.Fprintf(out, "project %s source %s status %s\n", result.ProjectID, result.SourceDigest, result.Status)
	fmt.Fprintf(out, "plan-digest %s\nexecution-digest %s\nprovenance-digest %s\ngovernance-digest %s\nevidence-digest %s\n", result.PlanDigest, result.ExecutionDigest, result.ProvenanceDigest, result.GovernanceDigest, result.EvidenceDigest)
	fmt.Fprintf(out, "evidence digest %s compatibility-breaking %t added %d removed %d modified %d affected %d reused %d qualification-steps %d\n", result.Evidence.Digest, result.Evidence.CompatibilityBreaking, result.Evidence.AddedCount, result.Evidence.RemovedCount, result.Evidence.DirectlyModifiedCount, result.Evidence.IndirectlyAffectedCount, result.Evidence.ReuseCount, result.Evidence.QualificationStepCount)
	if result.Evidence.ImpactStatement != "" {
		fmt.Fprintf(out, "impact %s\n", result.Evidence.ImpactStatement)
	}
	if result.Evidence.PhysicalWorkStatement != "" {
		fmt.Fprintf(out, "physical-work %s\n", result.Evidence.PhysicalWorkStatement)
	}
	if result.Evidence.ReuseStatement != "" {
		fmt.Fprintf(out, "reuse %s\n", result.Evidence.ReuseStatement)
	}
	if result.Evidence.QualificationPolicy != "" {
		fmt.Fprintf(out, "qualification-policy %s\n", result.Evidence.QualificationPolicy)
	}
	if result.Evidence.StalePolicy.Mode != "" {
		fmt.Fprintf(out, "stale-policy %s allow-retained-base %t\n", result.Evidence.StalePolicy.Mode, result.Evidence.StalePolicy.AllowRetainedBase)
	}
	if result.Evidence.RollbackClass != "" {
		fmt.Fprintf(out, "rollback-class %s\n", result.Evidence.RollbackClass)
	}
	if result.BaseGenerationID != "" {
		fmt.Fprintf(out, "base-generation %s revision %d\n", result.BaseGenerationID, result.BaseTargetRevision)
	}
	return nil
}

type DeliveryBuildOptions struct {
	PlanID      string
	ProjectID   string
	Credentials cliapi.Credentials
	Format      string
}

type DeliveryBuildResult struct {
	SchemaVersion   int    `json:"schemaVersion"`
	BuildID         string `json:"buildId"`
	PlanID          string `json:"planId"`
	PlanDigest      string `json:"planDigest"`
	SourceDigest    string `json:"sourceDigest"`
	ExecutionDigest string `json:"executionDigest"`
	CandidateID     string `json:"candidateId,omitempty"`
	SealID          string `json:"sealId,omitempty"`
	Status          string `json:"status"`
	Revision        int64  `json:"revision"`
}

type DeliveryBuildOperations interface {
	Build(context.Context, DeliveryBuildOptions) (DeliveryBuildResult, error)
}

func DeliveryBuildCommand(ctx context.Context, operations DeliveryBuildOperations) *cobra.Command {
	values := DeliveryBuildOptions{Format: "text"}
	command := &cobra.Command{
		Use:   "build <plan-id>",
		Short: "Build and seal a target-owned delivery plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if operations == nil {
				return fmt.Errorf("delivery build operations are required")
			}
			if values.Format != "text" && values.Format != "json" {
				return fmt.Errorf("build format must be text or json")
			}
			values.PlanID = strings.TrimSpace(args[0])
			result, err := operations.Build(ctx, values)
			if err != nil {
				return err
			}
			if values.Format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(result)
			}
			fmt.Fprintf(command.OutOrStdout(), "build %s plan %s status %s revision %d\n", result.BuildID, result.PlanID, result.Status, result.Revision)
			fmt.Fprintf(command.OutOrStdout(), "plan-digest %s source %s execution-digest %s\n", result.PlanDigest, result.SourceDigest, result.ExecutionDigest)
			if result.CandidateID != "" {
				fmt.Fprintf(command.OutOrStdout(), "candidate %s seal %s\n", result.CandidateID, result.SealID)
			}
			return nil
		},
	}
	command.Flags().StringVar(&values.Credentials.Token, "token", "", "ephemeral API token for one-shot automation")
	command.Flags().StringVar(&values.Format, "format", values.Format, "output format: text or json")
	return command
}

type DeliveryRollbackOptions struct {
	GenerationID string
	ProjectID    string
	Credentials  cliapi.Credentials
	Format       string
}

type DeliveryRollbackResult struct {
	SchemaVersion          int    `json:"schemaVersion"`
	PublicationID          string `json:"publicationId"`
	GenerationID           string `json:"generationId"`
	CandidateID            string `json:"candidateId"`
	PlanID                 string `json:"planId"`
	PlanDigest             string `json:"planDigest"`
	Status                 string `json:"status"`
	ExpectedTargetRevision int64  `json:"expectedTargetRevision"`
	ResultTargetRevision   int64  `json:"resultTargetRevision"`
}

type DeliveryRollbackOperations interface {
	Rollback(context.Context, DeliveryRollbackOptions) (DeliveryRollbackResult, error)
}

func DeliveryRollbackCommand(ctx context.Context, operations DeliveryRollbackOperations) *cobra.Command {
	values := DeliveryRollbackOptions{Format: "text"}
	command := &cobra.Command{
		Use:   "rollback <generation-id>",
		Short: "Governed rollback to a retained serving generation",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if operations == nil {
				return fmt.Errorf("delivery rollback operations are required")
			}
			if values.Format != "text" && values.Format != "json" {
				return fmt.Errorf("rollback format must be text or json")
			}
			values.GenerationID = strings.TrimSpace(args[0])
			result, err := operations.Rollback(ctx, values)
			if err != nil {
				return err
			}
			if values.Format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(result)
			}
			fmt.Fprintf(command.OutOrStdout(), "rollback publication %s generation %s status %s\n", result.PublicationID, result.GenerationID, result.Status)
			fmt.Fprintf(command.OutOrStdout(), "plan %s digest %s revision %d -> %d\n", result.PlanID, result.PlanDigest, result.ExpectedTargetRevision, result.ResultTargetRevision)
			return nil
		},
	}
	command.Flags().StringVar(&values.Credentials.Token, "token", "", "ephemeral API token for one-shot automation")
	command.Flags().StringVar(&values.Format, "format", values.Format, "output format: text or json")
	return command
}
