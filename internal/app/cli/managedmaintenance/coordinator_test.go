package managedmaintenance

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/releasecontract"
)

func requestFixture() Request {
	source := releasecontract.SourceCompatibility{PermissionProfile: releasecontract.Current().PermissionProfile, Schema: 1, Migrations: map[string]string{"001_initial.sql": strings.Repeat("a", 64)}, Engines: map[string]string{"github.com/duckdb/duckdb-go/v2": "v2.1.0", "github.com/riverqueue/river": "v0.47.0"}, RolePolicy: strings.Repeat("b", 64)}
	release := Release{ArtifactAdmissionDigest: "sha256:" + strings.Repeat("e", 64), Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64), Revision: strings.Repeat("a", 40), ConfigurationDigest: "sha256:" + strings.Repeat("c", 64), CredentialDigest: "sha256:" + strings.Repeat("d", 64)}
	r := Request{Version: 1, Target: "fixture", Predecessor: release, Candidate: release, SourceBefore: source, SourceAfter: source, Budgets: Budgets{Phase: time.Second, Total: time.Minute}}
	r.Candidate.Image = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	r.Candidate.Revision = strings.Repeat("b", 40)
	return r
}

type memoryJournal struct {
	state  State
	exists bool
}

func (j *memoryJournal) Load(context.Context) (State, error) {
	if !j.exists {
		return State{}, ErrNoOperation
	}
	return j.state, nil
}
func (j *memoryJournal) Save(_ context.Context, s State) error {
	j.state = s
	j.exists = true
	return nil
}

type recordingEffects struct {
	events []string
	fail   string
}

func (e *recordingEffects) step(name string) error {
	e.events = append(e.events, name)
	if e.fail == name {
		return errors.New("injected " + name)
	}
	return nil
}
func (e *recordingEffects) Preflight(context.Context) error      { return e.step("preflight") }
func (e *recordingEffects) CloseAdmission(context.Context) error { return e.step("close-work") }
func (e *recordingEffects) CloseIngress(context.Context) error   { return e.step("close-ingress") }
func (e *recordingEffects) DrainAndStop(context.Context) error   { return e.step("drain-stop") }
func (e *recordingEffects) StartPrepared(_ context.Context, r Release) error {
	return e.step("start:" + r.Revision)
}
func (e *recordingEffects) VerifyPrepared(context.Context, Release) error { return e.step("verify") }
func (e *recordingEffects) OpenWork(context.Context, Release) error       { return e.step("open-work") }
func (e *recordingEffects) OpenIngress(context.Context, Release) error    { return e.step("open-ingress") }
func (e *recordingEffects) FinalizeWork(context.Context, Release) error   { return e.step("finalize") }
func TestPreflightFailureLeavesPredecessorServing(t *testing.T) {
	r := requestFixture()
	j := &memoryJournal{}
	e := &recordingEffects{fail: "preflight"}
	c := Coordinator{Request: r, Journal: j, Effects: e}
	if c.Run(t.Context()) == nil {
		t.Fatal("accepted failed preflight")
	}
	if !reflect.DeepEqual(e.events, []string{"preflight"}) {
		t.Fatal(e.events)
	}
}
func TestInterruptedHandoffRequiresExplicitClosedRecovery(t *testing.T) {
	r := requestFixture()
	j := &memoryJournal{}
	e := &recordingEffects{fail: "open-work"}
	c := Coordinator{Request: r, Journal: j, Effects: e}
	if c.Run(t.Context()) == nil {
		t.Fatal("accepted failed worker startup")
	}
	if j.state.Phase == Succeeded {
		t.Fatal("failed startup committed")
	}
	if e.events[len(e.events)-2] != "close-work" || e.events[len(e.events)-1] != "close-ingress" {
		t.Fatal(e.events)
	}
	e.events = nil
	e.fail = ""
	if !errors.Is(c.Run(t.Context()), ErrRecoveryRequired) {
		t.Fatal("implicitly resumed interrupted operation")
	}
	if len(e.events) != 0 {
		t.Fatal(e.events)
	}
	if err := c.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if j.state.Phase != Recovered {
		t.Fatal(j.state)
	}
	if !reflect.DeepEqual(e.events, []string{"preflight", "close-work", "close-ingress", "drain-stop", "start:" + r.Predecessor.Revision, "verify", "open-work", "open-ingress", "finalize"}) {
		t.Fatal(e.events)
	}
}

func TestEveryPostClosureFailureRetainsExplicitRecovery(t *testing.T) {
	for _, step := range []string{"close-work", "close-ingress", "drain-stop", "start:" + requestFixture().Candidate.Revision, "verify", "open-work", "open-ingress", "finalize"} {
		t.Run(step, func(t *testing.T) {
			request := requestFixture()
			journal := &memoryJournal{}
			effects := &recordingEffects{fail: step}
			coordinator := Coordinator{Request: request, Journal: journal, Effects: effects}
			if coordinator.Run(t.Context()) == nil {
				t.Fatal("accepted injected failure")
			}
			if !journal.exists || journal.state.Phase == Succeeded {
				t.Fatal(journal.state)
			}
			if got := effects.events[len(effects.events)-2:]; !reflect.DeepEqual(got, []string{"close-work", "close-ingress"}) {
				t.Fatal(effects.events)
			}
			effects.events = nil
			if !errors.Is(coordinator.Run(t.Context()), ErrRecoveryRequired) || len(effects.events) != 0 {
				t.Fatal("implicitly resumed operation", effects.events)
			}
		})
	}
}

type delayedEffects struct{ recordingEffects }

func (e *delayedEffects) VerifyPrepared(ctx context.Context, _ Release) error {
	<-ctx.Done()
	return nil
}
func TestElapsedPhaseCannotReportSuccess(t *testing.T) {
	request := requestFixture()
	request.Budgets.Phase = 10 * time.Millisecond
	journal := &memoryJournal{}
	effects := &delayedEffects{}
	coordinator := Coordinator{Request: request, Journal: journal, Effects: effects}
	if err := coordinator.Run(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	if journal.state.Phase != Verifying {
		t.Fatal(journal.state)
	}
	for _, event := range effects.events {
		if event == "open-work" {
			t.Fatal("opened after timeout")
		}
	}
}
