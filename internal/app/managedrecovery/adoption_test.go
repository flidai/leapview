package managedrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
)

type adoptionSets struct {
	*admissionSets
	attempt recoveryset.ValidationAttempt
}

func (sets *adoptionSets) ValidationAttempt(context.Context, string) (recoveryset.ValidationAttempt, error) {
	return sets.attempt, nil
}

func TestManagedAdoptionRevalidatesInsideAtomicCommitBoundary(t *testing.T) {
	// These ports exercise boundary decisions only. Real PostgreSQL atomic
	// import and real promotion/restart have separate native regression tests;
	// none of these fixture identities supplies installed-host qualification.
	for _, scenario := range []string{"exact", "lost-fence", "changed-native", "changed-frontier", "changed-attempt", "changed-receipt", "transaction-failure"} {
		t.Run(scenario, func(t *testing.T) {
			config, authority, originalSets, _ := managedAdmissionFixture(t)
			result := originalSets.result
			attempt := recoveryset.ValidationAttempt{AttemptID: result.AttemptID, SetID: config.RecoverySetID, Status: recoveryset.ValidationPassed, FenceEpoch: originalSets.set.FenceEpoch, ResultDigest: result.ResultDigest, OwnerID: "independent-validator", AuditIdentity: "independent-validation-audit", StartedAt: result.RecordedAt.Add(-time.Second), CompletedAt: result.RecordedAt.Add(time.Second)}
			sets := &adoptionSets{admissionSets: originalSets, attempt: attempt}
			authority.Sets = sets
			snapshot, err := readManagedAdmission(t.Context(), config, authority)
			if err != nil {
				t.Fatal(err)
			}
			fence := &admissionFence{}
			receipt := ManagedPromotionReceipt{SchemaVersion: 1, Kind: "leapview/managed-postgresql-promotion", Status: "promoted-loopback-only", TargetID: snapshot.set.Delivery.TargetID, RecoverySetID: snapshot.set.ID, FrontierDigest: snapshot.set.FrontierDigest}
			raw, _ := json.Marshal(receipt)
			state := NativePostgresEvidence{ControlDigest: snapshot.report.Verification.ControlStateDigest, DuckLakeDigest: snapshot.report.Verification.DuckLakeStateDigest, Catalog: snapshot.report.Verification.Catalog}
			committed, rolledBack := false, false
			operations := adoptionOperations{
				service: func(context.Context, recoveryset.RecoverySet) (ManagedPromotionReceipt, []byte, error) {
					return receipt, raw, nil
				},
				verify: func(context.Context, recoveryset.RecoverySet) (NativePostgresEvidence, error) { return state, nil },
				now:    time.Now,
				adopt: func(_ context.Context, set recoveryset.RecoverySet, receivedAttempt recoveryset.ValidationAttempt, receivedResult recoveryset.ValidationResult, publisher string, beforeCommit func() error) error {
					if !set.IdentityEqual(snapshot.set) || receivedAttempt != attempt || receivedResult.ResultDigest != result.ResultDigest || publisher != "replacement-operator" {
						t.Fatal("atomic import did not receive exact authority")
					}
					switch scenario {
					case "lost-fence":
						fence.failAt = 3
					case "changed-native":
						state.ControlDigest = digestBytes([]byte("foreign-state"))
					case "changed-frontier":
						sets.set.FenceEpoch++
					case "changed-attempt":
						sets.attempt.FenceEpoch++
					case "changed-receipt":
						raw = []byte("foreign-receipt")
					case "transaction-failure":
						rolledBack = true
						return errors.New("private connection diagnostic")
					}
					if err := beforeCommit(); err != nil {
						rolledBack = true
						return err
					}
					committed = true
					return nil
				},
			}
			evidence, err := adoptManagedSnapshot(t.Context(), ManagedInput{Publisher: "replacement-operator"}, config, authority, snapshot, attempt, result, fence, operations)
			if scenario != "exact" {
				if err == nil || committed || !rolledBack || evidence.Kind != "" || strings.Contains(err.Error(), "private connection diagnostic") {
					t.Fatalf("unsafe atomic boundary: committed=%v rollback=%v err=%v", committed, rolledBack, err)
				}
				return
			}
			if err != nil || !committed || rolledBack || fence.calls != 3 || !evidence.OriginalsFenced || evidence.ActivationQualified || evidence.FullManagedProfileQualified || evidence.ValidationDigest != result.ResultDigest {
				t.Fatalf("incorrect bounded adoption: %+v %v", evidence, err)
			}
			encoded, _ := json.Marshal(evidence)
			for _, value := range []string{config.InstanceHome, config.Credentials.ControlURL, config.Credentials.DuckLakeURL, config.Credentials.KeyringPath} {
				if strings.Contains(string(encoded), value) {
					t.Fatal("adoption evidence exposed private input")
				}
			}
		})
	}
}

func TestManagedPromotionReceiptRejectsFrontierAndIdentitySubstitution(t *testing.T) {
	config, _, sets, _ := managedAdmissionFixture(t)
	config.Postgres.Frontier = PGFrontier{Stanza: "default", BackupSet: "20261009-080000F", TargetLSN: "0/ABC", SystemID: "1", Timeline: 1}
	config.Readback.Frontier = config.Postgres.Frontier
	config.Postgres.Destination = "/var/lib/postgresql/18"
	config.PrimaryFence.Primaries = []providerrestore.PrimaryEnrollment{{MachineID: strings.Repeat("a", 32), SystemIdentifier: "1", ClusterIdentity: "postgres-system-id:1"}}
	identity, err := config.Postgres.Frontier.RecoveryIdentity()
	if err != nil {
		t.Fatal(err)
	}
	set := sets.set
	for i := range set.ClusterPoints {
		set.ClusterPoints[i].ClusterIdentity = "postgres-system-id:1"
		set.ClusterPoints[i].RecoveryIdentity = identity
	}
	machine, generation, now := strings.Repeat("b", 32), "/nix/store/reviewed-generation", time.Now().UTC()
	receipt := ManagedPromotionReceipt{SchemaVersion: 1, Kind: "leapview/managed-postgresql-promotion", Status: "promoted-loopback-only", TargetID: set.Delivery.TargetID, RecoverySetID: set.ID, FrontierDigest: set.FrontierDigest, SystemIdentifier: "1", TargetLSN: "0/ABC", Timeline: 1, PromotedTimeline: 2, ReplayedThroughLSN: "0/ABD", ConfigurationDigest: digestBytes([]byte("config")), Port: 5432, PromotedAt: now,
		ReplacementMachineIDDigest: digestBytes([]byte("leapview-managed-recovery-qualification:" + machine)), DataDirectoryDigest: digestBytes([]byte("leapview-managed-recovery-data:" + config.Postgres.Destination)), ModuleGenerationDigest: digestBytes([]byte("leapview-managed-recovery-module:" + generation))}
	if err := validatePromotionReceipt(receipt, config, set, machine, generation, now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ManagedPromotionReceipt){
		"target":     func(r *ManagedPromotionReceipt) { r.TargetID = "foreign" },
		"frontier":   func(r *ManagedPromotionReceipt) { r.FrontierDigest = digestBytes([]byte("foreign")) },
		"cluster":    func(r *ManagedPromotionReceipt) { r.SystemIdentifier = "2" },
		"lsn":        func(r *ManagedPromotionReceipt) { r.TargetLSN = "0/ABD" },
		"timeline":   func(r *ManagedPromotionReceipt) { r.PromotedTimeline = 3 },
		"data":       func(r *ManagedPromotionReceipt) { r.DataDirectoryDigest = digestBytes([]byte("other-data")) },
		"generation": func(r *ManagedPromotionReceipt) { r.ModuleGenerationDigest = digestBytes([]byte("other-generation")) },
		"activation": func(r *ManagedPromotionReceipt) { r.ActivationQualified = true },
		"future":     func(r *ManagedPromotionReceipt) { r.PromotedAt = now.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			if validatePromotionReceipt(changed, config, set, machine, generation, now) == nil {
				t.Fatal("substituted promotion receipt accepted")
			}
		})
	}
	if validatePromotionReceipt(receipt, config, set, strings.Repeat("a", 32), generation, now) == nil {
		t.Fatal("original host accepted as replacement")
	}
}

func TestReplacementMaintenanceRetainsTLSNameAndRejectsEndpointSubstitution(t *testing.T) {
	config, _, _, _ := managedAdmissionFixture(t)
	config.Postgres.Frontier.SystemID = "1"
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(config.Credentials.ControlURL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.User = url.UserPassword("maintenance_owner", "private-maintenance-password")
	input := AuthorityInput{URLFile: filepath.Join(root, "url"), RootCAFile: filepath.Join(root, "ca"), Role: "maintenance_owner", SystemIdentifier: "1"}
	if os.WriteFile(input.URLFile, []byte(endpoint.String()), 0600) != nil || os.WriteFile(input.RootCAFile, []byte(config.Credentials.PostgresRootCA), 0600) != nil {
		t.Fatal("private test input unavailable")
	}
	connection, err := replacementMaintenanceConfiguration(input, config)
	if err != nil {
		t.Fatal(err)
	}
	if connection.TLSConfig == nil || connection.TLSConfig.InsecureSkipVerify || connection.TLSConfig.ServerName != endpoint.Hostname() || connection.Fallbacks != nil || connection.RuntimeParams["default_transaction_read_only"] != "off" {
		t.Fatal("replacement maintenance weakened TLS or read-only boundary")
	}
	endpoint.Host = "foreign.example:5432"
	if err := os.WriteFile(input.URLFile, []byte(endpoint.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := replacementMaintenanceConfiguration(input, config); err == nil {
		t.Fatal("foreign endpoint accepted for source-preserving replacement")
	}
	path := filepath.Join(root, "claimed-promotion.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"kind":"leapview/managed-postgresql-promotion"}`), 0640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readRootPromotionReceipt(path); err == nil {
		t.Fatal("operator-owned claimed receipt accepted")
	}
}
