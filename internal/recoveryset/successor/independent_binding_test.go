package successor

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/recoveryset"
)

// The selected scope and authority are independent test inputs, not assertions
// copied back from a mutated submission. This is not a physical recovery drill.
func independentFixture(t *testing.T) (RecoverySet3, Evidence) {
	t.Helper()
	raw, err := os.ReadFile("../testdata/successor-legacy/recoveryset-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := recoveryset.ParseRecoverySet(bytes.TrimSpace(raw))
	if err != nil {
		t.Fatal(err)
	}
	profiles := ProviderProfileSet{ProfileVersion: ProviderProfileVersion, Profiles: []ProviderProfile{{
		Implementation: "s3", AccountIdentity: "account-a", Endpoint: "https://objects.example.com", Region: "us-east-1", Bucket: "bucket-a", VersionSemantics: "opaque-exact-version",
		Namespaces: []Namespace{{ProjectID: "project-a", CollectionID: "collection-a", Prefix: "managed"}},
	}}}
	profileDigest, err := profiles.Digest()
	if err != nil {
		t.Fatal(err)
	}
	content := sha256.Sum256([]byte("historical bytes"))
	files := []File{{Path: "data.csv", SHA256: hex.EncodeToString(content[:]), Size: 16, Provider: ProviderIdentity{
		Implementation: "s3", AccountIdentity: "account-a", Endpoint: "https://objects.example.com", Region: "us-east-1", Bucket: "bucket-a", Prefix: "managed", Key: "managed/data.csv", VersionID: "version-a",
	}}}
	revisions := []Revision{{ProjectID: "project-a", CollectionID: "collection-a", RevisionID: "revision-a", RevisionManifestDigest: RevisionFileDigest(files), Files: files}}
	closure, err := ClosureDigest(revisions)
	if err != nil {
		t.Fatal(err)
	}
	roots := make([]ObjectRoot, len(root.ObjectRoots))
	for i, r := range root.ObjectRoots {
		roots[i] = ObjectRoot{r.Kind, r.URI, r.VersionID, r.Digest, r.ProviderRecoveryFrontier}
	}
	anchor := SourceAnchor{AnchorVersion: SourceAnchorVersion, SetID: root.ID, ClusterPoints: root.ClusterPoints, Delivery: root.Delivery, Serving: root.Serving, Catalog: root.Catalog, ObjectRoots: roots, Compatibility: root.Compatibility, ManagedClosureDigest: closure, ProviderProfileDigest: profileDigest}
	a, err := anchor.Digest()
	if err != nil {
		t.Fatal(err)
	}
	manifest := ManagedManifest2{ManifestVersion: ManagedManifestVersion, SetID: root.ID, SourceFrontierAnchorDigest: a, ManagedClosureDigest: closure, Revisions: revisions,
		Capture: Capture{AuthorityID: "authority-a", CaptureID: "capture-a", StartedAt: "2026-09-08T00:00:00.000000Z", CompletedAt: "2026-09-08T00:01:00.000000Z", ReceiptDigest: "sha256:" + strings.Repeat("0", 64)}}
	projection, err := manifest.ObservationProjectionDigest()
	if err != nil {
		t.Fatal(err)
	}
	core := ReceiptCore{CoreVersion: ReceiptCoreVersion, AuthorityID: "authority-a", KeyID: "key-a", CaptureID: "capture-a", SetID: root.ID, StartedAt: manifest.Capture.StartedAt, CompletedAt: manifest.Capture.CompletedAt,
		SourceFrontierAnchorDigest: a, ManagedClosureDigest: closure, ProviderProfileDigest: profileDigest, ObservationProjectionDigest: projection, MembershipCount: 1, ObjectCount: 1, Result: "verified"}
	manifest.Capture.ReceiptDigest, err = core.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Fixed seed is a public TEST VECTOR, never a deployment signing key.
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	receipt, err := SignReceipt(manifest, core, key)
	if err != nil {
		t.Fatal(err)
	}
	registry := AuthorityRegistry{RegistryVersion: AuthorityRegistryVersion, Keys: []AuthorityKey{{AuthorityID: "authority-a", KeyID: "key-a", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)), NotBefore: "2026-01-01T00:00:00.000000Z", NotAfter: "2027-01-01T00:00:00.000000Z", ProviderProfileDigests: []string{profileDigest}}}}
	m, err := manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set := RecoverySet3{ID: root.ID, SchemaVersion: RecoverySetVersion, ClusterPoints: root.ClusterPoints, Delivery: root.Delivery, Serving: root.Serving, Catalog: root.Catalog, ObjectRoots: roots, Compatibility: root.Compatibility,
		SourceFrontierAnchorDigest: a, ManagedObservationManifestVersion: ManagedManifestVersion, ManagedObservationManifestDigest: m, FenceEpoch: root.FenceEpoch, AuditIdentity: root.AuditIdentity, Status: root.Status, CreatedBy: root.CreatedBy, CreatedAt: "2026-09-08T00:02:00.000000Z"}
	set.FrontierDigest, err = set.Commitment()
	if err != nil {
		t.Fatal(err)
	}
	return set, Evidence{Anchor: anchor, Manifest: manifest, Receipt: receipt, Profiles: profiles, Authorities: registry, VerificationTime: time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC),
		ExpectedScope: ExpectedScope{SetID: root.ID, SourceFrontierAnchorDigest: a, ManagedClosureDigest: closure}}
}

func TestIndependentSuccessorBindingAndCanonicalRoundtrip(t *testing.T) {
	set, evidence := independentFixture(t)
	if err := set.ValidateEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	raw, err := set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRecoverySet3(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := parsed.ValidateEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	manifest, err := evidence.Manifest.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManagedManifest2(manifest); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append([]byte(" "), manifest...), append(append([]byte(nil), manifest...), '\n'), bytes.Replace(manifest, []byte(`"manifest_version":2`), []byte(`"MANIFEST_VERSION":2`), 1)} {
		if _, err := ParseManagedManifest2(bad); err == nil {
			t.Fatal("noncanonical manifest accepted")
		}
	}
}

func TestIndependentSuccessorSubstitutionFailsClosed(t *testing.T) {
	cases := map[string]func(*RecoverySet3, *Evidence){
		"root substitution": func(s *RecoverySet3, e *Evidence) { s.Delivery.TargetRevision++ },
		"anchor closure": func(s *RecoverySet3, e *Evidence) {
			e.Anchor.ManagedClosureDigest = "sha256:" + strings.Repeat("a", 64)
		},
		"profile account": func(s *RecoverySet3, e *Evidence) { e.Profiles.Profiles[0].AccountIdentity = "different-account" },
		"manifest provider": func(s *RecoverySet3, e *Evidence) {
			e.Manifest.Revisions[0].Files[0].Provider.VersionID = "other-version"
		},
		"selected closure": func(s *RecoverySet3, e *Evidence) {
			e.ExpectedScope.ManagedClosureDigest = "sha256:" + strings.Repeat("b", 64)
		},
		"signature": func(s *RecoverySet3, e *Evidence) {
			e.Receipt.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		},
		"revoked":                    func(s *RecoverySet3, e *Evidence) { e.Authorities.Keys[0].Revoked = true },
		"expired capture key":        func(s *RecoverySet3, e *Evidence) { e.Authorities.Keys[0].NotAfter = "2026-09-08T00:00:30.000000Z" },
		"unknown authority":          func(s *RecoverySet3, e *Evidence) { e.Authorities.Keys[0].AuthorityID = "other-authority" },
		"missing verification clock": func(s *RecoverySet3, e *Evidence) { e.VerificationTime = time.Time{} },
		"future capture":             func(s *RecoverySet3, e *Evidence) { e.VerificationTime = time.Date(2026, 9, 8, 0, 0, 30, 0, time.UTC) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			set, evidence := independentFixture(t)
			mutate(&set, &evidence)
			first := set.ValidateEvidence(evidence)
			if first == nil {
				t.Fatal("substitution accepted")
			}
			for i := 0; i < 10; i++ {
				next := set.ValidateEvidence(evidence)
				if next == nil || next.Error() != first.Error() {
					t.Fatalf("unstable rejection: %v versus %v", first, next)
				}
			}
		})
	}
}

func TestIndependentCanonicalizationDoesNotMutateInputs(t *testing.T) {
	set, evidence := independentFixture(t)
	set.ClusterPoints[0], set.ClusterPoints[1] = set.ClusterPoints[1], set.ClusterPoints[0]
	before, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.CanonicalJSON(); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("canonicalizer mutated caller's root slices")
	}
	if err := set.ValidateEvidence(evidence); err != nil {
		t.Fatalf("equivalent reordered roots rejected: %v", err)
	}
}

func TestIndependentSuccessorVersionMatrixAndMalformedEncoding(t *testing.T) {
	set, e := independentFixture(t)
	for name, tc := range map[string]struct {
		encode  func() ([]byte, error)
		parse   func([]byte) error
		version string
	}{
		"set":       {set.CanonicalJSON, func(b []byte) error { _, err := ParseRecoverySet3(b); return err }, `"schema_version":3`},
		"manifest":  {e.Manifest.CanonicalJSON, func(b []byte) error { _, err := ParseManagedManifest2(b); return err }, `"manifest_version":2`},
		"anchor":    {e.Anchor.CanonicalJSON, func(b []byte) error { _, err := ParseSourceAnchor(b); return err }, `"anchor_version":2`},
		"profiles":  {e.Profiles.CanonicalJSON, func(b []byte) error { _, err := ParseProviderProfileSet(b); return err }, `"profile_version":2`},
		"receipt":   {e.Receipt.CanonicalJSON, func(b []byte) error { _, err := ParseReceipt(b); return err }, `"receipt_version":2`},
		"authority": {e.Authorities.CanonicalJSON, func(b []byte) error { _, err := ParseAuthorityRegistry(b); return err }, `"registry_version":2`},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := tc.encode()
			if err != nil {
				t.Fatal(err)
			}
			if err = tc.parse(raw); err != nil {
				t.Fatalf("valid parse: %v", err)
			}
			versionKey := tc.version[:strings.LastIndex(tc.version, ":")+1]
			unknown := bytes.Replace(raw, []byte(tc.version), []byte(versionKey+"99"), 1)
			if err := tc.parse(unknown); !errors.Is(err, ErrUnsupportedVersion) {
				t.Fatalf("unsupported version category lost: %v", err)
			}
			for _, bad := range [][]byte{append([]byte(" "), raw...), append(append([]byte(nil), raw...), byte('\n')), []byte("{}"), bytes.Replace(raw, []byte(tc.version), []byte(tc.version+","+tc.version), 1)} {
				if err := tc.parse(bad); err == nil {
					t.Fatal("malformed/noncanonical encoding accepted")
				}
			}
		})
	}
}

func TestIndependentAuthorityCopiesAndMissingCommitment(t *testing.T) {
	set, e := independentFixture(t)
	key, err := e.Authorities.Lookup("authority-a", "key-a")
	if err != nil {
		t.Fatal(err)
	}
	key.ProviderProfileDigests[0] = "changed"
	copy, err := e.Authorities.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	copy.Keys[0].ProviderProfileDigests[0] = "changed"
	if err := set.ValidateEvidence(e); err != nil {
		t.Fatalf("derived authority value mutated original registry: %v", err)
	}
	set.FrontierDigest = ""
	if _, err := set.CanonicalJSON(); err == nil {
		t.Fatal("canonical wire accepted missing frontier commitment")
	}
}

func TestIndependentNestedOwnerTextCannotHashLossily(t *testing.T) {
	for _, bad := range []string{"bad\xffidentity", "decomposed-e\u0301", "line\nbreak"} {
		set, e := independentFixture(t)
		set.Serving.RuntimeVersion = bad
		if _, err := set.CanonicalJSON(); err == nil {
			t.Fatal("invalid nested owner text serialized")
		}
		e.Anchor.Serving.RuntimeVersion = bad
		if _, err := e.Anchor.Digest(); err == nil {
			t.Fatal("invalid nested owner text hashed")
		}
	}
}

func TestIndependentNestedVersionClassification(t *testing.T) {
	set, e := independentFixture(t)
	set.ManagedObservationManifestVersion = 99
	if err := set.Validate(); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("nested manifest version: %v", err)
	}
	e.Receipt.Core.CoreVersion = 99
	if err := e.Receipt.Validate(); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("nested core version: %v", err)
	}
}
