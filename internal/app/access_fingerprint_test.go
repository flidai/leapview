package app

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestPostgresFingerprintKeyIsStableAndPurposeSeparated(t *testing.T) {
	secret := "csrf-secret-012345678901234567890123"
	first, err := postgresFingerprintKey("", secret)
	if err != nil {
		t.Fatalf("fallback fingerprint key: %v", err)
	}
	second, err := postgresFingerprintKey("", secret)
	if err != nil {
		t.Fatalf("repeat fallback fingerprint key: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same fingerprint input produced different keys")
	}
	if len(first) != sha256.Size {
		t.Fatalf("fingerprint key length = %d, want %d", len(first), sha256.Size)
	}
	want := sha256.Sum256([]byte(accessFingerprintPurpose + secret))
	if !bytes.Equal(first, want[:]) {
		t.Fatal("fingerprint key does not use the stable access purpose")
	}
}

func TestPostgresFingerprintKeyPrefersDedicatedSecret(t *testing.T) {
	csrf := "csrf-secret-012345678901234567890123"
	dedicated := "token-hash-secret-012345678901234567"
	got, err := postgresFingerprintKey(dedicated, csrf)
	if err != nil {
		t.Fatalf("dedicated fingerprint key: %v", err)
	}
	want := sha256.Sum256([]byte(accessFingerprintPurpose + dedicated))
	if !bytes.Equal(got, want[:]) {
		t.Fatal("dedicated token hash key was not selected")
	}
	fallback, err := postgresFingerprintKey("", csrf)
	if err != nil {
		t.Fatalf("fallback fingerprint key: %v", err)
	}
	if bytes.Equal(got, fallback) {
		t.Fatal("dedicated and fallback fingerprint keys unexpectedly match")
	}
}

func TestPostgresFingerprintKeyRejectsShortSelectedSecret(t *testing.T) {
	tests := []struct {
		name      string
		dedicated string
		csrf      string
	}{
		{name: "short fallback", csrf: "short"},
		{name: "short dedicated does not fall back", dedicated: "short", csrf: "csrf-secret-012345678901234567890123"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := postgresFingerprintKey(test.dedicated, test.csrf); err == nil {
				t.Fatal("short selected secret was accepted")
			}
		})
	}
}
