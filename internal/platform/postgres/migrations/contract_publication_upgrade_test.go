package migrations_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractpublication"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestContractPublicationMigrationUpgradesRevisionSixWithRetainedData(t *testing.T) {
	harness := postgrestest.Start(t)
	owner := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator", Password: "contract-upgrade", Login: true})
	runtime := harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "contract-runtime", Login: true})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	harness.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	harness.GrantRole(t, owner, migrator)
	database := harness.NewDatabase(t, "contract_publication_upgrade")
	harness.GrantDatabase(t, database.Name, owner, "CREATE")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	harness.GrantDatabase(t, database.Name, runtime, "CONNECT")

	ctx := t.Context()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, `
		ALTER DATABASE contract_publication_upgrade OWNER TO leapview_control_owner;
		REVOKE ALL ON SCHEMA public FROM PUBLIC;
		GRANT USAGE, CREATE ON SCHEMA public TO leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	migrationDB, err := sql.Open("pgx", database.URL(migrator))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })
	provider, err := platformmigrations.NewProvider(migrationDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 6); err != nil {
		t.Fatalf("apply retained-data fixture through migration 006: %v", err)
	}
	current, _, err := provider.GetVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current != 6 {
		t.Fatalf("retained-data fixture revision = %d, want 6", current)
	}
	var publicationTableExists bool
	if err := admin.QueryRow(ctx, `SELECT to_regclass('project.contract_publication') IS NOT NULL`).Scan(&publicationTableExists); err != nil {
		t.Fatal(err)
	}
	if publicationTableExists {
		t.Fatal("pre-007 fixture already contains project.contract_publication")
	}

	runtimeDB, err := pgxpool.New(ctx, database.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	digest := func(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
	retained := retainedSourceBlob{
		ProjectID: "project:retained", SecurityDomain: "security:retained",
		Digest: digest("a"), SizeBytes: 128, ObjectKey: "sources/retained/orders.yaml",
		ContentType: "application/yaml", MetadataDigest: digest("b"),
	}
	if _, err := runtimeDB.Exec(ctx, `
		INSERT INTO project.source_blob(
			project_id, storage_security_domain, digest, size_bytes,
			object_key, content_type, metadata_digest
		) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		retained.ProjectID, retained.SecurityDomain, retained.Digest, retained.SizeBytes,
		retained.ObjectKey, retained.ContentType, retained.MetadataDigest); err != nil {
		t.Fatalf("insert pre-007 retained source record: %v", err)
	}

	if _, err := provider.UpTo(ctx, 7); err != nil {
		t.Fatalf("upgrade retained-data fixture through migration 007: %v", err)
	}
	current, _, err = provider.GetVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current != 7 {
		t.Fatalf("upgraded fixture revision = %d, want 7", current)
	}

	var after retainedSourceBlob
	if err := runtimeDB.QueryRow(ctx, `
		SELECT project_id, storage_security_domain, digest, size_bytes,
		       object_key, content_type, metadata_digest
		FROM project.source_blob
		WHERE project_id=$1 AND storage_security_domain=$2 AND digest=$3`,
		retained.ProjectID, retained.SecurityDomain, retained.Digest).
		Scan(&after.ProjectID, &after.SecurityDomain, &after.Digest, &after.SizeBytes,
			&after.ObjectKey, &after.ContentType, &after.MetadataDigest); err != nil {
		t.Fatalf("read retained source record after migration 007: %v", err)
	}
	if !reflect.DeepEqual(after, retained) {
		t.Fatalf("retained source record changed across migration 007: got %#v want %#v", after, retained)
	}

	input := retainedUpgradePublicationInput(t)
	tx, err := runtimeDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	published, err := projectpostgres.New(runtimeDB).PublishContractTx(ctx, tx, input, projectpostgres.PublicationAdmission{
		PolicyContext: contractpublication.PolicyContext{BaselineKind: contractpublication.BaselineGenesis},
		Now:           time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("publish contract after migration 007: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	replayTx, err := runtimeDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := projectpostgres.New(runtimeDB).ReplayContractPublicationTx(ctx, replayTx, published.InstanceID, published.AuthoredID, published.ResourceKind, published.Version)
	_ = replayTx.Rollback(ctx)
	if err != nil {
		t.Fatalf("replay contract after migration 007: %v", err)
	}
	if !contractpublication.EqualContractPublicationContent(published, replayed) {
		t.Fatalf("post-upgrade publication replay changed content: published=%#v replayed=%#v", published, replayed)
	}
}

type retainedSourceBlob struct {
	ProjectID      string
	SecurityDomain string
	Digest         string
	SizeBytes      int64
	ObjectKey      string
	ContentType    string
	MetadataDigest string
}

func retainedUpgradePublicationInput(t *testing.T) contractpublication.ContractPublicationInput {
	t.Helper()
	var source projectcontracts.Source
	const raw = `{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"warehouse","location":{"type":"path","path":"orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{"id":{"datatype":"Integer"}}}}}`
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		t.Fatal(err)
	}
	projection, err := contractprojection.ProjectSource(source, contractprojection.Contract{Version: "1.0.0", Compatibility: "backward"})
	if err != nil {
		t.Fatal(err)
	}
	return contractpublication.ContractPublicationInput{
		InstanceID: "instance:retained-upgrade",
		Projection: projection,
		Validation: contractpublication.ValidationEvidence{
			Version: contractpublication.ValidationEvidenceVersion,
			Checks: []contractpublication.ValidationCheck{{
				Name: "projection", Outcome: contractpublication.ValidationPassed,
				Reference: "fai-632-revision-006-retained-data-upgrade",
			}},
		},
	}
}
