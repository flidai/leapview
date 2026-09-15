package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

type projectPublishOperations struct {
	client      cliapi.Client
	checkpoints *projectcli.CandidateCheckpointStore
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
	result, err := operations.PublishResult(ctx, options)
	if err != nil {
		return err
	}
	if options.Format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	fmt.Fprintf(out, "publication %s candidate %s generation %s status %s\n", result.PublicationID, result.CandidateID, result.GenerationID, result.Status)
	fmt.Fprintf(out, "plan %s digest %s target-revision %d\n", result.PlanID, result.PlanDigest, result.TargetRevision)
	return nil
}

func (operations projectPublishOperations) PublishResult(
	ctx context.Context,
	options projectcli.PublishOptions,
) (projectcli.PublishResult, error) {
	if operations.client == nil {
		return projectcli.PublishResult{}, fmt.Errorf("Project publish API client is required")
	}
	transport, err := operations.client.Transport(ctx, options.Credentials)
	if err != nil {
		return projectcli.PublishResult{}, err
	}
	checkpoint := options.Checkpoint
	idempotencyKey := strings.TrimSpace(options.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey, err = publicationAttemptIdempotencyKey(checkpoint)
		if err != nil {
			return projectcli.PublishResult{}, err
		}
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
		return projectcli.PublishResult{}, mapDeliveryCLIError("publish delivery candidate", err)
	}
	if response.Body.Id == "" {
		return projectcli.PublishResult{}, fmt.Errorf("publish delivery candidate returned no durable publication identity")
	}
	if err := validatePublicationEvidence(response.Body, checkpoint); err != nil {
		return projectcli.PublishResult{}, err
	}
	if operations.checkpoints != nil {
		identity := projectcli.DeliveryObjectCheckpoint{ProjectID: checkpoint.ProjectID, TargetOrigin: options.Credentials.Target, TargetSelector: checkpoint.TargetSelector, TargetID: checkpoint.TargetID, Environment: checkpoint.Environment}
		if response.Body.CandidateId != "" {
			if err := operations.checkpoints.SaveObjectIdentity("candidate", response.Body.CandidateId, identity); err != nil {
				return projectcli.PublishResult{}, fmt.Errorf("persist published candidate identity: %w", err)
			}
		}
		if response.Body.GenerationId != "" {
			if err := operations.checkpoints.SaveObjectIdentity("generation", response.Body.GenerationId, identity); err != nil {
				return projectcli.PublishResult{}, fmt.Errorf("persist published generation identity: %w", err)
			}
		}
	}
	return projectcli.PublishResult{
		SchemaVersion: 1, PublicationID: response.Body.Id,
		Status: string(response.Body.Status), CandidateID: response.Body.CandidateId,
		GenerationID: response.Body.GenerationId, PlanID: response.Body.PlanId, PlanDigest: response.Body.PlanDigest,
		TargetRevision: response.Body.ResultTargetRevision,
	}, nil
}

func validatePublicationEvidence(evidence deploymentgen.DeliveryPublicationEvidenceResponse, checkpoint projectcli.CandidateCheckpoint) error {
	for _, identity := range []struct {
		name, expected, actual string
	}{
		{"project", strings.TrimSpace(checkpoint.ProjectID), strings.TrimSpace(evidence.ProjectId)},
		{"candidate", strings.TrimSpace(checkpoint.CandidateID), strings.TrimSpace(evidence.CandidateId)},
		{"plan", strings.TrimSpace(checkpoint.PlanID), strings.TrimSpace(evidence.PlanId)},
		{"plan digest", strings.TrimSpace(checkpoint.PlanDigest), strings.TrimSpace(evidence.PlanDigest)},
		{"target", strings.TrimSpace(checkpoint.TargetID), strings.TrimSpace(evidence.TargetId)},
		{"environment", strings.TrimSpace(checkpoint.Environment), strings.TrimSpace(evidence.Environment)},
	} {
		if identity.expected != "" && identity.expected != identity.actual {
			return fmt.Errorf("publication %s identity %q does not match retained checkpoint %q", identity.name, identity.actual, identity.expected)
		}
	}
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
