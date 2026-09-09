package successor

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

const testSetID = "11111111-1111-4111-8111-111111111111"
const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestEmptyManifestAndDetachedReceiptRoundTrip(t *testing.T) {
	profiles := ProviderProfileSet{ProfileVersion: ProviderProfileVersion, Profiles: []ProviderProfile{}}
	profileDigest, err := profiles.Digest()
	if err != nil {
		t.Fatal(err)
	}
	closureDigest, err := ClosureDigest([]Revision{})
	if err != nil {
		t.Fatal(err)
	}
	manifest := ManagedManifest2{ManifestVersion: ManagedManifestVersion, SetID: testSetID, SourceFrontierAnchorDigest: testDigest, ManagedClosureDigest: closureDigest, Capture: Capture{AuthorityID: "capture-authority", CaptureID: "capture-1", StartedAt: "2026-09-08T00:00:00.000000Z", CompletedAt: "2026-09-08T00:00:01.000000Z", ReceiptDigest: testDigest}, Revisions: []Revision{}}
	core := ReceiptCore{CoreVersion: ReceiptCoreVersion, AuthorityID: manifest.Capture.AuthorityID, KeyID: "key-1", CaptureID: manifest.Capture.CaptureID, SetID: manifest.SetID, StartedAt: manifest.Capture.StartedAt, CompletedAt: manifest.Capture.CompletedAt, SourceFrontierAnchorDigest: manifest.SourceFrontierAnchorDigest, ManagedClosureDigest: manifest.ManagedClosureDigest, ProviderProfileDigest: profileDigest, ObservationProjectionDigest: mustProjection(t, manifest), MembershipCount: 0, ObjectCount: 0, Result: "verified"}
	coreDigest, err := core.Digest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Capture.ReceiptDigest = coreDigest
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	registry := AuthorityRegistry{RegistryVersion: AuthorityRegistryVersion, Keys: []AuthorityKey{{AuthorityID: core.AuthorityID, KeyID: core.KeyID, Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(publicKey), NotBefore: "2026-09-07T00:00:00.000000Z", NotAfter: "2026-09-10T00:00:00.000000Z", ProviderProfileDigests: []string{profileDigest}}}}
	receipt, err := SignReceipt(manifest, core, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, mustCanonicalReceipt(t, parsed)) {
		t.Fatal("receipt canonical bytes changed on parse")
	}
	scope := ExpectedScope{SetID: manifest.SetID, SourceFrontierAnchorDigest: manifest.SourceFrontierAnchorDigest, ManagedClosureDigest: manifest.ManagedClosureDigest, Revisions: []Revision{}, EmptyScopeVerified: true}
	if err := parsed.Verify(manifest, scope, registry, profileDigest); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "\n") {
		t.Fatal("receipt has a noncanonical newline")
	}
}

func TestSharedObjectVersionConflictRejected(t *testing.T) {
	file := File{Path: "a.csv", SHA256: strings.Repeat("a", 64), Size: 1, Provider: ProviderIdentity{Implementation: "s3", AccountIdentity: "account", Endpoint: "https://objects.example", Region: "us-east-1", Bucket: "bucket", Key: "managed/a.csv", Prefix: "managed", VersionID: "version-1"}}
	files := []File{file}
	owner := RevisionFileDigest(files)
	left := Revision{ProjectID: "project-a", CollectionID: "collection", RevisionID: "revision-a", RevisionManifestDigest: owner, Files: files}
	rightFile := file
	rightFile.Provider.VersionID = "version-2"
	right := Revision{ProjectID: "project-b", CollectionID: "collection", RevisionID: "revision-b", RevisionManifestDigest: RevisionFileDigest([]File{rightFile}), Files: []File{rightFile}}
	if _, err := closureDigest([]Revision{left, right}); err != nil {
		t.Fatal(err)
	}
	manifest := ManagedManifest2{ManifestVersion: ManagedManifestVersion, SetID: testSetID, SourceFrontierAnchorDigest: testDigest, ManagedClosureDigest: testDigest, Capture: Capture{AuthorityID: "authority", CaptureID: "capture", StartedAt: "2026-09-08T00:00:00.000000Z", CompletedAt: "2026-09-08T00:00:01.000000Z", ReceiptDigest: testDigest}, Revisions: []Revision{left, right}}
	if err := manifest.Validate(); err == nil {
		t.Fatal("conflicting shared object version accepted")
	}
}

func TestParsersRejectNoncanonicalAndUnsupportedDocuments(t *testing.T) {
	profiles := ProviderProfileSet{ProfileVersion: ProviderProfileVersion, Profiles: []ProviderProfile{}}
	raw, err := profiles.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProviderProfileSet(append([]byte(" "), raw...)); err == nil {
		t.Fatal("leading whitespace accepted")
	}
	unsupported := bytes.Replace(raw, []byte(`"profile_version":2`), []byte(`"profile_version":99`), 1)
	if _, err := ParseProviderProfileSet(unsupported); err == nil {
		t.Fatal("unsupported profile version accepted")
	}
}

func mustProjection(t *testing.T, manifest ManagedManifest2) string {
	t.Helper()
	digest, err := manifest.ObservationProjectionDigest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
func mustCanonicalReceipt(t *testing.T, receipt SignedReceipt) []byte {
	t.Helper()
	raw, err := receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
