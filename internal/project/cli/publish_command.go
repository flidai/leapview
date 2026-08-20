package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/spf13/cobra"
)

type PublishOptions struct {
	ProjectPath string
	ProjectID   string
	Credentials cliapi.Credentials
	Checkpoint  CandidateCheckpoint
	CandidateID string
	Format      string
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

// PublishOperations is the Project-owned port for requesting policy-governed
// publication of an exact candidate.
type PublishOperations interface {
	Publish(context.Context, PublishOptions, io.Writer) error
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
	checkpoint := CandidateCheckpoint{ProjectPath: options.ProjectPath, TargetOrigin: credentials.Target, TargetSelector: identity.TargetSelector, TargetID: identity.TargetID, Environment: identity.Environment, ProjectID: options.ProjectID, CandidateID: options.CandidateID}
	if options.CandidateID != "" {
		checkpoint.CandidateID = options.CandidateID
	}
	options.Credentials = credentials
	options.Checkpoint = checkpoint
	return operations.Publish(ctx, options, out)
}
