package managedrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/flidai/leapview/internal/recoveryset"
)

// PGFrontier identifies an explicit retained backup and WAL frontier, never the
// repository's latest backup. SystemID must be checked against pg_control and
// the live restored cluster by the composition-owned verifier.
type PGFrontier struct {
	Stanza    string `json:"stanza"`
	BackupSet string `json:"backupSet"`
	TargetLSN string `json:"targetLsn"`
	SystemID  string `json:"systemId"`
}

func (frontier PGFrontier) RecoveryIdentity() (string, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(frontier.Stanza) ||
		!regexp.MustCompile(`^[0-9]{8}-[0-9]{6}F(_[0-9]{8}-[0-9]{6}[DI])?$`).MatchString(frontier.BackupSet) ||
		!regexp.MustCompile(`^[0-9A-F]{1,8}/[0-9A-F]{1,8}$`).MatchString(frontier.TargetLSN) ||
		!regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(frontier.SystemID) {
		return "", errors.New("exact pgBackRest backup/WAL/system identity required")
	}
	encoded, err := json.Marshal(frontier)
	if err != nil {
		return "", err
	}
	return "pgbackrest:" + digestBytes(encoded), nil
}

// PGReadback must start only the fenced staging cluster, verify the exact WAL
// replay/system identity and real application/catalog projection with TLS
// runtime roles, then stop it before returning. It grants no traffic admission.
type PGReadback func(context.Context, *PGStagingCluster, PGFrontier, providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error)

type PGBackRestConfig struct {
	TargetID      string
	RecoverySetID string
	Points        []recoveryset.ClusterRecoveryPoint
	Frontier      PGFrontier
	PGBackRest    string
	Bubblewrap    string
	ConfigFile    string
	ConfigDigest  string
	Destination   string
	Readback      PGReadback
}

type PGBackRest struct {
	config  PGBackRestConfig
	execute func(context.Context, string, []string, *os.File) error
}

func NewPGBackRest(config PGBackRestConfig) (*PGBackRest, error) {
	identity, err := config.Frontier.RecoveryIdentity()
	if err != nil || config.TargetID == "" || config.RecoverySetID == "" || len(config.Points) == 0 || len(config.Points) > 2 || config.Readback == nil {
		return nil, errors.New("exact managed PostgreSQL recovery configuration required")
	}
	seen := map[recoveryset.DatabaseRole]bool{}
	for _, point := range config.Points {
		if (point.DatabaseRole != recoveryset.DatabaseControl && point.DatabaseRole != recoveryset.DatabaseDuckLake) || seen[point.DatabaseRole] || point.ClusterIdentity != "postgres-system-id:"+config.Frontier.SystemID || point.RecoveryIdentity != identity || point.DatabaseIdentity == "" || strings.TrimSpace(point.DatabaseIdentity) != point.DatabaseIdentity {
			return nil, errors.New("managed PostgreSQL point differs from exact backup cluster")
		}
		seen[point.DatabaseRole] = true
	}
	if !pinnedProgram(config.PGBackRest) || !pinnedProgram(config.Bubblewrap) || !filepath.IsAbs(config.ConfigFile) || !filepath.IsAbs(config.Destination) || filepath.Clean(config.Destination) != config.Destination || config.Destination == "/" {
		return nil, errors.New("pinned pgBackRest and private canonical restore paths required")
	}
	value, err := securefs.ReadPrivateFile(config.ConfigFile)
	if err != nil || len(value) == 0 || digestBytes(value) != config.ConfigDigest {
		return nil, errors.New("retained private pgBackRest configuration differs")
	}
	if err := validatePGRestoreConfig(value); err != nil {
		return nil, err
	}
	config.Points = slices.Clone(config.Points)
	return &PGBackRest{config: config, execute: runPinnedRestore}, nil
}

func (restorer *PGBackRest) RestoreCluster(ctx context.Context, request providerrestore.DatabaseRequest) ([]providerrestore.DatabaseResult, error) {
	if restorer == nil || request.TargetID != restorer.config.TargetID || request.RecoverySetID != restorer.config.RecoverySetID || request.IdempotencyKey == "" || !samePGPoints(request.Points, restorer.config.Points) {
		return nil, errors.New("managed PostgreSQL request differs from retained frontier")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The private provider configuration is retained evidence, not ambient state.
	value, err := securefs.ReadPrivateFile(restorer.config.ConfigFile)
	if err != nil || digestBytes(value) != restorer.config.ConfigDigest {
		return nil, errors.New("retained pgBackRest configuration changed")
	}
	parent := filepath.Dir(restorer.config.Destination)
	lock, err := instancelock.AcquireNamed(parent, ".managed-pgbackrest.lock")
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	intentPath := filepath.Join(parent, ".managed-pgbackrest-"+filepath.Base(restorer.config.Destination)+".json")
	intent, err := json.Marshal(struct {
		Request      providerrestore.DatabaseRequest `json:"request"`
		Frontier     PGFrontier                      `json:"frontier"`
		ConfigDigest string                          `json:"configDigest"`
		Destination  string                          `json:"destination"`
	}{request, restorer.config.Frontier, restorer.config.ConfigDigest, restorer.config.Destination})
	if err != nil {
		return nil, err
	}
	if existing, err := securefs.ReadPrivateFile(intentPath); err == nil {
		if string(existing) != string(intent) {
			return nil, errors.New("managed PostgreSQL destination belongs to another operation")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("managed PostgreSQL restore intent unavailable")
	} else {
		if _, err := os.Lstat(restorer.config.Destination); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("unowned managed PostgreSQL destination exists or is inaccessible")
		}
		if err := securefs.WritePrivateFileAtomicOnce(intentPath, intent, 0600); err != nil {
			return nil, err
		}
	}
	started := time.Now().UTC()
	if info, err := os.Lstat(restorer.config.Destination); err == nil {
		if !info.IsDir() {
			return nil, errors.New("managed PostgreSQL destination is not a real directory")
		}
		return restorer.readback(ctx, restorer.config.Destination, request, started)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	stage, err := os.MkdirTemp(parent, ".managed-pgbackrest-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	args := []string{"--config=" + restorer.config.ConfigFile, "--stanza=" + restorer.config.Frontier.Stanza, "--set=" + restorer.config.Frontier.BackupSet, "--pg1-path=" + stage, "--type=lsn", "--target=" + restorer.config.Frontier.TargetLSN, "--target-action=pause", "--tablespace-map-all=" + filepath.Join(stage, ".managed-tablespaces"), "--no-link-all", "--log-level-console=off", "--log-level-file=off", "restore"}
	cluster := &PGStagingCluster{directory: stage, bubblewrap: restorer.config.Bubblewrap}
	args = append(cluster.arguments(), append([]string{restorer.config.PGBackRest}, args...)...)
	if err := restorer.execute(ctx, restorer.config.Bubblewrap, args, lock.InheritedFile()); err != nil {
		return nil, err
	}
	results, err := restorer.readback(ctx, stage, request, started)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, restorer.config.Destination); err != nil {
		return nil, err
	}
	dir, err := os.Open(parent)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return nil, err
	}
	return results, nil
}

func (restorer *PGBackRest) readback(ctx context.Context, path string, request providerrestore.DatabaseRequest, started time.Time) ([]providerrestore.DatabaseResult, error) {
	if err := validateDefaultPGLayout(path); err != nil {
		return nil, err
	}
	results, err := restorer.config.Readback(ctx, &PGStagingCluster{directory: path, bubblewrap: restorer.config.Bubblewrap}, restorer.config.Frontier, request)
	if err != nil {
		return nil, err
	}
	// WAL can introduce a tablespace absent from the retained base backup.
	// Recheck after the confined server is stopped, before exposing PGDATA.
	if err := validateDefaultPGLayout(path); err != nil {
		return nil, err
	}
	if len(results) != len(request.Points) {
		return nil, errors.New("managed PostgreSQL verification omitted a database")
	}
	for _, point := range request.Points {
		matches := 0
		for index, result := range results {
			if result.DatabaseRole == point.DatabaseRole && result.ClusterIdentity == point.ClusterIdentity && result.DatabaseIdentity == point.DatabaseIdentity && result.RecoveryIdentity == point.RecoveryIdentity {
				if !strings.HasPrefix(result.StateDigest, "sha256:") || len(result.StateDigest) != 71 || strings.Trim(result.StateDigest[7:], "0123456789abcdef") != "" {
					return nil, errors.New("managed PostgreSQL state proof invalid")
				}
				if point.DatabaseRole == recoveryset.DatabaseDuckLake && (result.Catalog == nil || *result.Catalog != request.Catalog) {
					return nil, errors.New("managed PostgreSQL catalog differs from authoritative snapshot")
				}
				results[index].Provider = "pgbackrest-managed"
				results[index].OperationID = request.IdempotencyKey
				results[index].StartedAt = started
				results[index].CompletedAt = time.Now().UTC()
				matches++
			}
		}
		if matches != 1 {
			return nil, errors.New("managed PostgreSQL verification differs from immutable cluster identity")
		}
	}
	return results, nil
}

func samePGPoints(a, b []recoveryset.ClusterRecoveryPoint) bool {
	if len(a) != len(b) {
		return false
	}
	for _, point := range a {
		matches := 0
		for _, candidate := range a {
			if candidate == point {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
		matches = 0
		for _, candidate := range b {
			if candidate == point {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	return true
}
