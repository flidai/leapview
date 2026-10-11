package managedrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
)

const moduleRestoreHelper = "/run/current-system/sw/bin/leapview-postgres-restore"
const moduleRestoreReceiptPath = "/var/lib/leapview-recovery-adoption/postgresql-restore.json"

type moduleRestoreRequest struct {
	SchemaVersion        int    `json:"schemaVersion"`
	TargetID             string `json:"targetID"`
	RecoverySetID        string `json:"recoverySetID"`
	FrontierDigest       string `json:"frontierDigest"`
	OccurrenceID         string `json:"occurrenceID"`
	OperationID          string `json:"operationID"`
	BackupSet            string `json:"backupSet"`
	SourceMachineID      string `json:"sourceMachineID"`
	ReplacementMachineID string `json:"replacementMachineID"`
	SystemIdentifier     string `json:"systemIdentifier"`
	TargetLSN            string `json:"targetLSN"`
	Timeline             uint32 `json:"timeline"`
	DataDirectoryDigest  string `json:"dataDirectoryDigest"`
}

type moduleRestoreReceipt struct {
	SchemaVersion               int       `json:"schemaVersion"`
	Kind                        string    `json:"kind"`
	Status                      string    `json:"status"`
	TargetID                    string    `json:"targetID"`
	RecoverySetID               string    `json:"recoverySetID"`
	FrontierDigest              string    `json:"frontierDigest"`
	OccurrenceID                string    `json:"occurrenceID"`
	OperationID                 string    `json:"operationID"`
	BackupSet                   string    `json:"backupSet"`
	ReplacementMachineIDDigest  string    `json:"replacementMachineIDDigest"`
	SystemIdentifier            string    `json:"systemIdentifier"`
	TargetLSN                   string    `json:"targetLSN"`
	Timeline                    uint32    `json:"timeline"`
	DataDirectoryDigest         string    `json:"dataDirectoryDigest"`
	Port                        uint16    `json:"port"`
	ModuleGenerationDigest      string    `json:"moduleGenerationDigest"`
	ConfigurationDigest         string    `json:"configurationDigest"`
	ReplayedThroughLSN          string    `json:"replayedThroughLSN"`
	RestoredAt                  time.Time `json:"restoredAt"`
	ActivationQualified         bool      `json:"activationQualified"`
	FullManagedProfileQualified bool      `json:"fullManagedProfileQualified"`
}

type moduleRestoreOperations struct {
	invoke     func(context.Context, string, moduleRestoreRequest) error
	receipt    func() (moduleRestoreReceipt, []byte, error)
	machine    func() (string, error)
	generation func() (string, error)
	verify     func(context.Context, uint16) (NativePostgresEvidence, error)
	now        func() time.Time
}

type modulePostgres struct {
	config     ManagedConfig
	set        recoveryset.RecoverySet
	verifyOnly bool
	operations moduleRestoreOperations
}

func newModulePostgres(config ManagedConfig, set recoveryset.RecoverySet) (*modulePostgres, error) {
	p, readback := config.Postgres, config.Readback
	identity, err := p.Frontier.RecoveryIdentity()
	if err != nil || config.PostgresProvider != "module-owned" || p.Frontier.Stanza != "default" || p.TargetID != set.Delivery.TargetID || p.RecoverySetID != set.ID || config.OccurrenceID == "" || !validContentDigest(set.FrontierDigest) || set.Validate() != nil || !samePGPoints(p.Points, set.ClusterPoints) || p.Frontier != readback.Frontier || len(p.Points) != 2 || len(config.PrimaryFence.Primaries) != 1 || !validQualificationMachineID(config.PrimaryFence.Primaries[0].MachineID) || config.PrimaryFence.Primaries[0].SystemIdentifier != p.Frontier.SystemID || config.Enrollment.Request.SourceSystemID != p.Frontier.SystemID || !filepath.IsAbs(p.Destination) || filepath.Clean(p.Destination) != p.Destination || p.Destination == "/" {
		return nil, errors.New("module-owned restore requires the exact enrolled authoritative PostgreSQL frontier")
	}
	if p.PGBackRest != "" || p.Bubblewrap != "" || p.ConfigFile != "" || p.ConfigDigest != "" || p.Readback != nil || readback.Postgres != "" || readback.PGControlData != "" || readback.PGBackRest != "" || readback.ProviderConfigFile != "" || readback.ProviderConfigDigest != "" || readback.ServerCertificateFile != "" || readback.ServerCertificateDigest != "" || readback.ServerKeyFile != "" || readback.ServerKeyDigest != "" {
		return nil, errors.New("module-owned restore forbids caller tools, provider configuration and service TLS keys")
	}
	seen := map[recoveryset.DatabaseRole]bool{}
	for _, point := range p.Points {
		if (point.DatabaseRole != recoveryset.DatabaseControl && point.DatabaseRole != recoveryset.DatabaseDuckLake) || seen[point.DatabaseRole] || point.ClusterIdentity != "postgres-system-id:"+p.Frontier.SystemID || point.RecoveryIdentity != identity {
			return nil, errors.New("module-owned PostgreSQL points differ from the exact backup frontier")
		}
		seen[point.DatabaseRole] = true
	}
	native := readback.Native
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`).MatchString(native.MetadataSchema) || !native.Set.IdentityEqual(set) {
		return nil, errors.New("module-owned native readback differs from exact managed set")
	}
	for _, endpoint := range [][2]string{{native.ControlURL, native.Roles.Control}, {native.DuckLakeURL, native.Roles.DuckLake}} {
		if _, err := managedConnectionConfig(endpoint[0], endpoint[1], native.RootCA); err != nil {
			return nil, err
		}
	}
	restorer := &modulePostgres{config: config, set: set, verifyOnly: set.Status == recoveryset.StatusPublished}
	restorer.operations = moduleRestoreOperations{
		invoke: invokeModuleRestore,
		receipt: func() (moduleRestoreReceipt, []byte, error) {
			var receipt moduleRestoreReceipt
			raw, err := readRootRecoveryReceipt(moduleRestoreReceiptPath, &receipt)
			return receipt, raw, err
		},
		machine:    func() (string, error) { return readQualificationMachineID("/etc/machine-id") },
		generation: func() (string, error) { return filepath.EvalSymlinks("/run/current-system") },
		verify: func(ctx context.Context, port uint16) (NativePostgresEvidence, error) {
			return native.verifyWithDial(ctx, loopbackPromotionDial(port))
		},
		now: time.Now,
	}
	return restorer, nil
}

func moduleRequest(config ManagedConfig, set recoveryset.RecoverySet, operation, machine string) (moduleRestoreRequest, error) {
	if len(config.PrimaryFence.Primaries) != 1 || !validQualificationMachineID(machine) || !validQualificationMachineID(config.PrimaryFence.Primaries[0].MachineID) || machine == config.PrimaryFence.Primaries[0].MachineID || operation == "" || config.Postgres.Frontier.Stanza != "default" {
		return moduleRestoreRequest{}, errors.New("module restore requires the actual distinct replacement and exact operation")
	}
	frontier := config.Postgres.Frontier
	return moduleRestoreRequest{1, set.Delivery.TargetID, set.ID, set.FrontierDigest, config.OccurrenceID, operation, frontier.BackupSet, config.PrimaryFence.Primaries[0].MachineID, machine, frontier.SystemID, frontier.TargetLSN, frontier.Timeline, digestBytes([]byte("leapview-managed-recovery-data:" + config.Postgres.Destination))}, nil
}

// The owner cannot traverse postgres-owned PGDATA. Authenticate freshness
// through the fixed module helper, which checks absent data, stopped service,
// retained intent and exact module-owned path without restoring or starting it.
func requireFreshModulePostgres(ctx context.Context, config ManagedConfig) error {
	machine, err := readQualificationMachineID("/etc/machine-id")
	set := recoveryset.RecoverySet{ID: config.RecoverySetID, FrontierDigest: config.Enrollment.Request.FrontierDigest}
	set.Delivery.TargetID = config.Credentials.TargetID
	request, requestErr := moduleRequest(config, set, "fresh:"+config.OccurrenceID, machine)
	if err != nil || requestErr != nil {
		return errors.New("module freshness requires actual exact replacement identity")
	}
	return invokeModuleRestore(ctx, "fresh", request)
}

func replayReachedTarget(replay, target string) bool {
	parse := func(value string) (uint64, bool) {
		if !regexp.MustCompile(`^[0-9A-F]{1,8}/[0-9A-F]{1,8}$`).MatchString(value) {
			return 0, false
		}
		parts := strings.Split(value, "/")
		high, err := strconv.ParseUint(parts[0], 16, 32)
		low, lowErr := strconv.ParseUint(parts[1], 16, 32)
		return high<<32 | low, err == nil && lowErr == nil
	}
	position, ok := parse(replay)
	frontier, targetOK := parse(target)
	return ok && targetOK && position >= frontier
}

func invokeModuleRestore(ctx context.Context, action string, request moduleRestoreRequest) error {
	switch action {
	case "fresh", "restore", "check", "start-readback", "stop-readback":
	default:
		return errors.New("unsupported module restore action")
	}
	resolved, err := filepath.EvalSymlinks(moduleRestoreHelper)
	if err != nil || !pinnedProgram(resolved) || filepath.Base(resolved) != "leapview-postgres-restore" {
		return errors.New("module-owned fixed restore helper unavailable")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return errors.New("exact module restore request cannot be encoded")
	}
	command := exec.CommandContext(ctx, "/run/wrappers/bin/sudo", "-n", "--", moduleRestoreHelper, action)
	command.Stdin = bytes.NewReader(raw)
	command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
	if command.Run() != nil {
		return errors.New("configured PostgreSQL restore action failed; keep application activation closed")
	}
	return nil
}

func (restorer *modulePostgres) validateReceipt(receipt moduleRestoreReceipt, request moduleRestoreRequest) error {
	machine, machineErr := restorer.operations.machine()
	generation, generationErr := restorer.operations.generation()
	if machineErr != nil || generationErr != nil || machine != request.ReplacementMachineID || !strings.HasPrefix(generation, "/nix/store/") || receipt.SchemaVersion != 1 || receipt.Kind != "leapview/module-postgresql-restore" || receipt.Status != "restored-stopped" || receipt.TargetID != request.TargetID || receipt.RecoverySetID != request.RecoverySetID || receipt.FrontierDigest != request.FrontierDigest || receipt.OccurrenceID != request.OccurrenceID || receipt.OperationID != request.OperationID || receipt.BackupSet != request.BackupSet || receipt.SystemIdentifier != request.SystemIdentifier || receipt.TargetLSN != request.TargetLSN || receipt.Timeline != request.Timeline || receipt.DataDirectoryDigest != request.DataDirectoryDigest || receipt.ReplacementMachineIDDigest != digestBytes([]byte("leapview-managed-recovery-qualification:"+machine)) || receipt.ModuleGenerationDigest != digestBytes([]byte("leapview-managed-recovery-module:"+generation)) || !validContentDigest(receipt.ConfigurationDigest) || receipt.Port == 0 || !replayReachedTarget(receipt.ReplayedThroughLSN, request.TargetLSN) || receipt.RestoredAt.IsZero() || receipt.RestoredAt.After(restorer.operations.now()) || receipt.ActivationQualified || receipt.FullManagedProfileQualified {
		return errors.New("root restore receipt differs from the current replacement and exact frontier")
	}
	return nil
}

func (restorer *modulePostgres) RestoreCluster(ctx context.Context, request providerrestore.DatabaseRequest) (results []providerrestore.DatabaseResult, resultErr error) {
	if restorer == nil || ctx.Err() != nil || request.TargetID != restorer.set.Delivery.TargetID || request.RecoverySetID != restorer.set.ID || request.IdempotencyKey == "" || !samePGPoints(request.Points, restorer.set.ClusterPoints) || request.Catalog != restorer.set.Catalog {
		return nil, errors.New("module PostgreSQL request differs from retained authority")
	}
	machine, err := restorer.operations.machine()
	wanted, requestErr := moduleRequest(restorer.config, restorer.set, request.IdempotencyKey, machine)
	if err != nil || requestErr != nil {
		return nil, errors.New("actual replacement identity unavailable")
	}
	started := restorer.operations.now().UTC()
	action := "restore"
	if restorer.verifyOnly {
		action = "check"
	}
	if restorer.operations.invoke(ctx, action, wanted) != nil {
		return nil, errors.New("exact root PostgreSQL restore unavailable")
	}
	if restorer.operations.invoke(ctx, "check", wanted) != nil {
		return nil, errors.New("root restored PostgreSQL is not stopped")
	}
	receipt, raw, err := restorer.operations.receipt()
	if err != nil || restorer.validateReceipt(receipt, wanted) != nil {
		return nil, errors.New("exact completed root restore receipt unavailable")
	}
	// Stop even after a failed start/readback, using a bounded uncancelled
	// context. Cleanup failure can invalidate success, never mask the first
	// failure or return provider stderr/credentials.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if restorer.operations.invoke(cleanup, "stop-readback", wanted) != nil && resultErr == nil {
			results, resultErr = nil, errors.New("module PostgreSQL readback cleanup failed")
		}
		if resultErr != nil {
			return
		}
		if restorer.operations.invoke(cleanup, "check", wanted) != nil {
			results, resultErr = nil, errors.New("module PostgreSQL stopped recheck failed")
			return
		}
		current, currentRaw, err := restorer.operations.receipt()
		if err != nil || restorer.validateReceipt(current, wanted) != nil || !reflect.DeepEqual(current, receipt) || !bytes.Equal(currentRaw, raw) {
			results, resultErr = nil, errors.New("root restore receipt changed during native readback")
		}
	}()
	readback, cancelReadback := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelReadback()
	if restorer.operations.invoke(readback, "start-readback", wanted) != nil {
		return nil, errors.New("module PostgreSQL private readback failed to start")
	}
	evidence, err := restorer.operations.verify(readback, receipt.Port)
	if err != nil || readback.Err() != nil || !validContentDigest(evidence.ControlDigest) || !validContentDigest(evidence.DuckLakeDigest) || evidence.Catalog != request.Catalog {
		return nil, errors.New("module PostgreSQL native TLS state differs from retained frontier")
	}
	completed := restorer.operations.now().UTC()
	if completed.Before(started) {
		return nil, errors.New("module PostgreSQL readback clock moved before its start")
	}
	for _, point := range request.Points {
		result := providerrestore.DatabaseResult{Provider: "pgbackrest-managed", OperationID: request.IdempotencyKey, DatabaseRole: point.DatabaseRole, ClusterIdentity: point.ClusterIdentity, DatabaseIdentity: point.DatabaseIdentity, RecoveryIdentity: point.RecoveryIdentity, StateDigest: evidence.ControlDigest, StartedAt: started, CompletedAt: completed}
		if point.DatabaseRole == recoveryset.DatabaseDuckLake {
			result.StateDigest = evidence.DuckLakeDigest
			catalog := evidence.Catalog
			result.Catalog = &catalog
		}
		results = append(results, result)
	}
	return results, nil
}
