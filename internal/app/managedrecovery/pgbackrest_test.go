package managedrecovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
)

func TestPGFrontierRequiresRetainedTimeline(t *testing.T) {
	frontier := PGFrontier{Stanza: "managed", BackupSet: "20261009-080000F", TargetLSN: "0/1700000", SystemID: "7620001234567890123"}
	if _, err := frontier.RecoveryIdentity(); err == nil {
		t.Fatal("missing retained WAL timeline accepted")
	}
	frontier.Timeline = 1
	first, err := frontier.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	frontier.Timeline = 2
	second, err := frontier.RecoveryIdentity()
	if err != nil || first == second {
		t.Fatal("retained WAL timelines share recovery identity")
	}
}

func pgBackRestFixture(t *testing.T) (*PGBackRest, providerrestore.DatabaseRequest) {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "pgbackrest.conf")
	contents := []byte("[global]\nrepo1-path=/private/retained/repository\n")
	if err := os.WriteFile(file, contents, 0600); err != nil {
		t.Fatal(err)
	}
	frontier := PGFrontier{Stanza: "managed", BackupSet: "20261009-080000F", TargetLSN: "0/1700000", SystemID: "7620001234567890123", Timeline: 1}
	identity, err := frontier.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	points := []recoveryset.ClusterRecoveryPoint{{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "postgres-system-id:" + frontier.SystemID, DatabaseIdentity: "control", RecoveryIdentity: identity}, {DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "postgres-system-id:" + frontier.SystemID, DatabaseIdentity: "ducklake", RecoveryIdentity: identity}}
	request := providerrestore.DatabaseRequest{TargetID: "target", RecoverySetID: "set", IdempotencyKey: "exact-pg-operation", Points: points, Catalog: recoveryset.CatalogCommit{CatalogID: "catalog", CatalogDatabase: "ducklake", CatalogUUID: "catalog-uuid", CatalogVersion: 3, SnapshotID: 4}}
	config := PGBackRestConfig{TargetID: "target", RecoverySetID: "set", Points: points, Frontier: frontier, PGBackRest: "/nix/store/pinned/bin/pgbackrest", Bubblewrap: "/nix/store/pinned/bin/bwrap", ConfigFile: file, ConfigDigest: digestBytes(contents), Destination: filepath.Join(root, "replacement")}
	config.Readback = func(_ context.Context, cluster *PGStagingCluster, actual PGFrontier, request providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
		if actual != frontier {
			t.Fatal("readback selected another frontier")
		}
		if data, err := os.ReadFile(filepath.Join(cluster.Directory(), "PG_VERSION")); err != nil || string(data) != "18" {
			return nil, errors.New("cluster not verified")
		}
		results := []providerrestore.DatabaseResult{}
		for _, point := range request.Points {
			result := providerrestore.DatabaseResult{DatabaseRole: point.DatabaseRole, ClusterIdentity: point.ClusterIdentity, DatabaseIdentity: point.DatabaseIdentity, RecoveryIdentity: point.RecoveryIdentity, StateDigest: "sha256:" + strings.Repeat("a", 64)}
			if point.DatabaseRole == recoveryset.DatabaseDuckLake {
				catalog := request.Catalog
				result.Catalog = &catalog
			}
			results = append(results, result)
		}
		return results, nil
	}
	restorer, err := NewPGBackRest(config)
	if err != nil {
		t.Fatal(err)
	}
	return restorer, request
}

func TestPGBackRestRequiresExactCommandAndVerifiedReadbackBeforeExposure(t *testing.T) {
	restorer, request := pgBackRestFixture(t)
	calls := 0
	restorer.execute = func(_ context.Context, program string, args []string, lock *os.File) error {
		calls++
		if program != restorer.config.Bubblewrap || lock == nil || !strings.Contains(strings.Join(args, " "), "--set=20261009-080000F") || !strings.Contains(strings.Join(args, " "), "--target=0/1700000 --target-timeline=1 --target-action=pause") {
			t.Fatalf("wrong exact restore command: %v", args)
		}
		if _, err := os.Lstat(restorer.config.Destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cluster exposed before readback")
		}
		stage := pgStageFromArgs(t, args)
		if err := os.Mkdir(filepath.Join(stage, "pg_tblspc"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(stage, "PG_VERSION"), []byte("18"), 0600)
	}
	results, err := restorer.RestoreCluster(t.Context(), request)
	if err != nil || len(results) != 2 {
		t.Fatalf("restore: %v", err)
	}
	if _, err := restorer.RestoreCluster(t.Context(), request); err != nil || calls != 1 {
		t.Fatalf("replay duplicated provider effect: %v", err)
	}
	foreign := request
	foreign.IdempotencyKey = "foreign"
	if _, err := restorer.RestoreCluster(t.Context(), foreign); err == nil {
		t.Fatal("another operation adopted restored cluster")
	}
}

func TestPGBackRestRejectsWrongFrontierConfigurationAndReadback(t *testing.T) {
	restorer, request := pgBackRestFixture(t)
	calls := 0
	restorer.execute = func(_ context.Context, _ string, args []string, _ *os.File) error {
		calls++
		return os.WriteFile(filepath.Join(pgStageFromArgs(t, args), "PG_VERSION"), []byte("wrong"), 0600)
	}
	wrong := request
	wrong.Points = append([]recoveryset.ClusterRecoveryPoint{}, request.Points...)
	wrong.Points[0].RecoveryIdentity = "latest"
	if _, err := restorer.RestoreCluster(t.Context(), wrong); err == nil || calls != 0 {
		t.Fatal("wrong frontier reached provider")
	}
	if _, err := restorer.RestoreCluster(t.Context(), request); err == nil {
		t.Fatal("invalid restored cluster accepted")
	}
	if _, err := os.Lstat(restorer.config.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unverified cluster exposed")
	}
	if err := os.WriteFile(restorer.config.ConfigFile, []byte("changed retained repository"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restorer.RestoreCluster(t.Context(), request); err == nil || calls != 1 {
		t.Fatal("changed provider configuration reached restore")
	}
}

func TestPGBackRestRejectsIncompleteReadbackAndDuplicateRoles(t *testing.T) {
	restorer, request := pgBackRestFixture(t)
	config := restorer.config
	config.Points = append([]recoveryset.ClusterRecoveryPoint{}, config.Points...)
	config.Points[1] = config.Points[0]
	if _, err := NewPGBackRest(config); err == nil {
		t.Fatal("duplicate database roles accepted")
	}
	restorer.config.Readback = func(context.Context, *PGStagingCluster, PGFrontier, providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
		return nil, nil
	}
	restorer.execute = func(context.Context, string, []string, *os.File) error { return nil }
	if _, err := restorer.RestoreCluster(t.Context(), request); err == nil {
		t.Fatal("missing readback accepted")
	}
	if _, err := os.Lstat(restorer.config.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("incomplete readback exposed")
	}
}

func pgStageFromArgs(t *testing.T, args []string) string {
	t.Helper()
	for _, arg := range args {
		if strings.HasPrefix(arg, "--pg1-path=") {
			return strings.TrimPrefix(arg, "--pg1-path=")
		}
	}
	t.Fatal("confined stage argument missing")
	return ""
}
