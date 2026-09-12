package postgres

// The successor repository is a storage foundation only. It creates and reads
// an immutable evidence graph; it has no publication, admission, or restore
// operation. PostgreSQL metadata and cached bytes are never a substitute for
// exact off-host reads or independently selected trust.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/flidai/leapview/internal/recoveryset"
	recoverydb "github.com/flidai/leapview/internal/recoveryset/postgres/internal/db"
	"github.com/flidai/leapview/internal/recoveryset/successor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrSuccessorInvalid   = errors.New("invalid successor persistence input")
	ErrSuccessorConflict  = errors.New("successor persistence identity conflicts with durable record")
	ErrSuccessorNotFound  = errors.New("successor persistence record not found")
	ErrSuccessorTampered  = errors.New("successor persistence evidence failed exact verification")
	ErrSuccessorUntrusted = errors.New("successor persistence trust input is unavailable")
)

const (
	PayloadFamilySet       = "leapview.recovery-set"
	PayloadFamilyManifest  = "leapview.managed-observation-manifest"
	PayloadFamilyAnchor    = "leapview.recovery-source-anchor"
	PayloadFamilyProfiles  = "leapview.managed-provider-profiles"
	PayloadFamilyCore      = "leapview.managed-capture-core"
	PayloadFamilyReceipt   = "leapview.managed-capture-receipt"
	PayloadFamilyAuthority = "leapview.authority-registry"
)

var rawSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var domainDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidatedLocator is a typed exact-version identity. Endpoint is metadata
// checked for canonical spelling only; the reader must use its own configured
// client/profile and must not treat this value as a mutable URL.
type ValidatedLocator struct {
	Backend                string
	StorageProfileID       string
	StorageProfileRevision int64
	AccountIdentity        string
	Endpoint               string
	Region                 string
	Bucket                 string
	Namespace              string
	Key                    string
	VersionID              string
	PayloadFamily          string
	PayloadVersion         int32
	PayloadDigest          string
	PayloadSHA256          string
	PayloadSize            int64
}

type PayloadReference struct {
	Locator        ValidatedLocator
	CanonicalBytes []byte
}

type TrustGeneration struct {
	IncarnationID string `json:"incarnation_id"`
	Revision      int64  `json:"revision"`
	PolicyDigest  string `json:"policy_digest"`
}

// TrustInput is returned by the independent trust resolver. It is never read
// from a persisted payload or SQL cache.
type TrustInput struct {
	Evidence   successor.Evidence
	Generation TrustGeneration
	// WorkerFence is the coordinator-resolved assignment fence that produced
	// the evidence. PostgreSQL independently locks and compares its current
	// fence before association; it is not part of the frozen wire contracts.
	WorkerFence int64
}

type EvidencePayloads struct {
	Set       PayloadReference
	Manifest  PayloadReference
	Anchor    PayloadReference
	Profiles  PayloadReference
	Core      PayloadReference
	Receipt   PayloadReference
	Authority PayloadReference
}

type ManifestInput struct {
	Set      successor.RecoverySet3
	Payloads EvidencePayloads
}

type Set3Input struct {
	Set      successor.RecoverySet3
	Payloads EvidencePayloads
}

type ExactVersionReader interface {
	ReadExact(context.Context, ValidatedLocator) (io.ReadCloser, error)
}

type SuccessorOptions struct {
	Reader ExactVersionReader
	Trust  func(context.Context, string) (TrustInput, error)
}

type SuccessorRepository struct {
	db     DBTX
	reader ExactVersionReader
	trust  func(context.Context, string) (TrustInput, error)
}

func NewSuccessorRepository(db DBTX, options SuccessorOptions) *SuccessorRepository {
	return &SuccessorRepository{db: db, reader: options.Reader, trust: options.Trust}
}

func successorQueries(db DBTX) *recoverydb.Queries { return recoverydb.New(db) }

func (r *SuccessorRepository) Configured() bool {
	return r != nil && r.db != nil && r.reader != nil && r.trust != nil
}

func (r *SuccessorRepository) resolveTrust(ctx context.Context, setID string) (TrustInput, error) {
	if !r.Configured() {
		return TrustInput{}, ErrSuccessorInvalid
	}
	input, err := r.trust(ctx, setID)
	if err != nil {
		return TrustInput{}, &successorTrustFailure{cause: err}
	}
	if !canonicalUUID(input.Generation.IncarnationID) || input.Generation.Revision <= 0 || !domainDigest.MatchString(input.Generation.PolicyDigest) || input.WorkerFence <= 0 || input.Evidence.VerificationTime.IsZero() {
		return TrustInput{}, fmt.Errorf("%w: malformed independent trust", ErrSuccessorUntrusted)
	}
	return input, nil
}

// Trust resolver errors may contain provider credentials or other sensitive
// details. Keep the typed cause available to errors.Is/As without including
// an untrusted body in diagnostics.
type successorTrustFailure struct {
	cause error
}

func (e *successorTrustFailure) Error() string {
	return "successor persistence trust resolution failed"
}
func (e *successorTrustFailure) Unwrap() []error { return []error{ErrSuccessorUntrusted, e.cause} }

// InsertManifest persists the complete evidence bundle and its immutable
// binding. It performs all exact off-host reads before opening PostgreSQL.
func (r *SuccessorRepository) InsertManifest(ctx context.Context, input ManifestInput) error {
	if !r.Configured() {
		return ErrSuccessorInvalid
	}
	trust, err := r.resolveTrust(ctx, input.Set.ID)
	if err != nil {
		return err
	}
	checked, err := r.verifyBundle(ctx, input.Set, input.Payloads, trust)
	if err != nil {
		return err
	}
	return r.withTx(ctx, func(tx DBTX) error {
		if err := checkStoredAssignment(ctx, tx, trust); err != nil {
			return err
		}
		return r.insertBundle(ctx, tx, input.Set, checked, trust, true)
	})
}

// ReadManifest resolves the set association by manifest digest, then reads
// every associated exact payload and re-runs signature/trust verification.
func (r *SuccessorRepository) ReadManifest(ctx context.Context, manifestDigest string) (successor.ManagedManifest2, error) {
	if !r.Configured() || !domainDigest.MatchString(manifestDigest) {
		return successor.ManagedManifest2{}, ErrSuccessorInvalid
	}
	binding, err := successorQueries(r.db).GetSuccessorBinding(ctx, manifestDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return successor.ManagedManifest2{}, ErrSuccessorNotFound
	}
	if err != nil {
		return successor.ManagedManifest2{}, err
	}
	setID, setRaw, setLocator, storedManifest := binding.SetID, binding.CanonicalSet, binding.SetLocator, binding.ManifestDigest
	trust, err := r.resolveTrust(ctx, setID)
	if err != nil {
		return successor.ManagedManifest2{}, err
	}
	if err := validateSuccessorVerificationMetadataRead(binding.VerificationMetadata, trust.Evidence.VerificationTime, binding.CaptureCoreRequired); err != nil {
		return successor.ManagedManifest2{}, tampered(err)
	}
	set, err := successor.ParseRecoverySet3(setRaw)
	if err != nil || set.ID != setID {
		return successor.ManagedManifest2{}, tampered(errors.New("stored set bytes"))
	}
	if storedManifest != manifestDigest {
		return successor.ManagedManifest2{}, tampered(errors.New("manifest association digest"))
	}
	if err := checkStoredAssignment(ctx, r.db, trust); err != nil {
		return successor.ManagedManifest2{}, err
	}
	if err := r.verifyStoredSet(ctx, set, setLocator, setRaw); err != nil {
		return successor.ManagedManifest2{}, err
	}
	bundle, err := r.readBundle(ctx, set, manifestDigest, nil)
	if err != nil {
		return successor.ManagedManifest2{}, err
	}
	if err := verifyEvidence(set, bundle, trust); err != nil {
		return successor.ManagedManifest2{}, err
	}
	if err := checkStoredAssignment(ctx, r.db, trust); err != nil {
		return successor.ManagedManifest2{}, err
	}
	return bundle.Manifest, nil
}

// CreateSet3 writes all evidence, roots, and association in one transaction.
// The transaction contains no provider/network work.
func (r *SuccessorRepository) CreateSet3(ctx context.Context, input Set3Input) (successor.RecoverySet3, error) {
	if !r.Configured() {
		return successor.RecoverySet3{}, ErrSuccessorInvalid
	}
	trust, err := r.resolveTrust(ctx, input.Set.ID)
	if err != nil {
		return successor.RecoverySet3{}, err
	}
	checked, err := r.verifyBundle(ctx, input.Set, input.Payloads, trust)
	if err != nil {
		return successor.RecoverySet3{}, err
	}
	setRaw, err := input.Set.CanonicalJSON()
	if err != nil {
		return successor.RecoverySet3{}, err
	}
	if len(checked.Set.CanonicalBytes) == 0 || !bytes.Equal(setRaw, checked.Set.CanonicalBytes) {
		return successor.RecoverySet3{}, tampered(errors.New("prepared set cache differs"))
	}
	if err := r.withTx(ctx, func(tx DBTX) error {
		if err := checkStoredAssignment(ctx, tx, trust); err != nil {
			return err
		}
		if err := r.insertBundle(ctx, tx, input.Set, checked, trust, true); err != nil {
			return err
		}
		return insertSet3(ctx, tx, input.Set, checked, trust, setRaw)
	}); err != nil {
		return successor.RecoverySet3{}, err
	}
	return input.Set, nil
}

func (r *SuccessorRepository) ReadSet3(ctx context.Context, setID string) (successor.RecoverySet3, error) {
	if !r.Configured() || !canonicalUUID(setID) {
		return successor.RecoverySet3{}, ErrSuccessorInvalid
	}
	setRow, err := successorQueries(r.db).GetRecoverySet3(ctx, setID)
	if errors.Is(err, pgx.ErrNoRows) {
		return successor.RecoverySet3{}, ErrSuccessorNotFound
	}
	if err != nil {
		return successor.RecoverySet3{}, err
	}
	setRaw, manifestDigest := setRow.CanonicalBytes, setRow.ManifestDigest
	set, err := successor.ParseRecoverySet3(setRaw)
	if err != nil || set.ID != setID {
		return successor.RecoverySet3{}, tampered(errors.New("stored set bytes"))
	}
	trust, err := r.resolveTrust(ctx, setID)
	if err != nil {
		return successor.RecoverySet3{}, err
	}
	if err := checkStoredAssignment(ctx, r.db, trust); err != nil {
		return successor.RecoverySet3{}, err
	}
	if err := verifyStoredSet3Rows(ctx, r.db, set, setRow, trust); err != nil {
		return successor.RecoverySet3{}, tampered(err)
	}
	binding, err := successorQueries(r.db).GetSuccessorBinding(ctx, manifestDigest)
	if err != nil {
		return successor.RecoverySet3{}, ErrSuccessorTampered
	}
	if binding.SetID != setID || !bytes.Equal(binding.CanonicalSet, setRaw) {
		return successor.RecoverySet3{}, tampered(errors.New("stored association differs from set"))
	}
	if binding.CaptureCoreRequired != setRow.CaptureCoreRequired ||
		(binding.CaptureCoreDigest == nil) != (setRow.CaptureCoreDigest == nil) ||
		(binding.CaptureCoreDigest != nil && *binding.CaptureCoreDigest != *setRow.CaptureCoreDigest) {
		return successor.RecoverySet3{}, tampered(errors.New("capture core association differs from set"))
	}
	if err := validateSuccessorVerificationMetadataRead(binding.VerificationMetadata, trust.Evidence.VerificationTime, binding.CaptureCoreRequired); err != nil {
		return successor.RecoverySet3{}, tampered(err)
	}
	if err := r.verifyStoredSet(ctx, set, binding.SetLocator, setRaw); err != nil {
		return successor.RecoverySet3{}, err
	}
	bundle, err := r.readBundle(ctx, set, manifestDigest, nil)
	if err != nil {
		return successor.RecoverySet3{}, err
	}
	if err := verifyEvidence(set, bundle, trust); err != nil {
		return successor.RecoverySet3{}, err
	}
	if err := checkStoredAssignment(ctx, r.db, trust); err != nil {
		return successor.RecoverySet3{}, err
	}
	return set, nil
}

type readBundle struct {
	Manifest  successor.ManagedManifest2
	Anchor    successor.SourceAnchor
	Profiles  successor.ProviderProfileSet
	Core      *successor.ReceiptCore
	Receipt   successor.SignedReceipt
	Authority successor.AuthorityRegistry
}

func (r *SuccessorRepository) verifyStoredSet(ctx context.Context, set successor.RecoverySet3, rawLocator, cached []byte) error {
	var locator ValidatedLocator
	if err := json.Unmarshal(rawLocator, &locator); err != nil {
		return tampered(err)
	}
	ref := PayloadReference{Locator: locator, CanonicalBytes: cached}
	checked, err := r.verifyPayload(ctx, PayloadFamilySet, ref, set)
	if err != nil {
		return err
	}
	if !bytes.Equal(checked.CanonicalBytes, cached) {
		return tampered(errors.New("stored set cache differs"))
	}
	return nil
}

func (r *SuccessorRepository) verifyBundle(ctx context.Context, set successor.RecoverySet3, p EvidencePayloads, trust TrustInput) (EvidencePayloads, error) {
	if set.Status != recoveryset.StatusPrepared || set.PublishedValidationAttemptID != "" {
		return EvidencePayloads{}, fmt.Errorf("%w: storage accepts prepared evidence only", ErrSuccessorInvalid)
	}
	if err := set.ValidateEvidence(trust.Evidence); err != nil {
		return EvidencePayloads{}, fmt.Errorf("%w: independent evidence: %v", ErrSuccessorUntrusted, err)
	}
	values := []struct {
		family   string
		ref      PayloadReference
		expected any
	}{
		{PayloadFamilyManifest, p.Manifest, trust.Evidence.Manifest},
		{PayloadFamilyAnchor, p.Anchor, trust.Evidence.Anchor},
		{PayloadFamilyProfiles, p.Profiles, trust.Evidence.Profiles},
		{PayloadFamilyCore, p.Core, trust.Evidence.Receipt.Core},
		{PayloadFamilyReceipt, p.Receipt, trust.Evidence.Receipt},
		{PayloadFamilyAuthority, p.Authority, trust.Evidence.Authorities},
	}
	checked := p
	setRef, err := r.verifyPayload(ctx, PayloadFamilySet, p.Set, set)
	if err != nil {
		return EvidencePayloads{}, err
	}
	checked.Set = setRef
	for i, value := range values {
		ref, err := r.verifyPayload(ctx, value.family, value.ref, value.expected)
		if err != nil {
			return EvidencePayloads{}, err
		}
		switch i {
		case 0:
			checked.Manifest = ref
		case 1:
			checked.Anchor = ref
		case 2:
			checked.Profiles = ref
		case 3:
			checked.Core = ref
		case 4:
			checked.Receipt = ref
		case 5:
			checked.Authority = ref
		}
	}
	if len(p.Set.CanonicalBytes) == 0 {
		return EvidencePayloads{}, fmt.Errorf("%w: prepared set canonical bytes are required", ErrSuccessorInvalid)
	}
	if _, err := successor.ParseRecoverySet3(p.Set.CanonicalBytes); err != nil {
		return EvidencePayloads{}, tampered(err)
	}
	if !bytes.Equal(p.Set.CanonicalBytes, mustCanonicalSet(set)) {
		return EvidencePayloads{}, tampered(errors.New("prepared set cache differs"))
	}
	checked.Set.CanonicalBytes = append([]byte(nil), p.Set.CanonicalBytes...)
	return checked, nil
}

func (r *SuccessorRepository) readBundle(ctx context.Context, set successor.RecoverySet3, manifestDigest string, _ any) (readBundle, error) {
	var b readBundle
	refs, err := r.loadEvidenceRefs(ctx, manifestDigest)
	if err != nil {
		return b, err
	}
	manifestRaw, err := r.readReference(ctx, PayloadFamilyManifest, refs.Manifest, nil)
	if err != nil {
		return b, err
	}
	b.Manifest, err = successor.ParseManagedManifest2(manifestRaw)
	if err != nil {
		return b, tampered(err)
	}
	anchorRaw, err := r.readReference(ctx, PayloadFamilyAnchor, refs.Anchor, nil)
	if err != nil {
		return b, err
	}
	b.Anchor, err = successor.ParseSourceAnchor(anchorRaw)
	if err != nil {
		return b, tampered(err)
	}
	profileRaw, err := r.readReference(ctx, PayloadFamilyProfiles, refs.Profiles, nil)
	if err != nil {
		return b, err
	}
	b.Profiles, err = successor.ParseProviderProfileSet(profileRaw)
	if err != nil {
		return b, tampered(err)
	}
	receiptRaw, err := r.readReference(ctx, PayloadFamilyReceipt, refs.Receipt, nil)
	if err != nil {
		return b, err
	}
	b.Receipt, err = successor.ParseReceipt(receiptRaw)
	if err != nil {
		return b, tampered(err)
	}
	if refs.CoreRequired {
		coreRaw, err := r.readReference(ctx, PayloadFamilyCore, refs.Core, nil)
		if err != nil {
			return b, err
		}
		core, err := successor.ParseReceiptCore(coreRaw)
		if err != nil {
			return b, tampered(err)
		}
		coreDigest, err := core.Digest()
		receiptCoreDigest, receiptCoreErr := b.Receipt.Core.Digest()
		if err != nil || receiptCoreErr != nil || coreDigest != refs.Core.Locator.PayloadDigest || coreDigest != receiptCoreDigest {
			return b, tampered(errors.New("capture core relationship"))
		}
		if coreDigest != b.Manifest.Capture.ReceiptDigest {
			return b, tampered(errors.New("capture core manifest relationship"))
		}
		b.Core = &core
	}
	authorityRaw, err := r.readReference(ctx, PayloadFamilyAuthority, refs.Authority, nil)
	if err != nil {
		return b, err
	}
	b.Authority, err = successor.ParseAuthorityRegistry(authorityRaw)
	if err != nil {
		return b, tampered(err)
	}
	if d, _ := b.Manifest.Digest(); d != manifestDigest || b.Manifest.SetID != set.ID {
		return b, tampered(errors.New("manifest association"))
	}
	return b, nil
}

type evidenceRefs struct {
	Manifest, Anchor, Profiles, Core, Receipt, Authority PayloadReference
	CoreRequired                                         bool
}

func (r *SuccessorRepository) loadEvidenceRefs(ctx context.Context, manifestDigest string) (evidenceRefs, error) {
	var refs evidenceRefs
	digests, err := successorQueries(r.db).GetSuccessorEvidenceDigests(ctx, manifestDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return refs, ErrSuccessorTampered
	}
	if err != nil {
		return refs, err
	}
	var err2 error
	refs.Manifest, err2 = r.loadRef(ctx, PayloadFamilyManifest, manifestDigest)
	if err2 != nil {
		return refs, err2
	}
	refs.Anchor, err2 = r.loadRef(ctx, PayloadFamilyAnchor, digests.AnchorDigest)
	if err2 != nil {
		return refs, err2
	}
	refs.Profiles, err2 = r.loadRef(ctx, PayloadFamilyProfiles, digests.ProfileDigest)
	if err2 != nil {
		return refs, err2
	}
	refs.CoreRequired = digests.CaptureCoreRequired
	if refs.CoreRequired && digests.CaptureCoreDigest == nil {
		return refs, ErrSuccessorTampered
	}
	if !refs.CoreRequired && digests.CaptureCoreDigest != nil {
		return refs, ErrSuccessorTampered
	}
	if digests.CaptureCoreDigest != nil {
		refs.Core, err2 = r.loadRef(ctx, PayloadFamilyCore, *digests.CaptureCoreDigest)
		if err2 != nil {
			return refs, err2
		}
	}
	refs.Receipt, err2 = r.loadRef(ctx, PayloadFamilyReceipt, digests.ReceiptDigest)
	if err2 != nil {
		return refs, err2
	}
	refs.Authority, err2 = r.loadRef(ctx, PayloadFamilyAuthority, digests.AuthorityDigest)
	if err2 != nil {
		return refs, err2
	}
	return refs, nil
}

func (r *SuccessorRepository) loadRef(ctx context.Context, family, digest string) (PayloadReference, error) {
	var ref PayloadReference
	storageFamily := dbFamily(family)
	row, err := successorQueries(r.db).GetSuccessorEvidence(ctx, recoverydb.GetSuccessorEvidenceParams{PayloadFamily: storageFamily, PayloadVersion: 2, PayloadDigest: digest})
	if errors.Is(err, pgx.ErrNoRows) {
		return ref, ErrSuccessorTampered
	}
	if err != nil {
		return ref, err
	}
	ref.Locator = ValidatedLocator{
		Backend:                row.Backend,
		StorageProfileID:       row.StorageProfileID,
		StorageProfileRevision: row.StorageProfileRevision,
		AccountIdentity:        row.AccountIdentity,
		Endpoint:               row.Endpoint,
		Region:                 row.Region,
		Bucket:                 row.Bucket,
		Namespace:              row.Namespace,
		Key:                    row.ObjectKey,
		VersionID:              row.VersionID,
		PayloadFamily:          publicFamily(row.PayloadFamily),
		PayloadVersion:         row.PayloadVersion,
		PayloadDigest:          row.PayloadDigest,
		PayloadSHA256:          strings.TrimPrefix(row.RawSha256, "sha256:"),
		PayloadSize:            row.ByteLength,
	}
	ref.CanonicalBytes = row.CanonicalBytes
	return ref, nil
}

func (r *SuccessorRepository) verifyPayload(ctx context.Context, family string, ref PayloadReference, expected any) (PayloadReference, error) {
	if err := ref.Locator.Validate(); err != nil {
		return PayloadReference{}, err
	}
	if dbFamily(family) != dbFamily(ref.Locator.PayloadFamily) || ref.Locator.PayloadVersion != familyVersion(family) {
		return PayloadReference{}, fmt.Errorf("%w: payload family/version", ErrSuccessorInvalid)
	}
	raw, err := r.readPayload(ctx, family, ref, expected)
	if err != nil {
		return PayloadReference{}, err
	}
	if len(ref.CanonicalBytes) > 0 && !bytes.Equal(ref.CanonicalBytes, raw) {
		return PayloadReference{}, tampered(errors.New("caller cache differs from exact bytes"))
	}
	ref.CanonicalBytes = append([]byte(nil), raw...)
	return ref, nil
}

func (r *SuccessorRepository) readPayload(ctx context.Context, family string, ref PayloadReference, expected any) ([]byte, error) {
	if r.reader == nil {
		return nil, ErrSuccessorUntrusted
	}
	if err := ref.Locator.Validate(); err != nil {
		return nil, err
	}
	body, err := r.reader.ReadExact(ctx, ref.Locator)
	if err != nil {
		return nil, &successorTransportFailure{stage: "exact-version read", cause: err}
	}
	if body == nil {
		return nil, tampered(errors.New("nil reader"))
	}
	limit := int64(successor.MaxDocumentBytes)
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	closeErr := body.Close()
	if err != nil {
		return nil, &successorTransportFailure{stage: "exact-version body", cause: err}
	}
	if closeErr != nil {
		return nil, &successorTransportFailure{stage: "exact-version close", cause: closeErr}
	}
	if int64(len(raw)) != ref.Locator.PayloadSize {
		return nil, tampered(errors.New("payload length mismatch"))
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != strings.TrimPrefix(ref.Locator.PayloadSHA256, "sha256:") {
		return nil, tampered(errors.New("raw SHA-256 mismatch"))
	}
	if err := parsePayload(family, raw, expected); err != nil {
		return nil, tampered(err)
	}
	digest, err := payloadDigest(family, raw)
	if err != nil || digest != ref.Locator.PayloadDigest {
		return nil, tampered(errors.New("domain digest mismatch"))
	}
	return raw, nil
}

// Provider errors may contain signed URLs or credentials. Keep the typed cause
// available to errors.Is/As without including an untrusted body in diagnostics.
type successorTransportFailure struct {
	stage string
	cause error
}

func (e *successorTransportFailure) Error() string {
	return "successor evidence " + e.stage + " failed"
}
func (e *successorTransportFailure) Unwrap() []error { return []error{ErrSuccessorTampered, e.cause} }

func (r *SuccessorRepository) readReference(ctx context.Context, family string, ref PayloadReference, expected any) ([]byte, error) {
	raw, err := r.readPayload(ctx, family, ref, expected)
	if err != nil {
		return nil, err
	}
	if len(ref.CanonicalBytes) > 0 && !bytes.Equal(ref.CanonicalBytes, raw) {
		return nil, tampered(errors.New("SQL canonical cache differs from exact bytes"))
	}
	return raw, nil
}

func parsePayload(family string, raw []byte, expected any) error {
	switch family {
	case PayloadFamilyManifest:
		v, err := successor.ParseManagedManifest2(raw)
		if err != nil {
			return err
		}
		if x, ok := expected.(successor.ManagedManifest2); ok {
			a, _ := v.Digest()
			b, _ := x.Digest()
			if a != b {
				return errors.New("manifest identity mismatch")
			}
		}
		return nil
	case PayloadFamilySet:
		v, err := successor.ParseRecoverySet3(raw)
		if err != nil {
			return err
		}
		if x, ok := expected.(successor.RecoverySet3); ok {
			a, _ := v.Digest()
			b, _ := x.Digest()
			if a != b {
				return errors.New("set identity mismatch")
			}
		}
		return nil
	case PayloadFamilyAnchor:
		v, err := successor.ParseSourceAnchor(raw)
		if err != nil {
			return err
		}
		if x, ok := expected.(successor.SourceAnchor); ok {
			a, _ := v.Digest()
			b, _ := x.Digest()
			if a != b {
				return errors.New("anchor identity mismatch")
			}
		}
		return nil
	case PayloadFamilyProfiles:
		v, err := successor.ParseProviderProfileSet(raw)
		if err != nil {
			return err
		}
		if x, ok := expected.(successor.ProviderProfileSet); ok {
			a, _ := v.Digest()
			b, _ := x.Digest()
			if a != b {
				return errors.New("profile identity mismatch")
			}
		}
		return nil
	case PayloadFamilyCore:
		v, err := successor.ParseReceiptCore(raw)
		if err != nil {
			return err
		}
		if x, ok := expected.(successor.ReceiptCore); ok {
			a, _ := v.Digest()
			b, _ := x.Digest()
			if a != b {
				return errors.New("capture core identity mismatch")
			}
		}
		return nil
	case PayloadFamilyReceipt:
		v, err := successor.ParseReceipt(raw)
		if err != nil {
			return err
		}
		if x, ok := expected.(successor.SignedReceipt); ok {
			a, _ := v.Digest()
			b, _ := x.Digest()
			if a != b {
				return errors.New("receipt identity mismatch")
			}
		}
		return nil
	case PayloadFamilyAuthority:
		v, err := successor.ParseAuthorityRegistry(raw)
		if err != nil {
			return err
		}
		if x, ok := expected.(successor.AuthorityRegistry); ok {
			a, _ := v.Digest()
			b, _ := x.Digest()
			if a != b {
				return errors.New("authority identity mismatch")
			}
		}
		return nil
	default:
		return errors.New("unsupported evidence family")
	}
}

func payloadDigest(family string, raw []byte) (string, error) {
	switch family {
	case PayloadFamilyManifest:
		v, err := successor.ParseManagedManifest2(raw)
		if err != nil {
			return "", err
		}
		return v.Digest()
	case PayloadFamilySet:
		v, err := successor.ParseRecoverySet3(raw)
		if err != nil {
			return "", err
		}
		return v.Digest()
	case PayloadFamilyAnchor:
		v, err := successor.ParseSourceAnchor(raw)
		if err != nil {
			return "", err
		}
		return v.Digest()
	case PayloadFamilyProfiles:
		v, err := successor.ParseProviderProfileSet(raw)
		if err != nil {
			return "", err
		}
		return v.Digest()
	case PayloadFamilyCore:
		v, err := successor.ParseReceiptCore(raw)
		if err != nil {
			return "", err
		}
		return v.Digest()
	case PayloadFamilyReceipt:
		v, err := successor.ParseReceipt(raw)
		if err != nil {
			return "", err
		}
		return v.Digest()
	case PayloadFamilyAuthority:
		v, err := successor.ParseAuthorityRegistry(raw)
		if err != nil {
			return "", err
		}
		return v.Digest()
	default:
		return "", errors.New("unsupported evidence family")
	}
}

func insertEvidence(ctx context.Context, db DBTX, ref PayloadReference) error {
	family := dbFamily(ref.Locator.PayloadFamily)
	q := successorQueries(db)
	err := q.InsertSuccessorEvidence(ctx, recoverydb.InsertSuccessorEvidenceParams{
		PayloadFamily:  family,
		PayloadVersion: int16(ref.Locator.PayloadVersion),
		PayloadDigest:  ref.Locator.PayloadDigest,
		CanonicalBytes: ref.CanonicalBytes,
	})
	if err != nil {
		return err
	}
	err = q.InsertSuccessorEvidenceLocator(ctx, recoverydb.InsertSuccessorEvidenceLocatorParams{
		PayloadFamily:          family,
		PayloadVersion:         int16(ref.Locator.PayloadVersion),
		PayloadDigest:          ref.Locator.PayloadDigest,
		Backend:                ref.Locator.Backend,
		StorageProfileID:       ref.Locator.StorageProfileID,
		StorageProfileRevision: ref.Locator.StorageProfileRevision,
		AccountIdentity:        ref.Locator.AccountIdentity,
		Endpoint:               ref.Locator.Endpoint,
		Region:                 ref.Locator.Region,
		Bucket:                 ref.Locator.Bucket,
		Namespace:              ref.Locator.Namespace,
		ObjectKey:              ref.Locator.Key,
		VersionID:              ref.Locator.VersionID,
		ByteLength:             ref.Locator.PayloadSize,
		RawSha256:              rawWithPrefix(ref.Locator.PayloadSHA256),
	})
	if err != nil {
		return err
	}
	stored, err := q.GetSuccessorEvidence(ctx, recoverydb.GetSuccessorEvidenceParams{PayloadFamily: family, PayloadVersion: int16(ref.Locator.PayloadVersion), PayloadDigest: ref.Locator.PayloadDigest})
	if err != nil {
		return err
	}
	storedLocator := ValidatedLocator{
		Backend:                stored.Backend,
		StorageProfileID:       stored.StorageProfileID,
		StorageProfileRevision: stored.StorageProfileRevision,
		AccountIdentity:        stored.AccountIdentity,
		Endpoint:               stored.Endpoint,
		Region:                 stored.Region,
		Bucket:                 stored.Bucket,
		Namespace:              stored.Namespace,
		Key:                    stored.ObjectKey,
		VersionID:              stored.VersionID,
		PayloadFamily:          publicFamily(stored.PayloadFamily),
		PayloadVersion:         stored.PayloadVersion,
		PayloadDigest:          stored.PayloadDigest,
		PayloadSHA256:          strings.TrimPrefix(stored.RawSha256, "sha256:"),
		PayloadSize:            stored.ByteLength,
	}
	if !bytes.Equal(stored.CanonicalBytes, ref.CanonicalBytes) || !sameLocator(storedLocator, ref.Locator) {
		return ErrSuccessorConflict
	}
	return nil
}

func (r *SuccessorRepository) insertBundle(ctx context.Context, db DBTX, set successor.RecoverySet3, p EvidencePayloads, trust TrustInput, bind bool) error {
	refs := []PayloadReference{p.Manifest, p.Anchor, p.Profiles, p.Core, p.Receipt, p.Authority}
	for _, ref := range refs {
		if err := insertEvidence(ctx, db, ref); err != nil {
			return err
		}
	}
	if !bind {
		return nil
	}
	return insertBinding(ctx, db, set, p, trust)
}

func insertBinding(ctx context.Context, db DBTX, set successor.RecoverySet3, p EvidencePayloads, trust TrustInput) error {
	md, _ := trust.Evidence.Manifest.Digest()
	ad, _ := trust.Evidence.Anchor.Digest()
	pd, _ := trust.Evidence.Profiles.Digest()
	cd, _ := trust.Evidence.Receipt.Core.Digest()
	rd, _ := trust.Evidence.Receipt.Digest()
	au, _ := trust.Evidence.Authorities.Digest()
	setRaw, _ := set.CanonicalJSON()
	locator, err := json.Marshal(p.Set.Locator)
	if err != nil {
		return err
	}
	metadata, err := marshalSuccessorVerificationMetadata(trust.Generation, trust.WorkerFence, trust.Evidence.VerificationTime)
	if err != nil {
		return err
	}
	q := successorQueries(db)
	err = q.InsertSuccessorBinding(ctx, recoverydb.InsertSuccessorBindingParams{
		ManifestDigest:       md,
		SetID:                set.ID,
		AnchorDigest:         ad,
		ProfileDigest:        pd,
		CaptureCoreDigest:    &cd,
		ReceiptDigest:        rd,
		AuthorityDigest:      au,
		CanonicalSet:         setRaw,
		SetLocator:           locator,
		VerificationMetadata: metadata,
	})
	if err != nil {
		return err
	}
	old, err := q.GetSuccessorBinding(ctx, md)
	if err != nil {
		return err
	}
	if old.ManifestDigest != md || old.SetID != set.ID || old.AnchorDigest != ad || old.ProfileDigest != pd ||
		!old.CaptureCoreRequired || old.CaptureCoreDigest == nil || *old.CaptureCoreDigest != cd || old.ReceiptDigest != rd || old.AuthorityDigest != au || !bytes.Equal(old.CanonicalSet, setRaw) ||
		!bytes.Equal(old.SetLocator, locator) || !successorVerificationMetadataAssignmentEqual(old.VerificationMetadata, trust.Generation, trust.WorkerFence) {
		return ErrSuccessorConflict
	}
	return nil
}

func insertSet3(ctx context.Context, db DBTX, set successor.RecoverySet3, p EvidencePayloads, trust TrustInput, setRaw []byte) error {
	md, _ := trust.Evidence.Manifest.Digest()
	ad, _ := trust.Evidence.Anchor.Digest()
	pd, _ := trust.Evidence.Profiles.Digest()
	rd, _ := trust.Evidence.Receipt.Digest()
	cd, _ := trust.Evidence.Receipt.Core.Digest()
	au, _ := trust.Evidence.Authorities.Digest()
	frontier, err := frontierProjection(set)
	if err != nil {
		return err
	}
	fd, _ := set.Digest()
	q := successorQueries(db)
	err = q.InsertRecoverySet3(ctx, recoverydb.InsertRecoverySet3Params{
		SetID:              set.ID,
		ManifestDigest:     md,
		AnchorDigest:       ad,
		ProfileDigest:      pd,
		ReceiptDigest:      rd,
		ReceiptCoreDigest:  cd,
		CaptureCoreDigest:  &cd,
		AuthorityDigest:    au,
		FrontierProjection: frontier,
		FrontierDigest:     fd,
		CanonicalBytes:     setRaw,
		CreatedBy:          set.CreatedBy,
	})
	if err != nil {
		return err
	}
	for _, root := range set.ObjectRoots {
		raw, _ := json.Marshal(root)
		err = q.InsertRecoverySet3Root(ctx, recoverydb.InsertRecoverySet3RootParams{
			SetID:                    set.ID,
			RootKind:                 root.Kind,
			RootUri:                  root.URI,
			VersionID:                root.VersionID,
			RootDigest:               root.Digest,
			ProviderRecoveryFrontier: root.ProviderRecoveryFrontier,
			CanonicalBytes:           raw,
		})
		if err != nil {
			return err
		}
	}
	row, err := q.GetRecoverySet3(ctx, set.ID)
	if err != nil {
		return err
	}
	return verifyStoredSet3Rows(ctx, db, set, row, trust)
}

func frontierProjection(set successor.RecoverySet3) ([]byte, error) {
	return set.CommitmentBytes()
}

func verifyStoredSet3Rows(ctx context.Context, db DBTX, set successor.RecoverySet3, row recoverydb.GetRecoverySet3Row, trust TrustInput) error {
	want, err := successorSet3ScalarsForOwner(set, trust, row.CanonicalBytes, row.CaptureCoreRequired)
	if err != nil {
		return tampered(err)
	}
	stored := successorSet3ScalarRow{
		SetID:               row.SetID,
		SchemaVersion:       row.SchemaVersion,
		ManifestFamily:      row.ManifestFamily,
		ManifestVersion:     row.ManifestVersion,
		ManifestDigest:      row.ManifestDigest,
		AnchorFamily:        row.AnchorFamily,
		AnchorVersion:       row.AnchorVersion,
		AnchorDigest:        row.AnchorDigest,
		ProfileFamily:       row.ProfileFamily,
		ProfileVersion:      row.ProfileVersion,
		ProfileDigest:       row.ProfileDigest,
		ReceiptFamily:       row.ReceiptFamily,
		ReceiptVersion:      row.ReceiptVersion,
		ReceiptDigest:       row.ReceiptDigest,
		ReceiptCoreDigest:   row.ReceiptCoreDigest,
		CaptureCoreRequired: row.CaptureCoreRequired,
		AuthorityFamily:     row.AuthorityFamily,
		AuthorityVersion:    row.AuthorityVersion,
		AuthorityDigest:     row.AuthorityDigest,
		FrontierProjection:  row.FrontierProjection,
		FrontierDigest:      row.FrontierDigest,
		CanonicalBytes:      row.CanonicalBytes,
		Status:              row.Status,
		CreatedBy:           row.CreatedBy,
	}
	if row.CaptureCoreDigest != nil {
		stored.CaptureCoreDigest = *row.CaptureCoreDigest
	}
	if !successorSet3ScalarsEqual(stored, want) {
		return ErrSuccessorConflict
	}
	storedRoots, err := successorQueries(db).GetRecoverySet3Roots(ctx, set.ID)
	if err != nil {
		return err
	}
	actualRoots := make([]successorSet3RootRow, 0, len(storedRoots))
	for _, root := range storedRoots {
		actualRoots = append(actualRoots, successorSet3RootRow{
			SetID:                    root.SetID,
			RootKind:                 root.RootKind,
			RootURI:                  root.RootUri,
			VersionID:                root.VersionID,
			RootDigest:               root.RootDigest,
			ProviderRecoveryFrontier: root.ProviderRecoveryFrontier,
			CanonicalBytes:           root.CanonicalBytes,
		})
	}
	wantRoots, err := successorSet3RootRowsForOwner(set)
	if err != nil {
		return tampered(err)
	}
	if !successorSet3RootRowsEqual(actualRoots, wantRoots) {
		return ErrSuccessorConflict
	}
	return nil
}

func checkStoredAssignment(ctx context.Context, db DBTX, want TrustInput) error {
	row, err := successorQueries(db).LockSuccessorAssignment(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSuccessorUntrusted
	}
	if err != nil {
		return err
	}
	if row.IncarnationID != want.Generation.IncarnationID || row.Revision != want.Generation.Revision || row.PolicyDigest != want.Generation.PolicyDigest || row.WorkerFence != want.WorkerFence {
		return ErrSuccessorConflict
	}
	return nil
}

func (r *SuccessorRepository) withTx(ctx context.Context, fn func(DBTX) error) error {
	b, ok := r.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return ErrSuccessorInvalid
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return successorPersistenceError(err)
	}
	return successorPersistenceError(tx.Commit(ctx))
}

func successorPersistenceError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %w", ErrSuccessorConflict, err)
	}
	return err
}

func familyVersion(f string) int32 {
	switch f {
	case PayloadFamilySet:
		return successor.RecoverySetVersion
	case PayloadFamilyManifest, PayloadFamilyAnchor, PayloadFamilyProfiles, PayloadFamilyCore, PayloadFamilyReceipt, PayloadFamilyAuthority:
		return 2
	}
	return 0
}
func dbFamily(f string) string {
	switch f {
	case PayloadFamilyManifest:
		return "manifest"
	case PayloadFamilyAnchor:
		return "anchor"
	case PayloadFamilyProfiles:
		return "profile"
	case PayloadFamilyCore:
		return "core"
	case PayloadFamilyReceipt:
		return "receipt"
	case PayloadFamilyAuthority:
		return "authority"
	default:
		return f
	}
}

func publicFamily(f string) string {
	switch f {
	case "manifest":
		return PayloadFamilyManifest
	case "anchor":
		return PayloadFamilyAnchor
	case "profile":
		return PayloadFamilyProfiles
	case "core":
		return PayloadFamilyCore
	case "receipt":
		return PayloadFamilyReceipt
	case "authority":
		return PayloadFamilyAuthority
	default:
		return f
	}
}
func sameLocator(a, b ValidatedLocator) bool {
	return a.Backend == b.Backend && a.StorageProfileID == b.StorageProfileID && a.StorageProfileRevision == b.StorageProfileRevision && a.AccountIdentity == b.AccountIdentity && a.Endpoint == b.Endpoint && a.Region == b.Region && a.Bucket == b.Bucket && a.Namespace == b.Namespace && a.Key == b.Key && a.VersionID == b.VersionID && dbFamily(a.PayloadFamily) == dbFamily(b.PayloadFamily) && a.PayloadVersion == b.PayloadVersion && a.PayloadDigest == b.PayloadDigest && strings.TrimPrefix(a.PayloadSHA256, "sha256:") == strings.TrimPrefix(b.PayloadSHA256, "sha256:") && a.PayloadSize == b.PayloadSize
}
func rawWithPrefix(s string) string { return strings.TrimPrefix(s, "sha256:") }
func verifyEvidence(set successor.RecoverySet3, b readBundle, trust TrustInput) error {
	trusted := trust.Evidence
	for _, pair := range [][2]string{
		{mustDigestManifest(b.Manifest), mustDigestManifest(trusted.Manifest)},
		{mustDigestAnchor(b.Anchor), mustDigestAnchor(trusted.Anchor)},
		{mustDigestProfiles(b.Profiles), mustDigestProfiles(trusted.Profiles)},
		{mustDigestReceipt(b.Receipt), mustDigestReceipt(trusted.Receipt)},
		{mustDigestAuthority(b.Authority), mustDigestAuthority(trusted.Authorities)},
	} {
		if pair[0] == "" || pair[0] != pair[1] {
			return fmt.Errorf("%w: persisted evidence differs from independent trust", ErrSuccessorUntrusted)
		}
	}
	if b.Core != nil {
		persistedCoreDigest, persistedCoreErr := b.Core.Digest()
		trustedCoreDigest, trustedCoreErr := trusted.Receipt.Core.Digest()
		if persistedCoreErr != nil || trustedCoreErr != nil || persistedCoreDigest != trustedCoreDigest || persistedCoreDigest != trusted.Manifest.Capture.ReceiptDigest {
			return fmt.Errorf("%w: persisted capture core differs from independent trust", ErrSuccessorUntrusted)
		}
	}
	if err := set.ValidateEvidence(trusted); err != nil {
		return fmt.Errorf("%w: %v", ErrSuccessorUntrusted, err)
	}
	return nil
}

func mustDigestManifest(v successor.ManagedManifest2) string   { d, _ := v.Digest(); return d }
func mustDigestAnchor(v successor.SourceAnchor) string         { d, _ := v.Digest(); return d }
func mustDigestProfiles(v successor.ProviderProfileSet) string { d, _ := v.Digest(); return d }
func mustDigestCore(v successor.ReceiptCore) string            { d, _ := v.Digest(); return d }
func mustDigestReceipt(v successor.SignedReceipt) string       { d, _ := v.Digest(); return d }
func mustDigestAuthority(v successor.AuthorityRegistry) string { d, _ := v.Digest(); return d }
func mustCanonicalSet(v successor.RecoverySet3) []byte         { b, _ := v.CanonicalJSON(); return b }
func tampered(err error) error                                 { return fmt.Errorf("%w: %v", ErrSuccessorTampered, err) }
func canonicalUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
