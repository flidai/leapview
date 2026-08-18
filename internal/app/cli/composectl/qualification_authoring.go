package composectl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	"github.com/flidai/leapview/internal/app/api/clienttransport"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
)

const qualificationBrowserImage = "mcr.microsoft.com/playwright:v1.61.1-noble"

const (
	qualificationReviewerEmail       = "authoring-reviewer@qualification.invalid"
	qualificationReviewerPrincipalID = "email_6f53ef0fb859d8d683ad4bb70d2e693b"
)

type qualificationAuthoringOptions struct {
	BundleRoot      string
	Image           string
	ClientBaseImage string
	CredentialsFile string
	ComposeProject  string
	EvidenceDir     string
	SourceRevision  string
	Target          string
	Project         string
	ProjectID       string
	Environment     string
}

type qualificationCredentials struct {
	Email                 string `json:"email"`
	TemporaryPassword     string `json:"temporaryPassword"`
	PublisherToken        string `json:"publisherToken"`
	PublisherTokenExpires string `json:"publisherTokenExpiresAt"`
	WorkloadToken         string `json:"workloadToken,omitempty"`
	ProjectDataToken      string `json:"projectDataToken,omitempty"`
	RecoveryControlToken  string `json:"recoveryControlToken,omitempty"`
	QualificationPassword string `json:"qualificationPassword"`
}

func (credentials qualificationCredentials) workloadToken() (string, error) {
	token := strings.TrimSpace(credentials.WorkloadToken)
	if token == "" {
		return "", fmt.Errorf("dedicated qualification workload token is required")
	}
	return token, nil
}

func (credentials qualificationCredentials) projectDataToken() (string, error) {
	token := strings.TrimSpace(credentials.ProjectDataToken)
	if token == "" {
		return "", fmt.Errorf("dedicated qualification project-data token is required")
	}
	return token, nil
}

func (credentials qualificationCredentials) recoveryControlToken() (string, error) {
	token := strings.TrimSpace(credentials.RecoveryControlToken)
	if token == "" {
		return "", fmt.Errorf("dedicated qualification recovery-control token is required")
	}
	return token, nil
}

func qualificationWorkloadCapabilities() []string {
	return []string{
		"RESOURCE_USE",
		"RESOURCE_READ",
		"RESOURCE_EDIT",
		"RESOURCE_PUBLISH",
	}
}

func qualificationProjectDataCapabilities() []string {
	return []string{"RESOURCE_READ"}
}

func qualificationReviewerCapabilities() []string {
	return []string{"PROJECT_ADMIN"}
}

type qualificationAuthoringReport struct {
	SchemaVersion  int                          `json:"schemaVersion"`
	Result         string                       `json:"result"`
	Target         string                       `json:"target"`
	Candidate      string                       `json:"candidate"`
	Revision       int64                        `json:"revision"`
	SourceArtifact string                       `json:"sourceArtifact"`
	Artifact       string                       `json:"artifact"`
	ReleaseDigest  string                       `json:"releaseDigest"`
	Principal      string                       `json:"principal"`
	SourceRevision string                       `json:"sourceRevision"`
	Phases         []qualificationPhaseEvidence `json:"phases"`
	Assertions     struct {
		BrowserApprovedLogin    bool `json:"browserApprovedLogin"`
		NativeKeyring           bool `json:"nativeKeyring"`
		PrivatePreview          bool `json:"privatePreview"`
		ExactCandidateActivated bool `json:"exactCandidateActivated"`
	} `json:"assertions"`
}

type qualificationBrowserToken struct {
	AccessToken string `json:"accessToken"`
}

func (c *Controller) runQualificationAuthoring(
	ctx context.Context,
	options qualificationAuthoringOptions,
) (report qualificationAuthoringReport, runErr error) {
	rootContext := ctx
	options = normalizeQualificationAuthoringOptions(options)
	if err := validateQualificationAuthoringOptions(options); err != nil {
		return report, err
	}
	var credentials qualificationCredentials
	if err := readQualificationJSON(options.CredentialsFile, &credentials); err != nil {
		return report, err
	}
	if credentials.Email == "" || credentials.TemporaryPassword == "" ||
		credentials.QualificationPassword == "" {
		return report, fmt.Errorf("authoring qualification credentials are incomplete")
	}
	if err := os.MkdirAll(options.EvidenceDir, 0o700); err != nil {
		return report, err
	}
	workDir := filepath.Join(options.EvidenceDir, ".authoring-work")
	if err := os.RemoveAll(workDir); err != nil {
		return report, err
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return report, err
	}
	report.SchemaVersion = qualificationEvidenceSchema
	report.Result = "failure"
	phases := newQualificationPhaseTracker(c.now)
	cleanup := qualificationCleanup{}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), qualificationCleanupTimeout)
		defer cancel()
		runErr = joinQualificationError(runErr, cleanup.Run(cleanupCtx))
		runErr = phases.Finish(runErr)
		if runErr != nil {
			report.Result = "failure"
		}
		report.Phases = phases.Evidence()
		if runErr != nil {
			_ = writeQualificationJSON(
				filepath.Join(options.EvidenceDir, "authoring-report.json"),
				report,
			)
		}
	}()
	cleanup.Add(func(context.Context) error { return os.RemoveAll(workDir) })

	ctx = phases.Begin(rootContext, "initial serving bootstrap", 20*time.Minute)
	if strings.TrimSpace(credentials.PublisherToken) == "" {
		return report, fmt.Errorf("initial qualification publisher token is required")
	}
	if err := c.bootstrapQualificationServingGeneration(
		ctx,
		options,
		credentials.PublisherToken,
	); err != nil {
		return report, err
	}
	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	ctx = phases.Begin(rootContext, "browser and client setup", 15*time.Minute)

	runSuffix := normalizedQualificationName(
		options.ComposeProject + "-" + strconv.Itoa(os.Getpid()),
	)
	clientImage := "leapview-authoring-client:" + runSuffix
	clientContainer := "leapview-authoring-client-" + runSuffix
	browserContainer := "leapview-authoring-browser-" + runSuffix
	certificateFile := filepath.Join(workDir, "caddy-root.crt")

	caddyOutput, err := c.qualificationCompose(ctx, options.BundleRoot, "ps", "--quiet", "caddy")
	if err != nil {
		return report, err
	}
	caddyContainer := strings.TrimSpace(string(caddyOutput))
	if caddyContainer == "" {
		return report, fmt.Errorf("qualification Caddy container is not running")
	}
	certCtx, cancelCert := qualificationContext(ctx, time.Minute)
	err = qualificationWait(certCtx, time.Second, func(waitCtx context.Context) (bool, error) {
		_, copyErr := c.qualificationDocker(
			waitCtx,
			nil,
			"cp",
			caddyContainer+":/data/caddy/pki/authorities/local/root.crt",
			certificateFile,
		)
		if copyErr != nil {
			return false, nil
		}
		info, statErr := os.Stat(certificateFile)
		return statErr == nil && info.Size() > 0, nil
	})
	cancelCert()
	if err != nil {
		return report, fmt.Errorf("copy qualification CA certificate: %w", err)
	}
	if err := os.Chmod(certificateFile, 0o644); err != nil {
		return report, err
	}

	qualificationRoot := filepath.Join(options.BundleRoot, "qualification")
	if _, err := c.qualificationDocker(
		ctx,
		nil,
		"build",
		"--file", filepath.Join(qualificationRoot, "Dockerfile.authoring-client"),
		"--build-arg", "LEAPVIEW_IMAGE="+options.ClientBaseImage,
		"--tag", clientImage,
		qualificationRoot,
	); err != nil {
		return report, fmt.Errorf("build qualification client: %w", err)
	}
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := c.qualificationDocker(cleanupCtx, nil, "image", "rm", "--force", clientImage)
		return err
	})
	if _, err := c.qualificationDocker(ctx, nil, "pull", qualificationBrowserImage); err != nil {
		return report, fmt.Errorf("pull qualification browser: %w", err)
	}

	browser, err := c.qualificationContainers.Start(ctx, qualificationContainerRequest{
		Name: browserContainer, Image: qualificationBrowserImage, NetworkMode: "host",
		Volumes: []qualificationContainerVolume{
			{Source: qualificationRoot, Target: "/qualification", ReadOnly: true},
			{Source: options.EvidenceDir, Target: "/evidence"},
		},
		Environment: map[string]string{
			"QUALIFICATION_URL":           options.Target,
			"QUALIFICATION_PROJECT_ID":    options.ProjectID,
			"QUALIFICATION_EVIDENCE_ROOT": "/evidence",
		},
		Command: []string{"sleep", "infinity"},
	})
	if err != nil {
		return report, fmt.Errorf("start qualification browser: %w", err)
	}
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := browser.Remove(cleanupCtx)
		return err
	})
	if _, err := browser.Exec(ctx, nil, "mkdir", "-p", "/work"); err != nil {
		return report, qualificationContainerOperationError(
			ctx, browser, "prepare authoring browser work directory", err,
		)
	}
	for _, name := range []string{"package.json", "authoring-worker.mjs"} {
		if _, err := browser.CopyTo(
			ctx,
			filepath.Join(qualificationRoot, name),
			"/work/"+name,
		); err != nil {
			return report, qualificationContainerOperationError(
				ctx, browser, "copy authoring browser asset "+name, err,
			)
		}
	}
	if _, err := browser.Exec(
		ctx, nil,
		"npm", "install", "--prefix", "/work", "--no-audit", "--no-fund", "--silent",
	); err != nil {
		return report, qualificationContainerOperationError(
			ctx, browser, "install authoring browser dependencies", err,
		)
	}
	browserWorker, err := startQualificationJSONWorker(
		rootContext,
		c.root,
		os.Environ(),
		c.dockerBin,
		"exec", "-i", browserContainer,
		"node", "/work/authoring-worker.mjs",
	)
	if err != nil {
		return report, fmt.Errorf("start qualification browser worker: %w", err)
	}
	cleanup.Add(func(context.Context) error { return browserWorker.Kill() })
	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	ctx = phases.Begin(rootContext, "reviewer provisioning", 10*time.Minute)

	var authenticated struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := browserWorker.CallContext(ctx, "signInAdministrator", map[string]string{
		"email":             credentials.Email,
		"temporaryPassword": credentials.TemporaryPassword,
		"password":          credentials.QualificationPassword,
	}, &authenticated, nil); err != nil {
		return report, err
	}
	var reviewer struct {
		Principal struct {
			Id string `json:"id"`
		} `json:"principal"`
		TemporaryPassword string `json:"temporaryPassword"`
	}
	if err := browserWorker.CallContext(ctx, "createReviewer", map[string]string{
		"email":       qualificationReviewerEmail,
		"displayName": "Authoring Qualification Reviewer",
		"principalId": qualificationReviewerPrincipalID,
	}, &reviewer, nil); err != nil {
		return report, err
	}
	if reviewer.Principal.Id == "" || reviewer.TemporaryPassword == "" {
		return report, fmt.Errorf("reviewer creation returned incomplete credentials")
	}
	var administratorToken qualificationBrowserToken
	if err := browserWorker.CallContext(ctx, "issueAdministratorToken", map[string]any{
		"capabilities": []string{"PROJECT_ADMIN", "RESOURCE_READ", "RESOURCE_PUBLISH"},
	}, &administratorToken, nil); err != nil {
		return report, err
	}
	if administratorToken.AccessToken == "" {
		return report, fmt.Errorf("browser worker returned an empty administrator token")
	}
	if err := browserWorker.CallContext(ctx, "signInReviewer", map[string]string{
		"email":             qualificationReviewerEmail,
		"temporaryPassword": reviewer.TemporaryPassword,
		"password":          credentials.QualificationPassword + "-reviewer",
	}, &authenticated, nil); err != nil {
		return report, err
	}
	var reviewerToken qualificationBrowserToken
	if err := browserWorker.CallContext(ctx, "issueReviewerToken", map[string]any{
		"capabilities": qualificationReviewerCapabilities(),
	}, &reviewerToken, nil); err != nil {
		return report, err
	}
	if reviewerToken.AccessToken == "" {
		return report, fmt.Errorf("browser worker returned an empty reviewer token")
	}
	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	apiClient := qualificationHTTPSClient()
	ctx = phases.Begin(rootContext, "native keyring login", 10*time.Minute)

	keyringPassword, err := randomHex(24)
	if err != nil {
		return report, err
	}
	clientEnvironment := append(
		os.Environ(),
		"QUALIFICATION_KEYRING_PASSWORD="+keyringPassword,
	)
	clientWorker, err := startQualificationJSONWorker(
		rootContext,
		c.root,
		clientEnvironment,
		c.dockerBin,
		"run", "--rm", "-i",
		"--name", clientContainer,
		"--network", "host",
		"--volume", certificateFile+":/run/certs/caddy-root.crt:ro",
		"--env", "QUALIFICATION_KEYRING_PASSWORD",
		"--env", "SSL_CERT_FILE=/run/certs/caddy-root.crt",
		clientImage,
		"dbus-run-session", "--",
		"/usr/local/libexec/leapviewctl",
		"qualify", "client-worker",
		"--target", options.Target,
		"--project", options.Project,
		"--source-revision", options.SourceRevision,
	)
	if err != nil {
		return report, fmt.Errorf("start qualification CLI worker: %w", err)
	}
	keyringPassword = ""
	cleanup.Add(func(context.Context) error { return clientWorker.Kill() })
	cleanup.Add(func(cleanupCtx context.Context) error {
		_, err := c.qualificationContainers.Existing(clientContainer).Remove(cleanupCtx)
		return ignoreQualificationNotFound(err)
	})

	if err := clientWorker.CallContext(
		ctx,
		"login",
		nil,
		&authenticated,
		func(event string, raw json.RawMessage) error {
			if event != "device_challenge" {
				return fmt.Errorf("unexpected CLI worker event %q", event)
			}
			var challenge qualificationLoginChallenge
			if err := json.Unmarshal(raw, &challenge); err != nil {
				return err
			}
			var authorized struct {
				Authorized bool `json:"authorized"`
			}
			return browserWorker.CallContext(ctx, "authorizeCLI", challenge, &authorized, nil)
		},
	); err != nil {
		return report, err
	}
	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	ctx = phases.Begin(rootContext, "private candidate preview", 10*time.Minute)
	var candidate QualificationCandidate
	if err := clientWorker.CallContext(ctx, "dev", nil, &candidate, nil); err != nil {
		return report, err
	}
	var preview struct {
		CandidateID       string `json:"candidateId"`
		GovernedOrderRows int    `json:"governedOrderRows"`
	}
	if err := browserWorker.CallContext(ctx, "verifyPreview", map[string]string{
		"candidateId": candidate.ID,
		"previewUrl":  candidate.PreviewURL,
	}, &preview, nil); err != nil {
		return report, err
	}
	if preview.CandidateID != candidate.ID || preview.GovernedOrderRows != 24 {
		return report, fmt.Errorf("browser verified the wrong candidate or governed row count")
	}
	if err := phases.Finish(nil); err != nil {
		return report, err
	}
	ctx = phases.Begin(rootContext, "protected publish", 15*time.Minute)
	var publication QualificationPublication
	if err := clientWorker.CallContext(ctx, "publish", nil, &publication, nil); err != nil {
		return report, err
	}
	if err := approveQualificationPublication(
		ctx,
		apiClient,
		options,
		administratorToken.AccessToken,
		reviewerToken.AccessToken,
		publication,
		runSuffix,
	); err != nil {
		return report, err
	}
	var activated QualificationPublication
	if err := clientWorker.CallContext(ctx, "publish", nil, &activated, nil); err != nil {
		return report, err
	}
	if activated.Status != "committed" {
		return report, fmt.Errorf("approved publication transitioned to %q", activated.Status)
	}
	publication, deployment, err := qualificationCanonicalPublicationEvidence(
		ctx,
		apiClient,
		options,
		administratorToken.AccessToken,
		candidate,
		publication,
		activated,
	)
	if err != nil {
		return report, err
	}
	if err := verifyExactAuthoringCandidate(candidate, publication, deployment); err != nil {
		return report, err
	}
	createAPIToken := func(name string, capabilities []string) (string, error) {
		var response struct {
			Token string `json:"token"`
		}
		err := browserWorker.CallContext(ctx, "createAdministratorAPIToken", map[string]any{
			"name": name, "capabilities": capabilities,
			"expiresAt": c.now().UTC().Add(2 * time.Hour).Format(time.RFC3339),
		}, &response, nil)
		if err != nil {
			return "", err
		}
		if response.Token == "" {
			return "", fmt.Errorf("browser worker returned an empty %s token", name)
		}
		return response.Token, nil
	}
	workloadToken, err := createAPIToken("qualification-workload", qualificationWorkloadCapabilities())
	if err != nil {
		return report, err
	}
	projectDataToken, err := createAPIToken("qualification-project-data", qualificationProjectDataCapabilities())
	if err != nil {
		return report, err
	}
	credentials.WorkloadToken = workloadToken
	credentials.ProjectDataToken = projectDataToken
	credentials.RecoveryControlToken = reviewerToken.AccessToken
	if err := writeQualificationJSON(options.CredentialsFile, credentials); err != nil {
		return report, fmt.Errorf("persist qualification scoped credentials: %w", err)
	}
	if err := phases.Finish(nil); err != nil {
		return report, err
	}

	report.Result = "success"
	report.Target = candidate.TargetID
	report.Candidate = candidate.ID
	report.Revision = candidate.Revision
	report.SourceArtifact = candidate.ArtifactDigest
	report.Artifact = publication.ArtifactDigest
	report.ReleaseDigest = candidate.ProvenanceDigest
	report.Principal = candidate.PrincipalID
	report.SourceRevision = publication.SourceRevision
	report.Assertions.BrowserApprovedLogin = true
	report.Assertions.NativeKeyring = true
	report.Assertions.PrivatePreview = true
	report.Assertions.ExactCandidateActivated = true
	report.Phases = phases.Evidence()
	if err := writeQualificationJSON(
		filepath.Join(options.EvidenceDir, "authoring-report.json"),
		report,
	); err != nil {
		return report, err
	}
	if _, err := fmt.Fprintf(
		c.stdout,
		"enterprise authoring qualification passed for candidate %s revision %d\n",
		candidate.ID,
		candidate.Revision,
	); err != nil {
		return report, err
	}
	return report, nil
}

func normalizeQualificationAuthoringOptions(options qualificationAuthoringOptions) qualificationAuthoringOptions {
	if options.ClientBaseImage == "" {
		options.ClientBaseImage = options.Image
	}
	if options.Target == "" {
		options.Target = "https://localhost"
	}
	if options.Project == "" {
		options.Project = "/app/evaluation/project/leapview.yaml"
	}
	if options.ProjectID == "" {
		options.ProjectID = "project:leapview-evaluation"
	}
	if options.Environment == "" {
		options.Environment = "evaluation"
	}
	return options
}

func validateQualificationAuthoringOptions(options qualificationAuthoringOptions) error {
	for label, value := range map[string]string{
		"bundle root":        options.BundleRoot,
		"image":              options.Image,
		"client base image":  options.ClientBaseImage,
		"credentials file":   options.CredentialsFile,
		"Compose project":    options.ComposeProject,
		"evidence directory": options.EvidenceDir,
		"target":             options.Target,
		"project":            options.Project,
		"project ID":         options.ProjectID,
		"environment":        options.Environment,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("qualification authoring %s is required", label)
		}
	}
	return nil
}

func qualificationHTTPSClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// The isolated production Compose target uses its generated local
			// Caddy CA. This client is restricted to that disposable target.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
}

func qualificationAPI(
	ctx context.Context,
	client *http.Client,
	method string,
	endpoint string,
	token string,
	body any,
	idempotencyKey string,
	result any,
) error {
	headers := make(http.Header)
	if body != nil {
		headers.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		headers.Set("Idempotency-Key", idempotencyKey)
	}
	_, err := qualificationGeneratedTransport("", token, client).DoAPIGen(
		ctx,
		apigenclient.Request{
			Method: method, Path: endpoint, Headers: headers, Body: body,
			ContentType: headers.Get("Content-Type"), Accept: "application/json",
		},
		result,
	)
	return err
}

func qualificationGeneratedTransport(
	target string,
	token string,
	client *http.Client,
) clienttransport.Transport {
	return clienttransport.Transport{
		Target: target, Token: token, Client: client,
		MaxResponseBytes: 8 << 20,
		PrepareRequest:   applyQualificationLoopbackHost,
	}
}

func qualificationCanonicalPublicationEvidence(
	ctx context.Context,
	httpClient *http.Client,
	options qualificationAuthoringOptions,
	token string,
	candidate QualificationCandidate,
	pending QualificationPublication,
	committed QualificationPublication,
) (QualificationPublication, QualificationDeployment, error) {
	client := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		options.Target,
		token,
		httpClient,
	))
	publication, err := client.GetDeliveryPublicationEvidence(
		ctx,
		deploymentgen.GenGetDeliveryPublicationEvidenceClientRequest{
			Project: options.ProjectID, Publication: pending.DeploymentID,
		},
	)
	if err != nil {
		return QualificationPublication{}, QualificationDeployment{}, err
	}
	serverPublication := publication.Body
	if serverPublication.Status != "committed" ||
		serverPublication.Id != pending.DeploymentID ||
		serverPublication.Id != committed.DeploymentID ||
		serverPublication.CandidateId != candidate.ID ||
		serverPublication.GenerationId != committed.GenerationID ||
		serverPublication.PlanId != candidate.PlanID ||
		serverPublication.PlanDigest != candidate.PlanDigest ||
		serverPublication.TargetId != candidate.TargetID {
		return QualificationPublication{}, QualificationDeployment{}, fmt.Errorf("canonical publication evidence does not match the previewed candidate")
	}
	plan, err := client.GetDeliveryPlanPreview(
		ctx,
		deploymentgen.GenGetDeliveryPlanPreviewClientRequest{
			Project: options.ProjectID, Plan: serverPublication.PlanId,
		},
	)
	if err != nil {
		return QualificationPublication{}, QualificationDeployment{}, err
	}
	if plan.Body.PlanDigest != candidate.PlanDigest ||
		plan.Body.SourceDigest != candidate.ArtifactDigest ||
		plan.Body.ProvenanceDigest != candidate.ProvenanceDigest ||
		plan.Body.TargetId != candidate.TargetID {
		return QualificationPublication{}, QualificationDeployment{}, fmt.Errorf("canonical plan evidence does not match the previewed candidate")
	}
	synchronizedCandidate, err := client.GetProjectCandidate(
		ctx,
		deploymentgen.GenGetProjectCandidateClientRequest{
			Project: options.ProjectID, Candidate: candidate.ID,
		},
	)
	if err != nil {
		return QualificationPublication{}, QualificationDeployment{}, err
	}
	serverSynchronizedCandidate := synchronizedCandidate.Body
	if serverSynchronizedCandidate.Id != candidate.ID ||
		serverSynchronizedCandidate.Revision != candidate.Revision ||
		serverSynchronizedCandidate.OwnerId != candidate.PrincipalID ||
		serverSynchronizedCandidate.TargetId != candidate.TargetID ||
		serverSynchronizedCandidate.ArtifactDigest != candidate.ArtifactDigest ||
		serverSynchronizedCandidate.ProvenanceDigest == nil ||
		*serverSynchronizedCandidate.ProvenanceDigest != candidate.ProvenanceDigest {
		return QualificationPublication{}, QualificationDeployment{}, fmt.Errorf("synchronized candidate evidence does not match the previewed candidate")
	}
	serverCandidate, err := client.GetDeliveryCandidateStatus(
		ctx,
		deploymentgen.GenGetDeliveryCandidateStatusClientRequest{
			Project: options.ProjectID, Candidate: candidate.ID,
		},
	)
	if err != nil {
		return QualificationPublication{}, QualificationDeployment{}, err
	}
	if serverCandidate.Body.Id != candidate.ID ||
		serverCandidate.Body.PlanId != candidate.PlanID ||
		serverCandidate.Body.PlanDigest != candidate.PlanDigest ||
		serverCandidate.Body.SourceDigest != candidate.ArtifactDigest ||
		serverCandidate.Body.TargetId != candidate.TargetID {
		return QualificationPublication{}, QualificationDeployment{}, fmt.Errorf("canonical candidate evidence does not match the previewed candidate")
	}
	generation, err := client.GetDeliveryGenerationStatus(
		ctx,
		deploymentgen.GenGetDeliveryGenerationStatusClientRequest{
			Project: options.ProjectID, Generation: serverPublication.GenerationId,
		},
	)
	if err != nil {
		return QualificationPublication{}, QualificationDeployment{}, err
	}
	if generation.Body.Status != "active" ||
		generation.Body.CandidateId != candidate.ID ||
		generation.Body.PlanId != candidate.PlanID ||
		generation.Body.PlanDigest != candidate.PlanDigest ||
		generation.Body.TargetId != candidate.TargetID ||
		generation.Body.ServingArtifactDigest != serverCandidate.Body.ServingArtifactDigest {
		return QualificationPublication{}, QualificationDeployment{}, fmt.Errorf("canonical generation evidence does not match the previewed candidate")
	}
	pending.ArtifactDigest = serverCandidate.Body.ServingArtifactDigest
	pending.CandidateRevision = serverSynchronizedCandidate.Revision
	pending.PrincipalID = serverSynchronizedCandidate.OwnerId
	pending.ReleaseDigest = *serverSynchronizedCandidate.ProvenanceDigest
	return pending, QualificationDeployment{
		CandidateID: candidate.ID, CandidateRevision: serverSynchronizedCandidate.Revision,
		TargetID: candidate.TargetID, PrincipalID: serverSynchronizedCandidate.OwnerId,
		ArtifactDigest: generation.Body.ServingArtifactDigest,
		ReleaseDigest:  *serverSynchronizedCandidate.ProvenanceDigest,
		GenerationID:   generation.Body.Id, PlanID: generation.Body.PlanId,
		PlanDigest: generation.Body.PlanDigest, Status: string(generation.Body.Status),
	}, nil
}

func (c *Controller) bootstrapQualificationServingGeneration(
	ctx context.Context,
	options qualificationAuthoringOptions,
	publisherToken string,
) error {
	output, err := c.qualificationCompose(
		ctx,
		options.BundleRoot,
		"ps", "--quiet", "leapview",
	)
	if err != nil {
		return err
	}
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return fmt.Errorf("qualification application container is not running")
	}
	environment := []string{
		"LEAPVIEW_API_TOKEN=" + publisherToken,
		"LEAPVIEW_TARGET=http://localhost:8080",
	}
	devOutput, err := c.qualificationContainers.Existing(containerID).Exec(
		ctx,
		nil,
		"env",
		environment[0],
		environment[1],
		"leapview", "dev", "--once", "--no-browser",
		"--bootstrap",
		"--project", options.Project,
		"--target", "http://localhost:8080",
		"--candidate-key", "qualification-serving-bootstrap",
		"--source-revision", options.SourceRevision,
		"--format", "json",
	)
	if err != nil {
		return fmt.Errorf("bootstrap qualification candidate: %w", err)
	}
	candidate, err := parseQualificationCandidate(string(devOutput), options.SourceRevision)
	if err != nil {
		return err
	}
	bootstrap := true
	client := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		options.Target,
		publisherToken,
		qualificationHTTPSClient(),
	))
	published, err := client.PublishProjectCandidate(
		ctx,
		deploymentgen.GenPublishProjectCandidateClientRequest{
			Project: options.ProjectID, Candidate: candidate.ID,
			Headers: deploymentgen.GenPublishProjectCandidateClientHeaders{
				IdempotencyKey: "qualification-serving-bootstrap-" + candidate.ID,
			},
			Body: deploymentgen.GenSchemaCandidatePublishRequest{
				Bootstrap: &bootstrap, ExpectedRevision: candidate.Revision,
				ProvenanceDigest: candidate.ProvenanceDigest,
				TargetId:         candidate.TargetID,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("publish qualification bootstrap candidate: %w", err)
	}
	deploymentID := published.Body.Id
	if deploymentID == "" {
		return fmt.Errorf("qualification bootstrap publication returned no deployment identity")
	}
	waitCtx, cancel := qualificationContext(ctx, 5*time.Minute)
	defer cancel()
	err = qualificationWait(waitCtx, 250*time.Millisecond, func(pollCtx context.Context) (bool, error) {
		current, getErr := client.GetDeployment(
			pollCtx,
			deploymentgen.GenGetDeploymentClientRequest{
				Project: options.ProjectID, Deployment: deploymentID,
			},
		)
		if getErr != nil {
			var problem *apigenclient.ProblemError
			if errors.As(getErr, &problem) && problem.Problem.Status == http.StatusServiceUnavailable {
				return false, nil
			}
			return false, getErr
		}
		switch current.Body.Status {
		case deploymentgen.DeploymentStatusActive:
			if current.Body.Evidence.CandidateId != candidate.ID ||
				current.Body.Evidence.CandidateRevision != candidate.Revision ||
				current.Body.Evidence.TargetId != candidate.TargetID {
				return false, fmt.Errorf("qualification bootstrap activated unexpected candidate evidence")
			}
			return true, nil
		case deploymentgen.DeploymentStatusQueued, deploymentgen.DeploymentStatusRunning:
			return false, nil
		case deploymentgen.DeploymentStatusCancelled, deploymentgen.DeploymentStatusFailed, deploymentgen.DeploymentStatusSuperseded:
			return false, fmt.Errorf("qualification bootstrap publication ended in %q", current.Body.Status)
		default:
			return false, fmt.Errorf("qualification bootstrap publication returned unknown status %q", current.Body.Status)
		}
	})
	return err
}

func approveQualificationPublication(
	ctx context.Context,
	client *http.Client,
	options qualificationAuthoringOptions,
	authorToken string,
	reviewerToken string,
	publication QualificationPublication,
	runSuffix string,
) error {
	author := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		options.Target,
		authorToken,
		client,
	))
	requested, err := author.RequestDeliveryPublicationApproval(
		ctx,
		deploymentgen.GenRequestDeliveryPublicationApprovalClientRequest{
			Project: options.ProjectID, Publication: publication.DeploymentID,
			Headers: deploymentgen.GenRequestDeliveryPublicationApprovalClientHeaders{
				IdempotencyKey: "authoring-request-approval-" + runSuffix,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("read canonical publication approval: %w", err)
	}
	if requested.Body.Id == "" || requested.Body.Status != "pending" ||
		requested.Body.RequestedBy != publication.PrincipalID {
		return fmt.Errorf("canonical publication approval is not pending")
	}
	reviewer := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		options.Target,
		reviewerToken,
		client,
	))
	approval, err := reviewer.ApproveDeliveryPublicationApproval(
		ctx,
		deploymentgen.GenApproveDeliveryPublicationApprovalClientRequest{
			Project: options.ProjectID, Publication: publication.DeploymentID,
			Approval: requested.Body.Id,
			Headers: deploymentgen.GenApproveDeliveryPublicationApprovalClientHeaders{
				IdempotencyKey: "authoring-approve-" + runSuffix,
			},
			Body: deploymentgen.GenSchemaDeploymentApprovalDecisionRequest{
				ExpectedRevision: requested.Body.Revision,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("approve canonical publication: %w", err)
	}
	if approval.Body.Status != "approved" {
		return fmt.Errorf("publication approval transitioned to %q", approval.Body.Status)
	}
	return nil
}

func qualificationArchitecture() string {
	return runtime.GOARCH
}
