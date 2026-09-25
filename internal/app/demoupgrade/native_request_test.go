package demoupgrade

import (
	"encoding/json"
	"strings"
	"testing"
)

func nativeRequestFixture(t *testing.T) NativeRequest {
	t.Helper()
	r := NativeRequest{DeploymentRunID: "456", DeploymentAttempt: "1", Version: 1, PredecessorImage: identity().Predecessor, PredecessorRevision: strings.Repeat("1", 40), CandidateImage: identity().Candidate, CandidateRevision: strings.Repeat("2", 40)}
	r.Qualification.Image = r.CandidateImage
	r.Qualification.Revision = r.CandidateRevision
	r.Qualification.RunID = "123"
	r.Qualification.RunAttempt = "1"
	r.Qualification.Qualified = true
	r.Plan.Mode = "database-upgrade-required"
	r.Plan.CurrentSchema = 28
	r.Plan.CandidateSchema = 30
	r.Plan.PredecessorRevision = r.PredecessorRevision
	r.Plan.CandidateRevision = r.CandidateRevision
	r.Plan.PendingMigrations = []string{"029_agent_configuration.sql", "030_browser_session_client_label.sql"}
	r.Plan.PendingMigrationDigests = map[string]string{"029_agent_configuration.sql": "55d04d342de0391ff743915867f2265e2309d6837b36d9833e6396fcec7d1a47", "030_browser_session_client_label.sql": "cd721999bae6b681f358f7730f683877b571f0da43af66021e4b63279470ee26"}
	raw, err := json.Marshal(map[string]any{"schemaVersion": 1, "image": r.CandidateImage, "digest": "sha256:" + hex64('b'), "registryDigest": "sha256:" + hex64('b'), "attestation": map[string]any{"verified": true, "repository": "flidai/leapview", "workflow": "flidai/leapview/.github/workflows/artifacts.yml", "sourceRevision": r.CandidateRevision}, "sbom": map[string]any{"discoverable": true, "predicateType": "https://spdx.dev/Document/v2.3"}, "vulnerabilityPolicy": map[string]any{"passed": true, "scanner": "trivy", "sha256": hex64('f')}})
	if err != nil {
		t.Fatal(err)
	}
	r.Admission = raw
	return r
}
func TestNativeRequestBindsQualifiedDigestSourceAndReviewedSQL(t *testing.T) {
	r := nativeRequestFixture(t)
	id, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if id.Candidate != r.CandidateImage || id.Target != "app-leapview-demo-02" {
		t.Fatal(id)
	}
}
func TestNativeRequestRejectsUnqualifiedAndUnreviewedChanges(t *testing.T) {
	tests := map[string]func(*NativeRequest){
		"receipt":       func(r *NativeRequest) { r.Qualification.Qualified = false },
		"source":        func(r *NativeRequest) { r.Qualification.Revision = strings.Repeat("3", 40) },
		"digest":        func(r *NativeRequest) { r.Qualification.Image = r.PredecessorImage },
		"future-schema": func(r *NativeRequest) { r.Plan.CandidateSchema = 31 },
		"history":       func(r *NativeRequest) { r.Plan.CurrentSchema = 29 },
		"SQL":           func(r *NativeRequest) { r.Plan.PendingMigrationDigests["029_agent_configuration.sql"] = hex64('a') },
		"engine": func(r *NativeRequest) {
			r.Plan.ChangedCompatibilityPaths = []string{"internal/analytics/duckdb/engine.go"}
		},
		"dependency":           func(r *NativeRequest) { r.Plan.ChangedCompatibilityPaths = []string{"go.sum"} },
		"caller-authorization": func(r *NativeRequest) { r.Plan.MigrationExecutionAuthorized = true },
		"OCI-evidence":         func(r *NativeRequest) { r.Admission = []byte(`{}`) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := nativeRequestFixture(t)
			mutate(&r)
			if _, err := r.Identity(); err == nil {
				t.Fatal("unsafe request accepted")
			}
		})
	}
}
