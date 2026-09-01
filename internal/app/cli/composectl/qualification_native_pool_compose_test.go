package composectl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/stretchr/testify/require"
)

type qualificationNativePoolFixture struct {
	identity physicalpool.PoolIdentity
	evidence physicalpool.Evidence
	output   []byte
}

func newQualificationNativePoolFixture(t *testing.T) qualificationNativePoolFixture {
	t.Helper()
	compatibility := physicalpool.Compatibility{
		DuckDBRuntime:         "duckdb-1",
		DuckLakeExtension:     "ducklake-1",
		CatalogFormat:         "catalog-1",
		StorageImplementation: "local",
		ObjectNamingContract:  "object-v1",
	}
	identity := physicalpool.PoolIdentity{
		StorageLocation:    filepath.Join(t.TempDir(), "pool"),
		StorageNamespace:   "qualification",
		Region:             "local",
		Tenant:             "qualification",
		EncryptionDomain:   "qualification",
		IsolationBoundary:  "qualification",
		RetentionAuthority: "qualification",
		Compatibility:      compatibility,
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: compatibility, ConformanceVersion: "qualification/v1",
		Checks: []physicalpool.EvidenceCheck{{ID: "storage", Passed: true}},
	})
	require.NoError(t, err)
	artifact := physicalpool.EvidenceArtifact{SchemaVersion: physicalpool.EvidenceArtifactSchemaVersion, Evidence: evidence}
	envelope := struct {
		SchemaVersion int                           `json:"schema_version"`
		Pool          physicalpool.PoolIdentity     `json:"pool"`
		Evidence      physicalpool.EvidenceArtifact `json:"evidence"`
	}{1, identity, artifact}
	encoded, err := json.Marshal(envelope)
	require.NoError(t, err)
	return qualificationNativePoolFixture{identity: identity, evidence: evidence, output: encoded}
}

func qualificationNativePoolBootstrapOutput(
	identity physicalpool.PoolIdentity,
	evidence physicalpool.Evidence,
	applied bool,
) []byte {
	pool, _ := physicalpool.NewPhysicalPool(identity)
	compatibilityDigest, _ := evidence.Compatibility.Digest()
	return []byte(fmt.Sprintf(
		"pool_id: %s\ncompatibility_digest: %s\nevidence_digest: %s\nconformance_version: %s\napplied: %t\n",
		pool.ID, compatibilityDigest, evidence.Digest, evidence.ConformanceVersion, applied,
	))
}

func newQualificationNativePoolController(
	t *testing.T,
	outputs ...[]byte,
) (*Controller, *recordingQualificationExecutor) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte("COMPOSE_HTTPS=0\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, appEnvName), []byte("LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID=\nLEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST=\nLEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL=stale\n"), 0o600))
	executor := &recordingQualificationExecutor{}
	index := 0
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe",
		qualificationExecutor: qualificationExecutorFunc(func(_ context.Context, request qualificationCommandRequest) ([]byte, error) {
			executor.requests = append(executor.requests, request)
			if index >= len(outputs) {
				return nil, fmt.Errorf("unexpected command %d: %v", index, request.Arguments)
			}
			result := outputs[index]
			index++
			return result, nil
		}),
	})
	require.NoError(t, err)
	return controller, executor
}

func TestPrepareQualificationNativePhysicalPoolWritesArtifactsAndExactComposeCommands(t *testing.T) {
	fixture := newQualificationNativePoolFixture(t)
	dryRun := qualificationNativePoolBootstrapOutput(fixture.identity, fixture.evidence, false)
	controller, executor := newQualificationNativePoolController(t, fixture.output, dryRun)
	evidenceDir := filepath.Join(t.TempDir(), "evidence")
	artifacts, err := controller.prepareQualificationNativePhysicalPool(t.Context(), evidenceDir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(evidenceDir, qualificationNativePhysicalPoolIdentityFile), artifacts.PoolPath)
	require.Equal(t, filepath.Join(evidenceDir, qualificationNativePhysicalPoolEvidenceFile), artifacts.EvidencePath)
	require.Equal(t, artifacts.PoolID, environmentValues(string(mustRead(t, filepath.Join(controller.root, appEnvName))))["LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID"])

	poolBytes := mustRead(t, artifacts.PoolPath)
	evidenceBytes := mustRead(t, artifacts.EvidencePath)
	var identity physicalpool.PoolIdentity
	require.NoError(t, json.Unmarshal(poolBytes, &identity))
	require.Equal(t, artifacts.Pool, identity)
	_, err = physicalpool.UnmarshalEvidenceArtifact(evidenceBytes)
	require.NoError(t, err)
	for _, path := range []string{artifacts.PoolPath, artifacts.EvidencePath} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	}

	pool, _ := physicalpool.NewPhysicalPool(fixture.identity)
	compatibilityDigest, _ := fixture.evidence.Compatibility.Digest()
	common := []string{"compose", "--project-directory", controller.root, "--env-file", filepath.Join(controller.root, deploymentEnvName), "--file", filepath.Join(controller.root, "compose.yaml")}
	wantGenerator := append(append([]string(nil), common...), "run", "--rm", "--no-deps", "leapview", "admin", "delivery", "pool", "qualify")
	wantDry := append(append([]string(nil), common...), qualificationNativePhysicalPoolBootstrapArguments(artifacts, false, nil)...)
	require.Len(t, executor.requests, 2)
	require.True(t, slices.Equal(wantGenerator, executor.requests[0].Arguments), executor.requests[0].Arguments)
	require.True(t, slices.Equal(wantDry, executor.requests[1].Arguments), executor.requests[1].Arguments)
	require.Equal(t, string(pool.ID), artifacts.PoolID)
	require.Equal(t, compatibilityDigest, artifacts.CompatibilityDigest)
	require.NotContains(t, string(poolBytes), "postgres://")
}

func TestApplyQualificationNativePhysicalPoolForwardsOnlyOperationNamesAndMatchesDryRun(t *testing.T) {
	fixture := newQualificationNativePoolFixture(t)
	dryRun := qualificationNativePoolBootstrapOutput(fixture.identity, fixture.evidence, false)
	applyRun := qualificationNativePoolBootstrapOutput(fixture.identity, fixture.evidence, true)
	controller, executor := newQualificationNativePoolController(t, fixture.output, dryRun, applyRun)
	artifacts, err := controller.prepareQualificationNativePhysicalPool(t.Context(), filepath.Join(t.TempDir(), "evidence"))
	require.NoError(t, err)
	topology := qualificationNativeEnvironmentTopologyFixture()
	require.NoError(t, controller.applyQualificationNativePhysicalPool(t.Context(), topology, artifacts))

	require.Len(t, executor.requests, 3)
	operation, err := qualificationNativePostgresOperationEnvironment(topology)
	require.NoError(t, err)
	for name, value := range operation {
		require.Equal(t, value, environmentValues(strings.Join(executor.requests[2].Environment, "\n"))[name])
		require.NotContains(t, strings.Join(executor.requests[2].Arguments, " "), value)
	}
	args := executor.requests[2].Arguments
	for name := range operation {
		require.Contains(t, args, name)
	}
	require.NotContains(t, string(mustRead(t, filepath.Join(controller.root, appEnvName))), topology.DuckLakeMigratorURL)
	require.NotContains(t, string(mustRead(t, filepath.Join(controller.root, appEnvName))), "stale")
}

func TestApplyQualificationNativePhysicalPoolRejectsChangedArtifactBeforeCompose(t *testing.T) {
	fixture := newQualificationNativePoolFixture(t)
	controller, executor := newQualificationNativePoolController(t, fixture.output, qualificationNativePoolBootstrapOutput(fixture.identity, fixture.evidence, false), qualificationNativePoolBootstrapOutput(fixture.identity, fixture.evidence, true))
	artifacts, err := controller.prepareQualificationNativePhysicalPool(t.Context(), filepath.Join(t.TempDir(), "evidence"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(artifacts.EvidencePath, []byte(`{"schema_version":1,"evidence":{}}`), 0o644))
	err = controller.applyQualificationNativePhysicalPool(t.Context(), qualificationNativeEnvironmentTopologyFixture(), artifacts)
	require.ErrorContains(t, err, "artifacts changed")
	require.Len(t, executor.requests, 2)
}

func TestApplyQualificationNativePhysicalPoolRejectsChangedBootstrapResult(t *testing.T) {
	fixture := newQualificationNativePoolFixture(t)
	changed := qualificationNativePoolBootstrapOutput(fixture.identity, fixture.evidence, true)
	changed = []byte(strings.Replace(string(changed), fixture.evidence.Digest, "sha256:"+strings.Repeat("d", 64), 1))
	controller, _ := newQualificationNativePoolController(t, fixture.output, qualificationNativePoolBootstrapOutput(fixture.identity, fixture.evidence, false), changed)
	artifacts, err := controller.prepareQualificationNativePhysicalPool(t.Context(), filepath.Join(t.TempDir(), "evidence"))
	require.NoError(t, err)
	err = controller.applyQualificationNativePhysicalPool(t.Context(), qualificationNativeEnvironmentTopologyFixture(), artifacts)
	require.ErrorContains(t, err, "evidence_digest changed")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}
