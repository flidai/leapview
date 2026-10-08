package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/cli/managedmaintenance"
	"github.com/flidai/leapview/internal/platform/hostmaintenance"
	"github.com/flidai/leapview/internal/platform/releasecontract"
)

// This bridge uses the real application admission/control handler and durable
// coordinator journal. Process and proxy effects are controlled here; it does
// not qualify Docker, SQL readiness, or a complete managed deployment profile.
func TestManagedEnrollmentApplicationAdmission(t *testing.T) {
	for _, failure := range []string{"none", "readiness", "finalization"} {
		t.Run(failure, func(t *testing.T) {
			request := managedEnrollmentRequest()
			root := t.TempDir()
			journal, err := managedmaintenance.OpenJournal(root, request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = journal.Close() }()
			if _, err := journal.Load(t.Context()); !errors.Is(err, managedmaintenance.ErrNoOperation) {
				t.Fatalf("initial enrollment must not seed an admitted journal: %v", err)
			}
			effects := &managedEnrollmentEffects{t: t, request: request, journal: journal, failReadiness: failure == "readiness", failFinalize: failure == "finalization"}
			effects.newProcess()
			if effects.product(http.MethodPost) != http.StatusServiceUnavailable || effects.writes.Load() != 0 || effects.workers.Load() != 0 {
				t.Fatal("initial process admitted work before enrollment")
			}
			coordinator := managedmaintenance.Coordinator{Request: request, Journal: journal, Effects: effects}
			err = coordinator.Run(t.Context())
			state, loadErr := journal.Load(t.Context())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if failure == "none" {
				if err != nil || state.Phase != managedmaintenance.Succeeded || !state.CommitEstablished {
					t.Fatalf("enrollment result: state=%+v err=%v", state, err)
				}
				if effects.gate.status().State != "admitted" || effects.workers.Load() != 1 || effects.writes.Load() != 1 {
					t.Fatal("successful enrollment did not finalize the published process")
				}
				if err := hostmaintenance.Check(root); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || effects.workers.Load() != 0 || effects.product(http.MethodPost) != http.StatusServiceUnavailable {
				t.Fatalf("failed enrollment left work open: %v", err)
			}
			wantPhase := managedmaintenance.Verifying
			if failure == "finalization" {
				wantPhase = managedmaintenance.Committed
			}
			if state.Phase != wantPhase || state.CommitEstablished != (failure == "finalization") {
				t.Fatalf("failure lost durable operation: %+v", state)
			}
			if hostmaintenance.Check(root) == nil {
				t.Fatal("unfinished enrollment allowed an ordinary host mutation")
			}

			// Reopen the on-disk journal as a new controller, preserving the data
			// acknowledged before finalization failed. Recover must restart the
			// closed process, not revive its terminal admission gate.
			oldGate := effects.gate
			oldWrites := effects.writes.Load()
			if failure == "finalization" && oldWrites != 1 {
				t.Fatal("publication did not acknowledge the test write before interruption")
			}
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal, err = managedmaintenance.OpenJournal(root, request)
			if err != nil {
				t.Fatal(err)
			}
			effects.journal = journal
			effects.failReadiness, effects.failFinalize = false, false
			coordinator = managedmaintenance.Coordinator{Request: request, Journal: journal, Effects: effects}
			if err := coordinator.Run(t.Context()); !errors.Is(err, managedmaintenance.ErrRecoveryRequired) {
				t.Fatalf("ordinary run resumed interrupted enrollment: %v", err)
			}
			if err := coordinator.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			state, err = journal.Load(t.Context())
			wantPhase = managedmaintenance.Recovered
			if failure == "finalization" {
				wantPhase = managedmaintenance.Succeeded
			}
			if err != nil || state.Phase != wantPhase || !state.CommitEstablished {
				t.Fatalf("recovery result: state=%+v err=%v", state, err)
			}
			digest, _ := request.Digest()
			if state.RequestDigest != digest || effects.gate.status().Operation != digest || effects.gate.status().State != "admitted" {
				t.Fatal("recovery changed the selected enrollment identity")
			}
			if effects.gate == oldGate || oldGate.status().State != "closed" || effects.writes.Load() != oldWrites+1 {
				t.Fatal("recovery reused a terminal gate or lost acknowledged data")
			}
			if err := hostmaintenance.Check(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func managedEnrollmentRequest() managedmaintenance.Request {
	source := releasecontract.SourceCompatibility{PermissionProfile: releasecontract.Current().PermissionProfile, Schema: 1, Migrations: map[string]string{"001_initial.sql": strings.Repeat("a", 64)}, Engines: map[string]string{"github.com/duckdb/duckdb-go/v2": "v2.1.0", "github.com/riverqueue/river": "v0.47.0"}, RolePolicy: strings.Repeat("b", 64)}
	release := managedmaintenance.Release{ArtifactAdmissionDigest: "sha256:" + strings.Repeat("e", 64), Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64), Revision: strings.Repeat("a", 40), ConfigurationDigest: "sha256:" + strings.Repeat("c", 64), CredentialDigest: "sha256:" + strings.Repeat("d", 64)}
	return managedmaintenance.Request{Version: 1, Operation: "enroll", Target: "application-enrollment", Predecessor: release, Candidate: release, SourceBefore: source, SourceAfter: source, Budgets: managedmaintenance.Budgets{Phase: 30 * time.Second, Total: time.Minute}}
}

type managedEnrollmentEffects struct {
	t                           *testing.T
	request                     managedmaintenance.Request
	journal                     *managedmaintenance.FileJournal
	gate                        *maintenanceAdmission
	control, public             http.Handler
	workers, writes             atomic.Int64
	failReadiness, failFinalize bool
}

func (e *managedEnrollmentEffects) newProcess() {
	prepare := func(context.Context) error {
		if e.failReadiness {
			return errors.New("runtime dependency unavailable")
		}
		return nil
	}
	gate := newMaintenanceAdmission(e.request.Candidate.Revision, prepare,
		func(context.Context) error { e.workers.Add(1); return nil },
		func(context.Context) error { e.workers.Store(0); return nil })
	e.gate = gate
	e.control = (&Application{lifecycle: &applicationLifecycleOwner{maintenance: gate}}).MaintenanceHandler()
	e.public = gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			e.writes.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}), prepare)
	e.t.Cleanup(func() { _ = gate.close(context.Background()) })
}

func (e *managedEnrollmentEffects) product(method string) int {
	w := httptest.NewRecorder()
	e.public.ServeHTTP(w, httptest.NewRequest(method, "/enrollment-counter", nil))
	return w.Code
}

func (e *managedEnrollmentEffects) invoke(ctx context.Context, action string) error {
	digest, err := e.request.Digest()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(maintenanceControlRequest{Revision: e.request.Candidate.Revision, Operation: digest, LeaseMilliseconds: e.request.Budgets.Phase.Milliseconds()})
	if err != nil {
		return err
	}
	w := httptest.NewRecorder()
	e.control.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodPost, action, bytes.NewReader(raw)))
	if w.Code != http.StatusOK {
		return fmt.Errorf("maintenance %s: status %d: %s", action, w.Code, w.Body.String())
	}
	return nil
}

func (e *managedEnrollmentEffects) Preflight(context.Context) error    { return nil }
func (e *managedEnrollmentEffects) CloseIngress(context.Context) error { return nil }
func (e *managedEnrollmentEffects) CloseAdmission(ctx context.Context) error {
	return e.invoke(ctx, "/close")
}
func (e *managedEnrollmentEffects) DrainAndStop(context.Context) error {
	if !e.gate.status().Drained || e.workers.Load() != 0 {
		return errors.New("process stopped before work drained")
	}
	return nil
}
func (e *managedEnrollmentEffects) StartPrepared(_ context.Context, selected managedmaintenance.Release) error {
	if selected != e.request.Candidate {
		return errors.New("enrollment selected another release")
	}
	e.newProcess()
	if e.product(http.MethodGet) != http.StatusServiceUnavailable || e.workers.Load() != 0 {
		return errors.New("fresh process began with work admitted")
	}
	return nil
}
func (e *managedEnrollmentEffects) VerifyPrepared(ctx context.Context, _ managedmaintenance.Release) error {
	if err := e.invoke(ctx, "/prepare"); err != nil {
		return err
	}
	if e.gate.status().State != "prepared" || e.workers.Load() != 0 || e.product(http.MethodGet) != http.StatusServiceUnavailable {
		return errors.New("preparation admitted work before the lease opened")
	}
	return nil
}
func (e *managedEnrollmentEffects) OpenWork(ctx context.Context, _ managedmaintenance.Release) error {
	return e.invoke(ctx, "/open")
}
func (e *managedEnrollmentEffects) OpenIngress(context.Context, managedmaintenance.Release) error {
	if e.gate.status().State != "provisional" || e.workers.Load() != 1 || e.product(http.MethodPost) != http.StatusOK {
		return errors.New("publication preceded provisional work authorization")
	}
	return nil
}
func (e *managedEnrollmentEffects) FinalizeWork(ctx context.Context, _ managedmaintenance.Release) error {
	state, err := e.journal.Load(ctx)
	if err != nil || state.Phase != managedmaintenance.Committed || !state.CommitEstablished {
		return errors.New("application finalization preceded durable publication commit")
	}
	if e.failFinalize {
		return errors.New("controller interrupted before finalization")
	}
	return e.invoke(ctx, "/finalize")
}
