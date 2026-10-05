package composectl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQualificationApprovalBindsReturnedScopeAndIndependentReviewer(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "exact approval"},
		{name: "wrong approval", mutate: func(v map[string]any) { v["id"] = "other" }},
		{name: "wrong project", mutate: func(v map[string]any) { v["projectId"] = "other" }},
		{name: "wrong environment", mutate: func(v map[string]any) { v["environment"] = "dev" }},
		{name: "wrong publication", mutate: func(v map[string]any) { v["deploymentId"] = "other" }},
		{name: "wrong request", mutate: func(v map[string]any) { v["requestDigest"] = "other" }},
		{name: "publisher approved", mutate: func(v map[string]any) { v["approvedBy"] = "publisher" }},
		{name: "different reviewer", mutate: func(v map[string]any) { v["approvedBy"] = "other" }},
		{name: "missing reviewer", mutate: func(v map[string]any) { delete(v, "approvedBy") }},
		{name: "still pending", mutate: func(v map[string]any) { v["status"] = "pending" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, http.MethodPost, r.Method)
				body := map[string]any{"id": "approval", "status": "pending", "projectId": "project", "environment": "prod", "deploymentId": "publication", "requestDigest": "sha256:" + strings.Repeat("a", 64), "requestedBy": "publisher", "revision": 1}
				if strings.HasSuffix(r.URL.Path, "/approve") {
					assert.Equal(t, "/api/v1/projects/project/delivery/publications/publication/approval-requests/approval/approve", r.URL.Path)
					assert.Equal(t, "Bearer reviewer-token", r.Header.Get("Authorization"))
					var decision struct {
						ExpectedRevision int64 `json:"expectedRevision"`
					}
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&decision))
					assert.Equal(t, int64(1), decision.ExpectedRevision)
					body["status"] = "approved"
					body["approvedBy"] = "reviewer"
					if test.mutate != nil {
						test.mutate(body)
					}
				} else {
					assert.Equal(t, "/api/v1/projects/project/delivery/publications/publication/approval-requests", r.URL.Path)
					assert.Equal(t, "Bearer author-token", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "application/json")
				if !strings.HasSuffix(r.URL.Path, "/approve") {
					w.WriteHeader(http.StatusCreated)
				}
				assert.NoError(t, json.NewEncoder(w).Encode(body))
			}))
			defer server.Close()
			evidence, err := approveQualificationPublication(t.Context(), server.Client(), qualificationAuthoringOptions{Target: server.URL, ProjectID: "project", Environment: "prod"}, "author-token", "reviewer-token", QualificationPublication{DeploymentID: "publication", PrincipalID: "publisher"}, "reviewer", "test")
			require.Equal(t, 2, calls, "approval error: %v", err)
			if test.mutate != nil {
				require.ErrorContains(t, err, "does not bind the independent reviewer")
				require.Empty(t, evidence)
			} else {
				require.NoError(t, err)
				require.Equal(t, "reviewer", evidence.ApprovedBy)
				require.Equal(t, "publication", evidence.DeploymentID)
			}
		})
	}
}
