package managedrecovery

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/jackc/pgx/v5"
)

// This restores seeded native-schema component fixtures with real pgBackRest,
// WAL, runtime accounts and TLS. It is not application publication acceptance.
func TestActualConfinedNativePGReadback(t *testing.T) {
	program, pgbin, bwrap := os.Getenv("LEAPVIEW_TEST_MANAGED_PGBACKREST"), os.Getenv("LEAPVIEW_TEST_MANAGED_POSTGRES_BIN"), os.Getenv("LEAPVIEW_TEST_MANAGED_BWRAP")
	if program == "" || pgbin == "" || bwrap == "" {
		t.Skip("explicit pinned PostgreSQL/pgBackRest/confinement inputs required")
	}
	if os.Geteuid() == 0 {
		t.Fatal("PostgreSQL requires an unprivileged process")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	base, err := os.MkdirTemp("", "lv-native-pg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	source, socket, repo := filepath.Join(base, "source"), filepath.Join(base, "socket"), filepath.Join(base, "repository")
	if err := os.Mkdir(socket, 0700); err != nil {
		t.Fatal(err)
	}
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	run := func(program string, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, program, args...)
		command.Env = append(os.Environ(), "LANG=C", "TZ=UTC")
		value, err := command.Output()
		if err != nil {
			t.Fatalf("actual pinned %s: %v", filepath.Base(program), err)
		}
		return value
	}
	configFile := filepath.Join(base, "archive.conf")
	provider := []byte(fmt.Sprintf("[global]\nrepo1-path=%s\nrepo1-retention-full=2\nstart-fast=y\nprocess-max=2\narchive-timeout=60\nlog-level-console=off\nlog-level-file=off\n[managed]\npg1-path=%s\npg1-port=%d\npg1-socket-path=%s\npg1-user=%s\n", repo, source, port, socket, owner.Username))
	if err := os.WriteFile(configFile, provider, 0600); err != nil {
		t.Fatal(err)
	}
	pgctl := filepath.Join(pgbin, "pg_ctl")
	run(filepath.Join(pgbin, "initdb"), "-D", source, "--no-locale", "--encoding=UTF8", "--auth-local=trust", "--auth-host=reject")
	file, err := os.OpenFile(filepath.Join(source, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(fmt.Sprintf("\nlisten_addresses=''\nport=%d\nunix_socket_directories='%s'\narchive_mode=on\narchive_command='%s --config=%s --stanza=managed archive-push %%p'\n", port, socket, program, configFile))
	if err := errors.Join(err, file.Close()); err != nil {
		t.Fatal(err)
	}
	run(pgctl, "-D", source, "-l", filepath.Join(base, "primary.log"), "-w", "start")
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, pgctl, "-D", source, "-m", "immediate", "-w", "stop").Run()
	})
	connect := func(database string) *pgx.Conn {
		t.Helper()
		connection, err := pgx.Connect(ctx, fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=disable", socket, port, owner.Username, database))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
		return connection
	}
	admin := connect("postgres")
	for _, statement := range []string{"CREATE ROLE managed_control_reader LOGIN PASSWORD 'control-reader-password'", "CREATE ROLE managed_duck_reader LOGIN PASSWORD 'duck-reader-password'", "CREATE DATABASE control", "CREATE DATABASE ducklake"} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	control, duck := connect("control"), connect("ducklake")
	set, _ := seedNativeManagedReadback(t, control, duck, "control", "ducklake")
	if _, err := control.Exec(ctx, "CREATE TABLE managed_acknowledgement(id integer PRIMARY KEY); INSERT INTO managed_acknowledgement VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	run(program, "--config="+configFile, "--stanza=managed", "stanza-create")
	run(program, "--config="+configFile, "--stanza=managed", "--type=full", "backup")
	var info []struct {
		Backup []struct {
			Label string `json:"label"`
		} `json:"backup"`
	}
	if err := json.Unmarshal(run(program, "--config="+configFile, "--stanza=managed", "--output=json", "info"), &info); err != nil || len(info) != 1 || len(info[0].Backup) != 1 {
		t.Fatal("exact full backup missing")
	}
	if _, err := control.Exec(ctx, "INSERT INTO managed_acknowledgement VALUES(2)"); err != nil {
		t.Fatal(err)
	}
	frontier := PGFrontier{Stanza: "managed", BackupSet: info[0].Backup[0].Label}
	if err := admin.QueryRow(ctx, "SELECT pg_current_wal_insert_lsn()::text,(SELECT system_identifier::text FROM pg_control_system()),(SELECT timeline_id FROM pg_control_checkpoint())").Scan(&frontier.TargetLSN, &frontier.SystemID, &frontier.Timeline); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exec(ctx, "UPDATE delivery.delivery_target SET target_revision=3;INSERT INTO managed_acknowledgement VALUES(3)"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "SELECT pg_switch_wal()"); err != nil {
		t.Fatal(err)
	}
	run(program, "--config="+configFile, "--stanza=managed", "check")
	identity, err := frontier.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for index := range set.ClusterPoints {
		set.ClusterPoints[index].ClusterIdentity = "postgres-system-id:" + frontier.SystemID
		set.ClusterPoints[index].RecoveryIdentity = identity
	}
	control.Close(ctx)
	duck.Close(ctx)
	admin.Close(ctx)
	run(pgctl, "-D", source, "-m", "fast", "-w", "stop")
	original, err := originalPGTreeDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	ca, cert, key := nativeReadbackCertificates(t)
	certificateFile, keyFile := filepath.Join(base, "server.crt"), filepath.Join(base, "server.key")
	for path, value := range map[string][]byte{certificateFile: cert, keyFile: key} {
		if err := os.WriteFile(path, value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	native := NativePostgresReadback{Set: set, MetadataSchema: "catalog_metadata", ControlURL: "postgres://managed_control_reader:control-reader-password@postgres.leapview.test/control?sslmode=verify-full", DuckLakeURL: "postgres://managed_duck_reader:duck-reader-password@postgres.leapview.test/ducklake?sslmode=verify-full", RootCA: string(ca), Roles: RuntimeRoles{Control: "managed_control_reader", DuckLake: "managed_duck_reader"}}
	configuration := PGNativeReadbackConfig{Native: native, Frontier: frontier, Postgres: filepath.Join(pgbin, "postgres"), PGControlData: filepath.Join(pgbin, "pg_controldata"), PGBackRest: program, ProviderConfigFile: configFile, ProviderConfigDigest: digestBytes(provider), ServerCertificateFile: certificateFile, ServerCertificateDigest: digestBytes(cert), ServerKeyFile: keyFile, ServerKeyDigest: digestBytes(key)}
	readback, err := NewPGNativeReadback(configuration)
	if err != nil {
		t.Fatal(err)
	}
	request := providerrestore.DatabaseRequest{TargetID: set.Delivery.TargetID, RecoverySetID: set.ID, Points: set.ClusterPoints, Catalog: set.Catalog, IdempotencyKey: "exact-native-restore"}
	makeRestorer := func(destination string, callback PGReadback) *PGBackRest {
		t.Helper()
		restorer, err := NewPGBackRest(PGBackRestConfig{TargetID: request.TargetID, RecoverySetID: request.RecoverySetID, Points: request.Points, Frontier: frontier, PGBackRest: program, Bubblewrap: bwrap, ConfigFile: configFile, ConfigDigest: digestBytes(provider), Destination: destination, Readback: callback})
		if err != nil {
			t.Fatal(err)
		}
		return restorer
	}
	positive := makeRestorer(filepath.Join(base, "replacement"), readback)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := positive.RestoreCluster(ctx, request)
		if err != nil || len(result) != 2 {
			t.Fatalf("real native TLS readback/retry: %v", err)
		}
		for _, value := range result {
			if !validContentDigest(value.StateDigest) {
				t.Fatal("native evidence missing")
			}
		}
	}
	t.Run("foreign publication cannot expose data", func(t *testing.T) {
		wrong := configuration
		wrong.Native.Set.Delivery.PublicationID = "018f3f83-7b2f-7b37-9f9e-000000000107"
		callback, err := NewPGNativeReadback(wrong)
		if err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(base, "foreign-replacement")
		if _, err := makeRestorer(destination, callback).RestoreCluster(ctx, request); err == nil {
			t.Fatal("foreign publication certified")
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			t.Fatal("failed native proof exposed data")
		}
	})
	t.Run("foreign retained TLS key", func(t *testing.T) {
		wrong := configuration
		wrong.ServerKeyDigest = digestBytes([]byte("foreign"))
		if _, err := NewPGNativeReadback(wrong); err == nil {
			t.Fatal("foreign retained key accepted")
		}
	})
	t.Run("foreign WAL timeline", func(t *testing.T) {
		wrong := configuration
		wrong.Frontier.Timeline++
		if _, err := NewPGNativeReadback(wrong); err == nil {
			t.Fatal("foreign WAL timeline accepted")
		}
	})
	if after, err := originalPGTreeDigest(source); err != nil || after != original {
		t.Fatal("native recovery mutated original retained cluster")
	}
}

func nativeReadbackCertificates(t *testing.T) (ca, cert, key []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "managed readback test CA"}, IsCA: true, BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"postgres.leapview.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, server, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
