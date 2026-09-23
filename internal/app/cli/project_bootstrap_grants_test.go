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

func TestBootstrapReplayVerifiesResourceGrantsAtSamePolicyRevision(t *testing.T) {
	const target, project, environment, principal = "target-grants", "project:demo", "prod", "principal-admin"
	scope := access.AuthorizationPolicyScope{TargetID: target, ProjectID: project, Environment: environment}
	bindings := make([]access.RoleBinding, 0, len(bootstrapBindingSpecs))
	for _, spec := range bootstrapBindingSpecs {
		binding, err := access.NewTypedRoleBinding(spec.id, spec.name, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: principal}, spec.role, graph.ResourceID(project))
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, binding)
	}
	resource, _ := access.NewResourceRef("dashboard:sales", graph.KindDashboard)
	grant := access.AuthorizationGrant{ID: "demo-read", Name: "Shared reader", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "shared"}, Resource: resource, Capability: access.CapabilityResourceRead}
	digest, err := access.AuthorizationPolicyDigest(scope, bindings, grant)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "stale", "omitted", "tampered", "paginated"} {
		t.Run(mode, func(t *testing.T) {
			grantReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Conflict", "status": 409, "detail": "policy already exists", "code": "ROLE_BINDING_CONFLICT", "errors": []any{}, "instance": r.URL.Path, "requestId": "request-grants"})
					return
				}
				response := map[string]any{"targetId": target, "projectId": project, "environment": environment, "policyRevision": 4, "policyDigest": digest, "page": map[string]any{}}
				if strings.HasSuffix(r.URL.Path, "/grants") {
					grantReads++
					item := map[string]any{"id": grant.ID, "name": grant.Name, "subjectType": "principal", "subjectId": grant.Subject.ID, "resourceId": resource.ID(), "resourceKind": resource.Kind(), "capability": grant.Capability, "policyRevision": 4, "policyDigest": digest}
					response["items"] = []any{item}
					switch mode {
					case "stale":
						response["policyRevision"] = 5
					case "omitted":
						response["items"] = []any{}
					case "tampered":
						item["name"] = "different"
					case "paginated":
						response["page"] = map[string]any{"nextCursor": "next"}
					}
				} else {
					items := make([]any, 0, len(bindings))
					for _, binding := range bindings {
						items = append(items, map[string]any{"id": binding.ID, "name": binding.Name, "subjectType": "principal", "subjectId": principal, "role": string(binding.PermissionRole), "permissionProfile": binding.PermissionProfile, "permissions": binding.Permissions, "policyRevision": 4, "policyDigest": digest})
					}
					response["items"] = items
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client := accessgen.NewGenClient(capabilityAPITransport{target: server.URL, token: "instance-admin", client: server.Client()})
			revision, actual, err := bootstrapProjectOwnerPolicy(t.Context(), client, target, project, environment, principal)
			if mode == "valid" {
				if err != nil || revision != 4 || actual != digest {
					t.Fatalf("replay rejected grants: %d %s %v", revision, actual, err)
				}
			} else if err == nil {
				t.Fatal("accepted inconsistent grant evidence")
			}
			if grantReads != 1 {
				t.Fatalf("grant reads=%d", grantReads)
			}
		})
	}
}
