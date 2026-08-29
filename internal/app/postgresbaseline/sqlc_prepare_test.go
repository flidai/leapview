package postgresbaseline_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

const sqlcToolchain = "go1.26.7"

// TestSQLCVetPreparesAgainstBaselinePostgreSQL18 proves that every generated
// PostgreSQL query prepares against the product's clean, current baseline.
// The checked-in sqlc.yaml deliberately contains no credentials; this test
// derives a short-lived config whose URI is supplied only through an
// environment variable.
func TestSQLCVetPreparesAgainstBaselinePostgreSQL18(t *testing.T) {
	if testing.Short() {
		t.Skip("database-backed sqlc prepare is an integration contract")
	}
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	for _, name := range []string{"leapview_control_runtime", "leapview_control_maintenance", "leapview_control_readonly", "leapview_control_backup"} {
		h.EnsureRole(t, postgrestest.Role{Name: name})
	}
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "leapview_sqlc_prepare")
	h.GrantDatabase(t, database.Name, owner, "CREATE")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgresbaseline.Apply(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply clean PostgreSQL baseline: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	root := repositoryRoot(t)
	config := prepareSQLCConfig(t, root)
	cmd := exec.CommandContext(ctx, "go", "run", "github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1", "vet", "--no-remote", "-f", config)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GOTOOLCHAIN="+sqlcToolchain,
		"LEAPVIEW_SQLC_PREPARE_DATABASE_URL="+database.AdminURL(),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("database-backed sqlc vet/prepare failed: %v\n%s", err, output)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve sqlc prepare test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func prepareSQLCConfig(t *testing.T, root string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "sqlc.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatalf("parse sqlc.yaml: %v", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		t.Fatal("sqlc.yaml must contain one mapping document")
	}
	rootNode := document.Content[0]
	sql := mappingValue(rootNode, "sql")
	if sql == nil || sql.Kind != yaml.SequenceNode {
		t.Fatal("sqlc.yaml sql field must be a sequence")
	}
	postgresStanzas := 0
	for _, stanza := range sql.Content {
		if stanza.Kind != yaml.MappingNode || scalarValue(stanza, "engine") != "postgresql" {
			continue
		}
		postgresStanzas++
		if mappingValue(stanza, "database") != nil || mappingValue(stanza, "rules") != nil {
			t.Fatal("checked-in PostgreSQL sqlc stanza unexpectedly contains database credentials or prepare rules")
		}
		stanza.Content = append(stanza.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "database"},
			&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "uri"},
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "${LEAPVIEW_SQLC_PREPARE_DATABASE_URL}"},
			}},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "rules"},
			&yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: "sqlc/db-prepare"},
			}},
		)
	}
	if postgresStanzas == 0 {
		t.Fatal("sqlc.yaml has no PostgreSQL stanzas")
	}

	config, err := os.CreateTemp(root, ".sqlc-prepare-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	configPath := config.Name()
	t.Cleanup(func() {
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove temporary sqlc prepare config: %v", err)
		}
	})
	encoder := yaml.NewEncoder(config)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		_ = config.Close()
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		_ = config.Close()
		t.Fatal(err)
	}
	if err := config.Close(); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func scalarValue(mapping *yaml.Node, key string) string {
	value := mappingValue(mapping, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}
	return value.Value
}

func TestPrepareSQLCConfigAddsDatabaseRuleOnlyToPostgreSQL(t *testing.T) {
	root := repositoryRoot(t)
	path := prepareSQLCConfig(t, root)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	sql := mappingValue(document.Content[0], "sql")
	postgres, other := 0, 0
	for _, stanza := range sql.Content {
		prepared := mappingValue(stanza, "database") != nil && mappingValue(stanza, "rules") != nil
		switch scalarValue(stanza, "engine") {
		case "postgresql":
			postgres++
			if !prepared {
				t.Error("PostgreSQL stanza lacks database-backed prepare configuration")
			}
		default:
			other++
			if prepared {
				t.Error("non-PostgreSQL stanza received database-backed prepare configuration")
			}
		}
	}
	if postgres == 0 || other == 0 {
		t.Fatalf("config coverage PostgreSQL=%d other=%d", postgres, other)
	}
}
