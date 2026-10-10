package managedrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const managedPromotionReceiptPath = "/var/lib/leapview-recovery-adoption/postgresql-promotion.json"

// ManagedAdoptionInput names an explicit private maintenance credential. The
// source cluster identity must be retained; OpenManagedAuthority intentionally
// cannot be used for a replacement physically restored from the source.
type ManagedAdoptionInput struct {
	SchemaVersion int            `json:"schemaVersion"`
	Maintenance   AuthorityInput `json:"maintenance"`
}

// ManagedPromotionReceipt is produced by the module's root promotion helper.
// Its owner, immutable generation, current local machine and live loopback
// service are verified independently; an operator-supplied promoted boolean
// never supplies this authority.
type ManagedPromotionReceipt struct {
	SchemaVersion               int       `json:"schemaVersion"`
	Kind                        string    `json:"kind"`
	Status                      string    `json:"status"`
	TargetID                    string    `json:"targetID"`
	RecoverySetID               string    `json:"recoverySetID"`
	FrontierDigest              string    `json:"frontierDigest"`
	ReplacementMachineIDDigest  string    `json:"replacementMachineIDDigest"`
	SystemIdentifier            string    `json:"systemIdentifier"`
	TargetLSN                   string    `json:"targetLSN"`
	Timeline                    uint32    `json:"timeline"`
	DataDirectoryDigest         string    `json:"dataDirectoryDigest"`
	ModuleGenerationDigest      string    `json:"moduleGenerationDigest"`
	ConfigurationDigest         string    `json:"configurationDigest"`
	ReplayedThroughLSN          string    `json:"replayedThroughLSN"`
	PromotedTimeline            uint32    `json:"promotedTimeline"`
	Port                        uint16    `json:"port"`
	PromotedAt                  time.Time `json:"promotedAt"`
	ActivationQualified         bool      `json:"activationQualified"`
	FullManagedProfileQualified bool      `json:"fullManagedProfileQualified"`
}

type ManagedAdoptionEvidence struct {
	SchemaVersion               int       `json:"schemaVersion"`
	Kind                        string    `json:"kind"`
	Status                      string    `json:"status"`
	TargetID                    string    `json:"targetId"`
	RecoverySetID               string    `json:"recoverySetId"`
	FrontierDigest              string    `json:"frontierDigest"`
	ValidationAttemptID         string    `json:"validationAttemptId"`
	ValidationDigest            string    `json:"validationDigest"`
	ReportSHA256                string    `json:"reportSha256"`
	PromotionSHA256             string    `json:"promotionSha256"`
	OriginalsFenced             bool      `json:"originalsFenced"`
	ActivationQualified         bool      `json:"activationQualified"`
	FullManagedProfileQualified bool      `json:"fullManagedProfileQualified"`
	AdoptedAt                   time.Time `json:"adoptedAt"`
}

func ReadManagedAdoptionInput(path string) (ManagedAdoptionInput, error) {
	raw, err := readBoundedManagedPrivateFile(path, maxManagedCredentialsBytes)
	var input ManagedAdoptionInput
	if err != nil || strictjson.DecodeWithOptions(raw, &input, strictjson.Options{MaxBytes: maxManagedCredentialsBytes}) != nil || input.SchemaVersion != 1 || input.Maintenance.URLFile == "" || input.Maintenance.RootCAFile == "" || input.Maintenance.Role == "" || input.Maintenance.SystemIdentifier == "" {
		return ManagedAdoptionInput{}, errors.New("explicit bounded private replacement maintenance input required")
	}
	return input, nil
}

func readRootPromotionReceipt(path string) (ManagedPromotionReceipt, []byte, error) {
	var receipt ManagedPromotionReceipt
	raw, err := readRootRecoveryReceipt(path, &receipt)
	return receipt, raw, err
}

func readRootRecoveryReceipt(path string, receipt any) ([]byte, error) {
	fail := func() ([]byte, error) {
		return nil, errors.New("authenticated root-owned promotion receipt unavailable")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fail()
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || !managedRootOwned(info) {
			return fail()
		}
		if parent == "/" {
			break
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0640 || !managedRootOwned(info) || info.Size() <= 0 || info.Size() > 16384 {
		return fail()
	}
	file, err := os.Open(path)
	if err != nil {
		return fail()
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fail()
	}
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	after, statErr := file.Stat()
	current, currentErr := os.Lstat(path)
	if err != nil || statErr != nil || currentErr != nil || !os.SameFile(info, current) || !managedRootOwned(current) || current.Mode().Perm() != 0640 || current.Size() != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || len(raw) != int(info.Size()) || strictjson.DecodeWithOptions(raw, receipt, strictjson.Options{MaxBytes: 16384}) != nil {
		return fail()
	}
	return raw, nil
}

func validatePromotionReceipt(receipt ManagedPromotionReceipt, config ManagedConfig, set recoveryset.RecoverySet, machine, generation string, now time.Time) error {
	frontier := config.Postgres.Frontier
	identity, err := frontier.RecoveryIdentity()
	if err != nil || frontier != config.Readback.Frontier || !validQualificationMachineID(machine) || len(config.PrimaryFence.Primaries) != 1 || machine == config.PrimaryFence.Primaries[0].MachineID || config.PrimaryFence.Primaries[0].SystemIdentifier != frontier.SystemID || frontier.SystemID != config.Enrollment.Request.SourceSystemID || receipt.SchemaVersion != 1 || receipt.Kind != "leapview/managed-postgresql-promotion" || receipt.Status != "promoted-loopback-only" || receipt.TargetID != set.Delivery.TargetID || receipt.RecoverySetID != set.ID || receipt.FrontierDigest != set.FrontierDigest || receipt.SystemIdentifier != frontier.SystemID || receipt.TargetLSN != frontier.TargetLSN || receipt.Timeline != frontier.Timeline || receipt.PromotedTimeline <= receipt.Timeline || receipt.PromotedTimeline != receipt.Timeline+1 || receipt.ReplayedThroughLSN == "" || !validContentDigest(receipt.ConfigurationDigest) || receipt.Port == 0 || receipt.ActivationQualified || receipt.FullManagedProfileQualified || receipt.PromotedAt.IsZero() || receipt.PromotedAt.After(now) || receipt.ReplacementMachineIDDigest != digestBytes([]byte("leapview-managed-recovery-qualification:"+machine)) || receipt.DataDirectoryDigest != digestBytes([]byte("leapview-managed-recovery-data:"+config.Postgres.Destination)) || !strings.HasPrefix(generation, "/nix/store/") || receipt.ModuleGenerationDigest != digestBytes([]byte("leapview-managed-recovery-module:"+generation)) {
		return errors.New("promotion receipt differs from actual replacement and exact enrolled frontier")
	}
	for _, point := range set.ClusterPoints {
		if point.ClusterIdentity != "postgres-system-id:"+frontier.SystemID || point.RecoveryIdentity != identity {
			return errors.New("promoted service does not bind the published PostgreSQL frontier")
		}
	}
	return nil
}

type adoptionOperations struct {
	service func(context.Context, recoveryset.RecoverySet) (ManagedPromotionReceipt, []byte, error)
	verify  func(context.Context, recoveryset.RecoverySet) (NativePostgresEvidence, error)
	adopt   func(context.Context, recoveryset.RecoverySet, recoveryset.ValidationAttempt, recoveryset.ValidationResult, string, func() error) error
	now     func() time.Time
}

// AdoptManagedRecovery atomically imports the independent publication into
// restored local control storage, with exact live native state and a fresh
// original fence at both transaction boundaries. The caller holds the instance
// home lock. PostgreSQL stays loopback-only; no application process is started.
func AdoptManagedRecovery(ctx context.Context, input ManagedInput, config ManagedConfig, authority ManagedAuthorities, replacement ManagedAdoptionInput) (ManagedAdoptionEvidence, error) {
	if validateAdoptionInputs(ctx, input, config) != nil || replacement.SchemaVersion != 1 {
		return ManagedAdoptionEvidence{}, errors.New("adoption requires exact enrolled inputs and explicit replacement credentials")
	}
	if _, err := replacementMaintenanceConfiguration(replacement.Maintenance, config); err != nil {
		return ManagedAdoptionEvidence{}, err
	}
	snapshot, err := readManagedAdmission(ctx, config, authority)
	if err != nil {
		return ManagedAdoptionEvidence{}, err
	}
	// The exact completed readback is already authoritative. Check retained
	// files and original exclusion again before the narrowly privileged helper
	// can cross its irreversible promotion boundary.
	fence, err := providerrestore.NewSSHPrimaryFence(config.PrimaryFence)
	if err != nil || fence.Verify(ctx, snapshot.set) != nil || verifyAdoptionRoots(ctx, config, snapshot) != nil {
		return ManagedAdoptionEvidence{}, errors.New("promotion requires exact completed recovery files and a fresh original fence")
	}
	if err := promoteConfiguredReplacement(ctx, config, snapshot.set, false); err != nil {
		return ManagedAdoptionEvidence{}, err
	}
	if fence.Verify(ctx, snapshot.set) != nil {
		return ManagedAdoptionEvidence{}, errors.New("original fence lost during promotion; keep replacement loopback-only")
	}
	receipt, _, err := readRootPromotionReceipt(managedPromotionReceiptPath)
	if err != nil {
		return ManagedAdoptionEvidence{}, err
	}
	pool, err := openReplacementMaintenance(ctx, replacement.Maintenance, config, receipt)
	if err != nil {
		return ManagedAdoptionEvidence{}, err
	}
	defer pool.Close()
	native := config.Readback.Native
	native.Set, native.ControlURL, native.DuckLakeURL, native.RootCA, native.Roles, native.Credentials = snapshot.set, config.Credentials.ControlURL, config.Credentials.DuckLakeURL, config.Credentials.PostgresRootCA, config.Roles, &config.Credentials
	dial := loopbackPromotionDial(receipt.Port)
	return adoptManagedRecovery(ctx, input, config, authority, adoptionOperations{
		service: func(ctx context.Context, set recoveryset.RecoverySet) (ManagedPromotionReceipt, []byte, error) {
			if err := promoteConfiguredReplacement(ctx, config, set, true); err != nil {
				return ManagedPromotionReceipt{}, nil, err
			}
			current, raw, err := readRootPromotionReceipt(managedPromotionReceiptPath)
			machine, machineErr := readQualificationMachineID("/etc/machine-id")
			generation, generationErr := filepath.EvalSymlinks("/run/current-system")
			if err != nil || machineErr != nil || generationErr != nil || validatePromotionReceipt(current, config, set, machine, generation, time.Now()) != nil || verifyReplacementService(ctx, pool, config, current) != nil {
				return ManagedPromotionReceipt{}, nil, errors.New("live replacement service no longer binds the authenticated promotion receipt")
			}
			return current, raw, nil
		},
		verify: func(ctx context.Context, set recoveryset.RecoverySet) (NativePostgresEvidence, error) {
			if !set.IdentityEqual(native.Set) || verifyAdoptionRoots(ctx, config, snapshot) != nil {
				return NativePostgresEvidence{}, errors.New("promoted replacement files differ from exact retained roots")
			}
			return native.verifyWithDial(ctx, dial)
		},
		adopt: func(ctx context.Context, set recoveryset.RecoverySet, attempt recoveryset.ValidationAttempt, result recoveryset.ValidationResult, publisher string, revalidate func() error) error {
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
			if err != nil {
				return errors.New("replacement adoption transaction unavailable")
			}
			defer tx.Rollback(context.Background())
			if _, err := recoverypostgres.New(tx).AdoptPublishedTx(ctx, tx, set, attempt, result, publisher); err != nil {
				return errors.New("replacement publication conflicts with exact authoritative validation")
			}
			if err := revalidate(); err != nil {
				return err
			}
			if err := tx.Commit(ctx); err != nil {
				return errors.New("replacement adoption commit failed; retry the exact frontier")
			}
			return nil
		},
		now: time.Now,
	})
}

func promoteConfiguredReplacement(ctx context.Context, config ManagedConfig, set recoveryset.RecoverySet, check bool) error {
	machine, err := readQualificationMachineID("/etc/machine-id")
	if err != nil || len(config.PrimaryFence.Primaries) != 1 || !validQualificationMachineID(config.PrimaryFence.Primaries[0].MachineID) || machine == config.PrimaryFence.Primaries[0].MachineID || config.Postgres.Frontier.Stanza != "default" {
		return errors.New("configured promotion requires the actual distinct replacement and module stanza")
	}
	request := struct {
		SchemaVersion        int    `json:"schemaVersion"`
		TargetID             string `json:"targetID"`
		RecoverySetID        string `json:"recoverySetID"`
		FrontierDigest       string `json:"frontierDigest"`
		SourceMachineID      string `json:"sourceMachineID"`
		ReplacementMachineID string `json:"replacementMachineID"`
		SystemIdentifier     string `json:"systemIdentifier"`
		TargetLSN            string `json:"targetLSN"`
		Timeline             uint32 `json:"timeline"`
		DataDirectoryDigest  string `json:"dataDirectoryDigest"`
	}{1, set.Delivery.TargetID, set.ID, set.FrontierDigest, config.PrimaryFence.Primaries[0].MachineID, machine, config.Postgres.Frontier.SystemID, config.Postgres.Frontier.TargetLSN, config.Postgres.Frontier.Timeline, digestBytes([]byte("leapview-managed-recovery-data:" + config.Postgres.Destination))}
	raw, err := json.Marshal(request)
	if err != nil {
		return errors.New("exact promotion request cannot be encoded")
	}
	const helper = "/run/current-system/sw/bin/leapview-postgres-promote"
	resolved, err := filepath.EvalSymlinks(helper)
	if err != nil || !pinnedProgram(resolved) || filepath.Base(resolved) != "leapview-postgres-promote" {
		return errors.New("module-owned fixed promotion helper unavailable")
	}
	args := []string{"-n", "--", helper}
	if check {
		args = append(args, "--check")
	}
	command := exec.CommandContext(ctx, "/run/wrappers/bin/sudo", args...)
	command.Stdin = bytes.NewReader(raw)
	command.Env = []string{"PATH=/nonexistent", "LANG=C", "TZ=UTC"}
	// No root helper/provider output is forwarded. The authenticated immutable
	// receipt is read from its fixed root-owned path after the command succeeds.
	if command.Run() != nil {
		return errors.New("configured exact PostgreSQL promotion failed; keep application activation closed")
	}
	return nil
}

func adoptManagedRecovery(ctx context.Context, input ManagedInput, config ManagedConfig, authority ManagedAuthorities, operations adoptionOperations) (ManagedAdoptionEvidence, error) {
	fail := func(message string) (ManagedAdoptionEvidence, error) {
		return ManagedAdoptionEvidence{}, errors.New(message)
	}
	if validateAdoptionInputs(ctx, input, config) != nil || operations.service == nil || operations.verify == nil || operations.adopt == nil || operations.now == nil {
		return fail("adoption requires exact enrolled inputs and live replacement verification")
	}
	snapshot, err := readManagedAdmission(ctx, config, authority)
	if err != nil {
		return fail("adoption requires exact authoritative completed restore evidence")
	}
	attempts, ok := authority.Sets.(interface {
		ValidationAttempt(context.Context, string) (recoveryset.ValidationAttempt, error)
	})
	if !ok {
		return fail("adoption requires the durable authoritative validation attempt")
	}
	attempt, err := attempts.ValidationAttempt(ctx, snapshot.set.PublishedValidationAttemptID)
	result, resultErr := authority.Sets.ValidationResult(ctx, snapshot.set.PublishedValidationAttemptID)
	if err != nil || resultErr != nil || attempt.Validate() != nil || attempt.SetID != snapshot.set.ID || attempt.Status != recoveryset.ValidationPassed || attempt.AttemptID != snapshot.set.PublishedValidationAttemptID || attempt.FenceEpoch != snapshot.set.FenceEpoch || attempt.ResultDigest != result.ResultDigest {
		return fail("adoption validation does not bind the exact published attempt and fence")
	}
	fence, err := providerrestore.NewSSHPrimaryFence(config.PrimaryFence)
	if err != nil {
		return fail("adoption requires the retained original-writer fence")
	}
	return adoptManagedSnapshot(ctx, input, config, authority, snapshot, attempt, result, fence, operations)
}

func validateAdoptionInputs(ctx context.Context, input ManagedInput, config ManagedConfig) error {
	if ctx.Err() != nil || input.RecoverySetID != config.RecoverySetID || input.OccurrenceID != config.OccurrenceID || input.InstanceHome != config.InstanceHome || input.Artifact != config.Artifact || input.Authority.SystemIdentifier != config.Enrollment.Request.AuthoritySystemID || input.Publisher == "" || config.Postgres.Frontier != config.Readback.Frontier || config.Postgres.Frontier.Stanza != "default" {
		return errors.New("adoption inputs differ from exact managed enrollment")
	}
	return nil
}

func adoptManagedSnapshot(ctx context.Context, input ManagedInput, config ManagedConfig, authority ManagedAuthorities, snapshot managedAdmissionSnapshot, attempt recoveryset.ValidationAttempt, result recoveryset.ValidationResult, fence providerrestore.PrimaryFence, operations adoptionOperations) (ManagedAdoptionEvidence, error) {
	fail := func(message string) (ManagedAdoptionEvidence, error) {
		return ManagedAdoptionEvidence{}, errors.New(message)
	}
	if fence.Verify(ctx, snapshot.set) != nil {
		return fail("adoption requires freshly observed original-writer fencing")
	}
	receipt, raw, err := operations.service(ctx, snapshot.set)
	if err != nil {
		return fail("adoption requires an authenticated live loopback-only promoted service")
	}
	revalidate := func() error {
		current, currentRaw, err := operations.service(ctx, snapshot.set)
		if err != nil || !reflect.DeepEqual(current, receipt) || string(currentRaw) != string(raw) {
			return errors.New("adoption promotion or live service changed")
		}
		observed, err := operations.verify(ctx, snapshot.set)
		expected := snapshot.report.Verification
		if err != nil || observed.ControlDigest != expected.ControlStateDigest || observed.DuckLakeDigest != expected.DuckLakeStateDigest || observed.Catalog != expected.Catalog {
			return errors.New("adoption native publication, keyring, catalog or file state changed")
		}
		currentSnapshot, err := readManagedAdmission(ctx, config, authority)
		attempts, ok := authority.Sets.(interface {
			ValidationAttempt(context.Context, string) (recoveryset.ValidationAttempt, error)
		})
		if !ok {
			return errors.New("adoption lost authoritative validation storage")
		}
		currentAttempt, attemptErr := attempts.ValidationAttempt(ctx, attempt.AttemptID)
		currentResult, resultErr := authority.Sets.ValidationResult(ctx, result.AttemptID)
		if err != nil || attemptErr != nil || resultErr != nil || !reflect.DeepEqual(currentSnapshot, snapshot) || !reflect.DeepEqual(currentAttempt, attempt) || !reflect.DeepEqual(currentResult, result) || ctx.Err() != nil || fence.Verify(ctx, snapshot.set) != nil {
			return errors.New("adoption lost the exact independent frontier or original fence")
		}
		return nil
	}
	if err := revalidate(); err != nil {
		return fail(err.Error())
	}
	if operations.adopt(ctx, snapshot.set, attempt, result, input.Publisher, revalidate) != nil {
		return fail("atomic replacement frontier adoption failed; keep application activation closed")
	}
	return ManagedAdoptionEvidence{SchemaVersion: 1, Kind: "leapview/managed-recovery-adoption", Status: "published-locally-loopback-only", TargetID: snapshot.set.Delivery.TargetID, RecoverySetID: snapshot.set.ID, FrontierDigest: snapshot.set.FrontierDigest, ValidationAttemptID: attempt.AttemptID, ValidationDigest: result.ResultDigest, ReportSHA256: snapshot.occurrence.Evidence[0].SHA256, PromotionSHA256: strings.TrimPrefix(digestBytes(raw), "sha256:"), OriginalsFenced: true, AdoptedAt: operations.now().UTC()}, nil
}

func loopbackPromotionDial(port uint16) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	}
}

func replacementMaintenanceConfiguration(input AuthorityInput, config ManagedConfig) (*pgx.ConnConfig, error) {
	raw, err := readBoundedManagedPrivateFile(input.URLFile, maxManagedCredentialsBytes)
	ca, caErr := readBoundedManagedPrivateFile(input.RootCAFile, maxManagedCredentialsBytes)
	if err != nil || caErr != nil || input.SystemIdentifier != config.Postgres.Frontier.SystemID || string(ca) != config.Credentials.PostgresRootCA {
		return nil, errors.New("explicit retained replacement maintenance identity unavailable")
	}
	connection, err := managedConnectionConfig(strings.TrimSpace(string(raw)), input.Role, string(ca))
	runtime, runtimeErr := managedConnectionConfig(config.Credentials.ControlURL, config.Roles.Control, config.Credentials.PostgresRootCA)
	if err != nil || runtimeErr != nil || connection.Host != runtime.Host || connection.Port != runtime.Port || connection.Database != runtime.Database || connection.User == config.Roles.Control {
		return nil, errors.New("replacement maintenance endpoint differs from exact retained control TLS identity")
	}
	connection.RuntimeParams["default_transaction_read_only"] = "off"
	connection.RuntimeParams["search_path"] = "pg_catalog"
	return connection, nil
}

func openReplacementMaintenance(ctx context.Context, input AuthorityInput, config ManagedConfig, receipt ManagedPromotionReceipt) (*pgxpool.Pool, error) {
	connection, err := replacementMaintenanceConfiguration(input, config)
	if err != nil || receipt.Port == 0 {
		return nil, errors.New("exact replacement maintenance configuration unavailable")
	}
	connection.DialFunc = loopbackPromotionDial(receipt.Port)
	connection.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	configuration, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, errors.New("replacement maintenance pool unavailable")
	}
	configuration.ConnConfig, configuration.MaxConns, configuration.MinConns = connection, 2, 0
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		return nil, errors.New("replacement maintenance connection failed")
	}
	if err := verifyReplacementService(ctx, pool, config, receipt); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func verifyReplacementService(ctx context.Context, pool *pgxpool.Pool, config ManagedConfig, receipt ManagedPromotionReceipt) error {
	var system, data, configuration, listen, lsn, user, database string
	var timeline uint32
	var recovery, ssl bool
	// sqlc-exception:managed-recovery-adoption -- observe the authenticated
	// module-owned replacement service before importing capability-owned rows.
	err := pool.QueryRow(ctx, `SELECT (pg_catalog.pg_control_system()).system_identifier::text,
pg_catalog.current_setting('data_directory'), pg_catalog.current_setting('config_file'), pg_catalog.current_setting('listen_addresses'),
COALESCE(pg_catalog.pg_last_wal_replay_lsn()::text,''), (pg_catalog.pg_control_checkpoint()).timeline_id,
pg_catalog.pg_is_in_recovery(), current_user,
pg_catalog.current_database(), (SELECT ssl FROM pg_catalog.pg_stat_ssl WHERE pid=pg_catalog.pg_backend_pid())`).Scan(&system, &data, &configuration, &listen, &lsn, &timeline, &recovery, &user, &database, &ssl)
	connection := pool.Config().ConnConfig
	// The root helper freshly proves this symlink resolves to its reviewed store
	// config. The maintenance process deliberately has no PGDATA traversal access.
	if err != nil || configuration != filepath.Join(config.Postgres.Destination, "postgresql.conf") || system != receipt.SystemIdentifier || data != config.Postgres.Destination || listen != "127.0.0.1" || (lsn != "" && lsn != receipt.ReplayedThroughLSN) || timeline != receipt.PromotedTimeline || recovery || !ssl || user != connection.User || database != connection.Database {
		return errors.New("actual replacement PostgreSQL service differs from authenticated loopback promotion")
	}
	return nil
}

func verifyAdoptionRoots(ctx context.Context, config ManagedConfig, snapshot managedAdmissionSnapshot) error {
	credentials, err := (ManagedSecretStore{Root: config.SecretRoot}).Load(ctx, snapshot.report.Handoff.Secrets)
	if err != nil || credentials != config.Credentials || credentials.ValidateForHandoff(snapshot.report.Handoff, config.Roles) != nil || len(config.Roots) != len(snapshot.report.Handoff.ManagedLocal.Roots) {
		return errors.New("adoption requires exact retained credentials and object roots")
	}
	for _, restored := range snapshot.report.Handoff.ManagedLocal.Roots {
		matches := 0
		for _, input := range config.Roots {
			if input.Root != restored.Root || input.StorageRoot != restored.StorageRoot || input.Destination != restored.Destination || input.ManifestDigest != restored.ContentManifestDigest || !reflect.DeepEqual(input.ArtifactMetadata, restored.ArtifactMetadata) {
				continue
			}
			restorer, err := NewRestic(input)
			if err != nil || restorer.verifyDestination(ctx) != nil {
				return errors.New("adoption restored object content differs from retained frontier")
			}
			matches++
		}
		if matches != 1 {
			return errors.New("adoption requires every exact retained file manifest")
		}
	}
	return nil
}
