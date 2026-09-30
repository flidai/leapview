package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/analytics/sourcework"
)

func TestSourceRuntimeRevalidationAfterAdmissionWaitDeniesBeforeSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate sourcework.Gate
		pause, err := gate.Pause()
		if err != nil {
			t.Fatal(err)
		}
		provider := &sourceWorkTestProvider{session: &sourceWorkTestSession{}}
		var resolverCalls, checkCalls atomic.Int32
		denied := errors.New("sensitive authority detail")
		runtime := NewSourceRuntimeWithCredentials(provider, sourceWorkCredentialResolverFunc(func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
			resolverCalls.Add(1)
			return nil, nil
		}))
		runtime.sourceWork = &gate
		ctx := sourcework.WithRevalidator(context.Background(), func(context.Context) error {
			checkCalls.Add(1)
			return denied
		})
		done := make(chan error, 1)
		go func() {
			_, err := runtime.Prepare(ctx, sourceWorkCredentialModel())
			done <- err
		}()
		synctest.Wait()
		if provider.calls.Load() != 0 || resolverCalls.Load() != 0 || checkCalls.Load() != 0 {
			t.Fatalf("paused Prepare escaped admission: provider=%d resolver=%d revalidation=%d", provider.calls.Load(), resolverCalls.Load(), checkCalls.Load())
		}
		if err := pause.Resume(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		prepareErr := <-done
		if !errors.Is(prepareErr, denied) {
			t.Fatalf("Prepare denial = %v, want wrapped authority denial", prepareErr)
		}
		if strings.Contains(prepareErr.Error(), "sensitive authority detail") {
			t.Fatalf("Prepare leaked denial diagnostics: %v", prepareErr)
		}
		if provider.calls.Load() != 0 || resolverCalls.Load() != 0 || checkCalls.Load() != 1 {
			t.Fatalf("denied Prepare reached provider/resolver or skipped revalidation: provider=%d resolver=%d revalidation=%d", provider.calls.Load(), resolverCalls.Load(), checkCalls.Load())
		}
		drained, err := gate.Pause()
		if err != nil {
			t.Fatal(err)
		}
		if err := drained.WaitDrained(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := drained.Resume(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSourceRuntimeRevalidatesAfterSessionBeforeConnectionResolution(t *testing.T) {
	fixture := newSourceWorkCleanupFixture(t, sourceWorkCleanupFailures{})
	var revoked atomic.Bool
	var resolverCalls, checks atomic.Int32
	fixture.runtime.db = sourceWorkSessionHookProvider{
		SessionProvider: fixture.runtime.db,
		afterSession:    func() { revoked.Store(true) },
	}
	fixture.runtime.resolver = sourceWorkCredentialResolverFunc(func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
		resolverCalls.Add(1)
		return semanticmodel.ConnectionAuth{"password": "credential-value"}, nil
	})
	denied := errors.New("grant revoked while waiting for the source session")
	ctx := sourcework.WithRevalidator(context.Background(), func(context.Context) error {
		checks.Add(1)
		if revoked.Load() {
			return denied
		}
		return nil
	})

	prepared, err := fixture.runtime.Prepare(ctx, sourceWorkCleanupModel())
	if prepared != nil {
		_ = prepared.Close()
		t.Fatal("session-boundary denial returned prepared sources")
	}
	if !errors.Is(err, denied) || strings.Contains(err.Error(), "grant revoked while waiting") {
		t.Fatalf("post-session denial = %v, want redacted wrapped denial", err)
	}
	if checks.Load() != 2 || resolverCalls.Load() != 0 {
		t.Fatalf("checks/resolutions = %d/%d, want two checks and no connection resolution", checks.Load(), resolverCalls.Load())
	}
	for _, prefix := range []string{"CREATE OR REPLACE TEMPORARY SECRET ", "ATTACH ", "CREATE TEMP TABLE "} {
		for _, statement := range fixture.session.statements {
			if strings.HasPrefix(statement, prefix) {
				t.Fatalf("post-session denial executed %q before connection resolution", statement)
			}
		}
	}
	requireSourceWorkDrains(t, fixture.gate)
}

type sourceWorkSessionHookProvider struct {
	analyticsresource.SessionProvider
	afterSession func()
}

func (p sourceWorkSessionHookProvider) Session(ctx context.Context) (analyticsresource.Session, error) {
	session, err := p.SessionProvider.Session(ctx)
	if err == nil && p.afterSession != nil {
		p.afterSession()
	}
	return session, err
}

func TestSourceRuntimeRevalidatesEveryExternalSource(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "first.csv")
	if err := os.WriteFile(firstPath, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	provider := &sourceWorkDBSessionProvider{db: db}
	var gate sourcework.Gate
	var resolverCalls, checkCalls atomic.Int32
	runtime := NewSourceRuntimeWithCredentials(provider, sourceWorkCredentialResolverFunc(func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
		resolverCalls.Add(1)
		return nil, nil
	}))
	runtime.sourceWork = &gate
	denied := errors.New("secret source grant diagnostics")
	ctx := sourcework.WithRevalidator(context.Background(), func(context.Context) error {
		if checkCalls.Add(1) == 5 {
			return denied
		}
		return nil
	})
	missingSecondPath := filepath.Join(directory, "must-not-be-read.csv")
	model := &semanticmodel.Model{
		DefaultConnection: "warehouse",
		Connections:       map[string]semanticmodel.Connection{"warehouse": {Kind: "source-test"}},
		Sources: map[string]semanticmodel.Source{
			"first":  {Connection: "warehouse", Path: firstPath, Format: "csv", EffectivePathLocation: testCSVPathLocationWithHeader(firstPath, true)},
			"second": {Connection: "warehouse", Path: missingSecondPath, Format: "csv", EffectivePathLocation: testCSVPathLocationWithHeader(missingSecondPath, true)},
		},
	}
	prepared, err := runtime.Prepare(ctx, model)
	if prepared != nil {
		_ = prepared.Close()
		t.Fatal("denied second source returned prepared sources")
	}
	if !errors.Is(err, denied) || strings.Contains(fmt.Sprint(err), "secret source grant diagnostics") {
		t.Fatalf("second-source denial = %v, want redacted wrapped denial", err)
	}
	if got := checkCalls.Load(); got != 5 {
		t.Fatalf("authority checks = %d, want admission, pre-resolution and pre-access checks per source", got)
	}
	if resolverCalls.Load() != 2 || provider.session == nil {
		t.Fatalf("second-source denial should follow per-source resolution and session acquisition: resolver=%d session=%v", resolverCalls.Load(), provider.session != nil)
	}
	if err := provider.session.PingContext(t.Context()); err == nil {
		t.Fatal("source session remained open after denied second source")
	}
	requireSourceWorkDrains(t, &gate)
}

func TestSourceRuntimeRevalidationQuarantinesUncertainSessionClose(t *testing.T) {
	closeDiagnostic := "private session close diagnostic"
	fixture := newSourceWorkCleanupFixture(t, sourceWorkCleanupFailures{closeErr: errors.New(closeDiagnostic)})
	denied := errors.New("private grant diagnostic")
	var checks atomic.Int32
	ctx := sourcework.WithRevalidator(context.Background(), func(context.Context) error {
		if checks.Add(1) == 2 {
			return denied
		}
		return nil
	})
	_, err := fixture.runtime.Prepare(ctx, sourceWorkCleanupModel())
	if !errors.Is(err, denied) || !errors.Is(err, errSourceCleanupFailed) {
		t.Fatalf("source denial with uncertain close = %v, want denial and cleanup failure", err)
	}
	if strings.Contains(err.Error(), closeDiagnostic) || strings.Contains(err.Error(), "private grant diagnostic") {
		t.Fatalf("source denial leaked private diagnostics: %v", err)
	}
	if checks.Load() != 2 {
		t.Fatalf("authority checks = %d, want session and source boundary checks", checks.Load())
	}
	for _, prefix := range []string{"CREATE OR REPLACE TEMPORARY SECRET ", "ATTACH ", "CREATE TEMP TABLE "} {
		for _, statement := range fixture.session.statements {
			if strings.HasPrefix(statement, prefix) {
				t.Fatalf("denied source boundary executed %q before source access", statement)
			}
		}
	}
	requireSourceWorkRemainsActive(t, fixture.gate)
}

func TestSourceRuntimeRevalidatesAfterSecretScopeWait(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	session := &sourceWorkCleanupSession{conn: conn, failures: sourceWorkCleanupFailures{}}
	contention := make(chan string, 1)
	provider := &sourceAuthorityTelemetryProvider{
		sourceWorkTestProvider: &sourceWorkTestProvider{session: session},
		contention:             contention,
	}
	var gate sourcework.Gate
	runtime := NewSourceRuntimeWithCredentials(provider, sourceWorkCredentialResolverFunc(func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
		return semanticmodel.ConnectionAuth{"password": "credential-value"}, nil
	}))
	runtime.sourceWork = &gate
	runtime.extensionAdmission = sourceWorkCleanupAdmission{}

	model := sourceWorkCleanupModel()
	logicalConnection := model.Connections["crm"]
	releaseHeld := lockSourceScope(logicalConnection, "crm", nil)
	held := true
	defer func() {
		if held {
			releaseHeld()
		}
	}()
	denied := errors.New("revoked grant diagnostics")
	var checks atomic.Int32
	var revoked atomic.Bool
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	prepareDone := make(chan error, 1)
	go func() {
		_, err := runtime.Prepare(sourcework.WithRevalidator(ctx, func(context.Context) error {
			checks.Add(1)
			if revoked.Load() {
				return denied
			}
			return nil
		}), model)
		prepareDone <- err
	}()
	select {
	case connector := <-contention:
		if connector != "postgres" {
			t.Fatalf("contended connector = %q, want postgres", connector)
		}
	case <-ctx.Done():
		t.Fatalf("source preparation did not wait on the held secret scope: %v", ctx.Err())
	}
	revoked.Store(true)
	releaseHeld()
	held = false
	select {
	case err := <-prepareDone:
		if !errors.Is(err, denied) || strings.Contains(err.Error(), "revoked grant diagnostics") {
			t.Fatalf("post-wait source denial = %v, want redacted wrapped denial", err)
		}
	case <-ctx.Done():
		t.Fatalf("source preparation did not finish after releasing scope: %v", ctx.Err())
	}
	if checks.Load() != 3 {
		t.Fatalf("authority checks = %d, want pre-session, pre-resolution and post-scope checks", checks.Load())
	}
	for _, prefix := range []string{"CREATE OR REPLACE TEMPORARY SECRET ", "ATTACH ", "CREATE TEMP TABLE "} {
		for _, statement := range session.statements {
			if strings.HasPrefix(statement, prefix) {
				t.Fatalf("post-wait denial executed %q before source access", statement)
			}
		}
	}
	requireSourceWorkDrains(t, &gate)
}

type sourceAuthorityTelemetryProvider struct {
	*sourceWorkTestProvider
	contention chan<- string
}

func (p *sourceAuthorityTelemetryProvider) ObserveSecretScopeContention(connector string) {
	p.contention <- connector
}

func (*sourceAuthorityTelemetryProvider) ObserveSourceAcquisition(string, string) {}
func (*sourceAuthorityTelemetryProvider) ObserveRefreshCleanup(bool)              {}

var _ refreshTelemetry = (*sourceAuthorityTelemetryProvider)(nil)
