package ducklake

// PostgreSQL-backed DuckLake attachment and commit identity primitives.
//
// This file intentionally contains no runtime composition. Runtime cutover
// wires these target primitives separately. Callers can use these helpers to
// build the exact statements and evidence required by a PostgreSQL metadata
// catalog without ever putting a PostgreSQL DSN (or credential) in an ATTACH
// statement.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
)

// CommitMarker exposes the engine-neutral catalog commit contract at the
// DuckLake writer boundary. Control-plane authorities depend on the contract
// package directly rather than importing this adapter.
type CommitMarker = catalogartifact.CommitMarker

const (
	CommitMarkerSchemaVersion = catalogartifact.CommitMarkerSchemaVersion
	MaxCommitMarkerBytes      = catalogartifact.MaxCommitMarkerBytes
	MaxCommitMarkerFieldBytes = catalogartifact.MaxCommitMarkerFieldBytes
)

var catalogIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	// ErrCommittedSnapshotNotFound means no persistent snapshot carries the
	// exact marker. It is distinct from an ambiguous or malformed catalog so
	// restart reconciliation can require positive session termination evidence.
	ErrCommittedSnapshotNotFound         = errors.New("DuckLake committed snapshot identity was not found")
	ErrCommittedSnapshotAmbiguous        = errors.New("multiple DuckLake snapshots match the commit marker")
	ErrCommittedSnapshotDigestMismatch   = errors.New("DuckLake committed snapshot marker digest differs from the attempt")
	ErrCommittedSnapshotIdentityMismatch = errors.New("DuckLake committed snapshot marker identity differs from the attempt")
)

// PhysicalMarkerAnomaly classifies persistent marker evidence that must be
// durably quarantined rather than treated as an absent commit.
type PhysicalMarkerAnomaly string

const (
	PhysicalMarkerAnomalyDuplicate        PhysicalMarkerAnomaly = "duplicate"
	PhysicalMarkerAnomalyDigestMismatch   PhysicalMarkerAnomaly = "digest_mismatch"
	PhysicalMarkerAnomalyIdentityMismatch PhysicalMarkerAnomaly = "identity_mismatch"
)

const (
	maxObservedMarkerDigests = 16
	maxObservedSnapshotIDs   = 128
)

// Fixed-size evidence arrays keep PhysicalMarkerResolution value-comparable
// for callers while bounding what a catalog scan can return.
type PhysicalMarkerObservedDigests [maxObservedMarkerDigests]string
type PhysicalMarkerObservedSnapshotIDs [maxObservedSnapshotIDs]int64

// PostgresCatalogMode selects the lifecycle contract for an attachment.
// Initialization is the only mode allowed to create a missing catalog.
type PostgresCatalogMode string

const (
	// PostgresCatalogInitialize permits CREATE_IF_NOT_EXISTS true and is
	// intended for one controlled bootstrap operation.
	PostgresCatalogInitialize PostgresCatalogMode = "initialize"
	// PostgresCatalogWriter attaches an existing catalog for a writer.  It
	// deliberately does not create a catalog implicitly.
	PostgresCatalogWriter PostgresCatalogMode = "writer"
	// PostgresCatalogServing attaches a qualified snapshot read-only.
	PostgresCatalogServing PostgresCatalogMode = "serving"
	// PostgresCatalogMarkerReadOnly attaches an existing PostgreSQL catalog
	// read-only without pinning one snapshot. It is reserved for exact commit
	// marker reconciliation; callers must not use it for serving reads.
	PostgresCatalogMarkerReadOnly PostgresCatalogMode = "marker_read_only"
	// PostgresCatalogRecovery is a descriptive alias for marker reconciliation.
	PostgresCatalogRecovery = PostgresCatalogMarkerReadOnly
	// PostgresCatalogMigrate is reserved for the fenced catalog upgrade
	// coordinator. It is never accepted by ordinary AttachSQL/Statements;
	// callers must use MigrationStatements so AUTOMATIC_MIGRATION=true is an
	// explicit, reviewable operation.
	PostgresCatalogMigrate PostgresCatalogMode = "migrate"
)

// PostgresCatalogConfig describes one DuckLake PostgreSQL metadata attach.
// PostgreSQL connection details are supplied by a separately provisioned
// DuckDB postgres secret.  No DSN or password is accepted by this type.
type PostgresCatalogConfig struct {
	// PhysicalPoolID scopes the metadata schema to one admitted pool/security
	// domain. It is required for fenced migration mode and, when supplied for
	// runtime modes, must match MetadataSchema exactly.
	PhysicalPoolID  string
	DuckLakeSecret  string
	PostgresSecret  string
	MetadataSchema  string
	DataPath        string
	Mode            PostgresCatalogMode
	SnapshotVersion int64
}

// Validate checks names, mode invariants, and required storage identity.
func (c PostgresCatalogConfig) Validate() error {
	if strings.ContainsAny(c.DataPath, "\x00\r\n") {
		return errors.New("DATA_PATH contains a control character")
	}
	if strings.TrimSpace(c.PhysicalPoolID) != "" && strings.TrimSpace(c.PhysicalPoolID) != c.PhysicalPoolID {
		return errors.New("physical pool id is not normalized")
	}
	if err := validateCatalogIdentifier("DuckLake secret", c.DuckLakeSecret); err != nil {
		return err
	}
	if err := validateCatalogIdentifier("PostgreSQL secret", c.PostgresSecret); err != nil {
		return err
	}
	if err := validateCatalogIdentifier("metadata schema", c.MetadataSchema); err != nil {
		return err
	}
	if c.PhysicalPoolID != "" && c.MetadataSchema != MetadataSchemaForPool(c.PhysicalPoolID) {
		return fmt.Errorf("metadata schema %q is not admitted for physical pool %q", c.MetadataSchema, c.PhysicalPoolID)
	}
	if strings.TrimSpace(string(c.Mode)) == "" {
		return errors.New("PostgreSQL DuckLake catalog mode is required")
	}
	switch c.Mode {
	case PostgresCatalogInitialize:
		if strings.TrimSpace(c.DataPath) == "" {
			return errors.New("DATA_PATH is required when initializing a PostgreSQL DuckLake catalog")
		}
		if c.SnapshotVersion != 0 {
			return errors.New("SNAPSHOT_VERSION is not valid while initializing a PostgreSQL DuckLake catalog")
		}
	case PostgresCatalogWriter:
		if strings.TrimSpace(c.DataPath) != "" {
			return errors.New("DATA_PATH must be loaded from catalog metadata for an existing writer attachment")
		}
		if c.SnapshotVersion != 0 {
			return errors.New("SNAPSHOT_VERSION is only valid for a read-only serving attachment")
		}
	case PostgresCatalogServing:
		if strings.TrimSpace(c.DataPath) != "" {
			return errors.New("DATA_PATH must be loaded from catalog metadata for a serving attachment")
		}
		if c.SnapshotVersion <= 0 {
			return errors.New("serving PostgreSQL DuckLake attachment requires a positive SNAPSHOT_VERSION")
		}
	case PostgresCatalogMarkerReadOnly:
		if strings.TrimSpace(c.DataPath) != "" {
			return errors.New("DATA_PATH must be loaded from catalog metadata for marker reconciliation")
		}
		if c.SnapshotVersion != 0 {
			return errors.New("SNAPSHOT_VERSION is not valid while resolving a commit marker")
		}
	case PostgresCatalogMigrate:
		if strings.TrimSpace(c.PhysicalPoolID) == "" {
			return errors.New("physical pool id is required when migrating a PostgreSQL DuckLake catalog")
		}
		if strings.TrimSpace(c.DataPath) != "" {
			return errors.New("DATA_PATH must be loaded from catalog metadata when migrating a PostgreSQL DuckLake catalog")
		}
		if c.SnapshotVersion != 0 {
			return errors.New("SNAPSHOT_VERSION is not valid while migrating a PostgreSQL DuckLake catalog")
		}
	default:
		return fmt.Errorf("unsupported PostgreSQL DuckLake catalog mode %q", c.Mode)
	}
	return nil
}

// MetadataSchemaForPool derives a stable, SQL-safe metadata namespace for one
// physical pool. The digest avoids leaking arbitrary tenant identifiers while
// preventing one catalog attach from reflecting another pool's tables.
func MetadataSchemaForPool(physicalPoolID string) string {
	digest := sha256.Sum256([]byte(physicalPoolID))
	return "leapview_catalog_" + hex.EncodeToString(digest[:])[:32]
}

func validateCatalogIdentifier(label, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", label)
	}
	if value != trimmed {
		return fmt.Errorf("%s %q is not normalized", label, value)
	}
	if !catalogIdentifierPattern.MatchString(value) {
		return fmt.Errorf("%s %q is not a safe SQL identifier", label, value)
	}
	return nil
}

// DuckLakeSecretSQL creates a temporary DuckLake secret whose metadata
// backend references a separately provisioned PostgreSQL secret.  The empty
// METADATA_PATH is intentional: credentials and endpoint details stay in the
// postgres secret and never appear in this SQL or an ATTACH diagnostic.
func (c PostgresCatalogConfig) DuckLakeSecretSQL() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"CREATE OR REPLACE TEMPORARY SECRET %s (TYPE ducklake, METADATA_PATH '', METADATA_PARAMETERS MAP {'TYPE': 'postgres', 'SECRET': '%s'})",
		quoteCatalogIdentifier(c.DuckLakeSecret), sqlLiteral(c.PostgresSecret)), nil
}

// AttachSQL builds the deterministic DuckLake attachment for this mode.
// AUTOMATIC_MIGRATION is always disabled.  A serving attachment always pins
// READ_ONLY and the exact snapshot supplied by its immutable seal.
func (c PostgresCatalogConfig) AttachSQL() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(c.DataPath) != "" {
		canonical, err := CanonicalDataPath(c.DataPath)
		if err != nil {
			return "", err
		}
		c.DataPath = canonical
	}
	if c.Mode == PostgresCatalogMigrate {
		return "", errors.New("PostgresCatalogMigrate requires the fenced MigrationStatements operation")
	}
	options := []string{
		fmt.Sprintf("METADATA_SCHEMA '%s'", sqlLiteral(c.MetadataSchema)),
		"AUTOMATIC_MIGRATION false",
		"DATA_INLINING_ROW_LIMIT 0",
	}
	if strings.TrimSpace(c.DataPath) != "" {
		options = append(options, fmt.Sprintf("DATA_PATH '%s'", sqlLiteral(c.DataPath)))
	}
	switch c.Mode {
	case PostgresCatalogInitialize:
		options = append(options, "CREATE_IF_NOT_EXISTS true")
	case PostgresCatalogWriter:
		options = append(options, "CREATE_IF_NOT_EXISTS false")
	case PostgresCatalogServing:
		options = append(options, "READ_ONLY", "CREATE_IF_NOT_EXISTS false", fmt.Sprintf("SNAPSHOT_VERSION %d", c.SnapshotVersion))
	case PostgresCatalogMarkerReadOnly:
		options = append(options, "READ_ONLY", "CREATE_IF_NOT_EXISTS false")
	}
	// A pooled :memory: DuckDB database may invoke the connector initializer
	// once per physical client while sharing the process-local attached
	// catalog. IF NOT EXISTS makes these idempotent warm-up attaches without
	// weakening CREATE_IF_NOT_EXISTS=false for the metadata catalog itself.
	return fmt.Sprintf("ATTACH IF NOT EXISTS 'ducklake:%s' AS %s (%s)", sqlLiteral(c.DuckLakeSecret), quoteCatalogIdentifier(catalogAlias), strings.Join(options, ", ")), nil
}

// MigrationStatements is the only SQL constructor that enables DuckLake's
// automatic schema migration. It is intentionally separate from AttachSQL so
// runtime composition cannot opt in by toggling a boolean.
func (c PostgresCatalogConfig) MigrationStatements() ([]string, error) {
	if c.Mode != PostgresCatalogMigrate {
		return nil, errors.New("MigrationStatements requires PostgresCatalogMigrate mode")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	secret, err := c.DuckLakeSecretSQL()
	if err != nil {
		return nil, err
	}
	attach := fmt.Sprintf("ATTACH 'ducklake:%s' AS %s (METADATA_SCHEMA '%s', AUTOMATIC_MIGRATION true, DATA_INLINING_ROW_LIMIT 0, CREATE_IF_NOT_EXISTS false)", sqlLiteral(c.DuckLakeSecret), quoteCatalogIdentifier(catalogAlias), sqlLiteral(c.MetadataSchema))
	return []string{secret, attach}, nil
}

// Statements returns the secret creation and attachment statements in their
// required order.  It is useful to execute through a target-owned
// CredentialBootstrap/ExecerContext and keeps the DSN out of this package.
func (c PostgresCatalogConfig) Statements() ([]string, error) {
	secret, err := c.DuckLakeSecretSQL()
	if err != nil {
		return nil, err
	}
	attach, err := c.AttachSQL()
	if err != nil {
		return nil, err
	}
	return []string{secret, attach}, nil
}

func quoteCatalogIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// ParseCommitMarker validates the canonical marker returned by DuckLake
// snapshot metadata.
func ParseCommitMarker(raw string) (CommitMarker, error) {
	return catalogartifact.ParseCommitMarker(raw)
}

func decodeCommitMarker(raw string) (CommitMarker, error) {
	return catalogartifact.DecodeCommitMarker([]byte(raw))
}

// SetCommitMarker writes canonical commit_extra_info inside the caller's
// DuckLake transaction.  Callers must invoke this before the transaction
// commits; writing the marker afterward cannot qualify the snapshot.
func SetCommitMarker(ctx context.Context, tx *sql.Tx, marker CommitMarker) error {
	if tx == nil {
		return errors.New("DuckLake commit transaction is nil")
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		return err
	}
	normalized, _ := marker.Normalize()
	_, err = tx.ExecContext(ctx,
		"CALL "+catalogAlias+".set_commit_message(?, ?, extra_info => ?)",
		"LeapView", "materialization "+normalized.AttemptID, canonical,
	)
	return err
}

// SnapshotLookup is the minimal query capability needed to resolve an exact
// committed snapshot.  It intentionally requires both QueryRow and Query so
// duplicate marker matches can be detected rather than choosing by recency.
type SnapshotLookup interface {
	queryRower
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// PhysicalMarkerResolution is the bounded value returned by a marker
// reconciliation. Found remains for compatibility with existing callers;
// Anomaly is non-empty when persistent marker evidence must be quarantined.
// Observed marker digests and snapshot IDs are bounded evidence for that
// quarantine decision and are never used to choose a snapshot.
type PhysicalMarkerResolution struct {
	SnapshotID            int64
	Found                 bool
	Anomaly               PhysicalMarkerAnomaly
	ObservedMarkerDigests PhysicalMarkerObservedDigests
	ObservedSnapshotIDs   PhysicalMarkerObservedSnapshotIDs
}

// PhysicalMarkerResolver is the narrow read-only capability needed by native
// build recovery. It intentionally exposes no materialization, transaction,
// maintenance, or catalog mutation methods.
type PhysicalMarkerResolver interface {
	ResolveCommittedMarker(context.Context, CommitMarker) (PhysicalMarkerResolution, error)
	Close() error
}

// PhysicalMarkerResolverFactory opens a read-only resolver. Implementations
// must create a fresh physical DuckDB session for every resolution call.
type PhysicalMarkerResolverFactory interface {
	OpenReadOnly(context.Context) (PhysicalMarkerResolver, error)
}

// PhysicalMarkerResolverFactoryFunc adapts a constructor for tests and
// embedders while retaining the explicit read-only factory boundary.
type PhysicalMarkerResolverFactoryFunc func(context.Context) (PhysicalMarkerResolver, error)

var _ PhysicalMarkerResolverFactory = PhysicalMarkerResolverFactoryFunc(nil)

func (f PhysicalMarkerResolverFactoryFunc) OpenReadOnly(ctx context.Context) (PhysicalMarkerResolver, error) {
	if f == nil {
		return nil, errors.New("DuckLake physical marker resolver factory is not configured")
	}
	return f(ctx)
}

func (f PhysicalMarkerResolverFactoryFunc) Open(ctx context.Context) (PhysicalMarkerResolver, error) {
	return f.OpenReadOnly(ctx)
}

// ResolveCommittedSnapshot first consults DuckLake's connection-local
// last_committed_snapshot and verifies its persistent marker.  On a fresh
// connection it scans persistent snapshot metadata and compares canonical
// marker identities in-process. It never orders by snapshot ID or treats
// catalog recency or JSON text formatting as build identity.
func ResolveCommittedSnapshot(ctx context.Context, queryer SnapshotLookup, marker CommitMarker) (int64, error) {
	if queryer == nil {
		return 0, errors.New("DuckLake snapshot lookup is nil")
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		return 0, err
	}
	var last sql.NullInt64
	lastErr := queryer.QueryRowContext(ctx, "SELECT id FROM "+catalogAlias+".last_committed_snapshot()").Scan(&last)
	if lastErr != nil && !errors.Is(lastErr, sql.ErrNoRows) {
		return 0, fmt.Errorf("read DuckLake last committed snapshot: %w", lastErr)
	}
	var lastMarkerMatch int64
	if lastErr == nil && last.Valid && last.Int64 > 0 {
		var extra string
		verifyErr := queryer.QueryRowContext(ctx,
			"SELECT CAST(commit_extra_info AS VARCHAR) FROM "+catalogAlias+".snapshots() WHERE snapshot_id = ?", last.Int64).Scan(&extra)
		switch {
		case verifyErr == nil && commitMarkerMatches(extra, canonical):
			lastMarkerMatch = last.Int64
		case verifyErr == nil, errors.Is(verifyErr, sql.ErrNoRows):
			// The connection-local pointer may be absent after restart or may
			// point at another writer's commit; reconcile persistent markers.
		default:
			return 0, fmt.Errorf("verify DuckLake last committed snapshot: %w", verifyErr)
		}
	}

	rows, err := queryer.QueryContext(ctx,
		"SELECT snapshot_id, CAST(commit_extra_info AS VARCHAR) FROM "+catalogAlias+".snapshots() WHERE commit_extra_info IS NOT NULL")
	if err != nil {
		return 0, fmt.Errorf("find DuckLake snapshot for commit marker: %w", err)
	}
	defer rows.Close()
	var exactIDs []int64
	var digestIDs []int64
	var identityIDs []int64
	var exactDigests []string
	var digestDigests []string
	var identityDigests []string
	for rows.Next() {
		var id int64
		var extra string
		if err := rows.Scan(&id, &extra); err != nil {
			return 0, err
		}
		parsed, parseErr := catalogartifact.DecodeCommitMarker([]byte(extra))
		if parseErr == nil {
			parsedCanonical, canonicalErr := parsed.CanonicalJSON()
			if canonicalErr != nil {
				parseErr = canonicalErr
			} else {
				digest := canonicalMarkerDigest(parsedCanonical)
				switch {
				case parsedCanonical == canonical:
					appendBoundedString(&exactDigests, digest, maxObservedMarkerDigests)
					exactIDs = appendBoundedInt64(exactIDs, id, maxObservedSnapshotIDs)
				case parsed.AttemptID == marker.AttemptID && parsed.PhysicalPoolID == marker.PhysicalPoolID && parsed.LeaseEpoch == marker.LeaseEpoch && parsed.DeliveryID == marker.DeliveryID && parsed.GenerationID == marker.GenerationID && (parsed.RequestDigest != marker.RequestDigest || parsed.PlanDigest != marker.PlanDigest):
					appendBoundedString(&digestDigests, digest, maxObservedMarkerDigests)
					digestIDs = appendBoundedInt64(digestIDs, id, maxObservedSnapshotIDs)
				case parsed.AttemptID == marker.AttemptID:
					appendBoundedString(&identityDigests, digest, maxObservedMarkerDigests)
					identityIDs = appendBoundedInt64(identityIDs, id, maxObservedSnapshotIDs)
				}
				continue
			}
		}
		// A malformed marker carrying this attempt identity is still an
		// identity anomaly; it must never collapse to an absent marker.
		if attemptIDFromMarkerJSON(extra) == marker.AttemptID {
			identityIDs = appendBoundedInt64(identityIDs, id, maxObservedSnapshotIDs)
			appendBoundedString(&identityDigests, rawMarkerDigest(extra), maxObservedMarkerDigests)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(exactIDs) > 1 {
		return 0, &markerAnomalyError{kind: PhysicalMarkerAnomalyDuplicate, observedDigests: exactDigests, observedIDs: exactIDs}
	}
	if len(digestIDs) > 0 {
		return 0, &markerAnomalyError{kind: PhysicalMarkerAnomalyDigestMismatch, observedDigests: digestDigests, observedIDs: digestIDs}
	}
	if len(identityIDs) > 0 {
		return 0, &markerAnomalyError{kind: PhysicalMarkerAnomalyIdentityMismatch, observedDigests: identityDigests, observedIDs: identityIDs}
	}
	switch len(exactIDs) {
	case 0:
		if lastMarkerMatch > 0 {
			return lastMarkerMatch, nil
		}
		return 0, ErrCommittedSnapshotNotFound
	case 1:
		if exactIDs[0] <= 0 {
			return 0, errors.New("DuckLake committed snapshot identity is invalid")
		}
		return exactIDs[0], nil
	}
	return 0, ErrCommittedSnapshotNotFound
}

// ResolveCommittedMarker returns a typed found/absent result while retaining
// the resolver's fail-closed ambiguity and catalog-error behavior.
func ResolveCommittedMarker(ctx context.Context, queryer SnapshotLookup, marker CommitMarker) (PhysicalMarkerResolution, error) {
	snapshotID, err := ResolveCommittedSnapshot(ctx, queryer, marker)
	if errors.Is(err, ErrCommittedSnapshotNotFound) {
		return PhysicalMarkerResolution{}, nil
	}
	var anomaly *markerAnomalyError
	if errors.As(err, &anomaly) {
		var digests PhysicalMarkerObservedDigests
		copy(digests[:], anomaly.observedDigests)
		var snapshotIDs PhysicalMarkerObservedSnapshotIDs
		copy(snapshotIDs[:], anomaly.observedIDs)
		return PhysicalMarkerResolution{Anomaly: anomaly.kind, ObservedMarkerDigests: digests, ObservedSnapshotIDs: snapshotIDs}, err
	}
	if err != nil {
		return PhysicalMarkerResolution{}, err
	}
	return PhysicalMarkerResolution{SnapshotID: snapshotID, Found: true}, nil
}

type markerAnomalyError struct {
	kind            PhysicalMarkerAnomaly
	observedDigests []string
	observedIDs     []int64
}

func (e *markerAnomalyError) Error() string {
	switch e.kind {
	case PhysicalMarkerAnomalyDuplicate:
		return ErrCommittedSnapshotAmbiguous.Error()
	case PhysicalMarkerAnomalyDigestMismatch:
		return ErrCommittedSnapshotDigestMismatch.Error()
	case PhysicalMarkerAnomalyIdentityMismatch:
		return ErrCommittedSnapshotIdentityMismatch.Error()
	default:
		return "DuckLake committed snapshot marker anomaly"
	}
}

func (e *markerAnomalyError) Unwrap() error {
	switch e.kind {
	case PhysicalMarkerAnomalyDuplicate:
		return ErrCommittedSnapshotAmbiguous
	case PhysicalMarkerAnomalyDigestMismatch:
		return ErrCommittedSnapshotDigestMismatch
	case PhysicalMarkerAnomalyIdentityMismatch:
		return ErrCommittedSnapshotIdentityMismatch
	default:
		return nil
	}
}

func canonicalMarkerDigest(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func rawMarkerDigest(raw string) string { return canonicalMarkerDigest(raw) }

func appendBoundedInt64(values []int64, value int64, limit int) []int64 {
	if value <= 0 || len(values) >= limit {
		return values
	}
	return append(values, value)
}

func appendBoundedString(values *[]string, value string, limit int) {
	if value == "" || len(*values) >= limit {
		return
	}
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}

func attemptIDFromMarkerJSON(raw string) string {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil {
		return ""
	}
	var attemptID string
	if json.Unmarshal(object["attempt_id"], &attemptID) != nil {
		return ""
	}
	return attemptID
}

func commitMarkerMatches(extra, canonical string) bool {
	if extra == canonical {
		return true
	}
	// DuckLake's metadata driver can return a JSON value with a different
	// whitespace representation.  Parse/re-marshal before rejecting it, while
	// still requiring the exact marker schema and identity.
	parsed, err := decodeCommitMarker(extra)
	if err != nil {
		return false
	}
	normalized, err := parsed.CanonicalJSON()
	return err == nil && normalized == canonical
}
