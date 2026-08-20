package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

type projectPublishOperations struct {
	client        cliapi.Client
	requireActive bool
	checkpoints   *projectcli.CandidateCheckpointStore
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
		projectPublishOperations{client: client, checkpoints: projectcli.NewCandidateCheckpointStore(candidateCheckpointPath())},
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
	idempotencyKey, err := publicationAttemptIdempotencyKey(checkpoint)
	if err != nil {
		return err
	}
	response, err := deploymentgen.NewGenClient(transport).PublishDeliveryCandidate(
		ctx,
		deploymentgen.GenPublishDeliveryCandidateClientRequest{
			Project:   checkpoint.ProjectID,
			Candidate: checkpoint.CandidateID,
			Headers: deploymentgen.GenPublishDeliveryCandidateClientHeaders{
				IdempotencyKey: idempotencyKey,
			},
		},
	)
	if err != nil {
		return mapDeliveryCLIError("publish delivery candidate", err)
	}
	if response.Body.Id == "" {
		return fmt.Errorf("publish delivery candidate returned no durable publication identity")
	}
	if operations.checkpoints != nil {
		identity := projectcli.DeliveryObjectCheckpoint{ProjectID: checkpoint.ProjectID, TargetOrigin: options.Credentials.Target}
		if response.Body.CandidateId != "" {
			_ = operations.checkpoints.SaveObjectIdentity("candidate", response.Body.CandidateId, identity)
		}
		if response.Body.GenerationId != "" {
			_ = operations.checkpoints.SaveObjectIdentity("generation", response.Body.GenerationId, identity)
		}
	}
	if options.Format == "json" {
		return json.NewEncoder(out).Encode(projectcli.PublishResult{
			SchemaVersion: 1, PublicationID: response.Body.Id,
			Status: string(response.Body.Status), CandidateID: response.Body.CandidateId,
			GenerationID: response.Body.GenerationId, PlanID: response.Body.PlanId, PlanDigest: response.Body.PlanDigest,
			TargetRevision: response.Body.ResultTargetRevision,
		})
	}
	fmt.Fprintf(out, "publication %s candidate %s generation %s status %s\n", response.Body.Id, response.Body.CandidateId, response.Body.GenerationId, response.Body.Status)
	fmt.Fprintf(out, "plan %s digest %s target-revision %d\n", response.Body.PlanId, response.Body.PlanDigest, response.Body.ResultTargetRevision)
	return nil
}

func publicationAttemptIdempotencyKey(
	checkpoint projectcli.CandidateCheckpoint,
) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("generate publication attempt identity: %w", err)
	}
	return deploymentIdempotencyKey(
		"delivery-publish",
		checkpoint.ProjectID,
		checkpoint.CandidateID,
		fmt.Sprintf("%x", nonce[:]),
	), nil
}

func candidateCheckpointPath() string {
	return filepath.Join(filepath.Dir(clientConfigPath()), "authoring.json")
}
