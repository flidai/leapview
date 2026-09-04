package cli

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/digest"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	projectdevloop "github.com/flidai/leapview/internal/project/devloop"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

type candidateSynchronizationTransport struct {
	client          *deploymentgen.GenClient
	principalClient *accessgen.GenClient
	canonicalOrigin string
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
	nativeTransport := newCandidateSynchronizationTransport(deploymentgen.NewGenClient(generic))
	nativeTransport.principalClient = accessgen.NewGenClient(generic)
	nativeTransport.canonicalOrigin = credentials.CanonicalOrigin
	transport := newProjectDevSynchronizationTransport(nativeTransport)
	return projectdevloop.NewTransportRemote(
		transport,
		uploadConcurrency,
	)
}

func newProjectDevSynchronizationTransport(native *candidateSynchronizationTransport) projectdevloop.SynchronizationTransport {
	return native
}

func newCandidateSynchronizationTransport(
	client *deploymentgen.GenClient,
) *candidateSynchronizationTransport {
	return &candidateSynchronizationTransport{
		client: client,
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
		"candidate-plan", request.ProjectID.String(), "", body,
	)
	if err != nil {
		return projectdevloop.SynchronizationPlan{}, err
	}
	request.IdempotencyKey = idempotencyKey
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
		PlanID:         response.Body.PlanId,
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
				ContentType:               "application/octet-stream",
				ContentDigest:             standardCandidateContentDigest(artifact.Digest),
				SourceSynchronizationPlan: request.PlanID,
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

// SynchronizeNative captures the source through candidate-sync's source-only
// protocol, then hands that retained source to the canonical delivery plan and
// build APIs.
func (transport *candidateSynchronizationTransport) SynchronizeNative(
	ctx context.Context,
	request projectdevloop.SyncRequest,
	maxParallelUploads int,
) (projectdevloop.Candidate, error) {
	if transport == nil || transport.client == nil || transport.principalClient == nil {
		return projectdevloop.Candidate{}, fmt.Errorf("native delivery client is not configured")
	}
	if maxParallelUploads < 1 || maxParallelUploads > 16 {
		return projectdevloop.Candidate{}, fmt.Errorf("native delivery requires 1-16 parallel uploads")
	}

	// RetainSource performs Plan -> Upload -> RetainSource with SourceOnly set
	// on the synchronization request. Reusing it keeps source attestation and
	// target-requested content handling identical to the standalone plan CLI.
	remote, err := projectdevloop.NewTransportRemote(transport, maxParallelUploads)
	if err != nil {
		return projectdevloop.Candidate{}, err
	}
	retained, err := remote.RetainSource(ctx, request.Snapshot)
	if err != nil {
		return projectdevloop.Candidate{}, fmt.Errorf("retain project source for native delivery: %w", err)
	}
	projectID := retained.ProjectID.String()
	if projectID == "" || retained.TargetID == "" || retained.Environment == "" ||
		retained.SourceDigest == "" || retained.SourceAttestationDigest == "" {
		return projectdevloop.Candidate{}, fmt.Errorf("native source retention returned incomplete identity")
	}
	principalResponse, err := transport.principalClient.GetCurrentPrincipal(ctx, accessgen.GenGetCurrentPrincipalClientRequest{})
	if err != nil {
		return projectdevloop.Candidate{}, fmt.Errorf("resolve native delivery principal: %w", err)
	}
	ownerID := strings.TrimSpace(principalResponse.Body.Id)
	if ownerID == "" || ownerID != principalResponse.Body.Id {
		return projectdevloop.Candidate{}, fmt.Errorf("native delivery principal response has no canonical identity")
	}

	planKey := deploymentIdempotencyKey(
		"dev-delivery-plan", projectID, retained.TargetID, retained.Environment,
		ownerID, request.Snapshot.CandidateKey, retained.SourceDigest,
		retained.SourceAttestationDigest,
	)
	planResponse, err := transport.client.CreateDeliveryPlan(
		ctx,
		deploymentgen.GenCreateDeliveryPlanClientRequest{
			Project: projectID,
			Headers: deploymentgen.GenCreateDeliveryPlanClientHeaders{IdempotencyKey: planKey},
			Body: deploymentgen.DeliveryPlanRequest{
				TargetId:                retained.TargetID,
				Operation:               deploymentgen.DeliveryOperationKindCodeChange,
				SourceDigest:            retained.SourceDigest,
				SourceAttestationDigest: retained.SourceAttestationDigest,
			},
		},
	)
	if err != nil {
		return projectdevloop.Candidate{}, fmt.Errorf("create native delivery plan: %w", err)
	}
	plan := planResponse.Body
	if plan.Id == "" || plan.Status != deploymentgen.DeliveryPlanStatusPlanned || plan.Operation != deploymentgen.DeliveryOperationKindCodeChange || plan.ProjectId != projectID || plan.TargetId != retained.TargetID ||
		plan.Environment != retained.Environment || plan.SourceDigest != retained.SourceDigest ||
		plan.SourceAttestationDigest != retained.SourceAttestationDigest || plan.PlanDigest == "" ||
		plan.ExecutionDigest == "" || plan.EvidenceDigest == "" || plan.ProvenanceDigest == "" {
		return projectdevloop.Candidate{}, fmt.Errorf("native delivery plan response does not match retained source")
	}
	for name, value := range map[string]string{
		"plan":       plan.PlanDigest,
		"execution":  plan.ExecutionDigest,
		"evidence":   plan.EvidenceDigest,
		"provenance": plan.ProvenanceDigest,
	} {
		if err := digest.ValidateSHA256Identity(value); err != nil {
			return projectdevloop.Candidate{}, fmt.Errorf("native delivery plan %s digest is invalid: %w", name, err)
		}
	}
	planID, err := canonicalNativeUUID(plan.Id, "plan")
	if err != nil {
		return projectdevloop.Candidate{}, err
	}

	buildKey := deploymentIdempotencyKey(
		"dev-delivery-build", projectID, ownerID, planID, plan.PlanDigest,
	)
	buildResponse, err := transport.client.BuildDeliveryPlan(
		ctx,
		deploymentgen.GenBuildDeliveryPlanClientRequest{
			Project: projectID,
			Plan:    planID,
			Headers: deploymentgen.GenBuildDeliveryPlanClientHeaders{IdempotencyKey: buildKey},
		},
	)
	if err != nil {
		return projectdevloop.Candidate{}, fmt.Errorf("build native delivery plan: %w", err)
	}
	build := buildResponse.Body
	if build.PlanId != planID || build.Status != deploymentgen.DeliveryBuildStatusSealed ||
		build.PlanDigest != plan.PlanDigest || build.SourceDigest != retained.SourceDigest ||
		build.ExecutionDigest != plan.ExecutionDigest {
		return projectdevloop.Candidate{}, fmt.Errorf("native delivery build response does not match plan")
	}
	if _, err := canonicalNativeUUID(build.Id, "build"); err != nil {
		return projectdevloop.Candidate{}, err
	}
	if build.CandidateId == nil || strings.TrimSpace(*build.CandidateId) == "" {
		return projectdevloop.Candidate{}, fmt.Errorf("native delivery build returned no candidate identity")
	}
	candidateID, err := canonicalNativeUUID(*build.CandidateId, "candidate")
	if err != nil {
		return projectdevloop.Candidate{}, err
	}
	if build.SnapshotSealId == nil || strings.TrimSpace(*build.SnapshotSealId) == "" {
		return projectdevloop.Candidate{}, fmt.Errorf("native delivery build returned no seal identity")
	}
	if _, err := canonicalNativeUUID(*build.SnapshotSealId, "seal"); err != nil {
		return projectdevloop.Candidate{}, err
	}
	previewURL, err := nativeCandidatePreviewURL(transport.canonicalOrigin, candidateID)
	if err != nil {
		return projectdevloop.Candidate{}, err
	}
	if build.CandidateRevision == nil || *build.CandidateRevision <= 0 {
		return projectdevloop.Candidate{}, fmt.Errorf("native delivery build returned invalid candidate revision")
	}
	return projectdevloop.Candidate{
		ID:               candidateID,
		ProjectID:        retained.ProjectID,
		OwnerID:          ownerID,
		ArtifactDigest:   retained.SourceDigest,
		PreviewURL:       previewURL,
		TargetID:         retained.TargetID,
		Environment:      retained.Environment,
		ProvenanceDigest: plan.ProvenanceDigest,
		Revision:         *build.CandidateRevision,
		PlanID:           planID,
		PlanDigest:       plan.PlanDigest,
		ExecutionDigest:  plan.ExecutionDigest,
		EvidenceDigest:   plan.EvidenceDigest,
	}, nil
}

func nativeCandidatePreviewURL(origin, candidateID string) (string, error) {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") {
		return "", fmt.Errorf("resolved target has no canonical HTTP origin")
	}
	candidateID = strings.TrimSpace(candidateID)
	if candidateID == "" {
		return "", fmt.Errorf("native delivery candidate identity is missing")
	}
	return parsed.Scheme + "://" + parsed.Host + "/candidates/" + url.PathEscape(candidateID), nil
}

func canonicalNativeUUID(value, name string) (string, error) {
	original := value
	value = strings.TrimSpace(value)
	parsed, err := uuid.Parse(value)
	if original != value || err != nil || parsed == uuid.Nil || parsed.String() != value {
		return "", fmt.Errorf("native delivery %s identity is not a canonical UUID", name)
	}
	return value, nil
}

func (transport *candidateSynchronizationTransport) RetainSource(
	ctx context.Context,
	request projectdevloop.SynchronizationPlanRequest,
) (projectdevloop.RetainedSource, error) {
	if transport == nil || transport.client == nil {
		return projectdevloop.RetainedSource{}, fmt.Errorf("candidate synchronization client is not configured")
	}
	body := candidateSynchronizationBody(request)
	idempotencyKey, err := candidateSynchronizationIdempotencyKey("source-retain", request.ProjectID.String(), request.PlanID, body)
	if err != nil {
		return projectdevloop.RetainedSource{}, err
	}
	response, err := transport.client.RetainProjectCandidateSource(ctx, deploymentgen.GenRetainProjectCandidateSourceClientRequest{
		Project: request.ProjectID.String(),
		Headers: deploymentgen.GenRetainProjectCandidateSourceClientHeaders{IdempotencyKey: idempotencyKey, SourceSynchronizationPlan: request.PlanID},
		Body:    body,
	})
	if err != nil {
		return projectdevloop.RetainedSource{}, err
	}
	bodyValue := response.Body
	projectID := strings.TrimSpace(request.ProjectID.String())
	if bodyValue.ProjectId != projectID || bodyValue.ProjectId != strings.TrimSpace(bodyValue.ProjectId) {
		return projectdevloop.RetainedSource{}, fmt.Errorf("retained source project identity does not match request")
	}
	retainedProjectID, err := projectgraph.NewResourceID(bodyValue.ProjectId)
	if err != nil || retainedProjectID != request.ProjectID {
		return projectdevloop.RetainedSource{}, fmt.Errorf("retained source project identity is invalid")
	}
	if bodyValue.SourceDigest != request.ArtifactDigest || bodyValue.SourceDigest != strings.TrimSpace(bodyValue.SourceDigest) {
		return projectdevloop.RetainedSource{}, fmt.Errorf("retained source digest does not match request")
	}
	for name, value := range map[string]string{
		"source":      bodyValue.SourceDigest,
		"project":     bodyValue.ProjectDigest,
		"attestation": bodyValue.SourceAttestationDigest,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return projectdevloop.RetainedSource{}, fmt.Errorf("retained source %s digest is incomplete", name)
		}
		if err := digest.ValidateSHA256Identity(value); err != nil {
			return projectdevloop.RetainedSource{}, fmt.Errorf("retained source %s digest is invalid: %w", name, err)
		}
	}
	if bodyValue.TargetId == "" || bodyValue.TargetId != strings.TrimSpace(bodyValue.TargetId) ||
		bodyValue.Environment == "" || bodyValue.Environment != strings.TrimSpace(bodyValue.Environment) {
		return projectdevloop.RetainedSource{}, fmt.Errorf("retained source target identity is incomplete")
	}
	return projectdevloop.RetainedSource{ProjectID: retainedProjectID, SourceDigest: bodyValue.SourceDigest, SourceAttestationDigest: bodyValue.SourceAttestationDigest, ProjectDigest: bodyValue.ProjectDigest, TargetID: bodyValue.TargetId, Environment: bodyValue.Environment}, nil
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
	for index, artifact := range request.Artifacts {
		body.Artifacts[index] = deploymentgen.CandidateSourceArtifact{
			Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes,
		}
	}
	return body
}

func candidateSynchronizationIdempotencyKey(
	kind,
	projectID,
	planID string,
	body deploymentgen.CandidateSynchronizationRequest,
) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode candidate synchronization idempotency identity: %w", err)
	}
	return deploymentIdempotencyKey(
		kind,
		projectID,
		planID,
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
