package composectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	qualificationFirstPublicationScope       = "managed-first-publication"
	qualificationFirstPublicationTarget      = "https://localhost"
	qualificationFirstPublicationCompose     = "leapview"
	qualificationFirstPublicationEnvironment = "prod"
	qualificationFirstPublicationReadyURL    = "http://127.0.0.1:8080/readyz"
	qualificationFirstPublicationCredentials = ".qualification-first-publication-credentials.json"
)

var qualificationFirstPublicationImage = regexp.MustCompile(`^ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}$`)

type QualificationFirstPublicationOptions struct {
	EvidenceDir             string
	AssetsRoot              string
	PreloadedClientImage    string
	PreloadedBrowserImage   string
	LifecycleCredentialFile string
}

type qualificationFirstPublicationProfile struct {
	Root             string
	Image            string
	ComposeProject   string
	CaddyDomain      string
	HTTPS            bool
	Target           string
	Environment      string
	ExternalPostgres bool
}

type qualificationFirstPublicationRequest struct {
	TargetURL           string `json:"targetURL"`
	ProjectID           string `json:"projectID"`
	Environment         string `json:"environment"`
	Image               string `json:"image"`
	ImageSourceRevision string `json:"imageSourceRevision"`
	SourceRevision      string `json:"sourceRevision"`
	CandidateID         string `json:"candidateID"`
	CandidateRevision   int64  `json:"candidateRevision"`
	TargetID            string `json:"targetID"`
	PrincipalID         string `json:"principalID"`
	ArtifactDigest      string `json:"artifactDigest"`
	ReleaseDigest       string `json:"releaseDigest"`
	PlanID              string `json:"planID"`
	PlanDigest          string `json:"planDigest"`
}

type qualificationFirstPublicationResult struct {
	CandidateID           string `json:"candidateID"`
	CandidateRevision     int64  `json:"candidateRevision"`
	TargetID              string `json:"targetID"`
	PublicationID         string `json:"publicationID"`
	PublicationStatus     string `json:"publicationStatus"`
	GenerationID          string `json:"generationID"`
	PrincipalID           string `json:"principalID"`
	SourceArtifactDigest  string `json:"sourceArtifactDigest"`
	ServingArtifactDigest string `json:"servingArtifactDigest"`
	ReleaseDigest         string `json:"releaseDigest"`
	SourceRevision        string `json:"sourceRevision"`
	PlanID                string `json:"planID"`
	PlanDigest            string `json:"planDigest"`
}

type qualificationFirstPublicationAssertions struct {
	FirstLoginConsumedOnce      bool `json:"firstLoginConsumedOnce"`
	ReadinessTransitionObserved bool `json:"readinessTransitionObserved"`
	TemporaryCredentialsRemoved bool `json:"temporaryCredentialsRemoved"`
	SecretsExcludedFromEvidence bool `json:"secretsExcludedFromEvidence"`
}

type qualificationFirstPublicationReport struct {
	SchemaVersion        int                                     `json:"schemaVersion"`
	Scope                string                                  `json:"scope"`
	Result               string                                  `json:"result"`
	Request              qualificationFirstPublicationRequest    `json:"request"`
	Publication          qualificationFirstPublicationResult     `json:"publication"`
	Approval             qualificationApprovalEvidence           `json:"approval"`
	PublisherPrincipalID string                                  `json:"publisherPrincipalID"`
	ReviewerPrincipalID  string                                  `json:"reviewerPrincipalID"`
	ReadinessBefore      int                                     `json:"readinessBefore"`
	ReadinessAfter       int                                     `json:"readinessAfter"`
	Phases               []qualificationPhaseEvidence            `json:"phases"`
	Assertions           qualificationFirstPublicationAssertions `json:"assertions"`
	LifecycleCredential  *qualificationLifecycleCredentialScope  `json:"lifecycleCredential,omitempty"`
}

func validateQualificationFirstPublicationProfile(profile qualificationFirstPublicationProfile) error {
	if profile.Root != "/opt/leapview" || !qualificationFirstPublicationImage.MatchString(profile.Image) ||
		profile.ComposeProject != qualificationFirstPublicationCompose || profile.CaddyDomain != "localhost" ||
		!profile.HTTPS || profile.Target != qualificationFirstPublicationTarget ||
		profile.Environment != qualificationFirstPublicationEnvironment || !profile.ExternalPostgres {
		return errors.New("first-publication qualification requires the installed immutable localhost HTTPS/external-Postgres profile")
	}
	return nil
}

func validateQualificationFirstPublicationReport(report qualificationFirstPublicationReport) error {
	if report.SchemaVersion != 1 || report.Scope != qualificationFirstPublicationScope || report.Result != "passed" {
		return errors.New("first-publication report has an unsupported schema or result")
	}
	request, result := report.Request, report.Publication
	if request.TargetURL != qualificationFirstPublicationTarget || request.ProjectID != qualificationProjectID ||
		request.Environment != qualificationFirstPublicationEnvironment || !qualificationFirstPublicationImage.MatchString(request.Image) ||
		!qualificationHistoricalRevisionPattern.MatchString(request.ImageSourceRevision) ||
		!qualificationSHA256Identity(request.SourceRevision) || request.CandidateID == "" || request.CandidateRevision < 1 ||
		request.TargetID == "" || request.PrincipalID == "" || !qualificationSHA256Identity(request.ArtifactDigest) ||
		!qualificationSHA256Identity(request.ReleaseDigest) ||
		request.PlanID == "" || !qualificationSHA256Identity(request.PlanDigest) {
		return errors.New("first-publication request binding is incomplete or outside the installed guest scope")
	}
	if result.CandidateID != request.CandidateID || result.CandidateRevision != request.CandidateRevision ||
		result.TargetID != request.TargetID || result.PrincipalID != request.PrincipalID ||
		result.SourceArtifactDigest != request.ArtifactDigest || !qualificationSHA256Identity(result.ServingArtifactDigest) ||
		result.ReleaseDigest != request.ReleaseDigest || result.SourceRevision != request.SourceRevision ||
		result.PlanID != request.PlanID || result.PlanDigest != request.PlanDigest ||
		result.PublicationID == "" || result.PublicationStatus != "committed" || result.GenerationID == "" {
		return errors.New("first-publication result does not match the requested candidate tuple")
	}
	if report.Approval.ID == "" || report.Approval.Status != "approved" ||
		report.Approval.ApprovedBy == "" || report.Approval.ApprovedBy != report.ReviewerPrincipalID ||
		report.Approval.DeploymentID != result.PublicationID || report.Approval.ProjectID != request.ProjectID ||
		report.Approval.Environment != request.Environment || !qualificationSHA256Identity(report.Approval.RequestDigest) ||
		report.PublisherPrincipalID != request.PrincipalID || report.PublisherPrincipalID == report.ReviewerPrincipalID {
		return errors.New("first-publication approval does not bind an independent reviewer to the committed publication")
	}
	phaseIndex := map[string]int{}
	for index, phase := range report.Phases {
		if _, duplicate := phaseIndex[phase.Name]; duplicate {
			return errors.New("first-publication phase evidence contains duplicates")
		}
		phaseIndex[phase.Name] = index
		if phase.Result != "success" {
			return errors.New("first-publication phase evidence is incomplete")
		}
	}
	reviewer, reviewerOK := phaseIndex["reviewer provisioning"]
	preview, previewOK := phaseIndex["private candidate preview"]
	publish, publishOK := phaseIndex["protected publish"]
	if !reviewerOK || !previewOK || !publishOK || reviewer >= preview || preview >= publish {
		return errors.New("first-publication reviewer provisioning did not precede planning and publication")
	}
	if report.ReadinessBefore != http.StatusServiceUnavailable || report.ReadinessAfter != http.StatusOK ||
		!report.Assertions.FirstLoginConsumedOnce || !report.Assertions.ReadinessTransitionObserved ||
		!report.Assertions.TemporaryCredentialsRemoved || !report.Assertions.SecretsExcludedFromEvidence {
		return errors.New("first-publication readiness or credential-boundary evidence is incomplete")
	}
	return validateQualificationLifecycleScope(report.LifecycleCredential, request)
}

func qualificationSHA256Identity(value string) bool {
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") && qualificationHistoricalSHA256Pattern.MatchString(strings.TrimPrefix(value, "sha256:"))
}

func (c *Controller) QualifyFirstPublication(ctx context.Context, options QualificationFirstPublicationOptions) (runErr error) {
	evidenceDir, err := filepath.Abs(strings.TrimSpace(options.EvidenceDir))
	if err != nil || strings.TrimSpace(options.EvidenceDir) == "" {
		return errors.New("first-publication qualification requires an absolute evidence directory")
	}
	if err := validateQualificationPreloadedImages(options.PreloadedClientImage, options.PreloadedBrowserImage); err != nil {
		return err
	}
	if options.LifecycleCredentialFile != "" {
		if err := validateQualificationLifecycleOutput(options.LifecycleCredentialFile, evidenceDir); err != nil {
			return err
		}
	}
	if err := os.Mkdir(evidenceDir, 0o700); err != nil {
		return fmt.Errorf("create private first-publication evidence directory: %w", err)
	}
	completed := false
	lifecycleWritten := false
	cleanup := qualificationCleanup{}
	var secrets []string
	defer func() {
		runErr = joinQualificationError(runErr, cleanup.Run(context.Background()))
		if !completed || runErr != nil {
			runErr = joinQualificationError(runErr, os.RemoveAll(evidenceDir))
			if lifecycleWritten {
				runErr = joinQualificationError(runErr, os.Remove(options.LifecycleCredentialFile))
			}
		}
		secrets = nil
	}()

	image, err := c.ConfiguredImage()
	if err != nil {
		return err
	}
	runningImage, err := c.RunningImage(ctx)
	if err != nil {
		return err
	}
	composeProject, err := envFileValue(c.path(deploymentEnvName), "COMPOSE_PROJECT_NAME")
	if err != nil {
		return err
	}
	caddyDomain, err := envFileValue(c.path(deploymentEnvName), "CADDY_DOMAIN")
	if err != nil {
		return err
	}
	httpsValue, err := envFileValue(c.path(deploymentEnvName), "COMPOSE_HTTPS")
	if err != nil {
		return err
	}
	applicationEnvironment, err := envFileValue(c.path(appEnvName), "LEAPVIEW_ENVIRONMENT")
	if err != nil {
		return err
	}
	target, err := envFileValue(c.path(appEnvName), "LEAPVIEW_PUBLIC_URL")
	if err != nil {
		return err
	}
	controlURL, err := envFileValue(c.path(appEnvName), "LEAPVIEW_POSTGRES_CONTROL_URL")
	if err != nil {
		return err
	}
	duckLakeURL, err := envFileValue(c.path(appEnvName), "LEAPVIEW_POSTGRES_DUCKLAKE_URL")
	if err != nil {
		return err
	}
	profile := qualificationFirstPublicationProfile{
		Root: c.root, Image: image, ComposeProject: composeProject, CaddyDomain: caddyDomain,
		HTTPS: httpsValue == "1", Target: target, Environment: applicationEnvironment,
		ExternalPostgres: qualificationPostgresAuthorityURL(controlURL) && qualificationPostgresAuthorityURL(duckLakeURL),
	}
	if runningImage != image {
		return errors.New("first-publication image selection differs from the running installed service")
	}
	if err := validateQualificationFirstPublicationProfile(profile); err != nil {
		return err
	}
	caddy, err := c.qualificationCompose(ctx, c.root, "ps", "--quiet", "caddy")
	if err != nil || strings.TrimSpace(string(caddy)) == "" {
		return errors.New("first-publication qualification requires the installed localhost HTTPS proxy")
	}
	if _, err := os.Lstat(c.path(credentialsName)); err != nil {
		return fmt.Errorf("fresh first-publication credentials are unavailable: %w", err)
	}
	imageIdentity, err := qualificationImageRuntimeIdentity(ctx, c, image)
	if err != nil {
		return err
	}
	assetsRoot := strings.TrimSpace(options.AssetsRoot)
	if assetsRoot == "" {
		return errors.New("first-publication qualification requires protected authoring assets")
	}
	assetsRoot, err = filepath.Abs(assetsRoot)
	if err != nil {
		return err
	}
	if err := validateQualificationAuthoringAssets(assetsRoot); err != nil {
		return fmt.Errorf("protected first-publication assets: %w", err)
	}

	readinessBefore, err := qualificationReadinessStatus(ctx, qualificationFirstPublicationReadyURL, profile.CaddyDomain)
	if err != nil {
		return fmt.Errorf("probe installed readiness before first publication: %w", err)
	}
	if readinessBefore != http.StatusServiceUnavailable {
		return fmt.Errorf("fresh installed target readiness before publication is %d, want 503", readinessBefore)
	}
	credentialsPath, removeCredentials, err := newQualificationCredentialDirectory(c.root)
	if err != nil {
		return err
	}
	cleanup.Add(func(context.Context) error { return removeCredentials() })

	var credentialOutput bytes.Buffer
	originalOutput := c.stdout
	c.stdout = &credentialOutput
	err = c.FirstLogin()
	c.stdout = originalOutput
	if err != nil {
		return fmt.Errorf("consume installed first-login credentials: %w", err)
	}
	var credentials qualificationCredentials
	if err := json.Unmarshal(credentialOutput.Bytes(), &credentials); err != nil {
		credentialOutput.Reset()
		return errors.New("installed first-login credentials have an unsupported contract")
	}
	credentialOutput.Reset()
	if credentials.Email == "" || credentials.TemporaryPassword == "" || credentials.ProjectClaimToken == "" ||
		credentials.ProjectClaimTokenExpiresAt == "" {
		return errors.New("installed first-login credentials are incomplete")
	}
	bootstrapEmail, err := envFileValue(c.path(appEnvName), "LEAPVIEW_BOOTSTRAP_ADMIN_EMAIL")
	if err != nil || credentials.Email != bootstrapEmail {
		return errors.New("first-login administrator differs from the installed bootstrap identity")
	}
	credentials.QualificationPassword, err = randomHex(24)
	if err != nil {
		return err
	}
	secrets = append(secrets, credentials.TemporaryPassword, credentials.ProjectClaimToken, credentials.QualificationPassword)
	container, err := c.qualificationApplicationContainer(ctx)
	if err != nil {
		return err
	}
	bootstrap, err := bootstrapQualificationProject(ctx, container, "http://localhost:8080", credentials.ProjectClaimToken, profile.Environment)
	if err != nil {
		return err
	}
	credentials.ClaimCredentialID = bootstrap.ClaimCredentialID
	credentials.PublisherToken = bootstrap.PublisherToken
	credentials.PublisherTokenExpires = bootstrap.PublisherTokenExpiresAt
	secrets = append(secrets, credentials.PublisherToken)
	if err := writeQualificationJSON(credentialsPath, credentials); err != nil {
		return err
	}
	if err := acknowledgeQualificationProjectClaim(ctx, container, "http://localhost:8080", credentials.PublisherToken, credentials.ClaimCredentialID); err != nil {
		return err
	}
	credentials.ProjectClaimToken = ""
	credentials.ProjectClaimTokenExpiresAt = ""
	if err := writeQualificationJSON(credentialsPath, credentials); err != nil {
		return err
	}
	syncOutput, err := container.Exec(ctx, nil, "env", "LEAPVIEW_API_TOKEN="+credentials.PublisherToken,
		"LEAPVIEW_TARGET=http://localhost:8080", "leapview", "data", "sync",
		"--source-root", "/app/evaluation/project", "--project-id", qualificationProjectID,
		"--connection", "sample", "--from", "/app/evaluation/data", "--format", "json")
	if err != nil {
		return fmt.Errorf("stage first-publication managed data: %w", err)
	}
	sourceRevision, err := parseStagedQualificationRevision(string(syncOutput))
	if err != nil {
		return err
	}
	authoring, err := c.runQualificationAuthoring(ctx, qualificationAuthoringOptions{
		BundleRoot: c.root, Image: image, CredentialsFile: credentialsPath,
		ComposeProject: profile.ComposeProject, EvidenceDir: evidenceDir,
		SourceRevision: sourceRevision, Target: profile.Target,
		ProjectID: qualificationProjectID, Environment: profile.Environment,
		AssetsRoot: assetsRoot, FirstPublicationOnly: true,
		PreloadedClientImage: options.PreloadedClientImage, PreloadedBrowserImage: options.PreloadedBrowserImage,
		LifecycleCredentialFile: options.LifecycleCredentialFile,
	})
	if err != nil {
		return fmt.Errorf("qualify first publication through installed authoring path: %w", err)
	}
	lifecycleWritten = authoring.LifecycleCredential != nil
	if lifecycleWritten {
		var retained qualificationLifecycleCredential
		if err := readQualificationJSON(options.LifecycleCredentialFile, &retained); err != nil {
			return err
		}
		secrets = append(secrets, retained.Token)
	}
	if authoring.Result != "success" || authoring.Principal == "" || authoring.ReviewerPrincipalID == "" ||
		authoring.Approval.Status != "approved" || authoring.Approval.ApprovedBy != authoring.ReviewerPrincipalID {
		return errors.New("installed authoring path did not return a complete independent approval and publication")
	}
	readinessAfter, err := waitQualificationReadinessStatus(ctx, qualificationFirstPublicationReadyURL, profile.CaddyDomain, 3*time.Minute)
	if err != nil {
		return fmt.Errorf("installed application did not become ready after first publication: %w", err)
	}
	if _, err := os.Lstat(c.path(credentialsName)); !os.IsNotExist(err) {
		return errors.New("one-time first-login credentials remain after qualification")
	}
	if _, err := os.Lstat(credentialsPath); err != nil {
		return errors.New("temporary qualification credentials are missing before cleanup")
	}
	if err := qualificationEvidenceExcludesSecrets(evidenceDir, secrets); err != nil {
		return err
	}
	result := qualificationFirstPublicationReport{
		SchemaVersion: 1, Scope: qualificationFirstPublicationScope, Result: "passed",
		Request: qualificationFirstPublicationRequest{
			TargetURL: profile.Target, ProjectID: qualificationProjectID, Environment: profile.Environment,
			Image: image, ImageSourceRevision: imageIdentity.Revision, SourceRevision: sourceRevision,
			CandidateID: authoring.Candidate, CandidateRevision: authoring.Revision, TargetID: authoring.Target,
			PrincipalID: authoring.Principal, ArtifactDigest: authoring.SourceArtifact, ReleaseDigest: authoring.ReleaseDigest,
			PlanID: authoring.PlanID, PlanDigest: authoring.PlanDigest,
		},
		Publication: qualificationFirstPublicationResult{
			CandidateID: authoring.PublishedCandidateID, CandidateRevision: authoring.PublishedCandidateRevision,
			TargetID:      authoring.PublishedTargetID,
			PublicationID: authoring.PublicationID, PublicationStatus: authoring.PublicationStatus,
			GenerationID: authoring.PublishedGenerationID, PrincipalID: authoring.PublishedPrincipalID,
			SourceArtifactDigest: authoring.PublishedSourceArtifact, ServingArtifactDigest: authoring.PublishedArtifact,
			ReleaseDigest: authoring.PublishedReleaseDigest, SourceRevision: authoring.PublishedSourceRevision,
			PlanID: authoring.PublishedPlanID, PlanDigest: authoring.PublishedPlanDigest,
		},
		Approval:             authoring.Approval,
		PublisherPrincipalID: authoring.AuthorPrincipalID, ReviewerPrincipalID: authoring.ReviewerPrincipalID,
		ReadinessBefore: readinessBefore, ReadinessAfter: readinessAfter, Phases: authoring.Phases,
		LifecycleCredential: authoring.LifecycleCredential,
		Assertions: qualificationFirstPublicationAssertions{
			FirstLoginConsumedOnce: true, ReadinessTransitionObserved: readinessBefore == http.StatusServiceUnavailable && readinessAfter == http.StatusOK,
			TemporaryCredentialsRemoved: true, SecretsExcludedFromEvidence: true,
		},
	}
	if err := validateQualificationFirstPublicationReport(result); err != nil {
		return err
	}
	if err := writeQualificationJSON(filepath.Join(evidenceDir, "first-publication-report.json"), result); err != nil {
		return err
	}
	if err := qualificationEvidenceExcludesSecrets(evidenceDir, secrets); err != nil {
		return err
	}
	if err := removeCredentials(); err != nil {
		return err
	}
	completed = true
	_, runErr = fmt.Fprintln(c.stdout, "installed first-publication qualification passed")
	return runErr
}

// Own a private directory before the atomic writer can create or rename a
// credential file. A failed directory sync after rename still requires cleanup.
func newQualificationCredentialDirectory(root string) (string, func() error, error) {
	directory, err := os.MkdirTemp(root, ".qualification-first-publication-")
	if err != nil {
		return "", nil, fmt.Errorf("create private qualification credential directory: %w", err)
	}
	return filepath.Join(directory, qualificationFirstPublicationCredentials), func() error {
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("remove temporary qualification credential directory: %w", err)
		}
		return nil
	}, nil
}

type qualificationImageRuntimeVersion struct {
	Revision    string `json:"revision"`
	Dirty       bool   `json:"dirty"`
	Development bool   `json:"development"`
}

func qualificationImageRuntimeIdentity(ctx context.Context, c *Controller, image string) (qualificationImageRuntimeVersion, error) {
	var identity qualificationImageRuntimeVersion
	output, err := c.qualificationDocker(ctx, nil, "run", "--rm", "--entrypoint", "/usr/local/libexec/leapviewctl", image, "version", "--format", "json")
	if err != nil {
		return identity, fmt.Errorf("read installed image runtime identity: %w", err)
	}
	var full struct {
		Product     string `json:"product"`
		Version     string `json:"version"`
		Revision    string `json:"revision"`
		BuildTime   string `json:"buildTime"`
		Dirty       bool   `json:"dirty"`
		Development bool   `json:"development"`
	}
	if err := json.Unmarshal(output, &full); err != nil || full.Product != "leapviewctl" ||
		!qualificationHistoricalRevisionPattern.MatchString(full.Revision) || full.Dirty || full.Development {
		return identity, errors.New("installed image runtime identity is not a clean immutable release")
	}
	identity.Revision, identity.Dirty, identity.Development = full.Revision, full.Dirty, full.Development
	return identity, nil
}

func qualificationPostgresAuthorityURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") && parsed.Hostname() == "postgres"
}

func qualificationReadinessStatus(ctx context.Context, endpoint, authority string) (int, error) {
	return hostHTTPStatus(ctx, endpoint, authority)
}

func waitQualificationReadinessStatus(ctx context.Context, endpoint, authority string, timeout time.Duration) (int, error) {
	waitCtx, cancel := qualificationContext(ctx, timeout)
	defer cancel()
	var status int
	err := qualificationWait(waitCtx, 2*time.Second, func(waitCtx context.Context) (bool, error) {
		var err error
		status, err = qualificationReadinessStatus(waitCtx, endpoint, authority)
		if err != nil {
			return false, nil
		}
		if status == http.StatusOK {
			return true, nil
		}
		if status != http.StatusServiceUnavailable {
			return false, fmt.Errorf("/readyz returned unexpected HTTP status %d", status)
		}
		return false, nil
	})
	return status, err
}

func qualificationEvidenceExcludesSecrets(evidenceDir string, secrets []string) error {
	return filepath.WalkDir(evidenceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("first-publication evidence contains a non-regular file")
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if qualificationSecretPattern.Match(contents) || qualificationBearerPattern.Match(contents) {
			return errors.New("first-publication evidence contains a secret field or bearer credential")
		}
		for _, secret := range secrets {
			if secret != "" && bytes.Contains(contents, []byte(secret)) {
				return errors.New("first-publication evidence contains a temporary credential value")
			}
		}
		return nil
	})
}
