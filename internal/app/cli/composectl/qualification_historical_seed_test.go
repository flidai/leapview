package composectl

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const qualificationHistoricalInternalTarget = "http://localhost:8080"

var qualificationHistoricalPostgresURLPattern = regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s"'<>]+`)

// qualificationHistoricalSeed is the actual predecessor workload created by
// seedQualificationHistoricalRuntime. Client secrets and the viewer's
// one-time password stay in memory for the duration of the isolated test.
type qualificationHistoricalSeed struct {
	Target                               string
	TargetID                             string
	ProjectID                            string
	Environment                          string
	DashboardID                          string
	PublisherPrincipalID                 string
	PublisherClientID                    string
	PublisherClientSecret                string
	ReviewerPrincipalID                  string
	ReleaseClientID                      string
	ReleaseClientSecret                  string
	ViewerPrincipalID                    string
	ViewerEmail                          string
	ViewerPassword                       string
	SourceRoot                           string
	DataPath                             string
	LegacyViewerGrantIDs                 []string
	LegacyPublisherGrantIDs              []string
	LegacyReviewerGrantID                string
	LegacyPublisherRoleBindingID         string
	LegacyPublisherDeployerRoleBindingID string
	LegacyReviewerRoleBindingID          string
}

type qualificationHistoricalInitialCredentials struct {
	Email                   string `json:"email"`
	TemporaryPassword       string `json:"temporaryPassword"`
	PublisherToken          string `json:"publisherToken"`
	PublisherTokenExpiresAt string `json:"publisherTokenExpiresAt"`
}

type qualificationHistoricalBootstrapResult struct {
	SchemaVersion               int    `json:"schemaVersion"`
	Type                        string `json:"type"`
	Target                      string `json:"target"`
	ProjectUID                  string `json:"projectUid"`
	Environment                 string `json:"environment"`
	AuthorizationPolicyRevision int64  `json:"authorizationPolicyRevision"`
}

type qualificationHistoricalServicePrincipal struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type qualificationHistoricalPrincipalCreate struct {
	Principal struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"principal"`
	TemporaryPassword string `json:"temporaryPassword"`
}

type qualificationHistoricalGrant struct {
	ID             string `json:"id"`
	ResourceID     string `json:"resourceId"`
	ResourceKind   string `json:"resourceKind"`
	SubjectType    string `json:"subjectType"`
	SubjectID      string `json:"subjectId"`
	Capability     string `json:"capability"`
	PolicyRevision int64  `json:"policyRevision"`
}

type qualificationHistoricalRoleBinding struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	SubjectType    string   `json:"subjectType"`
	SubjectID      string   `json:"subjectId"`
	Role           string   `json:"role"`
	Capabilities   []string `json:"capabilities"`
	PolicyRevision int64    `json:"policyRevision"`
}

// seedQualificationHistoricalRuntime turns an already-running pinned schema-32
// predecessor into a representative CFO installation using only the old
// image's own CLI and authenticated APIs. The caller owns application and
// PostgreSQL lifecycle; this helper owns bootstrap identity, service
// credentials, explicit legacy grants, and private synthetic data files.
func seedQualificationHistoricalRuntime(
	ctx context.Context,
	t *testing.T,
	app qualificationContainer,
	target string,
	projectID string,
	sourceRoot string,
	controlMigratorURL string,
	beforeBootstrap ...func(context.Context) error,
) (qualificationHistoricalSeed, error) {
	initial, err := initializeQualificationHistoricalRuntime(ctx, t, app, controlMigratorURL)
	if err != nil {
		return qualificationHistoricalSeed{}, err
	}
	return seedQualificationHistoricalRuntimeWithCredentials(ctx, t, app, target, projectID, sourceRoot, initial, beforeBootstrap...)
}

// initializeQualificationHistoricalRuntime runs the schema-32 image's real
// one-time initialization command in a utility container and captures the
// generated administrator and publisher credentials in memory. Callers may
// initialize the shared native database before starting the application.
func initializeQualificationHistoricalRuntime(
	ctx context.Context,
	t *testing.T,
	app qualificationContainer,
	controlMigratorURL string,
) (qualificationHistoricalInitialCredentials, error) {
	var initial qualificationHistoricalInitialCredentials
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || app == nil {
		return initial, errors.New("historical predecessor test and utility container are required")
	}
	command, err := qualificationHistoricalPredecessorInitializationCommand(controlMigratorURL)
	if err != nil {
		return initial, err
	}
	initialOutput, err := app.Exec(ctx, nil, command...)
	if err != nil {
		return initial, qualificationHistoricalPredecessorInitializationError(err, initialOutput, controlMigratorURL)
	}
	if err := validateQualificationHistoricalInitialCredentials(initialOutput, &initial); err != nil {
		return qualificationHistoricalInitialCredentials{}, fmt.Errorf("%w (child output withheld; %d bytes)", err, len(initialOutput))
	}
	return initial, nil
}

func qualificationHistoricalPredecessorInitializationCommand(controlMigratorURL string) ([]string, error) {
	command := []string{"leapview", "admin", "initialize", "--format", "json"}
	controlMigratorURL = strings.TrimSpace(controlMigratorURL)
	if controlMigratorURL == "" {
		return nil, errors.New("predecessor initialization control migrator URL is required")
	}
	canonicalURL, err := canonicalPostgresConnectionURL(postgresConnection{
		name: "predecessor initialization control migrator", value: controlMigratorURL,
		role: qualificationNativePostgresControlMigratorRole, database: qualificationNativePostgresControlDatabase,
	})
	if err != nil {
		return nil, err
	}
	return append([]string{
		"env", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=" + canonicalURL,
	}, command...), nil
}

func qualificationHistoricalPredecessorInitializationError(err error, output []byte, secrets ...string) error {
	detail := "command failed"
	if err != nil {
		safe := redactQualificationBytes([]byte(err.Error()))
		for _, secret := range secrets {
			if secret != "" {
				safe = bytes.ReplaceAll(safe, []byte(secret), []byte("[REDACTED]"))
			}
		}
		detail = string(qualificationHistoricalPostgresURLPattern.ReplaceAll(safe, []byte("[REDACTED]")))
		detail = strings.TrimSpace(detail)
		if detail == "" {
			detail = "command failed"
		}
	}
	// Initialization output includes one-time passwords and bearer credentials.
	// Keep the byte count for failure triage, but never attach the child output.
	return fmt.Errorf("run predecessor admin initialize: %s (child output withheld; %d bytes)", detail, len(output))
}

// seedQualificationHistoricalRuntimeWithCredentials finishes setup against a
// running schema-32 app using credentials previously returned by
// initializeQualificationHistoricalRuntime. The optional hook runs after
// credentials are available and immediately before bootstrap-project.
func seedQualificationHistoricalRuntimeWithCredentials(
	ctx context.Context,
	t *testing.T,
	app qualificationContainer,
	target string,
	projectID string,
	sourceRoot string,
	initial qualificationHistoricalInitialCredentials,
	beforeBootstrap ...func(context.Context) error,
) (qualificationHistoricalSeed, error) {
	return seedQualificationHistoricalRuntimeWithCredentialsAcknowledgedBy(ctx, t, app, app, target, projectID, sourceRoot, initial, beforeBootstrap...)
}

// seedQualificationHistoricalRuntimeWithCredentialsSkippingAcknowledgement
// lets callers that initialized through a separate utility container remove
// the one-time credential bundle from that container after this helper returns.
func seedQualificationHistoricalRuntimeWithCredentialsSkippingAcknowledgement(
	ctx context.Context,
	t *testing.T,
	app qualificationContainer,
	target string,
	projectID string,
	sourceRoot string,
	initial qualificationHistoricalInitialCredentials,
	beforeBootstrap ...func(context.Context) error,
) (qualificationHistoricalSeed, error) {
	return seedQualificationHistoricalRuntimeWithCredentialsAcknowledgedBy(ctx, t, app, nil, target, projectID, sourceRoot, initial, beforeBootstrap...)
}

func seedQualificationHistoricalRuntimeWithCredentialsAcknowledgedBy(
	ctx context.Context,
	t *testing.T,
	app qualificationContainer,
	acknowledgementContainer qualificationContainer,
	target string,
	projectID string,
	sourceRoot string,
	initial qualificationHistoricalInitialCredentials,
	beforeBootstrap ...func(context.Context) error,
) (qualificationHistoricalSeed, error) {
	var result qualificationHistoricalSeed
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || app == nil {
		return result, errors.New("historical predecessor test and application container are required")
	}
	target = strings.TrimRight(strings.TrimSpace(target), "/")
	projectID = strings.TrimSpace(projectID)
	sourceRoot = strings.TrimSpace(sourceRoot)
	if target == "" || projectID == "" || sourceRoot == "" {
		return result, errors.New("historical predecessor target, project, and CFO source root are required")
	}
	if !filepath.IsAbs(sourceRoot) {
		return result, errors.New("historical predecessor CFO source root must be absolute")
	}
	sourceInfo, err := os.Stat(sourceRoot)
	if err != nil || !sourceInfo.IsDir() {
		return result, errors.New("historical predecessor CFO source root must be an existing directory")
	}
	if err := projectIDValidate(projectID); err != nil {
		return result, fmt.Errorf("historical predecessor project identity is invalid: %w", err)
	}
	graph, err := projectcompiler.CompileGraph(sourceRoot)
	if err != nil {
		return result, fmt.Errorf("compile historical CFO source graph: %w", err)
	}
	dashboardID := projectgraph.ResourceID("dashboard:cfo-command-center")
	dashboard, ok := graph.Resource(dashboardID)
	if !ok || dashboard.Kind != projectgraph.KindDashboard {
		return result, errors.New("historical CFO source root is missing dashboard:cfo-command-center")
	}
	semanticID := projectgraph.ResourceID("semantic-model:finance")
	semanticModel, ok := graph.Resource(semanticID)
	if !ok || semanticModel.Kind != projectgraph.KindSemanticModel {
		return result, errors.New("historical CFO source root is missing semantic-model:finance")
	}
	if err := graph.Validate(); err != nil {
		return result, fmt.Errorf("validate historical CFO source graph: %w", err)
	}
	dataPath, err := qualificationHistoricalSyntheticCFOData(t)
	if err != nil {
		return result, err
	}
	if err := validateQualificationHistoricalInitialCredentials(nil, &initial); err != nil {
		return result, err
	}
	if len(beforeBootstrap) > 1 {
		return result, errors.New("historical predecessor accepts at most one pre-bootstrap hook")
	}
	if len(beforeBootstrap) == 1 && beforeBootstrap[0] != nil {
		if err := beforeBootstrap[0](ctx); err != nil {
			return result, fmt.Errorf("prepare historical predecessor before project bootstrap: %w", err)
		}
	}
	bootstrapOutput, err := app.Exec(ctx, nil,
		"env",
		"LEAPVIEW_API_TOKEN="+initial.PublisherToken,
		"LEAPVIEW_TARGET="+qualificationHistoricalInternalTarget,
		"leapview", "bootstrap-project", qualificationHistoricalInternalTarget,
		"--project-uid", projectID,
		"--format", "json",
	)
	if err != nil {
		return result, errors.New("run predecessor bootstrap-project")
	}
	var bootstrap qualificationHistoricalBootstrapResult
	if err := json.Unmarshal(bootstrapOutput, &bootstrap); err != nil ||
		bootstrap.SchemaVersion != 1 || bootstrap.Type != "projectBootstrapped" ||
		strings.TrimRight(strings.TrimSpace(bootstrap.Target), "/") != qualificationHistoricalInternalTarget ||
		bootstrap.ProjectUID != projectID || strings.TrimSpace(bootstrap.Environment) == "" ||
		bootstrap.AuthorizationPolicyRevision <= 0 {
		return result, errors.New("predecessor bootstrap-project returned incompatible target policy evidence")
	}
	// The initial credential has been captured in process memory and used to
	// establish the real target ProjectUID, so close the one-time recovery file.
	if acknowledgementContainer != nil {
		ackOutput, ackErr := acknowledgementContainer.Exec(ctx, nil, "leapview", "admin", "initialize", "--acknowledge-credentials")
		if ackErr != nil {
			diagnostic := strings.TrimSpace(string(ackOutput))
			for _, secret := range []string{initial.PublisherToken, initial.TemporaryPassword} {
				if secret != "" {
					diagnostic = strings.ReplaceAll(diagnostic, secret, "[redacted]")
				}
			}
			if len(diagnostic) > 512 {
				diagnostic = diagnostic[:512]
			}
			return result, fmt.Errorf("acknowledge predecessor initial credentials failed (%v): %s", ackErr, diagnostic)
		}
	}

	runID := uuid.NewString()
	api := func(operation string, body any, idempotencyKey string, paths ...string) ([]byte, error) {
		pathValues := make(map[string]string, len(paths))
		for _, path := range paths {
			name, value, ok := strings.Cut(path, "=")
			if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("predecessor API operation %s has an invalid path value", operation)
			}
			pathValues[name] = value
		}
		path := ""
		method := ""
		switch operation {
		case "getInstance":
			method, path = http.MethodGet, "/api/v1/instance"
		case "listServicePrincipals":
			method, path = http.MethodGet, "/api/v1/service-principals"
		case "createServicePrincipal":
			method, path = http.MethodPost, "/api/v1/service-principals"
		case "createServicePrincipalSecret":
			method, path = http.MethodPost, "/api/v1/service-principals/"+url.PathEscape(pathValues["servicePrincipal"])+"/secrets"
		case "createPrincipal":
			method, path = http.MethodPost, "/api/v1/principals"
		case "createGrant":
			method, path = http.MethodPost, "/api/v1/projects/"+url.PathEscape(pathValues["project"])+"/grants"
		case "listGrants":
			method, path = http.MethodGet, "/api/v1/projects/"+url.PathEscape(pathValues["project"])+"/grants"
		case "createProjectRoleBinding":
			method, path = http.MethodPost, "/api/v1/projects/"+url.PathEscape(pathValues["project"])+"/role-bindings"
		case "listProjectRoleBindings":
			method, path = http.MethodGet, "/api/v1/projects/"+url.PathEscape(pathValues["project"])+"/role-bindings"
		default:
			return nil, fmt.Errorf("predecessor API operation %s is not part of this bounded seed contract", operation)
		}
		endpoint, err := url.Parse(target + path)
		if err != nil {
			return nil, fmt.Errorf("build predecessor API operation %s URL: %w", operation, err)
		}
		var requestBody io.Reader
		if body != nil {
			encoded, encodeErr := json.Marshal(body)
			if encodeErr != nil {
				return nil, encodeErr
			}
			requestBody = bytes.NewReader(encoded)
		}
		request, requestErr := http.NewRequestWithContext(ctx, method, endpoint.String(), requestBody)
		if requestErr != nil {
			return nil, requestErr
		}
		if operation == "listGrants" || operation == "listProjectRoleBindings" {
			query := endpoint.Query()
			query.Set("limit", "200")
			if pageToken := strings.TrimSpace(pathValues["pageToken"]); pageToken != "" {
				query.Set("pageToken", pageToken)
			}
			request.URL.RawQuery = query.Encode()
		}
		request.Host = "demo.leapview.dev"
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+initial.PublisherToken)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			request.Header.Set("Idempotency-Key", idempotencyKey)
		}
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			return nil, fmt.Errorf("predecessor API operation %s failed: %s", operation, qualificationHistoricalCommandDiagnostic(nil, requestErr, initial.PublisherToken, initial.TemporaryPassword))
		}
		defer response.Body.Close()
		output, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return nil, fmt.Errorf("read predecessor API operation %s response: %w", operation, readErr)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			// Do not mark these host-side calls as CLI commands: the predecessor's
			// API command fence has an incompatible generated dispatcher edge.
			// The call still executes through the real schema-32 HTTP API under
			// the predecessor's initialized administrator credential.
			diagnostic := qualificationHistoricalCommandDiagnostic(output, nil, initial.PublisherToken, initial.TemporaryPassword)
			return nil, fmt.Errorf("predecessor API operation %s returned HTTP %d: %s", operation, response.StatusCode, diagnostic)
		}
		return output, nil
	}
	instanceOutput, err := api("getInstance", nil, "")
	if err != nil {
		return result, err
	}
	var instance struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(instanceOutput, &instance) != nil || strings.TrimSpace(instance.ID) == "" {
		return result, errors.New("predecessor did not return its instance identity")
	}

	createServicePrincipal := func(label string) (string, string, error) {
		displayName := "Historical transition " + label + " " + runID
		principalOutput, callErr := api("createServicePrincipal", map[string]any{
			"displayName": displayName,
		}, "historical-"+runID+"-"+label+"-principal")
		recoveredFromGuard := false
		if callErr != nil {
			// This exact predecessor has a known command-contract bug on this one
			// API: CreateServicePrincipal commits through runAuditedMutation but
			// does not complete the generated command guard, so the runtime turns
			// its 201 into this 500 after the transaction has committed. Recover
			// only that known response by reading the unique run-scoped identity
			// back through the old API. Never retry the POST or swallow other errors.
			if !qualificationHistoricalCommandNotExecuted(callErr) {
				return "", "", callErr
			}
			recoveredFromGuard = true
			principalOutput, callErr = api("listServicePrincipals", nil, "")
			if callErr != nil {
				return "", "", fmt.Errorf("read back predecessor service principal after its known command-contract response: %w", callErr)
			}
		}
		var principal qualificationHistoricalServicePrincipal
		if recoveredFromGuard {
			var principals struct {
				Items []qualificationHistoricalServicePrincipal `json:"items"`
			}
			if json.Unmarshal(principalOutput, &principals) != nil {
				return "", "", errors.New("predecessor could not read back its service principal after the known command-contract response")
			}
			matches := make([]qualificationHistoricalServicePrincipal, 0, 1)
			for _, candidate := range principals.Items {
				if candidate.DisplayName == displayName && strings.TrimSpace(candidate.ID) != "" {
					matches = append(matches, candidate)
				}
			}
			if len(matches) != 1 {
				return "", "", fmt.Errorf("predecessor service principal readback found %d exact matches for %s", len(matches), label)
			}
			principal = matches[0]
		} else if json.Unmarshal(principalOutput, &principal) != nil || strings.TrimSpace(principal.ID) == "" {
			return "", "", fmt.Errorf("predecessor did not return the %s service principal identity", label)
		}
		secretOutput, callErr := api("createServicePrincipalSecret", map[string]any{
			"name":      "Historical transition " + label,
			"expiresAt": time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339),
		}, "historical-"+runID+"-"+label+"-secret", "servicePrincipal="+principal.ID)
		if callErr != nil {
			return "", "", callErr
		}
		var secret struct {
			Secret string `json:"secret"`
		}
		if json.Unmarshal(secretOutput, &secret) != nil || strings.TrimSpace(secret.Secret) == "" {
			return "", "", fmt.Errorf("predecessor did not return the %s service principal secret", label)
		}
		return principal.ID, secret.Secret, nil
	}

	publisherID, publisherSecret, err := createServicePrincipal("publisher")
	if err != nil {
		return result, err
	}
	reviewerID, releaseSecret, err := createServicePrincipal("reviewer")
	if err != nil {
		return result, err
	}
	viewerEmail := "historical-cfo-viewer-" + strings.ReplaceAll(runID, "-", "")[:12] + "@qualification.invalid"
	viewerOutput, err := api("createPrincipal", map[string]any{
		"email": viewerEmail, "displayName": "Historical CFO viewer",
	}, "historical-"+runID+"-viewer")
	if err != nil {
		return result, err
	}
	var viewer qualificationHistoricalPrincipalCreate
	if json.Unmarshal(viewerOutput, &viewer) != nil || strings.TrimSpace(viewer.Principal.ID) == "" ||
		strings.TrimSpace(viewer.Principal.Email) != viewerEmail || strings.TrimSpace(viewer.TemporaryPassword) == "" {
		return result, errors.New("predecessor did not return the real CFO viewer account")
	}

	createGrant := func(id, name, kind, resourceID, subjectID, capability string) (qualificationHistoricalGrant, error) {
		body := map[string]any{
			"id": id, "name": name, "resourceKind": kind, "resourceId": resourceID,
			"subjectType": "principal", "subjectId": subjectID,
			"capability": capability, "expectedRevision": bootstrap.AuthorizationPolicyRevision,
		}
		output, callErr := api("createGrant", body, "historical-"+runID+"-grant-"+id, "project="+projectID)
		if callErr != nil {
			return qualificationHistoricalGrant{}, callErr
		}
		var grant qualificationHistoricalGrant
		if json.Unmarshal(output, &grant) != nil || grant.ID != id || grant.ResourceKind != kind ||
			grant.ResourceID != resourceID || grant.SubjectType != "principal" || grant.SubjectID != subjectID ||
			grant.Capability != capability || grant.PolicyRevision <= bootstrap.AuthorizationPolicyRevision {
			return qualificationHistoricalGrant{}, fmt.Errorf("predecessor grant %s did not match its requested identity", id)
		}
		bootstrap.AuthorizationPolicyRevision = grant.PolicyRevision
		return grant, nil
	}
	createRoleBinding := func(id, name, subjectID string, role access.ProjectRole) (qualificationHistoricalRoleBinding, error) {
		body := map[string]any{
			"id": id, "name": name, "subjectType": "principal", "subjectId": subjectID,
			"role": string(role), "expectedRevision": bootstrap.AuthorizationPolicyRevision,
		}
		output, callErr := api("createProjectRoleBinding", body, "historical-"+runID+"-role-binding-"+id, "project="+projectID)
		if callErr != nil {
			return qualificationHistoricalRoleBinding{}, callErr
		}
		var binding qualificationHistoricalRoleBinding
		if json.Unmarshal(output, &binding) != nil || binding.ID != id || binding.SubjectType != "principal" ||
			binding.SubjectID != subjectID || binding.Role != string(role) ||
			!qualificationHistoricalRoleBindingHasCapabilities(binding, access.ProjectRoleCapabilities(role)) ||
			binding.PolicyRevision <= bootstrap.AuthorizationPolicyRevision {
			return qualificationHistoricalRoleBinding{}, fmt.Errorf("predecessor role binding %s did not match its requested identity and capability bundle", id)
		}
		bootstrap.AuthorizationPolicyRevision = binding.PolicyRevision
		return binding, nil
	}
	grantID := func(role, resourceID, capability string) string {
		safeResource := strings.NewReplacer(":", "-", ".", "-", "_", "-").Replace(resourceID)
		return "historical-" + role + "-" + safeResource + "-" + strings.ToLower(strings.ReplaceAll(capability, "_", "-"))
	}

	// The release identity retains the legacy ProjectAdmin authority required
	// by the schema-32 OAuth profile. The publisher receives resource-scoped
	// capabilities over only the CFO graph, with publish authority restricted
	// to the public dashboard resource.
	reviewerGrant, err := createGrant(
		grantID("reviewer", projectID, string(access.CapabilityProjectAdmin)),
		"Historical release reviewer", string(projectgraph.KindProjectNamespace), projectID,
		reviewerID, string(access.CapabilityProjectAdmin),
	)
	if err != nil {
		return result, err
	}
	publisherRoleBindingID := "historical-" + runID + "-publisher-contributor"
	if _, err := createRoleBinding(
		publisherRoleBindingID, "Historical CFO source contributor", publisherID, access.ProjectRoleContributor,
	); err != nil {
		return result, err
	}
	publisherDeployerRoleBindingID := "historical-" + runID + "-publisher-deployer"
	if _, err := createRoleBinding(
		publisherDeployerRoleBindingID, "Historical CFO delivery publisher", publisherID, access.ProjectRoleDeployer,
	); err != nil {
		return result, err
	}
	reviewerRoleBindingID := "historical-" + runID + "-reviewer-viewer"
	if _, err := createRoleBinding(
		reviewerRoleBindingID, "Historical release evidence reader", reviewerID, access.ProjectRoleViewer,
	); err != nil {
		return result, err
	}

	resources := graph.Resources()
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	publisherGrantIDs := make([]string, 0, len(resources)*3)
	for _, resource := range resources {
		for _, capability := range []access.Capability{
			access.CapabilityResourceUse,
			access.CapabilityResourceRead,
			access.CapabilityResourceEdit,
			access.CapabilityResourcePublish,
		} {
			if !access.SupportsCapability(resource.Kind, capability) {
				continue
			}
			id := grantID("publisher", string(resource.ID), string(capability))
			if _, err := createGrant(id, "Historical CFO publisher", string(resource.Kind), string(resource.ID), publisherID, string(capability)); err != nil {
				return result, err
			}
			publisherGrantIDs = append(publisherGrantIDs, id)
		}
	}
	viewerGrant, err := createGrant(
		grantID("viewer", string(dashboardID), string(access.CapabilityResourceRead)),
		"Historical CFO dashboard reader", string(projectgraph.KindDashboard), string(dashboardID),
		viewer.Principal.ID, string(access.CapabilityResourceRead),
	)
	if err != nil {
		return result, err
	}
	viewerSemanticUseGrant, err := createGrant(
		grantID("viewer", string(semanticID), string(access.CapabilityResourceUse)),
		"Historical CFO semantic data user", string(projectgraph.KindSemanticModel), string(semanticID),
		viewer.Principal.ID, string(access.CapabilityResourceUse),
	)
	if err != nil {
		return result, err
	}

	// Read back the target-owned policy through the predecessor API so the
	// helper proves that the viewer has exactly dashboard read and semantic
	// model use, without project-wide or physical-resource grants.
	grantList := make([]qualificationHistoricalGrant, 0, len(publisherGrantIDs)+3)
	seenGrantCursors := make(map[string]struct{})
	pageToken := ""
	grantListPages := 0
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		pathValues := []string{"project=" + projectID}
		if pageToken != "" {
			pathValues = append(pathValues, "pageToken="+pageToken)
		}
		grantListOutput, listErr := api("listGrants", nil, "", pathValues...)
		if listErr != nil {
			return result, listErr
		}
		var grantPage struct {
			Items []qualificationHistoricalGrant `json:"items"`
			Page  struct {
				NextCursor string `json:"nextCursor"`
			} `json:"page"`
		}
		if json.Unmarshal(grantListOutput, &grantPage) != nil {
			return result, errors.New("predecessor could not read back the historical authorization policy")
		}
		grantListPages++
		grantList = append(grantList, grantPage.Items...)
		if len(grantList) > 20000 {
			return result, errors.New("predecessor authorization policy exceeded the bounded historical readback size")
		}
		nextCursor := strings.TrimSpace(grantPage.Page.NextCursor)
		if nextCursor == "" {
			pageToken = ""
			break
		}
		if _, duplicate := seenGrantCursors[nextCursor]; duplicate {
			return result, errors.New("predecessor authorization policy repeated a pagination cursor")
		}
		seenGrantCursors[nextCursor] = struct{}{}
		pageToken = nextCursor
		if pageNumber == 99 {
			return result, errors.New("predecessor authorization policy exceeded the bounded pagination count")
		}
	}
	viewerGrants := make([]qualificationHistoricalGrant, 0, 2)
	for _, grant := range grantList {
		if grant.SubjectType == "principal" && grant.SubjectID == viewer.Principal.ID {
			viewerGrants = append(viewerGrants, grant)
		}
	}
	if len(viewerGrants) != 2 ||
		!qualificationHistoricalGrantMatches(viewerGrants, viewerGrant.ID, string(projectgraph.KindDashboard), string(dashboardID), string(access.CapabilityResourceRead)) ||
		!qualificationHistoricalGrantMatches(viewerGrants, viewerSemanticUseGrant.ID, string(projectgraph.KindSemanticModel), string(semanticID), string(access.CapabilityResourceUse)) {
		return result, fmt.Errorf("historical CFO viewer grant readback is not the exact dashboard-read plus finance-semantic-use pair (pages=%d totalGrants=%d viewerGrants=%d dashboardGrantPresent=%t semanticGrantPresent=%t)",
			grantListPages, len(grantList), len(viewerGrants), qualificationHistoricalGrantPresent(grantList, viewerGrant.ID), qualificationHistoricalGrantPresent(grantList, viewerSemanticUseGrant.ID))
	}

	roleBindings := make([]qualificationHistoricalRoleBinding, 0, 4)
	seenRoleBindingCursors := make(map[string]struct{})
	pageToken = ""
	roleBindingListPages := 0
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		pathValues := []string{"project=" + projectID}
		if pageToken != "" {
			pathValues = append(pathValues, "pageToken="+pageToken)
		}
		roleBindingOutput, listErr := api("listProjectRoleBindings", nil, "", pathValues...)
		if listErr != nil {
			return result, listErr
		}
		var roleBindingPage struct {
			Items []qualificationHistoricalRoleBinding `json:"items"`
			Page  struct {
				NextCursor string `json:"nextCursor"`
			} `json:"page"`
		}
		if json.Unmarshal(roleBindingOutput, &roleBindingPage) != nil {
			return result, errors.New("predecessor could not read back the historical project role bindings")
		}
		roleBindingListPages++
		roleBindings = append(roleBindings, roleBindingPage.Items...)
		if len(roleBindings) > 20000 {
			return result, errors.New("predecessor role binding policy exceeded the bounded historical readback size")
		}
		nextCursor := strings.TrimSpace(roleBindingPage.Page.NextCursor)
		if nextCursor == "" {
			pageToken = ""
			break
		}
		if _, duplicate := seenRoleBindingCursors[nextCursor]; duplicate {
			return result, errors.New("predecessor role binding policy repeated a pagination cursor")
		}
		seenRoleBindingCursors[nextCursor] = struct{}{}
		pageToken = nextCursor
		if pageNumber == 99 {
			return result, errors.New("predecessor role binding policy exceeded the bounded pagination count")
		}
	}
	publisherRoleBindings := qualificationHistoricalRoleBindingsForSubject(roleBindings, publisherID)
	reviewerRoleBindings := qualificationHistoricalRoleBindingsForSubject(roleBindings, reviewerID)
	if len(publisherRoleBindings) != 2 ||
		!qualificationHistoricalRoleBindingPresent(publisherRoleBindings, publisherRoleBindingID, "Historical CFO source contributor", publisherID, access.ProjectRoleContributor) ||
		!qualificationHistoricalRoleBindingPresent(publisherRoleBindings, publisherDeployerRoleBindingID, "Historical CFO delivery publisher", publisherID, access.ProjectRoleDeployer) {
		return result, fmt.Errorf("historical CFO publisher role binding readback is not the exact contributor-plus-deployer bundle (pages=%d totalBindings=%d publisherBindings=%d)",
			roleBindingListPages, len(roleBindings), len(publisherRoleBindings))
	}
	if len(reviewerRoleBindings) != 1 ||
		!qualificationHistoricalRoleBindingMatches(reviewerRoleBindings[0], reviewerRoleBindingID, "Historical release evidence reader", reviewerID, access.ProjectRoleViewer) {
		return result, fmt.Errorf("historical release reviewer role binding readback is not the exact viewer bundle (pages=%d totalBindings=%d reviewerBindings=%d)",
			roleBindingListPages, len(roleBindings), len(reviewerRoleBindings))
	}

	return qualificationHistoricalSeed{
		Target: target, TargetID: instance.ID, ProjectID: projectID, Environment: bootstrap.Environment,
		DashboardID:          string(dashboardID),
		PublisherPrincipalID: publisherID, PublisherClientID: publisherID,
		PublisherClientSecret: publisherSecret,
		ReviewerPrincipalID:   reviewerID, ReleaseClientID: reviewerID, ReleaseClientSecret: releaseSecret,
		ViewerPrincipalID: viewer.Principal.ID, ViewerEmail: viewer.Principal.Email,
		ViewerPassword: viewer.TemporaryPassword,
		SourceRoot:     sourceRoot, DataPath: dataPath,
		LegacyViewerGrantIDs:    []string{viewerGrant.ID, viewerSemanticUseGrant.ID},
		LegacyPublisherGrantIDs: publisherGrantIDs, LegacyReviewerGrantID: reviewerGrant.ID,
		LegacyPublisherRoleBindingID:         publisherRoleBindingID,
		LegacyPublisherDeployerRoleBindingID: publisherDeployerRoleBindingID,
		LegacyReviewerRoleBindingID:          reviewerRoleBindingID,
	}, nil
}

func qualificationHistoricalGrantPresent(grants []qualificationHistoricalGrant, expectedID string) bool {
	for _, grant := range grants {
		if grant.ID == expectedID {
			return true
		}
	}
	return false
}

func qualificationHistoricalGrantMatches(grants []qualificationHistoricalGrant, id, resourceKind, resourceID, capability string) bool {
	for _, grant := range grants {
		if grant.ID == id && grant.SubjectType == "principal" &&
			grant.ResourceKind == resourceKind && grant.ResourceID == resourceID &&
			grant.Capability == capability && grant.PolicyRevision > 0 {
			return true
		}
	}
	return false
}

func qualificationHistoricalRoleBindingsForSubject(bindings []qualificationHistoricalRoleBinding, subjectID string) []qualificationHistoricalRoleBinding {
	matching := make([]qualificationHistoricalRoleBinding, 0, 1)
	for _, binding := range bindings {
		if binding.SubjectType == "principal" && binding.SubjectID == subjectID {
			matching = append(matching, binding)
		}
	}
	return matching
}

func qualificationHistoricalRoleBindingMatches(binding qualificationHistoricalRoleBinding, id, name, subjectID string, role access.ProjectRole) bool {
	return binding.ID == id && binding.Name == name && binding.SubjectType == "principal" && binding.SubjectID == subjectID &&
		binding.Role == string(role) && binding.PolicyRevision > 0 &&
		qualificationHistoricalRoleBindingHasCapabilities(binding, access.ProjectRoleCapabilities(role))
}

func qualificationHistoricalRoleBindingPresent(bindings []qualificationHistoricalRoleBinding, id, name, subjectID string, role access.ProjectRole) bool {
	for _, binding := range bindings {
		if qualificationHistoricalRoleBindingMatches(binding, id, name, subjectID, role) {
			return true
		}
	}
	return false
}

func qualificationHistoricalRoleBindingHasCapabilities(binding qualificationHistoricalRoleBinding, expected []access.Capability) bool {
	if len(binding.Capabilities) != len(expected) {
		return false
	}
	for index, capability := range expected {
		if binding.Capabilities[index] != string(capability) {
			return false
		}
	}
	return true
}

func qualificationHistoricalCommandDiagnostic(output []byte, err error, secrets ...string) string {
	diagnostic := append([]byte(nil), output...)
	if err != nil {
		diagnostic = append(diagnostic, []byte("\n"+err.Error())...)
	}
	diagnostic = redactQualificationBytes(diagnostic)
	message := strings.TrimSpace(string(diagnostic))
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	if message == "" {
		return "no diagnostic output"
	}
	if len(message) > 768 {
		message = message[:768]
	}
	return message
}

func qualificationHistoricalCommandNotExecuted(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	start := strings.IndexByte(message, '{')
	end := strings.LastIndexByte(message, '}')
	if start < 0 || end < start {
		return false
	}
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal([]byte(message[start:end+1]), &problem) != nil {
		return false
	}
	return problem.Code == "COMMAND_CONTRACT_NOT_EXECUTED" &&
		problem.Detail == "The command did not execute through the generated command contract."
}

func TestQualificationHistoricalCommandNotExecutedRecognizesOnlyKnownResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "exact predecessor contract response",
			err:  errors.New(`predecessor API returned HTTP 500: {"code":"COMMAND_CONTRACT_NOT_EXECUTED","detail":"The command did not execute through the generated command contract."}`),
			want: true,
		},
		{
			name: "other problem code",
			err:  errors.New(`predecessor API returned HTTP 500: {"code":"INTERNAL","detail":"The command did not execute through the generated command contract."}`),
		},
		{
			name: "other detail",
			err:  errors.New(`predecessor API returned HTTP 500: {"code":"COMMAND_CONTRACT_NOT_EXECUTED","detail":"Something else failed."}`),
		},
		{name: "missing response", err: errors.New("predecessor API returned HTTP 500")},
		{name: "nil error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := qualificationHistoricalCommandNotExecuted(tc.err); got != tc.want {
				t.Fatalf("known predecessor command failure = %t, want %t", got, tc.want)
			}
		})
	}
}

func validateQualificationHistoricalInitialCredentials(output []byte, initial *qualificationHistoricalInitialCredentials) error {
	if initial == nil {
		return errors.New("predecessor initial credentials destination is required")
	}
	if output != nil {
		if err := json.Unmarshal(output, initial); err != nil {
			return errors.New("predecessor admin initialize returned invalid JSON credentials")
		}
	}
	if strings.TrimSpace(initial.Email) == "" || strings.TrimSpace(initial.TemporaryPassword) == "" ||
		strings.TrimSpace(initial.PublisherToken) == "" || strings.TrimSpace(initial.PublisherTokenExpiresAt) == "" {
		return errors.New("predecessor admin initialize returned incomplete credentials")
	}
	initialExpiry, err := time.Parse(time.RFC3339Nano, initial.PublisherTokenExpiresAt)
	if err != nil || !initialExpiry.After(time.Now().UTC()) {
		return errors.New("predecessor admin initialize returned an invalid publisher credential expiry")
	}
	return nil
}

func TestQualificationHistoricalPredecessorInitializationScopesMigratorCredential(t *testing.T) {
	topology := qualificationNativeEnvironmentTopologyFixture()
	command, err := qualificationHistoricalPredecessorInitializationCommand(topology.ControlMigratorURL)
	require.NoError(t, err)
	require.Equal(t, []string{
		"env", "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=" + topology.ControlMigratorURL,
		"leapview", "admin", "initialize", "--format", "json",
	}, command)
	joinedCommand := strings.Join(command, " ")
	require.NotContains(t, joinedCommand, "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL")
	require.NotContains(t, joinedCommand, "LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL")

	servingEnvironment, err := qualificationHistoricalApplicationEnvironment(topology)
	require.NoError(t, err)
	for _, key := range []string{
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL",
	} {
		require.NotContains(t, servingEnvironment, key, "operation credentials must not enter candidate serving environment")
	}

	_, err = qualificationHistoricalPredecessorInitializationCommand("")
	require.ErrorContains(t, err, "control migrator URL is required")
}

func TestQualificationHistoricalPredecessorInitializationErrorWithholdsChildCredentials(t *testing.T) {
	controlMigratorURL := "postgres://leapview_control_migrator:private-$password@postgres.internal/leapview_control?sslmode=verify-full"
	canonicalURL, err := canonicalPostgresConnectionURL(postgresConnection{
		name: "predecessor initialization control migrator", value: controlMigratorURL,
		role: qualificationNativePostgresControlMigratorRole, database: qualificationNativePostgresControlDatabase,
	})
	require.NoError(t, err)
	require.Contains(t, canonicalURL, "%24", "canonical PostgreSQL URLs encode literal dollar signs in credentials")
	childPassword := "historical-one-time-password"
	childOutput := []byte(`{"temporaryPassword":"` + childPassword + `","publisherToken":"historical-bearer-token"}`)
	err = qualificationHistoricalPredecessorInitializationError(
		errors.New("exec failed using "+canonicalURL), childOutput, controlMigratorURL,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exec failed using [REDACTED]")
	require.Contains(t, err.Error(), "child output withheld")
	require.Contains(t, err.Error(), fmt.Sprintf("%d bytes", len(childOutput)))
	require.NotContains(t, err.Error(), controlMigratorURL)
	require.NotContains(t, err.Error(), childPassword)
	require.NotContains(t, err.Error(), "historical-bearer-token")
}

func qualificationHistoricalSyntheticCFOData(t *testing.T) (string, error) {
	t.Helper()
	dataPath, err := os.MkdirTemp(t.TempDir(), "historical-cfo-data-")
	if err != nil {
		return "", fmt.Errorf("create private historical CFO data directory: %w", err)
	}
	if err := os.Chmod(dataPath, 0o700); err != nil {
		return "", fmt.Errorf("secure historical CFO data directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dataPath, "financial-sample.csv"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create historical CFO CSV: %w", err)
	}
	writer := csv.NewWriter(file)
	rows := [][]string{
		{"segment", "country", "product", "discount_band", "units_sold", "manufacturing_price", "sale_price", "gross_sales", "discounts", "net_sales", "cogs", "profit", "date_serial", "month_number", "month_name", "year"},
		{"Government", "Canada", "CFO-Cloud", "High", "920", "155", "350", "322000", "9660", "312340", "142600", "169740", "45292", "1", "January", "2024"},
		{"Enterprise", "United States", "CFO-Analytics", "Medium", "640", "120", "290", "185600", "7424", "178176", "76800", "101376", "45323", "2", "February", "2024"},
		{"Small Business", "United Kingdom", "CFO-Cloud", "Low", "410", "90", "240", "98400", "1968", "96432", "36900", "59532", "45352", "3", "March", "2024"},
		{"Government", "Germany", "CFO-Security", "None", "770", "135", "315", "242550", "0", "242550", "103950", "138600", "45383", "4", "April", "2024"},
		{"Enterprise", "Japan", "CFO-Analytics", "High", "510", "110", "275", "140250", "5610", "134640", "56100", "78540", "45413", "5", "May", "2024"},
		{"Small Business", "Australia", "CFO-Security", "Medium", "355", "85", "225", "79875", "3195", "76680", "30175", "46505", "45444", "6", "June", "2024"},
	}
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			_ = file.Close()
			return "", fmt.Errorf("write historical CFO CSV: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("flush historical CFO CSV: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync historical CFO CSV: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close historical CFO CSV: %w", err)
	}
	return dataPath, nil
}

func projectIDValidate(value string) error {
	parsed, err := projectgraph.NewResourceID(value)
	if err != nil {
		return err
	}
	if parsed.String() != value || !strings.HasPrefix(value, "project:") {
		return errors.New("project UID must use canonical project: identity")
	}
	return nil
}
