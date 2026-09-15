package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

type PublishOptions struct {
	ProjectID   string
	Credentials cliapi.Credentials
	Checkpoint  CandidateCheckpoint
	CandidateID string
	Format      string
	// IdempotencyKey is supplied by a retained deployment operation. Empty
	// preserves the standalone publish command's fresh-attempt behavior.
	IdempotencyKey string
}

type PublishResult struct {
	SchemaVersion  int    `json:"schemaVersion"`
	PublicationID  string `json:"publicationId"`
	GenerationID   string `json:"generationId"`
	CandidateID    string `json:"candidateId"`
	PlanID         string `json:"planId"`
	PlanDigest     string `json:"planDigest"`
	Status         string `json:"status"`
	TargetRevision int64  `json:"targetRevision,omitempty"`
}

// DeploymentStatusError lets the application adapter return a structured
// non-success outcome (pending approval, failure, or indeterminate) while
// retaining the selected operation and immutable identities in output.
type DeploymentStatusError struct {
	Result DeploymentOperationResult
}

func (e *DeploymentStatusError) Error() string {
	if e == nil {
		return "deployment did not commit activation"
	}
	if e.Result.NextAction != "" {
		return fmt.Sprintf("deployment operation %s: %s (%s)", e.Result.Handle, e.Result.Outcome, e.Result.NextAction)
	}
	return fmt.Sprintf("deployment operation %s: %s", e.Result.Handle, e.Result.Outcome)
}

// DeploymentSelectionError is a structured non-mutating failure for missing,
// ambiguous, or target-mismatched operation selection.
type DeploymentSelectionError struct {
	SchemaVersion int    `json:"schemaVersion"`
	Code          string `json:"code"`
	Handle        string `json:"handle,omitempty"`
	Detail        string `json:"detail"`
}

func (e *DeploymentSelectionError) Error() string {
	if e == nil {
		return "deployment operation selection failed"
	}
	return fmt.Sprintf("deployment operation selection failed (%s): %s", e.Code, e.Detail)
}

func WriteDeploymentSelectionError(out io.Writer, format, code, handle, detail string) error {
	selection := DeploymentSelectionError{SchemaVersion: 1, Code: code, Handle: strings.TrimSpace(handle), Detail: strings.TrimSpace(detail)}
	if format == "json" {
		return json.NewEncoder(out).Encode(selection)
	}
	fmt.Fprintf(out, "selection-error %s %s\n", selection.Code, selection.Detail)
	return nil
}

// DeploymentOperationResult is the stable structured deployment result. An
// active result is successful only after target publication evidence is
// committed; all other outcomes are returned as non-zero command results.
type DeploymentOperationResult struct {
	SchemaVersion             int                        `json:"schemaVersion"`
	Handle                    string                     `json:"handle"`
	CreatedAt                 string                     `json:"createdAt"`
	TargetOrigin              string                     `json:"targetOrigin"`
	TargetID                  string                     `json:"targetId,omitempty"`
	ProjectID                 string                     `json:"projectId"`
	Environment               string                     `json:"environment"`
	SourceRevision            string                     `json:"sourceRevision,omitempty"`
	SourceRepository          string                     `json:"sourceRepository,omitempty"`
	SourceRef                 string                     `json:"sourceRef,omitempty"`
	SourceChangeID            string                     `json:"sourceChangeId,omitempty"`
	SourceDigest              string                     `json:"sourceDigest,omitempty"`
	SourceAttestationDigest   string                     `json:"sourceAttestationDigest,omitempty"`
	ProvenanceDigest          string                     `json:"provenanceDigest,omitempty"`
	PlanID                    string                     `json:"planId,omitempty"`
	PlanDigest                string                     `json:"planDigest,omitempty"`
	PlanStatus                string                     `json:"planStatus,omitempty"`
	PlanEvidence              DeliveryPlanEvidenceResult `json:"planEvidence,omitempty"`
	GovernanceDigest          string                     `json:"governanceDigest,omitempty"`
	BaseGenerationID          string                     `json:"baseGenerationId,omitempty"`
	BaseTargetRevision        int64                      `json:"baseTargetRevision,omitempty"`
	ExecutionDigest           string                     `json:"executionDigest,omitempty"`
	EvidenceDigest            string                     `json:"evidenceDigest,omitempty"`
	BuildID                   string                     `json:"buildId,omitempty"`
	BuildRevision             int64                      `json:"buildRevision,omitempty"`
	CandidateID               string                     `json:"candidateId,omitempty"`
	CandidateRevision         int64                      `json:"candidateRevision,omitempty"`
	SealID                    string                     `json:"sealId,omitempty"`
	PublicationID             string                     `json:"publicationId,omitempty"`
	GenerationID              string                     `json:"generationId,omitempty"`
	PublicationTargetRevision int64                      `json:"publicationTargetRevision,omitempty"`
	Outcome                   DeploymentOperationOutcome `json:"outcome"`
	PublicationStatus         string                     `json:"publicationStatus,omitempty"`
	FailureCode               string                     `json:"failureCode,omitempty"`
	FailureDetail             string                     `json:"failureDetail,omitempty"`
	StatusURL                 string                     `json:"statusUrl,omitempty"`
	NextAction                string                     `json:"nextAction,omitempty"`
}

// PublishOperations is the Project-owned port for requesting policy-governed
// publication of an exact candidate.
type PublishOperations interface {
	Publish(context.Context, PublishOptions, io.Writer) error
}

// PublishResultOperations is an optional richer adapter used by deploy. The
// legacy PublishOperations port remains available for callers that only need
// the canonical command output.
type PublishResultOperations interface {
	PublishResult(context.Context, PublishOptions) (PublishResult, error)
}

// PublishCommand publishes one exact sealed candidate returned by build. The
// candidate checkpoint supplies project and target origin; callers cannot
// redirect this operation with a second destination selector.
func PublishCommand(
	ctx context.Context,
	client cliapi.Client,
	store *CandidateCheckpointStore,
	operations PublishOperations,
) *cobra.Command {
	values := PublishOptions{Format: "text"}
	command := &cobra.Command{
		Use:   "publish <candidate-id>",
		Short: "Publish an exact candidate through target policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			values.CandidateID = strings.TrimSpace(args[0])
			return RunPublish(
				ctx,
				client,
				store,
				operations,
				values,
				command.OutOrStdout(),
			)
		},
	}
	command.Flags().StringVar(
		&values.Credentials.Token, "token", "",
		"ephemeral API token for one-shot automation",
	)
	command.Flags().StringVar(
		&values.Format, "format", values.Format,
		"output format: text or json",
	)
	return command
}

// RunPublish promotes the exact sealed candidate checkpoint produced by Build.
// Project and target identity are read from the durable object checkpoint
// before credentials are resolved, so a caller cannot redirect publication.
func RunPublish(
	ctx context.Context,
	client cliapi.Client,
	store *CandidateCheckpointStore,
	operations PublishOperations,
	options PublishOptions,
	out io.Writer,
) error {
	if client == nil {
		return fmt.Errorf("Project CLI API client is required")
	}
	if store == nil {
		return fmt.Errorf("Project candidate checkpoint store is required")
	}
	if operations == nil {
		return fmt.Errorf("Project publish operations are required")
	}
	if options.Format != "text" && options.Format != "json" {
		return fmt.Errorf("publish format must be text or json")
	}
	if strings.TrimSpace(options.CandidateID) == "" {
		return fmt.Errorf("candidate id is required; run build and publish its sealed candidate")
	}
	identity, err := store.LoadObjectIdentity("candidate", options.CandidateID)
	if err != nil {
		return fmt.Errorf("resolve candidate checkpoint: %w", err)
	}
	if strings.TrimSpace(options.ProjectID) == "" {
		options.ProjectID = identity.ProjectID
	}
	if strings.TrimSpace(options.Credentials.Target) == "" {
		options.Credentials.Target = identity.TargetSelector
		if strings.TrimSpace(options.Credentials.Target) == "" {
			options.Credentials.Target = identity.TargetOrigin
		}
	}
	credentials, err := client.Resolve(ctx, options.Credentials)
	if err != nil {
		return err
	}
	checkpoint := CandidateCheckpoint{TargetOrigin: credentials.Target, TargetSelector: identity.TargetSelector, TargetID: identity.TargetID, Environment: identity.Environment, ProjectID: options.ProjectID, CandidateID: options.CandidateID}
	if options.CandidateID != "" {
		checkpoint.CandidateID = options.CandidateID
	}
	options.Credentials = credentials
	options.Checkpoint = checkpoint
	return operations.Publish(ctx, options, out)
}
