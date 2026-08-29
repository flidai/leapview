package ducklake

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func validPostgresCatalogConfig(mode PostgresCatalogMode) PostgresCatalogConfig {
	c := PostgresCatalogConfig{
		DuckLakeSecret: "leapview_lake", PostgresSecret: "leapview_pg",
		MetadataSchema: "leapview_catalog",
		Mode:           mode,
	}
	if mode == PostgresCatalogInitialize {
		c.DataPath = "s3://bucket/lake"
	}
	return c
}

func TestPostgresCatalogSQLReferencesSecretAndPinsOptions(t *testing.T) {
	initialize := validPostgresCatalogConfig(PostgresCatalogInitialize)
	secret, err := initialize.DuckLakeSecretSQL()
	if err != nil {
		t.Fatal(err)
	}
	if want := "CREATE OR REPLACE TEMPORARY SECRET \"leapview_lake\" (TYPE ducklake, METADATA_PATH '', METADATA_PARAMETERS MAP {'TYPE': 'postgres', 'SECRET': 'leapview_pg'})"; secret != want {
		t.Fatalf("secret SQL = %q, want %q", secret, want)
	}
	if strings.Contains(strings.ToLower(secret), "postgres://") || strings.Contains(strings.ToLower(secret), "password") {
		t.Fatalf("secret SQL contains connection credentials: %q", secret)
	}
	attach, err := initialize.AttachSQL()
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"METADATA_SCHEMA 'leapview_catalog'", "AUTOMATIC_MIGRATION false",
		"DATA_PATH 's3://bucket/lake'", "CREATE_IF_NOT_EXISTS true",
	} {
		if !strings.Contains(attach, required) {
			t.Fatalf("attach SQL missing %q: %s", required, attach)
		}
	}
	if strings.Contains(attach, "READ_ONLY") || strings.Contains(attach, "SNAPSHOT_VERSION") {
		t.Fatalf("initialization attach unexpectedly pins serving options: %s", attach)
	}
}

func TestPostgresCatalogServingRequiresExactReadOnlySnapshot(t *testing.T) {
	serving := validPostgresCatalogConfig(PostgresCatalogServing)
	serving.SnapshotVersion = 42
	attach, err := serving.AttachSQL()
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"METADATA_SCHEMA 'leapview_catalog'", "AUTOMATIC_MIGRATION false",
		"READ_ONLY", "CREATE_IF_NOT_EXISTS false", "SNAPSHOT_VERSION 42",
	} {
		if !strings.Contains(attach, required) {
			t.Fatalf("serving attach missing %q: %s", required, attach)
		}
	}
	for _, invalid := range []PostgresCatalogConfig{
		func() PostgresCatalogConfig { c := serving; c.SnapshotVersion = 0; return c }(),
		func() PostgresCatalogConfig { c := serving; c.Mode = PostgresCatalogInitialize; return c }(),
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid serving config accepted: %#v", invalid)
		}
	}
	writer := validPostgresCatalogConfig(PostgresCatalogWriter)
	writer.SnapshotVersion = 1
	if err := writer.Validate(); err == nil {
		t.Fatal("writer accepted SNAPSHOT_VERSION")
	}
	writer = validPostgresCatalogConfig(PostgresCatalogWriter)
	writer.DataPath = "s3://caller/path"
	if err := writer.Validate(); err == nil {
		t.Fatal("writer accepted caller DATA_PATH override")
	}
}

func TestPostgresCatalogValidationRejectsUnsafeIdentifiersAndMissingSchema(t *testing.T) {
	cases := []PostgresCatalogConfig{
		func() PostgresCatalogConfig {
			c := validPostgresCatalogConfig(PostgresCatalogWriter)
			c.DuckLakeSecret = "lake;DROP"
			return c
		}(),
		func() PostgresCatalogConfig {
			c := validPostgresCatalogConfig(PostgresCatalogWriter)
			c.MetadataSchema = ""
			return c
		}(),
		func() PostgresCatalogConfig {
			c := validPostgresCatalogConfig(PostgresCatalogWriter)
			c.Mode = "unknown"
			return c
		}(),
	}
	for _, tc := range cases {
		if err := tc.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %#v", tc)
		}
	}
}

func TestCommitMarkerCanonicalJSONAndBounds(t *testing.T) {
	marker := CommitMarker{
		SchemaVersion:  CommitMarkerSchemaVersion,
		DeploymentID:   "deployment-1",
		GenerationID:   "generation-2",
		RefreshID:      "refresh-3",
		AttemptID:      "attempt-4",
		LeaseEpoch:     7,
		FencingToken:   "fence-7",
		PlanDigest:     "sha256:" + strings.Repeat("a", 64),
		Project:        "project:demo",
		Environment:    "production",
		PhysicalPoolID: "pool-1",
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"deployment_id":"deployment-1","generation_id":"generation-2","refresh_id":"refresh-3","attempt_id":"attempt-4","lease_epoch":7,"fencing_token":"fence-7","plan_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","project":"project:demo","environment":"production","physical_pool_id":"pool-1"}`
	if canonical != want {
		t.Fatalf("canonical marker = %s, want %s", canonical, want)
	}
	parsed, err := ParseCommitMarker(canonical)
	if err != nil || parsed != marker {
		t.Fatalf("parse canonical marker = %#v, %v", parsed, err)
	}
	if _, err := ParseCommitMarker(canonical + " "); err == nil {
		t.Fatal("non-canonical marker accepted")
	}
	tooLong := marker
	tooLong.Project = strings.Repeat("x", MaxCommitMarkerFieldBytes+1)
	if _, err := tooLong.CanonicalJSON(); err == nil {
		t.Fatal("oversized marker field accepted")
	}
	missing := marker
	missing.LeaseEpoch = 0
	if _, err := missing.CanonicalJSON(); err == nil {
		t.Fatal("zero lease epoch accepted")
	}
	invalidDigest := marker
	invalidDigest.PlanDigest = "sha256:plan"
	if _, err := invalidDigest.CanonicalJSON(); err == nil {
		t.Fatal("non-sha256 plan digest accepted")
	}
}

func TestResolveCommittedSnapshotUsesLastCommitThenExactMarker(t *testing.T) {
	marker := CommitMarker{
		SchemaVersion: CommitMarkerSchemaVersion, DeploymentID: "deployment-1",
		GenerationID: "generation-2", RefreshID: "refresh-3", AttemptID: "attempt-4",
		LeaseEpoch: 7, PlanDigest: "sha256:" + strings.Repeat("a", 64), Project: "project:demo",
		Environment: "production", PhysicalPoolID: "pool-1",
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	db := openMarkerLookupDB(t, markerLookupState{lastID: 19, lastExtra: canonical})
	defer db.Close()
	lookup, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lookup.Close()
	got, err := ResolveCommittedSnapshot(context.Background(), lookup, marker)
	if err != nil || got != 19 {
		t.Fatalf("last committed snapshot = %d, %v; want 19", got, err)
	}
}

func TestResolveCommittedSnapshotRejectsDuplicatePersistentMarkers(t *testing.T) {
	marker := CommitMarker{
		SchemaVersion: CommitMarkerSchemaVersion, DeploymentID: "deployment-1",
		GenerationID: "generation-2", RefreshID: "refresh-3", AttemptID: "attempt-4",
		LeaseEpoch: 7, PlanDigest: "sha256:" + strings.Repeat("a", 64), Project: "project:demo",
		Environment: "production", PhysicalPoolID: "pool-1",
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	db := openMarkerLookupDB(t, markerLookupState{fallback: [][2]driver.Value{{int64(10), canonical}, {int64(11), canonical}}})
	defer db.Close()
	lookup, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lookup.Close()
	if _, err := ResolveCommittedSnapshot(context.Background(), lookup, marker); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("duplicate marker resolution error = %v", err)
	}
}

func TestResolveCommittedSnapshotPropagatesCatalogProbeError(t *testing.T) {
	marker := CommitMarker{
		SchemaVersion: CommitMarkerSchemaVersion, DeploymentID: "deployment-1",
		GenerationID: "generation-2", RefreshID: "refresh-3", AttemptID: "attempt-4",
		LeaseEpoch: 7, PlanDigest: "sha256:" + strings.Repeat("a", 64), Project: "project:demo",
		Environment: "production", PhysicalPoolID: "pool-1",
	}
	db := openMarkerLookupDB(t, markerLookupState{lastErr: errors.New("catalog unavailable")})
	defer db.Close()
	lookup, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lookup.Close()
	if _, err := ResolveCommittedSnapshot(context.Background(), lookup, marker); err == nil || !strings.Contains(err.Error(), "catalog unavailable") {
		t.Fatalf("probe error = %v, want catalog error", err)
	}
}

// markerLookupState and the tiny driver below let resolver tests exercise the
// database/sql contract without a PostgreSQL or DuckLake process.
type markerLookupState struct {
	lastID    int64
	lastExtra string
	fallback  [][2]driver.Value
	lastErr   error
}

var markerLookupDriverID atomic.Uint64

type markerLookupDriver struct{ state markerLookupState }

type markerLookupConn struct{ state markerLookupState }

func openMarkerLookupDB(t *testing.T, state markerLookupState) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("leapview_ducklake_marker_%d", markerLookupDriverID.Add(1))
	sql.Register(name, markerLookupDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func (d markerLookupDriver) Open(string) (driver.Conn, error) {
	return &markerLookupConn{state: d.state}, nil
}
func (c *markerLookupConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not used")
}
func (c *markerLookupConn) Close() error { return nil }
func (c *markerLookupConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not used")
}

func (c *markerLookupConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "last_committed_snapshot"):
		if c.state.lastErr != nil {
			return nil, c.state.lastErr
		}
		if c.state.lastID == 0 {
			return &markerRows{columns: []string{"id"}}, nil
		}
		return &markerRows{columns: []string{"id"}, values: [][]driver.Value{{c.state.lastID}}}, nil
	case strings.Contains(query, "WHERE snapshot_id"):
		return &markerRows{columns: []string{"commit_extra_info"}, values: [][]driver.Value{{c.state.lastExtra}}}, nil
	default:
		values := make([][]driver.Value, 0, len(c.state.fallback))
		for _, row := range c.state.fallback {
			values = append(values, []driver.Value{row[0], row[1]})
		}
		return &markerRows{columns: []string{"snapshot_id", "commit_extra_info"}, values: values}, nil
	}
}

type markerRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *markerRows) Columns() []string { return r.columns }
func (r *markerRows) Close() error      { return nil }
func (r *markerRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}
