package successor

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rebuildFixture is a test capture constructor. Runtime consumers must never
// repair submitted evidence this way: trusted inputs come from domain owners.
func rebuildFixture(t *testing.T, set RecoverySet3, e Evidence) (RecoverySet3, Evidence) {
	t.Helper()
	var err error
	e.Manifest.ManagedClosureDigest, err = ClosureDigest(e.Manifest.Revisions)
	if err != nil {
		t.Fatal(err)
	}
	e.Anchor.ManagedClosureDigest = e.Manifest.ManagedClosureDigest
	e.Anchor.ProviderProfileDigest, err = e.Profiles.Digest()
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Anchor.Digest()
	if err != nil {
		t.Fatal(err)
	}
	e.Manifest.SourceFrontierAnchorDigest = a
	e.ExpectedScope.SourceFrontierAnchorDigest = a
	e.ExpectedScope.ManagedClosureDigest = e.Manifest.ManagedClosureDigest
	e.Receipt.Core.SourceFrontierAnchorDigest = a
	e.Receipt.Core.ManagedClosureDigest = e.Manifest.ManagedClosureDigest
	e.Receipt.Core.ProviderProfileDigest = e.Anchor.ProviderProfileDigest
	e.Receipt.Core.ObservationProjectionDigest, err = e.Manifest.ObservationProjectionDigest()
	if err != nil {
		t.Fatal(err)
	}
	e.Receipt.Core.MembershipCount, e.Receipt.Core.ObjectCount, err = e.Manifest.ObservationCounts()
	if err != nil {
		t.Fatal(err)
	}
	e.ExpectedScope.EmptyScopeVerified = e.Receipt.Core.MembershipCount == 0
	e.Manifest.Capture.ReceiptDigest, err = e.Receipt.Core.Digest()
	if err != nil {
		t.Fatal(err)
	}
	e.Receipt, err = SignReceipt(e.Manifest, e.Receipt.Core, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	set.SourceFrontierAnchorDigest = a
	set.ManagedObservationManifestDigest, err = e.Manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set.FrontierDigest = ""
	set.FrontierDigest, err = set.Commitment()
	if err != nil {
		t.Fatal(err)
	}
	return set, e
}

func goldenFixture(t *testing.T, name string) (RecoverySet3, Evidence) {
	t.Helper()
	set, e := independentFixture(t)
	switch name {
	case "minimal":
	case "closure":
		second := e.Manifest.Revisions[0]
		second.RevisionID = "revision-b"
		second.Files = append([]File(nil), second.Files...)
		e.Manifest.Revisions = append(e.Manifest.Revisions, second)
	case "empty":
		e.Manifest.Revisions = []Revision{}
	default:
		t.Fatalf("unknown fixture %s", name)
	}
	return rebuildFixture(t, set, e)
}

func goldenDocuments(t *testing.T, set RecoverySet3, e Evidence) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for name, encode := range map[string]func() ([]byte, error){"set": set.CanonicalJSON, "manifest": e.Manifest.CanonicalJSON, "anchor": e.Anchor.CanonicalJSON, "profiles": e.Profiles.CanonicalJSON, "receipt-core": e.Receipt.Core.CanonicalJSON, "receipt": e.Receipt.CanonicalJSON, "authority": e.Authorities.CanonicalJSON, "signed-payload": e.Receipt.SignedBytes} {
		raw, err := encode()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = raw
	}
	return out
}

func TestSuccessorFrozenGoldens(t *testing.T) {
	for _, name := range []string{"minimal", "closure", "empty"} {
		t.Run(name, func(t *testing.T) {
			set, e := goldenFixture(t, name)
			if err := set.ValidateEvidence(e); err != nil {
				t.Fatal(err)
			}
			for document, raw := range goldenDocuments(t, set, e) {
				ext := ".json"
				if document == "signed-payload" {
					ext = ".txt"
				}
				path := filepath.Join("testdata", "frozen", name+"-"+document+ext)
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, bytes.TrimSuffix(want, []byte("\n"))) {
					t.Fatalf("%s golden bytes changed", document)
				}
			}
			digests := map[string]string{}
			for label, get := range map[string]func() (string, error){"frontier": set.Digest, "manifest": e.Manifest.Digest, "anchor": e.Anchor.Digest, "profiles": e.Profiles.Digest, "core": e.Receipt.Core.Digest, "receipt": e.Receipt.Digest, "authority": e.Authorities.Digest} {
				d, err := get()
				if err != nil {
					t.Fatal(err)
				}
				digests[label] = d
			}
			raw, err := json.Marshal(digests)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", "frozen", name+"-digests.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, bytes.TrimSuffix(want, []byte("\n"))) {
				t.Fatal("golden digests changed")
			}
			// Independent standard-library recomputation, not a call to the digest helper.
			manifest, _ := e.Manifest.CanonicalJSON()
			sum := sha256.Sum256(append([]byte("leapview/managed-observations/v2\n"), manifest...))
			if digests["manifest"] != "sha256:"+hex.EncodeToString(sum[:]) {
				t.Fatal("manifest domain boundary changed")
			}
			parsed, err := ParseManagedManifest2(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parsed.Digest(); err != nil {
				t.Fatal(err)
			}
			if name == "empty" {
				e.ExpectedScope.EmptyScopeVerified = false
				if err := set.ValidateEvidence(e); err == nil {
					t.Fatal("self-asserted empty scope accepted")
				}
			}
		})
	}
}

func TestSuccessorSharedObjectAndMembershipRejection(t *testing.T) {
	for _, name := range []string{"version", "digest", "size", "membership"} {
		t.Run(name, func(t *testing.T) {
			_, e := goldenFixture(t, "closure")
			switch name {
			case "version":
				e.Manifest.Revisions[1].Files[0].Provider.VersionID = "different"
			case "digest":
				e.Manifest.Revisions[1].Files[0].SHA256 = strings.Repeat("a", 64)
			case "size":
				e.Manifest.Revisions[1].Files[0].Size++
			case "membership":
				e.Manifest.Revisions[1].RevisionID = e.Manifest.Revisions[0].RevisionID
			}
			for i := range e.Manifest.Revisions {
				e.Manifest.Revisions[i].RevisionManifestDigest = RevisionFileDigest(e.Manifest.Revisions[i].Files)
			}
			if _, err := e.Manifest.CanonicalJSON(); err == nil {
				t.Fatal("conflicting shared object or duplicate membership accepted")
			}
		})
	}
}

func TestSuccessorManifestReorderingPreservesBytesAndOwnership(t *testing.T) {
	set, e := goldenFixture(t, "closure")
	second := e.Manifest.Revisions[0].Files[0]
	second.Path = "another.csv"
	second.Provider.Key = "managed/another.csv"
	e.Manifest.Revisions[0].Files = append(e.Manifest.Revisions[0].Files, second)
	e.Manifest.Revisions[0].RevisionManifestDigest = RevisionFileDigest(e.Manifest.Revisions[0].Files)
	_, e = rebuildFixture(t, set, e)
	want, err := e.Manifest.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := e.Manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	m := e.Manifest
	m.Revisions[0], m.Revisions[1] = m.Revisions[1], m.Revisions[0]
	m.Revisions[1].Files[0], m.Revisions[1].Files[1] = m.Revisions[1].Files[1], m.Revisions[1].Files[0]
	before, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("manifest canonicalization mutated caller slices")
	}
	if !bytes.Equal(got, want) {
		t.Fatal("membership ordering changed canonical bytes")
	}
	gotDigest, err := m.Digest()
	if err != nil || gotDigest != wantDigest {
		t.Fatalf("ordering changed digest: %v", err)
	}
}

func TestSuccessorBoundedCanonicalInputs(t *testing.T) {
	if _, err := ParseManagedManifest2(bytes.Repeat([]byte(" "), MaxDocumentBytes+1)); err == nil {
		t.Fatal("oversize manifest accepted")
	}
	if _, err := ParseRecoverySet3(bytes.Repeat([]byte(" "), MaxSetBytes+1)); err == nil {
		t.Fatal("oversize set accepted")
	}
	_, e := goldenFixture(t, "minimal")
	e.Manifest.Revisions[0].Files[0].Provider.VersionID = strings.Repeat("v", MaxTextBytes+1)
	if _, err := e.Manifest.CanonicalJSON(); err == nil {
		t.Fatal("oversize provider version accepted")
	}
	_, e = goldenFixture(t, "minimal")
	e.Manifest.Revisions = make([]Revision, MaxMembers+1)
	if _, err := e.Manifest.CanonicalJSON(); err == nil {
		t.Fatal("oversize membership accepted")
	}
}
