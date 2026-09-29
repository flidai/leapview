package composectl

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesscli "github.com/flidai/leapview/internal/access/cli"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

const qualificationHistoricalWorkloadLifetime = 5 * time.Minute

func verifyQualificationHistoricalTransitionAuthorization(
	ctx context.Context,
	result qualificationHistoricalCandidateResult,
	seed qualificationHistoricalSeed,
) error {
	return verifyQualificationHistoricalTypedAuthorization(ctx, qualificationHistoricalTypedAuthorizationInput{
		Transport: result.Proxy, Viewer: result.Viewer,
		TargetID: seed.TargetID, ProjectID: seed.ProjectID, DashboardID: seed.DashboardID,
		PublisherClientID: seed.PublisherClientID, PublisherSecret: seed.PublisherClientSecret,
		ReviewerClientID: seed.ReleaseClientID, ReviewerSecret: seed.ReleaseClientSecret,
		CandidateID: result.Transition.CandidateID, PublicationID: result.Transition.PublicationID,
		ApprovalRequestID: result.Transition.ApprovalRequestID, ApprovalRevision: result.ApprovalDecisionRevision,
	})
}

// qualificationHistoricalTypedAuthorizationInput binds every denial probe to
// the real predecessor principal and candidate publication produced by the
// historical-transition fixture. Resource IDs must come from the successful
// transition result so an authorization denial cannot pass because a resource
// was absent.
type qualificationHistoricalTypedAuthorizationInput struct {
	Transport         *qualificationHistoricalTransport
	Viewer            *qualificationHistoricalBrowser
	TargetID          string
	ProjectID         string
	DashboardID       string
	PublisherClientID string
	PublisherSecret   string
	ReviewerClientID  string
	ReviewerSecret    string
	CandidateID       string
	PublicationID     string
	ApprovalRequestID string
	ApprovalRevision  int64
}

// verifyQualificationHistoricalTypedAuthorization obtains short-lived OAuth
// workload tokens for the typed release roles, then probes real candidate API
// and browser routes. All negative probes require exactly 403; 404, 401,
// success, or unrelated failures are not accepted as authorization evidence.
func verifyQualificationHistoricalTypedAuthorization(
	ctx context.Context,
	input qualificationHistoricalTypedAuthorizationInput,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for name, value := range map[string]string{
		"target": input.TargetID, "project": input.ProjectID, "dashboard": input.DashboardID,
		"publisher client": input.PublisherClientID, "publisher secret": input.PublisherSecret,
		"reviewer client": input.ReviewerClientID, "reviewer secret": input.ReviewerSecret,
		"candidate": input.CandidateID, "publication": input.PublicationID,
		"approval request": input.ApprovalRequestID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("historical typed authorization requires the real %s identity", name)
		}
	}
	if input.ApprovalRevision <= 0 {
		return fmt.Errorf("historical typed authorization requires the real positive approval revision")
	}
	if input.Transport == nil || input.Viewer == nil {
		return fmt.Errorf("historical typed authorization requires the candidate transport and authenticated viewer")
	}
	workloadClient, err := qualificationHistoricalWorkloadHTTPClient(input.Transport)
	if err != nil {
		return err
	}
	oauth := accesscli.StandardOAuthClient{HTTPClient: workloadClient}
	origin := qualificationHistoricalOrigin
	publisherToken, err := qualificationHistoricalExchangeWorkloadToken(ctx, oauth, accesscli.WorkloadIdentityRequest{
		Origin: origin, InstanceID: input.TargetID, ProjectID: input.ProjectID,
		ClientID: input.PublisherClientID, ClientSecret: input.PublisherSecret,
		Actions:  []access.Action{access.ActionDeliveryPublish},
		Lifetime: qualificationHistoricalWorkloadLifetime,
	})
	if err != nil {
		return fmt.Errorf("exchange bounded publisher workload token: %w", err)
	}
	if err := validateQualificationHistoricalWorkloadToken(publisherToken, []access.Action{access.ActionDeliveryPublish}, input.ProjectID); err != nil {
		return fmt.Errorf("publisher workload token: %w", err)
	}
	reviewerToken, err := qualificationHistoricalExchangeWorkloadToken(ctx, oauth, accesscli.WorkloadIdentityRequest{
		Origin: origin, InstanceID: input.TargetID, ProjectID: input.ProjectID,
		ClientID: input.ReviewerClientID, ClientSecret: input.ReviewerSecret,
		Actions:  []access.Action{access.ActionDeliveryApprove},
		Lifetime: qualificationHistoricalWorkloadLifetime,
	})
	if err != nil {
		return fmt.Errorf("exchange bounded reviewer workload token: %w", err)
	}
	if err := validateQualificationHistoricalWorkloadToken(reviewerToken, []access.Action{access.ActionDeliveryApprove}, input.ProjectID); err != nil {
		return fmt.Errorf("reviewer workload token: %w", err)
	}
	// These deliberately broader probe credentials carry each denied action as
	// an explicit scope. The resulting 403s therefore exercise the persisted
	// typed role assignments, rather than passing because OAuth omitted the
	// action from the token's attenuation ceiling.
	publisherProbeActions := []access.Action{
		access.ActionDeliveryPublish,
		access.ActionDeliveryApprove,
		access.ActionProjectAccessRead,
	}
	publisherProbeToken, err := qualificationHistoricalExchangeWorkloadToken(ctx, oauth, accesscli.WorkloadIdentityRequest{
		Origin: origin, InstanceID: input.TargetID, ProjectID: input.ProjectID,
		ClientID: input.PublisherClientID, ClientSecret: input.PublisherSecret,
		Actions: publisherProbeActions, Lifetime: qualificationHistoricalWorkloadLifetime,
	})
	if err != nil {
		return fmt.Errorf("exchange publisher authorization-probe token: %w", err)
	}
	if err := validateQualificationHistoricalWorkloadToken(publisherProbeToken, publisherProbeActions, input.ProjectID); err != nil {
		return fmt.Errorf("publisher authorization-probe token: %w", err)
	}
	reviewerProbeActions := []access.Action{
		access.ActionDeliveryApprove,
		access.ActionDeliveryPublish,
		access.ActionDashboardUpdate,
	}
	reviewerProbeToken, err := qualificationHistoricalExchangeWorkloadToken(ctx, oauth, accesscli.WorkloadIdentityRequest{
		Origin: origin, InstanceID: input.TargetID, ProjectID: input.ProjectID,
		ClientID: input.ReviewerClientID, ClientSecret: input.ReviewerSecret,
		Actions: reviewerProbeActions, Lifetime: qualificationHistoricalWorkloadLifetime,
	})
	if err != nil {
		return fmt.Errorf("exchange reviewer authorization-probe token: %w", err)
	}
	if err := validateQualificationHistoricalWorkloadToken(reviewerProbeToken, reviewerProbeActions, input.ProjectID); err != nil {
		return fmt.Errorf("reviewer authorization-probe token: %w", err)
	}
	viewerToken, err := qualificationHistoricalCreateViewerToken(ctx, input.Viewer, input.ProjectID, input.DashboardID)
	if err != nil {
		return fmt.Errorf("create bounded viewer API token through the authenticated settings command: %w", err)
	}

	project := url.PathEscape(input.ProjectID)
	dashboard := url.PathEscape(input.DashboardID)
	candidate := url.PathEscape(input.CandidateID)
	publication := url.PathEscape(input.PublicationID)
	approval := url.PathEscape(input.ApprovalRequestID)
	base := origin + "/api/v1/projects/" + project
	dashboardSummary := base + "/authoring/dashboards/" + dashboard
	dashboardDraft := dashboardSummary + "/draft"
	viewerAPIClient, err := qualificationHistoricalWorkloadHTTPClient(input.Transport)
	if err != nil {
		return err
	}
	if err := qualificationHistoricalExpectStatus(ctx, viewerAPIClient, http.MethodGet, dashboardSummary, viewerToken, http.StatusOK); err != nil {
		return fmt.Errorf("bounded viewer token cannot read the existing dashboard: %w", err)
	}
	if err := qualificationHistoricalExpectForbidden(ctx, viewerAPIClient, http.MethodGet, dashboardDraft, viewerToken, ""); err != nil {
		return fmt.Errorf("bounded viewer token must not read the existing dashboard draft: %w", err)
	}
	checks := []struct {
		name   string
		method string
		path   string
		token  string
		body   string
	}{
		{
			name:   "publisher cannot approve the real publication request",
			method: http.MethodPost,
			path:   base + "/delivery/publications/" + publication + "/approval-requests/" + approval + "/approve",
			token:  publisherProbeToken.AccessToken,
			body:   fmt.Sprintf(`{"expectedRevision":%d}`, input.ApprovalRevision),
		},
		{
			name:   "publisher cannot inspect project administration",
			method: http.MethodGet,
			path:   base + "/role-bindings",
			token:  publisherProbeToken.AccessToken,
		},
		{
			name:   "reviewer cannot publish the real candidate",
			method: http.MethodPost,
			path:   base + "/delivery/candidates/" + candidate + "/publish",
			token:  reviewerProbeToken.AccessToken,
		},
		{
			name:   "reviewer cannot edit the real dashboard",
			method: http.MethodGet,
			path:   dashboardDraft,
			token:  reviewerProbeToken.AccessToken,
		},
	}
	for _, check := range checks {
		if err := qualificationHistoricalExpectForbidden(ctx, workloadClient, check.method, check.path, check.token, check.body); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
	}
	for _, path := range []string{"/admin/storage", "/explore"} {
		status, err := input.Viewer.status(ctx, origin+path)
		if err != nil {
			return fmt.Errorf("viewer request %s: %w", path, err)
		}
		if status != http.StatusForbidden {
			return fmt.Errorf("viewer request %s returned HTTP %d, want 403 on the existing protected page", path, status)
		}
	}
	return nil
}

func qualificationHistoricalExchangeWorkloadToken(
	ctx context.Context,
	oauth accesscli.AuthoringOAuthClient,
	request accesscli.WorkloadIdentityRequest,
) (*oauth2.Token, error) {
	if oauth == nil {
		return nil, fmt.Errorf("OAuth workload client is unavailable")
	}
	return oauth.Workload(ctx, request)
}

// qualificationHistoricalCreateViewerToken uses the supported browser-session
// personal-settings command. Its exact-resource permission is validated by
// the server against the viewer's currently effective authority before it is
// persisted. The resulting token is used only for this bounded authorization
// proof and expires after five minutes.
func qualificationHistoricalCreateViewerToken(
	ctx context.Context,
	browser *qualificationHistoricalBrowser,
	projectID, dashboardID string,
) (string, error) {
	if browser == nil || browser.client == nil {
		return "", fmt.Errorf("authenticated viewer browser is unavailable")
	}
	project := projectgraph.ResourceID(strings.TrimSpace(projectID))
	dashboard := projectgraph.ResourceID(strings.TrimSpace(dashboardID))
	resource, err := access.NewResourceRef(dashboard, projectgraph.KindDashboard)
	if err != nil {
		return "", fmt.Errorf("validate viewer dashboard resource: %w", err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, project, resource)
	if err != nil {
		return "", fmt.Errorf("construct exact viewer permission: %w", err)
	}

	settingsURL := qualificationHistoricalOrigin + "/admin/api-tokens"
	pageResponse, err := browser.get(ctx, settingsURL)
	if err != nil {
		return "", fmt.Errorf("open authenticated API-token settings: %w", err)
	}
	page, readErr := io.ReadAll(io.LimitReader(pageResponse.Body, 1<<20))
	_ = pageResponse.Body.Close()
	if readErr != nil {
		return "", fmt.Errorf("read authenticated API-token settings page: %w", readErr)
	}
	if pageResponse.StatusCode != http.StatusOK {
		return "", fmt.Errorf("authenticated API-token settings page returned HTTP %d", pageResponse.StatusCode)
	}
	csrfToken, err := qualificationHistoricalCSRFToken(page)
	if err != nil {
		return "", fmt.Errorf("read API-token settings CSRF token: %w", err)
	}
	requestID, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate settings request ID: %w", err)
	}
	idempotencyKey, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate settings idempotency key: %w", err)
	}
	tokenName := "historical-transition-viewer-" + uuid.NewString()
	expiresAt := time.Now().UTC().Add(qualificationHistoricalWorkloadLifetime).Truncate(time.Second)
	commandBody, err := json.Marshal(map[string]any{
		"personalTokenCommand": map[string]any{
			"action": "create", "name": tokenName,
			"description": "Short-lived historical transition authorization qualification",
			"permissions": []access.PermissionPair{pair},
			"expiresAt":   expiresAt.Format(time.RFC3339),
		},
	})
	if err != nil {
		return "", fmt.Errorf("encode bounded viewer token command: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		qualificationHistoricalOrigin+"/admin/personal-settings/command?section=api-tokens", bytes.NewReader(commandBody))
	if err != nil {
		return "", fmt.Errorf("build bounded viewer token command: %w", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", qualificationHistoricalOrigin)
	request.Header.Set("Referer", settingsURL)
	request.Header.Set("X-CSRF-Token", csrfToken)
	request.Header.Set("X-Request-ID", requestID.String())
	request.Header.Set("Idempotency-Key", idempotencyKey.String())
	request.Header.Set("X-LeapView-Operation-ID", accessgen.GenUIActionCreateCurrentAPIToken().OperationID())
	response, err := browser.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("send bounded viewer token command: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("bounded viewer token command returned HTTP %d", response.StatusCode)
	}
	secret, savedPair, savedExpiry, err := qualificationHistoricalReadNewToken(response.Body, tokenName)
	if err != nil {
		return "", err
	}
	if savedPair != pair {
		return "", fmt.Errorf("new viewer token did not persist exactly one dashboard.read permission for the selected dashboard")
	}
	remaining := time.Until(savedExpiry)
	if remaining < qualificationHistoricalWorkloadLifetime-10*time.Second || remaining > qualificationHistoricalWorkloadLifetime+10*time.Second {
		return "", fmt.Errorf("new viewer token expiry is outside the requested five-minute lifetime")
	}
	return secret, nil
}

func qualificationHistoricalReadNewToken(body io.Reader, tokenName string) (string, access.PermissionPair, time.Time, error) {
	type tokenRow struct {
		Name              string                  `json:"name"`
		PermissionProfile *string                 `json:"permissionProfile"`
		Permissions       []access.PermissionPair `json:"permissions"`
		ExpiresAt         string                  `json:"expiresAt"`
	}
	var event struct {
		PersonalSettings struct {
			Tokens struct {
				NewToken *string    `json:"newToken"`
				Items    []tokenRow `json:"items"`
			} `json:"tokens"`
		} `json:"personalSettings"`
	}
	scanner := bufio.NewScanner(io.LimitReader(body, 1<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var signals strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		const prefix = "data: signals "
		if strings.HasPrefix(line, prefix) {
			if signals.Len() > 0 {
				signals.WriteByte('\n')
			}
			signals.WriteString(strings.TrimPrefix(line, prefix))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", access.PermissionPair{}, time.Time{}, fmt.Errorf("read bounded viewer token command response: %w", err)
	}
	if signals.Len() == 0 || json.Unmarshal([]byte(signals.String()), &event) != nil {
		return "", access.PermissionPair{}, time.Time{}, fmt.Errorf("bounded viewer token command omitted its signal response")
	}
	if event.PersonalSettings.Tokens.NewToken == nil || strings.TrimSpace(*event.PersonalSettings.Tokens.NewToken) == "" {
		return "", access.PermissionPair{}, time.Time{}, fmt.Errorf("bounded viewer token command omitted the one-time token secret")
	}
	for _, row := range event.PersonalSettings.Tokens.Items {
		if row.Name != tokenName {
			continue
		}
		if row.PermissionProfile == nil || *row.PermissionProfile != access.PermissionCatalogProfile || len(row.Permissions) != 1 {
			return "", access.PermissionPair{}, time.Time{}, fmt.Errorf("new viewer token does not have one typed permission pair")
		}
		expiresAt, err := time.Parse(time.RFC3339, row.ExpiresAt)
		if err != nil {
			return "", access.PermissionPair{}, time.Time{}, fmt.Errorf("new viewer token omitted a valid expiry")
		}
		return *event.PersonalSettings.Tokens.NewToken, row.Permissions[0], expiresAt, nil
	}
	return "", access.PermissionPair{}, time.Time{}, fmt.Errorf("bounded viewer token response omitted its persisted token row")
}

func qualificationHistoricalWorkloadHTTPClient(transport *qualificationHistoricalTransport) (*http.Client, error) {
	proxy, err := url.Parse(strings.TrimSpace(transport.ProxyURL))
	if err != nil || proxy.Scheme == "" || proxy.Host == "" {
		return nil, fmt.Errorf("historical candidate proxy URL is invalid")
	}
	caContents, err := os.ReadFile(transport.CACert)
	if err != nil {
		return nil, fmt.Errorf("read historical candidate CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caContents) {
		return nil, fmt.Errorf("historical candidate CA contains no certificate")
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxy),
			TLSClientConfig: &tls.Config{
				RootCAs: roots, MinVersion: tls.VersionTLS12,
			},
		},
		Timeout: 20 * time.Second,
	}, nil
}

func validateQualificationHistoricalWorkloadToken(token *oauth2.Token, expectedActions []access.Action, expectedProject string) error {
	if token == nil || strings.TrimSpace(token.AccessToken) == "" || !strings.EqualFold(token.TokenType, "Bearer") {
		return fmt.Errorf("OAuth returned no bearer access token")
	}
	if len(expectedActions) == 0 {
		return fmt.Errorf("expected at least one typed workload action")
	}
	now := time.Now()
	remaining := time.Until(token.Expiry)
	if !token.Expiry.After(now) || remaining > qualificationHistoricalWorkloadLifetime+10*time.Second || remaining < qualificationHistoricalWorkloadLifetime-10*time.Second {
		return fmt.Errorf("OAuth token expiry is outside the requested five-minute lifetime")
	}
	expectedScope := make([]string, 0, len(expectedActions))
	for _, action := range expectedActions {
		expectedScope = append(expectedScope, string(action))
	}
	sort.Strings(expectedScope)
	if scope, _ := token.Extra("scope").(string); scope != strings.Join(expectedScope, " ") {
		return fmt.Errorf("OAuth token scope %q does not equal its exact requested actions %q", scope, strings.Join(expectedScope, " "))
	}
	if profile, _ := token.Extra("permission_profile").(string); profile != access.PermissionCatalogProfile {
		return fmt.Errorf("OAuth token did not use the typed permission profile")
	}
	expectedPairs, err := access.ProjectPermissionPairsForActions(projectgraph.ResourceID(expectedProject), expectedActions)
	if err != nil {
		return fmt.Errorf("construct expected workload permission pairs: %w", err)
	}
	rawPairs, ok := token.Extra("permissions").([]any)
	if !ok || len(rawPairs) != len(expectedPairs) {
		return fmt.Errorf("OAuth token has %d permission pairs, want exactly %d", len(rawPairs), len(expectedPairs))
	}
	encodedPairs, err := json.Marshal(rawPairs)
	if err != nil {
		return fmt.Errorf("encode OAuth permission pairs: %w", err)
	}
	var actualPairs []access.PermissionPair
	if err := json.Unmarshal(encodedPairs, &actualPairs); err != nil {
		return fmt.Errorf("decode OAuth typed permission pairs: %w", err)
	}
	expectedByKey := make(map[string]struct{}, len(expectedPairs))
	for _, pair := range expectedPairs {
		expectedByKey[pair.Key()] = struct{}{}
	}
	for _, pair := range actualPairs {
		if pair.Profile != access.PermissionCatalogProfile || pair.Target.ProjectID != projectgraph.ResourceID(expectedProject) {
			return fmt.Errorf("OAuth token permission pair is not bound to the expected typed project")
		}
		if _, exists := expectedByKey[pair.Key()]; !exists {
			return fmt.Errorf("OAuth token includes an unexpected typed permission pair for %q", pair.Action)
		}
		delete(expectedByKey, pair.Key())
	}
	if len(expectedByKey) != 0 {
		return fmt.Errorf("OAuth token omitted one or more requested typed permission pairs")
	}
	return nil
}

func qualificationHistoricalExpectForbidden(ctx context.Context, client *http.Client, method, endpoint, bearerToken, body string) error {
	status, err := qualificationHistoricalRequestStatus(ctx, client, method, endpoint, bearerToken, body)
	if err != nil {
		return err
	}
	if status != http.StatusForbidden {
		return fmt.Errorf("%s %s returned HTTP %d, want 403", method, endpoint, status)
	}
	return nil
}

func qualificationHistoricalExpectStatus(ctx context.Context, client *http.Client, method, endpoint, bearerToken string, expected int) error {
	status, err := qualificationHistoricalRequestStatus(ctx, client, method, endpoint, bearerToken, "")
	if err != nil {
		return err
	}
	if status != expected {
		return fmt.Errorf("%s %s returned HTTP %d, want %d", method, endpoint, status, expected)
	}
	return nil
}

func qualificationHistoricalRequestStatus(ctx context.Context, client *http.Client, method, endpoint, bearerToken, body string) (int, error) {
	var requestBody io.Reader
	if body != "" {
		requestBody = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, requestBody)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-LeapView-Client", "cli")
	if bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if qualificationHistoricalMutationMethod(method) {
		idempotencyKey, keyErr := uuid.NewV7()
		if keyErr != nil {
			return 0, fmt.Errorf("generate command idempotency key: %w", keyErr)
		}
		request.Header.Set("Idempotency-Key", idempotencyKey.String())
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("send authenticated request: %w", err)
	}
	defer response.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if readErr != nil {
		return response.StatusCode, fmt.Errorf("read authenticated response: %w", readErr)
	}
	return response.StatusCode, nil
}

func qualificationHistoricalMutationMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func (browser *qualificationHistoricalBrowser) status(ctx context.Context, endpoint string) (int, error) {
	if browser == nil || browser.client == nil {
		return 0, fmt.Errorf("historical viewer browser is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("build page request: %w", err)
	}
	response, err := browser.client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("send page request: %w", err)
	}
	defer response.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if readErr != nil {
		return response.StatusCode, fmt.Errorf("read page response: %w", readErr)
	}
	return response.StatusCode, nil
}

func TestQualificationHistoricalExpectForbiddenRequiresExact403(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"code":"test"}`)
			}))
			defer server.Close()

			err := qualificationHistoricalExpectForbidden(context.Background(), server.Client(), http.MethodGet, server.URL+"/real-resource", "", "")
			if status == http.StatusForbidden && err != nil {
				t.Fatalf("403 response rejected: %v", err)
			}
			if status != http.StatusForbidden && err == nil {
				t.Fatalf("HTTP %d counted as forbidden authorization evidence", status)
			}
		})
	}
}

func TestQualificationHistoricalAuthenticatedRequestSendsCredentialsAndMutationHeaders(t *testing.T) {
	const bearer = "viewer-token-for-test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+bearer {
			t.Errorf("Authorization = %q, want bearer credential", got)
		}
		if got := r.Header.Get("X-LeapView-Client"); got != "cli" {
			t.Errorf("X-LeapView-Client = %q, want cli", got)
		}
		key, err := uuid.Parse(r.Header.Get("Idempotency-Key"))
		if err != nil || key.Version() != 7 {
			t.Errorf("Idempotency-Key = %q, want UUIDv7", r.Header.Get("Idempotency-Key"))
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Errorf("Content-Type = %q for an empty mutation body, want empty", got)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	if err := qualificationHistoricalExpectForbidden(context.Background(), server.Client(), http.MethodPost,
		server.URL+"/real-candidate/publish", bearer, ""); err != nil {
		t.Fatalf("authenticated bodyless mutation was not recognized as forbidden: %v", err)
	}
}

func TestQualificationHistoricalWorkloadExchangeSendsClientCredentials(t *testing.T) {
	const (
		clientID     = "publisher-client"
		clientSecret = "publisher-secret"
		projectID    = "project:historical-transition"
		targetID     = "instance:qualification"
	)
	actions := []access.Action{access.ActionDeliveryPublish, access.ActionDeliveryApprove, access.ActionDashboardUpdate}
	pairs, err := access.ProjectPermissionPairsForActions(projectgraph.ResourceID(projectID), actions)
	if err != nil {
		t.Fatalf("construct expected token permissions: %v", err)
	}
	var received url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("OAuth method = %s, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse OAuth form: %v", err)
		}
		received = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"access_token": "bounded-test-access-token", "token_type": "Bearer", "expires_in": 300,
			"scope": "dashboard.update delivery.approve delivery.publish", "permission_profile": access.PermissionCatalogProfile,
			"permissions": pairs,
		}); err != nil {
			t.Errorf("encode workload OAuth response: %v", err)
		}
	}))
	defer server.Close()

	token, err := qualificationHistoricalExchangeWorkloadToken(context.Background(), accesscli.StandardOAuthClient{HTTPClient: server.Client()}, accesscli.WorkloadIdentityRequest{
		Origin: server.URL, InstanceID: targetID, ProjectID: projectID,
		ClientID: clientID, ClientSecret: clientSecret, Actions: actions,
		Lifetime: qualificationHistoricalWorkloadLifetime,
	})
	if err != nil {
		t.Fatalf("workload token exchange: %v", err)
	}
	for key, want := range map[string]string{
		"grant_type": "client_credentials", "client_id": clientID, "client_secret": clientSecret,
		"project_id": projectID, "scope": "delivery.publish delivery.approve dashboard.update", "lifetime_seconds": "300",
	} {
		if got := received.Get(key); got != want {
			t.Errorf("OAuth form %s = %q, want %q", key, got, want)
		}
	}
	if err := validateQualificationHistoricalWorkloadToken(token, actions, projectID); err != nil {
		t.Fatalf("exchanged workload token was not exact and bounded: %v", err)
	}
}

func TestQualificationHistoricalWorkloadTokenRequiresExactProbePermissions(t *testing.T) {
	const projectID = "project:historical-transition"
	expected := []access.Action{
		access.ActionDeliveryApprove,
		access.ActionDeliveryPublish,
		access.ActionDashboardUpdate,
	}
	valid := qualificationHistoricalTestOAuthToken(t, projectID, expected)
	if err := validateQualificationHistoricalWorkloadToken(valid, expected, projectID); err != nil {
		t.Fatalf("exact broader authorization-probe token rejected: %v", err)
	}

	withoutDeniedPublish := qualificationHistoricalTestOAuthToken(t, projectID, []access.Action{
		access.ActionDeliveryApprove,
		access.ActionDashboardUpdate,
	})
	if err := validateQualificationHistoricalWorkloadToken(withoutDeniedPublish, expected, projectID); err == nil {
		t.Fatal("token lacking the denied delivery.publish action was accepted for its 403 probe")
	}

	withUnexpectedAuthority := qualificationHistoricalTestOAuthToken(t, projectID, []access.Action{
		access.ActionDeliveryApprove,
		access.ActionDeliveryPublish,
		access.ActionDashboardUpdate,
		access.ActionProjectAccessRead,
	})
	if err := validateQualificationHistoricalWorkloadToken(withUnexpectedAuthority, expected, projectID); err == nil {
		t.Fatal("token with an unrequested project.access.read action was accepted")
	}
}

func qualificationHistoricalTestOAuthToken(t *testing.T, projectID string, actions []access.Action) *oauth2.Token {
	t.Helper()
	pairs, err := access.ProjectPermissionPairsForActions(projectgraph.ResourceID(projectID), actions)
	if err != nil {
		t.Fatalf("construct test permission pairs: %v", err)
	}
	scopes := make([]string, 0, len(actions))
	for _, action := range actions {
		scopes = append(scopes, string(action))
	}
	sort.Strings(scopes)
	extraJSON, err := json.Marshal(map[string]any{
		"scope": strings.Join(scopes, " "), "permission_profile": access.PermissionCatalogProfile,
		"permissions": pairs,
	})
	if err != nil {
		t.Fatalf("encode test OAuth extras: %v", err)
	}
	var extra map[string]any
	if err := json.Unmarshal(extraJSON, &extra); err != nil {
		t.Fatalf("decode test OAuth extras: %v", err)
	}
	return (&oauth2.Token{
		AccessToken: "test-workload-access-token", TokenType: "Bearer",
		Expiry: time.Now().Add(qualificationHistoricalWorkloadLifetime),
	}).WithExtra(extra)
}
