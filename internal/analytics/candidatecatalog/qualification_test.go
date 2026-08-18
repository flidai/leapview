//go:build duckdb_arrow

package candidatecatalog

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

func TestNormalizeAndQualifyRetainsOneSnapshotAndProbesClosure(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, t.TempDir())
	working, err := Open(ctx, testRequest(contract, t.TempDir()))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}

	// Set the rewrite threshold before producing a delete file, then create
	// history and a current data+delete closure.
	if _, err := working.Commit(ctx, "create", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.orders(id INTEGER, label VARCHAR); INSERT INTO model.orders VALUES (1, 'one'), (2, 'two')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := working.Exec(ctx, "CALL ducklake_set_option('lake', 'rewrite_delete_threshold', 1.0, schema => 'model', table_name => 'orders')"); err != nil {
		t.Fatal(err)
	}
	if _, err := working.Commit(ctx, "delete", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM model.orders WHERE id = 1")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	before, err := working.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) < 2 {
		t.Fatalf("snapshots before normalization = %#v, want inherited/intermediate history", before)
	}

	var checked bool
	qualified, err := NormalizeAndQualify(ctx, working, QualificationRequest{
		ExpectedTables: []LogicalRelation{{Schema: "model", Table: "orders"}},
		PolicyDigest:   "sha256:" + strings.Repeat("1", 64), ReviewerPolicyDigest: "sha256:" + strings.Repeat("2", 64),
		Expected: QualificationExpectations{GraphDigest: "sha256:" + strings.Repeat("3", 64)},
		Policy: func(_ context.Context, input QualificationInput) error {
			checked = true
			if input.Record.CurrentSnapshot == 0 || input.Record.Closure.Digest == "" || input.Record.CatalogDigest == "" || input.GraphDigest != "sha256:"+strings.Repeat("3", 64) || input.Record.GraphDigest != input.GraphDigest {
				return errors.New("policy did not receive exact catalog evidence")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("qualification policy was not called")
	}
	defer qualified.Remove()
	if len(qualified.Record.Closure.DataFiles) == 0 || len(qualified.Record.Closure.DeleteFiles) == 0 {
		t.Fatalf("closure = %#v, want data and delete files", qualified.Record.Closure)
	}
	if got := len(qualified.Record.Closure.Tables); got != 1 {
		t.Fatalf("visible tables = %d, want one base table", got)
	}

	preview, err := OpenQualifiedPreview(ctx, qualified)
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	if err := preview.env.Exec(ctx, "CREATE TABLE model.preview_mutation(value INTEGER)"); !errors.Is(err, ducklake.ErrReadOnlyEnvironment) {
		t.Fatalf("preview mutation error = %v, want read-only rejection", err)
	}
	after, err := preview.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != qualified.Record.CurrentSnapshot {
		t.Fatalf("preview snapshots = %#v, want one current snapshot", after)
	}
	rows, err := preview.Query(ctx, semanticquery.Plan{SQL: "SELECT count(*) AS count FROM model.orders", Columns: []string{"count"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0]["count"].(int64); got != 1 {
		t.Fatalf("preview row count = %d, want one", got)
	}
}

func TestNormalizeRejectsLiveInlineDataWithoutRepair(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, t.TempDir())
	working, err := Open(ctx, testRequest(contract, t.TempDir()))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	if _, err := working.Commit(ctx, "inline-create", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.inline_reject(id INTEGER, label VARCHAR)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := working.Exec(ctx, "CALL ducklake_set_option('lake', 'data_inlining_row_limit', 100, schema => 'model', table_name => 'inline_reject')"); err != nil {
		t.Fatal(err)
	}
	if _, err := working.Commit(ctx, "inline-insert", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO model.inline_reject VALUES (1, 'reject')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := working.Exec(ctx, "CALL ducklake_set_option('lake', 'data_inlining_row_limit', 0, schema => 'model', table_name => 'inline_reject')"); err != nil {
		t.Fatal(err)
	}
	before, err := working.DataInliningPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := before.ValidateZero(); err != nil {
		t.Fatalf("persisted inlining policy = %#v, want explicit zero options: %v", before, err)
	}
	if _, err := working.Normalize(ctx); !errors.Is(err, ducklake.ErrLiveInlineData) {
		t.Fatalf("Normalize() error = %v, want live inline data rejection", err)
	}
	after, err := working.DataInliningPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := after.ValidateZero(); err != nil {
		t.Fatalf("normalization changed inlining policy after rejection: %#v: %v", after, err)
	}
}

func TestNormalizeRejectsNonZeroInliningPolicyWithoutRepair(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, t.TempDir())
	working, err := Open(ctx, testRequest(contract, t.TempDir()))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	if _, err := working.Commit(ctx, "policy-create", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.policy_target(id INTEGER)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CALL ducklake_set_option('lake', 'data_inlining_row_limit', 101)",
		"CALL ducklake_set_option('lake', 'data_inlining_row_limit', 202, schema => 'model')",
		"CALL ducklake_set_option('lake', 'data_inlining_row_limit', 303, schema => 'model', table_name => 'policy_target')",
	} {
		if err := working.Exec(ctx, statement); err != nil {
			t.Fatalf("persist inlining policy (%s): %v", statement, err)
		}
	}
	before, err := working.DataInliningPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := before.ValidateZero(); !errors.Is(err, ducklake.ErrInliningNotDisabled) {
		t.Fatalf("persisted inlining policy = %#v, validation error = %v", before, err)
	}
	if _, err := working.Normalize(ctx); !errors.Is(err, ErrNormalizationFailed) || !errors.Is(err, ducklake.ErrInliningNotDisabled) {
		t.Fatalf("Normalize() error = %v, want normalization and nonzero-policy rejection", err)
	}
	after, err := working.DataInliningPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("normalization rewrote persisted inlining policy: before=%#v after=%#v", before, after)
	}
}

func TestNormalizeRejectsLiveInlineDeletesWithoutRepair(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, t.TempDir())
	working, err := Open(ctx, testRequest(contract, t.TempDir()))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer working.Close()
	if _, err := working.Commit(ctx, "delete-create", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.inline_delete AS SELECT range AS id FROM range(0, 2000)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CALL ducklake_set_option('lake', 'data_inlining_row_limit', 100, schema => 'model', table_name => 'inline_delete')",
		"CALL ducklake_set_option('lake', 'rewrite_delete_threshold', 1.0, schema => 'model', table_name => 'inline_delete')",
	} {
		if err := working.Exec(ctx, statement); err != nil {
			t.Fatalf("persist delete policy (%s): %v", statement, err)
		}
	}
	if _, err := working.Commit(ctx, "delete-update", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE model.inline_delete SET id = 3001 WHERE id = 1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := working.Exec(ctx, "CALL ducklake_set_option('lake', 'data_inlining_row_limit', 0, schema => 'model', table_name => 'inline_delete')"); err != nil {
		t.Fatal(err)
	}
	if _, err := working.Normalize(ctx); !errors.Is(err, ducklake.ErrLiveInlineData) {
		t.Fatalf("Normalize() error = %v, want live delete rejection", err)
	}
}

func TestQualificationPolicyAndExpectedSetValidation(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	if err := validateQualificationRequest(QualificationRequest{Policy: func(context.Context, QualificationInput) error { return nil }, PolicyDigest: valid}); !errors.Is(err, ErrIncompleteQualification) {
		t.Fatalf("missing reviewer digest error = %v, want incomplete qualification", err)
	}
	checks := QualificationChecks{LogicalGraph: func(context.Context, QualificationInput) error { return nil }, Schema: func(context.Context, QualificationInput) error { return nil }, Contracts: func(context.Context, QualificationInput) error { return nil }, Tests: func(context.Context, QualificationInput) error { return nil }, Audits: func(context.Context, QualificationInput) error { return nil }, DataDiffs: func(context.Context, QualificationInput) error { return nil }, ReviewerAuthorization: func(context.Context, QualificationInput) error { return nil }}
	if err := validateQualificationRequest(QualificationRequest{Checks: checks, ReviewerPolicyDigest: valid}); err != nil {
		t.Fatalf("complete named checks rejected: %v", err)
	}
	if err := validateExpectations([]CatalogTable{{Schema: "model", Table: "a"}, {Schema: "model", Table: "b"}}, QualificationRequest{ExpectedTables: []LogicalRelation{{Schema: "model", Table: "a"}}}); !errors.Is(err, ErrUnexpectedRelation) {
		t.Fatalf("extra relation error = %v, want exact-set rejection", err)
	}
	if err := validateExpectations([]CatalogTable{{Schema: "model", Table: "a"}, {Schema: "model", Table: "b"}}, QualificationRequest{ExpectedTables: []LogicalRelation{{Schema: "model", Table: "a"}}, AllowAdditional: true}); err != nil {
		t.Fatalf("AllowAdditional rejected: %v", err)
	}
}

func TestProbeClosureCanonicalizesAndDeduplicatesReferences(t *testing.T) {
	root := t.TempDir()
	contract := testPoolContract(t, filepath.Join(root, "data"))
	dataPath, err := contract.Pool.DataPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataPath, 0o700); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dataPath, "one.parquet")
	if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	closure := CatalogClosure{Files: []FileReference{{Kind: ducklake.DataFile, Reference: "one.parquet"}, {Kind: ducklake.DataFile, Reference: full}, {Kind: ducklake.DataFile, Reference: "./one.parquet"}}}
	if err := probeClosure(context.Background(), &closure, nil, contract); err != nil {
		t.Fatal(err)
	}
	if len(closure.Files) != 1 || len(closure.DataFiles) != 1 || closure.Digest == "" {
		t.Fatalf("canonical closure = %#v, want one deduplicated file", closure)
	}
}

func TestRemotePoolRequiresTargetObjectProbe(t *testing.T) {
	tuple := physicalpool.Compatibility{DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: "s3://bucket/base", StorageNamespace: "tenant", IsolationBoundary: "test", RetentionAuthority: "test", Compatibility: tuple})
	if err != nil {
		t.Fatal(err)
	}
	contract := &ducklake.PoolContract{Pool: pool, Tuple: tuple}
	closure := CatalogClosure{Files: []FileReference{{Kind: ducklake.DataFile, Reference: "s3://bucket/base/tenant/file.parquet"}}}
	if err := probeClosure(context.Background(), &closure, nil, contract); !errors.Is(err, ErrObjectProbe) {
		t.Fatalf("remote nil-probe error = %v, want object probe requirement", err)
	}
}

func TestQualificationFailureRemovesWorkingStaging(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, t.TempDir())
	working, err := Open(ctx, testRequest(contract, t.TempDir()))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := working.Commit(ctx, "failure", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.failure(value INTEGER); INSERT INTO model.failure VALUES (1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	staging := working.StagingPath()
	qualified, err := NormalizeAndQualify(ctx, working, QualificationRequest{
		PolicyDigest: "sha256:" + strings.Repeat("a", 64), ReviewerPolicyDigest: "sha256:" + strings.Repeat("b", 64),
		Policy: func(context.Context, QualificationInput) error { return errors.New("reviewer denied") },
	})
	if qualified.state != nil || err == nil || !errors.Is(err, ErrQualificationFailed) {
		t.Fatalf("policy failure result = %#v, %v; want no qualified catalog", qualified, err)
	}
	if _, statErr := os.Stat(staging); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed qualification staging remains: %v", statErr)
	}
}

func TestHistoricalFileReferenceIsAbsentFromCurrentUnionBeforeNormalization(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, t.TempDir())
	working, err := Open(ctx, testRequest(contract, t.TempDir()))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := working.Commit(ctx, "history-one", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.history(value INTEGER); INSERT INTO model.history VALUES (1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	old, err := working.CurrentFileSet(ctx, "history", "model", "history")
	if err != nil || len(old.DataFiles) == 0 {
		t.Fatalf("old file set = %#v, %v", old, err)
	}
	if _, err := working.Commit(ctx, "history-two", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE OR REPLACE TABLE model.history AS SELECT 2 AS value")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	current, err := working.CurrentFileSet(ctx, "history", "model", "history")
	if err != nil {
		t.Fatal(err)
	}
	for _, previous := range old.DataFiles {
		for _, now := range current.DataFiles {
			if previous == now {
				t.Fatalf("historical file %q remained in current union", previous)
			}
		}
	}
	_ = working.Close()
}

func TestNormalizeAndQualifyObjectProbeReceivesCanonicalReferences(t *testing.T) {
	ctx := context.Background()
	contract := testPoolContract(t, t.TempDir())
	working, err := Open(ctx, testRequest(contract, t.TempDir()))
	if extensionUnavailableForTest(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := working.Commit(ctx, "create", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS model; CREATE TABLE model.metrics(value INTEGER); INSERT INTO model.metrics VALUES (1)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var refs []string
	probe := ObjectProbeFunc(func(_ context.Context, reference string) (io.ReadCloser, error) {
		refs = append(refs, reference)
		return io.NopCloser(strings.NewReader("probe")), nil
	})
	qualified, err := NormalizeAndQualify(ctx, working, QualificationRequest{Probe: probe, PolicyDigest: "sha256:" + strings.Repeat("1", 64), ReviewerPolicyDigest: "sha256:" + strings.Repeat("2", 64), Policy: func(_ context.Context, _ QualificationInput) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer qualified.Remove()
	if len(refs) == 0 {
		t.Fatal("object probe was not used")
	}
	dataPath, err := contract.Pool.DataPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range refs {
		if !filepathWithin(dataPath, reference) {
			t.Fatalf("probe reference %q escaped pool %q", reference, dataPath)
		}
	}
}
