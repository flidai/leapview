package migrationcapability

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"
)

func TestOwnerEvidenceSignsCanonicalCapability(t *testing.T) {
	privateKey := ownerEvidenceTestKey(7)
	capability := duckLakeFixture()
	evidence, err := SignOwnerEvidence(capability, "ducklake-key-1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	registry := ownerEvidenceRegistry(capability.Owner, "ducklake-key-1", privateKey)
	verified, err := registry.Verify(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(verified, mustNormalize(t, capability)) {
		t.Fatalf("verified capability changed: %#v", verified)
	}
	document, err := evidence.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseOwnerEvidenceCanonical(document)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := evidence.Digest()
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := SignOwnerEvidence(capability, "ducklake-key-1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	repeatedDocument, _ := repeated.CanonicalJSON()
	repeatedDigest, _ := repeated.Digest()
	if !bytes.Equal(document, repeatedDocument) || digest != repeatedDigest || parsed.Proof.Signature != evidence.Proof.Signature {
		t.Fatal("same owner evidence did not produce deterministic bytes, digest, and signature")
	}
}

func TestOwnerEvidenceRejectsUntrustedAndSubstitutedClaims(t *testing.T) {
	privateKey := ownerEvidenceTestKey(8)
	capability := duckLakeFixture()
	evidence, err := SignOwnerEvidence(capability, "ducklake-key-1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	registry := ownerEvidenceRegistry(capability.Owner, "ducklake-key-1", privateKey)

	tests := map[string]struct {
		mutate   func(*OwnerEvidence)
		registry OwnerRegistry
	}{
		"missing proof": {mutate: func(value *OwnerEvidence) { value.Proof.Signature = "" }, registry: registry},
		"invalid proof": {mutate: func(value *OwnerEvidence) {
			value.Proof.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		}, registry: registry},
		"wrong artifact binding": {mutate: func(value *OwnerEvidence) { value.ArtifactAdmissionDigest = testDigest("d") }, registry: registry},
		"wrong target binding":   {mutate: func(value *OwnerEvidence) { value.TargetIdentityDigest = testDigest("e") }, registry: registry},
		"wrong owner identity":   {mutate: func(value *OwnerEvidence) { value.Owner.Identity = GooseOwnerIdentity }, registry: registry},
		"unregistered key":       {mutate: func(value *OwnerEvidence) { value.Proof.KeyID = "unknown-key" }, registry: registry},
		"attacker registry key": {
			mutate:   func(*OwnerEvidence) {},
			registry: ownerEvidenceRegistry(capability.Owner, "ducklake-key-1", ownerEvidenceTestKey(9)),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			mutated := evidence
			test.mutate(&mutated)
			if _, err := test.registry.Verify(mutated); err == nil ||
				(!errors.Is(err, ErrOwnerEvidenceInvalid) && !errors.Is(err, ErrOwnerEvidenceUntrusted)) {
				t.Fatalf("Verify error = %v, want owner-evidence rejection", err)
			}
		})
	}
}

func TestOwnerRegistryRejectsUnsupportedOwnerProfiles(t *testing.T) {
	privateKey := ownerEvidenceTestKey(10)
	registry := ownerEvidenceRegistry(Owner{Identity: "caller.owner", ContractVersion: "caller/v1"}, "key-1", privateKey)
	if err := registry.Validate(); !errors.Is(err, ErrOwnerEvidenceInvalid) {
		t.Fatalf("Validate error = %v, want invalid owner registry", err)
	}
}

func TestOwnerRegistryFrozenDoesNotAliasCallerConfiguration(t *testing.T) {
	privateKey := ownerEvidenceTestKey(11)
	capability := duckLakeFixture()
	registry := ownerEvidenceRegistry(capability.Owner, "ducklake-key-1", privateKey)
	frozen, err := registry.Frozen()
	if err != nil {
		t.Fatal(err)
	}
	registry.Keys[0].PublicKey = base64.StdEncoding.EncodeToString(ownerEvidenceTestKey(12).Public().(ed25519.PublicKey))
	evidence, err := SignOwnerEvidence(capability, "ducklake-key-1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := frozen.Verify(evidence); err != nil {
		t.Fatalf("frozen registry changed after caller mutation: %v", err)
	}
}

func ownerEvidenceRegistry(owner Owner, keyID string, privateKey ed25519.PrivateKey) OwnerRegistry {
	return OwnerRegistry{
		Version: OwnerRegistryVersion,
		Keys: []OwnerKey{{
			Owner: owner, KeyID: keyID, Algorithm: OwnerProofAlgorithm,
			PublicKey: base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
		}},
	}
}

func ownerEvidenceTestKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}
