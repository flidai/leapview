package composectl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/stretchr/testify/require"
)

type v010CommandArtifactResolver struct{}

func (v010CommandArtifactResolver) ResolveExact(
	context.Context,
	compatibility.V010ArtifactResolutionRequest,
) (compatibility.V010ResolvedArtifact, error) {
	return compatibility.V010ResolvedArtifact{
		Image: compatibility.ReleasedV010Image, ResolvedDigest: strings.TrimPrefix(compatibility.ReleasedV010Image[strings.LastIndex(compatibility.ReleasedV010Image, "@"):], "@"),
		Platform: compatibility.ReleasedV010Platform, PlatformManifestDigest: compatibility.ReleasedV010PlatformManifest,
		ConfigDigest: compatibility.ReleasedV010ConfigDigest, Authenticated: true,
		SourceRepository: compatibility.ReleasedV010SourceRepository, SourceTag: compatibility.ReleasedV010ID,
		Version: "0.1.0", SourceRevision: "5bf4aded574df459e80d81b77d1989ecd4fa7de0",
	}, nil
}

type v010CommandInputs struct {
	policyPath      string
	policySHA256    string
	admissionPath   string
	predecessorPath string
	evidenceDir     string
	policyDocument  []byte
}

func TestV010PreservationQualificationCommandPublishesCompleteOwnerValidatedEvidence(t *testing.T) {
	root := t.TempDir()
	inputs := writeV010CommandInputs(t, root)
	fixture := newV010DockerFixture(t)
	t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
	var stdout bytes.Buffer
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe", Stdout: &stdout,
		qualificationExecutor: qualificationExecutorFunc(fixture.execute),
	})
	require.NoError(t, err)

	command := Command(t.Context(), controller)
	command.SetArgs(v010CommandArguments(inputs))
	require.NoError(t, command.Execute())

	destination := filepath.Join(inputs.evidenceDir, v010QualificationEvidenceName)
	document, err := os.ReadFile(destination)
	require.NoError(t, err)
	evidence, err := compatibility.ValidateV010ReleaseIdentityEvidence(document, inputs.policyDocument)
	require.NoError(t, err)
	require.NotNil(t, evidence.Execution)
	require.NotNil(t, evidence.Execution.Preservation)
	require.NotNil(t, evidence.Execution.FreshCandidate)
	require.True(t, evidence.Execution.FreshCandidate.MutationFree)
	require.Equal(t, compatibility.ReasonDeniedFreshInstallOnly, evidence.Execution.FreshCandidate.Denials[0].ReasonCode)
	info, err := os.Stat(destination)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Contains(t, stdout.String(), destination)
	temporary, err := filepath.Glob(filepath.Join(inputs.evidenceDir, ".*"+v010QualificationEvidenceName+"*"))
	require.NoError(t, err)
	require.Empty(t, temporary)
}

func TestV010ArtifactReviewCommandPublishesPolicyBoundOwnerEvidence(t *testing.T) {
	root := t.TempDir()
	inputs := writeV010CommandInputs(t, root)
	fixture := newV010DockerFixture(t)
	t.Setenv("DOCKER_CONFIG", filepath.Dir(writeV010DockerCredentials(t)))
	destination := filepath.Join(root, "review", "v0.1-reviewed-identity.json")
	var stdout bytes.Buffer
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe", Stdout: &stdout,
		qualificationExecutor: qualificationExecutorFunc(fixture.execute),
	})
	require.NoError(t, err)
	command := Command(t.Context(), controller)
	command.SetArgs([]string{
		"qualify", "v0.1-artifact-review",
		"--transition-policy", inputs.policyPath,
		"--policy-sha256", inputs.policySHA256,
		"--evidence", destination,
	})
	require.NoError(t, command.Execute())

	document, err := os.ReadFile(destination)
	require.NoError(t, err)
	evidence, err := compatibility.ValidateV010ReleaseIdentityEvidence(document, inputs.policyDocument)
	require.NoError(t, err)
	require.Nil(t, evidence.Execution)
	require.Equal(t, inputs.policySHA256, evidence.PolicySHA256)
	require.Equal(t, compatibility.ReleasedV010Image, evidence.Identity.Image)
	require.Contains(t, stdout.String(), destination)
	info, err := os.Stat(destination)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestV010ArtifactReviewCommandRejectsWrongPolicyDigestBeforeRegistryAccess(t *testing.T) {
	root := t.TempDir()
	inputs := writeV010CommandInputs(t, root)
	fixture := newV010DockerFixture(t)
	destination := filepath.Join(root, "wrong-digest-review.json")
	require.NoError(t, os.WriteFile(destination, []byte("stale evidence"), 0o600))
	controller, err := New(Options{
		Root: root, DockerBin: "docker-probe",
		qualificationExecutor: qualificationExecutorFunc(fixture.execute),
	})
	require.NoError(t, err)
	command := Command(t.Context(), controller)
	command.SetArgs([]string{
		"qualify", "v0.1-artifact-review",
		"--transition-policy", inputs.policyPath,
		"--policy-sha256", strings.Repeat("0", 64),
		"--evidence", destination,
	})
	err = command.Execute()
	require.ErrorContains(t, err, "policy digest mismatch")
	require.Empty(t, fixture.requests)
	require.NoFileExists(t, destination)
}

func TestV010PreservationQualificationCommandRejectsUnboundInputsBeforeExecution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *v010CommandInputs) []string
		want   string
	}{
		{name: "missing admission evidence", mutate: func(_ *testing.T, inputs *v010CommandInputs) []string {
			arguments := v010CommandArguments(*inputs)
			return removeV010CommandFlag(arguments, "--candidate-admission")
		}, want: "required flag"},
		{name: "missing predecessor evidence", mutate: func(t *testing.T, inputs *v010CommandInputs) []string {
			inputs.predecessorPath = filepath.Join(t.TempDir(), "missing-predecessor.json")
			return v010CommandArguments(*inputs)
		}, want: "read reviewed v0.1 predecessor evidence"},
		{name: "wrong candidate digest", mutate: func(t *testing.T, inputs *v010CommandInputs) []string {
			var admission map[string]any
			document, err := os.ReadFile(inputs.admissionPath)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(document, &admission))
			admission["digest"] = "sha256:" + strings.Repeat("9", 64)
			require.NoError(t, writeQualificationJSON(inputs.admissionPath, admission))
			return v010CommandArguments(*inputs)
		}, want: "candidate admission does not authorize"},
		{name: "substituted candidate identity", mutate: func(t *testing.T, inputs *v010CommandInputs) []string {
			var admission map[string]any
			document, err := os.ReadFile(inputs.admissionPath)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(document, &admission))
			digest := "sha256:" + strings.Repeat("8", 64)
			admission["image"] = "ghcr.io/flidai/leapview@" + digest
			admission["digest"] = digest
			admission["registryDigest"] = digest
			require.NoError(t, writeQualificationJSON(inputs.admissionPath, admission))
			return v010CommandArguments(*inputs)
		}, want: "candidate admission does not authorize"},
		{name: "wrong policy digest", mutate: func(_ *testing.T, inputs *v010CommandInputs) []string {
			inputs.policySHA256 = strings.Repeat("0", 64)
			return v010CommandArguments(*inputs)
		}, want: "policy digest mismatch"},
		{name: "invalid predecessor evidence", mutate: func(t *testing.T, inputs *v010CommandInputs) []string {
			var evidence map[string]any
			document, err := os.ReadFile(inputs.predecessorPath)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(document, &evidence))
			evidence["artifact"].(map[string]any)["configDigest"] = "sha256:" + strings.Repeat("0", 64)
			require.NoError(t, writeQualificationJSON(inputs.predecessorPath, evidence))
			return v010CommandArguments(*inputs)
		}, want: "validate reviewed v0.1 predecessor evidence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			inputs := writeV010CommandInputs(t, root)
			arguments := test.mutate(t, &inputs)
			fixture := newV010DockerFixture(t)
			controller, err := New(Options{
				Root: root, DockerBin: "docker-probe", qualificationExecutor: qualificationExecutorFunc(fixture.execute),
			})
			require.NoError(t, err)
			command := Command(t.Context(), controller)
			command.SetArgs(arguments)
			err = command.Execute()
			require.ErrorContains(t, err, test.want)
			require.Empty(t, fixture.requests)
			require.NoFileExists(t, filepath.Join(inputs.evidenceDir, v010QualificationEvidenceName))
		})
	}
}

func writeV010CommandInputs(t *testing.T, root string) v010CommandInputs {
	t.Helper()
	policyDocument := v010CandidateBoundTestPolicy(t)
	policyPath := filepath.Join(root, "release-transition-policy.json")
	require.NoError(t, os.WriteFile(policyPath, policyDocument, 0o600))
	policyDigest := sha256.Sum256(policyDocument)
	policy, err := compatibility.ParsePolicy(policyDocument)
	require.NoError(t, err)
	candidate, ok := policy.ReleaseByID(policy.CandidateRelease)
	require.True(t, ok)
	identity := candidate.IdentityForPlatform(compatibility.ReleasedV010Platform)
	digest := identity.Image[strings.LastIndex(identity.Image, "@")+1:]
	admission := map[string]any{
		"schemaVersion": 1, "image": identity.Image, "digest": digest, "registryDigest": digest,
		"attestation": map[string]any{
			"verified": true, "repository": "flidai/leapview",
			"workflow": "flidai/leapview/.github/workflows/release.yml", "sourceRevision": identity.SourceRevision,
		},
		"sbom":                map[string]any{"discoverable": true},
		"vulnerabilityPolicy": map[string]any{"passed": true},
	}
	admissionPath := filepath.Join(root, "assembled-image-admission.json")
	require.NoError(t, writeQualificationJSON(admissionPath, admission))
	predecessor, err := compatibility.VerifyReleasedV010Artifact(t.Context(), compatibility.V010ArtifactVerificationOptions{
		PolicyDocument: policyDocument, Resolver: v010CommandArtifactResolver{},
		Now: func() time.Time { return time.Date(2026, time.July, 13, 15, 45, 27, 0, time.UTC) },
	})
	require.NoError(t, err)
	predecessorDocument, err := compatibility.MarshalV010ReleaseIdentityEvidence(predecessor, policyDocument)
	require.NoError(t, err)
	predecessorPath := filepath.Join(root, "v0.1-reviewed-identity.json")
	require.NoError(t, os.WriteFile(predecessorPath, predecessorDocument, 0o600))
	return v010CommandInputs{
		policyPath: policyPath, policySHA256: hex.EncodeToString(policyDigest[:]),
		admissionPath: admissionPath, predecessorPath: predecessorPath,
		evidenceDir: filepath.Join(root, "qualification-evidence"), policyDocument: policyDocument,
	}
}

func v010CommandArguments(inputs v010CommandInputs) []string {
	return []string{
		"qualify", "v0.1-preservation",
		"--candidate-admission", inputs.admissionPath,
		"--transition-policy", inputs.policyPath,
		"--policy-sha256", inputs.policySHA256,
		"--predecessor-evidence", inputs.predecessorPath,
		"--evidence-dir", inputs.evidenceDir,
	}
}

func removeV010CommandFlag(arguments []string, flag string) []string {
	result := make([]string, 0, len(arguments)-2)
	for index := 0; index < len(arguments); index++ {
		if arguments[index] == flag && index+1 < len(arguments) {
			index++
			continue
		}
		result = append(result, arguments[index])
	}
	return result
}
