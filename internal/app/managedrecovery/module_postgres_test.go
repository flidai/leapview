package managedrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flidai/leapview/pkg/strictjson"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
)

type modulePostgresFixture struct {
	provider *modulePostgres
	request  providerrestore.DatabaseRequest
	receipt  moduleRestoreReceipt
	calls    []string
	active   bool
	fail     string
	changed  bool
	cancel   context.CancelFunc
	clock    time.Time
}

func newModulePostgresFixture(t *testing.T, published bool) *modulePostgresFixture {
	t.Helper()
	config, _, sets, _ := managedAdmissionFixture(t)
	set := sets.set
	if !published {
		set.Status, set.PublishedValidationAttemptID = recoveryset.StatusPrepared, ""
	}
	frontier := PGFrontier{Stanza: "default", BackupSet: "20261010-000000F", TargetLSN: "0/ABC", SystemID: "1", Timeline: 1}
	identity, err := frontier.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	for i := range set.ClusterPoints {
		set.ClusterPoints[i].ClusterIdentity = "postgres-system-id:1"
		set.ClusterPoints[i].RecoveryIdentity = identity
	}
	set.FrontierDigest, err = set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	config.PostgresProvider = "module-owned"
	config.Postgres = PGBackRestConfig{TargetID: set.Delivery.TargetID, RecoverySetID: set.ID, Frontier: frontier, Points: set.CanonicalPoints(), Destination: "/var/lib/postgresql/18"}
	config.PrimaryFence.Primaries = []providerrestore.PrimaryEnrollment{{MachineID: strings.Repeat("a", 32), SystemIdentifier: "1", ClusterIdentity: "postgres-system-id:1"}}
	config.Readback = PGNativeReadbackConfig{Frontier: frontier, Native: NativePostgresReadback{Set: set, ControlURL: config.Credentials.ControlURL, DuckLakeURL: config.Credentials.DuckLakeURL, RootCA: config.Credentials.PostgresRootCA, Roles: config.Roles, MetadataSchema: "ducklake", Credentials: &config.Credentials}}
	provider, err := managedPostgresProvider(config, set)
	if err != nil {
		t.Fatal(err)
	}
	module, ok := provider.(*modulePostgres)
	if !ok {
		t.Fatalf("selected provider %T", provider)
	}
	wanted, err := moduleRequest(config, set, "provider-restore:occurrence", strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC()
	f := &modulePostgresFixture{provider: module, clock: clock, request: providerrestore.DatabaseRequest{TargetID: set.Delivery.TargetID, RecoverySetID: set.ID, IdempotencyKey: wanted.OperationID, Points: set.ClusterPoints, Catalog: set.Catalog}}
	f.receipt = moduleRestoreReceipt{SchemaVersion: 1, Kind: "leapview/module-postgresql-restore", Status: "restored-stopped", TargetID: wanted.TargetID, RecoverySetID: wanted.RecoverySetID, FrontierDigest: wanted.FrontierDigest, OccurrenceID: wanted.OccurrenceID, OperationID: wanted.OperationID, BackupSet: wanted.BackupSet, ReplacementMachineIDDigest: digestBytes([]byte("leapview-managed-recovery-qualification:" + wanted.ReplacementMachineID)), SystemIdentifier: wanted.SystemIdentifier, TargetLSN: wanted.TargetLSN, Timeline: wanted.Timeline, DataDirectoryDigest: wanted.DataDirectoryDigest, Port: 5432, ModuleGenerationDigest: digestBytes([]byte("leapview-managed-recovery-module:/nix/store/reviewed-generation")), ConfigurationDigest: digestBytes([]byte("configuration")), ReplayedThroughLSN: "0/ABC", RestoredAt: clock.Add(-time.Minute)}
	module.operations = moduleRestoreOperations{
		machine:    func() (string, error) { return strings.Repeat("b", 32), nil },
		generation: func() (string, error) { return "/nix/store/reviewed-generation", nil },
		now:        func() time.Time { return f.clock },
		receipt: func() (moduleRestoreReceipt, []byte, error) {
			value := f.receipt
			if f.changed {
				value.ConfigurationDigest = digestBytes([]byte("substituted"))
			}
			raw, _ := json.Marshal(value)
			return value, raw, nil
		},
		invoke: func(ctx context.Context, action string, request moduleRestoreRequest) error {
			f.calls = append(f.calls, action)
			if request != wanted {
				return errors.New("root action differs from retained intent")
			}
			if action == "start-readback" {
				f.active = true
			}
			if action == "stop-readback" {
				if ctx.Err() != nil {
					t.Fatal("cleanup retained cancelled context")
				}
				f.active = false
			}
			if action == f.fail {
				return errors.New("private provider credentials")
			}
			return nil
		},
		verify: func(context.Context, uint16) (NativePostgresEvidence, error) {
			f.calls = append(f.calls, "verify")
			if f.cancel != nil {
				f.cancel()
			}
			if f.fail == "verify" {
				return NativePostgresEvidence{}, errors.New("private provider credentials")
			}
			f.clock = f.clock.Add(time.Second)
			return NativePostgresEvidence{ControlDigest: digestBytes([]byte("control")), DuckLakeDigest: digestBytes([]byte("duck")), Catalog: set.Catalog}, nil
		},
	}
	return f
}

func TestModulePostgresComposesExactPrivateRestoreReadbackAndCompletedRetry(t *testing.T) {
	f := newModulePostgresFixture(t, false)
	result, err := f.provider.RestoreCluster(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || f.active || strings.Join(f.calls, ",") != "restore,check,start-readback,verify,stop-readback,check" {
		t.Fatalf("unverified or active result: %v %v", f.calls, err)
	}
	for _, proof := range result {
		if proof.Provider != "pgbackrest-managed" || proof.OperationID != f.request.IdempotencyKey || proof.CompletedAt.Before(proof.StartedAt) || proof.CompletedAt.After(f.clock) {
			t.Fatal("noncanonical provider result")
		}
	}
	f.calls = nil
	retry, err := f.provider.RestoreCluster(t.Context(), f.request)
	if err != nil || len(retry) != 2 || f.active {
		t.Fatalf("completed root retry failed: %v", err)
	}
	// The root helper authenticates its durable exact intent and never repeats
	// physical pgBackRest work; the adapter rechecks real native state each time.
	if strings.Join(f.calls, ",") != "restore,check,start-readback,verify,stop-readback,check" {
		t.Fatal(f.calls)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{f.provider.config.Credentials.ControlURL, strings.Repeat("b", 32), "private provider"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("public database result leaked private input")
		}
	}
}

func TestModulePostgresPublishedAdmissionCannotRestoreMissingData(t *testing.T) {
	f := newModulePostgresFixture(t, true)
	f.fail = "check"
	result, err := f.provider.RestoreCluster(t.Context(), f.request)
	if err == nil || result != nil || !reflect.DeepEqual(f.calls, []string{"check"}) {
		t.Fatalf("admission reached restore/start: %v %v", f.calls, err)
	}
	f.calls, f.fail = nil, ""
	if _, err = f.provider.RestoreCluster(t.Context(), f.request); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "check,check,start-readback,verify,stop-readback,check" {
		t.Fatal(f.calls)
	}
}

func TestModulePostgresRejectsWrongReceiptBeforeReadback(t *testing.T) {
	cases := map[string]func(*modulePostgresFixture){
		"frontier":  func(f *modulePostgresFixture) { f.receipt.FrontierDigest = digestBytes([]byte("other")) },
		"target":    func(f *modulePostgresFixture) { f.receipt.TargetID = "other" },
		"operation": func(f *modulePostgresFixture) { f.receipt.OperationID = "other" },
		"machine": func(f *modulePostgresFixture) {
			f.provider.operations.machine = func() (string, error) { return strings.Repeat("c", 32), nil }
		},
		"same source machine": func(f *modulePostgresFixture) {
			f.provider.operations.machine = func() (string, error) { return strings.Repeat("a", 32), nil }
		},
		"generation": func(f *modulePostgresFixture) {
			f.provider.operations.generation = func() (string, error) { return "/nix/store/other", nil }
		},
		"before target":      func(f *modulePostgresFixture) { f.receipt.ReplayedThroughLSN = "0/ABB" },
		"target higher word": func(f *modulePostgresFixture) { f.receipt.TargetLSN = "1/0" },
		"future receipt":     func(f *modulePostgresFixture) { f.receipt.RestoredAt = f.clock.Add(time.Second) },
		"activation":         func(f *modulePostgresFixture) { f.receipt.ActivationQualified = true },
		"profile":            func(f *modulePostgresFixture) { f.receipt.FullManagedProfileQualified = true },
		"untrusted receipt": func(f *modulePostgresFixture) {
			f.provider.operations.receipt = func() (moduleRestoreReceipt, []byte, error) {
				return moduleRestoreReceipt{}, nil, errors.New("private root diagnostic")
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newModulePostgresFixture(t, false)
			change(f)
			result, err := f.provider.RestoreCluster(t.Context(), f.request)
			if err == nil || result != nil || f.active || strings.Contains(strings.Join(f.calls, ","), "start-readback") || strings.Contains(err.Error(), "private") && strings.Contains(err.Error(), "diagnostic") {
				t.Fatalf("substituted receipt reached readback: %v %v", f.calls, err)
			}
		})
	}
}

func TestModulePostgresFailureInterruptionAndReceiptChangeFailClosedWithCleanup(t *testing.T) {
	for _, failure := range []string{"start-readback", "verify", "stop-readback", "changed receipt", "interruption", "clock"} {
		t.Run(failure, func(t *testing.T) {
			f := newModulePostgresFixture(t, false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.fail = failure
			if failure == "interruption" {
				f.cancel = cancel
			}
			if failure == "changed receipt" {
				original := f.provider.operations.verify
				f.provider.operations.verify = func(ctx context.Context, port uint16) (NativePostgresEvidence, error) {
					proof, err := original(ctx, port)
					f.changed = true
					return proof, err
				}
			}
			if failure == "clock" {
				original := f.provider.operations.verify
				f.provider.operations.verify = func(ctx context.Context, port uint16) (NativePostgresEvidence, error) {
					proof, err := original(ctx, port)
					f.clock = f.clock.Add(-2 * time.Second)
					return proof, err
				}
			}
			result, err := f.provider.RestoreCluster(ctx, f.request)
			if err == nil || result != nil || f.active || !strings.Contains(strings.Join(f.calls, ","), "stop-readback") || strings.Contains(err.Error(), "credentials") {
				t.Fatalf("failure became success or leaked private error: %v %v", f.calls, err)
			}
		})
	}
}

func TestModuleProviderSelectionRejectsCallerConfigurationAndForeignTopology(t *testing.T) {
	changes := map[string]func(*ManagedConfig){
		"provider": func(c *ManagedConfig) { c.PostgresProvider = "caller-defined" },
		"tools":    func(c *ManagedConfig) { c.Postgres.PGBackRest = "/nix/store/caller/bin/pgbackrest" },
		"config":   func(c *ManagedConfig) { c.Postgres.ConfigFile = "/private/caller.conf" },
		"tls key":  func(c *ManagedConfig) { c.Readback.ServerKeyFile = "/private/key" },
		"data":     func(c *ManagedConfig) { c.Postgres.Destination = "/var/lib/postgresql/../foreign" },
		"system":   func(c *ManagedConfig) { c.PrimaryFence.Primaries[0].SystemIdentifier = "2" },
		"frontier": func(c *ManagedConfig) { c.Readback.Frontier.TargetLSN = "0/ABD" },
		"TLS": func(c *ManagedConfig) {
			c.Readback.Native.ControlURL = strings.ReplaceAll(c.Readback.Native.ControlURL, "verify-full", "disable")
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newModulePostgresFixture(t, false)
			config := f.provider.config
			change(&config)
			if _, err := managedPostgresProvider(config, f.provider.set); err == nil {
				t.Fatal("foreign module configuration accepted")
			}
		})
	}
}

func TestModuleReplayPositionComparisonUsesFullWALWords(t *testing.T) {
	for _, test := range []struct {
		replay, target string
		valid          bool
	}{{"1/0", "0/FFFFFFFF", true}, {"0/FFFFFFFF", "1/0", false}, {"0/ABC", "0/ABC", true}, {"0/ABB", "0/ABC", false}, {"0/abc", "0/ABC", false}, {"100000000/0", "1/0", false}} {
		if replayReachedTarget(test.replay, test.target) != test.valid {
			t.Fatalf("replay comparison %q/%q", test.replay, test.target)
		}
	}
}

// Consume the actual producer's receipt shape rather than duplicating its
// field names silently. This is protocol compatibility, not installed proof.
func TestModuleRestoreReceiptDecodesActualProducerContract(t *testing.T) {
	f := newModulePostgresFixture(t, false)
	program, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("registered managed shell requires Python for helper contract")
	}
	config := filepath.Join(t.TempDir(), "postgresql.conf")
	if err := os.WriteFile(config, []byte("# module configuration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wanted, err := moduleRequest(f.provider.config, f.provider.set, f.request.IdempotencyKey, strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := json.Marshal(map[string]any{"request": wanted, "moduleGeneration": "/nix/store/reviewed-generation"})
	if err != nil {
		t.Fatal(err)
	}
	code := `import json, pathlib, sys
sys.path.insert(0, str(pathlib.Path("../../../deploy/managed/nixos/modules").resolve()))
import postgres_restore
helper = postgres_restore.ModuleRestore.__new__(postgres_restore.ModuleRestore)
helper.port = 5432
helper.config = pathlib.Path(sys.argv[1])
value = helper._public(json.load(sys.stdin))
value.update(replayedThroughLSN="0/ABC", restoredAt=sys.argv[2])
print(json.dumps(value))`
	command := exec.CommandContext(t.Context(), program, "-B", "-c", code, config, f.receipt.RestoredAt.Format("2006-01-02T15:04:05Z"))
	command.Stdin = strings.NewReader(string(binding))
	raw, err := command.Output()
	if err != nil {
		t.Fatal("actual module receipt producer failed")
	}
	var receipt moduleRestoreReceipt
	if strictjson.DecodeWithOptions(raw, &receipt, strictjson.Options{MaxBytes: 16384}) != nil || f.provider.validateReceipt(receipt, wanted) != nil {
		t.Fatal("actual module producer/consumer contract diverged")
	}
}

func TestModuleQualificationRetainsObjectFreshnessAndOverlapBoundaries(t *testing.T) {
	f := newQualificationFixture(t)
	f.config.PostgresProvider = "module-owned"
	// The owner cannot inspect this service-owned location. The fixed root
	// helper's fresh action is the actual filesystem precondition capability.
	f.config.Postgres.Destination = "/var/lib/postgresql/18"
	if err := requireFreshManagedDestinations(f.config); err != nil {
		t.Fatal(err)
	}
	f.config.Postgres.Destination = f.config.Roots[0].Destination
	if requireFreshManagedDestinations(f.config) == nil {
		t.Fatal("module selector bypassed destination overlap")
	}
	f.config.Postgres.Destination = "/var/lib/postgresql/18"
	if err := os.WriteFile(f.config.Roots[0].Destination, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if requireFreshManagedDestinations(f.config) == nil {
		t.Fatal("module selector bypassed existing object destination")
	}
}
