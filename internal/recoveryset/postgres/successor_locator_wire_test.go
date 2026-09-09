package postgres_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	recoverypg "github.com/flidai/leapview/internal/recoveryset/postgres"
)

func TestSuccessorLocatorFrozenWire(t *testing.T) {
	input, _, _ := successorInputs(t, "minimal")
	l := input.Payloads.Manifest.Locator
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit field order and envelope; values derive from the signed golden
	// manifest, not from the locator serializer under test.
	want := fmt.Sprintf(`{"kind":"leapview.recovery-evidence-locator","version":1,"target":{"backend":"s3","storage_profile_id":"22222222-2222-4222-8222-222222222222","storage_profile_revision":1,"account_identity":"qualification","endpoint":"https://evidence.example.test","region":"test-region-1","bucket":"qualification","namespace":"evidence","key":"evidence/managed-observation-manifest/v2/sha256/%s"},"version_id":"immutable-manifest","payload_family":"leapview.managed-observation-manifest","payload_version":2,"payload_digest":"%s","payload_sha256":"%s","payload_size":%d}`, l.PayloadSHA256, l.PayloadDigest, l.PayloadSHA256, l.PayloadSize)
	if string(raw) != want {
		t.Fatalf("frozen locator wire changed\ngot %s\nwant %s", raw, want)
	}
	var loaded recoverypg.ValidatedLocator
	if err := json.Unmarshal(raw, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded != l {
		t.Fatal("locator roundtrip changed identity")
	}
	oversized := l
	oversized.AccountIdentity = strings.Repeat(`"`, 4096)
	oversized.Region = strings.Repeat(`"`, 4096)
	if err := oversized.Validate(); err == nil {
		t.Fatal("oversized escaped wire document accepted as SQL locator")
	}
	for _, mutation := range []struct{ name, raw string }{
		{"unknown field", strings.Replace(want, `"version":1`, `"extra":0,"version":1`, 1)},
		{"duplicate field", strings.Replace(want, `"version":1`, `"version":1,"version":1`, 1)},
		{"reordered fields", strings.Replace(want, `"kind":"leapview.recovery-evidence-locator","version":1`, `"version":1,"kind":"leapview.recovery-evidence-locator"`, 1)},
		{"unsupported version", strings.Replace(want, `"version":1`, `"version":2`, 1)},
		{"noncanonical integer", strings.Replace(want, `"version":1`, `"version":1.0`, 1)},
		{"noncanonical escaping", strings.Replace(want, `"backend":"s3"`, `"backend":"\u00733"`, 1)},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			var value recoverypg.ValidatedLocator
			if err := json.Unmarshal([]byte(mutation.raw), &value); err == nil {
				t.Fatal("noncanonical transport wire accepted")
			}
		})
	}
}
