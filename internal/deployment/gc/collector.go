// Package gc implements the physical-pool mark-and-sweep protocol.
//
// The collector deliberately owns no storage credentials and no DuckLake
// cleanup capability.  A target supplies a pool-scoped object store and an
// immutable-catalog inspector; SQLite is used only through the control-plane
// ports below.
package gc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/google/uuid"
)

var (
	ErrInvalidConfig       = errors.New("global GC configuration is invalid")
	ErrCatalogQuarantined  = errors.New("rooted catalog was quarantined")
	ErrGCStale             = errors.New("global GC fence is stale")
	ErrDeleteUncertain     = errors.New("object delete acknowledgement is ambiguous")
	ErrObjectOutsidePool   = errors.New("object is outside the declared physical pool")
	ErrMissingObjectDigest = errors.New("pool object has no immutable digest")
)

// namespaceOwnershipMarkerKey is the durable, non-secret fence object inside
// every admitted pool namespace. Stores exclude it from deletion candidates;
// keeping it in the mark digest makes that exclusion part of the resumable GC
// evidence as well.
const namespaceOwnershipMarkerKey = ".leapview-pool-owner.json"

// Object is the provider-neutral inventory item returned by a target-owned
// pool store.  Digest must identify the exact version which Delete receives.
type Object struct {
	Key          string
	Digest       string
	Version      string
	Size         int64
	CreatedAt    time.Time
	LastModified time.Time
	Metadata     map[string]string
}

type DeleteRequest struct {
	PhysicalPoolID string
	Key            string
	Digest         string
	Version        string
}

type DeleteResponse struct {
	Deleted  bool
	NotFound bool
}

// PoolStore is intentionally pool-scoped.  Implementations must reject a
// prefix or bucket outside the declared pool before listing or deleting.
type PoolStore interface {
	Open(context.Context, string) (CatalogObject, error)
	ListPoolObjects(context.Context, string) ([]Object, error)
	DeleteConditional(context.Context, DeleteRequest) (DeleteResponse, error)
	Stat(context.Context, string, string) (Object, error)
}

type CatalogObject struct {
	Body     io.ReadCloser
	Size     int64
	Metadata map[string]string
}

// CatalogReachability is the complete mark evidence for one immutable catalog.
type CatalogReachability struct {
	CatalogKey    string
	CatalogDigest string
	DataFiles     []string
	DeleteFiles   []string
}

// Inspector independently verifies and reads one immutable artifact.  The
// default Inspector below attaches read-only; tests and remote targets may
// provide another implementation with the same fail-closed contract.
type Inspector interface {
	Inspect(context.Context, deployment.DeliveryRoot) (CatalogReachability, error)
}

type ControlPlane interface {
	AcquireGCFence(context.Context, deployment.GCFenceRequest) (deployment.GCFence, error)
	ReleaseGCFence(context.Context, deployment.GCFence, time.Time) error
	IsCurrentGCFence(context.Context, deployment.GCFence, time.Time) (bool, error)
	EnumerateRoots(context.Context, string, time.Time) (deployment.RootSet, error)
	CreateGCCycle(context.Context, deployment.DeliveryGCCycle) (deployment.DeliveryGCCycle, error)
	MarkGCCycle(context.Context, string, string) (deployment.DeliveryGCCycle, error)
	BeginGCDelete(context.Context, string) (deployment.DeliveryGCCycle, error)
	CreateGCDeleteIntent(context.Context, deployment.DeliveryGCDeleteIntent) (deployment.DeliveryGCDeleteIntent, error)
	CompleteGCDeleteIntent(context.Context, string, deployment.DeliveryGCDeleteIntentStatus, time.Time) (deployment.DeliveryGCDeleteIntent, error)
	CompleteGCCycle(context.Context, string, time.Time) (deployment.DeliveryGCCycle, error)
	AbortGCCycle(context.Context, string, string, time.Time) (deployment.DeliveryGCCycle, error)
}

// GracefulRootEnumerator lets a control plane keep recently expired reader
// leases in the mark set while physical readers drain. Implementations which
// do not persist query-root expiry (for example bounded test controls) retain
// the original EnumerateRoots behavior.
type GracefulRootEnumerator interface {
	EnumerateRootsWithGrace(context.Context, string, time.Time, time.Duration) (deployment.RootSet, error)
}

type PendingIntents interface {
	ListGCDeleteIntents(context.Context, string) ([]deployment.DeliveryGCDeleteIntent, error)
}

type Quarantiner interface {
	QuarantineRoot(context.Context, deployment.DeliveryRoot, string, time.Time) error
}

// Revalidator is called immediately before the destructive phase and before
// every bounded batch. Implementations re-check writer leases, query leases,
// publication/candidate state, and any target-specific retirement fence.
type Revalidator interface {
	RevalidateGC(context.Context, deployment.GCFence, time.Time) error
}

type Config struct {
	PhysicalPoolID string
	HolderID       string
	LeaseID        string
	CycleID        string
	// IDGenerator supplies run-scoped control-plane identities. A nil
	// generator uses the default cryptographically-random UUIDv7 generator.
	// Generated values must be canonical UUIDv7 strings.
	IDGenerator       func() (string, error)
	Now               func() time.Time
	LeaseDuration     time.Duration
	BatchSize         int
	BuildGrace        time.Duration
	OrphanGrace       time.Duration
	ReaderGrace       time.Duration
	ProtectedPrefixes []string
	PoolPrefix        string
	MetadataKeys      []string
	Ownership         physicalpool.NamespaceOwnership
	OwnershipClaim    physicalpool.OwnershipClaim
	OwnershipClaims   []physicalpool.OwnershipClaim
	RequireOwnership  bool
	DeletionLease     physicalpool.NamespaceDeletionLease
	LeaseOwnerID      string
	RequireLease      bool
}

type Result struct {
	Cycle      deployment.DeliveryGCCycle
	Roots      int
	Marked     int
	Candidates int
	Deleted    int
	Ambiguous  int
}

type Collector struct {
	Control     ControlPlane
	Store       PoolStore
	Inspector   Inspector
	Revalidator Revalidator
	Quarantiner Quarantiner
	Config      Config
}

func New(control ControlPlane, store PoolStore, inspector Inspector, config Config) (*Collector, error) {
	c := &Collector{Control: control, Store: store, Inspector: inspector, Config: config}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Collect is a readable alias used by maintenance schedulers.
func (c Collector) Collect(ctx context.Context) (Result, error) { return c.Run(ctx) }

func (c Collector) validate() error {
	if c.Control == nil || c.Store == nil || c.Inspector == nil {
		return fmt.Errorf("%w: control, store and inspector are required", ErrInvalidConfig)
	}
	if err := deployment.ValidateDeliveryID(c.Config.PhysicalPoolID); err != nil {
		return fmt.Errorf("%w: pool: %v", ErrInvalidConfig, err)
	}
	if err := deployment.ValidateDeliveryID(c.Config.HolderID); err != nil {
		return fmt.Errorf("%w: holder: %v", ErrInvalidConfig, err)
	}
	if c.Config.LeaseID != "" {
		if _, err := canonicalUUIDv7(c.Config.LeaseID); err != nil {
			return fmt.Errorf("%w: lease: %v", ErrInvalidConfig, err)
		}
	}
	if c.Config.CycleID != "" {
		if _, err := canonicalUUIDv7(c.Config.CycleID); err != nil {
			return fmt.Errorf("%w: cycle: %v", ErrInvalidConfig, err)
		}
	}
	if c.Config.Now == nil {
		c.Config.Now = func() time.Time { return time.Now().UTC() }
	}
	if c.Config.BatchSize <= 0 {
		c.Config.BatchSize = 64
	}
	if c.Config.LeaseDuration <= 0 {
		c.Config.LeaseDuration = 15 * time.Minute
	}
	if c.Config.BuildGrace < 0 || c.Config.OrphanGrace < 0 || c.Config.ReaderGrace < 0 {
		return fmt.Errorf("%w: grace periods cannot be negative", ErrInvalidConfig)
	}
	if c.Config.RequireOwnership {
		if c.Config.Ownership == nil {
			return fmt.Errorf("%w: physical-pool ownership marker is required", ErrInvalidConfig)
		}
		claims := c.Config.OwnershipClaims
		if len(claims) == 0 {
			claims = []physicalpool.OwnershipClaim{c.Config.OwnershipClaim}
		}
		for _, claim := range claims {
			if err := claim.Validate(); err != nil {
				return fmt.Errorf("%w: ownership claim: %v", ErrInvalidConfig, err)
			}
			if string(claim.PoolID) != c.Config.PhysicalPoolID {
				return fmt.Errorf("%w: ownership claim pool mismatch", ErrInvalidConfig)
			}
		}
	}
	if c.Config.RequireLease {
		if c.Config.DeletionLease == nil || strings.TrimSpace(c.Config.LeaseOwnerID) == "" {
			return fmt.Errorf("%w: physical-pool deletion lease is required", ErrInvalidConfig)
		}
	}
	return nil
}

func generateUUIDv7() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate UUIDv7 identity: %w", err)
	}
	return id.String(), nil
}

func canonicalUUIDv7(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", fmt.Errorf("UUIDv7 identity must be canonical")
	}
	id, err := uuid.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse UUIDv7 identity: %w", err)
	}
	if id.String() != value {
		return "", fmt.Errorf("UUIDv7 identity must use canonical lowercase RFC 4122 form")
	}
	if id.Version() != uuid.Version(7) || id.Variant() != uuid.RFC4122 {
		return "", fmt.Errorf("identity must be an RFC 4122 UUIDv7")
	}
	return value, nil
}

func (c Collector) newID() (string, error) {
	generator := c.Config.IDGenerator
	if generator == nil {
		generator = generateUUIDv7
	}
	value, err := generator()
	if err != nil {
		return "", fmt.Errorf("generate GC identity: %w", err)
	}
	value, err = canonicalUUIDv7(value)
	if err != nil {
		return "", fmt.Errorf("generate GC identity: %w", err)
	}
	return value, nil
}

// Run executes or resumes one bounded cycle. A corrupt or unavailable rooted
// catalog aborts before listing or deleting any candidate object.
func (c Collector) Run(ctx context.Context) (Result, error) {
	// Defaults are applied on the local value so a retry receives a fresh,
	// never-reused fence identity when the caller did not provide one.
	if c.Config.Now == nil {
		c.Config.Now = func() time.Time { return time.Now().UTC() }
	}
	if c.Config.BatchSize <= 0 {
		c.Config.BatchSize = 64
	}
	if c.Config.LeaseDuration <= 0 {
		c.Config.LeaseDuration = 15 * time.Minute
	}
	if c.Config.LeaseID == "" {
		leaseID, err := c.newID()
		if err != nil {
			return Result{}, err
		}
		c.Config.LeaseID = leaseID
	}
	if err := c.validate(); err != nil {
		return Result{}, err
	}
	now := c.Config.Now().UTC()
	if now.IsZero() {
		return Result{}, fmt.Errorf("%w: Now returned zero time", ErrInvalidConfig)
	}
	if c.Config.RequireOwnership {
		claims := c.Config.OwnershipClaims
		if len(claims) == 0 {
			claims = []physicalpool.OwnershipClaim{c.Config.OwnershipClaim}
		}
		var ownershipErr error
		for _, claim := range claims {
			if err := c.Config.Ownership.VerifyNamespaceOwnership(ctx, claim); err == nil {
				ownershipErr = nil
				break
			} else {
				ownershipErr = err
			}
		}
		if ownershipErr != nil {
			return Result{}, fmt.Errorf("%w: %v", physicalpool.ErrOwnershipConflict, ownershipErr)
		}
	}
	leaseToken := ""
	leaseOwnerID := c.Config.LeaseOwnerID
	holderID := c.Config.HolderID
	if c.Config.RequireLease {
		var leaseErr error
		// Keep the durable database owner as the authorization root, but use a
		// fresh holder identity for each run so a crashed/restarted worker cannot
		// accidentally treat a stale lease as its own.
		holderIdentity, identityErr := c.newID()
		if identityErr != nil {
			return Result{}, identityErr
		}
		fenceIdentity, identityErr := c.newID()
		if identityErr != nil {
			return Result{}, identityErr
		}
		leaseOwnerID = leaseOwnerID + "/" + holderIdentity
		holderID = holderID + "/" + fenceIdentity
		leaseToken, leaseErr = c.Config.DeletionLease.AcquireNamespaceDeletionLease(ctx, leaseOwnerID, c.Config.LeaseDuration)
		if leaseErr != nil {
			return Result{}, fmt.Errorf("%w: %v", physicalpool.ErrDeletionLeaseConflict, leaseErr)
		}
		defer func() {
			_ = c.Config.DeletionLease.ReleaseNamespaceDeletionLease(context.Background(), leaseOwnerID, leaseToken)
		}()
	}
	leaseID := c.Config.LeaseID
	fence, err := c.Control.AcquireGCFence(ctx, deployment.GCFenceRequest{ID: leaseID, PhysicalPoolID: c.Config.PhysicalPoolID, HolderID: holderID, CreatedAt: now, ExpiresAt: now.Add(c.Config.LeaseDuration)})
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = c.Control.ReleaseGCFence(context.Background(), fence, c.Config.Now().UTC()) }()

	var roots deployment.RootSet
	if graceful, ok := c.Control.(GracefulRootEnumerator); ok {
		roots, err = graceful.EnumerateRootsWithGrace(ctx, c.Config.PhysicalPoolID, now, c.Config.ReaderGrace)
	} else {
		roots, err = c.Control.EnumerateRoots(ctx, c.Config.PhysicalPoolID, now)
	}
	if err != nil {
		return Result{}, err
	}
	if roots.Revision != fence.RootRevision {
		return Result{}, ErrGCStale
	}
	cycleID := c.Config.CycleID
	if cycleID == "" {
		cycleID, err = c.newID()
		if err != nil {
			return Result{}, err
		}
	}
	cycle, err := c.Control.CreateGCCycle(ctx, deployment.DeliveryGCCycle{ID: cycleID, ActorID: c.Config.HolderID, PhysicalPoolID: c.Config.PhysicalPoolID, Epoch: fence.Epoch, RootRevision: roots.Revision, CreatedAt: now})
	if err != nil {
		return Result{}, err
	}
	result := Result{Cycle: cycle, Roots: len(roots.Roots)}
	if cycle.Status == deployment.DeliveryGCComplete {
		result.Cycle = cycle
		return result, nil
	}

	marks := map[string]struct{}{}
	for _, root := range roots.Roots {
		reach, inspectErr := c.Inspector.Inspect(ctx, root)
		if inspectErr != nil {
			reason := inspectErr.Error()
			// SQLite's GC guard intentionally rejects creation of a new root
			// while the fence is held. Release first, then persist quarantine;
			// no destructive phase has started and the next cycle sees the hold.
			var quarantineErr error
			if c.Quarantiner != nil {
				_ = c.Control.ReleaseGCFence(context.Background(), fence, c.Config.Now().UTC())
				quarantineErr = c.Quarantiner.QuarantineRoot(ctx, root, reason, now)
			}
			_, _ = c.Control.AbortGCCycle(ctx, cycle.ID, "catalog verification failed: "+reason, now)
			if quarantineErr != nil {
				return result, errors.Join(fmt.Errorf("%w: %s: %v", ErrCatalogQuarantined, root.ObjectKey, inspectErr), quarantineErr)
			}
			return result, fmt.Errorf("%w: %s: %v", ErrCatalogQuarantined, root.ObjectKey, inspectErr)
		}
		if reach.CatalogKey != root.ObjectKey || reach.CatalogDigest != root.CatalogDigest {
			return result, fmt.Errorf("%w: inspector returned catalog identity %q/%q for root %q/%q", ErrCatalogQuarantined, reach.CatalogKey, reach.CatalogDigest, root.ObjectKey, root.CatalogDigest)
		}
		marks[reach.CatalogKey] = struct{}{}
		for _, key := range append(reach.DataFiles, reach.DeleteFiles...) {
			marks[key] = struct{}{}
		}
	}
	for _, key := range c.Config.MetadataKeys {
		if key != "" {
			marks[key] = struct{}{}
		}
	}
	if c.Config.RequireOwnership {
		marks[namespaceOwnershipMarkerKey] = struct{}{}
	}
	markDigest := digestMarks(marks)
	result.Marked = len(marks)
	if cycle.MarkDigest != "" && cycle.MarkDigest != markDigest {
		_, _ = c.Control.AbortGCCycle(ctx, cycle.ID, "root mark changed during retry", c.Config.Now().UTC())
		return result, ErrGCStale
	}
	if cycle.Status == deployment.DeliveryGCRunning {
		cycle, err = c.Control.MarkGCCycle(ctx, cycle.ID, markDigest)
		if err != nil {
			return result, err
		}
	}
	if cycle.Status == deployment.DeliveryGCMarked {
		cycle, err = c.Control.BeginGCDelete(ctx, cycle.ID)
		if err != nil {
			return result, err
		}
	}
	result.Cycle = cycle

	if err := c.revalidate(ctx, fence, now); err != nil {
		return result, err
	}
	if c.Config.RequireLease {
		if err := c.Config.DeletionLease.VerifyNamespaceDeletionLease(ctx, leaseOwnerID, leaseToken); err != nil {
			return result, fmt.Errorf("%w: %v", physicalpool.ErrDeletionLeaseConflict, err)
		}
	}
	objects, err := c.Store.ListPoolObjects(ctx, c.Config.PhysicalPoolID)
	if err != nil {
		return result, err
	}
	candidates, err := c.selectCandidates(objects, marks, now)
	if err != nil {
		return result, err
	}
	result.Candidates = len(candidates)

	// On retry, pending intents are authoritative for the current cycle. New
	// candidates are added only if they still match the exact mark evidence.
	processed := map[string]struct{}{}
	if pending, ok := c.Control.(PendingIntents); ok {
		intents, listErr := pending.ListGCDeleteIntents(ctx, cycle.ID)
		if listErr != nil {
			return result, listErr
		}
		for start := 0; start < len(intents); start += c.Config.BatchSize {
			if err := c.revalidate(ctx, fence, c.Config.Now().UTC()); err != nil {
				return result, err
			}
			if err := c.verifyDeletionLease(ctx, leaseOwnerID, leaseToken); err != nil {
				return result, err
			}
			end := start + c.Config.BatchSize
			if end > len(intents) {
				end = len(intents)
			}
			for _, intent := range intents[start:end] {
				processed[intent.ObjectKey] = struct{}{}
				if intent.Status == deployment.DeliveryGCDeletePending {
					if err := c.deleteIntent(ctx, fence, intent, now); err != nil {
						return result, err
					}
					result.Deleted++
				}
			}
		}
	}
	for start := 0; start < len(candidates); start += c.Config.BatchSize {
		if err := c.revalidate(ctx, fence, c.Config.Now().UTC()); err != nil {
			return result, err
		}
		if err := c.verifyDeletionLease(ctx, leaseOwnerID, leaseToken); err != nil {
			return result, err
		}
		end := start + c.Config.BatchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		for _, object := range candidates[start:end] {
			if _, already := processed[object.Key]; already {
				continue
			}
			intentID, identityErr := c.newID()
			if identityErr != nil {
				return result, identityErr
			}
			intent := deployment.DeliveryGCDeleteIntent{ID: intentID, CycleID: cycle.ID, PhysicalPoolID: c.Config.PhysicalPoolID, ObjectKey: object.Key, ObjectDigest: object.Digest, ObjectVersion: object.Version, CreatedAt: c.Config.Now().UTC()}
			intent, err = c.Control.CreateGCDeleteIntent(ctx, intent)
			if err != nil {
				return result, err
			}
			if err := c.deleteIntent(ctx, fence, intent, c.Config.Now().UTC()); err != nil {
				return result, err
			}
			result.Deleted++
		}
	}
	if err := c.revalidate(ctx, fence, c.Config.Now().UTC()); err != nil {
		return result, err
	}
	if err := c.verifyDeletionLease(ctx, leaseOwnerID, leaseToken); err != nil {
		return result, err
	}
	cycle, err = c.Control.CompleteGCCycle(ctx, cycle.ID, c.Config.Now().UTC())
	result.Cycle = cycle
	return result, err
}

func (c Collector) verifyDeletionLease(ctx context.Context, ownerID, token string) error {
	if !c.Config.RequireLease {
		return nil
	}
	if err := c.Config.DeletionLease.VerifyNamespaceDeletionLease(ctx, ownerID, token); err != nil {
		return fmt.Errorf("%w: %v", physicalpool.ErrDeletionLeaseConflict, err)
	}
	return nil
}

func (c Collector) revalidate(ctx context.Context, fence deployment.GCFence, now time.Time) error {
	ok, err := c.Control.IsCurrentGCFence(ctx, fence, now)
	if err != nil {
		return err
	}
	if !ok {
		return ErrGCStale
	}
	if c.Revalidator != nil {
		return c.Revalidator.RevalidateGC(ctx, fence, now)
	}
	return nil
}

func digestMarks(marks map[string]struct{}) string {
	keys := make([]string, 0, len(marks))
	for key := range marks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	b, _ := json.Marshal(keys)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (c Collector) selectCandidates(objects []Object, marks map[string]struct{}, now time.Time) ([]Object, error) {
	result := make([]Object, 0, len(objects))
	for _, object := range objects {
		if strings.TrimSpace(object.Key) == "" || !validPoolKey(c.Config.PoolPrefix, object.Key) {
			return nil, fmt.Errorf("%w: %q", ErrObjectOutsidePool, object.Key)
		}
		if _, live := marks[object.Key]; live {
			continue
		}
		if err := deployment.ValidateDeliveryDigest(object.Digest); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrMissingObjectDigest, object.Key)
		}
		if protectedPrefix(object.Key, c.Config.ProtectedPrefixes) {
			continue
		}
		stamp := object.LastModified
		if stamp.IsZero() {
			stamp = object.CreatedAt
		}
		if stamp.IsZero() {
			// Unknown age cannot satisfy any configured grace period; retain it
			// until a target can provide immutable creation/version evidence.
			continue
		}
		if !stamp.IsZero() {
			grace := c.Config.OrphanGrace
			if buildPrefix(object.Key) {
				grace = c.Config.BuildGrace
			} else if readerPrefix(object.Key) {
				grace = c.Config.ReaderGrace
			}
			// Grace is inclusive: an object becomes eligible only once its age
			// exceeds the configured boundary.
			if grace > 0 && now.Sub(stamp) <= grace {
				continue
			}
		}
		result = append(result, object)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result, nil
}

func protectedPrefix(key string, prefixes []string) bool {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(strings.ReplaceAll(prefix, "\\", "/"))
		if prefix != "" && (key == prefix || strings.HasPrefix(key, strings.TrimSuffix(prefix, "/")+"/") || strings.Contains(key, "/"+strings.TrimSuffix(prefix, "/")+"/")) {
			return true
		}
	}
	return false
}

func buildPrefix(key string) bool {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	for _, prefix := range []string{"build/", "builds/", "inflight/", "in-flight/"} {
		if key == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func readerPrefix(key string) bool {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	for _, prefix := range []string{"reader/", "readers/"} {
		if key == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func validPoolKey(prefix, key string) bool {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || strings.Contains(key, "\x00") {
		return false
	}
	if strings.Contains(key, "://") {
		return strings.HasPrefix(key, strings.TrimRight(prefix, "/")+"/") || key == strings.TrimRight(prefix, "/")
	}
	if strings.Contains(key, "..") {
		for _, part := range strings.Split(key, "/") {
			if part == ".." {
				return false
			}
		}
	}
	if prefix == "" {
		return true
	}
	prefix = strings.TrimRight(strings.ReplaceAll(prefix, "\\", "/"), "/")
	return key == prefix || strings.HasPrefix(key, prefix+"/")
}

func (c Collector) deleteIntent(ctx context.Context, fence deployment.GCFence, intent deployment.DeliveryGCDeleteIntent, now time.Time) error {
	if err := c.revalidate(ctx, fence, now); err != nil {
		return err
	}
	// Resolve the provider generation immediately before the conditional delete.
	// The durable intent's digest is the stable evidence; a target which offers
	// versions must use this observed generation and reject replacement under a
	// matching key/digest.
	version := ""
	if current, statErr := c.Store.Stat(ctx, intent.PhysicalPoolID, intent.ObjectKey); statErr == nil {
		if current.Digest != intent.ObjectDigest {
			_, _ = c.Control.CompleteGCDeleteIntent(ctx, intent.ID, deployment.DeliveryGCDeleteAmbiguous, c.Config.Now().UTC())
			return ErrDeleteUncertain
		}
		if intent.ObjectVersion != "" && current.Version != intent.ObjectVersion {
			_, _ = c.Control.CompleteGCDeleteIntent(ctx, intent.ID, deployment.DeliveryGCDeleteAmbiguous, c.Config.Now().UTC())
			return ErrDeleteUncertain
		}
		version = current.Version
	} else if errors.Is(statErr, os.ErrNotExist) {
		_, completeErr := c.Control.CompleteGCDeleteIntent(ctx, intent.ID, deployment.DeliveryGCDeleteDeleted, c.Config.Now().UTC())
		return completeErr
	} else if statErr != nil {
		return fmt.Errorf("stat exact delete target: %w", statErr)
	}
	response, deleteErr := c.Store.DeleteConditional(ctx, DeleteRequest{PhysicalPoolID: intent.PhysicalPoolID, Key: intent.ObjectKey, Digest: intent.ObjectDigest, Version: version})
	status := deployment.DeliveryGCDeleteDeleted
	if deleteErr != nil || (!response.Deleted && !response.NotFound) {
		stat, statErr := c.Store.Stat(ctx, intent.PhysicalPoolID, intent.ObjectKey)
		if errors.Is(statErr, os.ErrNotExist) || response.NotFound {
			status = deployment.DeliveryGCDeleteDeleted
		} else if statErr == nil && stat.Digest != intent.ObjectDigest {
			status = deployment.DeliveryGCDeleteAmbiguous
		} else {
			return fmt.Errorf("%w: %s: %v", ErrDeleteUncertain, intent.ObjectKey, deleteErr)
		}
	}
	_, err := c.Control.CompleteGCDeleteIntent(ctx, intent.ID, status, c.Config.Now().UTC())
	return err
}
