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

// projectDeploymentLifecycle is retained as a source-compatible seam for
// downstream integrations. The public deploy command no longer invokes it:
// deploy is a thin composition of the canonical plan, build, and publish
// capabilities below.
type projectDeploymentLifecycle interface {
	Synchronize(context.Context, projectcli.DevOptions, io.Writer, io.Writer) error
	Publish(context.Context, projectcli.PublishOptions, io.Writer) error
}

type projectDeployOperations struct {
	client cliapi.Client
	// lifecycle is deprecated and intentionally ignored by Deploy. Keep the
	// field for source compatibility with integrations that construct this
	// value directly; canonical callers must provide planner, builder, and
	// publisher.
	lifecycle   projectDeploymentLifecycle
	planner     projectcli.DeliveryPlanOperations
	builder     projectcli.DeliveryBuildOperations
	publisher   projectcli.PublishOperations
	checkpoints *projectcli.CandidateCheckpointStore
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
			client:      client,
			planner:     projectDeliveryPlanOperations{client: client, remotes: projectDevRemoteFactory{client: client}, checkpoints: checkpoints},
			builder:     projectDeliveryBuildOperations{client: client, checkpoints: checkpoints},
			publisher:   projectPublishOperations{client: client, checkpoints: checkpoints},
			checkpoints: checkpoints,
		},
	)
}

func (operations projectDeployOperations) Deploy(
	ctx context.Context,
	options projectcli.DeployOptions,
	out io.Writer,
) error {
	if operations.client == nil || operations.planner == nil || operations.builder == nil || operations.publisher == nil {
		return fmt.Errorf("canonical project deployment operations are required (plan, build, and publish)")
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
	plan, err := operations.planner.Create(ctx, projectcli.DeliveryPlanOptions{
		ProjectPath:       options.ProjectPath,
		Credentials:       options.Credentials,
		Operation:         "code_change",
		CandidateKey:      projectDeploymentCandidateKey,
		UploadConcurrency: 4,
		Environment:       options.Environment,
	})
	if err != nil {
		return fmt.Errorf("create deployment plan: %w", err)
	}
	build, err := operations.builder.Build(ctx, projectcli.DeliveryBuildOptions{
		ProjectID:   plan.ProjectID,
		PlanID:      plan.PlanID,
		Credentials: options.Credentials,
	})
	if err != nil {
		return fmt.Errorf("build deployment plan: %w", err)
	}
	if strings.TrimSpace(build.CandidateID) == "" {
		return fmt.Errorf("build %s is %s and has not produced a sealed candidate; run leapview publish after the build is sealed", build.BuildID, build.Status)
	}
	checkpoint := projectcli.CandidateCheckpoint{
		ProjectPath: options.ProjectPath, TargetOrigin: options.Credentials.Target,
		TargetID: plan.TargetID, Environment: plan.Environment, ProjectID: plan.ProjectID,
		CandidateID: build.CandidateID, CandidateKey: projectDeploymentCandidateKey,
		ArtifactDigest: plan.SourceDigest, PlanID: plan.PlanID, PlanDigest: plan.PlanDigest,
		ExecutionDigest: plan.ExecutionDigest, EvidenceDigest: plan.EvidenceDigest,
	}
	if operations.checkpoints != nil {
		if err := operations.checkpoints.Save(checkpoint); err != nil {
			return fmt.Errorf("persist deployment checkpoint: %w", err)
		}
	}
	if err := operations.publisher.Publish(ctx, projectcli.PublishOptions{
		ProjectPath: options.ProjectPath, ProjectID: plan.ProjectID, Credentials: options.Credentials,
		Checkpoint: checkpoint, CandidateID: build.CandidateID,
		Format: "text",
	}, out); err != nil {
		return fmt.Errorf("publish deployment candidate: %w", err)
	}
	return nil
}
