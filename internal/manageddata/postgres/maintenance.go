package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
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
	_, err = db.Exec(contextOrBackground(ctx), `INSERT INTO managed_data.retention_root(root_id,project_id,environment,revision_id,state,evidence) VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT(root_id) DO NOTHING`, root.RootID, root.ProjectID, root.Environment, root.RevisionID, root.State, evidence)
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
	var out RetentionRoot
	var revision string
	var evidence []byte
	err = db.QueryRow(contextOrBackground(ctx), `SELECT root_id,project_id,environment,revision_id,state,evidence,created_at,updated_at FROM managed_data.retention_root WHERE root_id=$1`, id).Scan(&out.RootID, &out.ProjectID, &out.Environment, &revision, &out.State, &evidence, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetentionRoot{}, ErrNotFound
	}
	if err != nil {
		return RetentionRoot{}, err
	}
	out.RevisionID, out.Evidence = revision, append([]byte(nil), evidence...)
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
	tag, err := db.Exec(contextOrBackground(ctx), `UPDATE managed_data.retention_root SET state=$2,updated_at=clock_timestamp() WHERE root_id=$1 AND ((state='live' AND $2='retiring') OR (state='retiring' AND $2='expired'))`, id, target)
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
	err = db.QueryRow(contextOrBackground(ctx), `INSERT INTO managed_data.reconciliation_evidence(project_id,environment,object_key,observed_state,action,evidence) VALUES($1,$2,$3,$4,$5,$6::jsonb) RETURNING evidence_id,project_id,environment,object_key,observed_state,action,evidence,observed_at`, evidence.ProjectID, evidence.Environment, evidence.ObjectKey, evidence.ObservedState, evidence.Action, b).Scan(&evidence.EvidenceID, &evidence.ProjectID, &evidence.Environment, &evidence.ObjectKey, &evidence.ObservedState, &evidence.Action, &evidence.Evidence, &evidence.ObservedAt)
	return evidence, err
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
	var cutoff any
	if !before.IsZero() {
		cutoff = before.UTC()
	}
	var n int64
	err = db.QueryRow(contextOrBackground(ctx), `SELECT managed_data.prune_upload_sessions($1,$2)`, cutoff, limit).Scan(&n)
	return n, err
}
