//go:build leapview_static_http && duckdb_use_static_lib

package duckdbsession

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
)

func openCompiledHTTP(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	var platform string
	if err := db.QueryRow("PRAGMA platform").Scan(&platform); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"httpfs", "quack"} {
		builtin, ok := extension.CompiledBuiltin(name, platform)
		if !ok {
			t.Fatalf("missing builtin %s", name)
		}
		identity := extension.Identity{Builtin: true, Name: name, DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyCompiledBuiltin(context.Background(), db, identity); err != nil {
			t.Fatal(err)
		}
		var version string
		if err := db.QueryRow("SELECT extension_version FROM duckdb_extensions() WHERE extension_name = ?", name).Scan(&version); err != nil || version != builtin.SourceRevision {
			t.Fatalf("%s source identity %q: %v", name, version, err)
		}
	}
	for _, statement := range []string{"SET httpfs_client_implementation = 'curl'", "SET http_retries = 0"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestCompiledHTTPFSVerifiesTLSAndReadsParquetRanges(t *testing.T) {
	db := openCompiledHTTP(t)
	path := filepath.Join(t.TempDir(), "data.parquet")
	if _, err := db.Exec("COPY (SELECT i AS id FROM range(10000) rows(i)) TO '" + path + "' (FORMAT PARQUET, ROW_GROUP_SIZE 2048)"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ranges atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		}
		http.ServeContent(w, r, "data.parquet", time.Unix(1, 0), bytes.NewReader(data))
	}))
	defer server.Close()
	query := "SELECT count(*), sum(id) FROM read_parquet('" + server.URL + "/data.parquet') WHERE id >= 4096 AND id < 8192"
	var count, sum int64
	// Certificate verification must remain enabled; the unknown test CA is rejected.
	if err := db.QueryRow(query).Scan(&count, &sum); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("SET ca_cert_file = '" + ca + "'"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(query).Scan(&count, &sum); err != nil || count != 4096 || sum != 25163776 {
		t.Fatalf("HTTPS range read=(%d,%d): %v", count, sum, err)
	}
	if ranges.Load() == 0 {
		t.Fatal("Parquet read did not exercise HTTP byte ranges")
	}
}

func TestCompiledQuackAuthenticatesSharedHTTPClient(t *testing.T) {
	server := openCompiledHTTP(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	uri := "quack:" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Exec("FROM quack_serve('" + uri + "', token='test-native-token')"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = server.Exec("CALL quack_stop('" + uri + "')") }()
	client := openCompiledHTTP(t)
	query := "SELECT value FROM quack_query('" + uri + "', 'SELECT 42 AS value', token='test-native-token')"
	var value int
	if err := client.QueryRow(query).Scan(&value); err != nil || value != 42 {
		t.Fatalf("authenticated Quack read=%d: %v", value, err)
	}
	if err := client.QueryRow(strings.Replace(query, "test-native-token", "incorrect-token", 1)).Scan(&value); err == nil {
		t.Fatal("incorrect Quack token accepted")
	}
	// Closing and reopening the client exercises independent shared-CURL state.
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openCompiledHTTP(t)
	if err := reopened.QueryRow(query).Scan(&value); err != nil || value != 42 {
		t.Fatalf("reopened Quack read=%d: %v", value, err)
	}
}

// The manual native lane runs this binary in an isolated filesystem containing
// the image's CA layout. Ordinary package tests must never change host trust.
func TestCompiledHTTPFSDefaultTrust(t *testing.T) {
	mode := os.Getenv("LEAPVIEW_TEST_HTTP_CA_MODE")
	if mode == "" {
		t.Skip("manual native application lane runs the isolated image CA fixture")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "data.csv", time.Unix(1, 0), strings.NewReader("value\n42\n"))
	}))
	defer server.Close()
	if mode == "export" {
		if err := os.WriteFile(os.Getenv("LEAPVIEW_TEST_HTTP_CA_OUTPUT"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode != "verify" {
		t.Fatalf("unknown CA fixture mode %q", mode)
	}
	db := openCompiledHTTP(t)
	var value int
	// No ca_cert_file override: this exercises HTTPFS's actual default path
	// selection and CURL's verification with the packaged certificate layout.
	if err := db.QueryRow("SELECT value FROM read_csv('" + server.URL + "/data.csv')").Scan(&value); err != nil || value != 42 {
		t.Fatalf("default HTTPS trust read=%d: %v", value, err)
	}
}
