package hostinstall

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type memoryJournal struct {
	state State
	fail  Phase
}

func (m *memoryJournal) Load(context.Context) (State, error) { return m.state, nil }
func (m *memoryJournal) Save(_ context.Context, s State) error {
	if s.Phase == m.fail {
		return errors.New("disk failure")
	}
	m.state = s
	return nil
}

type effects struct {
	calls []string
	fail  string
}

func (e *effects) call(s string) error {
	e.calls = append(e.calls, s)
	if e.fail == s {
		return errors.New("injected")
	}
	return nil
}
func (e *effects) Admit(context.Context, Identity) error { return e.call("admit") }
func (e *effects) Quiesce(context.Context) error         { return e.call("quiesce") }
func (e *effects) CaptureAndVerify(context.Context, Identity) (string, error) {
	return "sha256:" + hex64('d'), e.call("capture-restore-verify")
}
func (e *effects) Migrate(context.Context, Identity, string) error { return e.call("migrate") }
func (e *effects) StartCandidateIsolated(context.Context, Identity) error {
	return e.call("candidate-isolated")
}
func (e *effects) ValidateCandidate(context.Context, Identity) error { return e.call("validate") }
func (e *effects) ExposeCandidate(context.Context, Identity) error   { return e.call("expose") }
func (e *effects) StopCandidate(context.Context) error               { return e.call("stop") }
func (e *effects) Restore(context.Context, Identity, string) error   { return e.call("restore") }
func (e *effects) VerifyPredecessor(context.Context, Identity) error {
	return e.call("verify-predecessor")
}
func (e *effects) ExposePredecessor(context.Context, Identity) error {
	return e.call("expose-predecessor")
}
func hex64(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
func identity() Identity {
	return Identity{Target: "demo-02", Predecessor: "ghcr.io/flidai/leapview@sha256:" + hex64('a'), Candidate: "ghcr.io/flidai/leapview@sha256:" + hex64('b'), ArtifactAdmissionDigest: "sha256:" + hex64('c')}
}
func setup() (*Coordinator, *memoryJournal, *effects) {
	j := &memoryJournal{state: State{Identity: identity(), Phase: Prepared}}
	e := &effects{}
	return &Coordinator{Journal: j, Effects: e}, j, e
}
func TestSuccessPersistsCommitBeforeExposing(t *testing.T) {
	c, j, e := setup()
	if err := c.Run(context.Background(), identity()); err != nil {
		t.Fatal(err)
	}
	if j.state.Phase != Succeeded {
		t.Fatal(j.state)
	}
	want := []string{"admit", "quiesce", "capture-restore-verify", "migrate", "candidate-isolated", "validate", "expose"}
	if !reflect.DeepEqual(e.calls, want) {
		t.Fatal(e.calls)
	}
}
func TestMigrationFailureRequiresExplicitPairedRecovery(t *testing.T) {
	c, j, e := setup()
	e.fail = "migrate"
	if err := c.Run(context.Background(), identity()); err == nil {
		t.Fatal("accepted")
	}
	if j.state.Phase != Migrating {
		t.Fatal(j.state)
	}
	e.calls = nil
	e.fail = ""
	if !errors.Is(c.Run(context.Background(), identity()), ErrRecoveryRequired) {
		t.Fatal("replayed migration")
	}
	if len(e.calls) != 0 {
		t.Fatal(e.calls)
	}
	if err := c.Recover(context.Background(), identity()); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop", "restore", "verify-predecessor", "expose-predecessor"}
	if !reflect.DeepEqual(e.calls, want) {
		t.Fatal(e.calls)
	}
	if j.state.Phase != Recovered {
		t.Fatal(j.state)
	}
}
func TestFailedRestoreNeverReopens(t *testing.T) {
	c, j, e := setup()
	j.state.Phase = Migrating
	j.state.RestoreRequired = true
	j.state.RecoveryDigest = "sha256:" + hex64('d')
	e.fail = "restore"
	if c.Recover(context.Background(), identity()) == nil {
		t.Fatal("accepted")
	}
	if j.state.Phase != Restoring {
		t.Fatal(j.state)
	}
	if !reflect.DeepEqual(e.calls, []string{"stop", "restore"}) {
		t.Fatal(e.calls)
	}
}
func TestInterruptedRecoveryRetriesRestoreBeforeVerify(t *testing.T) {
	c, j, e := setup()
	j.state.Phase = Restoring
	j.state.RestoreRequired = true
	j.state.RecoveryDigest = "sha256:" + hex64('d')
	if err := c.Recover(context.Background(), identity()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.calls, []string{"stop", "restore", "verify-predecessor", "expose-predecessor"}) {
		t.Fatal(e.calls)
	}
}
func TestCommitWriteFailureDoesNotExposeCandidate(t *testing.T) {
	c, j, e := setup()
	j.fail = Committed
	if c.Run(context.Background(), identity()) == nil {
		t.Fatal("accepted")
	}
	for _, v := range e.calls {
		if v == "expose" {
			t.Fatal(e.calls)
		}
	}
	if j.state.Phase != Validating {
		t.Fatal(j.state)
	}
}
func TestCommittedRetryOnlyExposesCandidate(t *testing.T) {
	c, j, e := setup()
	j.state.Phase = Committed
	j.state.RestoreRequired = true
	j.state.RecoveryDigest = "sha256:" + hex64('d')
	if err := c.Run(context.Background(), identity()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.calls, []string{"expose"}) {
		t.Fatal(e.calls)
	}
	if !errors.Is(c.Recover(context.Background(), identity()), ErrCommitted) {
		t.Fatal("rollback after exposure allowed")
	}
}
func TestIdentityMismatchHasNoEffects(t *testing.T) {
	c, _, e := setup()
	other := identity()
	other.Candidate = other.Predecessor
	if c.Run(context.Background(), other) == nil {
		t.Fatal("accepted")
	}
	if len(e.calls) != 0 {
		t.Fatal(e.calls)
	}
}
func TestEveryInterruptedForwardPhaseRequiresRecovery(t *testing.T) {
	for _, phase := range []Phase{Quiescing, Capturing, Verified, Migrating, Starting, Validating, Restoring, Reopening} {
		t.Run(string(phase), func(t *testing.T) {
			c, j, e := setup()
			j.state.Phase = phase
			switch phase {
			case Migrating, Starting, Validating, Restoring, Reopening:
				j.state.RestoreRequired = true
			}
			j.state.RecoveryDigest = "sha256:" + hex64('d')
			if !errors.Is(c.Run(context.Background(), identity()), ErrRecoveryRequired) {
				t.Fatal("accepted")
			}
			if len(e.calls) != 0 {
				t.Fatal(e.calls)
			}
		})
	}
}
func TestCancellationNeverStartsMigration(t *testing.T) {
	c, _, e := setup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.Run(ctx, identity()) == nil {
		t.Fatal("accepted")
	}
	if len(e.calls) != 0 {
		t.Fatal(e.calls)
	}
}

func TestEveryForwardEffectFailureStopsBeforeExposure(t *testing.T) {
	for _, effect := range []string{"admit", "quiesce", "capture-restore-verify", "migrate", "candidate-isolated", "validate"} {
		t.Run(effect, func(t *testing.T) {
			c, _, e := setup()
			e.fail = effect
			if c.Run(context.Background(), identity()) == nil {
				t.Fatal("expected failure")
			}
			for _, call := range e.calls {
				if call == "expose" {
					t.Fatal("traffic exposed on failed upgrade")
				}
			}
		})
	}
}
func TestFailureBeforeMigrationRestartsVerifiedPredecessorWithoutRestore(t *testing.T) {
	for _, phase := range []Phase{Quiescing, Capturing, Verified} {
		t.Run(string(phase), func(t *testing.T) {
			c, j, e := setup()
			j.state.Phase = phase
			if phase == Verified {
				j.state.RecoveryDigest = "sha256:" + hex64('d')
			}
			if err := c.Recover(context.Background(), identity()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(e.calls, []string{"stop", "verify-predecessor", "expose-predecessor"}) {
				t.Fatal(e.calls)
			}
		})
	}
}
func TestRecoveryReopenRetryNeverRestoresAcknowledgedWrites(t *testing.T) {
	c, j, e := setup()
	j.state.Phase = Reopening
	j.state.RestoreRequired = true
	j.state.RecoveryDigest = "sha256:" + hex64('d')
	if err := c.Recover(context.Background(), identity()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.calls, []string{"expose-predecessor"}) {
		t.Fatal(e.calls)
	}
}
func TestPredecessorVerificationFailureLeavesTrafficFenced(t *testing.T) {
	c, j, e := setup()
	j.state.Phase = Migrating
	j.state.RestoreRequired = true
	j.state.RecoveryDigest = "sha256:" + hex64('d')
	e.fail = "verify-predecessor"
	if c.Recover(context.Background(), identity()) == nil {
		t.Fatal("expected failure")
	}
	if j.state.Phase != Restoring {
		t.Fatal(j.state)
	}
	if !reflect.DeepEqual(e.calls, []string{"stop", "restore", "verify-predecessor"}) {
		t.Fatal(e.calls)
	}
}
