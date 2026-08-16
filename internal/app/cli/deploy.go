package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

const projectDeploymentCandidateKey = "deploy"

// projectDeploymentLifecycle is the single deployment path: the target first
// prepares and validates an exact candidate, then publishes that candidate's
// retained provenance. Direct client-side release assembly is intentionally
// not part of this contract.
type projectDeploymentLifecycle interface {
	Synchronize(context.Context, projectcli.DevOptions, io.Writer, io.Writer) error
	Publish(context.Context, projectcli.PublishOptions, io.Writer) error
}

type projectDeployOperations struct {
	client    cliapi.Client
	lifecycle projectDeploymentLifecycle
}

type canonicalProjectDeploymentLifecycle struct {
	client      cliapi.Client
	checkpoints *projectcli.CandidateCheckpointStore
	remotes     projectcli.DevRemoteFactory
	publisher   projectcli.PublishOperations
}

func deployCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	client := capabilityAPIClient{
		httpClient:        authoringRefreshingHTTPClient(http.DefaultClient),
		validateAuthoring: true,
	}
	checkpoints := projectcli.NewCandidateCheckpointStore(candidateCheckpointPath())
	return projectcli.DeployCommand(
		ctx,
		client,
		projectDeployOperations{
			client: client,
			lifecycle: canonicalProjectDeploymentLifecycle{
				client:      client,
				checkpoints: checkpoints,
				remotes:     projectDevRemoteFactory{client: client},
				publisher:   projectPublishOperations{client: client},
			},
		},
	)
}

func (operations projectDeployOperations) Deploy(
	ctx context.Context,
	options projectcli.DeployOptions,
	out io.Writer,
) error {
	if operations.client == nil || operations.lifecycle == nil {
		return fmt.Errorf("Project deployment lifecycle is required")
	}
	environment, err := operations.client.Environment(
		ctx,
		options.Credentials,
		options.Environment,
	)
	if err != nil {
		return err
	}
	if asserted := strings.TrimSpace(options.Environment); asserted != "" &&
		strings.TrimSpace(environment) != asserted {
		return fmt.Errorf(
			"target instance environment %q does not match asserted environment %q",
			environment,
			asserted,
		)
	}
	devOptions := projectcli.DevOptions{
		ProjectPath:       options.ProjectPath,
		Credentials:       options.Credentials,
		UploadConcurrency: 4,
		Once:              true,
		NoBrowser:         true,
		CandidateKey:      projectDeploymentCandidateKey,
		Format:            "text",
	}
	if err := operations.lifecycle.Synchronize(ctx, devOptions, out, out); err != nil {
		return fmt.Errorf("synchronize deployment candidate: %w", err)
	}
	if err := operations.lifecycle.Publish(ctx, projectcli.PublishOptions{
		ProjectPath:  options.ProjectPath,
		Credentials:  options.Credentials,
		CandidateKey: projectDeploymentCandidateKey,
		Format:       "text",
	}, out); err != nil {
		return fmt.Errorf("publish deployment candidate: %w", err)
	}
	return nil
}

func (lifecycle canonicalProjectDeploymentLifecycle) Synchronize(
	ctx context.Context,
	options projectcli.DevOptions,
	out,
	errOut io.Writer,
) error {
	return projectcli.RunDev(
		ctx,
		lifecycle.client,
		lifecycle.checkpoints,
		lifecycle.remotes,
		options,
		nil,
		out,
		errOut,
	)
}

func (lifecycle canonicalProjectDeploymentLifecycle) Publish(
	ctx context.Context,
	options projectcli.PublishOptions,
	out io.Writer,
) error {
	return projectcli.RunPublish(
		ctx,
		lifecycle.client,
		lifecycle.checkpoints,
		lifecycle.publisher,
		options,
		out,
	)
}
