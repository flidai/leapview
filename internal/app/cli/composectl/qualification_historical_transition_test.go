package composectl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/releasecontract"
	"github.com/stretchr/testify/require"
)

func TestQualificationHistoricalTransitionInputsFailClosed(t *testing.T) {
	evidenceDir := privateQualificationTempDir(t)
	valid := map[string]string{
		qualificationHistoricalTransitionRequiredEnv: "1",
		qualificationHistoricalTransitionImageEnv:    "sha256:" + repeatQualificationHex('a', 64),
		qualificationHistoricalTransitionRevisionEnv: qualificationHistoricalPredecessorRevision,
		qualificationHistoricalTransitionEvidenceEnv: filepath.Join(evidenceDir, "transition.json"),
	}
	options, err := readQualificationHistoricalTransitionOptions(valid)
	require.NoError(t, err)
	require.Equal(t, qualificationHistoricalPredecessorRevision, options.CandidateRevision)

	for _, key := range []string{
		qualificationHistoricalTransitionImageEnv,
		qualificationHistoricalTransitionRevisionEnv,
		qualificationHistoricalTransitionEvidenceEnv,
	} {
		t.Run("missing "+key, func(t *testing.T) {
			missing := cloneQualificationEnvironment(valid)
			delete(missing, key)
			_, err := readQualificationHistoricalTransitionOptions(missing)
			require.Error(t, err)
		})
	}
	_, err = readQualificationHistoricalTransitionOptions(map[string]string{
		qualificationHistoricalTransitionRequiredEnv: "0",
	})
	require.Error(t, err)
	invalidFinalMarker := cloneQualificationEnvironment(valid)
	invalidFinalMarker[qualificationHistoricalTransitionFinalEnv] = "yes"
	_, err = readQualificationHistoricalTransitionOptions(invalidFinalMarker)
	require.Error(t, err)

	finalTag := cloneQualificationEnvironment(valid)
	finalTag[qualificationHistoricalTransitionImageEnv] = "ghcr.io/flidai/leapview:main"
	finalTag[qualificationHistoricalTransitionFinalEnv] = "1"
	_, err = readQualificationHistoricalTransitionOptions(finalTag)
	require.Error(t, err)

	finalDigest := cloneQualificationEnvironment(valid)
	finalDigest[qualificationHistoricalTransitionImageEnv] = "ghcr.io/flidai/leapview@sha256:" + repeatQualificationHex('c', 64)
	finalDigest[qualificationHistoricalTransitionFinalEnv] = "1"
	finalDigest[qualificationHistoricalTransitionAdmissionEnv] = filepath.Join(evidenceDir, "admission.json")
	finalDigest["GITHUB_SHA"] = "5a535310a56d890ce298aaf951a9c910873add26"
	options, err = readQualificationHistoricalTransitionOptions(finalDigest)
	require.NoError(t, err)
	require.True(t, options.FinalArtifact)
	require.Equal(t, finalDigest["GITHUB_SHA"], options.AdmissionSourceRevision)
	missingAttestationSource := cloneQualificationEnvironment(finalDigest)
	delete(missingAttestationSource, "GITHUB_SHA")
	_, err = readQualificationHistoricalTransitionOptions(missingAttestationSource)
	require.Error(t, err)
	delete(finalDigest, qualificationHistoricalTransitionAdmissionEnv)
	_, err = readQualificationHistoricalTransitionOptions(finalDigest)
	require.Error(t, err)

	for _, candidate := range []string{"latest", "ghcr.io/flidai/leapview:main", "ghcr.io/flidai/other@sha256:" + repeatQualificationHex('b', 64)} {
		t.Run("reject candidate "+candidate, func(t *testing.T) {
			invalid := cloneQualificationEnvironment(valid)
			invalid[qualificationHistoricalTransitionImageEnv] = candidate
			_, err := readQualificationHistoricalTransitionOptions(invalid)
			require.Error(t, err)
		})
	}
}

func TestQualificationHistoricalCSRFTokenReadsPredecessorPageShell(t *testing.T) {
	for _, test := range []struct {
		name string
		page string
	}{
		{name: "hidden input", page: `<input type="hidden" name="gorilla.csrf.Token" value="form-token">`},
		{name: "page shell meta", page: `<meta name="csrf-token" content="shell-token">`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := qualificationHistoricalCSRFToken([]byte(test.page))
			require.NoError(t, err)
			if test.name == "hidden input" {
				require.Equal(t, "form-token", got)
			} else {
				require.Equal(t, "shell-token", got)
			}
		})
	}
}

func TestQualificationHistoricalTransitionReceiptRequiresEveryCheck(t *testing.T) {
	path := filepath.Join(privateQualificationTempDir(t), "evidence.json")
	checks := qualificationHistoricalTransitionChecks{
		LegacyPublication: true, LegacyViewer: true, TypedPolicyCaptured: true,
		IndependentApproval: true, PublisherNoSelfApproval: true,
		ViewerLeastPrivilege: true, RealPublicationAdapter: true,
		SubsequentDeploy: true,
	}
	partial := checks
	partial.ViewerLeastPrivilege = false
	require.Error(t, writeQualificationHistoricalTransitionReceipt(path, qualificationHistoricalPredecessorRevision, "sha256:"+repeatQualificationHex('a', 64), qualificationHistoricalPredecessorRevision, partial))
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "partial qualification must not leave a pass receipt")

	const candidateRevision = "5a535310a56d890ce298aaf951a9c910873add26"
	mismatchedValidatorPath := filepath.Join(privateQualificationTempDir(t), "wrong-validator.json")
	require.Error(t, writeQualificationHistoricalTransitionReceipt(mismatchedValidatorPath, qualificationHistoricalPredecessorRevision, "sha256:"+repeatQualificationHex('a', 64), candidateRevision, checks))
	_, err = os.Stat(mismatchedValidatorPath)
	require.True(t, os.IsNotExist(err), "candidate receipt cannot be emitted by a different validator source revision")

	require.NoError(t, writeQualificationHistoricalTransitionReceipt(path, candidateRevision, "sha256:"+repeatQualificationHex('a', 64), candidateRevision, checks))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	var receipt qualificationHistoricalTransitionReceipt
	require.NoError(t, json.Unmarshal(contents, &receipt))
	require.Equal(t, 1, receipt.Version)
	require.Equal(t, "passed", receipt.Status)
	require.Equal(t, qualificationHistoricalPredecessorImage, receipt.Predecessor.Image)
	require.Equal(t, qualificationHistoricalPredecessorRevision, receipt.Predecessor.Revision)
	require.Equal(t, qualificationHistoricalPredecessorSchema, receipt.Predecessor.Schema)
	require.Equal(t, releasecontract.LegacyPermissions, receipt.Predecessor.PermissionProfile)
	require.Equal(t, "sha256:"+repeatQualificationHex('a', 64), receipt.Candidate.Image)
	require.Equal(t, candidateRevision, receipt.Candidate.Revision)
	require.Equal(t, int(migrations.CurrentRevision), receipt.Candidate.Schema)
	require.Equal(t, releasecontract.TypedPermissions, receipt.Candidate.PermissionProfile)
	require.True(t, qualificationHistoricalTransitionChecksPassed(receipt.Checks))
}

func TestQualificationHistoricalTransitionAdmissionBindsExactImageAndRevision(t *testing.T) {
	const revision = "5a535310a56d890ce298aaf951a9c910873add26"
	const attestationRevision = "afafafafafafafafafafafafafafafafafafafaf"
	const digest = "sha256:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	options := qualificationHistoricalTransitionOptions{
		CandidateImage:          "ghcr.io/flidai/leapview@" + digest,
		CandidateRevision:       revision,
		AdmissionSourceRevision: attestationRevision,
		FinalArtifact:           true,
	}
	admission := map[string]any{
		"schemaVersion": 1, "image": options.CandidateImage, "digest": digest, "registryDigest": digest,
		"attestation": map[string]any{
			"verified": true, "repository": "flidai/leapview",
			"workflow": "flidai/leapview/.github/workflows/artifacts.yml", "sourceRevision": attestationRevision,
		},
		"sbom": map[string]any{"discoverable": true, "predicateType": "https://spdx.dev/Document/v2.3"},
		"vulnerabilityPolicy": map[string]any{
			"passed": true, "scanner": "trivy", "sha256": repeatQualificationHex('f', 64), "platform": "linux/amd64",
		},
	}
	path := filepath.Join(privateQualificationTempDir(t), "admission.json")
	write := func(value any) {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, encoded, 0o600))
	}
	write(admission)
	require.NoError(t, validateHistoricalTransitionAdmission(path, options))

	for name, mutate := range map[string]func(map[string]any){
		"image": func(value map[string]any) {
			value["image"] = "ghcr.io/flidai/leapview@sha256:" + repeatQualificationHex('a', 64)
		},
		"digest": func(value map[string]any) { value["registryDigest"] = "sha256:" + repeatQualificationHex('a', 64) },
		"workflow": func(value map[string]any) {
			value["attestation"].(map[string]any)["workflow"] = "foreign/workflow.yml"
		},
		"revision": func(value map[string]any) {
			value["attestation"].(map[string]any)["sourceRevision"] = qualificationHistoricalPredecessorRevision
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, err := json.Marshal(admission)
			require.NoError(t, err)
			var changed map[string]any
			require.NoError(t, json.Unmarshal(candidate, &changed))
			mutate(changed)
			write(changed)
			require.Error(t, validateHistoricalTransitionAdmission(path, options))
		})
	}

	symlink := filepath.Join(filepath.Dir(path), "admission-link.json")
	require.NoError(t, os.Symlink(path, symlink))
	require.Error(t, validateHistoricalTransitionAdmission(symlink, options))
}

func privateQualificationTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0o700))
	return directory
}

func cloneQualificationEnvironment(value map[string]string) map[string]string {
	result := make(map[string]string, len(value))
	for key, entry := range value {
		result[key] = entry
	}
	return result
}

func repeatQualificationHex(character byte, count int) string {
	result := make([]byte, count)
	for i := range result {
		result[i] = character
	}
	return string(result)
}
