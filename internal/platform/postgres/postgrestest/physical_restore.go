package postgrestest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/log"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RestorePhysical starts an independent server from one verified pg_basebackup
// of this harness's cluster. Every requested database and its roles therefore
// come from the same recovery point; no schema or application state is seeded
// on the replacement. Only a standalone harness is eligible: a shared package
// server must never be copied or stopped by an individual test. The original
// standalone server is stopped after backup verification before replacement.
func (h *Harness) RestorePhysical(t *testing.T, databases ...*Database) (*Harness, []*Database) {
	t.Helper()
	if h == nil || h.container == nil || h.shared != nil || len(databases) == 0 {
		t.Fatal("physical restore requires a standalone disposable PostgreSQL harness and databases")
	}
	for _, database := range databases {
		if database == nil || database.h != h {
			t.Fatal("physical restore database belongs to another harness")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	exec := func(args ...string) {
		t.Helper()
		// sqlc-exception: analyzer-incompatible. Testcontainers Exec runs a container command with argv, not a PostgreSQL statement.
		code, output, err := h.container.Exec(ctx, args)
		if err != nil {
			t.Fatalf("physical backup command: %v", err)
		}
		contents, readErr := io.ReadAll(io.LimitReader(output, 1<<20))
		if code != 0 || readErr != nil {
			t.Fatalf("physical backup command exit=%d: %v: %s", code, readErr, contents)
		}
	}
	// This is the same physical backup mechanism qualified by the provider
	// restore lane. WAL streaming makes the plain backup independently bootable.
	exec("su", "postgres", "-c", "pg_basebackup -U postgres -h /var/run/postgresql -D /tmp/qualification-physical-base -Fp -X stream -c fast")
	exec("su", "postgres", "-c", "pg_verifybackup /tmp/qualification-physical-base")
	exec("tar", "-C", "/tmp/qualification-physical-base", "-czf", "/tmp/qualification-physical.tar.gz", ".")
	archive, err := h.container.CopyFileFromContainer(ctx, "/tmp/qualification-physical.tar.gz")
	if err != nil {
		t.Fatalf("read physical backup archive: %v", err)
	}
	defer archive.Close()
	hash := sha256.New()
	// Keep the archive in a test-owned file rather than retaining a complete
	// cluster in the Go heap. PostgreSQL data stays on bounded container tmpfs.
	archivePath := t.TempDir() + "/physical.tar.gz"
	archiveFile, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("create physical backup archive: %v", err)
	}
	_, copyErr := io.Copy(io.MultiWriter(archiveFile, hash), archive)
	closeErr := archiveFile.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("stage physical backup archive: copy=%v close=%v", copyErr, closeErr)
	}
	// Fence the old server before the caller captures stopped application/home
	// directories. It cannot remain a second writable authority after restore.
	if err := h.container.Stop(ctx, nil); err != nil {
		t.Fatalf("stop original PostgreSQL authority: %v", err)
	}
	h.mu.Lock()
	h.stoppedForPhysicalRestore = true
	h.mu.Unlock()
	listener, privateIP := newPrivateTLSListener(t)
	ca, cert, key := tlsCertificateFiles(t, privateIP)
	files := []testcontainers.ContainerFile{
		{HostFilePath: archivePath, ContainerFilePath: "/tmp/physical.tar.gz", FileMode: 0600},
		{HostFilePath: ca, ContainerFilePath: "/tmp/ca.pem", FileMode: 0644},
		{HostFilePath: cert, ContainerFilePath: "/tmp/server.pem", FileMode: 0644},
		{HostFilePath: key, ContainerFilePath: "/tmp/server.key", FileMode: 0600},
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: PostgreSQL18Image, ExposedPorts: []string{"5432/tcp"}, Files: files,
			Tmpfs:      map[string]string{"/var/lib/postgresql": "rw,size=512m"},
			Entrypoint: []string{"sh", "-c"},
			Cmd:        []string{"mkdir -p /var/lib/postgresql/18/docker && tar -C /var/lib/postgresql/18/docker -xzf /tmp/physical.tar.gz && chown -R postgres:postgres /var/lib/postgresql /tmp/server.key && exec su postgres -c 'postgres -D /var/lib/postgresql/18/docker -c listen_addresses=* -c ssl=on -c ssl_ca_file=/tmp/ca.pem -c ssl_cert_file=/tmp/server.pem -c ssl_key_file=/tmp/server.key'"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithStartupTimeout(defaultStartupTimeout),
		}, Started: true, Logger: log.TestLogger(t),
	})
	if err != nil {
		if container != nil {
			testcontainers.CleanupContainer(t, container)
		}
		t.Fatalf("start restored PostgreSQL server: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := net.JoinHostPort(host, port.Port())
	adminURL := (&url.URL{Scheme: "postgres", User: url.UserPassword("postgres", defaultPassword), Host: endpoint, Path: "/postgres", RawQuery: "sslmode=disable"}).String()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if err := admin.Ping(ctx); err != nil {
		t.Fatalf("ping physically restored PostgreSQL: %v", err)
	}
	restored := &Harness{adminURL: adminURL, rootCert: ca, privateEndpoint: listener.Addr().String(), admin: admin, roles: make(map[string]Role)}
	servePrivateTLSRelay(listener, endpoint)
	result := make([]*Database, len(databases))
	for i, database := range databases {
		pool, err := pgxpool.New(ctx, restored.urlFor(database.Name, "postgres", defaultPassword))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		if err := pool.Ping(ctx); err != nil {
			t.Fatalf("restored database %s: %v", database.Name, err)
		}
		result[i] = &Database{h: restored, Name: database.Name, admin: pool}
	}
	t.Logf("physically restored %d native databases from verified pg_basebackup sha256:%s onto an independent PostgreSQL 18 endpoint", len(result), hex.EncodeToString(hash.Sum(nil)))
	return restored, result
}

func (h *Harness) physicalRestoreStopped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shared == nil && h.stoppedForPhysicalRestore
}
