package composectl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestQualificationPipelineGrantReadbackRejectsChangedAuthority(t *testing.T) {
	const projectID, principalID, targetID = "project:test", "principal", "target"
	digest := "sha256:" + strings.Repeat("a", 64)
	pair, err := qualificationPipelineRunPermission(projectID)
	if err != nil {
		t.Fatal(err)
	}
	if pair.Target.IncludeFuture || pair.Target.ResourceID != qualificationRefreshPipelineID {
		t.Fatal("workload run permission is not exact")
	}
	for _, mode := range []string{"valid", "different recipient", "future resource", "different pipeline", "stale item", "legacy capability", "extra grant", "other project", "paginated"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer reader" {
					t.Error("readback must use scoped authenticated GET")
				}
				item := map[string]any{"id": qualificationPipelineGrantID, "subjectType": "principal", "subjectId": principalID, "resourceId": qualificationRefreshPipelineID, "resourceKind": "pipeline", "permissionProfile": access.PermissionCatalogProfile, "permissions": []access.PermissionPair{pair}, "policyRevision": 5, "policyDigest": digest}
				items := []any{item}
				response := map[string]any{"targetId": targetID, "projectId": projectID, "environment": "evaluation", "policyRevision": 5, "policyDigest": digest, "page": map[string]string{}}
				switch mode {
				case "different recipient":
					item["subjectId"] = "other"
				case "future resource":
					changed := pair
					changed.Target.IncludeFuture = true
					changed.Target.ResourceID = ""
					item["permissions"] = []access.PermissionPair{changed}
				case "different pipeline":
					changed := pair
					changed.Target.ResourceID = "pipeline:other"
					item["permissions"] = []access.PermissionPair{changed}
				case "stale item":
					item["policyRevision"] = 4
				case "legacy capability":
					item["capability"] = "RESOURCE_USE"
				case "extra grant":
					items = append(items, item)
				case "other project":
					response["projectId"] = "project:other"
				case "paginated":
					response["page"] = map[string]string{"nextCursor": "more"}
				}
				response["items"] = items
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			grant, err := readQualificationPipelineGrant(t.Context(), server.Client(), server.URL, projectID, "evaluation", "reader", principalID, targetID, 5, digest)
			if mode == "valid" {
				if err != nil || len(grant.Permissions) != 1 || grant.Permissions[0] != pair {
					t.Fatalf("exact grant rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("accepted changed grant evidence")
			}
		})
	}
}
