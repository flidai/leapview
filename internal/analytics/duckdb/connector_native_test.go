package duckdb

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
)

func openNativeConnectorDB(t *testing.T, extensions ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{"SET threads = 2", "SET memory_limit = '256MiB'", "SET autoinstall_known_extensions = false", "SET autoload_known_extensions = false"} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	admission := extensionfixture.New(t, extensions...).Admission
	for _, name := range extensions {
		artifact, err := admission.AdmitExtension(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateAdmittedExtension(name, artifact); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), loadAdmittedExtensionStatement(artifact)); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
	}
	return db
}

func assertNativeConnectorRow(t *testing.T, db *sql.DB, model *semanticmodel.Model, source semanticmodel.Source) {
	t.Helper()
	relation, err := SourceRelation(model, source)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := describeRelationSchema(t.Context(), db, relation)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0].Name != "id" || columns[1].Name != "value" {
		t.Fatalf("native schema = %#v", columns)
	}
	rows, err := db.QueryContext(t.Context(), relation)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("missing native row: %v", rows.Err())
	}
	var id int
	var value string
	if err := rows.Scan(&id, &value); err != nil {
		t.Fatal(err)
	}
	if id != 1 || value != "x" || rows.Next() {
		t.Fatalf("native rows differ: id=%d value=%q", id, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeSQLiteSourceReadAndRecovery(t *testing.T) {
	db := openNativeConnectorDB(t, "sqlite")
	path := filepath.Join(t.TempDir(), "source.sqlite")
	for _, statement := range []string{
		"ATTACH '" + SQLString(path) + "' AS seed (TYPE sqlite)",
		"CREATE TABLE seed.rows (id INTEGER, value TEXT)",
		"INSERT INTO seed.rows VALUES (1, 'x')", "DETACH seed",
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	source := semanticmodel.Source{Connection: "local", Object: "rows"}
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": {Kind: "sqlite", RuntimeOptions: semanticmodel.ConnectionRuntimeOptions{Path: path}}}, Sources: map[string]semanticmodel.Source{"rows": source}}
	if err := prepareRefreshSourceAccess(t.Context(), db, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, db, model, source)
	missing := source
	missing.Object = "absent_table"
	relation, err := SourceRelation(model, missing)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := db.QueryContext(t.Context(), relation); err == nil {
		_ = rows.Close()
		t.Fatal("absent SQLite table unexpectedly read")
	}
	assertNativeConnectorRow(t, db, model, source)
	if _, err := db.ExecContext(t.Context(), "INSERT INTO conn_local.rows VALUES (2, 'denied')"); err == nil {
		t.Fatal("target-owned SQLite source attachment allowed a write")
	}
}

func TestNativeDuckLakeSourceReadAndRecovery(t *testing.T) {
	db := openNativeConnectorDB(t, "ducklake", "sqlite")
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog.sqlite")
	data := filepath.Join(root, "data")
	for _, statement := range []string{
		"ATTACH 'ducklake:sqlite:" + SQLString(catalog) + "' AS seed (DATA_PATH '" + SQLString(data) + "')",
		"CREATE TABLE seed.fixture_rows AS SELECT 1::BIGINT AS id, 'x' AS value", "DETACH seed",
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	source := semanticmodel.Source{Connection: "local", Object: "fixture_rows"}
	connection := semanticmodel.Connection{Kind: "ducklake", Path: "sqlite:" + catalog, RuntimeOptions: semanticmodel.ConnectionRuntimeOptions{DataPath: data}}
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": connection}, Sources: map[string]semanticmodel.Source{"rows": source}}
	if err := prepareRefreshSourceAccess(t.Context(), db, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, db, model, source)
	missing := source
	missing.Object = "absent_table"
	relation, err := SourceRelation(model, missing)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := db.QueryContext(t.Context(), relation); err == nil {
		_ = rows.Close()
		t.Fatal("absent DuckLake table unexpectedly read")
	}
	assertNativeConnectorRow(t, db, model, source)
}

func TestNativeHTTPAndAzureSourceReadAndRecovery(t *testing.T) {
	for _, kind := range []string{"http", "azure_blob"} {
		t.Run(kind, func(t *testing.T) {
			var signed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/fixtures/rows.csv" {
					http.NotFound(w, r)
					return
				}
				if strings.HasPrefix(r.Header.Get("Authorization"), "SharedKey fixture:") {
					signed.Add(1)
				}
				w.Header().Set("Content-Type", "text/csv")
				w.Header().Set("ETag", `"native-fixture"`)
				w.Header().Set("x-ms-blob-type", "BlockBlob")
				w.Header().Set("x-ms-version", "2021-12-02")
				w.Header().Set("x-ms-request-id", "native-fixture")
				w.Header().Set("x-ms-creation-time", "Thu, 01 Jan 2026 00:00:00 GMT")
				w.Header().Set("x-ms-server-encrypted", "true")
				w.Header().Set("x-ms-lease-status", "unlocked")
				w.Header().Set("x-ms-lease-state", "available")
				http.ServeContent(w, r, "rows.csv", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), bytes.NewReader([]byte("id,value\n1,x\n")))
			}))
			t.Cleanup(server.Close)
			connection := semanticmodel.Connection{Kind: kind, Scope: server.URL + "/fixtures/"}
			extensions := []string{"httpfs"}
			if kind == "azure_blob" {
				extensions = []string{"azure"}
				connection.Scope = "az://fixtures/"
				key := base64.StdEncoding.EncodeToString([]byte("owned-native-fixture-key"))
				connection.Auth = semanticmodel.ConnectionAuth{"connection_string": "DefaultEndpointsProtocol=http;AccountName=fixture;AccountKey=" + key + ";BlobEndpoint=" + server.URL + ";"}
			}
			db := openNativeConnectorDB(t, extensions...)
			source := semanticmodel.Source{Connection: "local", Path: "rows.csv", Format: "csv", EffectivePathLocation: testCSVPathLocationWithHeader("rows.csv", true)}
			model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": connection}, Sources: map[string]semanticmodel.Source{"rows": source}}
			if err := prepareRefreshSourceAccess(t.Context(), db, model, nil); err != nil {
				t.Fatal(err)
			}
			assertNativeConnectorRow(t, db, model, source)
			if kind == "azure_blob" && signed.Load() == 0 {
				t.Fatal("native Azure reader did not use the configured scoped SharedKey credential")
			}
			missing := source
			missing.Path = "missing.csv"
			missing.EffectivePathLocation = testCSVPathLocationWithHeader(missing.Path, true)
			relation, err := SourceRelation(model, missing)
			if err != nil {
				t.Fatal(err)
			}
			if rows, err := db.QueryContext(t.Context(), relation); err == nil {
				_ = rows.Close()
				t.Fatal("absent remote object unexpectedly read")
			}
			assertNativeConnectorRow(t, db, model, source)
		})
	}
}

func TestNativeQuackSourceReadDenialAndRecovery(t *testing.T) {
	server := openNativeConnectorDB(t, "httpfs", "quack")
	if _, err := server.ExecContext(t.Context(), "CREATE TABLE rows AS SELECT 1::BIGINT AS id, 'x' AS value"); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	uri := fmt.Sprintf("quack:127.0.0.1:%d", port)
	const token = "owned-native-quack-fixture"
	started, err := server.QueryContext(t.Context(), "CALL quack_serve('"+uri+"', token := '"+token+"')")
	if err != nil {
		t.Fatal(err)
	}
	for started.Next() {
	}
	if err := started.Err(); err != nil {
		t.Fatal(err)
	}
	if err := started.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := server.Exec("CALL quack_stop('" + uri + "')"); err != nil {
			t.Error(err)
		}
	})
	client := openNativeConnectorDB(t, "httpfs", "quack")
	source := semanticmodel.Source{Connection: "local", Object: "rows"}
	connection := semanticmodel.Connection{Kind: "quack", Host: "127.0.0.1", Port: port, Auth: semanticmodel.ConnectionAuth{"token": token}}
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": connection}, Sources: map[string]semanticmodel.Source{"rows": source}}
	if err := prepareRefreshSourceAccess(t.Context(), client, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, client, model, source)
	connection.Auth = semanticmodel.ConnectionAuth{"token": "incorrect-owned-fixture-token"}
	model.Connections["local"] = connection
	if err := prepareRefreshSourceAccess(t.Context(), client, model, nil); err != nil {
		t.Fatal(err)
	}
	relation, err := SourceRelation(model, source)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := client.QueryContext(t.Context(), relation); err == nil {
		_ = rows.Close()
		t.Fatal("native Quack accepted an incorrect scoped token")
	}
	connection.Auth = semanticmodel.ConnectionAuth{"token": token}
	model.Connections["local"] = connection
	if err := prepareRefreshSourceAccess(t.Context(), client, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, client, model, source)
}

func TestNativeGCSSourceReadAndRecovery(t *testing.T) {
	var signed atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixtures/rows.csv" {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=owned-fixture-key/") {
			signed.Add(1)
		}
		http.ServeContent(w, r, "rows.csv", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), bytes.NewReader([]byte("id,value\n1,x\n")))
	}))
	t.Cleanup(server.Close)
	db := openNativeConnectorDB(t, "httpfs")
	certificate := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err := os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "SET ca_cert_file = '"+SQLString(certificate)+"'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "SET enable_server_cert_verification = true"); err != nil {
		t.Fatal(err)
	}
	source := semanticmodel.Source{Connection: "local", Path: "rows.csv", Format: "csv", EffectivePathLocation: testCSVPathLocationWithHeader("rows.csv", true)}
	connection := semanticmodel.Connection{Kind: "gcs", Scope: "gs://fixtures/", Auth: semanticmodel.ConnectionAuth{"access_key_id": "owned-fixture-key", "secret_access_key": "owned-fixture-secret", "endpoint": server.Listener.Addr().String()}}
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": connection}, Sources: map[string]semanticmodel.Source{"rows": source}}
	if err := prepareRefreshSourceAccess(t.Context(), db, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, db, model, source)
	if signed.Load() == 0 {
		t.Fatal("native GCS reader did not use the configured scoped HMAC credential")
	}
	missing := source
	missing.Path = "missing.csv"
	missing.EffectivePathLocation = testCSVPathLocationWithHeader(missing.Path, true)
	relation, err := SourceRelation(model, missing)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := db.QueryContext(t.Context(), relation); err == nil {
		_ = rows.Close()
		t.Fatal("absent GCS object unexpectedly read")
	}
	assertNativeConnectorRow(t, db, model, source)
}

func TestNativeR2SourceReadAndRecovery(t *testing.T) {
	const hostname = "ownedfixture.r2.cloudflarestorage.com"
	var signed atomic.Int32
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: serial, DNSNames: []string{hostname}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != hostname || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=owned-fixture-key/") {
			http.Error(w, "unexpected endpoint or credential", http.StatusForbidden)
			return
		}
		signed.Add(1)
		if r.URL.Path != "/fixtures/rows.csv" {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "rows.csv", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), bytes.NewReader([]byte("id,value\n1,x\n")))
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	// R2's product contract derives its endpoint from the account ID. An owned
	// CONNECT proxy routes only that exact endpoint to the disposable TLS wire
	// fixture, preserving the native endpoint, credential and certificate checks
	// without DNS changes or an unsupported endpoint override in R2 auth.
	var mu sync.Mutex
	var connections []net.Conn
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != hostname+":443" {
			http.Error(w, "unexpected proxy destination", http.StatusForbidden)
			return
		}
		remote, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 2*time.Second)
		if err != nil {
			http.Error(w, "fixture unavailable", http.StatusBadGateway)
			return
		}
		client, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = remote.Close()
			t.Error(err)
			return
		}
		mu.Lock()
		connections = append(connections, client, remote)
		mu.Unlock()
		defer client.Close()
		defer remote.Close()
		_ = client.SetDeadline(time.Now().Add(15 * time.Second))
		_ = remote.SetDeadline(time.Now().Add(15 * time.Second))
		if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err := buffered.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.Copy(remote, buffered)
			_ = remote.Close()
		}()
		_, _ = io.Copy(client, remote)
		_ = client.Close()
		<-done
	}))
	t.Cleanup(func() {
		proxy.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, connection := range connections {
			_ = connection.Close()
		}
	})
	db := openNativeConnectorDB(t, "httpfs")
	ca := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"SET ca_cert_file = '" + SQLString(ca) + "'", "SET enable_server_cert_verification = true", "SET http_proxy = '" + SQLString(proxy.URL) + "'"} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	source := semanticmodel.Source{Connection: "local", Path: "rows.csv", Format: "csv", EffectivePathLocation: testCSVPathLocationWithHeader("rows.csv", true)}
	connection := semanticmodel.Connection{Kind: "r2", Scope: "r2://fixtures/", Auth: semanticmodel.ConnectionAuth{"access_key_id": "owned-fixture-key", "secret_access_key": "owned-fixture-secret", "account_id": "ownedfixture"}}
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": connection}, Sources: map[string]semanticmodel.Source{"rows": source}}
	if err := prepareRefreshSourceAccess(t.Context(), db, model, nil); err != nil {
		t.Fatal(err)
	}
	assertNativeConnectorRow(t, db, model, source)
	if signed.Load() == 0 {
		t.Fatal("native R2 reader did not use the account endpoint and scoped HMAC credential")
	}
	missing := source
	missing.Path = "missing.csv"
	missing.EffectivePathLocation = testCSVPathLocationWithHeader(missing.Path, true)
	relation, err := SourceRelation(model, missing)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := db.QueryContext(t.Context(), relation); err == nil {
		_ = rows.Close()
		t.Fatal("absent R2 object unexpectedly read")
	}
	assertNativeConnectorRow(t, db, model, source)
}
