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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func serveBootstrapOwnerBinding(t *testing.T, w http.ResponseWriter, r *http.Request, targetID, projectID, environment, principalID string) bool {
	t.Helper()
	if r.URL.EscapedPath() != "/api/v1/projects/"+url.PathEscape(projectID)+"/role-bindings" {
		return false
	}
	if r.Header.Get("Authorization") != "Bearer instance-admin" {
		t.Errorf("owner binding request = %s %s headers=%v", r.Method, r.URL.String(), r.Header)
		http.Error(w, "invalid owner binding request", http.StatusBadRequest)
		return true
	}
	bindings := make([]access.RoleBinding, 0, len(bootstrapBindingSpecs))
	for _, spec := range bootstrapBindingSpecs {
		binding, err := access.NewTypedRoleBinding(spec.id, spec.name, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, spec.role, projectgraph.ResourceID(projectID))
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
	}
	digest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"targetId": targetID, "projectId": projectID, "environment": environment, "policyRevision": int64(len(bindings)), "policyDigest": digest,
			"items": func() []any {
				items := make([]any, 0, len(bindings))
				for _, binding := range bindings {
					items = append(items, map[string]any{
						"id": binding.ID, "name": binding.Name, "subjectType": "principal", "subjectId": principalID,
						"role": string(binding.PermissionRole), "permissionProfile": binding.PermissionProfile, "permissions": binding.Permissions, "policyRevision": int64(len(bindings)), "policyDigest": digest,
					})
				}
				return items
			}(),
			"page": map[string]any{},
		})
		return true
	}
	if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") == "" {
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
	if input.Id != bootstrapOwnerBindingID || input.Name == nil || *input.Name != bootstrapOwnerBindingName || input.SubjectType != "principal" || input.SubjectId != principalID || input.Role != string(access.PermissionRoleProjectAdmin) || input.ExpectedRevision != 0 {
		t.Errorf("owner binding request = %#v", input)
		http.Error(w, "incompatible owner binding", http.StatusBadRequest)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": input.Id, "name": *input.Name, "subjectType": input.SubjectType, "subjectId": input.SubjectId,
		"role": input.Role, "permissionProfile": bindings[0].PermissionProfile, "permissions": bindings[0].Permissions, "policyRevision": 1,
		"policyDigest": func() string {
			ownerDigest, _ := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, bindings[:1])
			return ownerDigest
		}(),
	})
	return true
}

func bootstrapRoleBindingResponse(t *testing.T, binding access.RoleBinding, pairs []access.PermissionPair) accessgen.GenSchemaRoleBindingResponse {
	t.Helper()
	encoded, err := json.Marshal(pairs)
	if err != nil {
		t.Fatal(err)
	}
	var generated []accessgen.GenSchemaPermissionPair
	if err := json.Unmarshal(encoded, &generated); err != nil {
		t.Fatal(err)
	}
	profile := accessgen.GenSchemaPermissionCatalogProfile(binding.PermissionProfile)
	return accessgen.GenSchemaRoleBindingResponse{
		Id: binding.ID, Name: binding.Name, SubjectType: string(binding.Subject.Kind), SubjectId: binding.Subject.ID,
		Role: string(binding.PermissionRole), PermissionProfile: &profile, Permissions: &generated,
		PolicyRevision: 1, PolicyDigest: "sha256:" + strings.Repeat("a", 64),
	}
}

func TestBootstrapTypedCreateResponseRejectsAlteredPermissionPairs(t *testing.T) {
	const targetID, projectID, environment, principalID = "lvinst_pairs", "project:pairs", "production", "principal-pairs"
	binding, err := access.NewTypedRoleBinding(bootstrapOwnerBindingID, bootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleProjectAdmin, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	altered := append([]access.PermissionPair(nil), binding.Permissions...)
	altered[0], altered[1] = altered[1], altered[0]
	response := bootstrapRoleBindingResponse(t, binding, altered)
	if err := validateBootstrapOwnerBinding(response, targetID, projectID, environment, principalID); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("altered bootstrap create response error = %v, want canonical pair rejection", err)
	}
}

func TestBootstrapListedPolicyRejectsPermissionPairsFromAnotherProject(t *testing.T) {
	const targetID, projectID, environment, principalID = "lvinst_pairs_list", "project:pairs-list", "production", "principal-pairs-list"
	binding, err := access.NewTypedRoleBinding(bootstrapOwnerBindingID, bootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleProjectAdmin, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	wrongProject, err := access.NewTypedRoleBinding(bootstrapOwnerBindingID, bootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleProjectAdmin, projectgraph.ResourceID("project:other"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, []access.RoleBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	response := bootstrapRoleBindingResponse(t, binding, wrongProject.Permissions)
	response.PolicyDigest = digest
	policy := accessgen.GenSchemaRoleBindingListResponse{
		TargetId: targetID, ProjectId: projectID, Environment: environment, PolicyRevision: 1, PolicyDigest: digest,
		Items: []accessgen.GenSchemaRoleBindingResponse{response},
	}
	if _, err := validateBootstrapPolicy(policy, targetID, projectID, environment); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("mismatched listed policy error = %v, want canonical pair rejection", err)
	}
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
	if event.SchemaVersion != 1 || event.Type != "projectBootstrapped" || event.Target != server.URL || event.ProjectUID != "project:issued" || event.Environment != "production" || event.AuthorizationPolicyRevision != 3 || event.AuthorizationPolicyDigest == "" {
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
	projectAdmin, err := access.NewTypedRoleBinding(bootstrapOwnerBindingID, bootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleProjectAdmin, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	editor, err := access.NewTypedRoleBinding(bootstrapEditorBindingID, bootstrapEditorBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleEditor, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	releaseOperator, err := access.NewTypedRoleBinding(bootstrapReleaseOperatorBindingID, bootstrapReleaseOperatorBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleReleaseOperator, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	bindings := []access.RoleBinding{binding, projectAdmin, editor, releaseOperator}
	digest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, bindings)
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
			items := make([]any, 0, len(bindings))
			for _, item := range bindings {
				if item.TypedRoleBinding() {
					items = append(items, map[string]any{"id": item.ID, "name": item.Name, "subjectType": "principal", "subjectId": principalID, "role": string(item.PermissionRole), "permissionProfile": item.PermissionProfile, "permissions": item.Permissions, "policyRevision": 7, "policyDigest": digest})
				} else {
					encodedCapabilities := make([]string, len(item.Capabilities))
					for index, capability := range item.Capabilities {
						encodedCapabilities[index] = string(capability)
					}
					items = append(items, map[string]any{"id": item.ID, "name": item.Name, "subjectType": "principal", "subjectId": principalID, "role": string(item.Role), "capabilities": encodedCapabilities, "policyRevision": 7, "policyDigest": digest})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"targetId": targetID, "projectId": projectID, "environment": environment, "policyRevision": 7, "policyDigest": digest,
				"items": items,
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

func TestBootstrapProjectOwnerPolicyAddsTypedDeliveryRolesToLegacyPolicy(t *testing.T) {
	const targetID, projectID, environment, principalID = "lvinst_legacy", "project:legacy", "production", "principal-legacy"
	scope := access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}
	legacy := access.RoleBinding{ID: bootstrapOwnerBindingID, Name: bootstrapOwnerBindingName, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, Role: access.ProjectRoleAdmin, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin)}
	bindings := []access.RoleBinding{legacy}
	revision := int64(7)
	var posts []accessgen.GenSchemaRoleBindingCreateRequest
	encodePolicy := func(w http.ResponseWriter) {
		digest, err := access.AuthorizationPolicyDigest(scope, bindings)
		if err != nil {
			t.Fatal(err)
		}
		items := make([]any, 0, len(bindings))
		for _, item := range bindings {
			if item.TypedRoleBinding() {
				items = append(items, map[string]any{"id": item.ID, "name": item.Name, "subjectType": string(item.Subject.Kind), "subjectId": item.Subject.ID, "role": string(item.PermissionRole), "permissionProfile": item.PermissionProfile, "permissions": item.Permissions, "policyRevision": revision, "policyDigest": digest})
				continue
			}
			capabilities := make([]string, len(item.Capabilities))
			for index, capability := range item.Capabilities {
				capabilities[index] = string(capability)
			}
			items = append(items, map[string]any{"id": item.ID, "name": item.Name, "subjectType": string(item.Subject.Kind), "subjectId": item.Subject.ID, "role": string(item.Role), "capabilities": capabilities, "policyRevision": revision, "policyDigest": digest})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"targetId": targetID, "projectId": projectID, "environment": environment, "policyRevision": revision, "policyDigest": digest, "items": items, "page": map[string]any{}})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			encodePolicy(w)
			return
		}
		var input accessgen.GenSchemaRoleBindingCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		posts = append(posts, input)
		if input.Role == string(access.PermissionRoleProjectAdmin) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Conflict", "status": 409, "detail": "policy already exists", "code": "ROLE_BINDING_CONFLICT", "errors": []any{}, "instance": r.URL.Path, "requestId": "request-legacy"})
			return
		}
		spec := bootstrapBindingSpecs[1]
		if input.Role == string(access.PermissionRoleReleaseOperator) {
			spec = bootstrapBindingSpecs[2]
		}
		created, err := access.NewTypedRoleBinding(spec.id, spec.name, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, spec.role, projectgraph.ResourceID(projectID))
		if err != nil {
			t.Fatal(err)
		}
		if input.ExpectedRevision != revision || input.Id != spec.id {
			t.Errorf("create %s request = %#v at revision %d", spec.role, input, revision)
		}
		bindings = append(bindings, created)
		revision++
		digest, err := access.AuthorizationPolicyDigest(scope, bindings)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": created.ID, "name": created.Name, "subjectType": string(created.Subject.Kind), "subjectId": created.Subject.ID, "role": string(created.PermissionRole), "permissionProfile": created.PermissionProfile, "permissions": created.Permissions, "policyRevision": revision, "policyDigest": digest})
	}))
	defer server.Close()

	client := accessgen.NewGenClient(capabilityAPITransport{target: server.URL, token: "instance-admin", client: server.Client()})
	gotRevision, _, err := bootstrapProjectOwnerPolicy(t.Context(), client, targetID, projectID, environment, principalID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRevision != 9 || len(posts) != 3 || posts[0].Role != string(access.PermissionRoleProjectAdmin) || posts[1].Role != string(access.PermissionRoleEditor) || posts[2].Role != string(access.PermissionRoleReleaseOperator) {
		t.Fatalf("bootstrap role posts = %#v, revision %d", posts, gotRevision)
	}
}

func TestBootstrapProjectOwnerPolicyRepairsCanonicalEmptyRevision(t *testing.T) {
	const targetID, projectID, environment, principalID = "lvinst_empty", "project:empty", "production", "principal-empty"
	scope := access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}
	emptyDigest, err := access.AuthorizationPolicyDigest(scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := access.NewTypedRoleBinding(bootstrapOwnerBindingID, bootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleProjectAdmin, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	ownerDigest, err := access.AuthorizationPolicyDigest(scope, []access.RoleBinding{owner})
	if err != nil {
		t.Fatal(err)
	}
	editor, err := access.NewTypedRoleBinding(bootstrapEditorBindingID, bootstrapEditorBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleEditor, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	releaseOperator, err := access.NewTypedRoleBinding(bootstrapReleaseOperatorBindingID, bootstrapReleaseOperatorBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleReleaseOperator, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	finalBindings := []access.RoleBinding{owner, editor, releaseOperator}
	finalDigest, err := access.AuthorizationPolicyDigest(scope, finalBindings)
	if err != nil {
		t.Fatal(err)
	}
	postCount, getCount := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			postCount++
			var input accessgen.GenSchemaRoleBindingCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Errorf("decode owner binding: %v", err)
			}
			if postCount == 1 {
				if input.ExpectedRevision != 0 {
					t.Errorf("initial expected revision = %d, want 0", input.ExpectedRevision)
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Conflict", "status": 409, "detail": "policy already exists", "code": "ROLE_BINDING_CONFLICT", "errors": []any{}, "instance": r.URL.Path, "requestId": "request-empty"})
				return
			}
			if input.ExpectedRevision != 1 {
				t.Errorf("repair expected revision = %d, want 1", input.ExpectedRevision)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": owner.ID, "name": owner.Name, "subjectType": "principal", "subjectId": principalID, "role": string(owner.PermissionRole),
				"permissionProfile": owner.PermissionProfile, "permissions": owner.Permissions, "policyRevision": 2, "policyDigest": ownerDigest,
			})
		case http.MethodGet:
			getCount++
			items := []any{}
			revision, digest := int64(1), emptyDigest
			if getCount == 2 {
				revision, digest = 3, finalDigest
				for _, item := range finalBindings {
					items = append(items, map[string]any{
						"id": item.ID, "name": item.Name, "subjectType": "principal", "subjectId": principalID, "role": string(item.PermissionRole),
						"permissionProfile": item.PermissionProfile, "permissions": item.Permissions, "policyRevision": revision, "policyDigest": digest,
					})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"targetId": targetID, "projectId": projectID, "environment": environment, "policyRevision": revision, "policyDigest": digest,
				"items": items, "page": map[string]any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := accessgen.NewGenClient(capabilityAPITransport{target: server.URL, token: "instance-admin", client: server.Client()})
	revision, digest, err := bootstrapProjectOwnerPolicy(t.Context(), client, targetID, projectID, environment, principalID)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 3 || digest != finalDigest || postCount != 2 || getCount != 2 {
		t.Fatalf("repaired policy = revision %d digest %q requests %d/%d", revision, digest, postCount, getCount)
	}
}

func TestBootstrapProjectOwnerPolicyReportsCurrentHeadAfterHistoricalReplay(t *testing.T) {
	const targetID, projectID, environment, principalID = "lvinst_replay", "project:replay", "production", "principal-replay"
	owner, err := access.NewTypedRoleBinding(bootstrapOwnerBindingID, bootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleProjectAdmin, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	viewer := access.RoleBinding{ID: "viewer-binding", Name: "Viewer", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "viewer-principal"}, Role: access.ProjectRoleViewer, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleViewer)}
	revisionOneDigest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, []access.RoleBinding{owner})
	if err != nil {
		t.Fatal(err)
	}
	currentDigest, err := access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, []access.RoleBinding{owner, viewer})
	if err != nil {
		t.Fatal(err)
	}
	editor, err := access.NewTypedRoleBinding(bootstrapEditorBindingID, bootstrapEditorBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleEditor, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	releaseOperator, err := access.NewTypedRoleBinding(bootstrapReleaseOperatorBindingID, bootstrapReleaseOperatorBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principalID}, access.PermissionRoleReleaseOperator, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	currentBindings := []access.RoleBinding{owner, viewer, editor, releaseOperator}
	currentDigest, err = access.AuthorizationPolicyDigest(access.AuthorizationPolicyScope{TargetID: targetID, ProjectID: projectID, Environment: environment}, currentBindings)
	if err != nil {
		t.Fatal(err)
	}
	encodeCapabilities := func(values []access.Capability) []string {
		encoded := make([]string, len(values))
		for index, capability := range values {
			encoded[index] = string(capability)
		}
		return encoded
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": owner.ID, "name": owner.Name, "subjectType": "principal", "subjectId": principalID, "role": string(owner.PermissionRole),
				"permissionProfile": owner.PermissionProfile, "permissions": owner.Permissions, "policyRevision": 1, "policyDigest": revisionOneDigest,
			})
		case http.MethodGet:
			items := make([]any, 0, len(currentBindings))
			for _, item := range currentBindings {
				if item.TypedRoleBinding() {
					items = append(items, map[string]any{"id": item.ID, "name": item.Name, "subjectType": "principal", "subjectId": item.Subject.ID, "role": string(item.PermissionRole), "permissionProfile": item.PermissionProfile, "permissions": item.Permissions, "policyRevision": 4, "policyDigest": currentDigest})
				} else {
					items = append(items, map[string]any{"id": item.ID, "name": item.Name, "subjectType": "principal", "subjectId": item.Subject.ID, "role": "viewer", "capabilities": encodeCapabilities(item.Capabilities), "policyRevision": 4, "policyDigest": currentDigest})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"targetId": targetID, "projectId": projectID, "environment": environment, "policyRevision": 4, "policyDigest": currentDigest,
				"items": items,
				"page":  map[string]any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := accessgen.NewGenClient(capabilityAPITransport{target: server.URL, token: "instance-admin", client: server.Client()})
	revision, digest, err := bootstrapProjectOwnerPolicy(t.Context(), client, targetID, projectID, environment, principalID)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 4 || digest != currentDigest {
		t.Fatalf("bootstrap policy = revision %d digest %q, want current revision 4 digest %q", revision, digest, currentDigest)
	}
}

func TestBootstrapProjectOwnerPolicyRejectsMalformedCreatedEvidence(t *testing.T) {
	owner, err := access.NewTypedRoleBinding(bootstrapOwnerBindingID, bootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal"}, access.PermissionRoleProjectAdmin, "project")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": bootstrapOwnerBindingID, "name": bootstrapOwnerBindingName, "subjectType": "principal", "subjectId": "principal", "role": string(owner.PermissionRole),
			"permissionProfile": owner.PermissionProfile, "permissions": owner.Permissions, "policyRevision": 1, "policyDigest": "malformed",
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
