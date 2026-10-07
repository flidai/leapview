package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestDeclaredDevelopmentInputGrantsAreExactVerifiedAndIdempotent(t *testing.T) {
	for _, scenario := range []string{"valid", "tampered", "missing after create", "foreign principal", "foreign connection", "foreign project", "paginated"} {
		t.Run(scenario, func(t *testing.T) {
			const target, project, principal = "local-target", "project:local", "local-owner"
			scope := access.AuthorizationPolicyScope{TargetID: target, ProjectID: project, Environment: "dev"}
			binding, err := access.NewTypedRoleBinding("owner", "Owner", access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principal}, access.PermissionRoleProjectAdmin, graph.ResourceID(project))
			if err != nil {
				t.Fatal(err)
			}
			bindings := []access.RoleBinding{binding}
			var grants []access.AuthorizationGrant
			var revision int64 = 1
			creates := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer local-token" {
					t.Error("missing native credentials")
				}
				isGrant := strings.HasSuffix(r.URL.Path, "/grants")
				if !isGrant && !strings.HasSuffix(r.URL.Path, "/role-bindings") {
					t.Errorf("unexpected route %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if r.Method == http.MethodPost {
					creates++
					var body accessgen.GenSchemaTargetGrantCreateRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !isGrant || body.Capability != nil || body.PermissionProfile == nil || string(*body.PermissionProfile) != access.PermissionCatalogProfile || body.Permissions == nil || len(*body.Permissions) != 1 || body.ResourceId != "connection:sample" || body.ResourceKind != "connection" || body.SubjectId != principal || body.SubjectType != "principal" || body.ExpectedRevision != revision || r.Header.Get("Idempotency-Key") == "" {
						t.Errorf("unexpected declared grant %#v", body)
					}
					encoded, _ := json.Marshal(body.Permissions)
					pairs, err := access.DecodePermissionPairs(encoded)
					if err != nil {
						t.Error(err)
						http.Error(w, "invalid", 400)
						return
					}
					resource, _ := access.NewResourceRef("connection:sample", graph.KindConnection)
					expected, _ := access.NewExactPermissionPair(access.ActionConnectionUpload, graph.ResourceID(project), resource)
					a, _ := json.Marshal(pairs)
					b, _ := json.Marshal([]access.PermissionPair{expected})
					if string(a) != string(b) {
						t.Errorf("grant authority=%s want %s", a, b)
					}
					grant := access.AuthorizationGrant{ID: body.Id, Resource: resource, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principal}, PermissionProfile: access.PermissionCatalogProfile, Permissions: pairs}
					if body.Name != nil {
						grant.Name = *body.Name
					}
					if scenario == "foreign principal" {
						grant.Subject.ID = "another-owner"
					}
					if scenario == "foreign connection" {
						grant.Resource, _ = access.NewResourceRef("connection:other", graph.KindConnection)
						grant.Permissions[0], _ = access.NewExactPermissionPair(access.ActionConnectionUpload, graph.ResourceID(project), grant.Resource)
					}
					if scenario != "missing after create" {
						grants = append(grants, grant)
						revision++
					}
				}
				digest, err := access.AuthorizationPolicyDigest(scope, bindings, grants...)
				if err != nil {
					t.Error(err)
					http.Error(w, "digest", 500)
					return
				}
				if scenario == "tampered" {
					digest = "sha256:" + strings.Repeat("f", 64)
				}
				item := func(g access.AuthorizationGrant) map[string]any {
					if scenario == "foreign project" {
						g.Permissions = append([]access.PermissionPair(nil), g.Permissions...)
						g.Permissions[0], _ = access.NewExactPermissionPair(access.ActionConnectionUpload, "project:other", g.Resource)
					}
					return map[string]any{"id": g.ID, "name": g.Name, "subjectType": g.Subject.Kind, "subjectId": g.Subject.ID, "resourceId": g.Resource.ID(), "resourceKind": g.Resource.Kind(), "permissionProfile": g.PermissionProfile, "permissions": g.Permissions, "policyRevision": revision, "policyDigest": digest}
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					if len(grants) > 0 {
						_ = json.NewEncoder(w).Encode(item(grants[len(grants)-1]))
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{})
					}
					return
				}
				response := map[string]any{"targetId": target, "projectId": project, "environment": "dev", "policyRevision": revision, "policyDigest": digest, "page": map[string]any{}}
				if scenario == "paginated" {
					response["page"] = map[string]any{"nextCursor": "more"}
				}
				items := []any{}
				if isGrant {
					for _, g := range grants {
						items = append(items, item(g))
					}
				} else {
					items = append(items, map[string]any{"id": binding.ID, "name": binding.Name, "subjectType": "principal", "subjectId": principal, "role": binding.PermissionRole, "permissionProfile": binding.PermissionProfile, "permissions": binding.Permissions, "policyRevision": revision, "policyDigest": digest})
				}
				response["items"] = items
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client := accessgen.NewGenClient(capabilityAPITransport{target: server.URL, token: "local-token", client: server.Client()})
			err = ensureDeclaredDevelopmentInputGrants(t.Context(), client, target, project, "dev", principal, []string{"connection:sample"})
			if scenario != "valid" {
				if err == nil {
					t.Fatal("accepted inconsistent grant policy")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = ensureDeclaredDevelopmentInputGrants(t.Context(), client, target, project, "dev", principal, []string{"connection:sample"}); err != nil {
				t.Fatal(err)
			}
			if creates != 1 || len(grants) != 1 {
				t.Fatalf("creates=%d grants=%d want one exact durable grant", creates, len(grants))
			}
		})
	}
}
