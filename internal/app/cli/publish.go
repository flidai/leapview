package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

type projectPublishOperations struct {
	client        cliapi.Client
	requireActive bool
}

func publishCommand(ctx context.Context) *cobra.Command {
	client := capabilityAPIClient{
		httpClient:        authoringRefreshingHTTPClient(http.DefaultClient),
		validateAuthoring: true,
	}
	return projectcli.PublishCommand(
		ctx,
		client,
		projectcli.NewCandidateCheckpointStore(candidateCheckpointPath()),
		projectPublishOperations{client: client},
	)
}

func (operations projectPublishOperations) Publish(
	ctx context.Context,
	options projectcli.PublishOptions,
	out io.Writer,
) error {
	if operations.client == nil {
		return fmt.Errorf("Project publish API client is required")
	}
	transport, err := operations.client.Transport(ctx, options.Credentials)
	if err != nil {
		return err
	}
	checkpoint := options.Checkpoint
	response, err := deploymentgen.NewGenClient(transport).PublishProjectCandidate(
		ctx,
		deploymentgen.GenPublishProjectCandidateClientRequest{
			Project:   checkpoint.ProjectID,
			Candidate: checkpoint.CandidateID,
			Headers: deploymentgen.GenPublishProjectCandidateClientHeaders{
				IdempotencyKey: deploymentIdempotencyKey(
					"candidate-publish-v2",
					checkpoint.ProjectID,
					checkpoint.CandidateID,
					fmt.Sprintf("%d", checkpoint.CandidateRevision),
					checkpoint.ProvenanceDigest,
					checkpoint.TargetID,
				),
			},
			Body: deploymentgen.CandidatePublishRequest{
				ExpectedRevision: checkpoint.CandidateRevision,
				ProvenanceDigest: checkpoint.ProvenanceDigest,
				TargetId:         checkpoint.TargetID,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("publish candidate: %w", mapPublishProjectCandidateFailure(err))
	}
	if response.Body.Approval != nil &&
		response.Body.Approval.Status == deploymentgen.DeploymentApprovalStatusPending {
		if operations.requireActive {
			return fmt.Errorf(
				"publish request %s is pending approval; target bootstrap requires an active deployment",
				response.Body.Id,
			)
		}
		if options.Format == "json" {
			return writePublicationResult(out, response.Body, checkpoint)
		}
		fmt.Fprintf(out, "publish request %s pending approval\n", response.Body.Id)
		writePublicationEvidence(out, response.Body)
		return nil
	}
	deployment, err := waitForCandidatePublish(
		ctx,
		deploymentgen.NewGenClient(transport),
		response.Body,
	)
	if err != nil {
		return err
	}
	if options.Format == "json" {
		return writePublicationResult(out, deployment, checkpoint)
	}
	fmt.Fprintf(
		out,
		"published %s deployment %s\n",
		deployment.ReleaseId,
		deployment.Id,
	)
	writePublicationEvidence(out, deployment)
	return nil
}

func writePublicationResult(
	out io.Writer,
	deployment deploymentgen.DeploymentResponse,
	checkpoint projectcli.CandidateCheckpoint,
) error {
	sourceRevision := ""
	if deployment.Evidence.SourceRevision != nil {
		sourceRevision = deployment.Evidence.SourceRevision.Revision
	}
	candidateID := deployment.Evidence.CandidateId
	if candidateID == "" {
		candidateID = checkpoint.CandidateID
	}
	candidateRevision := deployment.Evidence.CandidateRevision
	if candidateRevision == 0 {
		candidateRevision = checkpoint.CandidateRevision
	}
	targetID := deployment.Evidence.TargetId
	if targetID == "" {
		targetID = checkpoint.TargetID
	}
	artifactDigest := deployment.Evidence.ArtifactDigest
	if artifactDigest == "" {
		artifactDigest = checkpoint.ArtifactDigest
	}
	releaseDigest := deployment.Evidence.ReleaseDigest
	if releaseDigest == "" {
		releaseDigest = checkpoint.ProvenanceDigest
	}
	return json.NewEncoder(out).Encode(projectcli.PublishResult{
		SchemaVersion:     1,
		DeploymentID:      deployment.Id,
		Status:            string(deployment.Status),
		CandidateID:       candidateID,
		CandidateRevision: candidateRevision,
		TargetID:          targetID,
		PrincipalID:       deployment.CreatedBy,
		ArtifactDigest:    artifactDigest,
		ReleaseDigest:     releaseDigest,
		SourceRevision:    sourceRevision,
	})
}

func writePublicationEvidence(
	out io.Writer,
	deployment deploymentgen.DeploymentResponse,
) {
	sourceRevision := "none"
	if deployment.Evidence.SourceRevision != nil {
		sourceRevision = deployment.Evidence.SourceRevision.Revision
	}
	fmt.Fprintf(
		out,
		"evidence result %s artifact %s target %s candidate %s revision %d principal %s source %s release %s\n",
		deployment.Status,
		deployment.Evidence.ArtifactDigest,
		deployment.Evidence.TargetId,
		deployment.Evidence.CandidateId,
		deployment.Evidence.CandidateRevision,
		deployment.CreatedBy,
		sourceRevision,
		deployment.Evidence.ReleaseDigest,
	)
}

func waitForCandidatePublish(
	ctx context.Context,
	client *deploymentgen.GenClient,
	deployment deploymentgen.DeploymentResponse,
) (deploymentgen.DeploymentResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	projectID := deployment.ProjectId
	releaseID := deployment.ReleaseId
	deploymentID := deployment.Id
	if client == nil || projectID == "" || releaseID == "" ||
		deploymentID == "" {
		return deploymentgen.DeploymentResponse{}, fmt.Errorf(
			"publish candidate returned inconsistent deployment identity",
		)
	}
	for {
		if deployment.ProjectId != projectID ||
			deployment.ReleaseId != releaseID ||
			deployment.Id != deploymentID {
			return deploymentgen.DeploymentResponse{}, fmt.Errorf(
				"publish candidate returned inconsistent deployment scope",
			)
		}
		switch deployment.Status {
		case deploymentgen.DeploymentStatusActive:
			return deployment, nil
		case deploymentgen.DeploymentStatusQueued,
			deploymentgen.DeploymentStatusRunning:
		case deploymentgen.DeploymentStatusFailed,
			deploymentgen.DeploymentStatusCancelled,
			deploymentgen.DeploymentStatusSuperseded:
			detail := ""
			if deployment.Error != nil {
				detail = ": " + *deployment.Error
			}
			return deploymentgen.DeploymentResponse{}, fmt.Errorf(
				"publish candidate deployment %s%s",
				deployment.Status,
				detail,
			)
		default:
			return deploymentgen.DeploymentResponse{}, fmt.Errorf(
				"publish candidate returned unexpected deployment status %q",
				deployment.Status,
			)
		}
		select {
		case <-ctx.Done():
			return deploymentgen.DeploymentResponse{}, fmt.Errorf(
				"wait for candidate publication: %w",
				ctx.Err(),
			)
		case <-time.After(100 * time.Millisecond):
		}
		response, err := client.GetDeployment(
			ctx,
			deploymentgen.GenGetDeploymentClientRequest{
				Project: projectID, Deployment: deploymentID,
			},
		)
		if err != nil {
			return deploymentgen.DeploymentResponse{}, fmt.Errorf(
				"get candidate publication: %w",
				err,
			)
		}
		deployment = response.Body
	}
}

func candidateCheckpointPath() string {
	return filepath.Join(filepath.Dir(clientConfigPath()), "authoring.json")
}
