// Package capture assembles successor recovery evidence from independently
// owned managed-data and trust inputs. It has no admission, publication,
// startup, restore, or key-persistence behavior.
package capture

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/recoveryset/successor"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalid    = errors.New("successor capture input is invalid")
	ErrIncomplete = errors.New("successor capture evidence is incomplete")
	ErrConflict   = errors.New("successor capture evidence conflicts")
	ErrSigning    = errors.New("successor capture signing failed")
)

const canonicalTimeLayout = "2006-01-02T15:04:05.000000Z"

// ObservationReader resolves only immutable facts already captured from a
// successful provider write. It must never synthesize a version from latest.
type ObservationReader interface {
	ProviderVersionObservation(context.Context, string, string) (manageddata.ProviderVersionObservation, error)
}

// ObservationVerifier replays the exact provider version and verifies its
// bytes, size, and digest through trusted provider configuration. Its errors
// must not contain credentials or provider response bodies.
type ObservationVerifier interface {
	VerifyExact(context.Context, manageddata.ProviderVersionObservation) error
}

type ObservationVerifierFunc func(context.Context, manageddata.ProviderVersionObservation) error

func (f ObservationVerifierFunc) VerifyExact(ctx context.Context, observation manageddata.ProviderVersionObservation) error {
	return f(ctx, observation)
}

// Clock is the trusted clock selected by the owner of the capture worker. It
// is deliberately not part of Request: a worker cannot choose the evidence's
// verification time.
type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

// TrustedClock is an explicit name for the same injected clock boundary.
type TrustedClock = Clock
type TrustedClockFunc = ClockFunc

// TrustGeneration identifies the authoritative policy snapshot. A capture
// worker may present an assignment, but it cannot make that assignment
// authoritative; Resolve must return the same tuple.
type TrustGeneration struct {
	IncarnationID string `json:"incarnation_id"`
	Revision      int64  `json:"revision"`
	PolicyDigest  string `json:"policy_digest"`
}

// TrustAssignment is the immutable worker lease context used for one capture.
// WorkerFence is an owner-issued fence epoch, not a value that the worker can
// advance locally.
type TrustAssignment struct {
	Generation  TrustGeneration
	WorkerFence int64
	Deadline    time.Time
}

// SigningRequest is the only input provided to a signer. Payload and all
// nested slices are copied before the call; implementations never receive a
// pointer into mutable capture state or any private key material.
type SigningRequest struct {
	Payload     []byte
	AuthorityID string
	KeyID       string
	Authority   successor.AuthorityKey
	Generation  TrustGeneration
	WorkerFence int64
	Deadline    time.Time
}

// Signer delegates receipt signing to an authority boundary. Implementations
// receive the frozen domain-separated receipt payload and its trust context;
// private keys never enter a CaptureResult or any recovery evidence document.
type Signer interface {
	Sign(context.Context, SigningRequest) ([]byte, error)
}

type SignerFunc func(context.Context, SigningRequest) ([]byte, error)

func (f SignerFunc) Sign(ctx context.Context, request SigningRequest) ([]byte, error) {
	return f(ctx, request)
}

// ProfileBinding selects the durable managed-data profile that corresponds to
// one independently trusted successor namespace. The profile ID is lookup
// metadata only and is not added to the frozen Manifest v2 wire format.
type ProfileBinding struct {
	ProjectID            string
	CollectionID         string
	Prefix               string
	ObservationProfileID string
}

// TrustPolicy is returned by an independently owned policy/scope boundary.
// None of these fields are accepted from Request. The service freezes a deep
// copy before it reads observations or emits evidence.
type TrustPolicy struct {
	Profiles        successor.ProviderProfileSet
	Authorities     successor.AuthorityRegistry
	ProfileBindings []ProfileBinding
	ExpectedScope   successor.ExpectedScope
	Assignment      TrustAssignment
}

// PolicyScopeResolver is the independent authority for provider profiles,
// authority keys, expected membership scope, and the current worker fence.
type PolicyScopeResolver interface {
	Resolve(context.Context, Request) (TrustPolicy, error)
}

type PolicyScopeResolverFunc func(context.Context, Request) (TrustPolicy, error)

func (f PolicyScopeResolverFunc) Resolve(ctx context.Context, request Request) (TrustPolicy, error) {
	return f(ctx, request)
}

type Request struct {
	Base        recoveryset.RecoverySet
	CaptureID   string
	AuthorityID string
	KeyID       string
	StartedAt   time.Time
	CompletedAt time.Time
	Assignment  TrustAssignment
}

// Documents are the exact canonical payloads intended for the existing
// exact-version evidence upload and RecoverySet v3 persistence path.
type Documents struct {
	Set         []byte
	Manifest    []byte
	Anchor      []byte
	Profiles    []byte
	Receipt     []byte
	Authorities []byte
}

type Result struct {
	Set       successor.RecoverySet3
	Evidence  successor.Evidence
	Documents Documents
}

type Service struct {
	source       manageddata.ProjectionCaptureSource
	observations ObservationReader
	verifier     ObservationVerifier
	signer       Signer
	resolver     PolicyScopeResolver
	clock        Clock
}

func New(source manageddata.ProjectionCaptureSource, observations ObservationReader, verifier ObservationVerifier, signer Signer, resolver PolicyScopeResolver, clock Clock) (*Service, error) {
	if source == nil || observations == nil || verifier == nil || signer == nil || resolver == nil || clock == nil {
		return nil, fmt.Errorf("%w: source, observation reader, exact verifier, signer, trust resolver, and clock are required", ErrInvalid)
	}
	return &Service{source: source, observations: observations, verifier: verifier, signer: signer, resolver: resolver, clock: clock}, nil
}

// Capture constructs and verifies a complete successor evidence graph. The
// caller remains responsible for uploading these canonical documents and for
// invoking the existing RecoverySet v3 persistence path with exact locators.
func (s *Service) Capture(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	policy, err := s.resolve(ctx, request)
	if err != nil {
		return Result{}, err
	}
	verificationTime, err := s.trustedNow(policy.Assignment.Deadline)
	if err != nil {
		return Result{}, err
	}
	if verificationTime.Before(request.CompletedAt) {
		return Result{}, fmt.Errorf("%w: trusted verification clock predates capture completion", ErrConflict)
	}
	captureCtx, cancel := context.WithDeadline(ctx, policy.Assignment.Deadline)
	defer cancel()
	projection, err := s.source.CaptureManagedProjection(captureCtx)
	if err != nil {
		return Result{}, &sourceFailure{cause: err}
	}
	revisions, err := s.joinObservations(captureCtx, projection, request, policy)
	if err != nil {
		return Result{}, err
	}
	return s.build(captureCtx, request, projection, revisions, policy, verificationTime)
}

func validateRequest(request Request) error {
	if request.Base.SchemaVersion != recoveryset.SchemaVersion {
		return fmt.Errorf("%w: base recovery set must use the existing v1 owner contract", ErrInvalid)
	}
	if request.Base.ManagedEvidence != nil {
		return fmt.Errorf("%w: legacy managed evidence cannot be reinterpreted as successor evidence", ErrInvalid)
	}
	if request.Base.Status != recoveryset.StatusPrepared || request.Base.PublishedValidationAttemptID != "" {
		return fmt.Errorf("%w: base recovery set must be prepared and unpublished", ErrInvalid)
	}
	if _, err := request.Base.Normalize(); err != nil {
		return fmt.Errorf("%w: base recovery set: %v", ErrInvalid, err)
	}
	if !canonicalID(request.CaptureID) || !canonicalID(request.AuthorityID) || !canonicalID(request.KeyID) {
		return fmt.Errorf("%w: capture and authority identities are required", ErrInvalid)
	}
	if !canonicalCaptureTime(request.StartedAt) || !canonicalCaptureTime(request.CompletedAt) || request.CompletedAt.Before(request.StartedAt) {
		return fmt.Errorf("%w: capture chronology must use nonzero UTC microsecond timestamps", ErrInvalid)
	}
	if err := validateAssignment(request.Assignment); err != nil {
		return err
	}
	if !request.CompletedAt.Before(request.Assignment.Deadline) {
		return fmt.Errorf("%w: capture completion must precede assignment deadline", ErrConflict)
	}
	return nil
}

func canonicalCaptureTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond()%1_000 == 0
}

func canonicalID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > successor.MaxTextBytes || !utf8.ValidString(value) || norm.NFC.String(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}

func canonicalPrefix(value string) bool {
	if value == "" {
		return true
	}
	if !canonicalID(value) || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func canonicalUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func canonicalDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validateGeneration(generation TrustGeneration) error {
	if !canonicalUUID(generation.IncarnationID) || generation.Revision <= 0 || !canonicalDigest(generation.PolicyDigest) {
		return fmt.Errorf("%w: trust generation is malformed", ErrInvalid)
	}
	return nil
}

func validateAssignment(assignment TrustAssignment) error {
	if err := validateGeneration(assignment.Generation); err != nil {
		return err
	}
	if assignment.WorkerFence <= 0 {
		return fmt.Errorf("%w: worker fence must be positive", ErrInvalid)
	}
	if !canonicalCaptureTime(assignment.Deadline) {
		return fmt.Errorf("%w: assignment deadline must use a UTC microsecond timestamp", ErrInvalid)
	}
	return nil
}

func (s *Service) resolve(ctx context.Context, request Request) (TrustPolicy, error) {
	policy, err := s.resolver.Resolve(ctx, request)
	if err != nil {
		return TrustPolicy{}, &policyResolutionFailure{cause: err}
	}
	frozen, err := freezePolicy(policy)
	if err != nil {
		return TrustPolicy{}, err
	}
	if !sameAssignment(frozen.Assignment, request.Assignment) {
		return TrustPolicy{}, fmt.Errorf("%w: request assignment is stale", ErrConflict)
	}
	if frozen.ExpectedScope.SetID != request.Base.ID {
		return TrustPolicy{}, fmt.Errorf("%w: expected scope is assigned to a different recovery set", ErrConflict)
	}
	return frozen, nil
}

func (s *Service) trustedNow(deadline time.Time) (time.Time, error) {
	// Persist only the canonical UTC microsecond representation. Trusted
	// clocks are ordinary time sources and may return local time or carry
	// sub-microsecond precision; normalizing here keeps the persistence
	// boundary independent of the clock implementation.
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	if !canonicalCaptureTime(now) {
		return time.Time{}, fmt.Errorf("%w: trusted verification clock must use a UTC microsecond timestamp", ErrInvalid)
	}
	if !now.Before(deadline) {
		return time.Time{}, fmt.Errorf("%w: capture assignment deadline has expired", ErrConflict)
	}
	return now, nil
}

func freezePolicy(policy TrustPolicy) (TrustPolicy, error) {
	if err := policy.Profiles.Validate(); err != nil {
		return TrustPolicy{}, fmt.Errorf("%w: provider profiles are not independently trusted", ErrInvalid)
	}
	if err := policy.Authorities.Validate(); err != nil {
		return TrustPolicy{}, fmt.Errorf("%w: authority registry is not independently trusted", ErrInvalid)
	}
	if err := validateAssignment(policy.Assignment); err != nil {
		return TrustPolicy{}, err
	}
	if err := validateScope(policy.ExpectedScope); err != nil {
		return TrustPolicy{}, err
	}
	bindings := make([]ProfileBinding, len(policy.ProfileBindings))
	copy(bindings, policy.ProfileBindings)
	sort.Slice(bindings, func(i, j int) bool {
		left, right := bindings[i], bindings[j]
		if left.ProjectID != right.ProjectID {
			return left.ProjectID < right.ProjectID
		}
		if left.CollectionID != right.CollectionID {
			return left.CollectionID < right.CollectionID
		}
		if left.Prefix != right.Prefix {
			return left.Prefix < right.Prefix
		}
		return left.ObservationProfileID < right.ObservationProfileID
	})
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if !canonicalID(binding.ProjectID) || !canonicalID(binding.CollectionID) || !canonicalID(binding.ObservationProfileID) || !canonicalPrefix(binding.Prefix) || strings.HasSuffix(binding.Prefix, "/") {
			return TrustPolicy{}, fmt.Errorf("%w: provider profile binding is malformed", ErrInvalid)
		}
		key := binding.ProjectID + "\x00" + binding.CollectionID + "\x00" + binding.Prefix
		if _, duplicate := seen[key]; duplicate {
			return TrustPolicy{}, fmt.Errorf("%w: duplicate provider profile binding", ErrConflict)
		}
		seen[key] = struct{}{}
	}
	profiles, err := policy.Profiles.Normalize()
	if err != nil {
		return TrustPolicy{}, fmt.Errorf("%w: provider profiles: %v", ErrInvalid, err)
	}
	authorities, err := policy.Authorities.Normalize()
	if err != nil {
		return TrustPolicy{}, fmt.Errorf("%w: authority registry: %v", ErrInvalid, err)
	}
	scope := cloneScope(policy.ExpectedScope)
	return TrustPolicy{Profiles: profiles, Authorities: authorities, ProfileBindings: bindings, ExpectedScope: scope, Assignment: policy.Assignment}, nil
}

func validateScope(scope successor.ExpectedScope) error {
	// The policy boundary selects the recovery-set identity and complete
	// provider-free managed membership before PostgreSQL capture. It cannot
	// pre-authorize the source-anchor digest because that digest includes the
	// restore point and WAL position created by the capture transaction. The
	// service binds that value after capture and before signing.
	if !canonicalUUID(scope.SetID) || scope.SourceFrontierAnchorDigest != "" || !canonicalDigest(scope.ManagedClosureDigest) || scope.Revisions == nil {
		return fmt.Errorf("%w: expected scope is malformed or incomplete", ErrInvalid)
	}
	closure, err := successor.ClosureDigest(scope.Revisions)
	if err != nil || closure != scope.ManagedClosureDigest {
		return fmt.Errorf("%w: expected scope closure is not canonical", ErrConflict)
	}
	for _, revision := range scope.Revisions {
		for _, file := range revision.Files {
			if file.Provider != (successor.ProviderIdentity{}) {
				return fmt.Errorf("%w: expected scope must be provider-free", ErrInvalid)
			}
		}
	}
	return nil
}

func cloneScope(scope successor.ExpectedScope) successor.ExpectedScope {
	copy := scope
	copy.Revisions = make([]successor.Revision, len(scope.Revisions))
	for i, revision := range scope.Revisions {
		copy.Revisions[i] = successor.Revision{ProjectID: revision.ProjectID, CollectionID: revision.CollectionID, RevisionID: revision.RevisionID, RevisionManifestDigest: revision.RevisionManifestDigest, Files: make([]successor.File, len(revision.Files))}
		for j, file := range revision.Files {
			copy.Revisions[i].Files[j] = successor.File{Path: file.Path, SHA256: file.SHA256, Size: file.Size}
		}
		sort.Slice(copy.Revisions[i].Files, func(a, b int) bool { return copy.Revisions[i].Files[a].Path < copy.Revisions[i].Files[b].Path })
	}
	sort.Slice(copy.Revisions, func(i, j int) bool {
		left, right := copy.Revisions[i], copy.Revisions[j]
		if left.ProjectID != right.ProjectID {
			return left.ProjectID < right.ProjectID
		}
		if left.CollectionID != right.CollectionID {
			return left.CollectionID < right.CollectionID
		}
		return left.RevisionID < right.RevisionID
	})
	return copy
}

func samePolicy(left, right TrustPolicy) bool {
	return reflect.DeepEqual(left.Profiles, right.Profiles) && reflect.DeepEqual(left.Authorities, right.Authorities) && reflect.DeepEqual(left.ProfileBindings, right.ProfileBindings) && reflect.DeepEqual(left.ExpectedScope, right.ExpectedScope) && sameAssignment(left.Assignment, right.Assignment)
}

func sameAssignment(left, right TrustAssignment) bool {
	return left.Generation == right.Generation && left.WorkerFence == right.WorkerFence && left.Deadline.Equal(right.Deadline)
}

func (s *Service) joinObservations(ctx context.Context, projection manageddata.CapturedProjection, request Request, policy TrustPolicy) ([]successor.Revision, error) {
	revisions := make([]successor.Revision, 0, len(projection.Revisions))
	for _, captured := range projection.Revisions {
		revision := successor.Revision{
			ProjectID: captured.ProjectID, CollectionID: captured.CollectionID,
			RevisionID: captured.RevisionID, RevisionManifestDigest: captured.ManifestDigest,
			Files: make([]successor.File, 0, len(captured.Files)),
		}
		for _, file := range captured.Files {
			profile, prefix, profileID, objectKey, err := selectProfile(policy, revision.ProjectID, revision.CollectionID, file.StorageKey)
			if err != nil {
				return nil, err
			}
			observed, err := s.observations.ProviderVersionObservation(ctx, profileID, objectKey)
			if err != nil {
				return nil, &observationFailure{cause: err}
			}
			if err := validateObservation(observed, profileID, profile, prefix, objectKey, file, request.StartedAt); err != nil {
				return nil, fmt.Errorf("revision %q path %q: %w", revision.RevisionID, file.Path, err)
			}
			if err := s.verifier.VerifyExact(ctx, observed); err != nil {
				return nil, &verificationFailure{cause: err}
			}
			revision.Files = append(revision.Files, successor.File{
				Path: file.Path, SHA256: file.SHA256, Size: file.Size,
				Provider: successor.ProviderIdentity{
					Implementation: observed.Profile.Implementation, AccountIdentity: observed.Profile.AccountIdentity,
					Endpoint: observed.Profile.Endpoint, Region: observed.Profile.Region, Bucket: observed.Profile.Bucket,
					Prefix: prefix, Key: observed.ObjectKey, VersionID: observed.VersionID,
				},
			})
		}
		revisions = append(revisions, revision)
	}
	return revisions, nil
}

func selectProfile(policy TrustPolicy, projectID, collectionID, storageKey string) (successor.ProviderProfile, string, string, string, error) {
	u, err := url.Parse(storageKey)
	if err != nil || u.Scheme != "s3" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/") {
		return successor.ProviderProfile{}, "", "", "", fmt.Errorf("%w: managed storage key is not a canonical S3 URI", ErrConflict)
	}
	objectKey := strings.TrimPrefix(u.Path, "/")
	if objectKey == "" || (&url.URL{Scheme: "s3", Host: u.Host, Path: "/" + objectKey}).String() != storageKey {
		return successor.ProviderProfile{}, "", "", "", fmt.Errorf("%w: managed storage key is ambiguous", ErrConflict)
	}
	var selected successor.ProviderProfile
	var selectedPrefix, profileID string
	matches := 0
	for _, binding := range policy.ProfileBindings {
		if binding.ProjectID != projectID || binding.CollectionID != collectionID {
			continue
		}
		for _, profile := range policy.Profiles.Profiles {
			if profile.Bucket != u.Host {
				continue
			}
			for _, namespace := range profile.Namespaces {
				if namespace.ProjectID == projectID && namespace.CollectionID == collectionID && namespace.Prefix == binding.Prefix && (namespace.Prefix == "" || strings.HasPrefix(objectKey, namespace.Prefix+"/")) {
					selected, selectedPrefix, profileID = profile, namespace.Prefix, binding.ObservationProfileID
					matches++
				}
			}
		}
	}
	if matches != 1 {
		return successor.ProviderProfile{}, "", "", "", fmt.Errorf("%w: managed object has %d trusted provider profile matches", ErrConflict, matches)
	}
	return selected, selectedPrefix, profileID, objectKey, nil
}

func validateObservation(observed manageddata.ProviderVersionObservation, profileID string, profile successor.ProviderProfile, prefix, objectKey string, file manageddata.CapturedProjectionFile, startedAt time.Time) error {
	if err := manageddata.ValidateProviderVersionObservation(observed); err != nil {
		return fmt.Errorf("%w: stored provider observation is invalid", ErrConflict)
	}
	if observed.Profile.ProfileID != profileID || observed.Profile.Implementation != profile.Implementation || observed.Profile.AccountIdentity != profile.AccountIdentity || observed.Profile.Endpoint != profile.Endpoint || observed.Profile.Region != profile.Region || observed.Profile.Bucket != profile.Bucket || observed.Profile.Namespace != prefix {
		return fmt.Errorf("%w: provider profile identity differs from trusted configuration", ErrConflict)
	}
	if observed.ObjectKey != objectKey || observed.SHA256 != file.SHA256 || observed.Size != file.Size {
		return fmt.Errorf("%w: provider observation does not match managed closure", ErrConflict)
	}
	if observed.CapturedAt.After(startedAt) {
		return fmt.Errorf("%w: provider observation is newer than the selected capture boundary", ErrConflict)
	}
	return nil
}

func (s *Service) build(ctx context.Context, request Request, projection manageddata.CapturedProjection, revisions []successor.Revision, policy TrustPolicy, verificationTime time.Time) (Result, error) {
	base := request.Base
	control := recoveryset.ClusterRecoveryPoint{
		DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "postgres:" + projection.SystemIdentity,
		DatabaseIdentity: projection.DatabaseIdentity,
		RecoveryIdentity: fmt.Sprintf("postgres:%s:%d:%s:%s", projection.SystemIdentity, projection.Timeline, projection.LSN, projection.RestorePointName),
	}
	points := make([]recoveryset.ClusterRecoveryPoint, 0, len(base.ClusterPoints))
	for _, point := range base.ClusterPoints {
		if point.DatabaseRole == recoveryset.DatabaseControl {
			continue
		}
		points = append(points, point)
	}
	points = append(points, control)
	base.ClusterPoints = points
	base.SchemaVersion = recoveryset.SchemaVersion
	base.FrontierDigest = ""
	normalizedBase, err := base.Normalize()
	if err != nil {
		return Result{}, fmt.Errorf("%w: base recovery frontier: %v", ErrInvalid, err)
	}
	closureDigest, err := successor.ClosureDigest(revisions)
	if err != nil {
		return Result{}, fmt.Errorf("%w: managed closure: %v", ErrConflict, err)
	}
	profileDigest, err := policy.Profiles.Digest()
	if err != nil {
		return Result{}, fmt.Errorf("%w: provider profiles: %v", ErrInvalid, err)
	}
	roots := make([]successor.ObjectRoot, len(normalizedBase.ObjectRoots))
	for i, root := range normalizedBase.ObjectRoots {
		roots[i] = successor.ObjectRoot{Kind: root.Kind, URI: root.URI, VersionID: root.VersionID, Digest: root.Digest, ProviderRecoveryFrontier: root.ProviderRecoveryFrontier}
	}
	anchor := successor.SourceAnchor{
		AnchorVersion: successor.SourceAnchorVersion, SetID: normalizedBase.ID, ClusterPoints: normalizedBase.ClusterPoints,
		Delivery: normalizedBase.Delivery, Serving: normalizedBase.Serving, Catalog: normalizedBase.Catalog,
		ObjectRoots: roots, Compatibility: normalizedBase.Compatibility,
		ManagedClosureDigest: closureDigest, ProviderProfileDigest: profileDigest,
	}
	anchorDigest, err := anchor.Digest()
	if err != nil {
		return Result{}, fmt.Errorf("%w: source anchor: %v", ErrInvalid, err)
	}
	started, completed := request.StartedAt.Format(canonicalTimeLayout), request.CompletedAt.Format(canonicalTimeLayout)
	manifest := successor.ManagedManifest2{
		ManifestVersion: successor.ManagedManifestVersion, SetID: normalizedBase.ID,
		SourceFrontierAnchorDigest: anchorDigest, ManagedClosureDigest: closureDigest, Revisions: revisions,
		Capture: successor.Capture{AuthorityID: request.AuthorityID, CaptureID: request.CaptureID, StartedAt: started, CompletedAt: completed, ReceiptDigest: "sha256:" + strings.Repeat("0", 64)},
	}
	projectionDigest, err := manifest.ObservationProjectionDigest()
	if err != nil {
		return Result{}, fmt.Errorf("%w: observation projection: %v", ErrConflict, err)
	}
	memberships, objects, err := manifest.ObservationCounts()
	if err != nil {
		return Result{}, fmt.Errorf("%w: observation counts: %v", ErrConflict, err)
	}
	core := successor.ReceiptCore{
		CoreVersion: successor.ReceiptCoreVersion, AuthorityID: request.AuthorityID, KeyID: request.KeyID,
		CaptureID: request.CaptureID, SetID: normalizedBase.ID, StartedAt: started, CompletedAt: completed,
		SourceFrontierAnchorDigest: anchorDigest, ManagedClosureDigest: closureDigest, ProviderProfileDigest: profileDigest,
		ObservationProjectionDigest: projectionDigest, MembershipCount: memberships, ObjectCount: objects, Result: "verified",
	}
	manifest.Capture.ReceiptDigest, err = core.Digest()
	if err != nil {
		return Result{}, fmt.Errorf("%w: receipt core: %v", ErrInvalid, err)
	}
	manifestDigest, err := manifest.Digest()
	if err != nil {
		return Result{}, fmt.Errorf("%w: manifest: %v", ErrConflict, err)
	}
	// Check the complete provider-free scope before invoking the signer. The
	// independently resolved revisions, closure, set, and source anchor must
	// already agree with this frozen manifest; signing cannot authorize a
	// divergent capture.
	expectedScope := cloneScope(policy.ExpectedScope)
	expectedScope.SourceFrontierAnchorDigest = anchorDigest
	if err := policy.Profiles.ValidateManifest(manifest, expectedScope); err != nil {
		return Result{}, fmt.Errorf("%w: independent expected scope differs from captured manifest", ErrConflict)
	}
	receipt := successor.SignedReceipt{ReceiptVersion: successor.ReceiptVersion, Core: core, ManifestDigest: manifestDigest}
	key, err := validateSigningAuthority(policy, request, profileDigest)
	if err != nil {
		return Result{}, err
	}
	signedPayload, err := receipt.SignedBytes()
	if err != nil {
		return Result{}, fmt.Errorf("%w: receipt payload: %v", ErrInvalid, err)
	}
	// Re-resolve at the last possible point before signing. The first frozen
	// policy remains the source of evidence; the second snapshot only confirms
	// that its authority, scope, generation, fence, and deadline are unchanged.
	latest, err := s.resolve(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if !samePolicy(policy, latest) {
		return Result{}, fmt.Errorf("%w: trust policy changed before signing", ErrConflict)
	}
	verificationTime, err = s.trustedNow(latest.Assignment.Deadline)
	if err != nil {
		return Result{}, err
	}
	if verificationTime.Before(request.CompletedAt) {
		return Result{}, fmt.Errorf("%w: trusted verification clock predates capture completion", ErrConflict)
	}
	signingRequest := SigningRequest{
		Payload: append([]byte(nil), signedPayload...), AuthorityID: request.AuthorityID, KeyID: request.KeyID,
		Authority: cloneAuthorityKey(key), Generation: latest.Assignment.Generation,
		WorkerFence: latest.Assignment.WorkerFence, Deadline: latest.Assignment.Deadline,
	}
	signature, err := s.signer.Sign(ctx, signingRequest)
	if err != nil {
		return Result{}, &signingFailure{cause: err}
	}
	if len(signature) != ed25519.SignatureSize {
		return Result{}, fmt.Errorf("%w: signer returned invalid Ed25519 signature length", ErrSigning)
	}
	receipt.Signature = base64.StdEncoding.EncodeToString(signature)
	set := successor.RecoverySet3{
		ID: normalizedBase.ID, SchemaVersion: successor.RecoverySetVersion, ClusterPoints: normalizedBase.ClusterPoints,
		Delivery: normalizedBase.Delivery, Serving: normalizedBase.Serving, Catalog: normalizedBase.Catalog,
		ObjectRoots: roots, Compatibility: normalizedBase.Compatibility,
		SourceFrontierAnchorDigest: anchorDigest, ManagedObservationManifestVersion: successor.ManagedManifestVersion,
		ManagedObservationManifestDigest: manifestDigest, FenceEpoch: normalizedBase.FenceEpoch,
		AuditIdentity: normalizedBase.AuditIdentity, Status: normalizedBase.Status,
		CreatedBy: normalizedBase.CreatedBy, CreatedAt: normalizedBase.CreatedAt.Format(canonicalTimeLayout),
	}
	set.FrontierDigest, err = set.Commitment()
	if err != nil {
		return Result{}, fmt.Errorf("%w: recovery frontier commitment: %v", ErrInvalid, err)
	}
	evidence := successor.Evidence{
		Anchor: anchor, Manifest: manifest, Receipt: receipt, Profiles: policy.Profiles, Authorities: policy.Authorities,
		ExpectedScope:    expectedScope,
		VerificationTime: verificationTime,
	}
	if err := set.ValidateEvidence(evidence); err != nil {
		return Result{}, fmt.Errorf("%w: complete evidence graph: %v", ErrInvalid, err)
	}
	documents, err := canonicalDocuments(set, evidence)
	if err != nil {
		return Result{}, err
	}
	return Result{Set: set, Evidence: evidence, Documents: documents}, nil
}

func validateSigningAuthority(policy TrustPolicy, request Request, profileDigest string) (successor.AuthorityKey, error) {
	key, err := policy.Authorities.Lookup(request.AuthorityID, request.KeyID)
	if err != nil {
		return successor.AuthorityKey{}, fmt.Errorf("%w: signing authority is unavailable", ErrInvalid)
	}
	if err := key.Validate(); err != nil {
		return successor.AuthorityKey{}, fmt.Errorf("%w: signing authority is invalid", ErrInvalid)
	}
	if key.Revoked {
		return successor.AuthorityKey{}, fmt.Errorf("%w: signing authority is revoked", ErrInvalid)
	}
	notBefore, errBefore := time.Parse(canonicalTimeLayout, key.NotBefore)
	notAfter, errAfter := time.Parse(canonicalTimeLayout, key.NotAfter)
	if errBefore != nil || errAfter != nil || request.StartedAt.Before(notBefore) || request.CompletedAt.Before(notBefore) || !request.StartedAt.Before(notAfter) || !request.CompletedAt.Before(notAfter) {
		return successor.AuthorityKey{}, fmt.Errorf("%w: capture is outside signing authority validity", ErrInvalid)
	}
	for _, allowed := range key.ProviderProfileDigests {
		if allowed == profileDigest {
			return cloneAuthorityKey(key), nil
		}
	}
	return successor.AuthorityKey{}, fmt.Errorf("%w: signing authority does not allow the selected provider profile", ErrInvalid)
}

func cloneAuthorityKey(key successor.AuthorityKey) successor.AuthorityKey {
	key.ProviderProfileDigests = append([]string(nil), key.ProviderProfileDigests...)
	return key
}

func canonicalDocuments(set successor.RecoverySet3, evidence successor.Evidence) (Documents, error) {
	var documents Documents
	encoders := []struct {
		name string
		dst  *[]byte
		fn   func() ([]byte, error)
	}{
		{"set", &documents.Set, set.CanonicalJSON}, {"manifest", &documents.Manifest, evidence.Manifest.CanonicalJSON},
		{"anchor", &documents.Anchor, evidence.Anchor.CanonicalJSON}, {"profiles", &documents.Profiles, evidence.Profiles.CanonicalJSON},
		{"receipt", &documents.Receipt, evidence.Receipt.CanonicalJSON}, {"authorities", &documents.Authorities, evidence.Authorities.CanonicalJSON},
	}
	for _, encoder := range encoders {
		raw, err := encoder.fn()
		if err != nil {
			return Documents{}, fmt.Errorf("%w: canonical %s: %v", ErrInvalid, encoder.name, err)
		}
		*encoder.dst = raw
	}
	return documents, nil
}

type signingFailure struct{ cause error }

func (e *signingFailure) Error() string   { return "successor capture signing failed" }
func (e *signingFailure) Unwrap() []error { return []error{ErrSigning, e.cause} }

type verificationFailure struct{ cause error }

func (e *verificationFailure) Error() string {
	return "successor capture exact provider-version verification failed"
}
func (e *verificationFailure) Unwrap() []error { return []error{ErrIncomplete, e.cause} }

// observationFailure intentionally has a static diagnostic. Provider
// adapters may include credentials, object contents, or remote response text
// in their errors; callers still retain errors.Is/As access to the typed cause.
type observationFailure struct{ cause error }

func (e *observationFailure) Error() string {
	return "successor capture provider observation lookup failed"
}
func (e *observationFailure) Unwrap() []error { return []error{ErrIncomplete, e.cause} }

type policyResolutionFailure struct{ cause error }

func (e *policyResolutionFailure) Error() string {
	return "successor capture trust policy resolution failed"
}
func (e *policyResolutionFailure) Unwrap() []error { return []error{ErrInvalid, e.cause} }

type sourceFailure struct{ cause error }

func (e *sourceFailure) Error() string   { return "successor capture managed projection failed" }
func (e *sourceFailure) Unwrap() []error { return []error{ErrIncomplete, e.cause} }
