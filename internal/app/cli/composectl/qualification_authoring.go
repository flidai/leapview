package composectl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/app/api/clienttransport"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	platformgen "github.com/flidai/leapview/internal/platform/http/api/gen"
)

const qualificationBrowserImage = "mcr.microsoft.com/playwright:v1.61.1-noble"

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
	return []string{"RESOURCE_READ", "RESOURCE_PUBLISH"}
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
	ctx = phases.Begin(rootContext, "browser and client setup", 15*time.Minute)

	runSuffix := normalizedQualificationName(
		options.ComposeProject + "-" + strconv.Itoa(os.Getpid()),
	)
	clientImage := "leapview-authoring-client:" + runSuffix
	clientContainer := "leapview-authoring-client-" + runSuffix
	browserContainer := "leapview-authoring-browser-" + runSuffix
	certificateFile := filepath.Join(workDir, "caddy-root.crt")

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
	var administratorToken qualificationBrowserToken
	if err := browserWorker.CallContext(ctx, "issueAdministratorToken", map[string]any{
		"privileges": []string{"MANAGE_GRANTS"},
	}, &administratorToken, nil); err != nil {
		return report, err
	}
	if administratorToken.AccessToken == "" {
		return report, fmt.Errorf("browser worker returned an empty administrator token")
	}

	apiClient := qualificationHTTPSClient()
	administratorAPI := accessgen.NewGenClient(qualificationGeneratedTransport(
		options.Target,
		administratorToken.AccessToken,
		apiClient,
	))
	reviewerEmail := fmt.Sprintf("authoring-reviewer-%d@qualification.invalid", time.Now().UnixNano())
	displayName := "Authoring Qualification Reviewer"
	reviewerResponse, err := administratorAPI.CreatePrincipal(
		ctx,
		accessgen.GenCreatePrincipalClientRequest{
			Headers: accessgen.GenCreatePrincipalClientHeaders{
				IdempotencyKey: "authoring-reviewer-" + runSuffix,
			},
			Body: accessgen.GenSchemaPrincipalCreateRequest{
				Email:       reviewerEmail,
				DisplayName: &displayName,
			},
		},
	)
	if err != nil {
		return report, mapQualificationCreatePrincipalFailure(err)
	}
	reviewer := reviewerResponse.Body
	if reviewer.Principal.Id == "" || reviewer.TemporaryPassword == "" {
		return report, fmt.Errorf("reviewer creation returned incomplete credentials")
	}
	for _, capability := range qualificationReviewerCapabilities() {
		_, err := administratorAPI.CreateGrant(
			ctx,
			accessgen.GenCreateGrantClientRequest{
				Project: options.ProjectID,
				Headers: accessgen.GenCreateGrantClientHeaders{
					IdempotencyKey: "authoring-reviewer-" +
						strings.ToLower(capability) + "-" + runSuffix,
				},
				Body: accessgen.GenSchemaGrantRequest{
					Capability:   platformgen.Capability(capability),
					ResourceId:   options.ProjectID,
					ResourceKind: platformgen.ResourceKindProject,
					SubjectId:    reviewer.Principal.Id,
					SubjectType:  "principal",
				},
			},
		)
		if err != nil {
			return report, fmt.Errorf(
				"grant qualification reviewer %s: %w",
				capability,
				mapQualificationCreateGrantFailure(err),
			)
		}
	}
	if err := browserWorker.CallContext(ctx, "signInReviewer", map[string]string{
		"email":             reviewerEmail,
		"temporaryPassword": reviewer.TemporaryPassword,
		"password":          credentials.QualificationPassword + "-reviewer",
	}, &authenticated, nil); err != nil {
		return report, err
	}
	var reviewerToken qualificationBrowserToken
	if err := browserWorker.CallContext(ctx, "issueReviewerToken", map[string]any{
		"privileges": []string{
			"VIEW_ITEM",
			"APPROVE_DEPLOYMENT",
			"ACTIVATE_DEPLOYMENT",
		},
	}, &reviewerToken, nil); err != nil {
		return report, err
	}
	if reviewerToken.AccessToken == "" {
		return report, fmt.Errorf("browser worker returned an empty reviewer token")
	}
	if err := phases.Finish(nil); err != nil {
		return report, err
	}
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
	deployment, err := approveAndActivateQualificationPublication(
		ctx,
		apiClient,
		options,
		reviewerToken.AccessToken,
		publication,
		runSuffix,
	)
	if err != nil {
		return report, err
	}
	if err := verifyExactAuthoringCandidate(candidate, publication, deployment); err != nil {
		return report, err
	}
	workloadToken, err := c.createQualificationAPIToken(
		ctx,
		apiClient,
		options.Target,
		administratorToken.AccessToken,
		"qualification-workload",
		qualificationWorkloadCapabilities(),
		"qualification-workload-"+runSuffix,
	)
	if err != nil {
		return report, err
	}
	projectDataToken, err := c.createQualificationAPIToken(
		ctx,
		apiClient,
		options.Target,
		administratorToken.AccessToken,
		"qualification-project-data",
		qualificationProjectDataCapabilities(),
		"qualification-project-data-"+runSuffix,
	)
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

func (c *Controller) createQualificationAPIToken(
	ctx context.Context,
	client *http.Client,
	target string,
	authorizationToken string,
	name string,
	capabilityNames []string,
	idempotencyKey string,
) (string, error) {
	expiresAt := c.now().UTC().Add(2 * time.Hour).Format(time.RFC3339)
	capabilities := make([]platformgen.Capability, 0, len(capabilityNames))
	for _, name := range capabilityNames {
		capabilities = append(capabilities, platformgen.Capability(name))
	}
	body := accessgen.GenSchemaAPITokenCreateRequest{
		Name: name, Capabilities: &capabilities, ExpiresAt: &expiresAt,
	}
	response, err := accessgen.NewGenClient(qualificationGeneratedTransport(
		target,
		authorizationToken,
		client,
	)).CreateCurrentAPIToken(
		ctx,
		accessgen.GenCreateCurrentAPITokenClientRequest{
			Headers: accessgen.GenCreateCurrentAPITokenClientHeaders{
				IdempotencyKey: idempotencyKey,
			},
			Body: body,
		},
	)
	if err != nil {
		return "", mapQualificationCreateCurrentAPITokenFailure(err)
	}
	if response.Body.Token == "" {
		return "", fmt.Errorf("%s token creation returned an empty credential", name)
	}
	return response.Body.Token, nil
}

func normalizeQualificationAuthoringOptions(options qualificationAuthoringOptions) qualificationAuthoringOptions {
	if options.ClientBaseImage == "" {
		options.ClientBaseImage = options.Image
	}
	if options.Target == "" {
		options.Target = "https://localhost"
	}
	if options.Project == "" {
		options.Project = "/workspace/evaluation/project/leapview.yaml"
	}
	if options.ProjectID == "" {
		options.ProjectID = "leapview-evaluation"
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

func approveAndActivateQualificationPublication(
	ctx context.Context,
	client *http.Client,
	options qualificationAuthoringOptions,
	token string,
	publication QualificationPublication,
	runSuffix string,
) (QualificationDeployment, error) {
	deployments := deploymentgen.NewGenClient(qualificationGeneratedTransport(
		options.Target,
		token,
		client,
	))
	getRequest := deploymentgen.GenGetDeploymentClientRequest{
		Project: options.ProjectID, Deployment: publication.DeploymentID,
	}
	getResponse, err := deployments.GetDeployment(ctx, getRequest)
	if err != nil {
		return QualificationDeployment{}, err
	}
	response := getResponse.Body
	if response.Approval == nil || response.Approval.Status != "pending" {
		return QualificationDeployment{}, fmt.Errorf("publication approval is not pending")
	}
	approval, err := deployments.ApproveDeployment(
		ctx,
		deploymentgen.GenApproveDeploymentClientRequest{
			Project: options.ProjectID, Deployment: publication.DeploymentID,
			Approval: response.Approval.Id,
			Headers: deploymentgen.GenApproveDeploymentClientHeaders{
				IdempotencyKey: "authoring-approve-" + runSuffix,
			},
			Body: deploymentgen.GenSchemaDeploymentApprovalDecisionRequest{
				ExpectedRevision: response.Approval.Revision,
			},
		},
	)
	if err != nil {
		return QualificationDeployment{}, mapQualificationApproveDeploymentFailure(err)
	}
	if approval.Body.Status != "approved" {
		return QualificationDeployment{}, fmt.Errorf("publication approval transitioned to %q", approval.Body.Status)
	}
	if _, err := deployments.ActivateDeployment(
		ctx,
		deploymentgen.GenActivateDeploymentClientRequest{
			Project: options.ProjectID, Deployment: publication.DeploymentID,
			Headers: deploymentgen.GenActivateDeploymentClientHeaders{
				IdempotencyKey: "authoring-activate-" + runSuffix,
			},
		},
	); err != nil {
		return QualificationDeployment{}, mapQualificationActivateDeploymentFailure(err)
	}
	activationCtx, cancel := qualificationContext(ctx, 5*time.Minute)
	defer cancel()
	err = qualificationWait(activationCtx, 250*time.Millisecond, func(waitCtx context.Context) (bool, error) {
		current, err := deployments.GetDeployment(waitCtx, getRequest)
		if err != nil {
			return false, err
		}
		response = current.Body
		switch response.Status {
		case "active":
			return true, nil
		case "cancelled", "failed", "superseded":
			message := ""
			if response.Error != nil {
				message = *response.Error
			}
			return false, fmt.Errorf(
				"publication activation ended in %s: %s",
				response.Status,
				message,
			)
		default:
			return false, nil
		}
	})
	if err != nil {
		return QualificationDeployment{}, err
	}
	return QualificationDeployment{
		CandidateID:       response.Evidence.CandidateId,
		CandidateRevision: response.Evidence.CandidateRevision,
		TargetID:          response.Evidence.TargetId,
		PrincipalID:       response.CreatedBy,
		ArtifactDigest:    response.Evidence.ArtifactContentDigest,
		ReleaseDigest:     response.Evidence.ReleaseDigest,
		Status:            string(response.Status),
	}, nil
}

func qualificationArchitecture() string {
	return runtime.GOARCH
}
