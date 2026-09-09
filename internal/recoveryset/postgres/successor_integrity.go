package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/flidai/leapview/internal/recoveryset/successor"
)

const successorVerificationTimeLayout = "2006-01-02T15:04:05.000000Z"

// successorVerificationMetadata is an archival observation of the generation
// and independently selected verification clock used for the first durable
// association. It is not a replacement for the independently resolved trust
// input used during a read.
type successorVerificationMetadata struct {
	Generation TrustGeneration
	VerifiedAt time.Time
}

type successorVerificationMetadataJSON struct {
	IncarnationID string `json:"incarnation_id"`
	Revision      int64  `json:"revision"`
	PolicyDigest  string `json:"policy_digest"`
	VerifiedAt    string `json:"verified_at"`
}

// marshalSuccessorVerificationMetadata emits the current flat metadata
// format, extending the original generation tuple with a canonical UTC
// microsecond verification clock. The clock is selected by the trust resolver,
// never taken from a receipt or request.
func marshalSuccessorVerificationMetadata(generation TrustGeneration, verifiedAt time.Time) ([]byte, error) {
	if err := validateSuccessorTrustGeneration(generation); err != nil {
		return nil, err
	}
	verifiedAt, err := canonicalSuccessorVerificationTime(verifiedAt)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(successorVerificationMetadataJSON{
		IncarnationID: generation.IncarnationID,
		Revision:      generation.Revision,
		PolicyDigest:  generation.PolicyDigest,
		VerifiedAt:    verifiedAt.Format(successorVerificationTimeLayout),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal successor verification metadata: %w", err)
	}
	return raw, nil
}

// parseSuccessorVerificationMetadata accepts only the exact JSON shape emitted
// by marshalSuccessorVerificationMetadata. In particular, it rejects unknown
// fields, duplicate fields (via canonical-byte comparison), trailing values,
// non-canonical generation values, and non-UTC/microsecond clocks.
func parseSuccessorVerificationMetadata(raw []byte) (successorVerificationMetadata, error) {
	if len(raw) == 0 || len(raw) > successorLocatorLimit {
		return successorVerificationMetadata{}, errors.New("successor verification metadata is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var encoded successorVerificationMetadataJSON
	if err := decoder.Decode(&encoded); err != nil {
		return successorVerificationMetadata{}, fmt.Errorf("parse successor verification metadata: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return successorVerificationMetadata{}, errors.New("successor verification metadata has trailing JSON")
		}
		return successorVerificationMetadata{}, fmt.Errorf("parse successor verification metadata trailing JSON: %w", err)
	}
	generation := TrustGeneration{IncarnationID: encoded.IncarnationID, Revision: encoded.Revision, PolicyDigest: encoded.PolicyDigest}
	verifiedAt, err := time.Parse(successorVerificationTimeLayout, encoded.VerifiedAt)
	if err != nil {
		return successorVerificationMetadata{}, fmt.Errorf("parse successor verification clock: %w", err)
	}
	canonical, err := marshalSuccessorVerificationMetadata(generation, verifiedAt)
	if err != nil {
		return successorVerificationMetadata{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return successorVerificationMetadata{}, errors.New("successor verification metadata is not canonical JSON")
	}
	return successorVerificationMetadata{Generation: generation, VerifiedAt: verifiedAt}, nil
}

// successorVerificationMetadataGenerationEqual is intended for immutable
// write-replay checks. The first winner's VerifiedAt is retained; a retry must
// match the identity/generation but must not replace that original clock.
func successorVerificationMetadataGenerationEqual(raw []byte, want TrustGeneration) bool {
	got, err := parseSuccessorVerificationMetadata(raw)
	return err == nil && got.Generation == want
}

// validateSuccessorVerificationMetadataRead validates historical metadata and
// rejects a stored verification clock that lies in the future of the current
// independently selected trust clock. It intentionally does not require the
// historical generation to equal the current generation; current-generation
// fencing is a separate policy check.
func validateSuccessorVerificationMetadataRead(raw []byte, current time.Time) error {
	got, err := parseSuccessorVerificationMetadata(raw)
	if err != nil {
		return err
	}
	if current.IsZero() {
		return errors.New("current successor verification clock is missing")
	}
	if got.VerifiedAt.After(current.UTC()) {
		return errors.New("successor verification metadata clock is in the future")
	}
	return nil
}

func canonicalSuccessorVerificationTime(value time.Time) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, errors.New("successor verification clock is missing")
	}
	return value.UTC().Truncate(time.Microsecond), nil
}

func validateSuccessorTrustGeneration(generation TrustGeneration) error {
	if !canonicalUUID(generation.IncarnationID) || generation.Revision <= 0 || !domainDigest.MatchString(generation.PolicyDigest) {
		return fmt.Errorf("%w: malformed successor trust generation", ErrSuccessorInvalid)
	}
	return nil
}

// successorSet3ScalarRow is the complete owner-associated scalar tuple kept
// beside recovery_set_v3.canonical_bytes.  It intentionally excludes created_at
// and generated hashes: those are persistence timestamps/derivations, while
// every identity and association scalar is compared explicitly.
type successorSet3ScalarRow struct {
	SetID string

	SchemaVersion int32

	ManifestFamily  string
	ManifestVersion int32
	ManifestDigest  string

	AnchorFamily  string
	AnchorVersion int32
	AnchorDigest  string

	ProfileFamily  string
	ProfileVersion int32
	ProfileDigest  string

	ReceiptFamily     string
	ReceiptVersion    int32
	ReceiptDigest     string
	ReceiptCoreDigest string

	AuthorityFamily  string
	AuthorityVersion int32
	AuthorityDigest  string

	FrontierProjection []byte
	FrontierDigest     string
	CanonicalBytes     []byte
	Status             string
	CreatedBy          string
}

// successorSet3ScalarsForOwner derives the expected SQL tuple from the
// canonical successor set and independently selected evidence.  canonical is
// checked against the owner serialization so a caller cannot accidentally
// compare SQL metadata against a different byte representation.
func successorSet3ScalarsForOwner(set successor.RecoverySet3, trust TrustInput, canonical []byte) (successorSet3ScalarRow, error) {
	ownerCanonical, err := set.CanonicalJSON()
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	if len(canonical) == 0 {
		canonical = ownerCanonical
	}
	if !bytes.Equal(canonical, ownerCanonical) {
		return successorSet3ScalarRow{}, fmt.Errorf("%w: successor set canonical bytes differ from owner", ErrSuccessorConflict)
	}
	frontier, err := frontierProjection(set)
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	frontierDigest, err := set.Digest()
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	manifestDigest, err := trust.Evidence.Manifest.Digest()
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	anchorDigest, err := trust.Evidence.Anchor.Digest()
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	profileDigest, err := trust.Evidence.Profiles.Digest()
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	receiptDigest, err := trust.Evidence.Receipt.Digest()
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	receiptCoreDigest, err := trust.Evidence.Receipt.Core.Digest()
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	authorityDigest, err := trust.Evidence.Authorities.Digest()
	if err != nil {
		return successorSet3ScalarRow{}, err
	}
	return successorSet3ScalarRow{
		SetID: set.ID,

		SchemaVersion: set.SchemaVersion,

		ManifestFamily:  dbFamily(PayloadFamilyManifest),
		ManifestVersion: familyVersion(PayloadFamilyManifest),
		ManifestDigest:  manifestDigest,

		AnchorFamily:  dbFamily(PayloadFamilyAnchor),
		AnchorVersion: familyVersion(PayloadFamilyAnchor),
		AnchorDigest:  anchorDigest,

		ProfileFamily:  dbFamily(PayloadFamilyProfiles),
		ProfileVersion: familyVersion(PayloadFamilyProfiles),
		ProfileDigest:  profileDigest,

		ReceiptFamily:     dbFamily(PayloadFamilyReceipt),
		ReceiptVersion:    familyVersion(PayloadFamilyReceipt),
		ReceiptDigest:     receiptDigest,
		ReceiptCoreDigest: receiptCoreDigest,

		AuthorityFamily:  dbFamily(PayloadFamilyAuthority),
		AuthorityVersion: familyVersion(PayloadFamilyAuthority),
		AuthorityDigest:  authorityDigest,

		FrontierProjection: append([]byte(nil), frontier...),
		FrontierDigest:     frontierDigest,
		CanonicalBytes:     append([]byte(nil), canonical...),
		Status:             string(set.Status),
		CreatedBy:          set.CreatedBy,
	}, nil
}

func successorSet3ScalarsEqual(stored, want successorSet3ScalarRow) bool {
	return stored.SetID == want.SetID &&
		stored.SchemaVersion == want.SchemaVersion &&
		stored.ManifestFamily == want.ManifestFamily &&
		stored.ManifestVersion == want.ManifestVersion &&
		stored.ManifestDigest == want.ManifestDigest &&
		stored.AnchorFamily == want.AnchorFamily &&
		stored.AnchorVersion == want.AnchorVersion &&
		stored.AnchorDigest == want.AnchorDigest &&
		stored.ProfileFamily == want.ProfileFamily &&
		stored.ProfileVersion == want.ProfileVersion &&
		stored.ProfileDigest == want.ProfileDigest &&
		stored.ReceiptFamily == want.ReceiptFamily &&
		stored.ReceiptVersion == want.ReceiptVersion &&
		stored.ReceiptDigest == want.ReceiptDigest &&
		stored.ReceiptCoreDigest == want.ReceiptCoreDigest &&
		stored.AuthorityFamily == want.AuthorityFamily &&
		stored.AuthorityVersion == want.AuthorityVersion &&
		stored.AuthorityDigest == want.AuthorityDigest &&
		bytes.Equal(stored.FrontierProjection, want.FrontierProjection) &&
		stored.FrontierDigest == want.FrontierDigest &&
		bytes.Equal(stored.CanonicalBytes, want.CanonicalBytes) &&
		stored.Status == want.Status &&
		stored.CreatedBy == want.CreatedBy
}

type successorSet3RootRow struct {
	SetID                    string
	RootKind                 string
	RootURI                  string
	VersionID                string
	RootDigest               string
	ProviderRecoveryFrontier string
	CanonicalBytes           []byte
}

func successorSet3RootRowsForOwner(set successor.RecoverySet3) ([]successorSet3RootRow, error) {
	normalized, err := set.Normalize()
	if err != nil {
		return nil, err
	}
	rows := make([]successorSet3RootRow, 0, len(normalized.ObjectRoots))
	for _, root := range normalized.ObjectRoots {
		canonical, err := json.Marshal(root)
		if err != nil {
			return nil, fmt.Errorf("marshal successor root: %w", err)
		}
		rows = append(rows, successorSet3RootRow{
			SetID:                    normalized.ID,
			RootKind:                 root.Kind,
			RootURI:                  root.URI,
			VersionID:                root.VersionID,
			RootDigest:               root.Digest,
			ProviderRecoveryFrontier: root.ProviderRecoveryFrontier,
			CanonicalBytes:           canonical,
		})
	}
	return rows, nil
}

// successorSet3RootRowsEqual compares the complete root association as an
// unordered set keyed by root kind.  It rejects duplicates, missing roots, and
// extra roots instead of allowing an ON CONFLICT no-op to hide a mismatch.
func successorSet3RootRowsEqual(stored []successorSet3RootRow, want []successorSet3RootRow) bool {
	if len(stored) != len(want) {
		return false
	}
	expected := make(map[string]successorSet3RootRow, len(want))
	for _, row := range want {
		if _, exists := expected[row.RootKind]; exists {
			return false
		}
		expected[row.RootKind] = row
	}
	seen := make(map[string]struct{}, len(stored))
	for _, row := range stored {
		if _, exists := seen[row.RootKind]; exists {
			return false
		}
		seen[row.RootKind] = struct{}{}
		wantRow, exists := expected[row.RootKind]
		if !exists || row.SetID != wantRow.SetID || row.RootURI != wantRow.RootURI || row.VersionID != wantRow.VersionID || row.RootDigest != wantRow.RootDigest || row.ProviderRecoveryFrontier != wantRow.ProviderRecoveryFrontier || !bytes.Equal(row.CanonicalBytes, wantRow.CanonicalBytes) {
			return false
		}
	}
	return len(seen) == len(expected)
}
