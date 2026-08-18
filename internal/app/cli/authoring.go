package cli

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/spf13/cobra"
)

type candidateSynchronizationTransport struct {
	client    *deploymentgen.GenClient
	sessionID string
}

type projectDevRemoteFactory struct {
	client cliapi.Client
}

func devCommand(ctx context.Context) *cobra.Command {
	client := capabilityAPIClient{
		httpClient:        authoringRefreshingHTTPClient(http.DefaultClient),
		validateAuthoring: true,
	}
	return projectcli.DevCommand(
		ctx,
		client,
		projectcli.NewCandidateCheckpointStore(candidateCheckpointPath()),
		projectDevRemoteFactory{client: client},
		openSystemBrowser,
		projectDeliveryPlanOperations{client: client, remotes: projectDevRemoteFactory{client: client}, checkpoints: projectcli.NewCandidateCheckpointStore(candidateCheckpointPath())},
	)
}

func (factory projectDevRemoteFactory) Remote(
	ctx context.Context,
	credentials cliapi.Credentials,
	uploadConcurrency int,
) (projectdevloop.Remote, error) {
	if factory.client == nil {
		return nil, fmt.Errorf("Project CLI API client is required")
	}
	generic, err := factory.client.Transport(ctx, credentials)
	if err != nil {
		return nil, err
	}
	return projectdevloop.NewTransportRemote(
		newCandidateSynchronizationTransport(deploymentgen.NewGenClient(generic)),
		uploadConcurrency,
	)
}

func newCandidateSynchronizationTransport(
	client *deploymentgen.GenClient,
) *candidateSynchronizationTransport {
	return &candidateSynchronizationTransport{
		client: client, sessionID: apitransport.NewRequestID(),
	}
}

func (transport *candidateSynchronizationTransport) Plan(
	ctx context.Context,
	request projectdevloop.SynchronizationPlanRequest,
) (projectdevloop.SynchronizationPlan, error) {
	if transport == nil || transport.client == nil {
		return projectdevloop.SynchronizationPlan{}, fmt.Errorf("candidate synchronization client is not configured")
	}
	body := candidateSynchronizationBody(request)
	idempotencyKey, err := candidateSynchronizationIdempotencyKey(
		"candidate-plan", request.ProjectID.String(), transport.sessionID, body,
	)
	if err != nil {
		return projectdevloop.SynchronizationPlan{}, err
	}
	response, err := transport.client.PlanProjectCandidateSynchronization(
		ctx,
		deploymentgen.GenPlanProjectCandidateSynchronizationClientRequest{
			Project: request.ProjectID.String(),
			Headers: deploymentgen.GenPlanProjectCandidateSynchronizationClientHeaders{
				IdempotencyKey: idempotencyKey,
			},
			Body: body,
		},
	)
	if err != nil {
		return projectdevloop.SynchronizationPlan{}, err
	}
	if response.Body.ArtifactDigest != request.ArtifactDigest {
		return projectdevloop.SynchronizationPlan{}, fmt.Errorf("target synchronization plan does not match requested artifact")
	}
	return projectdevloop.SynchronizationPlan{
		MissingDigests: append([]string(nil), response.Body.MissingDigests...),
	}, nil
}

func (transport *candidateSynchronizationTransport) Upload(
	ctx context.Context,
	request projectdevloop.SynchronizationPlanRequest,
	artifact projectdevloop.Artifact,
) error {
	if transport == nil || transport.client == nil {
		return fmt.Errorf("candidate synchronization client is not configured")
	}
	response, err := transport.client.UploadProjectCandidateSourceBlob(
		ctx,
		deploymentgen.GenUploadProjectCandidateSourceBlobClientRequest{
			Project: request.ProjectID.String(), Digest: artifact.Digest,
			Headers: deploymentgen.GenUploadProjectCandidateSourceBlobClientHeaders{
				ContentType:   "application/octet-stream",
				ContentDigest: standardCandidateContentDigest(artifact.Digest),
			},
			Body: append([]byte(nil), artifact.Content...),
		},
	)
	if err != nil {
		return mapUploadProjectCandidateSourceBlobFailure(err)
	}
	if response.Body.Digest != artifact.Digest ||
		response.Body.SizeBytes != int64(len(artifact.Content)) {
		return fmt.Errorf("target source upload acknowledgement does not match artifact")
	}
	return nil
}

func (transport *candidateSynchronizationTransport) Commit(
	ctx context.Context,
	request projectdevloop.SynchronizationPlanRequest,
) (projectdevloop.Candidate, error) {
	if transport == nil || transport.client == nil {
		return projectdevloop.Candidate{}, fmt.Errorf("candidate synchronization client is not configured")
	}
	body := candidateSynchronizationBody(request)
	idempotencyKey, err := candidateSynchronizationIdempotencyKey(
		"candidate-sync", request.ProjectID.String(), transport.sessionID, body,
	)
	if err != nil {
		return projectdevloop.Candidate{}, err
	}
	response, err := transport.client.CommitProjectCandidateSynchronization(
		ctx,
		deploymentgen.GenCommitProjectCandidateSynchronizationClientRequest{
			Project: request.ProjectID.String(),
			Headers: deploymentgen.GenCommitProjectCandidateSynchronizationClientHeaders{
				IdempotencyKey: idempotencyKey,
			},
			Body: body,
		},
	)
	if err != nil {
		return projectdevloop.Candidate{}, mapCommitProjectCandidateSynchronizationFailure(err)
	}
	if response.Body.ProvenanceDigest == nil {
		return projectdevloop.Candidate{}, fmt.Errorf("target candidate is missing publication provenance")
	}
	projectID, err := projectgraph.NewResourceID(response.Body.ProjectId)
	if err != nil {
		return projectdevloop.Candidate{}, fmt.Errorf("target candidate project identity: %w", err)
	}
	return projectdevloop.Candidate{
		ID: response.Body.Id, ProjectID: projectID,
		OwnerID:          response.Body.OwnerId,
		ArtifactDigest:   response.Body.ArtifactDigest,
		PreviewURL:       response.Body.PreviewUrl,
		TargetID:         response.Body.TargetId,
		Environment:      response.Body.Environment,
		ProvenanceDigest: *response.Body.ProvenanceDigest,
		Revision:         response.Body.Revision,
	}, nil
}

func (transport *candidateSynchronizationTransport) RetainSource(
	ctx context.Context,
	request projectdevloop.SynchronizationPlanRequest,
) (projectdevloop.RetainedSource, error) {
	if transport == nil || transport.client == nil {
		return projectdevloop.RetainedSource{}, fmt.Errorf("candidate synchronization client is not configured")
	}
	body := candidateSynchronizationBody(request)
	idempotencyKey, err := candidateSynchronizationIdempotencyKey("source-retain", request.ProjectID.String(), transport.sessionID, body)
	if err != nil {
		return projectdevloop.RetainedSource{}, err
	}
	response, err := transport.client.RetainProjectCandidateSource(ctx, deploymentgen.GenRetainProjectCandidateSourceClientRequest{
		Project: request.ProjectID.String(),
		Headers: deploymentgen.GenRetainProjectCandidateSourceClientHeaders{IdempotencyKey: idempotencyKey},
		Body:    body,
	})
	if err != nil {
		return projectdevloop.RetainedSource{}, err
	}
	return projectdevloop.RetainedSource{ProjectID: request.ProjectID, SourceDigest: response.Body.SourceDigest, SourceAttestationDigest: response.Body.SourceAttestationDigest, ProjectDigest: response.Body.ProjectDigest, TargetID: response.Body.TargetId, Environment: response.Body.Environment}, nil
}

func candidateSynchronizationBody(
	request projectdevloop.SynchronizationPlanRequest,
) deploymentgen.CandidateSynchronizationRequest {
	body := deploymentgen.CandidateSynchronizationRequest{
		ProjectFile: request.ProjectFile, ArtifactDigest: request.ArtifactDigest,
		Artifacts: make([]deploymentgen.CandidateSourceArtifact, len(request.Artifacts)),
	}
	if request.SourceOnly {
		value := true
		body.SourceOnly = &value
	}
	if request.CandidateKey != "" {
		value := request.CandidateKey
		body.CandidateKey = &value
	}
	if request.SourceRevision != nil {
		body.SourceRevision = &deploymentgen.CandidateSourceRevision{
			Revision: request.SourceRevision.Revision,
		}
		if request.SourceRevision.Repository != "" {
			value := request.SourceRevision.Repository
			body.SourceRevision.Repository = &value
		}
		if request.SourceRevision.Ref != "" {
			value := request.SourceRevision.Ref
			body.SourceRevision.Ref = &value
		}
		if request.SourceRevision.ChangeID != "" {
			value := request.SourceRevision.ChangeID
			body.SourceRevision.ChangeId = &value
		}
	}
	if request.ExpectedCandidateID != "" {
		value := request.ExpectedCandidateID
		body.ExpectedCandidateId = &value
	}
	if request.ExpectedArtifactDigest != "" {
		value := request.ExpectedArtifactDigest
		body.ExpectedArtifactDigest = &value
	}
	for index, artifact := range request.Artifacts {
		body.Artifacts[index] = deploymentgen.CandidateSourceArtifact{
			Path: artifact.Path, Digest: artifact.Digest,
		}
	}
	return body
}

func candidateSynchronizationIdempotencyKey(
	kind,
	projectID,
	sessionID string,
	body deploymentgen.CandidateSynchronizationRequest,
) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode candidate synchronization idempotency identity: %w", err)
	}
	return deploymentIdempotencyKey(
		kind,
		projectID,
		sessionID,
		string(encoded),
	), nil
}

func standardCandidateContentDigest(identity string) string {
	decoded, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(identity), "sha256:"))
	if err != nil || len(decoded) != 32 {
		return ""
	}
	return "sha-256=:" + base64.StdEncoding.EncodeToString(decoded) + ":"
}
