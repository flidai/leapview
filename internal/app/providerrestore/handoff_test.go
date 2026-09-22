package providerrestore

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/recoveryset"
)

func TestReplacementHandoffRejectsPlaceholderAndCredentialBearingIdentity(t *testing.T) {
	set := providerRestoreSet(t)
	handoff := validReplacementHandoff(set)
	if err := handoff.Validate(set, handoff.Artifact.Image); err != nil {
		t.Fatal(err)
	}

	placeholder := handoff
	placeholder.Artifact.Image = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	if err := placeholder.Validate(set, placeholder.Artifact.Image); err == nil {
		t.Fatal("placeholder OCI identity was accepted")
	}

	credentialBearing := handoff
	credentialBearing.Providers = append([]ProviderEndpoint(nil), handoff.Providers...)
	credentialBearing.Providers[0].Endpoint = "postgres://runtime:secret@db.example.test:5432"
	if err := credentialBearing.Validate(set, credentialBearing.Artifact.Image); err == nil {
		t.Fatal("credential-bearing provider endpoint was accepted")
	}

	credentialQuery := handoff
	credentialQuery.Providers = append([]ProviderEndpoint(nil), handoff.Providers...)
	credentialQuery.Providers[0].Endpoint = "postgres://db.example.test:5432?password=secret"
	if err := credentialQuery.Validate(set, credentialQuery.Artifact.Image); err == nil {
		t.Fatal("credential-bearing provider endpoint query was accepted")
	}
}

func TestSecretBundleIsDigestBoundAndSeparateFromEvidence(t *testing.T) {
	privateRoot := t.TempDir()
	evidenceRoot := t.TempDir()
	store := FileSecretBundleStore{Root: privateRoot}
	bundle := CredentialBundle{SchemaVersion: 1, ControlURL: "postgres://runtime:control-secret@127.0.0.1:5432/control?sslmode=disable", DuckLakeURL: "postgres://runtime:duck-secret@127.0.0.1:5432/ducklake?sslmode=disable", ObjectEndpoint: "https://objects.example.test", ObjectRegion: "us-east-1", ObjectAccessKey: "access-secret", ObjectSecretKey: "object-secret"}
	reference, err := store.Save(t.Context(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := (FileSecretBundleStore{Root: privateRoot}).Load(t.Context(), reference)
	if err != nil || loaded != bundle {
		t.Fatalf("independent secret load = %#v, error=%v", loaded, err)
	}
	encoded, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"control-secret", "duck-secret", "access-secret", "object-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("secret %q leaked into the reference", secret)
		}
	}
	parsed, err := url.Parse(reference.URI)
	if err != nil {
		t.Fatal(err)
	}
	if relative, err := filepath.Rel(evidenceRoot, parsed.Path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatal("private bundle was written under evidence root")
	}
	raw, err := os.ReadFile(parsed.Path)
	if err != nil || !strings.Contains(string(raw), "object-secret") {
		t.Fatalf("private bundle did not contain expected credential: %v", err)
	}
	info, err := os.Stat(parsed.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private bundle mode = %v", info.Mode().Perm())
	}
}

func validReplacementHandoff(set recoveryset.RecoverySet) ReplacementHandoff {
	return ReplacementHandoff{
		SchemaVersion: HandoffSchemaVersion, Kind: HandoffKind, Status: HandoffAvailable,
		RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, TargetID: set.Delivery.TargetID,
		Artifact: compatibility.ReleaseIdentity{Version: "0.2.0-rc.2", SourceRevision: "69652fb20101de80497cbf47ecfb402f700d6552", Image: testRunnableArtifact, Distribution: "oci", Platform: "linux/amd64"},
		Providers: []ProviderEndpoint{
			{Role: "control", Provider: "postgresql", ResourceID: "postgres-a", Endpoint: "postgres://db.example.test:5432", Database: "control", CredentialSecretKey: "postgres.control.url"},
			{Role: "ducklake", Provider: "postgresql", ResourceID: "postgres-a", Endpoint: "postgres://db.example.test:5432", Database: "ducklake", CredentialSecretKey: "postgres.ducklake.url"},
			{Role: "objects", Provider: "s3", ResourceID: "objects-a", Endpoint: "https://objects.example.test", Region: "us-east-1", Bucket: "restored", CredentialSecretKey: "object.credentials"},
		},
		Secrets:     SecretBundleReference{Provider: "root-readable-file", URI: "file:///run/leapview/recovery/private.json", SHA256: strings64("c"), Version: "1", Keys: []string{"postgres.control.url", "postgres.ducklake.url", "object.credentials"}},
		AvailableAt: time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC),
	}
}
