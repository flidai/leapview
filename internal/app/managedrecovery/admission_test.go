package managedrecovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/refresh/recovery"
)

func TestManagedAdmissionDoesNotReinterpretRemoteConsumer(t *testing.T) {
	handoff, _, _ := managedCredentialFixture(t)
	report := providerrestore.Report{SchemaVersion: providerrestore.ReportSchemaVersion, Kind: providerrestore.ReportKind, Status: providerrestore.StatusSucceeded, OccurrenceID: handoff.ManagedLocal.OccurrenceID, Handoff: handoff}
	if err := providerrestore.ValidateHandoffReport(report, providerrestore.HandoffExpectations{}); err == nil {
		t.Fatal("remote consumer accepted a managed-local report")
	}
	if _, err := AdmitManagedRecovery(t.Context(), ManagedConfig{}, ManagedAuthorities{}); err == nil {
		t.Fatal("missing authoritative completed restore admitted")
	}
}

func TestManagedAdmissionRevalidatesExactCompletedFrontierAndFence(t *testing.T) {
	config, authority, _, _ := managedAdmissionFixture(t)
	snapshot, err := readManagedAdmission(t.Context(), config, authority)
	if err != nil {
		t.Fatal(err)
	}
	if err := providerrestore.ValidateHandoffReport(snapshot.report, providerrestore.HandoffExpectations{}); err == nil {
		t.Fatal("real persisted local profile became a remote handoff")
	}
	fence := &admissionFence{}
	evidence, err := verifyManagedAdmission(t.Context(), config, authority, snapshot, fence, admissionVerifier{value: snapshot.report.Verification})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "verified-before-activation" || evidence.ActivationQualified || !evidence.OriginalsFenced || fence.calls != 2 || evidence.ReportSHA256 != snapshot.occurrence.Evidence[0].SHA256 {
		t.Fatal("incomplete or overclaimed admission")
	}
	for name, modify := range map[string]func(*admissionFence, *admissionVerifier){
		"missing fence":                          func(f *admissionFence, _ *admissionVerifier) { f.failAt = 1 },
		"fence lost after provider verification": func(f *admissionFence, _ *admissionVerifier) { f.failAt = 2 },
		"changed control state":                  func(_ *admissionFence, v *admissionVerifier) { v.value.ControlStateDigest = "changed" },
		"missing files or keyring":               func(_ *admissionFence, v *admissionVerifier) { v.err = os.ErrNotExist },
	} {
		t.Run(name, func(t *testing.T) {
			f, v := &admissionFence{}, admissionVerifier{value: snapshot.report.Verification}
			modify(f, &v)
			if _, err := verifyManagedAdmission(t.Context(), config, authority, snapshot, f, v); err == nil {
				t.Fatal("changed physical admission accepted")
			}
		})
	}
}

func TestManagedAdmissionRejectsStaleTamperedAndWrongProfileAuthority(t *testing.T) {
	for name, mutate := range map[string]func(ManagedConfig, *admissionSets, *admissionLedger){
		"pending occurrence": func(_ ManagedConfig, _ *admissionSets, l *admissionLedger) {
			l.occurrence.Status = recovery.StatusPending
		},
		"different artifact": func(_ ManagedConfig, _ *admissionSets, l *admissionLedger) { l.occurrence.ArtifactIdentity = "foreign" },
		"unpublished set": func(_ ManagedConfig, s *admissionSets, _ *admissionLedger) {
			s.set.Status = recoveryset.StatusPrepared
			s.set.PublishedValidationAttemptID = ""
		},
		"validation changed": func(_ ManagedConfig, s *admissionSets, _ *admissionLedger) {
			s.result.ResultDigest = digestBytes([]byte("foreign"))
		},
		"validation bytes changed": func(_ ManagedConfig, s *admissionSets, _ *admissionLedger) { s.result.Evidence = []byte("{}") },
		"remote profile with valid durable digest": func(c ManagedConfig, _ *admissionSets, l *admissionLedger) {
			store := providerrestore.FileEvidenceStore{Root: c.EvidenceRoot}
			report, err := store.Load(t.Context(), l.occurrence.Evidence[0])
			if err != nil {
				t.Fatal(err)
			}
			report.Handoff.SchemaVersion, report.Handoff.ManagedLocal = providerrestore.HandoffSchemaVersion, nil
			reference, err := store.Save(t.Context(), report)
			if err != nil {
				t.Fatal(err)
			}
			l.occurrence.Evidence = []recovery.EvidenceReference{reference}
		},
		"report bytes changed": func(c ManagedConfig, _ *admissionSets, l *admissionLedger) {
			if err := os.WriteFile(filepath.Join(c.EvidenceRoot, l.occurrence.Evidence[0].SHA256+".json"), []byte("{}"), 0600); err != nil {
				panic(err)
			}
		},
		"linked report": func(c ManagedConfig, _ *admissionSets, l *admissionLedger) {
			path := filepath.Join(c.EvidenceRoot, l.occurrence.Evidence[0].SHA256+".json")
			if err := os.Rename(path, path+".original"); err != nil {
				panic(err)
			}
			if err := os.Symlink(path+".original", path); err != nil {
				panic(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, a, s, l := managedAdmissionFixture(t)
			mutate(c, s, l)
			if _, err := readManagedAdmission(t.Context(), c, a); err == nil {
				t.Fatal("stale or tampered admission accepted")
			}
		})
	}
	config, authority, sets, _ := managedAdmissionFixture(t)
	snapshot, err := readManagedAdmission(t.Context(), config, authority)
	if err != nil {
		t.Fatal(err)
	}
	verifier := admissionVerifier{value: snapshot.report.Verification, run: func() { sets.set.Status = recoveryset.StatusInvalid }}
	if _, err := verifyManagedAdmission(t.Context(), config, authority, snapshot, &admissionFence{}, verifier); err == nil {
		t.Fatal("frontier changed during verification accepted")
	}
	if _, err := AdmitManagedRecovery(t.Context(), config, authority); err == nil {
		t.Fatal("missing physical restored providers accepted")
	}
}

func TestManagedAdmissionInvalidationRemovesOnlyOwnPrivateEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "admission.json")
	if err := InvalidateManagedAdmissionEvidence(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ManagedAdmissionEvidence{SchemaVersion: 1, Kind: "leapview/managed-local-recovery-admission", Status: "verified-before-activation"})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := InvalidateManagedAdmissionEvidence(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("stale admission retained")
	}
	for name, value := range map[string][]byte{"credentials": []byte(`{"schemaVersion":1,"profile":"managed-local-v1","controlUrl":"secret"}`), "foreign": []byte(`{"kind":"another-report"}`)} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, value, 0600); err != nil {
				t.Fatal(err)
			}
			if err := InvalidateManagedAdmissionEvidence(path); err == nil {
				t.Fatal("unrelated private document removed")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}
