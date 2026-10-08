package managedmaintenance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/hostmaintenance"
)

func enrollmentFixture() Request {
	r := requestFixture()
	r.Operation = "enroll"
	r.Candidate = r.Predecessor
	return r
}

func TestEnrollmentBindsOneExactReleaseAndSource(t *testing.T) {
	for _, variant := range []string{"valid", "unknown-operation", "different-image", "different-revision", "different-admission", "different-configuration", "different-credential", "different-source", "implicit-enrollment"} {
		t.Run(variant, func(t *testing.T) {
			r := enrollmentFixture()
			switch variant {
			case "unknown-operation":
				r.Operation = "bootstrap"
			case "different-image":
				r.Candidate.Image = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
			case "different-revision":
				r.Candidate.Revision = strings.Repeat("b", 40)
			case "different-admission":
				r.Candidate.ArtifactAdmissionDigest = "sha256:" + strings.Repeat("f", 64)
			case "different-configuration":
				r.Candidate.ConfigurationDigest = "sha256:" + strings.Repeat("f", 64)
			case "different-credential":
				r.Candidate.CredentialDigest = "sha256:" + strings.Repeat("f", 64)
			case "different-source":
				r.SourceAfter.Migrations = map[string]string{"001_initial.sql": strings.Repeat("c", 64)}
			case "implicit-enrollment":
				r.Operation = ""
			}
			if err := r.Validate(); (err == nil) != (variant == "valid") {
				t.Fatalf("Validate=%v", err)
			}
		})
	}
}

func TestEnrollmentPreservesLegacyRequestIdentity(t *testing.T) {
	r := requestFixture()
	// The old wire shape must remain byte-identical for unfinished operations.
	legacy := struct {
		Version      int     `json:"version"`
		Target       string  `json:"target"`
		Predecessor  Release `json:"predecessor"`
		Candidate    Release `json:"candidate"`
		SourceBefore any     `json:"sourceBefore"`
		SourceAfter  any     `json:"sourceAfter"`
		Budgets      Budgets `json:"budgets"`
	}{r.Version, r.Target, r.Predecessor, r.Candidate, r.SourceBefore, r.SourceAfter, r.Budgets}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Digest()
	if err != nil || got != byteIdentity(raw) {
		t.Fatalf("legacy identity changed: %s %v", got, err)
	}
}

func TestEnrollmentActionCannotBeImplicit(t *testing.T) {
	for _, action := range []string{"run", "enroll", "recover", "status"} {
		for _, enrollment := range []bool{false, true} {
			r := requestFixture()
			if enrollment {
				r = enrollmentFixture()
			}
			want := action == "recover" || action == "status" || (action == "enroll") == enrollment
			if err := validateRequestAction(action, r); (err == nil) != want {
				t.Fatalf("action=%s enrollment=%t error=%v", action, enrollment, err)
			}
		}
	}
}

func TestEnrollmentInterruptionUsesDurableSameReleaseRecovery(t *testing.T) {
	for _, failure := range []string{"close-work", "close-ingress", "drain-stop", "start:" + enrollmentFixture().Candidate.Revision, "verify", "open-work", "open-ingress", "finalize"} {
		t.Run(failure, func(t *testing.T) {
			r := enrollmentFixture()
			root := t.TempDir()
			j, err := OpenJournal(root, r)
			if err != nil {
				t.Fatal(err)
			}
			e := &recordingEffects{fail: failure}
			c := Coordinator{Request: r, Journal: j, Effects: e}
			if c.Run(t.Context()) == nil {
				t.Fatal("accepted interrupted enrollment")
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			if hostmaintenance.Check(root) == nil {
				t.Fatal("ordinary host mutation bypassed interrupted enrollment")
			}
			other := r
			other.Budgets.Total++
			if substitute, err := OpenJournal(root, other); !errors.Is(err, ErrRecoveryRequired) {
				if substitute != nil {
					_ = substitute.Close()
				}
				t.Fatalf("substituted enrollment request: %v", err)
			}
			j, err = OpenJournal(root, r)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			e = &recordingEffects{}
			c.Journal, c.Effects = j, e
			if !errors.Is(c.Run(t.Context()), ErrRecoveryRequired) || len(e.events) != 0 {
				t.Fatal("implicitly resumed enrollment")
			}
			if err := c.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := []string{"preflight", "close-work", "close-ingress", "drain-stop", "start:" + r.Candidate.Revision, "verify", "open-work", "open-ingress", "finalize"}
			if !reflect.DeepEqual(e.events, want) {
				t.Fatal(e.events)
			}
			state, err := j.Load(t.Context())
			if err != nil || !state.CommitEstablished || (state.Phase != Succeeded && state.Phase != Recovered) {
				t.Fatalf("state=%+v error=%v", state, err)
			}
			if err := hostmaintenance.Check(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompletedEnrollmentAllowsSubsequentCompatibleHandoff(t *testing.T) {
	root := t.TempDir()
	enrollment := enrollmentFixture()
	journal, err := OpenJournal(root, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{Request: enrollment, Journal: journal, Effects: &recordingEffects{}}
	if err := coordinator.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	handoff := requestFixture()
	journal, err = OpenJournal(root, handoff)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	digest, _ := enrollment.Digest()
	raw, err := os.ReadFile(filepath.Join(root, "managed-image-history", digest[7:]+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var enrolled State
	if err := json.Unmarshal(raw, &enrolled); err != nil || enrolled.RequestDigest != digest || enrolled.Phase != Succeeded {
		t.Fatalf("enrollment evidence lost: %+v %v", enrolled, err)
	}
	coordinator = &Coordinator{Request: handoff, Journal: journal, Effects: &recordingEffects{}}
	if err := coordinator.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, err := journal.Load(t.Context())
	if err != nil || state.Phase != Succeeded || state.RequestDigest == digest {
		t.Fatalf("subsequent handoff failed: %+v %v", state, err)
	}
}

func TestEnrollmentCannotReplaceACompletedManagedOperation(t *testing.T) {
	for _, first := range []string{"enrollment", "handoff"} {
		t.Run(first, func(t *testing.T) {
			root := t.TempDir()
			original := enrollmentFixture()
			if first == "handoff" {
				original = requestFixture()
			}
			journal, err := OpenJournal(root, original)
			if err != nil {
				t.Fatal(err)
			}
			coordinator := Coordinator{Request: original, Journal: journal, Effects: &recordingEffects{}}
			if err := coordinator.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(root, JournalName))
			if err != nil {
				t.Fatal(err)
			}
			next := enrollmentFixture()
			next.Predecessor = requestFixture().Candidate
			next.Candidate = next.Predecessor
			if reopened, err := OpenJournal(root, next); err == nil {
				_ = reopened.Close()
				t.Fatal("second enrollment bypassed the compatible handoff contract")
			}
			after, err := os.ReadFile(filepath.Join(root, JournalName))
			if err != nil || string(after) != string(before) {
				t.Fatal("rejected enrollment changed the existing operation")
			}
			if _, err := os.Stat(filepath.Join(root, "managed-image-history")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected enrollment archived the existing operation: %v", err)
			}
			journal, err = OpenJournal(root, original)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			effects := &recordingEffects{}
			coordinator.Journal, coordinator.Effects = journal, effects
			if err := coordinator.Run(t.Context()); err != nil || len(effects.events) != 0 {
				t.Fatalf("original operation lost terminal idempotence: %v %v", err, effects.events)
			}
		})
	}
}
