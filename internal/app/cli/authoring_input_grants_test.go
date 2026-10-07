package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/project/graph"
)

func TestDeclaredDevelopmentInputGrantsAreExactVerifiedAndIdempotent(t *testing.T) {
	for _, scenario := range []string{"valid", "tampered", "missing after create", "foreign principal", "foreign connection", "foreign project", "paginated", "operator failure"} {
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
				if r.Method != http.MethodGet {
					t.Error("declared input attempted a public policy mutation")
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
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
			stage := func(_ context.Context, grant access.AuthorizationGrant, expectedRevision int64, operationID string) error {
				creates++
				if scenario == "operator failure" {
					return errors.New("owned runtime unavailable")
				}
				resource, _ := access.NewResourceRef("connection:sample", graph.KindConnection)
				expected, _ := access.NewExactPermissionPair(access.ActionConnectionUpload, graph.ResourceID(project), resource)
				if grant.Name != "" || grant.Subject.ID != principal || grant.Subject.Kind != access.SubjectKindPrincipal || grant.Resource != resource || grant.Capability != "" || grant.PermissionProfile != access.PermissionCatalogProfile || len(grant.Permissions) != 1 || grant.Permissions[0] != expected || expectedRevision != revision || operationID == "" {
					t.Errorf("unexpected offline grant %#v revision %d operation %q", grant, expectedRevision, operationID)
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
				return nil
			}

			err = ensureDeclaredDevelopmentInputGrants(t.Context(), client, target, project, "dev", principal, []string{"connection:sample"}, stage)
			if scenario != "valid" {
				if err == nil {
					t.Fatal("accepted inconsistent grant policy")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = ensureDeclaredDevelopmentInputGrants(t.Context(), client, target, project, "dev", principal, []string{"connection:sample"}, stage); err != nil {
				t.Fatal(err)
			}
			if creates != 1 || len(grants) != 1 {
				t.Fatalf("creates=%d grants=%d want one exact durable grant", creates, len(grants))
			}
		})
	}
}
