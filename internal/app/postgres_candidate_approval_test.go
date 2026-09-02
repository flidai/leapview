package app

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

	"github.com/flidai/leapview/internal/access"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/servingstate"
)

const (
	qualificationReviewerFixturePlaceholder = "0198f2c0-7c7a-7f00-8a11-00000000f650"
	qualificationReviewerPrincipalID        = "0198f2c0-7c7a-7f00-8a11-000000000777"
)

type candidateApprovalStateReaderFake struct {
	state    servingstate.State
	artifact servingstate.Artifact
}

func (f candidateApprovalStateReaderFake) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return f.state, nil
}

func (f candidateApprovalStateReaderFake) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return f.artifact, nil
}

func TestCandidateApprovalCapabilitiesUsesQualificationFixtureProjectAdmin(t *testing.T) {
	sourceProjectPath := qualificationEvaluationProjectPath(t)
	projectRoot := filepath.Join(t.TempDir(), "project")
	if err := os.CopyFS(projectRoot, os.DirFS(filepath.Dir(sourceProjectPath))); err != nil {
		t.Fatalf("copy qualification project: %v", err)
	}
	reviewerGrantPath := filepath.Join(projectRoot, "access", "qualification-reviewer-admin.yaml")
	reviewerGrant, err := os.ReadFile(reviewerGrantPath)
	if err != nil {
		t.Fatal(err)
	}
	placeholder := []byte("principalId: " + qualificationReviewerFixturePlaceholder)
	if bytes.Count(reviewerGrant, placeholder) != 1 {
		t.Fatal("qualification reviewer fixture does not contain its identity placeholder")
	}
	reviewerGrant = bytes.Replace(reviewerGrant, placeholder, []byte("principalId: "+qualificationReviewerPrincipalID), 1)
	if err := os.WriteFile(reviewerGrantPath, reviewerGrant, 0o600); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(projectRoot, "leapview.yaml")
	project, err := projectcompiler.Compile(projectPath)
	if err != nil {
		t.Fatalf("compile qualification project: %v", err)
	}
	plan, err := projectcompiler.PlanProject(projectPath)
	if err != nil {
		t.Fatalf("plan qualification project: %v", err)
	}
	var body bytes.Buffer
	manifest, artifactDigest, err := projectbundle.PackCompiledProject(project, plan, &body)
	if err != nil {
		t.Fatalf("pack qualification project: %v", err)
	}
	_, compiled, err := projectbundle.ValidateArtifactBytes(body.Bytes())
	if err != nil {
		t.Fatalf("validate qualification artifact: %v", err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	accessPolicyJSON, err := json.Marshal(compiled.Manifest.Access)
	if err != nil {
		t.Fatal(err)
	}

	const storageDomain = "qualification-test"
	store, err := platformobjectstore.NewMemoryStore(platformobjectstore.MemoryStoreConfig{StorageSecurityDomain: storageDomain})
	if err != nil {
		t.Fatal(err)
	}
	metadataHash := sha256.Sum256(nil)
	metadataDigest := "sha256:" + hex.EncodeToString(metadataHash[:])
	locator := "serving-artifacts/" + strings.TrimPrefix(artifactDigest, "sha256:") + ".tar.gz"
	info, err := store.PutImmutable(t.Context(), locator, bytes.NewReader(body.Bytes()), platformobjectstore.ObjectMetadata{
		StorageSecurityDomain: storageDomain,
		Digest:                artifactDigest,
		SizeBytes:             int64(body.Len()),
		ContentType:           servingstate.ArtifactBundleContentType,
		MetadataDigest:        metadataDigest,
	})
	if err != nil {
		t.Fatal(err)
	}

	generationID := servingstate.ID("0198f2c0-7c7a-7f00-8a11-000000000901")
	artifact := servingstate.Artifact{
		ID:                    "artifact-" + strings.TrimPrefix(artifactDigest, "sha256:"),
		ServingStateID:        generationID,
		Digest:                artifactDigest,
		Format:                servingstate.ArtifactBundleFormat,
		Locator:               locator,
		StorageSecurityDomain: storageDomain,
		ContentType:           servingstate.ArtifactBundleContentType,
		MetadataDigest:        metadataDigest,
		ManifestJSON:          string(manifestJSON),
		SizeBytes:             info.SizeBytes,
	}
	state := servingstate.State{
		ID:               generationID,
		ProjectID:        compiled.ProjectID,
		Environment:      "evaluation",
		Status:           servingstate.StatusValidated,
		Digest:           artifactDigest,
		ProjectDigest:    compiled.ProjectDigest,
		AccessPolicyJSON: string(accessPolicyJSON),
	}
	subjects := func(context.Context, string) ([]access.SubjectRef, error) {
		reviewer, subjectErr := access.NewSubjectRef(access.SubjectKindPrincipal, qualificationReviewerPrincipalID)
		return []access.SubjectRef{reviewer}, subjectErr
	}

	projectID, environment, capabilities, err := candidateApprovalCapabilities(
		t.Context(), candidateApprovalStateReaderFake{state: state, artifact: artifact}, store, subjects,
		string(generationID), qualificationReviewerPrincipalID,
	)
	if err != nil {
		t.Fatalf("candidate approval capabilities: %v", err)
	}
	if projectID != "project:leapview-evaluation" || environment != "evaluation" {
		t.Fatalf("candidate identity = (%q, %q)", projectID, environment)
	}
	for _, capability := range capabilities {
		if capability == access.CapabilityProjectAdmin {
			return
		}
	}
	t.Fatalf("qualification reviewer capabilities = %v, want PROJECT_ADMIN", capabilities)
}

func qualificationEvaluationProjectPath(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(directory, "evaluation", "project", "leapview.yaml")
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("qualification project not found from %s", directory)
		}
		directory = parent
	}
}
