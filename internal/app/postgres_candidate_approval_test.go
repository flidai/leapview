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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/servingstate"
)

const (
	qualificationReviewerPrincipalID = "0198f2c0-7c7a-7f00-8a11-000000000777"
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

func TestCandidateApprovalPermissionsUsesPersistedTypedReviewerPolicy(t *testing.T) {
	sourceRoot := qualificationEvaluationSourceRoot(t)
	project, err := projectcompiler.Compile(sourceRoot)
	if err != nil {
		t.Fatalf("compile qualification source root: %v", err)
	}
	plan, err := projectcompiler.PlanSourceRoot(sourceRoot)
	if err != nil {
		t.Fatalf("plan qualification source root: %v", err)
	}
	var body bytes.Buffer
	manifest, artifactDigest, err := projectbundle.PackCompiledProject(project, plan, &body)
	if err != nil {
		t.Fatalf("pack qualification source bundle: %v", err)
	}
	_, compiled, err := projectbundle.ValidateArtifactBytes(body.Bytes())
	if err != nil {
		t.Fatalf("validate qualification artifact: %v", err)
	}
	manifestJSON, err := json.Marshal(manifest)
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
	reviewer, err := access.NewSubjectRef(access.SubjectKindPrincipal, qualificationReviewerPrincipalID)
	if err != nil {
		t.Fatal(err)
	}
	reviewerBinding, err := access.NewTypedRoleBinding("role-binding:qualification-reviewer", "qualification reviewer", reviewer, access.PermissionRoleReleaseApprover, projectgraph.ResourceID("project:leapview-evaluation"))
	if err != nil {
		t.Fatal(err)
	}
	accessPolicyJSON, err := json.Marshal(projectmanifest.AccessPolicy{RoleBindings: map[string]projectmanifest.RoleBinding{
		"qualification-reviewer": {
			ID: reviewerBinding.ID, Name: reviewerBinding.Name,
			PermissionProfile: reviewerBinding.PermissionProfile, PermissionRole: reviewerBinding.PermissionRole, Permissions: reviewerBinding.Permissions,
			Subject: projectmanifest.Subject{Kind: "principal", PrincipalID: qualificationReviewerPrincipalID},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	state := servingstate.State{
		ID:               generationID,
		ProjectID:        "project:leapview-evaluation",
		Environment:      "evaluation",
		Status:           servingstate.StatusValidated,
		Digest:           artifactDigest,
		ProjectDigest:    compiled.BundleDigest,
		AccessPolicyJSON: string(accessPolicyJSON),
	}
	subjects := func(context.Context, string) ([]access.SubjectRef, error) {
		reviewer, subjectErr := access.NewSubjectRef(access.SubjectKindPrincipal, qualificationReviewerPrincipalID)
		return []access.SubjectRef{reviewer}, subjectErr
	}

	projectID, environment, permissions, err := candidateApprovalPermissions(
		t.Context(), candidateApprovalStateReaderFake{state: state, artifact: artifact}, store, subjects,
		string(generationID), qualificationReviewerPrincipalID,
	)
	if err != nil {
		t.Fatalf("candidate approval permissions: %v", err)
	}
	if projectID != "project:leapview-evaluation" || environment != "evaluation" {
		t.Fatalf("candidate identity = (%q, %q)", projectID, environment)
	}
	approvePair, err := access.NewProjectPermissionPair(access.ActionDeliveryApprove, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	if !access.PermissionSetAllows(permissions, approvePair) {
		t.Fatalf("qualification reviewer permissions = %v, want exact delivery.approve project pair", permissions)
	}
	publishPair, err := access.NewProjectPermissionPair(access.ActionDeliveryPublish, projectgraph.ResourceID(projectID))
	if err != nil {
		t.Fatal(err)
	}
	if access.PermissionSetAllows(permissions, publishPair) {
		t.Fatalf("release approver unexpectedly has delivery.publish authority: %v", permissions)
	}
}

func qualificationEvaluationSourceRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(directory, "evaluation", "project")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("qualification project not found from %s", directory)
		}
		directory = parent
	}
}
