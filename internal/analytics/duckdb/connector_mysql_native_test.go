//go:build integration

package duckdb

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

//go:embed testdata/mysql/Dockerfile
var nativeMySQLDockerfile []byte

func TestNativeMySQLSourceReadDenialAndRecovery(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0600, Size: int64(len(nativeMySQLDockerfile))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(nativeMySQLDockerfile); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	const password = "owned-native-mysql-fixture"
	// The entrypoint first exposes a temporary socket server before creating
	// fixture_reader. Readiness must prove the final TCP server accepts the
	// same credential and database used by the native source attachment.
	ready := wait.ForExec([]string{"env", "MYSQL_PWD=" + password, "mysql", "--protocol=TCP", "--host=127.0.0.1", "--user=fixture_reader", "fixtures", "--execute=SELECT 1;"}).WithStartupTimeout(90 * time.Second)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{Started: true, ContainerRequest: testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{ContextArchive: bytes.NewReader(archive.Bytes()), Repo: "leapview-test/mysql", Tag: "26.7-v1", KeepImage: true},
		Env:            map[string]string{"MYSQL_ROOT_PASSWORD": "owned-native-mysql-root", "MYSQL_DATABASE": "fixtures", "MYSQL_USER": "fixture_reader", "MYSQL_PASSWORD": password},
		ExposedPorts:   []string{"3306/tcp"}, WaitingFor: ready,
	}})
	if container != nil {
		testcontainers.CleanupContainer(t, container)
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			logContext, cancelLogs := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelLogs()
			logs, err := container.Logs(logContext)
			if err != nil {
				t.Logf("read owned MySQL fixture failure logs: %v", err)
				return
			}
			defer logs.Close()
			data, _ := io.ReadAll(io.LimitReader(logs, 64<<10))
			diagnostics := strings.ReplaceAll(string(data), password, "<fixture-password>")
			diagnostics = strings.ReplaceAll(diagnostics, "owned-native-mysql-root", "<root-fixture-password>")
			t.Logf("owned MySQL fixture failure logs: %s", diagnostics)
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	code, output, err := container.Exec(ctx, []string{"env", "MYSQL_PWD=" + password, "mysql", "--protocol=TCP", "--host=127.0.0.1", "--user=fixture_reader", "fixtures", "--execute=CREATE TABLE fixture_rows (id BIGINT, value VARCHAR(20)); INSERT INTO fixture_rows VALUES (1, 'x');"})
	if err != nil || code != 0 {
		var diagnostics string
		if output != nil {
			data, _ := io.ReadAll(io.LimitReader(output, 4096))
			diagnostics = strings.ReplaceAll(string(data), password, "<fixture-password>")
		}
		t.Fatalf("initialize MySQL fixture: exit=%d error=%v diagnostics=%s", code, err, diagnostics)
	}
	if output != nil {
		_, _ = io.Copy(io.Discard, output)
	}
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := container.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(mapped.Port())
	if err != nil {
		t.Fatal(err)
	}
	db := openNativeConnectorDB(t, "mysql")
	source := semanticmodel.Source{Connection: "local", Object: "fixture_rows"}
	connection := semanticmodel.Connection{Kind: "mysql", Host: host, Port: port, Database: "fixtures", Username: "fixture_reader", Auth: semanticmodel.ConnectionAuth{"password": password}}
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": connection}, Sources: map[string]semanticmodel.Source{"rows": source}}
	if err := prepareRefreshSourceAccess(ctx, db, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, db, model, source)
	// Exercise the connector's selected TLS backend, not only password lookup
	// and archive membership. The pinned server generates its fixture TLS key.
	var status, cipher string
	if err := db.QueryRowContext(ctx, "SELECT * FROM mysql_query('conn_local', 'SHOW SESSION STATUS LIKE ''Ssl_cipher''')").Scan(&status, &cipher); err != nil || status != "Ssl_cipher" || cipher == "" {
		t.Fatalf("native MySQL did not negotiate TLS: status=%q cipher=%q error=%v", status, cipher, err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO conn_local.fixture_rows VALUES (2, 'denied')"); err == nil {
		t.Fatal("target-owned MySQL source attachment allowed a write")
	}
	if _, err := db.ExecContext(ctx, "DETACH conn_local"); err != nil {
		t.Fatal(err)
	}
	connection.Auth = semanticmodel.ConnectionAuth{"password": "incorrect-owned-fixture-password"}
	model.Connections["local"] = connection
	if err := prepareRefreshSourceAccess(ctx, db, model, nil); err == nil {
		t.Fatal("native MySQL accepted an incorrect scoped credential")
	}
	connection.Auth = semanticmodel.ConnectionAuth{"password": password}
	model.Connections["local"] = connection
	if err := prepareRefreshSourceAccess(ctx, db, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, db, model, source)
}
