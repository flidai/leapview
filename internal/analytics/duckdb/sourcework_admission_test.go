package duckdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	"github.com/flidai/leapview/internal/platform/transaction"
)

func TestSourceRuntimePrepareWaitsAtGateBeforeProviderAndResolver(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate sourcework.Gate
		pause, err := gate.Pause()
		if err != nil {
			t.Fatal(err)
		}
		provider := &sourceWorkTestProvider{session: &sourceWorkTestSession{}}
		var resolverCalls atomic.Int32
		runtime := NewSourceRuntimeWithCredentials(provider, sourceWorkCredentialResolverFunc(func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
			resolverCalls.Add(1)
			return nil, nil
		}))
		runtime.sourceWork = &gate

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		prepareDone := make(chan error, 1)
		go func() {
			_, err := runtime.Prepare(ctx, sourceWorkCredentialModel())
			prepareDone <- err
		}()
		synctest.Wait()

		if provider.calls.Load() != 0 || resolverCalls.Load() != 0 {
			t.Fatalf("paused Prepare reached provider/resolver: provider calls=%d resolver calls=%d", provider.calls.Load(), resolverCalls.Load())
		}
		select {
		case err := <-prepareDone:
			t.Fatalf("Prepare returned while admission was paused: %v", err)
		default:
		}

		cancel()
		synctest.Wait()
		if err := <-prepareDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled paused Prepare error = %v, want context.Canceled", err)
		}
		if provider.calls.Load() != 0 || resolverCalls.Load() != 0 {
			t.Fatalf("canceled paused Prepare reached provider/resolver: provider calls=%d resolver calls=%d", provider.calls.Load(), resolverCalls.Load())
		}
		if err := pause.Resume(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSourceRuntimePrepareLeaseIncludesSynchronousSessionCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate sourcework.Gate
		resolveStarted := make(chan struct{})
		allowResolveReturn := make(chan struct{})
		closeStarted := make(chan struct{})
		allowSessionClose := make(chan struct{})
		var resolveReleaseOnce, closeReleaseOnce sync.Once
		defer resolveReleaseOnce.Do(func() { close(allowResolveReturn) })
		defer closeReleaseOnce.Do(func() { close(allowSessionClose) })
		session := &sourceWorkBlockingCloseSession{closeStarted: closeStarted, allowClose: allowSessionClose}
		provider := &sourceWorkTestProvider{session: session}
		resolverErr := errors.New("credential resolver stopped")
		runtime := NewSourceRuntimeWithCredentials(provider, sourceWorkCredentialResolverFunc(func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
			close(resolveStarted)
			<-allowResolveReturn
			return nil, resolverErr
		}))
		runtime.sourceWork = &gate
		model := sourceWorkCredentialModel()
		model.Sources = map[string]semanticmodel.Source{
			"accounts": {Connection: "crm", Object: "accounts"},
		}

		prepareDone := make(chan error, 1)
		go func() {
			_, err := runtime.Prepare(context.Background(), model)
			prepareDone <- err
		}()
		synctest.Wait()
		<-resolveStarted
		pause, err := gate.Pause()
		if err != nil {
			t.Fatal(err)
		}
		drainDone := make(chan error, 1)
		go func() { drainDone <- pause.WaitDrained(context.Background()) }()
		synctest.Wait()
		if err, finished := sourceWorkResult(drainDone); finished {
			t.Fatalf("drain finished while credential resolution was active: %v", err)
		}

		resolveReleaseOnce.Do(func() { close(allowResolveReturn) })
		synctest.Wait()
		<-closeStarted
		if err, finished := sourceWorkResult(drainDone); finished {
			t.Fatalf("drain finished while session cleanup was active: %v", err)
		}

		closeReleaseOnce.Do(func() { close(allowSessionClose) })
		synctest.Wait()
		if err := <-prepareDone; !errors.Is(err, resolverErr) {
			t.Fatalf("Prepare error = %v, want resolver error", err)
		}
		if err := <-drainDone; err != nil {
			t.Fatalf("WaitDrained after session cleanup: %v", err)
		}
		if err := pause.Resume(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSourceRuntimePreparedLocalSourceDoesNotRetainAdmissionLease(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "orders.csv")
	if err := os.WriteFile(path, []byte("order_id,revenue\n1,10.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var gate sourcework.Gate
	provider := &sourceWorkDBSessionProvider{db: db}
	runtime := NewSourceRuntime(provider)
	runtime.sourceWork = &gate
	model := &semanticmodel.Model{
		DefaultConnection: "local",
		Connections:       map[string]semanticmodel.Connection{"local": {Kind: "managed", Root: directory}},
		Sources: map[string]semanticmodel.Source{
			"orders": {
				Connection:            "local",
				Path:                  "orders.csv",
				Format:                "csv",
				EffectivePathLocation: testCSVPathLocationWithHeader("orders.csv", true),
			},
		},
	}
	prepared, err := runtime.Prepare(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = prepared.Close()
		if provider.session != nil {
			_ = provider.session.Close()
		}
	}()

	pause, err := gate.Pause()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pause.WaitDrained(ctx); err != nil {
		t.Fatalf("prepared source kept admission active after Prepare returned: %v", err)
	}
	if err := pause.Resume(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenProjectMaterializeRuntimeForwardsSourceWorkGate(t *testing.T) {
	var gate sourcework.Gate
	runtime, err := OpenProjectMaterializeRuntime(context.Background(), ProjectRuntimeConfig{
		ProjectID: "test",
		Models: map[string]*semanticmodel.Model{"test": {
			Tables:   map[string]semanticmodel.Table{"orders": {ModelName: "orders"}},
			Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		}},
		Database:            sourceWorkTestProjectDatabase{},
		SourceWork:          &gate,
		SkipInitialRefresh:  true,
		MaterializationOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.sources.sourceWork != &gate {
		t.Fatal("project runtime did not forward its source work gate")
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func sourceWorkCredentialModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		DefaultConnection: "crm",
		Connections:       map[string]semanticmodel.Connection{"crm": {Kind: "postgres"}},
	}
}

type sourceWorkCredentialResolverFunc func(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error)

func (f sourceWorkCredentialResolverFunc) Resolve(ctx context.Context, name string, connection semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
	return f(ctx, name, connection)
}

type sourceWorkTestProvider struct {
	session analyticsresource.Session
	calls   atomic.Int32
}

func (p *sourceWorkTestProvider) Session(context.Context) (analyticsresource.Session, error) {
	p.calls.Add(1)
	return p.session, nil
}

type sourceWorkTestSession struct{}

func (*sourceWorkTestSession) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}

func (*sourceWorkTestSession) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, fmt.Errorf("unexpected QueryContext")
}

func (*sourceWorkTestSession) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

type sourceWorkBlockingCloseSession struct {
	sourceWorkTestSession
	closeStarted chan struct{}
	allowClose   <-chan struct{}
}

func (s *sourceWorkBlockingCloseSession) Close() error {
	close(s.closeStarted)
	<-s.allowClose
	return nil
}

func sourceWorkResult(done <-chan error) (error, bool) {
	select {
	case err := <-done:
		return err, true
	default:
		return nil, false
	}
}

type sourceWorkDBSessionProvider struct {
	db      *sql.DB
	session *sql.Conn
}

func (p *sourceWorkDBSessionProvider) Session(ctx context.Context) (analyticsresource.Session, error) {
	session, err := p.db.Conn(ctx)
	p.session = session
	return session, err
}

type sourceWorkTestProjectDatabase struct{}

func (sourceWorkTestProjectDatabase) Exec(context.Context, string) error { return nil }
func (sourceWorkTestProjectDatabase) Close() error                       { return nil }
func (sourceWorkTestProjectDatabase) Path() string                       { return "" }
func (sourceWorkTestProjectDatabase) Session(context.Context) (analyticsresource.Session, error) {
	return &sourceWorkTestSession{}, nil
}
func (sourceWorkTestProjectDatabase) ValidateSnapshot(context.Context, int64) error { return nil }
func (sourceWorkTestProjectDatabase) CommitTransaction(context.Context, string, map[string]string, func(transaction.Transaction) error) (int64, error) {
	return 0, nil
}
