package successor

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"sort"
)

// ReceiptCore is the cycle-free capture commitment embedded in a signed
// receipt and referenced by manifest.capture.receipt_digest.
type ReceiptCore struct {
	CoreVersion                 int32  `json:"core_version"`
	AuthorityID                 string `json:"authority_id"`
	KeyID                       string `json:"key_id"`
	CaptureID                   string `json:"capture_id"`
	SetID                       string `json:"set_id"`
	StartedAt                   string `json:"started_at"`
	CompletedAt                 string `json:"completed_at"`
	SourceFrontierAnchorDigest  string `json:"source_frontier_anchor_digest"`
	ManagedClosureDigest        string `json:"managed_closure_digest"`
	ProviderProfileDigest       string `json:"provider_profile_digest"`
	ObservationProjectionDigest string `json:"observation_projection_digest"`
	MembershipCount             int64  `json:"membership_count"`
	ObjectCount                 int64  `json:"object_count"`
	Result                      string `json:"result"`
}

// SignedReceipt is detached from the manifest to avoid a digest cycle. The
// signature covers receipt_version, core and manifest_digest, not the core
// digest or a digest of the signature itself.
type SignedReceipt struct {
	ReceiptVersion int32       `json:"receipt_version"`
	Core           ReceiptCore `json:"core"`
	ManifestDigest string      `json:"manifest_digest"`
	Signature      string      `json:"signature"`
}

type AuthorityKey struct {
	AuthorityID            string   `json:"authority_id"`
	KeyID                  string   `json:"key_id"`
	Algorithm              string   `json:"algorithm"`
	PublicKey              string   `json:"public_key"`
	NotBefore              string   `json:"not_before"`
	NotAfter               string   `json:"not_after"`
	Revoked                bool     `json:"revoked"`
	ProviderProfileDigests []string `json:"provider_profile_digests"`
}

type AuthorityRegistry struct {
	RegistryVersion int32          `json:"registry_version"`
	Keys            []AuthorityKey `json:"keys"`
}

type Receipt = SignedReceipt

func (c ReceiptCore) Validate() error {
	if c.CoreVersion != ReceiptCoreVersion {
		return fmt.Errorf("%w: core version %d", ErrUnsupportedVersion, c.CoreVersion)
	}
	if err := id(c.AuthorityID, "receipt authority_id"); err != nil {
		return err
	}
	if err := id(c.KeyID, "receipt key_id"); err != nil {
		return err
	}
	if err := id(c.CaptureID, "receipt capture_id"); err != nil {
		return err
	}
	if !canonicalUUID(c.SetID) {
		return invalid("receipt set_id must be a canonical UUID")
	}
	start, err := canonicalTime(c.StartedAt)
	if err != nil {
		return err
	}
	end, err := canonicalTime(c.CompletedAt)
	if err != nil || end.Before(start) {
		return invalid("receipt chronology is invalid")
	}
	if !digest(c.SourceFrontierAnchorDigest) {
		return invalid("receipt source anchor digest is malformed")
	}
	if !digest(c.ManagedClosureDigest) {
		return invalid("receipt managed closure digest is malformed")
	}
	if !digest(c.ProviderProfileDigest) {
		return invalid("receipt provider profile digest is malformed")
	}
	if !digest(c.ObservationProjectionDigest) {
		return invalid("receipt observation projection digest is malformed")
	}
	if c.MembershipCount < 0 || c.MembershipCount > MaxMembers || c.ObjectCount < 0 || c.ObjectCount > MaxMembers {
		return invalid("receipt counts are out of bounds")
	}
	if c.Result != "verified" {
		return invalid("receipt result must be verified")
	}
	return nil
}

func (c ReceiptCore) CanonicalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return marshal(c, MaxDocumentBytes)
}
func (c ReceiptCore) Digest() (string, error) {
	raw, err := c.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return hash(coreDomain, raw), nil
}

// ParseReceiptCore parses the standalone capture-core transport document. The
// core is also embedded in SignedReceipt for signature verification, but this
// parser intentionally verifies the independently retained canonical bytes.
func ParseReceiptCore(raw []byte) (ReceiptCore, error) {
	var core ReceiptCore
	if err := strictDecode(raw, &core, MaxDocumentBytes); err != nil {
		return core, err
	}
	if _, err := exactObject(raw, map[string]bool{
		"core_version": true, "authority_id": true, "key_id": true,
		"capture_id": true, "set_id": true, "started_at": true,
		"completed_at": true, "source_frontier_anchor_digest": true,
		"managed_closure_digest": true, "provider_profile_digest": true,
		"observation_projection_digest": true, "membership_count": true,
		"object_count": true, "result": true,
	}, map[string]bool{
		"core_version": true, "authority_id": true, "key_id": true,
		"capture_id": true, "set_id": true, "started_at": true,
		"completed_at": true, "source_frontier_anchor_digest": true,
		"managed_closure_digest": true, "provider_profile_digest": true,
		"observation_projection_digest": true, "membership_count": true,
		"object_count": true, "result": true,
	}, "receipt core"); err != nil {
		return core, err
	}
	canonical, err := core.CanonicalJSON()
	if err != nil {
		return core, err
	}
	if !bytes.Equal(raw, canonical) {
		return core, invalid("receipt core is not canonical JSON")
	}
	return core, nil
}

func (r SignedReceipt) Validate() error {
	if r.ReceiptVersion != ReceiptVersion {
		return fmt.Errorf("%w: receipt version %d", ErrUnsupportedVersion, r.ReceiptVersion)
	}
	if err := r.Core.Validate(); err != nil {
		return err
	}
	if !digest(r.ManifestDigest) {
		return invalid("receipt manifest digest is malformed")
	}
	if _, err := base64Exact(r.Signature, ed25519.SignatureSize); err != nil {
		return err
	}
	return nil
}

func (r SignedReceipt) CanonicalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return marshal(r, MaxDocumentBytes)
}
func (r SignedReceipt) Digest() (string, error) {
	raw, err := r.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return hash(receiptDomain, raw), nil
}

func (r SignedReceipt) SignedBytes() ([]byte, error) {
	if r.ReceiptVersion != ReceiptVersion {
		return nil, fmt.Errorf("%w: receipt version %d", ErrUnsupportedVersion, r.ReceiptVersion)
	}
	if err := r.Core.Validate(); err != nil {
		return nil, err
	}
	if !digest(r.ManifestDigest) {
		return nil, invalid("receipt manifest digest is malformed")
	}
	type unsigned struct {
		ReceiptVersion int32       `json:"receipt_version"`
		Core           ReceiptCore `json:"core"`
		ManifestDigest string      `json:"manifest_digest"`
	}
	raw, err := marshal(unsigned{r.ReceiptVersion, r.Core, r.ManifestDigest}, MaxDocumentBytes)
	if err != nil {
		return nil, err
	}
	return append([]byte(signatureDomain), raw...), nil
}

func (r SignedReceipt) Sign(privateKey ed25519.PrivateKey) (SignedReceipt, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return SignedReceipt{}, invalid("Ed25519 private key size")
	}
	unsigned, err := r.SignedBytes()
	if err != nil {
		return SignedReceipt{}, err
	}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, unsigned))
	return r, nil
}

func SignReceipt(manifest ManagedManifest2, core ReceiptCore, privateKey ed25519.PrivateKey) (SignedReceipt, error) {
	coreDigest, err := core.Digest()
	if err != nil {
		return SignedReceipt{}, err
	}
	if manifest.Capture.ReceiptDigest != coreDigest {
		return SignedReceipt{}, invalid("manifest receipt core digest does not match")
	}
	if err := coreMatchesManifest(core, manifest); err != nil {
		return SignedReceipt{}, err
	}
	manifestDigest, err := manifest.Digest()
	if err != nil {
		return SignedReceipt{}, err
	}
	return (SignedReceipt{ReceiptVersion: ReceiptVersion, Core: core, ManifestDigest: manifestDigest}).Sign(privateKey)
}

func ParseReceipt(raw []byte) (SignedReceipt, error) {
	var receipt SignedReceipt
	if err := strictDecode(raw, &receipt, MaxDocumentBytes); err != nil {
		return receipt, err
	}
	if _, err := exactObject(raw, map[string]bool{"receipt_version": true, "core": true, "manifest_digest": true, "signature": true}, map[string]bool{"receipt_version": true, "core": true, "manifest_digest": true, "signature": true}, "receipt"); err != nil {
		return receipt, err
	}
	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		return receipt, err
	}
	if !bytes.Equal(raw, canonical) {
		return receipt, invalid("receipt is not canonical JSON")
	}
	return receipt, nil
}

func (r SignedReceipt) Verify(manifest ManagedManifest2, scope ExpectedScope, registry AuthorityRegistry, expectedProfileDigest string) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := manifest.ValidateFor(scope); err != nil {
		return err
	}
	if err := manifest.validateObservations(); err != nil {
		return err
	}
	manifestDigest, err := manifest.Digest()
	if err != nil || manifestDigest != r.ManifestDigest {
		return invalid("receipt manifest binding mismatch")
	}
	if err := coreMatchesManifest(r.Core, manifest); err != nil {
		return err
	}
	coreDigest, err := r.Core.Digest()
	if err != nil || coreDigest != manifest.Capture.ReceiptDigest {
		return invalid("receipt core binding mismatch")
	}
	if expectedProfileDigest == "" || r.Core.ProviderProfileDigest != expectedProfileDigest {
		return invalid("receipt provider profile binding mismatch")
	}
	key, err := registry.Lookup(r.Core.AuthorityID, r.Core.KeyID)
	if err != nil {
		return err
	}
	if err := key.Validate(); err != nil {
		return err
	}
	if key.Revoked {
		return invalid("receipt authority key is revoked")
	}
	start, _ := canonicalTime(r.Core.StartedAt)
	end, _ := canonicalTime(r.Core.CompletedAt)
	notBefore, _ := canonicalTime(key.NotBefore)
	notAfter, _ := canonicalTime(key.NotAfter)
	if start.Before(notBefore) || end.Before(notBefore) || !start.Before(notAfter) || !end.Before(notAfter) {
		return invalid("receipt capture is outside authority validity")
	}
	allowed := false
	for _, profile := range key.ProviderProfileDigests {
		if profile == expectedProfileDigest {
			allowed = true
			break
		}
	}
	if !allowed {
		return invalid("receipt provider profile is not authorized")
	}
	public, err := base64Exact(key.PublicKey, ed25519.PublicKeySize)
	if err != nil {
		return err
	}
	message, err := r.SignedBytes()
	if err != nil {
		return err
	}
	signature, _ := base64Exact(r.Signature, ed25519.SignatureSize)
	if !ed25519.Verify(ed25519.PublicKey(public), message, signature) {
		return invalid("invalid receipt signature")
	}
	memberships, objects, err := manifest.ObservationCounts()
	if err != nil || memberships != r.Core.MembershipCount || objects != r.Core.ObjectCount {
		return invalid("receipt observation counts do not match manifest")
	}
	projection, err := manifest.ObservationProjectionDigest()
	if err != nil || projection != r.Core.ObservationProjectionDigest {
		return invalid("receipt observation projection mismatch")
	}
	return nil
}

func coreMatchesManifest(core ReceiptCore, manifest ManagedManifest2) error {
	if core.AuthorityID != manifest.Capture.AuthorityID || core.CaptureID != manifest.Capture.CaptureID || core.SetID != manifest.SetID || core.StartedAt != manifest.Capture.StartedAt || core.CompletedAt != manifest.Capture.CompletedAt || core.SourceFrontierAnchorDigest != manifest.SourceFrontierAnchorDigest || core.ManagedClosureDigest != manifest.ManagedClosureDigest {
		return invalid("receipt core does not match manifest capture")
	}
	return nil
}

func (k AuthorityKey) Validate() error {
	if err := id(k.AuthorityID, "authority_id"); err != nil {
		return err
	}
	if err := id(k.KeyID, "authority key_id"); err != nil {
		return err
	}
	if k.Algorithm != "Ed25519" {
		return invalid("authority algorithm must be Ed25519")
	}
	if _, err := base64Exact(k.PublicKey, ed25519.PublicKeySize); err != nil {
		return err
	}
	before, err := canonicalTime(k.NotBefore)
	if err != nil {
		return err
	}
	after, err := canonicalTime(k.NotAfter)
	if err != nil || !before.Before(after) {
		return invalid("authority validity interval is invalid")
	}
	if k.ProviderProfileDigests == nil {
		return invalid("authority profile allowlist must be explicit")
	}
	for i, profile := range k.ProviderProfileDigests {
		if !digest(profile) {
			return invalid("authority profile digest %d is malformed", i)
		}
		if i > 0 && k.ProviderProfileDigests[i-1] >= profile {
			return invalid("authority profile allowlist must be sorted and unique")
		}
	}
	return nil
}

func (r AuthorityRegistry) Validate() error {
	if r.RegistryVersion != AuthorityRegistryVersion {
		return fmt.Errorf("%w: authority registry version %d", ErrUnsupportedVersion, r.RegistryVersion)
	}
	if r.Keys == nil {
		return invalid("authority registry keys must be explicit")
	}
	seen := map[string]struct{}{}
	for _, key := range r.Keys {
		if err := key.Validate(); err != nil {
			return err
		}
		identity := key.AuthorityID + "\x00" + key.KeyID
		if _, exists := seen[identity]; exists {
			return invalid("duplicate authority key")
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func (r AuthorityRegistry) Normalize() (AuthorityRegistry, error) {
	if err := r.Validate(); err != nil {
		return AuthorityRegistry{}, err
	}
	normalized := AuthorityRegistry{RegistryVersion: r.RegistryVersion, Keys: make([]AuthorityKey, len(r.Keys))}
	copy(normalized.Keys, r.Keys)
	for i := range normalized.Keys {
		normalized.Keys[i].ProviderProfileDigests = make([]string, len(r.Keys[i].ProviderProfileDigests))
		copy(normalized.Keys[i].ProviderProfileDigests, r.Keys[i].ProviderProfileDigests)
	}
	sort.Slice(normalized.Keys, func(i, j int) bool {
		if normalized.Keys[i].AuthorityID != normalized.Keys[j].AuthorityID {
			return normalized.Keys[i].AuthorityID < normalized.Keys[j].AuthorityID
		}
		return normalized.Keys[i].KeyID < normalized.Keys[j].KeyID
	})
	return normalized, nil
}
func (r AuthorityRegistry) CanonicalJSON() ([]byte, error) {
	normalized, err := r.Normalize()
	if err != nil {
		return nil, err
	}
	return marshal(normalized, MaxDocumentBytes)
}
func (r AuthorityRegistry) Digest() (string, error) {
	raw, err := r.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return hash("leapview/authority-registry/v2\n", raw), nil
}
func (r AuthorityRegistry) Lookup(authorityID, keyID string) (AuthorityKey, error) {
	if err := r.Validate(); err != nil {
		return AuthorityKey{}, err
	}
	for _, key := range r.Keys {
		if key.AuthorityID == authorityID && key.KeyID == keyID {
			copied := make([]string, len(key.ProviderProfileDigests))
			copy(copied, key.ProviderProfileDigests)
			key.ProviderProfileDigests = copied
			return key, nil
		}
	}
	return AuthorityKey{}, invalid("authority key is not trusted")
}

func ParseAuthorityRegistry(raw []byte) (AuthorityRegistry, error) {
	var registry AuthorityRegistry
	if err := strictDecode(raw, &registry, MaxDocumentBytes); err != nil {
		return registry, err
	}
	if _, err := exactObject(raw, map[string]bool{"registry_version": true, "keys": true}, map[string]bool{"registry_version": true, "keys": true}, "authority registry"); err != nil {
		return registry, err
	}
	canonical, err := registry.CanonicalJSON()
	if err != nil {
		return registry, err
	}
	if !bytes.Equal(raw, canonical) {
		return registry, invalid("authority registry is not canonical JSON")
	}
	return registry, nil
}
