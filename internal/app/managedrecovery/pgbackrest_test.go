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

func pgBackRestFixture(t *testing.T) (*PGBackRest, providerrestore.DatabaseRequest) {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "pgbackrest.conf")
	contents := []byte("[global]\nrepo1-path=/private/retained/repository\n")
	if err := os.WriteFile(file, contents, 0600); err != nil {
		t.Fatal(err)
	}
	frontier := PGFrontier{Stanza: "managed", BackupSet: "20261009-080000F", TargetLSN: "0/1700000", SystemID: "7620001234567890123"}
	identity, err := frontier.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	points := []recoveryset.ClusterRecoveryPoint{{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "postgres-system-id:" + frontier.SystemID, DatabaseIdentity: "control", RecoveryIdentity: identity}, {DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "postgres-system-id:" + frontier.SystemID, DatabaseIdentity: "ducklake", RecoveryIdentity: identity}}
	request := providerrestore.DatabaseRequest{TargetID: "target", RecoverySetID: "set", IdempotencyKey: "exact-pg-operation", Points: points, Catalog: recoveryset.CatalogCommit{CatalogID: "catalog", CatalogDatabase: "ducklake", CatalogUUID: "catalog-uuid", CatalogVersion: 3, SnapshotID: 4}}
	config := PGBackRestConfig{TargetID: "target", RecoverySetID: "set", Points: points, Frontier: frontier, PGBackRest: "/nix/store/pinned/bin/pgbackrest", ConfigFile: file, ConfigDigest: digestBytes(contents), Destination: filepath.Join(root, "replacement")}
	config.Readback = func(_ context.Context, path string, actual PGFrontier, request providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
		if actual != frontier {
			t.Fatal("readback selected another frontier")
		}
		if data, err := os.ReadFile(filepath.Join(path, "PG_VERSION")); err != nil || string(data) != "18" {
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
		if program != restorer.config.PGBackRest || lock == nil || !strings.Contains(strings.Join(args, " "), "--set=20261009-080000F") || !strings.Contains(strings.Join(args, " "), "--target=0/1700000 --target-action=pause") {
			t.Fatalf("wrong exact restore command: %v", args)
		}
		if _, err := os.Lstat(restorer.config.Destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("cluster exposed before readback")
		}
		stage := strings.TrimPrefix(args[3], "--pg1-path=")
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
		return os.WriteFile(filepath.Join(strings.TrimPrefix(args[3], "--pg1-path="), "PG_VERSION"), []byte("wrong"), 0600)
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
	restorer.config.Readback = func(context.Context, string, PGFrontier, providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
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
