package composectl

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationFirstPublicationProfileRejectsNonlocalOrUnverifiedTargets(t *testing.T) {
	valid := qualificationFirstPublicationProfile{
		Root:           "/opt/leapview",
		Image:          "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		ComposeProject: "leapview", CaddyDomain: "localhost", HTTPS: true,
		Target: "https://localhost", Environment: "prod", ExternalPostgres: true,
	}
	require.NoError(t, validateQualificationFirstPublicationProfile(valid))

	for name, mutate := range map[string]func(*qualificationFirstPublicationProfile){
		"other root":                 func(profile *qualificationFirstPublicationProfile) { profile.Root = "/tmp/other" },
		"public domain":              func(profile *qualificationFirstPublicationProfile) { profile.Target = "https://example.com" },
		"http target":                func(profile *qualificationFirstPublicationProfile) { profile.Target = "http://localhost" },
		"alternate host":             func(profile *qualificationFirstPublicationProfile) { profile.CaddyDomain = "qualification.invalid" },
		"https disabled":             func(profile *qualificationFirstPublicationProfile) { profile.HTTPS = false },
		"unexpected compose project": func(profile *qualificationFirstPublicationProfile) { profile.ComposeProject = "other" },
		"mutable image":              func(profile *qualificationFirstPublicationProfile) { profile.Image = "leapview:latest" },
		"bundled postgres":           func(profile *qualificationFirstPublicationProfile) { profile.ExternalPostgres = false },
		"wrong environment":          func(profile *qualificationFirstPublicationProfile) { profile.Environment = "evaluation" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := valid
			mutate(&invalid)
			require.Error(t, validateQualificationFirstPublicationProfile(invalid))
		})
	}
}

func TestQualificationAuthoringAssetsRequireAllRegularProtectedFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Dockerfile.authoring-client", "package.json", "authoring-worker.mjs"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("protected"), 0o600))
	}
	require.NoError(t, validateQualificationAuthoringAssets(root))
	require.NoError(t, os.Remove(filepath.Join(root, "authoring-worker.mjs")))
	require.ErrorContains(t, validateQualificationAuthoringAssets(root), "authoring-worker.mjs")
}

func TestQualificationReadinessStatusUsesLoopbackReadyzStatus(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				require.Equal(t, http.MethodGet, request.Method)
				require.Equal(t, "/readyz", request.URL.Path)
				writer.WriteHeader(status)
			}))
			defer server.Close()
			got, err := qualificationReadinessStatus(t.Context(), server.URL+"/readyz")
			require.NoError(t, err)
			require.Equal(t, status, got)
		})
	}
}

func TestQualificationFirstPublicationEvidenceRejectsSecretFieldsAndValues(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "authoring-report.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"accessToken":"token-value"}`), 0o600))
	require.ErrorContains(t, qualificationEvidenceExcludesSecrets(root, []string{"known-password"}), "secret field")
	require.NoError(t, os.WriteFile(path, []byte(`{"reviewer":"metadata"}`), 0o600))
	require.ErrorContains(t, qualificationEvidenceExcludesSecrets(root, []string{"metadata"}), "credential value")
	require.NoError(t, qualificationEvidenceExcludesSecrets(root, []string{"not-present"}))
}

func TestQualificationFirstPublicationCleanupPreservesPreexistingCredentials(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, qualificationFirstPublicationCredentials)
	require.NoError(t, os.WriteFile(path, []byte("preexisting-private-credential"), 0o600))
	temporaryPath, cleanup, err := newQualificationCredentialWorkspace(root)
	require.NoError(t, err)
	info, err := os.Stat(filepath.Dir(temporaryPath))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	// Simulate an atomic writer which renamed its output, then failed while
	// syncing the directory. Ownership already exists before the write begins.
	require.NoError(t, os.WriteFile(temporaryPath, []byte("new-private-credential"), 0o600))
	require.NoError(t, cleanup())
	require.NoFileExists(t, temporaryPath)
	require.NoDirExists(t, filepath.Dir(temporaryPath))
	require.NoError(t, cleanup())
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "preexisting-private-credential", string(contents))
}

func TestQualificationFirstPublicationReportBindsRequestApprovalAndCommitWithoutSecrets(t *testing.T) {
	report := validFirstPublicationReport()
	require.NoError(t, validateQualificationFirstPublicationReport(report))

	tests := map[string]func(*qualificationFirstPublicationReport){
		"target":                   func(report *qualificationFirstPublicationReport) { report.Request.TargetURL = "https://public.example" },
		"project":                  func(report *qualificationFirstPublicationReport) { report.Request.ProjectID = "project:other" },
		"environment":              func(report *qualificationFirstPublicationReport) { report.Request.Environment = "evaluation" },
		"request result candidate": func(report *qualificationFirstPublicationReport) { report.Publication.CandidateID = "candidate-other" },
		"request result target":    func(report *qualificationFirstPublicationReport) { report.Publication.TargetID = "target-other" },
		"request result plan": func(report *qualificationFirstPublicationReport) {
			report.Publication.PlanDigest = "sha256:" + strings.Repeat("9", 64)
		},
		"commit approval publication binding": func(report *qualificationFirstPublicationReport) { report.Approval.DeploymentID = "publication-other" },
		"reviewer phase order": func(report *qualificationFirstPublicationReport) {
			report.Phases = swapQualificationPhases(report.Phases, "reviewer provisioning", "private candidate preview")
		},
		"distinct reviewer": func(report *qualificationFirstPublicationReport) {
			report.ReviewerPrincipalID = report.PublisherPrincipalID
		},
		"approved by reviewer": func(report *qualificationFirstPublicationReport) {
			report.Approval.ApprovedBy = report.PublisherPrincipalID
		},
		"readiness transition": func(report *qualificationFirstPublicationReport) { report.ReadinessAfter = 503 },
		"secret excluded": func(report *qualificationFirstPublicationReport) {
			report.Assertions.SecretsExcludedFromEvidence = false
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			invalid := validFirstPublicationReport()
			mutate(&invalid)
			require.Error(t, validateQualificationFirstPublicationReport(invalid))
		})
	}
}

func swapQualificationPhases(phases []qualificationPhaseEvidence, first, second string) []qualificationPhaseEvidence {
	result := append([]qualificationPhaseEvidence(nil), phases...)
	firstIndex, secondIndex := -1, -1
	for index, phase := range result {
		if phase.Name == first {
			firstIndex = index
		}
		if phase.Name == second {
			secondIndex = index
		}
	}
	if firstIndex >= 0 && secondIndex >= 0 {
		result[firstIndex], result[secondIndex] = result[secondIndex], result[firstIndex]
	}
	return result
}

func validFirstPublicationReport() qualificationFirstPublicationReport {
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	artifact := "sha256:" + strings.Repeat("c", 64)
	plan := "sha256:" + strings.Repeat("d", 64)
	sourceRevision := "sha256:" + strings.Repeat("b", 64)
	phases := []qualificationPhaseEvidence{
		{Name: "reviewer provisioning", Result: "success"},
		{Name: "native keyring login", Result: "success"},
		{Name: "private candidate preview", Result: "success"},
		{Name: "protected publish", Result: "success"},
	}
	return qualificationFirstPublicationReport{
		SchemaVersion: 1, Scope: "managed-first-publication", Result: "passed",
		Request: qualificationFirstPublicationRequest{
			TargetURL: "https://localhost", ProjectID: qualificationProjectID, Environment: "prod",
			Image: image, ImageSourceRevision: strings.Repeat("1", 40), SourceRevision: sourceRevision,
			CandidateID: "candidate-1", CandidateRevision: 1, TargetID: "target-1",
			PrincipalID: "publisher-principal", ArtifactDigest: artifact,
			ReleaseDigest: "sha256:" + strings.Repeat("f", 64), PlanID: "plan-1", PlanDigest: plan,
		},
		Publication: qualificationFirstPublicationResult{
			CandidateID: "candidate-1", CandidateRevision: 1, TargetID: "target-1",
			PublicationID: "publication-1", PublicationStatus: "committed", GenerationID: "generation-1",
			PrincipalID: "publisher-principal", SourceArtifactDigest: artifact,
			ServingArtifactDigest: "sha256:" + strings.Repeat("9", 64),
			ReleaseDigest:         "sha256:" + strings.Repeat("f", 64), SourceRevision: sourceRevision,
			PlanID: "plan-1", PlanDigest: plan,
		},
		Approval: qualificationApprovalEvidence{
			ID: "approval-1", Status: "approved", ApprovedBy: "reviewer-principal",
			DeploymentID: "publication-1", ProjectID: qualificationProjectID, Environment: "prod",
			RequestDigest: "sha256:" + strings.Repeat("e", 64),
		},
		PublisherPrincipalID: "publisher-principal", ReviewerPrincipalID: "reviewer-principal",
		ReadinessBefore: 503, ReadinessAfter: 200, Phases: phases,
		Assertions: qualificationFirstPublicationAssertions{
			FirstLoginConsumedOnce: true, ReadinessTransitionObserved: true,
			TemporaryCredentialsRemoved: true, SecretsExcludedFromEvidence: true,
		},
	}
}
