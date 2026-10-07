package hostinstall

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
)

func nativeRequestFixture(t *testing.T) NativeRequest {
	t.Helper()
	r := NativeRequest{DeploymentRunID: "456", DeploymentAttempt: "1", Version: 1, PredecessorImage: identity().Predecessor, PredecessorRevision: strings.Repeat("1", 40), CandidateImage: identity().Candidate, CandidateRevision: strings.Repeat("2", 40)}
	r.Profile = profileFixture()
	r.Qualification.Image = r.CandidateImage
	r.Qualification.Revision = r.CandidateRevision
	r.Qualification.RunID = "123"
	r.Qualification.RunAttempt = "1"
	r.Qualification.Qualified = true
	r.Plan.Mode = "database-upgrade-required"
	r.Plan.CurrentSchema = 28
	r.Plan.CandidateSchema = int(migrations.CurrentRevision)
	r.Plan.PredecessorRevision = r.PredecessorRevision
	r.Plan.CandidateRevision = r.CandidateRevision
	r.Plan.PendingMigrationDigests = map[string]string{}
	before := SourceCompatibility{PermissionProfile: "leapview.permissions/v1", Schema: 28, Migrations: map[string]string{}, Engines: map[string]string{"github.com/riverqueue/river": "v0.47.0", "github.com/duckdb/duckdb-go/v2": "v2.1.0"}, RolePolicy: hex64('a')}
	after := SourceCompatibility{PermissionProfile: "leapview.permissions/v1", Schema: r.Plan.CandidateSchema, Migrations: map[string]string{}, Engines: before.Engines, RolePolicy: before.RolePolicy}
	files, err := fs.ReadDir(migrations.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		prefix, _, _ := strings.Cut(f.Name(), "_")
		n, err := strconv.Atoi(prefix)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := fs.ReadFile(migrations.MigrationFS(), f.Name())
		if err != nil {
			t.Fatal(err)
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(raw))
		after.Migrations[f.Name()] = hash
		if n <= r.Plan.CurrentSchema {
			before.Migrations[f.Name()] = hash
		} else {
			r.Plan.PendingMigrations = append(r.Plan.PendingMigrations, f.Name())
			r.Plan.PendingMigrationDigests[f.Name()] = hash
		}
	}
	r.Plan.SourceBefore = before
	r.Plan.SourceAfter = after
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
	if id.Candidate != r.CandidateImage || id.Target != r.Profile.ID {
		t.Fatal(id)
	}
}

// The host must reject incomplete source evidence even when both sides report
// the same incomplete engine set and the caller's claimed mode is consistent.
func TestNativeRequestRejectsIncompleteEngineEvidence(t *testing.T) {
	for name, engines := range map[string]map[string]string{
		"missing DuckDB":            {"github.com/riverqueue/river": "v0.47.0"},
		"missing River":             {"github.com/duckdb/duckdb-go/v2": "v2.1.0"},
		"empty engine version":      {"github.com/duckdb/duckdb-go/v2": "", "github.com/riverqueue/river": "v0.47.0"},
		"whitespace engine version": {"github.com/duckdb/duckdb-go/v2": " ", "github.com/riverqueue/river": "v0.47.0"},
		"mutable engine version":    {"github.com/duckdb/duckdb-go/v2": "main", "github.com/riverqueue/river": "v0.47.0"},
		"unknown engine name":       {"": "v1.0.0"},
	} {
		t.Run(name, func(t *testing.T) {
			request := nativeRequestFixture(t)
			request.Plan.SourceBefore.Engines = engines
			request.Plan.SourceAfter.Engines = engines
			if _, err := request.Identity(); err == nil {
				t.Fatal("accepted incomplete immutable engine evidence")
			}
		})
	}
}
func TestNativeRequestRejectsUnqualifiedAndUnreviewedChanges(t *testing.T) {
	tests := map[string]func(*NativeRequest){
		"receipt":       func(r *NativeRequest) { r.Qualification.Qualified = false },
		"source":        func(r *NativeRequest) { r.Qualification.Revision = strings.Repeat("3", 40) },
		"digest":        func(r *NativeRequest) { r.Qualification.Image = r.PredecessorImage },
		"future-schema": func(r *NativeRequest) { r.Plan.CandidateSchema++ },
		"history":       func(r *NativeRequest) { r.Plan.CurrentSchema = 29 },
		"SQL":           func(r *NativeRequest) { r.Plan.PendingMigrationDigests["029_agent_configuration.sql"] = hex64('a') },
		"engine": func(r *NativeRequest) {
			r.Plan.CompatibilityChanges = []string{"internal/analytics/duckdb/engine.go"}
		},
		"dependency":           func(r *NativeRequest) { r.Plan.CompatibilityChanges = []string{"go.sum"} },
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

func profileFixture() MaintenanceProfile {
	return MaintenanceProfile{Version: 1, ID: "instance-a", Hostname: "host-a", Root: "/opt/instance-a", StateRoot: "/etc/instance-a-recovery", Project: "leapview-cfo", AppService: "leapview", ProxyService: "caddy", Postgres: nativePG, PostgresImage: "docker.io/library/postgres:18-alpine@sha256:" + hex64('d'), Network: "leapview-cfo_default", Origin: "https://demo.leapview.dev", HTTPBinding: "127.0.0.1:8082", HTTPSBinding: "127.0.0.1:8443", RehearsalBinding: "127.0.0.1:8444", Volumes: map[string]string{"postgres": "pg-data", "home": "home-data", "caddy-data": "proxy-data", "caddy-config": "proxy-config"}}
}

const nativePG = "demo02-postgres-cfo"
const nativeApp = "leapview-cfo-leapview-1"
const nativeCaddy = "leapview-cfo-caddy-1"
const nativeNetwork = "leapview-cfo_default"

const nativePGImage = "docker.io/library/postgres:18-alpine@sha256:63bdc97d67b5133bf0e5ebd500bec6d046fa851dc81340d838f0347e616107e8"

func TestNativeRequestSeparatelyBindsProtectedWorkflowAndImageRevision(t *testing.T) {
	r := nativeRequestFixture(t)
	original, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	r.CandidateAttestationRevision = strings.Repeat("3", 40)
	if _, err := r.Identity(); err == nil {
		t.Fatal("accepted admission from a different protected workflow revision")
	}
	r.Admission = []byte(strings.Replace(string(r.Admission), r.CandidateRevision, r.CandidateAttestationRevision, 1))
	bound, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if bound.ArtifactAdmissionDigest == original.ArtifactAdmissionDigest {
		t.Fatal("operation identity omitted the protected workflow revision")
	}
	if r.Qualification.Revision != r.CandidateRevision {
		t.Fatal("image qualification identity was replaced by the signing workflow identity")
	}
	r.CandidateAttestationRevision = "main"
	if _, err := r.Identity(); err == nil {
		t.Fatal("accepted a mutable protected workflow revision")
	}
}
