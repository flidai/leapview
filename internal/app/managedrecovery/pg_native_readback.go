package managedrecovery

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/jackc/pgx/v5"
)

type PGNativeReadbackConfig struct {
	Native                  NativePostgresReadback
	Frontier                PGFrontier
	Postgres                string
	PGControlData           string
	PGBackRest              string
	ProviderConfigFile      string
	ProviderConfigDigest    string
	ServerCertificateFile   string
	ServerCertificateDigest string
	ServerKeyFile           string
	ServerKeyDigest         string
}

// NewPGNativeReadback returns the production callback. The only PostgreSQL
// launch path is the mandatory kernel-confined staging capability. Exact
// runtime URLs retain their admitted TLS server name but connect solely to
// this private loopback listener, never the original or an ambient server.
func NewPGNativeReadback(config PGNativeReadbackConfig) (PGReadback, error) {
	identity, err := config.Frontier.RecoveryIdentity()
	if err != nil || config.Native.Set.Validate() != nil || len(config.Native.Set.ClusterPoints) != 2 || !pinnedProgram(config.Postgres) || filepath.Base(config.Postgres) != "postgres" || !pinnedProgram(config.PGControlData) || filepath.Base(config.PGControlData) != "pg_controldata" || filepath.Dir(config.Postgres) != filepath.Dir(config.PGControlData) || !pinnedProgram(config.PGBackRest) || filepath.Base(config.PGBackRest) != "pgbackrest" {
		return nil, errors.New("exact native recovery identity and pinned PostgreSQL tools required")
	}
	config.Native.Set, err = config.Native.Set.Normalize()
	if err != nil {
		return nil, err
	}
	if config.Native.Credentials != nil {
		credentials := *config.Native.Credentials
		config.Native.Credentials = &credentials
	}
	for _, point := range config.Native.Set.ClusterPoints {
		if point.ClusterIdentity != "postgres-system-id:"+config.Frontier.SystemID || point.RecoveryIdentity != identity {
			return nil, errors.New("native recovery set differs from retained PostgreSQL WAL frontier")
		}
	}
	for _, value := range []string{config.Native.Roles.Control, config.Native.Roles.DuckLake} {
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,62}$`).MatchString(value) {
			return nil, errors.New("managed runtime role must be an explicit PostgreSQL identifier")
		}
	}
	if _, err := managedConnectionConfig(config.Native.ControlURL, config.Native.Roles.Control, config.Native.RootCA); err != nil {
		return nil, err
	}
	if _, err := managedConnectionConfig(config.Native.DuckLakeURL, config.Native.Roles.DuckLake, config.Native.RootCA); err != nil {
		return nil, err
	}
	if _, _, err := config.privateInputs(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, cluster *PGStagingCluster, frontier PGFrontier, request providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
		if cluster == nil || frontier != config.Frontier || request.TargetID != config.Native.Set.Delivery.TargetID || request.RecoverySetID != config.Native.Set.ID || request.IdempotencyKey == "" || !samePGPoints(request.Points, config.Native.Set.ClusterPoints) || request.Catalog != config.Native.Set.Catalog {
			return nil, errors.New("native staging readback differs from exact managed operation")
		}
		return config.readback(ctx, cluster, request)
	}, nil
}

func (config PGNativeReadbackConfig) privateInputs() ([]byte, []byte, error) {
	provider, err := readBoundedManagedPrivateFile(config.ProviderConfigFile, maxManagedCredentialsBytes)
	if err != nil || !validContentDigest(config.ProviderConfigDigest) || digestBytes(provider) != config.ProviderConfigDigest {
		return nil, nil, errors.New("retained private PostgreSQL archive configuration differs")
	}
	if err := validatePGRestoreConfig(provider); err != nil {
		return nil, nil, err
	}
	certificate, err := readBoundedManagedPrivateFile(config.ServerCertificateFile, maxManagedCredentialsBytes)
	if err != nil || !validContentDigest(config.ServerCertificateDigest) || digestBytes(certificate) != config.ServerCertificateDigest {
		return nil, nil, errors.New("retained PostgreSQL server certificate differs")
	}
	key, err := readBoundedManagedPrivateFile(config.ServerKeyFile, maxManagedCredentialsBytes)
	if err != nil || !validContentDigest(config.ServerKeyDigest) || digestBytes(key) != config.ServerKeyDigest {
		return nil, nil, errors.New("retained PostgreSQL private server key differs")
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, nil, errors.New("retained PostgreSQL TLS server identity invalid")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, errors.New("retained PostgreSQL TLS certificate invalid")
	}
	roots, err := postgresRoots(config.Native.RootCA)
	if err != nil {
		return nil, nil, err
	}
	intermediates := x509.NewCertPool()
	for _, der := range pair.Certificate[1:] {
		entry, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, nil, errors.New("invalid PostgreSQL server certificate chain")
		}
		intermediates.AddCert(entry)
	}
	for _, entry := range []struct{ url, role string }{{config.Native.ControlURL, config.Native.Roles.Control}, {config.Native.DuckLakeURL, config.Native.Roles.DuckLake}} {
		parsed, err := managedPostgresURL(entry.url, entry.role)
		if err != nil {
			return nil, nil, err
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: parsed.Hostname(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
			return nil, nil, errors.New("retained PostgreSQL server identity differs from runtime TLS endpoints")
		}
	}
	return certificate, key, nil
}

func (config PGNativeReadbackConfig) readback(ctx context.Context, cluster *PGStagingCluster, request providerrestore.DatabaseRequest) (results []providerrestore.DatabaseResult, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	certificate, key, err := config.privateInputs()
	if err != nil {
		return nil, err
	}
	if err := config.verifySystemID(ctx, cluster.Directory()); err != nil {
		return nil, err
	}
	files := map[string][]byte{".managed-readback.crt": certificate, ".managed-readback.key": key, ".managed-readback.conf": []byte("# managed recovery confined readback\n"), ".managed-readback.hba": []byte("hostssl all \"" + config.Native.Roles.Control + "\" 127.0.0.1/32 scram-sha-256\nhostssl all \"" + config.Native.Roles.DuckLake + "\" 127.0.0.1/32 scram-sha-256\n")}
	for name, value := range files {
		path := filepath.Join(cluster.Directory(), name)
		// This directory is private staging and the provider adapter has already
		// rejected all links. Use exclusive replacement of our own private files.
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() || !managedFileOwned(info) || info.Mode().Perm()&0077 != 0 {
				return nil, errors.New("untrusted retained readback configuration path")
			}
			if err := os.Remove(path); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := file.Write(value)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	setting := func(name, value string) []string { return []string{"-c", name + "=" + value} }
	args := []string{}
	// Readback uses only verified TLS on loopback. A Unix socket is unused and
	// would prevent startup when the private restore destination is long.
	settings := [][2]string{{"config_file", filepath.Join(cluster.Directory(), ".managed-readback.conf")}, {"hba_file", filepath.Join(cluster.Directory(), ".managed-readback.hba")}, {"listen_addresses", "127.0.0.1"}, {"port", strconv.Itoa(port)}, {"unix_socket_directories", ""}, {"ssl", "on"}, {"ssl_cert_file", filepath.Join(cluster.Directory(), ".managed-readback.crt")}, {"ssl_key_file", filepath.Join(cluster.Directory(), ".managed-readback.key")}, {"ssl_min_protocol_version", "TLSv1.2"}, {"shared_preload_libraries", ""}, {"session_preload_libraries", ""}, {"local_preload_libraries", ""}, {"archive_mode", "off"}, {"logging_collector", "off"}, {"recovery_target_lsn", config.Frontier.TargetLSN}, {"recovery_target_timeline", strconv.FormatUint(uint64(config.Frontier.Timeline), 10)}, {"recovery_target_action", "pause"}, {"restore_command", shellLiteral(config.PGBackRest) + " --config=" + shellLiteral(config.ProviderConfigFile) + " --stanza=" + shellLiteral(config.Frontier.Stanza) + " --pg1-path=" + shellLiteral(cluster.Directory()) + " archive-get '%f' '%p'"}}
	for _, entry := range settings {
		args = append(args, setting(entry[0], entry[1])...)
	}
	command, err := cluster.PostgresCommand(ctx, config.Postgres, args...)
	if err != nil {
		return nil, err
	}
	var diagnostics postgresFailureOutput
	command.Stderr = &diagnostics
	if err := command.Start(); err != nil {
		return nil, errors.New("confined PostgreSQL startup failed")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		stop := exec.CommandContext(cleanup, filepath.Join(filepath.Dir(config.Postgres), "pg_ctl"), "-D", cluster.Directory(), "-m", "fast", "-w", "stop")
		stop.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
		var shutdownDiagnostics postgresFailureOutput
		stop.Stderr = &shutdownDiagnostics
		if err := stop.Run(); err != nil {
			resultErr = errors.Join(resultErr, postgresProcessFailure("pinned PostgreSQL shutdown failed", err, &shutdownDiagnostics))
			_ = command.Cancel()
		}
		select {
		case err := <-done:
			if err != nil && resultErr == nil {
				resultErr = errors.New("confined PostgreSQL did not shut down cleanly")
			}
		case <-time.After(20 * time.Second):
			_ = command.Cancel()
			<-done
			resultErr = errors.Join(resultErr, errors.New("confined PostgreSQL shutdown deadline exceeded"))
		}
		if resultErr == nil {
			resultErr = config.verifyStopped(ctx, cluster.Directory())
		}
	}()
	dial := func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}
	controlConfig, err := managedConnectionConfig(config.Native.ControlURL, config.Native.Roles.Control, config.Native.RootCA)
	if err != nil {
		return nil, err
	}
	controlConfig.DialFunc = dial
	controlConfig.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	var control *pgx.Conn
	for {
		select {
		case err := <-done:
			done <- err
			return nil, postgresProcessFailure("confined PostgreSQL stopped before readback", err, &diagnostics)
		default:
		}
		control, err = pgx.ConnectConfig(ctx, controlConfig)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("confined PostgreSQL readiness deadline exceeded")
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer control.Close(context.Background())
	for {
		var paused, recovering bool
		var target, timeline string
		if err := control.QueryRow(ctx, "SELECT pg_is_in_recovery(),pg_is_wal_replay_paused(),current_setting('recovery_target_lsn'),current_setting('recovery_target_timeline')").Scan(&recovering, &paused, &target, &timeline); err != nil {
			return nil, errors.New("restored PostgreSQL WAL frontier unavailable")
		}
		if !recovering || target != config.Frontier.TargetLSN || timeline != strconv.FormatUint(uint64(config.Frontier.Timeline), 10) {
			return nil, errors.New("restored PostgreSQL replay identity differs")
		}
		if paused {
			break
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("restored PostgreSQL did not pause at its retained WAL frontier")
		case <-time.After(100 * time.Millisecond):
		}
	}
	evidence, err := config.Native.verifyWithDial(ctx, dial)
	if err != nil {
		return nil, err
	}
	for _, point := range request.Points {
		result := providerrestore.DatabaseResult{DatabaseRole: point.DatabaseRole, ClusterIdentity: point.ClusterIdentity, DatabaseIdentity: point.DatabaseIdentity, RecoveryIdentity: point.RecoveryIdentity, StateDigest: evidence.ControlDigest}
		if point.DatabaseRole == recoveryset.DatabaseDuckLake {
			result.StateDigest = evidence.DuckLakeDigest
			catalog := evidence.Catalog
			result.Catalog = &catalog
		}
		results = append(results, result)
	}
	return results, nil
}

func shellLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (config PGNativeReadbackConfig) controlData(ctx context.Context, directory string) ([]byte, error) {
	command := exec.CommandContext(ctx, config.PGControlData, directory)
	command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
	var output boundedControlData
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return nil, errors.New("pinned PostgreSQL control identity read failed")
	}
	return output.Bytes(), nil
}

type boundedControlData struct{ bytes.Buffer }

func (output *boundedControlData) Write(value []byte) (int, error) {
	if output.Len()+len(value) > 16384 {
		return 0, errors.New("PostgreSQL control metadata exceeds bound")
	}
	return output.Buffer.Write(value)
}

func (config PGNativeReadbackConfig) verifySystemID(ctx context.Context, directory string) error {
	value, err := config.controlData(ctx, directory)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(value), "\n") {
		if suffix, ok := strings.CutPrefix(line, "Database system identifier:"); ok && strings.TrimSpace(suffix) == config.Frontier.SystemID {
			return nil
		}
	}
	return errors.New("restored PostgreSQL system identifier differs from retained cluster")
}

func (config PGNativeReadbackConfig) verifyStopped(ctx context.Context, directory string) error {
	value, err := config.controlData(ctx, directory)
	if err != nil {
		return err
	}
	var stopped, system bool
	var checkpointTimeline, minimumTimeline uint64
	for _, line := range strings.Split(string(value), "\n") {
		name, suffix, _ := strings.Cut(line, ":")
		switch name {
		case "Database cluster state":
			stopped = strings.TrimSpace(suffix) == "shut down in recovery"
		case "Database system identifier":
			system = strings.TrimSpace(suffix) == config.Frontier.SystemID
		case "Latest checkpoint's TimeLineID":
			checkpointTimeline, _ = strconv.ParseUint(strings.TrimSpace(suffix), 10, 32)
		case "Min recovery ending loc's timeline":
			minimumTimeline, _ = strconv.ParseUint(strings.TrimSpace(suffix), 10, 32)
		}
	}
	if minimumTimeline == 0 {
		minimumTimeline = checkpointTimeline
	}
	if !stopped || !system || minimumTimeline != uint64(config.Frontier.Timeline) {
		return errors.New("recovered PostgreSQL shutdown or durable cluster/timeline identity differs")
	}
	return nil
}
