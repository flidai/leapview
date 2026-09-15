package migrationcapability

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
)

const (
	OwnerEvidenceVersion       = "migration-capability-owner-evidence/v1"
	OwnerEvidenceDigestDomain  = "leapview/migration-capability-owner-evidence/v1\n"
	OwnerEvidenceSigningDomain = "leapview/migration-capability-owner-evidence-signature/v1\n"
	OwnerRegistryVersion       = "migration-capability-owner-registry/v1"
	OwnerProofAlgorithm        = "Ed25519"
	MaxOwnerEvidenceBytes      = 128 << 10
	MaxOwnerKeys               = 64
)

var (
	ErrOwnerEvidenceInvalid   = errors.New("migration capability owner evidence is invalid")
	ErrOwnerEvidenceUntrusted = errors.New("migration capability owner evidence is untrusted")
)

// OwnerProof authenticates one immutable owner envelope. The private key is
// held by the subsystem owner and is never part of this contract.
type OwnerProof struct {
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

// OwnerEvidence carries the exact canonical capability bytes signed by the
// subsystem owner. The duplicated identities are intentional: verification
// rejects substitution between the signed envelope and nested capability.
type OwnerEvidence struct {
	Version                 string     `json:"version"`
	Owner                   Owner      `json:"owner"`
	ArtifactAdmissionDigest string     `json:"artifactAdmissionDigest"`
	TargetIdentityDigest    string     `json:"targetIdentityDigest"`
	Subsystem               Subsystem  `json:"subsystem"`
	CapabilityDigest        string     `json:"capabilityDigest"`
	CapabilityBytes         string     `json:"capabilityBytes"`
	Proof                   OwnerProof `json:"proof"`
}

// OwnerKey is trusted configuration, not caller evidence. A registry may keep
// old public keys so immutable historical evidence remains verifiable after
// signing-key rotation.
type OwnerKey struct {
	Owner     Owner  `json:"owner"`
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"publicKey"`
}

type OwnerRegistry struct {
	Version string     `json:"version"`
	Keys    []OwnerKey `json:"keys"`
}

func (r OwnerRegistry) Validate() error {
	_, err := r.normalizedKeys()
	return err
}

// Frozen returns a validated deep copy suitable for retaining across a
// production authority lifetime. Later caller mutations cannot replace a
// trusted public key after composition.
func (r OwnerRegistry) Frozen() (OwnerRegistry, error) {
	keys, err := r.normalizedKeys()
	if err != nil {
		return OwnerRegistry{}, err
	}
	return OwnerRegistry{Version: OwnerRegistryVersion, Keys: keys}, nil
}

type ownerEvidenceSigningDocument struct {
	Version                 string    `json:"version"`
	Owner                   Owner     `json:"owner"`
	ArtifactAdmissionDigest string    `json:"artifactAdmissionDigest"`
	TargetIdentityDigest    string    `json:"targetIdentityDigest"`
	Subsystem               Subsystem `json:"subsystem"`
	CapabilityDigest        string    `json:"capabilityDigest"`
	CapabilityBytes         string    `json:"capabilityBytes"`
	KeyID                   string    `json:"keyId"`
	Algorithm               string    `json:"algorithm"`
}

// SignOwnerEvidence is the qualification/publication helper used by a
// subsystem owner. Publication still verifies the resulting detached proof
// against independently trusted registry configuration.
func SignOwnerEvidence(capability Capability, keyID string, privateKey ed25519.PrivateKey) (OwnerEvidence, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return OwnerEvidence{}, fmt.Errorf("%w: private key", ErrOwnerEvidenceInvalid)
	}
	document, err := capability.CanonicalJSON()
	if err != nil {
		return OwnerEvidence{}, fmt.Errorf("%w: capability: %v", ErrOwnerEvidenceInvalid, err)
	}
	digest, err := capability.Digest()
	if err != nil {
		return OwnerEvidence{}, fmt.Errorf("%w: capability digest: %v", ErrOwnerEvidenceInvalid, err)
	}
	evidence := OwnerEvidence{
		Version:                 OwnerEvidenceVersion,
		Owner:                   capability.Owner,
		ArtifactAdmissionDigest: capability.ArtifactAdmissionDigest,
		TargetIdentityDigest:    capability.TargetIdentityDigest,
		Subsystem:               capability.Subsystem,
		CapabilityDigest:        digest,
		CapabilityBytes:         base64.StdEncoding.EncodeToString(document),
		Proof:                   OwnerProof{KeyID: keyID, Algorithm: OwnerProofAlgorithm},
	}
	message, err := evidence.signingBytes()
	if err != nil {
		return OwnerEvidence{}, err
	}
	evidence.Proof.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	return evidence.normalized()
}

func (e OwnerEvidence) CanonicalJSON() ([]byte, error) {
	normalized, err := e.normalized()
	if err != nil {
		return nil, err
	}
	document, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode migration capability owner evidence: %w", err)
	}
	if len(document) > MaxOwnerEvidenceBytes {
		return nil, fmt.Errorf("%w: canonical document exceeds %d bytes", ErrOwnerEvidenceInvalid, MaxOwnerEvidenceBytes)
	}
	return document, nil
}

func (e OwnerEvidence) Digest() (string, error) {
	document, err := e.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(OwnerEvidenceDigestDomain), document...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ParseOwnerEvidenceCanonical(document []byte) (OwnerEvidence, error) {
	if len(document) == 0 || len(document) > MaxOwnerEvidenceBytes {
		return OwnerEvidence{}, fmt.Errorf("%w: canonical document size", ErrOwnerEvidenceInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var evidence OwnerEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return OwnerEvidence{}, fmt.Errorf("%w: decode: %v", ErrOwnerEvidenceInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return OwnerEvidence{}, fmt.Errorf("%w: trailing data", ErrOwnerEvidenceInvalid)
	}
	canonical, err := evidence.CanonicalJSON()
	if err != nil {
		return OwnerEvidence{}, err
	}
	if !bytes.Equal(document, canonical) {
		return OwnerEvidence{}, ErrNonCanonical
	}
	return evidence.normalized()
}

// Verify authenticates an owner envelope and returns only its verified nested
// capability. The caller cannot select a key outside this trusted registry.
func (r OwnerRegistry) Verify(evidence OwnerEvidence) (Capability, error) {
	keys, err := r.normalizedKeys()
	if err != nil {
		return Capability{}, err
	}
	normalized, capability, err := evidence.decodedCapability(true)
	if err != nil {
		return Capability{}, err
	}
	keyIndex := slices.IndexFunc(keys, func(key OwnerKey) bool {
		return key.Owner == normalized.Owner && key.KeyID == normalized.Proof.KeyID && key.Algorithm == normalized.Proof.Algorithm
	})
	if keyIndex < 0 {
		return Capability{}, fmt.Errorf("%w: owner key is not registered", ErrOwnerEvidenceUntrusted)
	}
	publicKey, err := decodeBase64Exact(keys[keyIndex].PublicKey, ed25519.PublicKeySize)
	if err != nil {
		return Capability{}, fmt.Errorf("%w: registered public key", ErrOwnerEvidenceInvalid)
	}
	signature, _ := decodeBase64Exact(normalized.Proof.Signature, ed25519.SignatureSize)
	message, err := normalized.signingBytes()
	if err != nil {
		return Capability{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), message, signature) {
		return Capability{}, fmt.Errorf("%w: invalid owner signature", ErrOwnerEvidenceUntrusted)
	}
	return capability, nil
}

func (e OwnerEvidence) normalized() (OwnerEvidence, error) {
	normalized, _, err := e.decodedCapability(true)
	return normalized, err
}

func (e OwnerEvidence) decodedCapability(requireSignature bool) (OwnerEvidence, Capability, error) {
	if e.Version != OwnerEvidenceVersion {
		return OwnerEvidence{}, Capability{}, fmt.Errorf("%w: unsupported version", ErrOwnerEvidenceInvalid)
	}
	if e.Proof.Algorithm != OwnerProofAlgorithm || validateText(e.Proof.KeyID) != nil {
		return OwnerEvidence{}, Capability{}, fmt.Errorf("%w: owner proof profile", ErrOwnerEvidenceInvalid)
	}
	if requireSignature {
		if _, err := decodeBase64Exact(e.Proof.Signature, ed25519.SignatureSize); err != nil {
			return OwnerEvidence{}, Capability{}, fmt.Errorf("%w: owner signature", ErrOwnerEvidenceInvalid)
		}
	}
	capabilityBytes, err := base64.StdEncoding.Strict().DecodeString(e.CapabilityBytes)
	if err != nil || len(capabilityBytes) == 0 || len(capabilityBytes) > MaxCanonicalBytes {
		return OwnerEvidence{}, Capability{}, fmt.Errorf("%w: capability bytes", ErrOwnerEvidenceInvalid)
	}
	capability, err := ParseCanonical(capabilityBytes)
	if err != nil {
		return OwnerEvidence{}, Capability{}, fmt.Errorf("%w: capability: %v", ErrOwnerEvidenceInvalid, err)
	}
	digest, err := capability.Digest()
	if err != nil || e.CapabilityDigest != digest {
		return OwnerEvidence{}, Capability{}, fmt.Errorf("%w: capability digest", ErrOwnerEvidenceInvalid)
	}
	if e.Owner != capability.Owner || e.ArtifactAdmissionDigest != capability.ArtifactAdmissionDigest ||
		e.TargetIdentityDigest != capability.TargetIdentityDigest || e.Subsystem != capability.Subsystem {
		return OwnerEvidence{}, Capability{}, fmt.Errorf("%w: capability binding", ErrOwnerEvidenceInvalid)
	}
	return e, capability, nil
}

func (e OwnerEvidence) signingBytes() ([]byte, error) {
	normalized, _, err := e.decodedCapability(false)
	if err != nil {
		return nil, err
	}
	document, err := json.Marshal(ownerEvidenceSigningDocument{
		Version: normalized.Version, Owner: normalized.Owner,
		ArtifactAdmissionDigest: normalized.ArtifactAdmissionDigest,
		TargetIdentityDigest:    normalized.TargetIdentityDigest, Subsystem: normalized.Subsystem,
		CapabilityDigest: normalized.CapabilityDigest, CapabilityBytes: normalized.CapabilityBytes,
		KeyID: normalized.Proof.KeyID, Algorithm: normalized.Proof.Algorithm,
	})
	if err != nil {
		return nil, fmt.Errorf("encode migration capability owner signing document: %w", err)
	}
	return append([]byte(OwnerEvidenceSigningDomain), document...), nil
}

func (r OwnerRegistry) normalizedKeys() ([]OwnerKey, error) {
	if r.Version != OwnerRegistryVersion || len(r.Keys) == 0 || len(r.Keys) > MaxOwnerKeys {
		return nil, fmt.Errorf("%w: owner registry", ErrOwnerEvidenceInvalid)
	}
	keys := append([]OwnerKey(nil), r.Keys...)
	for _, key := range keys {
		ownerIdentity, ownerContract, err := ownerForSubsystemOwner(key.Owner)
		if err != nil || ownerIdentity != key.Owner.Identity || ownerContract != key.Owner.ContractVersion ||
			validateText(key.KeyID) != nil || key.Algorithm != OwnerProofAlgorithm {
			return nil, fmt.Errorf("%w: owner key profile", ErrOwnerEvidenceInvalid)
		}
		if _, err := decodeBase64Exact(key.PublicKey, ed25519.PublicKeySize); err != nil {
			return nil, fmt.Errorf("%w: owner public key", ErrOwnerEvidenceInvalid)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Owner.Identity != keys[j].Owner.Identity {
			return keys[i].Owner.Identity < keys[j].Owner.Identity
		}
		return keys[i].KeyID < keys[j].KeyID
	})
	for i := 1; i < len(keys); i++ {
		if keys[i].Owner == keys[i-1].Owner && keys[i].KeyID == keys[i-1].KeyID {
			return nil, fmt.Errorf("%w: duplicate owner key", ErrOwnerEvidenceInvalid)
		}
	}
	return keys, nil
}

func ownerForSubsystemOwner(owner Owner) (string, string, error) {
	for _, subsystem := range []Subsystem{SubsystemGoose, SubsystemRiverJobs, SubsystemDuckLake, SubsystemPhysicalPool} {
		identity, contract, err := ownerFor(subsystem)
		if err == nil && owner.Identity == identity && owner.ContractVersion == contract {
			return identity, contract, nil
		}
	}
	return "", "", fmt.Errorf("%w: unsupported owner", ErrOwnerEvidenceInvalid)
}

func decodeBase64Exact(value string, size int) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != size || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("base64 value is not canonical")
	}
	return decoded, nil
}
