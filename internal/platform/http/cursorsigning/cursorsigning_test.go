package cursorsigning

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestKeyRingVerifiesExistingCursorsDuringRotation(t *testing.T) {
	v1 := bytes.Repeat([]byte{1}, 32)
	v2 := bytes.Repeat([]byte{2}, 32)
	if err := Configure("v1", map[string][]byte{"v1": v1}); err != nil {
		t.Fatal(err)
	}
	old := Sign("q1", []byte(`{"offset":1}`))
	if err := Configure("v2", map[string][]byte{"v1": v1, "v2": v2}); err != nil {
		t.Fatal(err)
	}
	if payload, err := Verify("q1", old); err != nil || string(payload) != `{"offset":1}` {
		t.Fatalf("verify old cursor payload=%s err=%v", payload, err)
	}
	next := Sign("q1", []byte(`{"offset":2}`))
	if !bytes.Contains([]byte(next), []byte("q1.v2.")) {
		t.Fatalf("new cursor does not use v2: %s", next)
	}
	if err := Configure("v2", map[string][]byte{"v2": v2}); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify("q1", old); err == nil {
		t.Fatal("retired key unexpectedly verified an old cursor")
	}
}

func revokeFixtureKey(t *testing.T, key []byte) {
	t.Helper()
	fingerprint := keyFingerprint(key)
	revokedKeyDigests[fingerprint] = true
	t.Cleanup(func() { delete(revokedKeyDigests, fingerprint) })
}

func TestConfigureRejectsExposedActiveKeyWithoutReplacingTrustedRing(t *testing.T) {
	trusted := bytes.Repeat([]byte{3}, 32)
	exposed := bytes.Repeat([]byte{4}, 32)
	if err := Configure("trusted", map[string][]byte{"trusted": trusted}); err != nil {
		t.Fatal(err)
	}
	token := Sign("q1", []byte(`{"offset":1}`))
	revokeFixtureKey(t, exposed)
	// A different key ID cannot make the exposed material trustworthy.
	for _, key := range [][]byte{exposed, append(append([]byte(nil), exposed...), 0)} {
		if err := Configure("renamed", map[string][]byte{"renamed": key}); err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("exposed or HMAC-equivalent active key was accepted: %v", err)
		}
	}
	if _, err := Verify("q1", token); err != nil {
		t.Fatalf("failed configuration replaced trusted ring: %v", err)
	}
	if next := Sign("q1", []byte(`{"offset":2}`)); !strings.HasPrefix(next, "q1.trusted.") {
		t.Fatal("failed configuration changed the signing key")
	}
}

func TestConfigureImmediatelyExcludesExposedVerificationKeys(t *testing.T) {
	exposed := bytes.Repeat([]byte{5}, 32)
	trusted := bytes.Repeat([]byte{6}, 32)
	if err := Configure("old", map[string][]byte{"old": exposed}); err != nil {
		t.Fatal(err)
	}
	old := Sign("q1", []byte(`{"offset":1}`))
	revokeFixtureKey(t, exposed)
	if err := Configure("new", map[string][]byte{"old": exposed, "renamed": append(append([]byte(nil), exposed...), 0), "new": trusted}); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify("q1", old); err == nil {
		t.Fatal("exposed key remained usable during rotation grace period")
	}
	if _, ok := configured.Load().keys["renamed"]; ok {
		t.Fatal("renamed exposed verification key was loaded")
	}
	if _, err := Verify("q1", Sign("q1", []byte(`{"offset":2}`))); err != nil {
		t.Fatalf("trusted replacement key does not verify: %v", err)
	}
}

func TestConfigureRejectsHashEquivalentExposedLongKey(t *testing.T) {
	exposed := bytes.Repeat([]byte{7}, 80)
	digest := sha256.Sum256(exposed)
	if err := Configure("old", map[string][]byte{"old": exposed}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"offset":1}`)
	old := Sign("q1", payload)
	if err := Configure("old", map[string][]byte{"old": digest[:]}); err != nil {
		t.Fatal(err)
	}
	if Sign("q1", payload) != old {
		t.Fatal("fixture keys do not produce equivalent HMAC signatures")
	}
	revokeFixtureKey(t, exposed)
	if err := Configure("renamed", map[string][]byte{"renamed": digest[:]}); err == nil {
		t.Fatal("hash-equivalent exposed signing key was accepted")
	}
}
