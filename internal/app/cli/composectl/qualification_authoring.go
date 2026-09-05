package composectl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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

const qualificationBrowserImage = "mcr.microsoft.com/playwright:v1.62.1-noble"

const (
	qualificationReviewerEmail = "authoring-reviewer@qualification.invalid"
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
	AuditToken            string `json:"auditToken,omitempty"`
	AuthorPrincipalID     string `json:"authorPrincipalId,omitempty"`
	ReviewerPrincipalID   string `json:"reviewerPrincipalId,omitempty"`
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
	GenerationID   string                       `json:"generationId"`
	SnapshotSealID string                       `json:"snapshotSealId"`
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

	// Native delivery owns candidate planning, sealing, and publication. There
	// is intentionally no legacy "bootstrap" publication here: the first
	// generation must follow the same review-gated path as every later change.
	// This keeps target revision zero fail-closed until the normal authoring
	// journey has an explicit approval and activation authority.
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

	var administrator struct {
		Authenticated bool `json:"authenticated"`
		Principal     struct {
			Id string `json:"id"`
		} `json:"principal"`
	}
	if err := browserWorker.CallContext(ctx, "signInAdministrator", map[string]string{
		"email":             credentials.Email,
		"temporaryPassword": credentials.TemporaryPassword,
		"password":          credentials.QualificationPassword,
	}, &administrator, nil); err != nil {
		return report, err
	}
	if !administrator.Authenticated || administrator.Principal.Id == "" {
		return report, fmt.Errorf("administrator sign-in returned no durable principal")
	}
	if err := validateQualificationNativeUUID(administrator.Principal.Id, "administrator principal"); err != nil {
		return report, err
	}
	var authenticated struct {
		Authenticated bool `json:"authenticated"`
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
	}, &reviewer, nil); err != nil {
		return report, err
	}
	if reviewer.Principal.Id == "" || reviewer.TemporaryPassword == "" {
		return report, fmt.Errorf("reviewer creation returned incomplete credentials")
	}
	if err := validateQualificationNativeUUID(reviewer.Principal.Id, "reviewer principal"); err != nil {
		return report, err
	}
	var administratorToken qualificationBrowserToken
	if err := browserWorker.CallContext(ctx, "issueAdministratorToken", map[string]any{
		"capabilities": []string{"PROJECT_ADMIN", "RESOURCE_READ", "RESOURCE_EDIT", "RESOURCE_PUBLISH"},
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
	apiClient, err := qualificationHTTPSClient(certificateFile)
	if err != nil {
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
		"--env", qualificationAuthorPrincipalEnv+"="+administrator.Principal.Id,
		"--env", qualificationReviewerPrincipalEnv+"="+reviewer.Principal.Id,
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
	if strings.TrimSpace(candidate.PreviewURL) == "" {
		return report, fmt.Errorf("native delivery candidate %s has no private preview URL; preview authority is not available", candidate.ID)
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
	activated, err := waitQualificationNativePublication(
		ctx, apiClient, options, administratorToken.AccessToken, publication,
	)
	if err != nil {
		return report, err
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
	auditToken, err := createAPIToken("qualification-audit", []string{"PROJECT_ADMIN"})
	if err != nil {
		return report, err
	}
	credentials.WorkloadToken = workloadToken
	credentials.ProjectDataToken = projectDataToken
	credentials.RecoveryControlToken = reviewerToken.AccessToken
	credentials.AuditToken = auditToken
	credentials.AuthorPrincipalID = administrator.Principal.Id
	credentials.ReviewerPrincipalID = reviewer.Principal.Id
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
	report.GenerationID = deployment.GenerationID
	report.SnapshotSealID = deployment.SnapshotSealID
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

// waitQualificationNativePublication observes the publication created by the
// native PublishDeliveryCandidate command. Approval schedules activation in a
// separate worker; issuing publish a second time would create another pending
// publication instead of observing this one.
func waitQualificationNativePublication(
	ctx context.Context,
	client *http.Client,
	options qualificationAuthoringOptions,
	token string,
	pending QualificationPublication,
) (QualificationPublication, error) {
	api := deploymentgen.NewGenClient(qualificationGeneratedTransport(options.Target, token, client))
	waitCtx, cancel := qualificationContext(ctx, 5*time.Minute)
	defer cancel()
	var committed QualificationPublication
	err := qualificationWait(waitCtx, 2*time.Second, func(pollCtx context.Context) (bool, error) {
		current, err := api.GetDeliveryPublicationEvidence(
			pollCtx,
			deploymentgen.GenGetDeliveryPublicationEvidenceClientRequest{
				Project: options.ProjectID, Publication: pending.DeploymentID,
			},
		)
		if err != nil {
			if qualificationTransientDeploymentError(err) {
				return false, nil
			}
			return false, err
		}
		body := current.Body
		if body.Id != pending.DeploymentID || body.CandidateId != pending.CandidateID ||
			body.PlanId != pending.PlanID || body.PlanDigest != pending.PlanDigest ||
			body.TargetId != pending.TargetID {
			return false, fmt.Errorf("native publication evidence does not match the requested candidate")
		}
		switch body.Status {
		case deploymentgen.DeliveryPublicationStatusPending:
			return false, nil
		case deploymentgen.DeliveryPublicationStatusCommitted:
			committed = pending
			committed.Status = string(body.Status)
			committed.GenerationID = body.GenerationId
			return true, nil
		case deploymentgen.DeliveryPublicationStatusRejected, deploymentgen.DeliveryPublicationStatusIndeterminate:
			return false, fmt.Errorf("native publication ended in %q", body.Status)
		default:
			return false, fmt.Errorf("native publication returned unknown status %q", body.Status)
		}
	})
	if err != nil {
		return QualificationPublication{}, err
	}
	return committed, nil
}

func normalizeQualificationAuthoringOptions(options qualificationAuthoringOptions) qualificationAuthoringOptions {
	if options.ClientBaseImage == "" {
		options.ClientBaseImage = options.Image
	}
	if options.Target == "" {
		options.Target = "https://localhost"
	}
	if options.Project == "" {
		// The client image copies and re-owns the fixture under /qualification so the
		// unprivileged author user can traverse and read it. The base image's
		// /app copy deliberately retains production runtime ownership.
		options.Project = "/qualification/evaluation/project/leapview.yaml"
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

func qualificationHTTPSClient(certificateFile string) (*http.Client, error) {
	certificate, err := os.ReadFile(certificateFile)
	if err != nil {
		return nil, fmt.Errorf("read qualification CA certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return nil, errors.New("qualification CA certificate is invalid")
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			// Trust only the generated CA copied from this isolated production
			// Compose target. Normal hostname and certificate verification remain
			// enabled; qualification must fail if the endpoint identity drifts.
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		},
	}, nil
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
	planMatches := qualificationPlanMatchesCandidate(plan.Body, candidate)
	provenanceMatches := plan.Body.ProvenanceDigest == candidate.ProvenanceDigest
	if !planMatches || !provenanceMatches {
		return QualificationPublication{}, QualificationDeployment{}, fmt.Errorf(
			"canonical plan evidence does not match the previewed candidate (plan digest=%t, source digest=%t, provenance=%t, target=%t)",
			plan.Body.PlanDigest == candidate.PlanDigest,
			plan.Body.SourceDigest == candidate.ArtifactDigest,
			plan.Body.ProvenanceDigest == candidate.ProvenanceDigest,
			plan.Body.TargetId == candidate.TargetID,
		)
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
	snapshotSealID := ""
	if serverCandidate.Body.SnapshotSealId != nil {
		snapshotSealID = strings.TrimSpace(*serverCandidate.Body.SnapshotSealId)
	}
	if serverCandidate.Body.Id != candidate.ID ||
		serverCandidate.Body.Status != deploymentgen.DeliveryCandidateStatusReady ||
		serverCandidate.Body.PlanId != candidate.PlanID ||
		serverCandidate.Body.PlanDigest != candidate.PlanDigest ||
		serverCandidate.Body.SourceDigest != candidate.ArtifactDigest ||
		serverCandidate.Body.TargetId != candidate.TargetID || snapshotSealID == "" {
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
	pending.CandidateRevision = candidate.Revision
	pending.PrincipalID = candidate.PrincipalID
	pending.ReleaseDigest = candidate.ProvenanceDigest
	pending.Status = string(serverPublication.Status)
	pending.GenerationID = serverPublication.GenerationId
	return pending, QualificationDeployment{
		CandidateID: candidate.ID, CandidateRevision: candidate.Revision,
		TargetID: candidate.TargetID, PrincipalID: candidate.PrincipalID,
		ArtifactDigest: generation.Body.ServingArtifactDigest,
		ReleaseDigest:  candidate.ProvenanceDigest,
		GenerationID:   generation.Body.Id, SnapshotSealID: snapshotSealID,
		PlanID:     generation.Body.PlanId,
		PlanDigest: generation.Body.PlanDigest, Status: string(generation.Body.Status),
	}, nil
}

func qualificationPlanMatchesCandidate(plan deploymentgen.DeliveryPlanPreviewResponse, candidate QualificationCandidate) bool {
	// Candidate provenance identifies the synchronized project artifact, while
	// plan provenance identifies the canonical delivery inputs (including the
	// retained source attestation). They are independently bound by the plan
	// digest and must not be compared as if they were the same hash domain.
	return plan.PlanDigest == candidate.PlanDigest &&
		plan.SourceDigest == candidate.ArtifactDigest &&
		plan.TargetId == candidate.TargetID
}

// qualificationTransientDeploymentError treats generated problem responses and
// plain-text 503/429s from authorization, readiness, or rate-limit middleware
// as transient while a native publication is being activated. These responses
// may bypass APIGen's problem+json envelope, so checking only ProblemError
// would abort observation before the async worker finishes.
func qualificationTransientDeploymentError(err error) bool {
	if err == nil {
		return false
	}
	var problem *apigenclient.ProblemError
	if errors.As(err, &problem) {
		return problem.Problem.Status == http.StatusServiceUnavailable || problem.Problem.Status == http.StatusTooManyRequests || problem.Response.StatusCode == http.StatusServiceUnavailable || problem.Response.StatusCode == http.StatusTooManyRequests
	}
	message := strings.TrimSpace(err.Error())
	return strings.HasSuffix(message, ": "+http.StatusText(http.StatusServiceUnavailable)) || strings.HasSuffix(message, ": "+http.StatusText(http.StatusTooManyRequests))
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
		publication.PrincipalID == "" || requested.Body.RequestedBy != publication.PrincipalID {
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
