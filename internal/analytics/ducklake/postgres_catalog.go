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
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	// CommitMarkerSchemaVersion is the version of the persistent
	// commit_extra_info marker written by LeapView materialization.
	CommitMarkerSchemaVersion = 1

	// MaxCommitMarkerBytes bounds the JSON document stored in DuckLake's
	// commit_extra_info column.  Markers are identity evidence, not a general
	// metadata channel.
	MaxCommitMarkerBytes = 4096
	// MaxCommitMarkerFieldBytes prevents one identity from consuming the
	// complete bounded metadata document.
	MaxCommitMarkerFieldBytes = 512
)

var catalogIdentifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	// ErrCommittedSnapshotNotFound means no persistent snapshot carries the
	// exact marker. It is distinct from an ambiguous or malformed catalog so
	// restart reconciliation can require positive session termination evidence.
	ErrCommittedSnapshotNotFound  = errors.New("DuckLake committed snapshot identity was not found")
	ErrCommittedSnapshotAmbiguous = errors.New("multiple DuckLake snapshots match the commit marker")
)

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
)

// PostgresCatalogConfig describes one DuckLake PostgreSQL metadata attach.
// PostgreSQL connection details are supplied by a separately provisioned
// DuckDB postgres secret.  No DSN or password is accepted by this type.
type PostgresCatalogConfig struct {
	DuckLakeSecret  string
	PostgresSecret  string
	MetadataSchema  string
	DataPath        string
	Mode            PostgresCatalogMode
	SnapshotVersion int64
}

// Validate checks names, mode invariants, and required storage identity.
func (c PostgresCatalogConfig) Validate() error {
	if err := validateCatalogIdentifier("DuckLake secret", c.DuckLakeSecret); err != nil {
		return err
	}
	if err := validateCatalogIdentifier("PostgreSQL secret", c.PostgresSecret); err != nil {
		return err
	}
	if err := validateCatalogIdentifier("metadata schema", c.MetadataSchema); err != nil {
		return err
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
	default:
		return fmt.Errorf("unsupported PostgreSQL DuckLake catalog mode %q", c.Mode)
	}
	return nil
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
	options := []string{
		fmt.Sprintf("METADATA_SCHEMA '%s'", sqlLiteral(c.MetadataSchema)),
		"AUTOMATIC_MIGRATION false",
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
	}
	return fmt.Sprintf("ATTACH 'ducklake:%s' AS %s (%s)", sqlLiteral(c.DuckLakeSecret), quoteCatalogIdentifier(catalogAlias), strings.Join(options, ", ")), nil
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

// CommitMarker is the durable identity written to DuckLake commit metadata.
// Fields are deliberately relational-style scalar identities rather than an
// unbounded metadata map.
type CommitMarker struct {
	SchemaVersion  int    `json:"schema_version"`
	DeliveryID     string `json:"delivery_id"`
	GenerationID   string `json:"generation_id"`
	AttemptID      string `json:"attempt_id"`
	LeaseEpoch     int64  `json:"lease_epoch"`
	FencingToken   string `json:"fencing_token,omitempty"`
	RequestDigest  string `json:"request_digest"`
	PlanDigest     string `json:"plan_digest"`
	Project        string `json:"project"`
	Environment    string `json:"environment"`
	PhysicalPoolID string `json:"physical_pool_id"`
}

// Normalize validates the marker without mutating the receiver.  It is also
// used by snapshot reconciliation.
func (m CommitMarker) Normalize() (CommitMarker, error) {
	for name, value := range map[string]string{
		"delivery_id": m.DeliveryID, "generation_id": m.GenerationID,
		"attempt_id":     m.AttemptID,
		"request_digest": m.RequestDigest,
		"plan_digest":    m.PlanDigest, "project": m.Project,
		"environment": m.Environment, "physical_pool_id": m.PhysicalPoolID,
	} {
		if err := validateMarkerValue(name, value); err != nil {
			return CommitMarker{}, err
		}
	}
	if m.LeaseEpoch <= 0 {
		return CommitMarker{}, errors.New("commit marker lease_epoch must be positive")
	}
	if err := validatePlanDigest(m.PlanDigest); err != nil {
		return CommitMarker{}, err
	}
	if err := validatePlanDigest(m.RequestDigest); err != nil {
		return CommitMarker{}, fmt.Errorf("commit marker request_digest: %w", err)
	}
	if strings.TrimSpace(m.FencingToken) != "" {
		if err := validateMarkerValue("fencing_token", m.FencingToken); err != nil {
			return CommitMarker{}, err
		}
	}
	if m.SchemaVersion != CommitMarkerSchemaVersion {
		return CommitMarker{}, fmt.Errorf("unsupported commit marker schema version %d", m.SchemaVersion)
	}
	return m, nil
}

func validatePlanDigest(value string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return errors.New("commit marker plan_digest must be a sha256 digest")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, prefix)); err != nil {
		return fmt.Errorf("commit marker plan_digest is not valid sha256: %w", err)
	}
	return nil
}

func validateMarkerValue(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("commit marker %s is required", name)
	}
	if value != strings.TrimSpace(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("commit marker %s is not normalized", name)
	}
	if len(value) > MaxCommitMarkerFieldBytes {
		return fmt.Errorf("commit marker %s exceeds %d bytes", name, MaxCommitMarkerFieldBytes)
	}
	return nil
}

// CanonicalJSON returns the stable JSON representation used in
// commit_extra_info.  Struct field order is intentional and stable; no map is
// used for identity fields.
func (m CommitMarker) CanonicalJSON() (string, error) {
	normalized, err := m.Normalize()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal DuckLake commit marker: %w", err)
	}
	if len(encoded) > MaxCommitMarkerBytes {
		return "", fmt.Errorf("DuckLake commit marker is %d bytes, maximum is %d", len(encoded), MaxCommitMarkerBytes)
	}
	return string(encoded), nil
}

// ParseCommitMarker validates and normalizes a marker read from
// commit_extra_info.  Unknown fields are rejected to avoid silently accepting
// an unreviewed marker schema.
func ParseCommitMarker(raw string) (CommitMarker, error) {
	if strings.TrimSpace(raw) == "" {
		return CommitMarker{}, errors.New("DuckLake commit marker is empty")
	}
	marker, err := decodeCommitMarker(raw)
	if err != nil {
		return CommitMarker{}, err
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		return CommitMarker{}, err
	}
	if canonical != raw {
		return CommitMarker{}, errors.New("DuckLake commit marker is not canonical JSON")
	}
	return marker.Normalize()
}

func decodeCommitMarker(raw string) (CommitMarker, error) {
	var marker CommitMarker
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return CommitMarker{}, fmt.Errorf("decode DuckLake commit marker: %w", err)
	}
	// Reject trailing JSON values as well as trailing text. CanonicalJSON below
	// catches whitespace for ParseCommitMarker; this check keeps the matcher
	// from accepting concatenated marker documents.
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CommitMarker{}, errors.New("DuckLake commit marker contains trailing data")
	}
	return marker.Normalize()
}

// SetCommitMarker writes canonical commit_extra_info inside the caller's
// DuckLake transaction.  Callers must invoke this before the transaction
// commits; writing the marker afterward cannot qualify the snapshot.
func SetCommitMarker(ctx context.Context, tx *sql.Tx, marker CommitMarker) error {
	if tx == nil {
		return errors.New("DuckLake commit transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
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

// ResolveCommittedSnapshot first consults DuckLake's connection-local
// last_committed_snapshot and verifies its persistent marker.  On a fresh
// connection it scans snapshots for the exact canonical marker.  It never
// orders by snapshot ID or treats catalog recency as build identity.
func ResolveCommittedSnapshot(ctx context.Context, queryer SnapshotLookup, marker CommitMarker) (int64, error) {
	if queryer == nil {
		return 0, errors.New("DuckLake snapshot lookup is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		return 0, err
	}
	var last sql.NullInt64
	lastErr := queryer.QueryRowContext(ctx, "SELECT id FROM "+catalogAlias+".last_committed_snapshot()").Scan(&last)
	switch {
	case lastErr == nil && last.Valid && last.Int64 > 0:
		var extra string
		verifyErr := queryer.QueryRowContext(ctx,
			"SELECT CAST(commit_extra_info AS VARCHAR) FROM "+catalogAlias+".snapshots() WHERE snapshot_id = ?", last.Int64).Scan(&extra)
		switch {
		case verifyErr == nil && commitMarkerMatches(extra, canonical):
			return last.Int64, nil
		case verifyErr == nil, errors.Is(verifyErr, sql.ErrNoRows):
			// The connection-local pointer may be absent after restart or may
			// point at another writer's commit; reconcile persistent markers.
		default:
			return 0, fmt.Errorf("verify DuckLake last committed snapshot: %w", verifyErr)
		}
	case lastErr == nil, errors.Is(lastErr, sql.ErrNoRows):
		// NULL/empty connection-local evidence requires persistent lookup.
	default:
		return 0, fmt.Errorf("read DuckLake last committed snapshot: %w", lastErr)
	}

	rows, err := queryer.QueryContext(ctx,
		"SELECT snapshot_id, CAST(commit_extra_info AS VARCHAR) FROM "+catalogAlias+".snapshots() WHERE CAST(commit_extra_info AS VARCHAR) = ?", canonical)
	if err != nil {
		return 0, fmt.Errorf("find DuckLake snapshot for commit marker: %w", err)
	}
	defer rows.Close()
	var found int64
	count := 0
	for rows.Next() {
		var id int64
		var extra string
		if err := rows.Scan(&id, &extra); err != nil {
			return 0, err
		}
		if !commitMarkerMatches(extra, canonical) {
			continue
		}
		found = id
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	switch count {
	case 0:
		return 0, ErrCommittedSnapshotNotFound
	case 1:
		if found <= 0 {
			return 0, errors.New("DuckLake committed snapshot identity is invalid")
		}
		return found, nil
	default:
		return 0, ErrCommittedSnapshotAmbiguous
	}
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
