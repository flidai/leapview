package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Copy the actual publication's physical cluster, then archive and restore it
// with the managed provider. The source uses the pinned conformance container;
// every recovery operation uses the explicitly supplied locked native tools.
type managedJourneyRestoredCluster struct {
	directory, pgbin, pgbackrest, providerFile, caFile, certFile, keyFile string
	frontier                                                              managedrecovery.PGFrontier
	native                                                                managedrecovery.NativePostgresReadback
}

func managedJourneyPhysicalRestore(t *testing.T, f *sourceCredentialHTTPJourney, native managedrecovery.NativePostgresReadback) managedJourneyRestoredCluster {
	pgbin, pgbackrest, bwrap := os.Getenv("LEAPVIEW_TEST_MANAGED_POSTGRES_BIN"), os.Getenv("LEAPVIEW_TEST_MANAGED_PGBACKREST"), os.Getenv("LEAPVIEW_TEST_MANAGED_BWRAP")
	if pgbin == "" || pgbackrest == "" || bwrap == "" {
		t.Skip("explicit locked PostgreSQL, pgBackRest and confinement tools required")
	}
	require.NotZero(t, os.Geteuid(), "PostgreSQL must run as an unprivileged owner")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	// Exercise retained data paths beyond sockaddr_un's limit. Local fixture
	// sockets must remain independent of the provider's destination length.
	base, err := os.MkdirTemp("", "lv-production-recovery-"+strings.Repeat("retained-", 12))
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	source, socket := filepath.Join(base, "source"), managedJourneySocketDirectory(t)
	run := func(program string, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, program, args...)
		output, err := command.Output()
		// The PostgreSQL connection argument contains disposable credentials;
		// neither arguments nor provider stderr belong in a test failure report.
		require.NoError(t, err, "pinned provider %s", filepath.Base(program))
		return output
	}
	run(filepath.Join(pgbin, "pg_basebackup"), "--dbname="+f.harness.PhysicalBackupURL(t), "--pgdata="+source, "--wal-method=stream", "--checkpoint=fast")
	providerFile := filepath.Join(base, "archive.conf")
	// Keep locks private and outside the retained base, which confinement
	// mounts read-only. The sandbox can recreate this path in its private /tmp.
	provider := []byte(fmt.Sprintf("[global]\nrepo1-path=%s\nlock-path=%s\nrepo1-retention-full=2\nstart-fast=y\nprocess-max=2\narchive-timeout=60\nlog-level-console=off\nlog-level-file=off\n[managed]\npg1-path=%s\npg1-port=5432\npg1-socket-path=%s\npg1-user=postgres\n", filepath.Join(base, "repository"), filepath.Join(socket, "pgbackrest-locks"), source, socket))
	require.NoError(t, os.WriteFile(providerFile, provider, 0600))
	configuration := fmt.Sprintf("listen_addresses=''\nport=5432\nunix_socket_directories='%s'\narchive_mode=on\narchive_command='%s --config=%s --stanza=managed archive-push %%p'\n", socket, pgbackrest, providerFile)
	require.NoError(t, os.WriteFile(filepath.Join(source, "postgresql.conf"), []byte(configuration), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "postgresql.auto.conf"), nil, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "pg_hba.conf"), []byte("local all postgres trust\n"), 0600))
	pgctl := filepath.Join(pgbin, "pg_ctl")
	run(pgctl, "-D", source, "-l", filepath.Join(base, "primary.log"), "-w", "start")
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, pgctl, "-D", source, "-m", "immediate", "-w", "stop").Run()
	})
	admin, err := pgx.Connect(ctx, fmt.Sprintf("host=%s port=5432 user=postgres dbname=postgres sslmode=disable", socket))
	require.NoError(t, err)
	defer admin.Close(context.Background())
	run(pgbackrest, "--config="+providerFile, "--stanza=managed", "stanza-create")
	run(pgbackrest, "--config="+providerFile, "--stanza=managed", "--type=full", "backup")
	var inventory []struct {
		Backup []struct {
			Label string `json:"label"`
		} `json:"backup"`
	}
	require.NoError(t, json.Unmarshal(run(pgbackrest, "--config="+providerFile, "--stanza=managed", "--output=json", "info"), &inventory))
	require.Len(t, inventory, 1)
	require.Len(t, inventory[0].Backup, 1)
	frontier := managedrecovery.PGFrontier{Stanza: "managed", BackupSet: inventory[0].Backup[0].Label}
	require.NoError(t, admin.QueryRow(ctx, `SELECT pg_current_wal_insert_lsn()::text,system_identifier::text,(SELECT timeline_id FROM pg_control_checkpoint()) FROM pg_control_system()`).Scan(&frontier.TargetLSN, &frontier.SystemID, &frontier.Timeline))
	_, err = admin.Exec(ctx, "SELECT pg_switch_wal()")
	require.NoError(t, err)
	run(pgbackrest, "--config="+providerFile, "--stanza=managed", "check")
	require.NoError(t, admin.Close(ctx))
	run(pgctl, "-D", source, "-m", "fast", "-w", "stop")
	identity, err := frontier.RecoveryIdentity()
	require.NoError(t, err)
	for index := range native.Set.ClusterPoints {
		require.Equal(t, "postgres-system-id:"+frontier.SystemID, native.Set.ClusterPoints[index].ClusterIdentity, "physical backup must preserve the original system identity")
		native.Set.ClusterPoints[index].RecoveryIdentity = identity
	}
	endpoint, err := url.Parse(f.config.PostgresControlURL)
	require.NoError(t, err)
	privateIP := net.ParseIP(endpoint.Hostname())
	require.NotNil(t, privateIP, "production fixture must retain its admitted private endpoint")
	ca, cert, key := managedJourneyCertificates(t, privateIP)
	certFile, keyFile := filepath.Join(base, "server.crt"), filepath.Join(base, "server.key")
	caFile := filepath.Join(base, "ca.crt")
	require.NoError(t, os.WriteFile(caFile, ca, 0600))
	require.NoError(t, os.WriteFile(certFile, cert, 0600))
	require.NoError(t, os.WriteFile(keyFile, key, 0600))
	for _, target := range []*string{&native.ControlURL, &native.DuckLakeURL} {
		parsed, err := url.Parse(*target)
		require.NoError(t, err)
		parsed.Host = "postgres.leapview.test"
		*target = parsed.String()
	}
	native.RootCA = string(ca)
	readback, err := managedrecovery.NewPGNativeReadback(managedrecovery.PGNativeReadbackConfig{Native: native, Frontier: frontier, Postgres: filepath.Join(pgbin, "postgres"), PGControlData: filepath.Join(pgbin, "pg_controldata"), PGBackRest: pgbackrest, ProviderConfigFile: providerFile, ProviderConfigDigest: managedJourneyDigest(provider), ServerCertificateFile: certFile, ServerCertificateDigest: managedJourneyDigest(cert), ServerKeyFile: keyFile, ServerKeyDigest: managedJourneyDigest(key)})
	require.NoError(t, err)
	restorer, err := managedrecovery.NewPGBackRest(managedrecovery.PGBackRestConfig{TargetID: native.Set.Delivery.TargetID, RecoverySetID: native.Set.ID, Points: native.Set.ClusterPoints, Frontier: frontier, PGBackRest: pgbackrest, Bubblewrap: bwrap, ConfigFile: providerFile, ConfigDigest: managedJourneyDigest(provider), Destination: filepath.Join(base, "replacement"), Readback: readback})
	require.NoError(t, err)
	request := providerrestore.DatabaseRequest{TargetID: native.Set.Delivery.TargetID, RecoverySetID: native.Set.ID, Points: native.Set.ClusterPoints, Catalog: native.Set.Catalog, IdempotencyKey: "actual-production-publication"}
	for attempt := 0; attempt < 2; attempt++ {
		results, err := restorer.RestoreCluster(ctx, request)
		require.NoError(t, err)
		require.Len(t, results, 2)
		for _, result := range results {
			require.NotEmpty(t, result.StateDigest)
		}
	}
	return managedJourneyRestoredCluster{filepath.Join(base, "replacement"), pgbin, pgbackrest, providerFile, caFile, certFile, keyFile, frontier, native}
}

func managedJourneySocketDirectory(t *testing.T) string {
	t.Helper()
	// TMPDIR may itself exceed PostgreSQL's Unix socket path limit on CI.
	// MkdirTemp creates a private 0700 directory; process cleanup is registered
	// later, so PostgreSQL stops before this directory is removed.
	directory, err := os.MkdirTemp("/tmp", "lv-pg-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	return directory
}

func managedJourneyDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func managedJourneyCertificates(t *testing.T, privateIPs ...net.IP) (ca, cert, key []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "managed production journey CA"}, IsCA: true, BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	server := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"postgres.leapview.test"}, IPAddresses: privateIPs, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, server, caTemplate, &serverKey.PublicKey, caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
