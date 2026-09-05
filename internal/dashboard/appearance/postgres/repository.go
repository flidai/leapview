// Package postgres implements native PostgreSQL dashboard appearance storage.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	db "github.com/flidai/leapview/internal/dashboard/appearance/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Tx = pgx.Tx

type AuditInput struct {
	AuditID           string
	DomainEventID     string
	ActorID           string
	Action            string
	ProjectID         string
	DashboardID       string
	AggregateSequence int64
	MetadataJSON      string
}

type AuditPort interface {
	RecordAuditEvent(context.Context, Tx, AuditInput) error
}

type EventInput struct {
	EventID     string
	ProjectID   string
	DashboardID string
	Revision    int64
	ActorID     string
	Patch       dashboardappearance.Patch
}

// Event is the canonical projection returned after the durable event append.
// The repository validates every immutable identity field before constructing
// the audit input from it.
type Event struct {
	EventID          string
	ProjectID        string
	DashboardID      string
	ActorID          string
	Revision         int64
	Patch            dashboardappearance.Patch
	AggregateVersion int64
}

type EventPort interface {
	AppendEvent(context.Context, Tx, EventInput) (Event, error)
}

type Options struct {
	Audit  AuditPort
	Events EventPort
}

var (
	ErrUnavailable   = errors.New("dashboard PostgreSQL appearance store is unavailable")
	ErrAuditMissing  = errors.New("dashboard appearance audit port is unavailable")
	ErrEventsMissing = errors.New("dashboard appearance event port is unavailable")
	ErrConflict      = errors.New("dashboard appearance revision conflict")
)

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrUnavailable
	}
	_, err := tx.Exec(ctx, schemaSQL) // sqlc-exception: schema-ddl
	return err
}

type Repository struct {
	db     DBTX
	audit  AuditPort
	events EventPort
	native bool
}

func New(db DBTX, options Options) (*Repository, error) {
	if db == nil {
		return nil, ErrUnavailable
	}
	if options.Audit == nil {
		return nil, ErrAuditMissing
	}
	if options.Events == nil {
		return nil, ErrEventsMissing
	}
	if _, ok := db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}); !ok {
		return nil, fmt.Errorf("dashboard appearance PostgreSQL handle must support transactions")
	}
	return &Repository{db: db, audit: options.Audit, events: options.Events, native: true}, nil
}

// IsNative reports whether the repository was constructed by New.
func (r *Repository) IsNative() bool { return r != nil && r.native }

func (r *Repository) Ping(ctx context.Context) error {
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	_, err := db.New(r.db).Ping(ctx)
	return err
}

func (r *Repository) ListProject(ctx context.Context, projectID projectgraph.ResourceID) (map[projectgraph.ResourceID]dashboardappearance.Record, error) {
	if r == nil || r.db == nil {
		return nil, ErrUnavailable
	}
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project ID: %w", err)
	}
	rows, err := db.New(r.db).ListProject(ctx, projectID.String())
	if err != nil {
		return nil, err
	}
	out := make(map[projectgraph.ResourceID]dashboardappearance.Record, len(rows))
	for _, row := range rows {
		id, err := projectgraph.NewResourceID(row.DashboardID)
		if err != nil {
			return nil, fmt.Errorf("stored dashboard ID: %w", err)
		}
		out[id] = dashboardappearance.Record{Key: dashboardappearance.Key{ProjectID: projectID, DashboardID: id}, Value: dashboardappearance.Value{Icon: row.Icon, Color: row.Color}, Revision: row.Revision}
	}
	return out, nil
}

func (r *Repository) Get(ctx context.Context, key dashboardappearance.Key) (dashboardappearance.Record, error) {
	if r == nil || r.db == nil {
		return dashboardappearance.Record{}, ErrUnavailable
	}
	if err := validateKey(key); err != nil {
		return dashboardappearance.Record{}, err
	}
	return get(ctx, r.db, key)
}

func (r *Repository) ApplyPatch(ctx context.Context, key dashboardappearance.Key, actorID string, patch dashboardappearance.Patch) (dashboardappearance.Record, error) {
	if r == nil || r.db == nil {
		return dashboardappearance.Record{}, ErrUnavailable
	}
	b, ok := r.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return dashboardappearance.Record{}, ErrUnavailable
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := r.ApplyPatchTx(ctx, tx, key, actorID, patch)
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return dashboardappearance.Record{}, err
	}
	return row, nil
}

func (r *Repository) ApplyPatchTx(ctx context.Context, tx Tx, key dashboardappearance.Key, actorID string, patch dashboardappearance.Patch) (dashboardappearance.Record, error) {
	return r.applyPatchTx(ctx, tx, key, actorID, patch, 0, false)
}

func (r *Repository) ApplyPatchCAS(ctx context.Context, tx Tx, key dashboardappearance.Key, expectedRevision int64, actorID string, patch dashboardappearance.Patch) (dashboardappearance.Record, error) {
	return r.applyPatchTx(ctx, tx, key, actorID, patch, expectedRevision, true)
}

func (r *Repository) applyPatchTx(ctx context.Context, tx Tx, key dashboardappearance.Key, actorID string, patch dashboardappearance.Patch, expected int64, cas bool) (dashboardappearance.Record, error) {
	if r == nil || r.audit == nil {
		return dashboardappearance.Record{}, ErrAuditMissing
	}
	if r.events == nil {
		return dashboardappearance.Record{}, ErrEventsMissing
	}
	if tx == nil {
		return dashboardappearance.Record{}, ErrUnavailable
	}
	if err := validateKey(key); err != nil {
		return dashboardappearance.Record{}, err
	}
	if err := dashboardappearance.ValidatePatch(patch); err != nil {
		return dashboardappearance.Record{}, err
	}
	if patch.Icon == nil && patch.Color == nil {
		return dashboardappearance.Record{}, dashboardappearance.ErrEmptyPatch
	}
	icon, iconPresent, color, colorPresent := "", patch.Icon != nil, "", patch.Color != nil
	if patch.Icon != nil {
		icon = dashboardappearance.StoredValue(*patch.Icon)
	}
	if patch.Color != nil {
		color = dashboardappearance.StoredValue(*patch.Color)
	}
	var err error
	if cas {
		if expected <= 0 {
			return dashboardappearance.Record{}, ErrConflict
		}
		// A CAS mutation must observe an existing revision. PostgreSQL's
		// INSERT ... ON CONFLICT form would otherwise create a missing row even
		// when the caller supplied a positive expected revision.
		current, getErr := db.New(tx).Get(ctx, db.GetParams{ProjectID: key.ProjectID.String(), DashboardID: key.DashboardID.String()})
		if errors.Is(getErr, pgx.ErrNoRows) || (getErr == nil && current.Revision != expected) {
			return dashboardappearance.Record{}, ErrConflict
		}
		if getErr != nil {
			return dashboardappearance.Record{}, getErr
		}
		var rows int64
		rows, err = db.New(tx).UpsertCAS(ctx, db.UpsertCASParams{ProjectID: key.ProjectID.String(), DashboardID: key.DashboardID.String(), Icon: icon, Color: color, UpdatedBy: strings.TrimSpace(actorID), IconPresent: iconPresent, ColorPresent: colorPresent, ExpectedRevision: expected})
		if err == nil && rows != 1 {
			return dashboardappearance.Record{}, ErrConflict
		}
	} else {
		err = db.New(tx).Upsert(ctx, db.UpsertParams{ProjectID: key.ProjectID.String(), DashboardID: key.DashboardID.String(), Icon: icon, Color: color, UpdatedBy: strings.TrimSpace(actorID), IconPresent: iconPresent, ColorPresent: colorPresent})
	}
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	record, err := get(ctx, tx, key)
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return dashboardappearance.Record{}, fmt.Errorf("generate dashboard appearance event ID: %w", err)
	}
	eventIDString := eventID.String()
	emitted, err := r.events.AppendEvent(ctx, tx, EventInput{EventID: eventIDString, ProjectID: key.ProjectID.String(), DashboardID: key.DashboardID.String(), Revision: record.Revision, ActorID: actorID, Patch: patch})
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	if err := validateEvent(emitted, eventIDString, key, actorID, record.Revision, patch); err != nil {
		return dashboardappearance.Record{}, err
	}
	auditID, err := uuid.NewV7()
	if err != nil {
		return dashboardappearance.Record{}, fmt.Errorf("generate dashboard appearance audit ID: %w", err)
	}
	metadata, err := json.Marshal(struct {
		Icon  string `json:"icon"`
		Color string `json:"color"`
	}{Icon: icon, Color: color})
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	if err := r.audit.RecordAuditEvent(ctx, tx, AuditInput{AuditID: auditID.String(), DomainEventID: emitted.EventID, ActorID: actorID, Action: "dashboard.appearance.updated", ProjectID: key.ProjectID.String(), DashboardID: key.DashboardID.String(), AggregateSequence: emitted.AggregateVersion, MetadataJSON: string(metadata)}); err != nil {
		return dashboardappearance.Record{}, err
	}
	return record, nil
}

func validateEvent(event Event, eventID string, key dashboardappearance.Key, actorID string, revision int64, patch dashboardappearance.Patch) error {
	parsed, err := uuid.Parse(event.EventID)
	if err != nil || parsed.Version() != 7 || event.EventID != eventID {
		return fmt.Errorf("%w: appearance event identity differs", ErrConflict)
	}
	if event.ProjectID != key.ProjectID.String() || event.DashboardID != key.DashboardID.String() || event.ActorID != actorID || event.Revision != revision || event.AggregateVersion != revision || !equalPatch(event.Patch, patch) {
		return fmt.Errorf("%w: appearance event aggregate differs", ErrConflict)
	}
	return nil
}

func equalPatch(left, right dashboardappearance.Patch) bool {
	return equalOptionalString(left.Icon, right.Icon) && equalOptionalString(left.Color, right.Color)
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func get(ctx context.Context, q DBTX, key dashboardappearance.Key) (dashboardappearance.Record, error) {
	row, err := db.New(q).Get(ctx, db.GetParams{ProjectID: key.ProjectID.String(), DashboardID: key.DashboardID.String()})
	if errors.Is(err, pgx.ErrNoRows) {
		return dashboardappearance.Record{}, dashboardappearance.ErrInvalid
	}
	if err != nil {
		return dashboardappearance.Record{}, err
	}
	return dashboardappearance.Record{Key: key, Value: dashboardappearance.Value{Icon: row.Icon, Color: row.Color}, Revision: row.Revision}, nil
}

func validateKey(key dashboardappearance.Key) error {
	if err := key.ProjectID.Validate(); err != nil {
		return fmt.Errorf("project ID: %w", err)
	}
	if err := key.DashboardID.Validate(); err != nil {
		return fmt.Errorf("dashboard ID: %w", err)
	}
	return nil
}

var _ dashboardappearance.Store = (*Repository)(nil)
