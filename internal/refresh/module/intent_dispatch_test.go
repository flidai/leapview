package module

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/refresh/artifact"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/servingstate"
)

const intentTestDigest = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type manualIntentMemoryStore struct {
	rows []refreshpostgres.ManualIntent
}

func (s *manualIntentMemoryStore) InTx(ctx context.Context, fn func(refreshpostgres.Tx) error) error {
	return fn(nil)
}

func (s *manualIntentMemoryStore) CreateManualIntentWithAudit(ctx context.Context, input refreshpostgres.ManualIntentInput, audit func(context.Context, refreshpostgres.Tx, refreshpostgres.ManualIntent) error) (refreshpostgres.ManualIntent, bool, error) {
	for _, row := range s.rows {
		if row.ProjectID == input.ProjectID && row.Environment == input.Environment && row.PrincipalID == input.PrincipalID && row.IdempotencyKey == input.IdempotencyKey {
			if row.RequestDigest != input.RequestDigest {
				return refreshpostgres.ManualIntent{}, false, refreshpostgres.ErrConflict
			}
			return row, false, nil
		}
	}
	if input.IntentID == "" {
		id, err := refreshpostgres.NewUUIDv7()
		if err != nil {
			return refreshpostgres.ManualIntent{}, false, err
		}
		input.IntentID = id
	}
	if input.ReservedRunID == "" {
		id, err := refreshpostgres.NewUUIDv7()
		if err != nil {
			return refreshpostgres.ManualIntent{}, false, err
		}
		input.ReservedRunID = id
	}
	row := refreshpostgres.ManualIntent{ManualIntentInput: input, Status: refreshpostgres.ManualIntentWaiting, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if audit != nil {
		if err := audit(ctx, nil, row); err != nil {
			return refreshpostgres.ManualIntent{}, false, err
		}
	}
	s.rows = append(s.rows, row)
	return row, true, nil
}

func (s *manualIntentMemoryStore) ClaimNextManualIntent(_ context.Context, scope refreshpostgres.Scope, owner string, lease time.Duration) (refreshpostgres.ManualIntent, bool, error) {
	for i := range s.rows {
		row := &s.rows[i]
		if row.ProjectID != scope.ProjectID || row.Environment != scope.Environment || row.Status != refreshpostgres.ManualIntentWaiting {
			continue
		}
		row.Status = refreshpostgres.ManualIntentClaimed
		row.LeaseOwner = owner
		row.FenceGeneration++
		row.LeaseExpiresAt = time.Now().Add(lease)
		return *row, true, nil
	}
	return refreshpostgres.ManualIntent{}, false, nil
}

func (s *manualIntentMemoryStore) AttachManualIntentTx(_ context.Context, _ refreshpostgres.Tx, id, owner string, fence int64, runID string) error {
	row := s.row(id)
	if row == nil || row.Status != refreshpostgres.ManualIntentClaimed || row.LeaseOwner != owner || row.FenceGeneration != fence || row.ReservedRunID != runID {
		return refreshpostgres.ErrStaleFence
	}
	row.Status = refreshpostgres.ManualIntentAttached
	row.AttachedRunID = runID
	row.LeaseOwner = ""
	return nil
}

func (s *manualIntentMemoryStore) ReleaseManualIntentTx(_ context.Context, _ refreshpostgres.Tx, id, owner string, fence int64) error {
	return s.transition(id, owner, fence, refreshpostgres.ManualIntentWaiting)
}

func (s *manualIntentMemoryStore) MarkManualIntentStaleTx(_ context.Context, _ refreshpostgres.Tx, id, owner string, fence int64) error {
	return s.transition(id, owner, fence, refreshpostgres.ManualIntentStale)
}

func (s *manualIntentMemoryStore) transition(id, owner string, fence int64, status string) error {
	row := s.row(id)
	if row == nil || row.Status != refreshpostgres.ManualIntentClaimed || row.LeaseOwner != owner || row.FenceGeneration != fence {
		return refreshpostgres.ErrStaleFence
	}
	row.Status = status
	row.LeaseOwner = ""
	return nil
}

func (s *manualIntentMemoryStore) GetManualIntent(_ context.Context, scope refreshpostgres.Scope, id string) (refreshpostgres.ManualIntent, error) {
	for _, row := range s.rows {
		if row.ProjectID == scope.ProjectID && row.Environment == scope.Environment && row.IntentID == id {
			return row, nil
		}
	}
	return refreshpostgres.ManualIntent{}, refreshpostgres.ErrNotFound
}

func (s *manualIntentMemoryStore) GetManualIntentByIdempotency(_ context.Context, scope refreshpostgres.Scope, principalID, key string) (refreshpostgres.ManualIntent, error) {
	for _, row := range s.rows {
		if row.ProjectID == scope.ProjectID && row.Environment == scope.Environment && row.PrincipalID == principalID && row.IdempotencyKey == key {
			return row, nil
		}
	}
	return refreshpostgres.ManualIntent{}, refreshpostgres.ErrNotFound
}

func (s *manualIntentMemoryStore) ListManualIntents(_ context.Context, scope refreshpostgres.Scope, targetID string, limit int) ([]refreshpostgres.ManualIntent, error) {
	var out []refreshpostgres.ManualIntent
	for _, row := range s.rows {
		if row.ProjectID == scope.ProjectID && row.Environment == scope.Environment && (targetID == "" || row.TargetID == targetID) && (row.Status == refreshpostgres.ManualIntentWaiting || row.Status == refreshpostgres.ManualIntentClaimed) {
			out = append(out, row)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (s *manualIntentMemoryStore) ListRecentStaleManualIntents(_ context.Context, scope refreshpostgres.Scope, targetID string, limit int) ([]refreshpostgres.ManualIntent, error) {
	var out []refreshpostgres.ManualIntent
	for i := len(s.rows) - 1; i >= 0; i-- {
		row := s.rows[i]
		if row.ProjectID == scope.ProjectID && row.Environment == scope.Environment && row.TargetID == targetID && row.Status == refreshpostgres.ManualIntentStale {
			out = append(out, row)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (s *manualIntentMemoryStore) CancelManualIntentWithAudit(ctx context.Context, scope refreshpostgres.Scope, id string, audit func(context.Context, refreshpostgres.Tx, refreshpostgres.ManualIntent) error) (refreshpostgres.ManualIntent, error) {
	row := s.row(id)
	if row == nil || row.ProjectID != scope.ProjectID || row.Environment != scope.Environment {
		return refreshpostgres.ManualIntent{}, refreshpostgres.ErrNotFound
	}
	if row.Status == refreshpostgres.ManualIntentCancelled {
		return *row, nil
	}
	if row.Status != refreshpostgres.ManualIntentWaiting && row.Status != refreshpostgres.ManualIntentClaimed {
		return refreshpostgres.ManualIntent{}, refreshpostgres.ErrConflict
	}
	row.Status = refreshpostgres.ManualIntentCancelled
	if audit != nil {
		if err := audit(ctx, nil, *row); err != nil {
			return refreshpostgres.ManualIntent{}, err
		}
	}
	return *row, nil
}

func (s *manualIntentMemoryStore) row(id string) *refreshpostgres.ManualIntent {
	for i := range s.rows {
		if s.rows[i].IntentID == id {
			return &s.rows[i]
		}
	}
	return nil
}

type manualIntentTestAuditWriter struct {
	intents []access.AuditIntent
}

func (w *manualIntentTestAuditWriter) RecordRefreshAuditTx(_ context.Context, _ refreshpostgres.Tx, intent access.AuditIntent) error {
	w.intents = append(w.intents, intent)
	return nil
}

type manualIntentTestCancelAuditWriter struct{ calls int }

func (w *manualIntentTestCancelAuditWriter) RecordRefreshCancelAuditTx(context.Context, refreshpostgres.Tx, access.AuditIntent) error {
	w.calls++
	return nil
}

type intentTestRunPersistence struct {
	*testRunPersistence
	existing map[string]refreshrun.RunRecord
	created  []refreshrun.RunTreeInput
}

func (r *intentTestRunPersistence) GetRun(_ context.Context, _ refreshrun.ReadScope, id string) (refreshrun.RunRecord, error) {
	if run, ok := r.existing[id]; ok {
		return run, nil
	}
	return refreshrun.RunRecord{}, sql.ErrNoRows
}

func (r *intentTestRunPersistence) CreateRunTree(_ context.Context, tree refreshrun.RunTreeInput) (refreshrun.RunRecord, []refreshrun.RunRecord, error) {
	r.created = append(r.created, tree)
	root := tree.Root
	return refreshrun.RunRecord{
		ID: root.RunID, Identity: root.Identity, SemanticModelID: root.SemanticModelID, PipelineID: root.PipelineID,
		PrincipalID: root.PrincipalID, TargetType: root.TargetType, TargetID: root.TargetID,
		TriggerType: root.TriggerType, InvocationSource: root.InvocationSource, Status: refreshrun.RunStatusQueued,
	}, nil, nil
}

type intentTestAuditContextWriter struct {
	intents []access.AuditIntent
}

func (w *intentTestAuditContextWriter) RecordRefreshAuditTx(_ context.Context, _ refreshpostgres.Tx, intent access.AuditIntent) error {
	w.intents = append(w.intents, intent)
	return nil
}

func intentTestDefinition() *artifact.Definition {
	return &artifact.Definition{
		Pipelines: map[string]refreshschedule.Definition{
			"daily": {ID: "daily", Name: "daily", SemanticModelID: "sales", SelectionDigest: intentTestDigest, Timezone: "UTC"},
		},
		ModelTables: map[string]semanticmodel.Table{"orders": {}},
		Models: map[string]*semanticmodel.Model{
			"sales": {
				Name:     "sales",
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
				Tables: map[string]semanticmodel.Table{
					"orders": {
						ModelName: "orders", GrainEntity: "order_id",
						Entities:   map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}},
						Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
					},
				},
			},
		},
	}
}

type intentTestArtifactLoader struct{ definition *artifact.Definition }

func (l intentTestArtifactLoader) Load(_ context.Context, activeArtifact servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
	return refreshrun.LoadedArtifact{Definition: l.definition}, nil
}

func newIntentTestModule(t *testing.T) (*Module, *manualIntentMemoryStore, *intentTestRunPersistence, *manualIntentTestAuditWriter, *string, *projectgraph.ServingIdentity, *artifact.Definition) {
	t.Helper()
	identity := projectgraphIdentity("sales", "dev", "generation_a")
	activeGeneration := identity
	currentSource := intentTestDigest
	definition := intentTestDefinition()
	state := servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment)}
	activeArtifact := servingstate.Artifact{ServingStateID: state.ID, Digest: intentTestDigest, Format: "tar.gz"}
	active := refreshrun.ServingState{State: state, Artifact: activeArtifact}
	store := &manualIntentMemoryStore{}
	runs := &intentTestRunPersistence{testRunPersistence: &testRunPersistence{}, existing: make(map[string]refreshrun.RunRecord)}
	loader := intentTestArtifactLoader{definition: definition}
	audit := &manualIntentTestAuditWriter{}
	m := &Module{
		runs: runs, manualIntents: store, manualTargetID: "instance_native", manualIntentOwner: "dispatcher_test",
		manualIntentCreateAuditWriter: audit, manualIntentCancelAuditWriter: &manualIntentTestCancelAuditWriter{},
		logger: slogDiscard(), durableAudit: true,
		resolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) { return activeGeneration, nil },
		service: refreshrun.Service{
			ServingStates: reconciliationStates{state: state, artifact: activeArtifact},
			ResolveActive: func(_ context.Context, got projectgraph.ServingIdentity) (refreshrun.ServingState, error) {
				if got.ProjectID != activeGeneration.ProjectID || got.Environment != activeGeneration.Environment || got.GenerationID != activeGeneration.GenerationID {
					return refreshrun.ServingState{}, fmt.Errorf("unexpected identity %#v", got)
				}
				return active, nil
			},
			ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) { return 4, nil },
			ResolveSourceDigest:   func(context.Context, projectgraph.ServingIdentity) (string, error) { return currentSource, nil },
			CanonicalExecutor: func(context.Context, refreshrun.JobRecord) (refreshrun.CanonicalRefreshResult, error) {
				return refreshrun.CanonicalRefreshResult{}, nil
			},
			Runs: runs, Artifacts: loader,
		},
	}
	return m, store, runs, audit, &currentSource, &activeGeneration, definition
}

func slogDiscard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func beginIntentTestCommand(t *testing.T, m *Module, key string) context.Context {
	t.Helper()
	ctx, err := m.BeginPipelineUICommand(context.Background(), PipelineUICommandInvocation{
		Action: "run", Project: "sales", IdempotencyKey: key, RequestID: strings.TrimPrefix(key, "ui:"), CorrelationID: "corr-" + strings.TrimPrefix(key, "ui:"),
	})
	if err != nil {
		t.Fatalf("begin UI command: %v", err)
	}
	return ctx
}

func TestQueueManualPipelineIntentAcceptsAndReplaysWithoutCreatingRun(t *testing.T) {
	m, store, runs, audit, currentSource, _, definition := newIntentTestModule(t)
	ctx := beginIntentTestCommand(t, m, "ui:request_1")
	command := ManualPipelineIntentCommand{Identity: projectgraphIdentity("sales", "dev", "generation_a"), PipelineID: "daily", PrincipalID: "user:test", IdempotencyKey: "ui:request_1"}
	first, err := m.QueueManualPipelineIntent(ctx, command)
	if err != nil {
		t.Fatalf("accept manual intent: %v", err)
	}
	if first.Status != refreshpostgres.ManualIntentWaiting || first.IntentID == "" || first.ReservedRunID == "" {
		t.Fatalf("accepted receipt = %#v", first)
	}
	if len(runs.created) != 0 {
		t.Fatalf("acceptance created %d runs before dispatch", len(runs.created))
	}
	if len(audit.intents) != 1 || audit.intents[0].Action != "refresh.request.accepted" {
		t.Fatalf("acceptance audit = %#v", audit.intents)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(audit.intents[0].MetadataJSON), &payload); err != nil || payload["status"] != refreshpostgres.ManualIntentWaiting {
		t.Fatalf("acceptance audit metadata = %q, err=%v", audit.intents[0].MetadataJSON, err)
	}

	// A request/key replay remains canonical even when a later publication
	// changes both the serving source and currently compiled definition.
	*currentSource = "sha256:" + strings.Repeat("b", 64)
	definition.Pipelines = nil
	second, err := m.QueueManualPipelineIntent(ctx, command)
	if err != nil {
		t.Fatalf("replay accepted manual intent after publication: %v", err)
	}
	if second.IntentID != first.IntentID || second.ReservedRunID != first.ReservedRunID || len(store.rows) != 1 {
		t.Fatalf("replay = %#v; original = %#v; rows=%d", second, first, len(store.rows))
	}
	if len(audit.intents) != 1 || len(runs.created) != 0 {
		t.Fatalf("replay duplicated audit/run: audits=%d runs=%d", len(audit.intents), len(runs.created))
	}
}

func TestCancelClaimedManualPipelineIntentBeforeRunAttachment(t *testing.T) {
	m, store, _, _, _, _, _ := newIntentTestModule(t)
	identity := projectgraphIdentity("sales", "dev", "generation_a")
	accepted, err := m.QueueManualPipelineIntent(beginIntentTestCommand(t, m, "ui:request_cancel_claimed"), ManualPipelineIntentCommand{
		Identity: identity, PipelineID: "daily", PrincipalID: "user:test", IdempotencyKey: "ui:request_cancel_claimed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ClaimNextManualIntent(t.Context(), refreshpostgres.Scope{ProjectID: identity.ProjectID.String(), Environment: identity.Environment}, "dispatcher_test", time.Minute); err != nil || !ok {
		t.Fatalf("claim request: ok=%v err=%v", ok, err)
	}
	ctx, err := m.BeginPipelineUICommand(t.Context(), PipelineUICommandInvocation{
		Action: "cancel-intent", Project: identity.ProjectID.String(), IdempotencyKey: "ui:cancel_claimed",
		RequestID: "cancel_claimed", CorrelationID: "cancel_claimed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CancelManualPipelineIntentForUI(ctx, identity, "daily", accepted.IntentID, "user:test", "ui:cancel_claimed"); err != nil {
		t.Fatalf("cancel claimed request: %v", err)
	}
	if row := store.row(accepted.IntentID); row == nil || row.Status != refreshpostgres.ManualIntentCancelled {
		t.Fatalf("cancelled request = %#v", row)
	}
}

func TestDispatchManualIntentMarksSourceChangedRequestStale(t *testing.T) {
	m, store, runs, _, currentSource, _, _ := newIntentTestModule(t)
	command := ManualPipelineIntentCommand{Identity: projectgraphIdentity("sales", "dev", "generation_a"), PipelineID: "daily", PrincipalID: "user:test", IdempotencyKey: "ui:request_stale"}
	if _, err := m.QueueManualPipelineIntent(beginIntentTestCommand(t, m, command.IdempotencyKey), command); err != nil {
		t.Fatalf("accept manual intent: %v", err)
	}
	*currentSource = "sha256:" + strings.Repeat("b", 64)
	if err := m.dispatchOneManualIntent(context.Background()); err != nil {
		t.Fatalf("dispatch stale intent: %v", err)
	}
	if store.rows[0].Status != refreshpostgres.ManualIntentStale || len(runs.created) != 0 {
		t.Fatalf("stale intent status=%q created runs=%d", store.rows[0].Status, len(runs.created))
	}
	views, err := m.ListManualPipelineIntents(context.Background(), refreshrun.ReadScope{ProjectID: projectgraph.ResourceID("sales"), Environment: "dev"})
	if err != nil || len(views) != 1 || views[0].Reason != "Pipeline definition changed while waiting; start a new request" {
		t.Fatalf("stale projection = %#v, err=%v", views, err)
	}
}

func TestDispatchManualIntentRecoversReservedRunAfterCrash(t *testing.T) {
	m, store, runs, _, currentSource, activeGeneration, _ := newIntentTestModule(t)
	command := ManualPipelineIntentCommand{Identity: *activeGeneration, PipelineID: "daily", PrincipalID: "user:test", IdempotencyKey: "ui:request_recover"}
	accepted, err := m.QueueManualPipelineIntent(beginIntentTestCommand(t, m, command.IdempotencyKey), command)
	if err != nil {
		t.Fatalf("accept manual intent: %v", err)
	}
	older := *activeGeneration
	older.GenerationID = "generation_before_cutover"
	runs.existing[accepted.ReservedRunID] = refreshrun.RunRecord{
		ID: accepted.ReservedRunID, Identity: older, PipelineID: "daily", TargetType: refreshrun.TargetRefreshPipeline,
		TargetID: "daily", PrincipalID: "user:test", TriggerType: refreshrun.TriggerManual,
		Status: refreshrun.RunStatusSucceeded,
	}
	activeGeneration.GenerationID = "generation_after_cutover"
	*currentSource = "sha256:" + strings.Repeat("c", 64)
	if err := m.dispatchOneManualIntent(context.Background()); err != nil {
		t.Fatalf("recover reserved run: %v", err)
	}
	if store.rows[0].Status != refreshpostgres.ManualIntentAttached || store.rows[0].AttachedRunID != accepted.ReservedRunID {
		t.Fatalf("recovered intent = %#v", store.rows[0])
	}
	if len(runs.created) != 0 {
		t.Fatalf("crash recovery created a second run: %#v", runs.created)
	}
}

func TestDispatchManualIntentQueuesReservedRunAgainstCurrentGeneration(t *testing.T) {
	m, store, runs, _, _, activeGeneration, _ := newIntentTestModule(t)
	command := ManualPipelineIntentCommand{Identity: *activeGeneration, PipelineID: "daily", PrincipalID: "user:test", IdempotencyKey: "ui:request_dispatch"}
	accepted, err := m.QueueManualPipelineIntent(beginIntentTestCommand(t, m, command.IdempotencyKey), command)
	if err != nil {
		t.Fatalf("accept manual intent: %v", err)
	}
	activeGeneration.GenerationID = "generation_at_dispatch"
	state := servingstate.State{ID: servingstate.ID(activeGeneration.GenerationID), ProjectID: activeGeneration.ProjectID, Environment: servingstate.Environment(activeGeneration.Environment)}
	activeArtifact := servingstate.Artifact{ServingStateID: state.ID, Digest: intentTestDigest, Format: "tar.gz"}
	m.service.ServingStates = reconciliationStates{state: state, artifact: activeArtifact}
	m.service.ResolveActive = func(context.Context, projectgraph.ServingIdentity) (refreshrun.ServingState, error) {
		return refreshrun.ServingState{State: state, Artifact: activeArtifact}, nil
	}
	if err := m.dispatchOneManualIntent(context.Background()); err != nil {
		t.Fatalf("dispatch manual intent: %v", err)
	}
	if len(runs.created) != 1 {
		t.Fatalf("queued run trees = %d, want one", len(runs.created))
	}
	root := runs.created[0].Root
	if root.RunID != accepted.ReservedRunID || root.Identity.GenerationID != "generation_at_dispatch" || root.PipelineID.String() != "daily" || root.PrincipalID != "user:test" {
		t.Fatalf("queued root = %#v", root)
	}
	if store.rows[0].Status != refreshpostgres.ManualIntentAttached || store.rows[0].AttachedRunID != accepted.ReservedRunID {
		t.Fatalf("attached intent = %#v", store.rows[0])
	}
}

func TestQueueManualPipelineIntentRequestDigestIsGenerationIndependent(t *testing.T) {
	identity := projectgraphIdentity("sales", "dev", "one")
	pipeline, _ := projectgraph.NewResourceID("daily")
	first, err := manualIntentRequestDigest(identity, "user:test", pipeline, "instance_native", "")
	if err != nil {
		t.Fatal(err)
	}
	identity.GenerationID = "two"
	second, err := manualIntentRequestDigest(identity, "user:test", pipeline, "instance_native", "")
	if err != nil || first != second {
		t.Fatalf("request digest changed with generation: %q != %q, err=%v", first, second, err)
	}
}

var _ manualIntentRepository = (*manualIntentMemoryStore)(nil)
var _ PostgresRefreshAuditWriter = (*manualIntentTestAuditWriter)(nil)
var _ PostgresCancelAuditWriter = (*manualIntentTestCancelAuditWriter)(nil)
var _ RunPersistence = (*intentTestRunPersistence)(nil)
