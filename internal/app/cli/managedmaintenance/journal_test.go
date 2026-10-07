package managedmaintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/hostmaintenance"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
)

func TestFileJournalRestartReconcilesCommittedCandidate(t *testing.T) {
	root := t.TempDir()
	request := requestFixture()
	journal, err := OpenJournal(root, request)
	if err != nil {
		t.Fatal(err)
	}
	if journal.InheritedLockFile() == nil {
		t.Fatal("missing controller descriptor")
	}
	if _, err := OpenJournal(root, request); !errors.Is(err, instancelock.ErrAlreadyInUse) {
		t.Fatalf("concurrent owner: %v", err)
	}
	effects := &recordingEffects{fail: "finalize"}
	coordinator := Coordinator{Request: request, Journal: journal, Effects: effects}
	if coordinator.Run(t.Context()) == nil {
		t.Fatal("accepted lost finalization")
	}
	state, err := journal.Load(t.Context())
	if err != nil || state.Phase != Committed || !state.CommitEstablished {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if journal.InheritedLockFile() != nil {
		t.Fatal("released descriptor exposed")
	}
	if hostmaintenance.Check(root) == nil {
		t.Fatal("ordinary mutation admitted after crash")
	}
	journal, err = OpenJournal(root, request)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	effects = &recordingEffects{fail: "verify"}
	coordinator.Journal, coordinator.Effects = journal, effects
	if !errors.Is(coordinator.Run(t.Context()), ErrRecoveryRequired) {
		t.Fatal("implicit restart resumed work")
	}
	if coordinator.Recover(t.Context()) == nil {
		t.Fatal("accepted failed reconciliation")
	}
	effects.events, effects.fail = nil, ""
	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, event := range effects.events {
		if event == "start:"+request.Predecessor.Revision {
			t.Fatal("lost commit selected predecessor")
		}
	}
	state, err = journal.Load(t.Context())
	if err != nil || state.Phase != Succeeded {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if err := hostmaintenance.Check(root); err != nil {
		t.Fatal(err)
	}
}

func TestFileJournalPreservesFailedHandoffAndRejectsSubstitution(t *testing.T) {
	root := t.TempDir()
	request := requestFixture()
	journal, err := OpenJournal(root, request)
	if err != nil {
		t.Fatal(err)
	}
	effects := &recordingEffects{fail: "verify"}
	coordinator := Coordinator{Request: request, Journal: journal, Effects: effects}
	if coordinator.Run(t.Context()) == nil {
		t.Fatal("accepted failed verification")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	other := request
	other.Candidate.Revision = strings.Repeat("c", 40)
	if _, err := OpenJournal(root, other); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("replaced interrupted operation: %v", err)
	}
	journal, err = OpenJournal(root, request)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	coordinator.Journal = journal
	effects.fail = ""
	effects.events = nil
	if err := coordinator.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := journal.Load(t.Context())
	if err != nil || state.Phase != Recovered {
		t.Fatalf("%+v %v", state, err)
	}
	found := false
	for _, event := range effects.events {
		if event == "start:"+request.Predecessor.Revision {
			found = true
		}
	}
	if !found {
		t.Fatal(effects.events)
	}
}

func TestFileJournalRejectsPairedUpgradeAndUnsafeWrites(t *testing.T) {
	root := t.TempDir()
	request := requestFixture()
	upgrade := filepath.Join(root, hostmaintenance.JournalName)
	if err := os.WriteFile(upgrade, []byte(`{"version":1,"state":{"phase":"migrating"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(root, request); err == nil {
		t.Fatal("admitted image handoff during paired recovery")
	}
	if err := os.Remove(upgrade); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenJournal(root, request)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	digest, _ := request.Digest()
	now := time.Now().UTC()
	state := State{Version: 1, RequestDigest: digest, Target: request.Target, Phase: Prepared, StartedAt: now, Deadline: now.Add(request.Budgets.Total)}
	if err := journal.Save(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*State){func(s *State) { s.Phase = OpeningWork }, func(s *State) { s.Target = "other" }, func(s *State) { s.Deadline = s.Deadline.Add(time.Hour) }, func(s *State) { s.RequestDigest = "sha256:" + strings.Repeat("f", 64) }} {
		invalid := state
		change(&invalid)
		if journal.Save(t.Context(), invalid) == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if journal.Save(canceled, state) == nil {
		t.Fatal("accepted canceled journal write")
	}
}

func TestFileJournalRejectsCorruption(t *testing.T) {
	for _, kind := range []string{"symlink", "permissions", "unknown-field", "trailing", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, JournalName)
			raw := []byte(`{"version":1,"phase":"succeeded","unknown":true}`)
			if kind == "trailing" {
				raw = append(raw, []byte(" {}")...)
			}
			if kind == "oversized" {
				raw = []byte(strings.Repeat(" ", 16385))
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "permissions" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" {
				if err := os.Rename(path, path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".real", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := OpenJournal(root, requestFixture()); err == nil {
				t.Fatal("accepted corrupt journal")
			}
		})
	}
}
