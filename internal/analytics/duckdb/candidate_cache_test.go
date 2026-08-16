package duckdb

import "testing"

func TestProjectQueryCacheNamespacePartitionsCandidateSecurityBoundaries(t *testing.T) {
	base := ProjectRuntimeConfig{
		SnapshotID: 11, ServingStateID: "state_1", ProjectID: "sales",
		Environment: "prod", SemanticDigest: "semantic", ArtifactDigest: "artifact",
		SourceDataDigest: "data", CandidateID: "cand_1",
		AuthorizationFingerprint: "policy-a", BindingFingerprint: "bindings-a",
	}
	namespace := projectQueryCacheNamespace(base)
	for name, mutate := range map[string]func(*ProjectRuntimeConfig){
		"candidate": func(config *ProjectRuntimeConfig) {
			config.CandidateID = "cand_2"
		},
		"authorization": func(config *ProjectRuntimeConfig) {
			config.AuthorizationFingerprint = "policy-b"
		},
		"binding": func(config *ProjectRuntimeConfig) {
			config.BindingFingerprint = "bindings-b"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if got := projectQueryCacheNamespace(changed); got == namespace {
				t.Fatalf("%s boundary reused candidate cache namespace %q", name, got)
			}
		})
	}
}
