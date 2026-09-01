package offline

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type fakeLock struct {
	released int
}

func (lock *fakeLock) Release() error {
	lock.released++
	return nil
}

type fakeLocker struct {
	acquired int
	lock     fakeLock
}

func (locker *fakeLocker) Acquire(context.Context) (Lock, error) {
	locker.acquired++
	return &locker.lock, nil
}

type fakeState struct {
	environment    string
	environmentErr error
	existing       bool
	initialized    bool
	bound          string
}

func (state *fakeState) Environment(context.Context) (string, error) {
	return state.environment, state.environmentErr
}

func (state *fakeState) ExistingEnvironment(context.Context) (string, bool, error) {
	return state.environment, state.existing, state.environmentErr
}

func (state *fakeState) BindEnvironment(_ context.Context, environment string) error {
	state.environment, state.bound, state.environmentErr = environment, environment, nil
	return nil
}

func (state *fakeState) Initialized(context.Context) (bool, error) {
	return state.initialized, nil
}

type memoryRecovery struct {
	contents    []byte
	removed     int
	removeErr   error
	removeErrAt int
}

func (recovery *memoryRecovery) Read() ([]byte, error) {
	if recovery.contents == nil {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), recovery.contents...), nil
}

func (recovery *memoryRecovery) Write(contents []byte) error {
	recovery.contents = append([]byte(nil), contents...)
	return nil
}

func (recovery *memoryRecovery) Remove() error {
	recovery.removed++
	if recovery.removeErr != nil && (recovery.removeErrAt == 0 || recovery.removed == recovery.removeErrAt) {
		return recovery.removeErr
	}
	if recovery.contents == nil {
		return os.ErrNotExist
	}
	recovery.contents = nil
	return nil
}

type fakeInitializer struct {
	calls  int
	input  InitializationInput
	result InitialCredentials
	err    error
}

func (initializer *fakeInitializer) Initialize(
	_ context.Context,
	input InitializationInput,
	prepare func(InitialCredentials) error,
) (InitialCredentials, error) {
	initializer.calls++
	initializer.input = input
	if err := prepare(initializer.result); err != nil {
		return InitialCredentials{}, err
	}
	return initializer.result, initializer.err
}

type fakeRetention struct {
	calls  int
	policy RetentionPolicy
	result RetentionResult
}

func (retention *fakeRetention) Prune(_ context.Context, policy RetentionPolicy) (RetentionResult, error) {
	retention.calls++
	retention.policy = policy
	return retention.result, nil
}

func TestInitializeOwnsValidationRecoveryAndAccessSequencing(t *testing.T) {
	now := time.Date(2026, 7, 29, 7, 0, 0, 0, time.UTC)
	locker := &fakeLocker{}
	state := &fakeState{environmentErr: ErrStateNotFound}
	recovery := &memoryRecovery{}
	initializer := &fakeInitializer{result: InitialCredentials{
		Email: "owner@example.com", TemporaryPassword: "temporary",
		PublisherToken: "publisher", PublisherTokenExpiresAt: now.Add(24 * time.Hour).Format(time.RFC3339),
	}}
	service := New(Config{
		HomeDir: "/instance", Production: true, BootstrapEmail: "owner@example.com", Environment: "prod",
	}, Dependencies{
		Locker: locker, State: state, Recovery: recovery, Initializer: initializer,
		Now: func() time.Time { return now },
	})
	var out bytes.Buffer
	if err := service.Initialize(context.Background(), InitializeRequest{Format: "json"}, &out); err != nil {
		t.Fatal(err)
	}
	if locker.acquired != 1 || locker.lock.released != 1 {
		t.Fatalf("lock lifecycle = acquired %d released %d", locker.acquired, locker.lock.released)
	}
	if state.bound != "prod" {
		t.Fatalf("bound environment = %q", state.bound)
	}
	if initializer.calls != 1 || initializer.input.Email != "owner@example.com" ||
		initializer.input.Environment != "prod" || !initializer.input.Now.Equal(now) {
		t.Fatalf("initialization input = %#v calls=%d", initializer.input, initializer.calls)
	}
	if !bytes.Equal(out.Bytes(), recovery.contents) {
		t.Fatalf("delivered credentials and recovery differ:\nout=%s\nrecovery=%s", out.Bytes(), recovery.contents)
	}
	if _, err := DecodeInitialCredentials(out.Bytes()); err != nil {
		t.Fatalf("decode initialized credentials: %v", err)
	}
}

func TestInitializeReplaysPreparedCredentialsWithoutMutatingAccess(t *testing.T) {
	contents := []byte(`{"email":"owner@example.com","temporaryPassword":"temporary","publisherToken":"publisher","publisherTokenExpiresAt":"2026-07-30T07:00:00Z"}` + "\n")
	locker := &fakeLocker{}
	initializer := &fakeInitializer{}
	service := New(Config{Production: true, BootstrapEmail: "owner@example.com"}, Dependencies{
		Locker:      locker,
		State:       &fakeState{environment: "prod", initialized: true},
		Recovery:    &memoryRecovery{contents: contents},
		Initializer: initializer,
	})
	var out bytes.Buffer
	if err := service.Initialize(context.Background(), InitializeRequest{Format: "json"}, &out); err != nil {
		t.Fatal(err)
	}
	if initializer.calls != 0 || !bytes.Equal(out.Bytes(), contents) {
		t.Fatalf("initializer calls=%d output=%q", initializer.calls, out.String())
	}
}

func TestInitializeReportsCredentialCleanupFailureAfterMutationFailure(t *testing.T) {
	mutationErr := errors.New("commit failed")
	cleanupErr := errors.New("remove denied")
	recovery := &memoryRecovery{removeErr: cleanupErr, removeErrAt: 2}
	service := New(Config{Production: true, BootstrapEmail: "owner@example.com", Environment: "prod"}, Dependencies{
		Locker:   &fakeLocker{},
		State:    &fakeState{environment: "prod", existing: true},
		Recovery: recovery,
		Initializer: &fakeInitializer{result: InitialCredentials{
			Email: "owner@example.com", TemporaryPassword: "temporary", PublisherToken: "publisher",
			PublisherTokenExpiresAt: "2026-07-30T07:00:00Z",
		}, err: mutationErr},
	})

	err := service.Initialize(context.Background(), InitializeRequest{Format: "json"}, io.Discard)
	if !errors.Is(err, mutationErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("initialization cleanup error = %v, want mutation and cleanup errors", err)
	}
	if recovery.removed != 2 || len(recovery.contents) == 0 {
		t.Fatalf("failed cleanup evidence removed=%d contents=%q", recovery.removed, recovery.contents)
	}
}

func TestMaintenanceOwnsPolicyAndExclusiveApplySemantics(t *testing.T) {
	locker := &fakeLocker{}
	retention := &fakeRetention{result: RetentionResult{
		AuditEventsDeleted: 1, DeliveredAuditIntentsDeleted: 8, QueryEventsDeleted: 2, ArchivedAgentConversationsDeleted: 3,
		ExpiredOAuthStatesDeleted: 4, StaleSessionsDeleted: 5,
		StaleAPITokensDeleted: 6, StaleServicePrincipalSecretsDeleted: 7,
	}}
	service := New(Config{}, Dependencies{Locker: locker, Retention: retention})
	var out bytes.Buffer
	request := MaintenanceRequest{AuditDays: 10, QueryDays: 11, ArchivedAgentDays: 12, AuthStateDays: 13}
	if err := service.Maintenance(context.Background(), request, &out); err != nil {
		t.Fatal(err)
	}
	if locker.acquired != 0 || !retention.policy.DryRun ||
		retention.policy.AuditEventsMaxAge != 10*24*time.Hour ||
		retention.policy.AuthStateMaxAge != 13*24*time.Hour {
		t.Fatalf("dry-run policy = %#v lock=%d", retention.policy, locker.acquired)
	}
	if !strings.Contains(out.String(), "mode: dry-run") || !strings.Contains(out.String(), "delivered audit intents: 8") || !strings.Contains(out.String(), "stale service principal secrets: 7") {
		t.Fatalf("maintenance output = %q", out.String())
	}
	request.Apply = true
	if err := service.Maintenance(context.Background(), request, io.Discard); err != nil {
		t.Fatal(err)
	}
	if locker.acquired != 1 || locker.lock.released != 1 || retention.policy.DryRun {
		t.Fatalf("apply policy = %#v lock=%d/%d", retention.policy, locker.acquired, locker.lock.released)
	}
}

func TestMaintenanceRejectsNegativeRetentionBeforeDependencies(t *testing.T) {
	service := New(Config{}, Dependencies{})
	err := service.Maintenance(context.Background(), MaintenanceRequest{AuditDays: -1}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "zero or greater") {
		t.Fatalf("maintenance error = %v", err)
	}
}

func TestResolveEnvironmentPropagatesUnexpectedStateFailures(t *testing.T) {
	service := New(Config{}, Dependencies{State: &fakeState{environmentErr: errors.New("broken")}})
	if _, err := service.resolveEnvironment(context.Background()); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("resolve environment error = %v", err)
	}
	service = New(Config{}, Dependencies{State: &fakeState{environmentErr: sql.ErrNoRows}})
	if _, err := service.resolveEnvironment(context.Background()); !strings.Contains(err.Error(), sql.ErrNoRows.Error()) {
		t.Fatalf("raw sql sentinel must not cross the port: %v", err)
	}
}
