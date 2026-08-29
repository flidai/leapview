package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	manageddb "github.com/flidai/leapview/internal/manageddata/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RetentionRoot records durable reachability for an admitted managed-data
// revision. DuckLake snapshot roots are owned by the DuckLake capability.
// Root state is monotonic: live -> retiring -> expired.
type RetentionRoot struct {
	RootID, ProjectID, Environment, RevisionID string
	State                                      string
	Evidence                                   json.RawMessage
	CreatedAt, UpdatedAt                       time.Time
}

type ReconciliationEvidence struct {
	EvidenceID                                               int64
	ProjectID, Environment, ObjectKey, ObservedState, Action string
	Evidence                                                 json.RawMessage
	ObservedAt                                               time.Time
}

func boundedJSON(v json.RawMessage, max int) ([]byte, error) {
	if len(v) == 0 {
		v = []byte(`{}`)
	}
	if len(v) > max || !json.Valid(v) {
		return nil, ErrInvalid
	}
	return append([]byte(nil), v...), nil
}

func boundedObject(v json.RawMessage, max int) ([]byte, error) {
	if len(v) == 0 {
		return nil, ErrInvalid
	}
	b, err := boundedJSON(v, max)
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(b, &object); err != nil || object == nil || len(object) == 0 {
		return nil, ErrInvalid
	}
	return b, nil
}

func (r *Repository) RecordRetentionRoot(ctx context.Context, root RetentionRoot) (RetentionRoot, error) {
	db, err := requireDB(r)
	if err != nil {
		return RetentionRoot{}, err
	}
	// DuckLake snapshot retention/root state has its own capability-owned
	// authority.  Managed-data roots intentionally admit revisions only; a
	// cross-database snapshot tuple would otherwise be unverifiable here.
	if root.RootID == "" || root.ProjectID == "" || root.Environment == "" || root.RevisionID == "" {
		return RetentionRoot{}, ErrInvalid
	}
	if root.State == "" {
		root.State = "live"
	}
	if root.State != "live" && root.State != "retiring" && root.State != "expired" {
		return RetentionRoot{}, ErrInvalid
	}
	evidence, err := boundedObject(root.Evidence, 65536)
	if err != nil {
		return RetentionRoot{}, err
	}
	err = manageddb.New(db).InsertRetentionRoot(contextOrBackground(ctx), manageddb.InsertRetentionRootParams{RootID: root.RootID, ProjectID: root.ProjectID, Environment: root.Environment, RevisionID: &root.RevisionID, State: root.State, Evidence: evidence})
	if err != nil {
		return RetentionRoot{}, err
	}
	stored, err := r.RetentionRootByID(ctx, root.RootID)
	if err != nil {
		return RetentionRoot{}, err
	}
	if stored.ProjectID != root.ProjectID || stored.Environment != root.Environment || stored.RevisionID != root.RevisionID || stored.State != root.State || !sameJSON(stored.Evidence, evidence) {
		return RetentionRoot{}, ErrConflict
	}
	return stored, nil
}

func (r *Repository) RetentionRootByID(ctx context.Context, id string) (RetentionRoot, error) {
	db, err := requireDB(r)
	if err != nil {
		return RetentionRoot{}, err
	}
	row, err := manageddb.New(db).GetRetentionRoot(contextOrBackground(ctx), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetentionRoot{}, ErrNotFound
	}
	if err != nil {
		return RetentionRoot{}, err
	}
	out := RetentionRoot{RootID: row.RootID, ProjectID: row.ProjectID, Environment: row.Environment, State: row.State, Evidence: append([]byte(nil), row.Evidence...), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	if row.RevisionID != nil {
		out.RevisionID = *row.RevisionID
	}
	return out, nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func (r *Repository) TransitionRetentionRoot(ctx context.Context, id, target string) (RetentionRoot, error) {
	db, err := requireDB(r)
	if err != nil {
		return RetentionRoot{}, err
	}
	if target != "retiring" && target != "expired" {
		return RetentionRoot{}, ErrInvalid
	}
	tag, err := manageddb.New(db).TransitionRetentionRoot(contextOrBackground(ctx), manageddb.TransitionRetentionRootParams{RootID: id, State: target})
	if err != nil {
		return RetentionRoot{}, err
	}
	if tag.RowsAffected() != 1 {
		root, e := r.RetentionRootByID(ctx, id)
		if e != nil {
			return RetentionRoot{}, e
		}
		if root.State == target {
			return root, nil
		}
		return RetentionRoot{}, ErrConflict
	}
	return r.RetentionRootByID(ctx, id)
}

func (r *Repository) RecordReconciliationEvidence(ctx context.Context, evidence ReconciliationEvidence) (ReconciliationEvidence, error) {
	db, err := requireDB(r)
	if err != nil {
		return ReconciliationEvidence{}, err
	}
	if evidence.ProjectID == "" || evidence.Environment == "" || evidence.ObjectKey == "" || evidence.Action == "" {
		return ReconciliationEvidence{}, ErrInvalid
	}
	b, err := boundedObject(evidence.Evidence, 65536)
	if err != nil {
		return ReconciliationEvidence{}, err
	}
	row, err := manageddb.New(db).InsertReconciliationEvidence(contextOrBackground(ctx), manageddb.InsertReconciliationEvidenceParams{ProjectID: evidence.ProjectID, Environment: evidence.Environment, ObjectKey: evidence.ObjectKey, ObservedState: evidence.ObservedState, Action: evidence.Action, Evidence: b})
	if err != nil {
		return evidence, err
	}
	evidence.EvidenceID, evidence.ProjectID, evidence.Environment, evidence.ObjectKey, evidence.ObservedState, evidence.Action, evidence.Evidence, evidence.ObservedAt = row.EvidenceID, row.ProjectID, row.Environment, row.ObjectKey, row.ObservedState, row.Action, append([]byte(nil), row.Evidence...), row.ObservedAt.Time
	return evidence, nil
}

// PruneUploadSessions invokes the bounded SECURITY DEFINER maintenance
// function. Runtime callers cannot delete rows directly.
func (r *Repository) PruneUploadSessions(ctx context.Context, before time.Time, limit int) (int64, error) {
	db, err := requireDB(r)
	if err != nil {
		return 0, err
	}
	if limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	cutoff := pgtype.Timestamptz{}
	if !before.IsZero() {
		cutoff = pgtype.Timestamptz{Time: before.UTC(), Valid: true}
	}
	return manageddb.New(db).PruneUploadSessions(contextOrBackground(ctx), manageddb.PruneUploadSessionsParams{Cutoff: cutoff, PLimit: int32(limit)})
}
