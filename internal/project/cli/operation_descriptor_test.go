package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestDeploymentOperationStoreRetainsCredentialFreeExactIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployment-operations.json")
	store := NewDeploymentOperationStore(path)
	descriptor, err := NewDeploymentOperation("release-42", "https://target.example/", "prod", "project-1", "production", filepath.Join(t.TempDir(), "dashboards"), "candidate-sync:deploy")
	if err != nil {
		t.Fatal(err)
	}
	descriptor.TargetID = "target-1"
	descriptor.SourceDigest = "sha256:" + strings.Repeat("a", 64)
	descriptor.SourceAttestationDigest = "sha256:" + strings.Repeat("b", 64)
	descriptor.ProvenanceDigest = "sha256:" + strings.Repeat("c", 64)
	descriptor.PlanID = "plan-1"
	if err := store.Create(descriptor); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Select("release-42", "https://target.example", "project-1", "production")
	if err != nil || !reflect.DeepEqual(loaded, descriptor) {
		t.Fatalf("loaded descriptor = %#v, err = %v", loaded, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(content)), "token") || strings.Contains(strings.ToLower(string(content)), "password") {
		t.Fatalf("operation descriptor contains credential material: %s", content)
	}
	if mode := func() os.FileMode { info, _ := os.Stat(path); return info.Mode().Perm() }(); mode != 0o600 {
		t.Fatalf("operation descriptor mode = %o, want 600", mode)
	}
}

func TestDeploymentOperationStoreRejectsAmbiguousOrMismatchedSelection(t *testing.T) {
	store := NewDeploymentOperationStore(filepath.Join(t.TempDir(), "deployment-operations.json"))
	for _, handle := range []string{"release-a", "release-b"} {
		descriptor, err := NewDeploymentOperation(handle, "https://target.example", "prod", "project-1", "production", "", "candidate-sync:deploy")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Create(descriptor); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.List("https://target.example", "project-1", "production")
	if err != nil || len(items) != 2 || items[0].Handle != "release-a" || items[1].Handle != "release-b" {
		t.Fatalf("List() = %#v, err = %v", items, err)
	}
	if _, err := store.Select("release-a", "https://other.example", "project-1", "production"); !errors.Is(err, ErrDeploymentOperationTargetMismatch) {
		t.Fatalf("target mismatch error = %v", err)
	}
	if _, err := store.Select("missing", "https://target.example", "project-1", "production"); !errors.Is(err, ErrDeploymentOperationNotFound) {
		t.Fatalf("unknown handle error = %v", err)
	}
}

func TestDeploymentOperationStoreConcurrentUpdatesMergeWithoutLostIdentity(t *testing.T) {
	store := NewDeploymentOperationStore(filepath.Join(t.TempDir(), "deployment-operations.json"))
	base, err := NewDeploymentOperation("release-concurrent", "https://target.example", "prod", "project-1", "production", "", "candidate-sync:deploy")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(base); err != nil {
		t.Fatal(err)
	}
	first, second := base, base
	first.PlanID, first.SourceDigest = "plan-1", "sha256:"+strings.Repeat("a", 64)
	second.BuildID, second.CandidateID = "build-1", "candidate-1"
	var group sync.WaitGroup
	group.Add(2)
	errors := make(chan error, 2)
	go func() { defer group.Done(); errors <- store.Save(first) }()
	go func() { defer group.Done(); errors <- store.Save(second) }()
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.Load(base.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PlanID != first.PlanID || loaded.BuildID != second.BuildID || loaded.CandidateID != second.CandidateID {
		t.Fatalf("concurrent update lost identity: %#v", loaded)
	}
}

func TestDeploymentOperationStoreExportImportVerifiesTargetAndRejectsUnknownFields(t *testing.T) {
	store := NewDeploymentOperationStore(filepath.Join(t.TempDir(), "source.json"))
	descriptor, err := NewDeploymentOperation("release-export", "https://target.example", "prod", "project-1", "production", "", "candidate-sync:deploy")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(descriptor); err != nil {
		t.Fatal(err)
	}
	var artifact bytes.Buffer
	if err := store.Export(descriptor.Handle, &artifact); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(artifact.String()), "secret") || strings.Contains(strings.ToLower(artifact.String()), "token") {
		t.Fatalf("export contains credential material: %s", artifact.String())
	}
	destination := NewDeploymentOperationStore(filepath.Join(t.TempDir(), "destination.json"))
	imported, err := destination.Import(bytes.NewReader(artifact.Bytes()), "https://target.example", "project-1", "production")
	if err != nil || imported.Handle != descriptor.Handle {
		t.Fatalf("imported descriptor = %#v, err = %v", imported, err)
	}
	var unknown map[string]any
	if err := json.Unmarshal(artifact.Bytes(), &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["token"] = "must-not-be-accepted"
	poisoned, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := destination.Import(bytes.NewReader(poisoned), "https://target.example", "project-1", "production"); err == nil {
		t.Fatal("Import accepted unknown credential-bearing field")
	}
}

func TestDeploymentOperationDescriptorRejectsCredentialBearingEvidence(t *testing.T) {
	for name, secret := range map[string]string{
		"userinfo URL":        "postgres://user:supersecret@db.example/prod",
		"structured token":    `{"token":"supersecret"}`,
		"compound access key": "access_key_id=AKIAIOSFODNN7EXAMPLE",
		"connection string":   "connection_string=Server=db;Pwd=supersecret",
	} {
		t.Run(name, func(t *testing.T) {
			descriptor, err := NewDeploymentOperation("release-secret", "https://target.example", "prod", "project-1", "production", "", "candidate-sync:deploy")
			if err != nil {
				t.Fatal(err)
			}
			descriptor.FailureDetail = secret
			if err := NewDeploymentOperationStore(filepath.Join(t.TempDir(), "operations.json")).Create(descriptor); err == nil {
				t.Fatalf("credential-bearing descriptor was accepted: %s", secret)
			}
		})
	}
}

func TestPortableDeploymentSourceRejectsCredentialMaterial(t *testing.T) {
	for name, content := range map[string]string{
		"inline token":      "title: Orders\ntoken: supersecret\n",
		"credential URL":    "description: postgres://user:supersecret@db.example/prod\n",
		"provider key":      "access_key_id=AKIAIOSFODNN7EXAMPLE\n",
		"connection string": "connection_string=Server=db;Pwd=supersecret\n",
	} {
		t.Run(name, func(t *testing.T) {
			hash := sha256.Sum256([]byte(content))
			artifact := DeploymentSourceArtifact{Path: "dashboards/orders.yaml", Digest: "sha256:" + hex.EncodeToString(hash[:]), SizeBytes: int64(len(content)), Content: []byte(content)}
			if err := validatePortableSourceArtifacts("project-1", "", "", []DeploymentSourceArtifact{artifact}); err == nil {
				t.Fatal("credential-bearing portable source was accepted")
			}
		})
	}
}

func TestDeploymentOperationImportRejectsOversizedDocumentBeforeDecode(t *testing.T) {
	const limit = int64(1024)
	oversized := io.MultiReader(
		strings.NewReader(`{"version":1,"operations":{},"padding":"`),
		io.LimitReader(zeroReader{}, limit),
		strings.NewReader(`"}`),
	)
	if _, err := decodeDeploymentOperationDocumentWithLimit(oversized, limit); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized import error = %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 'x'
	}
	return len(buffer), nil
}
