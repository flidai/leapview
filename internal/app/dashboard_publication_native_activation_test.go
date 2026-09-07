package app

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	publicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// nativeActivationDBStub is only used to construct native repositories for
// composition tests. The stale-generation test must return before any query
// or transaction method is reached.
type nativeActivationDBStub struct{}

func (nativeActivationDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("native activation test database should not execute")
}
func (nativeActivationDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("native activation test database should not query")
}
func (nativeActivationDBStub) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("native activation test database should not query")
}
func (nativeActivationDBStub) Begin(context.Context) (pgx.Tx, error) {
	panic("stale native activation should not begin a transaction")
}

type nativeActivationStateStub struct {
	state   servingstate.State
	current servingstate.State
}

func (s nativeActivationStateStub) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return s.state, nil
}
func (s nativeActivationStateStub) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return s.current, servingstate.Artifact{}, nil
}

type nativeActivationBeginStub struct{}

func (nativeActivationBeginStub) Begin(context.Context) (pgx.Tx, error) {
	panic("stale native activation should not begin a transaction")
}

type nativeActivationFenceStub struct{}

func (nativeActivationFenceStub) ValidateActiveGeneration(context.Context, pgx.Tx, projectgraph.ServingIdentity) error {
	return nil
}

type nativeActivationTxStub struct {
	pgx.Tx
	rollbackCount int
	commitCount   int
}

func (tx *nativeActivationTxStub) Rollback(context.Context) error {
	tx.rollbackCount++
	return nil
}
func (tx *nativeActivationTxStub) Commit(context.Context) error {
	tx.commitCount++
	return nil
}

type nativeActivationTrackedBegin struct{ tx *nativeActivationTxStub }

func (b nativeActivationTrackedBegin) Begin(context.Context) (pgx.Tx, error) { return b.tx, nil }

type nativeActivationFailingFence struct{ err error }

func (f nativeActivationFailingFence) ValidateActiveGeneration(context.Context, pgx.Tx, projectgraph.ServingIdentity) error {
	return f.err
}

type nativeActivationCaptureFence struct {
	tx  pgx.Tx
	err error
}

func (f *nativeActivationCaptureFence) ValidateActiveGeneration(_ context.Context, tx pgx.Tx, _ projectgraph.ServingIdentity) error {
	f.tx = tx
	return f.err
}

func TestNativeDashboardPublicationReconcilerRequiresNativeAuthorities(t *testing.T) {
	db := nativeActivationDBStub{}
	publicationRepo, err := publicationpostgres.New(db, nativePublicationAuditStubForApp{}, nativePublicationEventStubForApp{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewNativeDashboardPublicationReconciler(NativeDashboardPublicationActivationConfig{
		Begin: nativeActivationBeginStub{}, Publications: publicationRepo,
		Project: projectpostgres.New(db), Access: &accessmodule.Module{}, GenerationFence: nativeActivationFenceStub{},
	}); err != nil {
		t.Fatalf("native authority bundle rejected: %v", err)
	}
	if _, err := NewNativeDashboardPublicationReconciler(NativeDashboardPublicationActivationConfig{
		Begin: nativeActivationBeginStub{}, Publications: publicationpostgres.NewRepository(db),
		Project: projectpostgres.New(db), Access: &accessmodule.Module{}, GenerationFence: nativeActivationFenceStub{},
	}); err == nil {
		t.Fatal("read-only publication repository was accepted as native activation authority")
	}
}

func TestNativeDashboardPublicationReconcilerSkipsStaleGenerationBeforeBegin(t *testing.T) {
	db := nativeActivationDBStub{}
	publicationRepo, err := publicationpostgres.New(db, nativePublicationAuditStubForApp{}, nativePublicationEventStubForApp{})
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewNativeDashboardPublicationReconciler(NativeDashboardPublicationActivationConfig{
		Begin: nativeActivationBeginStub{}, Publications: publicationRepo,
		Project: projectpostgres.New(db), Access: &accessmodule.Module{}, GenerationFence: nativeActivationFenceStub{},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := servingstate.State{ID: "generation-old", ProjectID: projectgraph.ResourceID("project:activation"), Environment: "dev"}
	current := state
	current.ID = "generation-new"
	activated := deployment.Deployment{ServingIdentity: projectgraph.ServingIdentity{ProjectID: state.ProjectID, Environment: "dev", GenerationID: string(state.ID)}, ActivationPrincipal: "principal:publisher"}
	if err := reconciler.Reconcile(t.Context(), nativeActivationStateStub{state: state, current: current}, activated); err != nil {
		t.Fatalf("stale activation returned error: %v", err)
	}
}

func TestNativeDashboardPublicationReconcilerRollsBackWhenGenerationChangesAfterPrecheck(t *testing.T) {
	db := nativeActivationDBStub{}
	publicationRepo, err := publicationpostgres.New(db, nativePublicationAuditStubForApp{}, nativePublicationEventStubForApp{})
	if err != nil {
		t.Fatal(err)
	}
	tx := &nativeActivationTxStub{}
	generationChanged := errors.New("generation changed")
	reconciler, err := NewNativeDashboardPublicationReconciler(NativeDashboardPublicationActivationConfig{
		Begin: nativeActivationTrackedBegin{tx: tx}, Publications: publicationRepo,
		Project: projectpostgres.New(db), Access: &accessmodule.Module{},
		GenerationFence: nativeActivationFailingFence{err: generationChanged},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := servingstate.State{ID: "generation-raced", ProjectID: projectgraph.ResourceID("project:activation"), Environment: "dev", DashboardPublicationsJSON: `{}`}
	activated := deployment.Deployment{ServingIdentity: projectgraph.ServingIdentity{ProjectID: state.ProjectID, Environment: "dev", GenerationID: string(state.ID)}, ActivationPrincipal: "principal:publisher"}
	if err := reconciler.Reconcile(t.Context(), nativeActivationStateStub{state: state, current: state}, activated); !errors.Is(err, generationChanged) {
		t.Fatalf("generation fence error = %v, want %v", err, generationChanged)
	}
	if tx.rollbackCount != 1 || tx.commitCount != 0 {
		t.Fatalf("transaction boundaries rollback=%d commit=%d, want rollback=1 commit=0", tx.rollbackCount, tx.commitCount)
	}
}

func TestNativeDashboardPublicationReconcilerUsesCallerTransactionExactly(t *testing.T) {
	db := nativeActivationDBStub{}
	publicationRepo, err := publicationpostgres.New(db, nativePublicationAuditStubForApp{}, nativePublicationEventStubForApp{})
	if err != nil {
		t.Fatal(err)
	}
	tx := &nativeActivationTxStub{}
	fenceErr := errors.New("fence stops before writes")
	fence := &nativeActivationCaptureFence{err: fenceErr}
	reconciler, err := NewNativeDashboardPublicationReconciler(NativeDashboardPublicationActivationConfig{
		Begin: nativeActivationBeginStub{}, Publications: publicationRepo,
		Project: projectpostgres.New(db), Access: &accessmodule.Module{}, GenerationFence: fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := servingstate.State{ID: "generation-exact", ProjectID: projectgraph.ResourceID("project:activation"), Environment: "dev", DashboardPublicationsJSON: `{}`}
	activated := deployment.Deployment{ServingIdentity: projectgraph.ServingIdentity{ProjectID: state.ProjectID, Environment: "dev", GenerationID: string(state.ID)}, ActivationPrincipal: "principal:publisher"}
	if err := reconciler.ReconcileTx(t.Context(), tx, nativeActivationStateStub{state: state, current: state}, activated); !errors.Is(err, fenceErr) {
		t.Fatalf("caller transaction error = %v, want %v", err, fenceErr)
	}
	if fence.tx != tx {
		t.Fatal("generation fence did not receive the exact caller-owned transaction")
	}
	if tx.rollbackCount != 0 || tx.commitCount != 0 {
		t.Fatalf("ReconcileTx changed caller transaction boundaries rollback=%d commit=%d", tx.rollbackCount, tx.commitCount)
	}
}

// The native repository constructor deliberately requires these ports. The
// stubs keep this composition test independent from PostgreSQL while the
// publication repository's integration tests cover SQL/event/audit behavior.
type nativePublicationAuditStubForApp struct{}

func (nativePublicationAuditStubForApp) RecordAuditIntent(context.Context, publicationpostgres.Tx, access.AuditIntent) error {
	return nil
}

type nativePublicationEventStubForApp struct{}

func (nativePublicationEventStubForApp) AppendEvent(_ context.Context, _ publicationpostgres.Tx, input publicationpostgres.EventInput) (publicationpostgres.Event, error) {
	return publicationpostgres.Event{EventID: input.EventID, ProjectID: input.ProjectID, PublicationID: input.PublicationID, ActorID: input.ActorID, CorrelationID: input.CorrelationID, Revision: input.Revision, AggregateVersion: input.Revision, Type: input.Type, ServingStateID: input.ServingStateID, Payload: input.Payload}, nil
}
