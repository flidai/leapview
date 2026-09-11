package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
)

func serveBootstrapOwnerBinding(t *testing.T, w http.ResponseWriter, r *http.Request, targetID, projectID, environment, principalID string) bool {
	t.Helper()
	if r.URL.EscapedPath() != "/api/v1/projects/"+url.PathEscape(projectID)+"/role-bindings" {
		return false
	}
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer instance-admin" || r.Header.Get("Idempotency-Key") == "" {
		t.Errorf("owner binding request = %s %s headers=%v", r.Method, r.URL.String(), r.Header)
		http.Error(w, "invalid owner binding request", http.StatusBadRequest)
		return true
	}
	var input accessgen.GenSchemaRoleBindingCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		t.Errorf("decode owner binding request: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return true
	}
	if input.Id != bootstrapOwnerBindingID || input.Name == nil || *input.Name != bootstrapOwnerBindingName || input.SubjectType != "principal" || input.SubjectId != principalID || input.Role != "admin" || input.ExpectedRevision != 0 {
		t.Errorf("owner binding request = %#v", input)
		http.Error(w, "incompatible owner binding", http.StatusBadRequest)
		return true
	}
	capabilities := access.ProjectRoleCapabilities(access.ProjectRoleAdmin)
	digest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, []access.RoleBinding{{
		ID: input.Id, Name: *input.Name, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, Role: access.ProjectRoleAdmin, Capabilities: capabilities,
	}})
	if err != nil {
		t.Fatal(err)
	}
	encodedCapabilities := make([]string, len(capabilities))
	for index, capability := range capabilities {
		encodedCapabilities[index] = string(capability)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": input.Id, "name": *input.Name, "subjectType": input.SubjectType, "subjectId": input.SubjectId,
		"role": input.Role, "capabilities": encodedCapabilities, "policyRevision": 1, "policyDigest": digest,
	})
	return true
}

func TestBootstrapProjectPersistsAuthorityBeforeTargetRequests(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if _, err := os.Stat(configPath); err != nil {
			t.Errorf("target request %d arrived before issuer state was saved: %v", requestCount, err)
		}
		if r.URL.Path == "/api/v1/instance" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"lvinst_test","canonicalOrigin":"http://` + r.Host + `","environment":"production"}`))
			return
		}
		if serveBootstrapOwnerBinding(t, w, r, "lvinst_test", "project:issued", "production", "admin") {
			return
		}
		if r.URL.Path != "/api/v1/instance/project-claim" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projectUid":"project:issued","environment":"production","claimedBy":"admin","claimedAt":"2026-09-06T00:00:00Z"}`))
	}))
	defer server.Close()

	var output strings.Builder
	command := bootstrapProjectCommand(context.Background(), &rootOptions{token: "instance-admin"})
	command.SetOut(&output)
	command.SetArgs([]string{server.URL, "--token", "instance-admin", "--project-uid", "project:issued", "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var event struct {
		SchemaVersion               int    `json:"schemaVersion"`
		Type                        string `json:"type"`
		Target                      string `json:"target"`
		ProjectUID                  string `json:"projectUid"`
		Environment                 string `json:"environment"`
		AuthorizationPolicyRevision int64  `json:"authorizationPolicyRevision"`
		AuthorizationPolicyDigest   string `json:"authorizationPolicyDigest"`
	}
	if err := json.Unmarshal([]byte(output.String()), &event); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != 1 || event.Type != "projectBootstrapped" || event.Target != server.URL || event.ProjectUID != "project:issued" || event.Environment != "production" || event.AuthorizationPolicyRevision != 1 || event.AuthorizationPolicyDigest == "" {
		t.Fatalf("bootstrap event = %#v", event)
	}
	authority, err := cliapi.NewProfileStore(configPath).ResolveProjectAuthority("project:issued", validateProjectAuthorityResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if authority.ProjectUID != event.ProjectUID {
		t.Fatalf("authority ProjectUID = %q, event = %q", authority.ProjectUID, event.ProjectUID)
	}
}

func TestBootstrapProjectOwnerPolicyVerifiesExistingPolicyWithoutReplacingIt(t *testing.T) {
	const targetID, projectID, environment, principalID = "lvinst_existing", "project:existing", "production", "principal-existing"
	capabilities := access.ProjectRoleCapabilities(access.ProjectRoleOwner)
	binding := access.RoleBinding{ID: "existing-owner", Name: "Existing owner", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, Role: access.ProjectRoleOwner, Capabilities: capabilities}
	digest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, []access.RoleBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	var postCount, getCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			postCount++
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Conflict", "status": 409, "detail": "policy already exists", "code": "ROLE_BINDING_CONFLICT", "errors": []any{}, "instance": r.URL.Path, "requestId": "request-existing"})
		case http.MethodGet:
			getCount++
			encodedCapabilities := make([]string, len(capabilities))
			for index, capability := range capabilities {
				encodedCapabilities[index] = string(capability)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"targetId": targetID, "projectId": projectID, "environment": environment, "policyRevision": 7, "policyDigest": digest,
				"items": []any{map[string]any{"id": binding.ID, "name": binding.Name, "subjectType": "principal", "subjectId": principalID, "role": "owner", "capabilities": encodedCapabilities, "policyRevision": 7, "policyDigest": digest}},
				"page":  map[string]any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := accessgen.NewGenClient(capabilityAPITransport{target: server.URL, token: "instance-admin", client: server.Client()})
	revision, gotDigest, err := bootstrapProjectOwnerPolicy(t.Context(), client, targetID, projectID, environment, principalID)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 7 || gotDigest != digest || postCount != 1 || getCount != 1 {
		t.Fatalf("verified policy = revision %d digest %q requests %d/%d", revision, gotDigest, postCount, getCount)
	}
}

func TestBootstrapProjectOwnerPolicyRejectsMalformedCreatedEvidence(t *testing.T) {
	capabilities := access.ProjectRoleCapabilities(access.ProjectRoleAdmin)
	encodedCapabilities := make([]string, len(capabilities))
	for index, capability := range capabilities {
		encodedCapabilities[index] = string(capability)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": bootstrapOwnerBindingID, "name": bootstrapOwnerBindingName, "subjectType": "principal", "subjectId": "principal", "role": "admin",
			"capabilities": encodedCapabilities, "policyRevision": 1, "policyDigest": "malformed",
		})
	}))
	defer server.Close()
	client := accessgen.NewGenClient(capabilityAPITransport{target: server.URL, token: "instance-admin", client: server.Client()})
	if _, _, err := bootstrapProjectOwnerPolicy(t.Context(), client, "target", "project", "production", "principal"); err == nil || !strings.Contains(err.Error(), "invalid policy digest") {
		t.Fatalf("bootstrap error = %v, want malformed digest rejection", err)
	}
}

func TestBootstrapProjectRejectsMismatchedTargetResponse(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/instance" {
			_, _ = w.Write([]byte(`{"id":"lvinst_test","canonicalOrigin":"http://` + r.Host + `","environment":"production"}`))
			return
		}
		_, _ = w.Write([]byte(`{"projectUid":"project:replacement","environment":"production","claimedBy":"admin","claimedAt":"2026-09-06T00:00:00Z"}`))
	}))
	defer server.Close()

	command := bootstrapProjectCommand(context.Background(), &rootOptions{token: "instance-admin"})
	command.SetArgs([]string{server.URL, "--token", "instance-admin", "--project-uid", "project:issued"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "returned ProjectUID") {
		t.Fatalf("bootstrap error = %v, want mismatched ProjectUID rejection", err)
	}
	authority, err := cliapi.NewProfileStore(configPath).ResolveProjectAuthority("project:issued", validateProjectAuthorityResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if authority.ProjectUID != "project:issued" {
		t.Fatalf("durable authority changed to %q", authority.ProjectUID)
	}
}

func TestBootstrapProjectReusesAuthorityAcrossTargetsAndRejectsReplacementBeforeHTTP(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
	type claim struct {
		ProjectUID  string `json:"projectUid"`
		IssuerID    string `json:"issuerId"`
		Environment string `json:"environment"`
	}
	newTarget := func(environment string) (*httptest.Server, *claim) {
		var received claim
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/instance" {
				_, _ = w.Write([]byte(`{"id":"lvinst_` + environment + `","canonicalOrigin":"http://` + r.Host + `","environment":"` + environment + `"}`))
				return
			}
			if received.ProjectUID != "" && serveBootstrapOwnerBinding(t, w, r, "lvinst_"+environment, received.ProjectUID, environment, "admin") {
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"projectUid":"` + received.ProjectUID + `","environment":"` + received.Environment + `","claimedBy":"admin","claimedAt":"2026-09-06T00:00:00Z"}`))
		}))
		return server, &received
	}
	development, developmentClaim := newTarget("development")
	defer development.Close()
	production, productionClaim := newTarget("production")
	defer production.Close()

	for _, target := range []string{development.URL, production.URL} {
		command := bootstrapProjectCommand(context.Background(), &rootOptions{})
		command.SetArgs([]string{target, "--token", "instance-admin"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if developmentClaim.ProjectUID == "" || developmentClaim.ProjectUID != productionClaim.ProjectUID || developmentClaim.IssuerID == "" || developmentClaim.IssuerID != productionClaim.IssuerID || developmentClaim.Environment != "development" || productionClaim.Environment != "production" {
		t.Fatalf("claims = %#v, %#v", *developmentClaim, *productionClaim)
	}

	var replacementCalls int
	replacement := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { replacementCalls++ }))
	defer replacement.Close()
	command := bootstrapProjectCommand(context.Background(), &rootOptions{})
	command.SetArgs([]string{replacement.URL, "--token", "instance-admin", "--project-uid", "project:replacement"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "conflicts with durable state") {
		t.Fatalf("replacement error = %v", err)
	}
	if replacementCalls != 0 {
		t.Fatalf("replacement target received %d HTTP calls", replacementCalls)
	}
}

func TestBootstrapProjectRejectsMalformedIdentityResponses(t *testing.T) {
	tests := []struct {
		name, instanceID, instanceEnvironment, projectUID, responseEnvironment, claimedBy, claimedAt, want string
	}{
		{name: "environment mismatch", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "development", claimedBy: "admin", claimedAt: "2026-09-06T00:00:00Z", want: "returned environment"},
		{name: "incomplete instance id", instanceID: "", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "admin", claimedAt: "2026-09-06T00:00:00Z", want: "incomplete bootstrap identity"},
		{name: "incomplete instance environment", instanceID: "lvinst_test", instanceEnvironment: "", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "admin", claimedAt: "2026-09-06T00:00:00Z", want: "incomplete bootstrap identity"},
		{name: "blank claimed by", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "", claimedAt: "2026-09-06T00:00:00Z", want: "non-canonical claimedBy"},
		{name: "noncanonical claimed by", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: " admin ", claimedAt: "2026-09-06T00:00:00Z", want: "non-canonical claimedBy"},
		{name: "invalid claimed at", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "admin", claimedAt: "not-a-time", want: "invalid claimedAt"},
		{name: "zero claimed at", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "admin", claimedAt: "0001-01-01T00:00:00Z", want: "invalid claimedAt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "cli.json")
			t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/instance" {
					_, _ = w.Write([]byte(`{"id":"` + test.instanceID + `","canonicalOrigin":"http://` + r.Host + `","environment":"` + test.instanceEnvironment + `"}`))
					return
				}
				_, _ = w.Write([]byte(`{"projectUid":"` + test.projectUID + `","environment":"` + test.responseEnvironment + `","claimedBy":"` + test.claimedBy + `","claimedAt":"` + test.claimedAt + `"}`))
			}))
			defer server.Close()

			command := bootstrapProjectCommand(context.Background(), &rootOptions{})
			command.SetArgs([]string{server.URL, "--token", "instance-admin", "--project-uid", "project:issued"})
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("bootstrap error = %v, want %q", err, test.want)
			}
		})
	}
}
