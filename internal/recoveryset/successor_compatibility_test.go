package recoveryset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/recoveryset/observation"
)

const successorLegacyFixtureDir = "testdata/successor-legacy"

func TestSuccessorCompatibilityPreservesLegacyCanonicalRecoverySetBytes(t *testing.T) {
	cases := []struct {
		name       string
		fixture    string
		wantSum    string
		wantDigest string
		build      func(*testing.T) RecoverySet
	}{
		{
			name:       "v1",
			fixture:    "recoveryset-v1.json",
			wantSum:    "3d4cebac6e1eebf3ec8c153857f84087fb8aaa8e947cc4fda1538420910afa2e",
			wantDigest: "sha256:a1f8f8b06d9ed369f9a4b720489465a73b7c386687408f05fea0d2153d0aa7eb",
			build:      testSet,
		},
		{
			name:       "v2",
			fixture:    "recoveryset-v2.json",
			wantSum:    "7b767d337fc89878964c7598595f60020b8a7d1f6d2dd253fdeb7b4ec9a64339",
			wantDigest: "sha256:2182572301a3097fd7052f07ebc02ce30d721a66ee9badba30f50c878c170920",
			build:      v2Set,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := successorLegacyFixture(t, tc.fixture)
			got, err := tc.build(t).CanonicalJSON()
			if err != nil {
				t.Fatalf("CanonicalJSON() error: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("canonical bytes changed:\n got: %s\nwant: %s", got, want)
			}
			if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != tc.wantSum {
				t.Fatalf("canonical bytes hash changed: %x", sum)
			}
			if digest, err := tc.build(t).Digest(); err != nil || digest != tc.wantDigest {
				t.Fatalf("frontier digest = %q, error = %v; want %q", digest, err, tc.wantDigest)
			}
			parsed, err := ParseRecoverySet(want)
			if err != nil {
				t.Fatalf("ParseRecoverySet(legacy %s) error: %v", tc.name, err)
			}
			parsedBytes, err := parsed.CanonicalJSON()
			if err != nil {
				t.Fatalf("parsed CanonicalJSON() error: %v", err)
			}
			if !bytes.Equal(parsedBytes, want) {
				t.Fatalf("legacy parse/re-encode changed canonical bytes:\n got: %s\nwant: %s", parsedBytes, want)
			}
		})
	}
}

func TestSuccessorCompatibilityPreservesLegacyObservationCanonicalBytes(t *testing.T) {
	want := successorLegacyFixture(t, "observation-manifest-v1.json")
	successorLegacyHash(t, "observation-manifest-v1.sha256", want)
	parsed, err := observation.Parse(want)
	if err != nil {
		t.Fatalf("observation.Parse(legacy manifest) error: %v", err)
	}
	got, err := parsed.CanonicalJSON()
	if err != nil {
		t.Fatalf("legacy observation CanonicalJSON() error: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("legacy observation canonical bytes changed:\n got: %s\nwant: %s", got, want)
	}
}

func TestSuccessorCompatibilityLegacyRecoverySetReaderRejectsSchema3(t *testing.T) {
	_, err := ParseRecoverySet(successorLegacyFixture(t, "recoveryset-schema3.json"))
	if err == nil {
		t.Fatal("ParseRecoverySet accepted successor schema version 3")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseRecoverySet schema 3 error = %v, want ErrInvalid", err)
	}
}

func TestSuccessorCompatibilityLegacyObservationReaderRejectsManifest2(t *testing.T) {
	_, err := observation.Parse(successorLegacyFixture(t, "observation-manifest-schema2.json"))
	if err == nil {
		t.Fatal("observation.Parse accepted successor manifest schema version 2")
	}
	if !errors.Is(err, observation.ErrInvalid) {
		t.Fatalf("observation.Parse manifest schema 2 error = %v, want observation.ErrInvalid", err)
	}
}

func successorLegacyFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(successorLegacyFixtureDir, name))
	if err != nil {
		t.Fatalf("read successor legacy fixture %q: %v", name, err)
	}
	// Text fixtures carry the conventional final newline; the canonical JSON
	// bytes themselves do not. Keep every other byte significant so accidental
	// rewrites or formatting changes fail the golden comparison.
	if bytes.HasSuffix(raw, []byte("\r\n")) {
		raw = raw[:len(raw)-2]
	} else if bytes.HasSuffix(raw, []byte("\n")) {
		raw = raw[:len(raw)-1]
	}
	if len(raw) < 2 || strings.TrimSpace(string(raw)) != string(raw) {
		t.Fatalf("fixture %q is not a compact canonical JSON document", name)
	}
	return raw
}

func successorLegacyHash(t *testing.T, name string, content []byte) {
	t.Helper()
	got := sha256.Sum256(content)
	wantRaw, err := os.ReadFile(filepath.Join(successorLegacyFixtureDir, name))
	if err != nil {
		t.Fatalf("read successor legacy hash %q: %v", name, err)
	}
	want := strings.TrimSpace(string(wantRaw))
	if want != hex.EncodeToString(got[:]) {
		t.Fatalf("fixture %q hash = %s, want %s", name, hex.EncodeToString(got[:]), want)
	}
}

func TestSuccessorCompatibilityLegacyGoldenHashFiles(t *testing.T) {
	for _, name := range []string{"recoveryset-v1.json", "recoveryset-v2.json"} {
		content := successorLegacyFixture(t, name)
		successorLegacyHash(t, strings.TrimSuffix(name, ".json")+".sha256", content)
	}
}
