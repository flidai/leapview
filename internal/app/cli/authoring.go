package cli

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/app/cli/localdocker"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/digest"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	developmentsession "github.com/flidai/leapview/internal/project/developmentsession"
	developmenthttpstore "github.com/flidai/leapview/internal/project/developmentsession/httpstore"
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
	client                 cliapi.Client
	stageDevelopmentInputs func(context.Context, cliapi.Credentials, localDevelopmentSession) error
	bootstrapOwnerPolicy   func(context.Context, apigenclient.Transport, localDevelopmentSession) error
}

type localDevelopmentSession struct {
	profile localDevelopmentProfile
	state   localruntime.State
	output  io.Writer
}

type localDevelopmentSessionContextKey struct{}

type profileApplyingDevRemote struct {
	remote projectdevloop.Remote
	client *analyticsgen.GenClient
	local  localDevelopmentSession
}

func (remote *profileApplyingDevRemote) Synchronize(ctx context.Context, request projectdevloop.SyncRequest) (projectdevloop.Candidate, error) {
	if remote == nil || remote.remote == nil || remote.client == nil {
		return projectdevloop.Candidate{}, errors.New("development profile application transport is unavailable")
	}
	profile := remote.local.profile
	if request.Snapshot.GraphDigest == "" || request.Snapshot.GraphDigest != profile.GraphDigest {
		return projectdevloop.Candidate{}, errors.New("compiled graph differs from the local runtime profile identity; run `leapview dev reset`, review the profile, and start again")
	}
	projectID := request.Snapshot.ProjectID.String()
	targetID := remote.local.state.Authority.InstanceID
	if targetID == "" {
		return projectdevloop.Candidate{}, errors.New("local runtime target identity is unavailable")
	}
	mode := analyticsgen.DevelopmentProfileApplicationModeNew
	applicationID := developmentProfileApplicationID(remote.local)
	current, err := remote.client.GetDevelopmentProfileApplication(ctx, analyticsgen.GenGetDevelopmentProfileApplicationClientRequest{Project: projectID, Target: targetID})
	if err == nil {
		if current.Body.GraphDigest != profile.GraphDigest || current.Body.ProfileDigest != profile.Profile.ProfileDigest {
			return projectdevloop.Candidate{}, errors.New("retained development profile differs from this runtime; run `leapview dev reset`, review the profile, and start again")
		}
		applicationID = current.Body.ApplicationId
		mode = analyticsgen.DevelopmentProfileApplicationModeResume
	} else if !isDevelopmentProfileNotFound(err) {
		return projectdevloop.Candidate{}, fmt.Errorf("read retained development profile application: %w", err)
	}
	body := analyticsgen.DevelopmentProfileApplicationRequest{
		ApplicationId: applicationID, Mode: mode, SourceDigest: request.Snapshot.Digest,
		GraphDigest: profile.GraphDigest, ProfileDigest: profile.Profile.ProfileDigest,
		Connections: developmentProfileConnectionIntents(projectID, remote.local.state.Authority.Environment, profile),
	}
	response, err := remote.client.ApplyDevelopmentProfile(ctx, analyticsgen.GenApplyDevelopmentProfileClientRequest{
		Project: projectID, Target: targetID,
		Headers: analyticsgen.GenApplyDevelopmentProfileClientHeaders{IdempotencyKey: developmentProfileIdempotencyKey(applicationID, mode)},
		Body:    body,
	})
	if err != nil {
		return projectdevloop.Candidate{}, fmt.Errorf("apply development profile: %w", err)
	}
	if response.Body.Status != analyticsgen.DevelopmentProfileApplicationStatusApplied || response.Body.GraphDigest != profile.GraphDigest || response.Body.ProfileDigest != profile.Profile.ProfileDigest {
		return projectdevloop.Candidate{}, errors.New("local runtime did not acknowledge the exact applied development profile")
	}
	return remote.remote.Synchronize(ctx, request)
}

func developmentProfileApplicationID(local localDevelopmentSession) string {
	identity := local.state.Checkout.ID + "\x00" + local.state.Runtime.OwnerID + "\x00" + local.profile.Profile.ProfileDigest
	return "profile_" + uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String()
}

func developmentProfileIdempotencyKey(applicationID string, mode analyticsgen.DevelopmentProfileApplicationMode) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview:development-profile:"+applicationID+"\x00"+string(mode))).String()
}

func isDevelopmentProfileNotFound(err error) bool {
	var problem *apigenclient.ProblemError
	return errors.As(err, &problem) && problem.Response.StatusCode == http.StatusNotFound
}

func developmentProfileConnectionIntents(projectID, environment string, profile localDevelopmentProfile) []analyticsgen.DevelopmentProfileConnectionIntent {
	connections := make([]analyticsgen.DevelopmentProfileConnectionIntent, len(profile.Profile.Connections))
	for index, connection := range profile.Profile.Connections {
		endpoint := analyticsgen.TargetConnectionEndpoint{
			Host: optionalAuthoringString(connection.Endpoint.Host), Database: optionalAuthoringString(connection.Endpoint.Database),
			ObjectScope: optionalAuthoringString(connection.Endpoint.ObjectScope), SourceIdentity: optionalAuthoringString(connection.Endpoint.SourceIdentity),
			TlsMode: optionalAuthoringString(connection.Endpoint.TLSMode),
		}
		if connection.Endpoint.Port != 0 {
			port := int32(connection.Endpoint.Port)
			endpoint.Port = &port
		}
		if len(connection.Endpoint.Options) > 0 {
			options := make(map[string]string, len(connection.Endpoint.Options))
			for key, value := range connection.Endpoint.Options {
				options[key] = value
			}
			endpoint.Options = &options
		}
		intent := analyticsgen.DevelopmentProfileConnectionIntent{
			LogicalConnection: connection.ID.String(), ConnectorKind: connection.ConnectorKind,
			AuthenticationMode: analyticsgen.TargetConnectionAuthenticationModeNone, Endpoint: endpoint,
		}
		if variable := connection.Credentials.EnvironmentVariable; variable != "" {
			intent.AuthenticationMode = analyticsgen.TargetConnectionAuthenticationModeExternalBundle
			intent.CredentialReference = &analyticsgen.TargetConnectionCredentialReference{ProjectId: projectID, Environment: environment, SecretPath: "/", SecretKey: variable}
		}
		connections[index] = intent
	}
	return connections
}

func optionalAuthoringString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func devCommand(ctx context.Context) *cobra.Command {
	client := capabilityAPIClient{
		httpClient:        authoringRefreshingHTTPClient(http.DefaultClient),
		validateAuthoring: true,
	}
	remotes := projectDevRemoteFactory{client: client, stageDevelopmentInputs: stageDeclaredDevelopmentInputs}
	remote := projectcli.DevCommand(
		ctx,
		client,
		projectcli.NewCandidateCheckpointStore(candidateCheckpointPath()),
		remotes,
		openSystemBrowser,
		projectDeliveryPlanOperations{client: client, remotes: remotes, checkpoints: projectcli.NewCandidateCheckpointStore(candidateCheckpointPath())},
	)
	command := dispatchLocalDevCommand(ctx, remote, localdocker.Resolve, runLocalDevRuntime)
	addLocalDevLifecycleCommands(ctx, command, localdocker.Resolve, newLocalRuntimeController, readApplicationDevelopmentProfileStatus)
	return command
}

type localDockerResolver func(context.Context, localdocker.Options) (localdocker.Endpoint, error)
type localDevRuntime func(context.Context, localdocker.Endpoint, *cobra.Command, []string, func(*cobra.Command, []string) error) error

// dispatchLocalDevCommand makes explicit --target the only route to the
// existing remote workflow. Bare dev is decided here, before target profiles,
// stored logins, or LEAPVIEW_TARGET can be resolved by the remote command.
func dispatchLocalDevCommand(
	ctx context.Context,
	command *cobra.Command,
	resolve localDockerResolver,
	start localDevRuntime,
) *cobra.Command {
	remoteRun := command.RunE
	var dockerContext, dockerHost, profileFile, profileName string
	var allowUpstreamRead bool
	command.Flags().StringVar(&dockerContext, "docker-context", "", "explicit local Docker context")
	command.Flags().StringVar(&dockerHost, "docker-host", "", "explicit local Docker Unix socket")
	command.Flags().StringVar(&profileFile, "profile-file", "", "explicit local development profile file")
	command.Flags().StringVar(&profileName, "profile", "", "local development profile name")
	command.Flags().BoolVar(&allowUpstreamRead, "allow-upstream-read", false, "consent to profile connection tests and approved upstream reads")
	command.RunE = func(command *cobra.Command, args []string) error {
		if command.Flags().Changed("target") {
			target, err := command.Flags().GetString("target")
			if err != nil {
				return err
			}
			if strings.TrimSpace(target) == "" {
				return fmt.Errorf("explicit remote --target must not be empty")
			}
			if command.Flags().Changed("docker-context") || command.Flags().Changed("docker-host") {
				return fmt.Errorf("Docker endpoint flags cannot be combined with remote --target")
			}
			if command.Flags().Changed("profile-file") || command.Flags().Changed("profile") || command.Flags().Changed("allow-upstream-read") {
				return fmt.Errorf("local profile flags cannot be combined with remote --target")
			}
			return remoteRun(command, args)
		}
		for _, name := range []string{"token", "project-id", "bootstrap"} {
			if flag := command.Flags().Lookup(name); flag != nil && command.Flags().Changed(name) {
				return fmt.Errorf("--%s requires an explicit remote --target", name)
			}
		}
		if len(args) == 1 {
			if flag := command.Flags().Lookup("source-root"); flag != nil && command.Flags().Changed("source-root") {
				return fmt.Errorf("choose either --source-root or positional source root, not both")
			}
		}
		if resolve == nil || start == nil {
			return fmt.Errorf("local development runtime is not configured")
		}
		endpoint, err := resolve(ctx, localdocker.Options{
			ExplicitContext: dockerContext,
			ExplicitHost:    dockerHost,
		})
		if err != nil {
			return err
		}
		return start(ctx, endpoint, command, args, remoteRun)
	}
	return command
}

func runLocalDevRuntime(
	ctx context.Context,
	endpoint localdocker.Endpoint,
	command *cobra.Command,
	args []string,
	remoteRun func(*cobra.Command, []string) error,
) error {
	profile, err := prepareLocalDevelopmentProfile(command, args)
	if err != nil {
		return err
	}
	if err := reportLocalDevelopmentProfile(command, profile); err != nil {
		return err
	}
	allowUpstreamRead, err := command.Flags().GetBool("allow-upstream-read")
	if err != nil {
		return err
	}
	if len(profile.Profile.Connections) > 0 && !allowUpstreamRead {
		return errors.New("development profile connection testing can contact upstream systems; review the summary and pass --allow-upstream-read to consent")
	}
	controller, err := newLocalRuntimeControllerForProfile(endpoint, command, profile.Credentials, localruntime.DevelopmentProfileIdentity{
		Name: profile.Profile.ProfileName, GraphDigest: profile.GraphDigest, ProfileDigest: profile.Profile.ProfileDigest,
	})
	if err != nil {
		return err
	}
	if remoteRun == nil {
		return fmt.Errorf("local development synchronization is not configured")
	}
	return controller.RunAction(ctx, func(actionContext context.Context, state localruntime.State) error {
		if strings.TrimSpace(state.Session.TargetName) == "" {
			return fmt.Errorf("local runtime did not establish an authoring target")
		}
		if err := command.Flags().Set("target", state.Session.TargetName); err != nil {
			return err
		}
		actionContext = context.WithValue(actionContext, localDevelopmentSessionContextKey{}, localDevelopmentSession{profile: profile, state: state, output: command.OutOrStdout()})
		command.SetContext(actionContext)
		return remoteRun(command, args)
	})
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
	remote, err := projectdevloop.NewTransportRemote(
		transport,
		uploadConcurrency,
	)
	if err != nil {
		return nil, err
	}
	local, localDevelopment := ctx.Value(localDevelopmentSessionContextKey{}).(localDevelopmentSession)
	if !localDevelopment {
		return remote, nil
	}
	bootstrap := factory.bootstrapOwnerPolicy
	if bootstrap == nil {
		bootstrap = bootstrapLocalOwnerPolicy
	}
	if err := bootstrap(ctx, generic, local); err != nil {
		return nil, fmt.Errorf("bootstrap local Project authorization policy: %w", err)
	}
	if factory.stageDevelopmentInputs != nil {
		if err := factory.stageDevelopmentInputs(ctx, credentials, local); err != nil {
			return nil, fmt.Errorf("stage declared development inputs: %w", err)
		}
	}
	return &profileApplyingDevRemote{remote: remote, client: analyticsgen.NewGenClient(generic), local: local}, nil
}

func bootstrapLocalOwnerPolicy(ctx context.Context, transport apigenclient.Transport, local localDevelopmentSession) error {
	client := accessgen.NewGenClient(transport)
	principal, err := client.GetCurrentPrincipal(ctx, accessgen.GenGetCurrentPrincipalClientRequest{})
	if err != nil {
		return fmt.Errorf("resolve local Project owner: %w", err)
	}
	_, _, err = bootstrapProjectOwnerPolicy(
		ctx, client, local.state.Authority.InstanceID,
		local.state.Authority.ProjectUID, local.state.Authority.Environment,
		strings.TrimSpace(principal.Body.Id),
	)
	return err
}

// DevelopmentSession supplies the durable local pointer only for the local
// runtime path. The canonical checkout ID is also the worktree identity; the
// local runtime owner is used solely as the authenticated owner component.
func (factory projectDevRemoteFactory) DevelopmentSession(ctx context.Context, credentials cliapi.Credentials) (*projectcli.DevSessionBinding, error) {
	local, ok := ctx.Value(localDevelopmentSessionContextKey{}).(localDevelopmentSession)
	if !ok {
		return nil, nil
	}
	if factory.client == nil {
		return nil, errors.New("development session client is unavailable")
	}
	transport, err := factory.client.Transport(ctx, credentials)
	if err != nil {
		return nil, err
	}
	principalResponse, err := accessgen.NewGenClient(transport).GetCurrentPrincipal(ctx, accessgen.GenGetCurrentPrincipalClientRequest{})
	if err != nil {
		return nil, fmt.Errorf("resolve development session owner: %w", err)
	}
	ownerID := strings.TrimSpace(principalResponse.Body.Id)
	checkoutID := strings.TrimSpace(local.state.Checkout.ID)
	targetID := strings.TrimSpace(local.state.Authority.InstanceID)
	environment := strings.TrimSpace(local.state.Authority.Environment)
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(credentials.ProjectID))
	if err != nil || ownerID == "" || checkoutID == "" || targetID == "" || environment == "" {
		return nil, fmt.Errorf("local development session identity is incomplete")
	}
	key := developmentsession.Key{OwnerID: ownerID, CheckoutID: checkoutID, WorktreeID: checkoutID, ProjectID: projectID, TargetID: targetID, Environment: environment}
	httpClient := http.DefaultClient
	if provider, ok := factory.client.(interface{ HTTPClient() *http.Client }); ok && provider.HTTPClient() != nil {
		httpClient = provider.HTTPClient()
	}
	origin := strings.TrimRight(strings.TrimSpace(credentials.CanonicalOrigin), "/")
	if origin == "" {
		origin = strings.TrimRight(strings.TrimSpace(credentials.Target), "/")
	}
	store, err := developmenthttpstore.New(httpClient, origin, credentials.Token, key)
	if err != nil {
		return nil, err
	}
	return &projectcli.DevSessionBinding{Store: store, Key: key}, nil
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
		ArtifactDigest: request.ArtifactDigest,
		Artifacts:      make([]deploymentgen.CandidateSourceArtifact, len(request.Artifacts)),
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
