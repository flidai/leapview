package managedrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

// The existing canonical report/enrollment fixture supplies authority contracts
// only. These injected operations never count as real installed-host proof.
type qualificationFixture struct {
	input      ManagedInput
	config     ManagedConfig
	authority  ManagedAuthorities
	sets       *admissionSets
	ledger     *admissionLedger
	operations qualificationOperations
	report     providerrestore.Report
	admission  ManagedAdmissionEvidence
	clock      time.Time
	calls      []string
}

func newQualificationFixture(t *testing.T) *qualificationFixture {
	t.Helper()
	config, authority, sets, ledger := managedAdmissionFixture(t)
	report, err := (providerrestore.FileEvidenceStore{Root: config.EvidenceRoot}).Load(t.Context(), ledger.occurrence.Evidence[0])
	if err != nil {
		t.Fatal(err)
	}
	config.Postgres.Destination = filepath.Join(config.InstanceHome, "postgres")
	for _, root := range report.Handoff.ManagedLocal.Roots {
		if err := os.MkdirAll(filepath.Dir(root.Destination), 0700); err != nil {
			t.Fatal(err)
		}
		config.Roots = append(config.Roots, ResticConfig{Root: root.Root, StorageRoot: root.StorageRoot, Destination: root.Destination})
	}
	config.PrimaryFence = providerrestore.PrimaryFenceSSHConfig{TargetID: sets.set.Delivery.TargetID, Primaries: []providerrestore.PrimaryEnrollment{{MachineID: strings.Repeat("a", 32), SystemIdentifier: "1", ClusterIdentity: "postgres-system-id:1"}}}
	clock := time.Now().UTC()
	report.StartedAt, report.CompletedAt = clock.Add(time.Second), clock.Add(10*time.Second)
	// Refresh only the test fixture's persisted report after assigning its
	// execution timestamps; retained bytes must bind the returned report.
	reference, err := (providerrestore.FileEvidenceStore{Root: config.EvidenceRoot}).Save(t.Context(), report)
	if err != nil {
		t.Fatal(err)
	}
	f := &qualificationFixture{config: config, authority: authority, sets: sets, ledger: ledger, report: report, clock: clock}
	f.input = ManagedInput{SchemaVersion: 1, Profile: providerrestore.ManagedLocalProfile, RecoverySetID: config.RecoverySetID, OccurrenceID: config.OccurrenceID, InstanceHome: config.InstanceHome, Artifact: config.Artifact, Enrollment: config.Enrollment, ValidationAttemptID: report.Admission.ValidationAttemptID, Validator: "operator", Publisher: "publisher", Authority: AuthorityInput{SystemIdentifier: "2"}}
	f.admission = ManagedAdmissionEvidence{SchemaVersion: 1, Kind: "leapview/managed-local-recovery-admission", Status: "verified-before-activation", OccurrenceID: config.OccurrenceID, RecoverySetID: config.RecoverySetID, TargetID: sets.set.Delivery.TargetID, FrontierDigest: sets.set.FrontierDigest, ArtifactIdentity: config.Artifact.Image, ValidationAttemptID: report.Admission.ValidationAttemptID, ValidationDigest: report.Admission.ValidationDigest, ReportSHA256: reference.SHA256, OriginalsFenced: true}
	sets.set.Status, sets.set.PublishedValidationAttemptID = recoveryset.StatusPrepared, ""
	ledger.occurrence.Status, ledger.occurrence.Fence, ledger.occurrence.Evidence = recovery.StatusPending, recovery.Fence{}, nil
	f.operations = qualificationOperations{
		machineID: func() (string, error) { return strings.Repeat("b", 32), nil },
		now:       func() time.Time { return f.clock },
		restore: func(context.Context) (providerrestore.Report, bool, error) {
			f.calls = append(f.calls, "restore")
			f.clock = f.clock.Add(10 * time.Second)
			return f.report, true, nil
		},
		replay: func(context.Context) (providerrestore.Report, bool, error) {
			f.calls = append(f.calls, "replay")
			f.clock = f.clock.Add(4 * time.Second)
			return f.report, false, nil
		},
		admit: func(context.Context) (ManagedAdmissionEvidence, error) {
			f.calls = append(f.calls, "admit")
			f.clock = f.clock.Add(3 * time.Second)
			f.admission.VerifiedAt = f.clock
			return f.admission, nil
		},
	}
	return f
}

func (f *qualificationFixture) run(t *testing.T) (ManagedQualificationEvidence, error) {
	t.Helper()
	return qualifyManagedRecovery(t.Context(), f.input, f.config, f.authority, f.operations)
}

func TestManagedQualificationRecordsOnlyMeasuredPreactivationExercise(t *testing.T) {
	f := newQualificationFixture(t)
	evidence, err := f.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "restore,replay,admit" || evidence.RestoreDurationMillis != 10000 || evidence.CompletedReplayDurationMillis != 4000 || evidence.AdmissionDurationMillis != 3000 || evidence.CompletedAt.Sub(evidence.StartedAt) != 17*time.Second {
		t.Fatalf("incorrect execution order or measurement: %v %+v", f.calls, evidence)
	}
	if evidence.ActivationQualified || evidence.FullManagedProfileQualified || evidence.Admission.ActivationQualified || evidence.Admission.ReportSHA256 != f.admission.ReportSHA256 {
		t.Fatal("bounded exercise became activation or profile authority")
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{f.config.Credentials.ControlURL, f.config.Credentials.DuckLakeURL, f.config.Credentials.KeyringPath, f.config.InstanceHome, strings.Repeat("b", 32)} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("qualification receipt exposed private configuration or raw machine identity")
		}
	}
}

func TestManagedQualificationRejectsSubstitutionBeforeRestore(t *testing.T) {
	cases := map[string]func(*qualificationFixture){
		"authority":       func(f *qualificationFixture) { f.authority.AuthoritySystemIdentifier = "1" },
		"input authority": func(f *qualificationFixture) { f.input.Authority.SystemIdentifier = "3" },
		"source system":   func(f *qualificationFixture) { f.config.PrimaryFence.Primaries[0].SystemIdentifier = "3" },
		"same host": func(f *qualificationFixture) {
			f.operations.machineID = func() (string, error) { return strings.Repeat("a", 32), nil }
		},
		"missing identity": func(f *qualificationFixture) {
			f.operations.machineID = func() (string, error) { return "", errors.New("private diagnostic") }
		},
		"receipt home":       func(f *qualificationFixture) { f.config.Enrollment.Request.InstanceHome = "/private/other" },
		"input set":          func(f *qualificationFixture) { f.input.RecoverySetID = "other" },
		"input artifact":     func(f *qualificationFixture) { f.input.Artifact.SourceRevision = strings.Repeat("f", 40) },
		"completed intent":   func(f *qualificationFixture) { f.ledger.occurrence.Status = recovery.StatusSucceeded },
		"attempted intent":   func(f *qualificationFixture) { f.ledger.occurrence.AttemptCount = 1 },
		"expired intent":     func(f *qualificationFixture) { f.clock = f.ledger.occurrence.ExpiresAt.Add(time.Second) },
		"published frontier": func(f *qualificationFixture) { f.sets.set.Status = recoveryset.StatusPublished },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newQualificationFixture(t)
			change(f)
			if _, err := f.run(t); err == nil || len(f.calls) != 0 || strings.Contains(err.Error(), "private diagnostic") {
				t.Fatalf("substituted or stale authority executed restore: calls=%v err=%v", f.calls, err)
			}
		})
	}
}

func TestManagedQualificationRequiresFreshPrivateNonoverlappingDestinations(t *testing.T) {
	cases := map[string]func(*qualificationFixture){
		"existing postgres": func(f *qualificationFixture) {
			if err := os.Mkdir(f.config.Postgres.Destination, 0700); err != nil {
				t.Fatal(err)
			}
		},
		"existing root": func(f *qualificationFixture) {
			if err := os.WriteFile(f.config.Roots[0].Destination, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"missing parent": func(f *qualificationFixture) {
			f.config.Postgres.Destination = filepath.Join(f.config.InstanceHome, "missing", "postgres")
		},
		"nonprivate parent": func(f *qualificationFixture) {
			parent := filepath.Join(f.config.InstanceHome, "public")
			if err := os.Mkdir(parent, 0755); err != nil {
				t.Fatal(err)
			}
			f.config.Postgres.Destination = filepath.Join(parent, "postgres")
		},
		"linked parent": func(f *qualificationFixture) {
			parent := filepath.Join(f.config.InstanceHome, "redirected")
			if err := os.Symlink(f.config.InstanceHome, parent); err != nil {
				t.Fatal(err)
			}
			f.config.Postgres.Destination = filepath.Join(parent, "postgres")
		},
		"linked destination": func(f *qualificationFixture) {
			if err := os.Symlink("missing", f.config.Postgres.Destination); err != nil {
				t.Fatal(err)
			}
		},
		"duplicate destinations": func(f *qualificationFixture) { f.config.Postgres.Destination = f.config.Roots[0].Destination },
		"missing roots":          func(f *qualificationFixture) { f.config.Roots = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newQualificationFixture(t)
			change(f)
			if _, err := f.run(t); err == nil || len(f.calls) != 0 {
				t.Fatalf("dirty destination executed provider effects: %v %v", f.calls, err)
			}
		})
	}
}

func TestManagedQualificationNeverAcceptsFailedOrChangedReplayAdmission(t *testing.T) {
	cases := map[string]func(*qualificationFixture){
		"restore failed": func(f *qualificationFixture) {
			f.operations.restore = func(context.Context) (providerrestore.Report, bool, error) {
				return providerrestore.Report{}, true, errors.New("private-provider-output")
			}
		},
		"restore reused completion": func(f *qualificationFixture) {
			previous := f.operations.restore
			f.operations.restore = func(ctx context.Context) (providerrestore.Report, bool, error) {
				value, _, err := previous(ctx)
				return value, false, err
			}
		},
		"restore identity":            func(f *qualificationFixture) { f.report.OccurrenceID = "other" },
		"restore outside measurement": func(f *qualificationFixture) { f.report.StartedAt = f.clock.Add(-time.Second) },
		"restore completed before start": func(f *qualificationFixture) {
			f.report.CompletedAt = f.report.StartedAt.Add(-time.Second)
		},
		"restore completed after return": func(f *qualificationFixture) {
			f.report.CompletedAt = f.clock.Add(11 * time.Second)
		},
		"clock reversed": func(f *qualificationFixture) {
			previous := f.operations.restore
			f.operations.restore = func(ctx context.Context) (providerrestore.Report, bool, error) {
				value, ran, err := previous(ctx)
				f.clock = f.clock.Add(-20 * time.Second)
				return value, ran, err
			}
		},
		"replay failed": func(f *qualificationFixture) {
			f.operations.replay = func(context.Context) (providerrestore.Report, bool, error) {
				return providerrestore.Report{}, false, errors.New("private-provider-output")
			}
		},
		"replay mutated report": func(f *qualificationFixture) {
			previous := f.operations.replay
			f.operations.replay = func(ctx context.Context) (providerrestore.Report, bool, error) {
				value, ran, err := previous(ctx)
				value.FrontierDigest = digestBytes([]byte("other"))
				return value, ran, err
			}
		},
		"replay restored again": func(f *qualificationFixture) {
			previous := f.operations.replay
			f.operations.replay = func(ctx context.Context) (providerrestore.Report, bool, error) {
				value, _, err := previous(ctx)
				return value, true, err
			}
		},
		"admission failed": func(f *qualificationFixture) {
			f.operations.admit = func(context.Context) (ManagedAdmissionEvidence, error) {
				return ManagedAdmissionEvidence{}, errors.New("private-provider-output")
			}
		},
		"admission identity":           func(f *qualificationFixture) { f.admission.RecoverySetID = "other" },
		"admission report substituted": func(f *qualificationFixture) { f.admission.ReportSHA256 = strings.Repeat("f", 64) },
		"admission activated":          func(f *qualificationFixture) { f.admission.ActivationQualified = true },
		"admission fence lost":         func(f *qualificationFixture) { f.admission.OriginalsFenced = false },
		"admission verified before replay": func(f *qualificationFixture) {
			previous := f.operations.admit
			f.operations.admit = func(ctx context.Context) (ManagedAdmissionEvidence, error) {
				value, err := previous(ctx)
				value.VerifiedAt = f.clock.Add(-4 * time.Second)
				return value, err
			}
		},
		"admission verified after return": func(f *qualificationFixture) {
			previous := f.operations.admit
			f.operations.admit = func(ctx context.Context) (ManagedAdmissionEvidence, error) {
				value, err := previous(ctx)
				value.VerifiedAt = f.clock.Add(time.Second)
				return value, err
			}
		},
		"replacement identity changed": func(f *qualificationFixture) {
			calls := 0
			f.operations.machineID = func() (string, error) {
				calls++
				if calls == 1 {
					return strings.Repeat("b", 32), nil
				}
				return strings.Repeat("c", 32), nil
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newQualificationFixture(t)
			change(f)
			evidence, err := f.run(t)
			if err == nil || evidence.SchemaVersion != 0 || strings.Contains(err.Error(), "private-provider-output") {
				t.Fatalf("failed exercise emitted qualification or private diagnostics: %+v %v", evidence, err)
			}
		})
	}
}

func TestManagedQualificationMachineIdentityUsesBoundedActualFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "machine-id")
	id := strings.Repeat("a", 32)
	if err := os.WriteFile(file, []byte(id+"\n"), 0444); err != nil {
		t.Fatal(err)
	}
	if actual, err := readQualificationMachineID(file); err != nil || actual != id {
		t.Fatalf("actual file identity: %q %v", actual, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readQualificationMachineID(link); err == nil {
		t.Fatal("linked machine identity accepted")
	}
	for _, value := range []string{"", strings.Repeat("0", 32), strings.Repeat("A", 32), id + "\n\n", id + " "} {
		if err := os.Chmod(file, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readQualificationMachineID(file); err == nil {
			t.Fatal("invalid machine identity accepted")
		}
	}
}

func TestManagedQualificationOutputCannotOverwriteInputsOrProviderState(t *testing.T) {
	f := newQualificationFixture(t)
	output := filepath.Join(f.config.SecretRoot, "qualification.json")
	if err := ValidateManagedQualificationOutput(output, f.config); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.config.Postgres.Destination, f.config.Roots[0].Destination, f.config.Credentials.KeyringPath, filepath.Join(f.config.InstanceHome, "missing", "qualification.json"), "relative.json"} {
		if err := ValidateManagedQualificationOutput(path, f.config); err == nil {
			t.Fatal("unsafe qualification output accepted")
		}
	}
	if err := os.WriteFile(output, []byte("retained receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManagedQualificationOutput(output, f.config); err == nil {
		t.Fatal("existing qualification receipt could be overwritten")
	}
}
