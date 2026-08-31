// Package postgres persists query activity in the PostgreSQL control plane.
// Query events are append-only evidence. The repository accepts pgx's native
// DBTX surface and never opens a database/sql connection.
package postgres

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/queryaudit"
	auditdb "github.com/flidai/leapview/internal/analytics/queryaudit/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MaintenanceDBTX is the native PostgreSQL surface for the separately
// authenticated retention pool. It intentionally has the same pgx method set
// as DBTX so pools and caller-owned transactions need no adapter; PostgreSQL
// role grants remain the enforcement boundary.
type MaintenanceDBTX interface {
	DBTX
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Tx is the strict caller-owned PostgreSQL transaction surface. Pools satisfy
// DBTX for standalone operations but intentionally cannot satisfy Tx.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

var (
	ErrInvalid  = errors.New("invalid query audit event")
	ErrNotFound = errors.New("query audit event not found")
	ErrConflict = errors.New("query audit event conflict")
)

const (
	MaxErrorBytes     = 16 << 10
	MaxSQLBytes       = 64 << 10
	MaxPlanBytes      = 64 << 10
	MaxQueryJSONBytes = 64 << 10
	MaxSearchBytes    = 512
	MaxFilterValues   = 256
	MaxPageSize       = 1000
	DefaultPageSize   = 100
	MaxPruneBatch     = 1000
)

//go:embed schema.sql
var schemaFS embed.FS

var schemaSQL = func() string {
	data, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}()

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// sqlc-exception: schema-ddl. schema.sql is the capability-owned DDL,
	// triggers, and grants executed by migration runners.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

type Repository struct{ db DBTX }

// Maintenance owns destructive retention work. It is deliberately separate
// from Repository so request-serving code has no prune method to call by
// accident; the database role may execute only the bounded owner function.
type Maintenance struct{ db MaintenanceDBTX }

// PruneResult is the durable evidence returned by one retention batch. Before
// records the requested policy boundary; Cutoff is PostgreSQL's effective
// (future-capped) boundary; Removed is the exact number of events deleted.
type PruneResult struct {
	Before  time.Time
	Cutoff  time.Time
	FloorAt time.Time
	Removed int64
}

var _ queryaudit.Repository = (*Repository)(nil)

func New(db DBTX) *Repository           { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository { return New(db) }

// PostgreSQLAuthority marks this repository as the native query-audit
// authority.  Production analytics composition uses the marker together with
// Configured so a generic queryaudit.Store cannot silently select a SQLite or
// other non-PostgreSQL implementation.
func (*Repository) PostgreSQLAuthority() {}

// Configured reports whether the repository has a native PostgreSQL handle.
// Pool readiness and schema revision remain owned by the application
// lifecycle, so this is intentionally a shallow composition check.
func (r *Repository) Configured() bool { return r != nil && r.db != nil }

// NewMaintenance constructs the bounded query-audit retention facade for a
// separately authenticated maintenance connection pool.
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

type storedEvent struct {
	ID                                                                             uuid.UUID
	RetryIdentity, ProjectID, PrincipalID                                          string
	Surface, Operation, QueryKind, ModelID, Target                                 string
	ObjectType, ObjectID, RequestID, CorrelationID, Status                         string
	DurationMS, QueueWaitMS, PlanningMS, ConnectionWaitMS, DatabaseMS, ExecutionMS int64
	ExecutionState                                                                 string
	RowsReturned, BytesEstimate                                                    int64
	Error, SQL, PlanText                                                           string
	QueryJSON                                                                      []byte
	CreatedAt                                                                      time.Time
}

func storedFromValues(id, retry, project, principal, surface, operation, kind, model, target, objectType, objectID, requestID, correlation, status string, duration, queueWait, planning, connectionWait, database, execution int64, executionState string, rowsReturned, bytesEstimate int64, eventErr, sqlText, planText, queryJSON string, createdAt pgtype.Timestamptz) (storedEvent, error) {
	parsedID, err := uuid.Parse(id)
	if err != nil {
		return storedEvent{}, err
	}
	if !createdAt.Valid {
		return storedEvent{}, fmt.Errorf("%w: persisted created_at is null", ErrInvalid)
	}
	var document any
	if decodeErr := json.Unmarshal([]byte(queryJSON), &document); decodeErr != nil {
		return storedEvent{}, fmt.Errorf("%w: persisted query_json: %v", ErrInvalid, decodeErr)
	}
	canonical, marshalErr := json.Marshal(document)
	if marshalErr != nil {
		return storedEvent{}, fmt.Errorf("%w: persisted query_json: %v", ErrInvalid, marshalErr)
	}
	return storedEvent{ID: parsedID, RetryIdentity: retry, ProjectID: project, PrincipalID: principal, Surface: surface, Operation: operation, QueryKind: kind, ModelID: model, Target: target, ObjectType: objectType, ObjectID: objectID, RequestID: requestID, CorrelationID: correlation, Status: status, DurationMS: duration, QueueWaitMS: queueWait, PlanningMS: planning, ConnectionWaitMS: connectionWait, DatabaseMS: database, ExecutionMS: execution, ExecutionState: executionState, RowsReturned: rowsReturned, BytesEstimate: bytesEstimate, Error: eventErr, SQL: sqlText, PlanText: planText, QueryJSON: canonical, CreatedAt: createdAt.Time.UTC()}, nil
}

func storedFromGet(row auditdb.GetQueryEventRow) (storedEvent, error) {
	return storedFromValues(row.EventID, row.RetryIdentity, row.ProjectID, row.PrincipalID, row.Surface, row.Operation, row.QueryKind, row.ModelID, row.Target, row.ObjectType, row.ObjectID, row.RequestID, row.CorrelationID, row.Status, row.DurationMs, row.QueueWaitMs, row.PlanningMs, row.ConnectionWaitMs, row.DatabaseMs, row.ExecutionMs, row.ExecutionState, row.RowsReturned, row.BytesEstimate, row.Error, row.SqlText, row.PlanText, row.QueryJson, row.CreatedAt)
}

func storedFromFind(row auditdb.FindQueryEventByIdentityRow) (storedEvent, error) {
	return storedFromValues(row.EventID, row.RetryIdentity, row.ProjectID, row.PrincipalID, row.Surface, row.Operation, row.QueryKind, row.ModelID, row.Target, row.ObjectType, row.ObjectID, row.RequestID, row.CorrelationID, row.Status, row.DurationMs, row.QueueWaitMs, row.PlanningMs, row.ConnectionWaitMs, row.DatabaseMs, row.ExecutionMs, row.ExecutionState, row.RowsReturned, row.BytesEstimate, row.Error, row.SqlText, row.PlanText, row.QueryJson, row.CreatedAt)
}

func storedFromList(row auditdb.ListQueryEventsRow) (storedEvent, error) {
	return storedFromValues(row.EventID, row.RetryIdentity, row.ProjectID, row.PrincipalID, row.Surface, row.Operation, row.QueryKind, row.ModelID, row.Target, row.ObjectType, row.ObjectID, row.RequestID, row.CorrelationID, row.Status, row.DurationMs, row.QueueWaitMs, row.PlanningMs, row.ConnectionWaitMs, row.DatabaseMs, row.ExecutionMs, row.ExecutionState, row.RowsReturned, row.BytesEstimate, row.Error, row.SqlText, row.PlanText, row.QueryJson, row.CreatedAt)
}

func (r *Repository) getStored(ctx context.Context, id uuid.UUID) (storedEvent, error) {
	row, err := auditdb.New(r.db).GetQueryEvent(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return storedEvent{}, err
	}
	return storedFromGet(row)
}

func (r *Repository) findStored(ctx context.Context, id uuid.UUID, retry string) (storedEvent, error) {
	row, err := auditdb.New(r.db).FindQueryEventByIdentity(ctx, auditdb.FindQueryEventByIdentityParams{EventID: pgtype.UUID{Bytes: id, Valid: true}, RetryIdentity: retry})
	stored, convErr := storedFromFind(row)
	if err == nil {
		err = convErr
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return storedEvent{}, fmt.Errorf("%w: conflicting insert was not visible", ErrConflict)
	}
	return stored, err
}

func (r *Repository) RecordQueryEvent(ctx context.Context, input queryaudit.EventInput) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("%w: repository is unavailable", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, eventID, retry, err := normalizeInput(input)
	if err != nil {
		return err
	}
	returnedText, err := auditdb.New(r.db).InsertQueryEvent(ctx, auditdb.InsertQueryEventParams{
		EventID: pgtype.UUID{Bytes: eventID, Valid: true}, RetryIdentity: retry, ProjectID: normalized.ProjectID.String(), PrincipalID: normalized.PrincipalID, Surface: normalized.Surface, Operation: normalized.Operation, QueryKind: normalized.QueryKind, ModelID: normalized.ModelID, Target: normalized.Target, ObjectType: normalized.ObjectType, ObjectID: normalized.ObjectID, RequestID: normalized.RequestID, CorrelationID: normalized.CorrelationID, Status: normalized.Status,
		DurationMs: normalized.DurationMS, QueueWaitMs: normalized.QueueWaitMS, PlanningMs: normalized.PlanningMS, ConnectionWaitMs: normalized.ConnectionWaitMS, DatabaseMs: normalized.DatabaseMS, ExecutionMs: normalized.ExecutionMS, ExecutionState: normalized.ExecutionState, RowsReturned: int64(normalized.RowsReturned), BytesEstimate: normalized.BytesEstimate, Error: normalized.Error, SqlText: normalized.SQL, PlanText: normalized.PlanText, QueryJson: []byte(normalized.QueryJSON),
	})
	if err == nil {
		if returnedText != eventID.String() {
			return fmt.Errorf("%w: inserted event identity mismatch", ErrConflict)
		}
		_, err = r.getStored(ctx, eventID)
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	stored, err := r.findStored(ctx, eventID, retry)
	if err != nil {
		return err
	}
	if stored.ID != eventID {
		return conflict(eventID.String(), "event_id")
	}
	if stored.RetryIdentity != retry {
		return conflict(eventID.String(), "retry_identity")
	}
	if field := differingField(stored, normalized); field != "" {
		return conflict(eventID.String(), field)
	}
	return nil
}

func (r *Repository) GetQueryEvent(ctx context.Context, id string) (queryaudit.Event, error) {
	if r == nil || r.db == nil {
		return queryaudit.Event{}, fmt.Errorf("%w: repository is unavailable", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	eventID, err := parseUUID(id)
	if err != nil {
		return queryaudit.Event{}, err
	}
	stored, err := r.getStored(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return queryaudit.Event{}, ErrNotFound
	}
	if err != nil {
		return queryaudit.Event{}, err
	}
	return toEvent(stored)
}

func (r *Repository) ListQueryEvents(ctx context.Context, filter queryaudit.Filter) ([]queryaudit.Event, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("%w: repository is unavailable", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	if err := validateFilter(filter); err != nil {
		return nil, err
	}
	if len(filter.Search) > MaxSearchBytes || !utf8.ValidString(filter.Search) {
		return nil, fmt.Errorf("%w: search exceeds %d bytes or is not UTF-8", ErrInvalid, MaxSearchBytes)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if filter.PageToken != "" {
		if filter.CursorTime != "" || filter.CursorID != "" {
			return nil, fmt.Errorf("%w: page token cannot be combined with explicit cursor", ErrInvalid)
		}
		var err error
		filter.CursorTime, filter.CursorID, err = decodePageToken(filter.PageToken)
		if err != nil {
			return nil, err
		}
	} else if (filter.CursorTime == "") != (filter.CursorID == "") {
		return nil, fmt.Errorf("%w: cursor time and cursor id must be supplied together", ErrInvalid)
	}
	projects := projectValues(filter)
	principals := stringValues(filter.PrincipalID, filter.PrincipalIDs)
	surfaces := stringValues(filter.Surface, filter.Surfaces)
	operations := strings.TrimSpace(filter.Operation)
	kinds := stringValues(filter.QueryKind, filter.QueryKinds)
	model := strings.TrimSpace(filter.ModelID)
	target := strings.TrimSpace(filter.Target)
	statuses := stringValues(filter.Status, filter.Statuses)
	search := strings.TrimSpace(filter.Search)
	var fromTime, toTime, cursorTime pgtype.Timestamptz
	if value := strings.TrimSpace(filter.From); value != "" {
		parsed, parseErr := parseTime(value)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: from time: %v", ErrInvalid, parseErr)
		}
		fromTime = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	if value := strings.TrimSpace(filter.To); value != "" {
		parsed, parseErr := parseTime(value)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: to time: %v", ErrInvalid, parseErr)
		}
		toTime = pgtype.Timestamptz{Time: parsed, Valid: true}
	}
	var cursorID pgtype.UUID
	if value := strings.TrimSpace(filter.CursorTime); value != "" {
		parsed, parseErr := parseTime(value)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: cursor time: %v", ErrInvalid, parseErr)
		}
		cursorTime = pgtype.Timestamptz{Time: parsed, Valid: true}
		id, parseErr := parseUUID(filter.CursorID)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: cursor id: %v", ErrInvalid, parseErr)
		}
		cursorID = pgtype.UUID{Bytes: id, Valid: true}
	}
	rows, err := auditdb.New(r.db).ListQueryEvents(ctx, auditdb.ListQueryEventsParams{
		HasProject: len(projects) > 0, ProjectIds: projects, HasPrincipal: len(principals) > 0, PrincipalIds: principals,
		HasSurface: len(surfaces) > 0, Surfaces: surfaces, HasOperation: operations != "", Operation: operations,
		HasQueryKind: len(kinds) > 0, QueryKinds: kinds, HasModel: model != "", ModelID: model, HasTarget: target != "", Target: target,
		HasStatus: len(statuses) > 0, Statuses: statuses, HasSearch: search != "", Search: search,
		HasFrom: fromTime.Valid, FromTime: fromTime, HasTo: toTime.Valid, ToTime: toTime,
		HasCursor: cursorTime.Valid, CursorTime: cursorTime, CursorID: cursorID, PageSize: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	events := make([]queryaudit.Event, 0, limit)
	for _, row := range rows {
		stored, scanErr := storedFromList(row)
		if scanErr != nil {
			return nil, scanErr
		}
		event, convertErr := toEvent(stored)
		if convertErr != nil {
			return nil, convertErr
		}
		events = append(events, event)
	}
	return events, nil
}

func (r *Repository) ListQueryEventFilterOptions(ctx context.Context, field, search string, limit int) ([]queryaudit.FilterOption, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("%w: repository is unavailable", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	field = strings.TrimSpace(field)
	if _, ok := filterColumn(field); !ok {
		return nil, fmt.Errorf("%w: unsupported query event filter option field %q", ErrInvalid, field)
	}
	if len(search) > MaxSearchBytes || !utf8.ValidString(search) {
		return nil, fmt.Errorf("%w: search exceeds %d bytes or is not UTF-8", ErrInvalid, MaxSearchBytes)
	}
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	options := make([]queryaudit.FilterOption, 0, limit)
	search = strings.TrimSpace(search)
	pageSize := int32(limit)
	appendOption := func(value string, count int64) {
		options = append(options, queryaudit.FilterOption{Value: value, Count: int(count)})
	}
	switch field {
	case "project":
		rows, err := auditdb.New(r.db).ListQueryEventFilterOptions(ctx, auditdb.ListQueryEventFilterOptionsParams{Search: search, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			appendOption(row.Value, row.Count)
		}
	case "principal":
		rows, err := auditdb.New(r.db).ListPrincipalFilterOptions(ctx, auditdb.ListPrincipalFilterOptionsParams{Search: search, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			appendOption(row.Value, row.Count)
		}
	case "surface":
		rows, err := auditdb.New(r.db).ListSurfaceFilterOptions(ctx, auditdb.ListSurfaceFilterOptionsParams{Search: search, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			appendOption(row.Value, row.Count)
		}
	case "kind":
		rows, err := auditdb.New(r.db).ListKindFilterOptions(ctx, auditdb.ListKindFilterOptionsParams{Search: search, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			appendOption(row.Value, row.Count)
		}
	case "status":
		rows, err := auditdb.New(r.db).ListStatusFilterOptions(ctx, auditdb.ListStatusFilterOptionsParams{Search: search, PageSize: pageSize})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			appendOption(row.Value, row.Count)
		}
	}
	return options, nil
}

// Prune removes at most limit query-audit events at or before before. The
// operation runs through the SECURITY DEFINER owner function on a separate
// maintenance connection and commits one bounded batch. A zero cutoff is
// rejected so callers must make the retention policy's time range explicit.
func (m *Maintenance) Prune(ctx context.Context, before time.Time, limit int) (PruneResult, error) {
	var result PruneResult
	if m == nil || m.db == nil {
		return result, ErrInvalid
	}
	if limit < 1 || limit > MaxPruneBatch || before.IsZero() {
		return result, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := m.db.(beginner)
	if !ok {
		return result, errors.New("query audit maintenance requires a pgx transaction-capable DB")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return result, err
	}
	result, err = m.PruneTx(ctx, tx, before, limit)
	if err != nil {
		_ = tx.Rollback(context.Background())
		return PruneResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = tx.Rollback(context.Background())
		return PruneResult{}, err
	}
	return result, nil
}

// PruneTx executes one bounded retention batch on a caller-owned transaction.
// It does not commit or roll back, allowing maintenance orchestration to write
// its own retention evidence in the same transaction when needed.
func (m *Maintenance) PruneTx(ctx context.Context, tx Tx, before time.Time, limit int) (PruneResult, error) {
	if m == nil || tx == nil {
		return PruneResult{}, ErrInvalid
	}
	if limit < 1 || limit > MaxPruneBatch || before.IsZero() {
		return PruneResult{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	before = before.UTC()
	row, err := auditdb.New(tx).PruneQueryEvents(ctx, auditdb.PruneQueryEventsParams{
		Before: pgtype.Timestamptz{Time: before, Valid: true},
		Batch:  int32(limit),
	})
	if err != nil {
		return PruneResult{}, err
	}
	if !row.Cutoff.Valid || !row.FloorAt.Valid || row.Removed < 0 || row.Removed > int64(limit) {
		return PruneResult{}, fmt.Errorf("%w: invalid query-event prune evidence", ErrConflict)
	}
	return PruneResult{Before: before, Cutoff: row.Cutoff.Time.UTC(), FloorAt: row.FloorAt.Time.UTC(), Removed: row.Removed}, nil
}

func toEvent(stored storedEvent) (queryaudit.Event, error) {
	projectID, err := projectgraph.NewResourceID(stored.ProjectID)
	if err != nil {
		return queryaudit.Event{}, fmt.Errorf("%w: persisted project id: %v", ErrInvalid, err)
	}
	return queryaudit.Event{ID: stored.ID.String(), EventInput: queryaudit.EventInput{
		EventID: stored.ID.String(), RetryIdentity: stored.RetryIdentity, ProjectID: projectID,
		PrincipalID: stored.PrincipalID, Surface: stored.Surface, Operation: stored.Operation, QueryKind: stored.QueryKind,
		ModelID: stored.ModelID, Target: stored.Target, ObjectType: stored.ObjectType, ObjectID: stored.ObjectID,
		RequestID: stored.RequestID, CorrelationID: stored.CorrelationID, Status: stored.Status, DurationMS: stored.DurationMS,
		QueueWaitMS: stored.QueueWaitMS, PlanningMS: stored.PlanningMS, ConnectionWaitMS: stored.ConnectionWaitMS,
		DatabaseMS: stored.DatabaseMS, ExecutionMS: stored.ExecutionMS, ExecutionState: stored.ExecutionState,
		RowsReturned: int(stored.RowsReturned), BytesEstimate: stored.BytesEstimate, Error: stored.Error, SQL: stored.SQL,
		PlanText: stored.PlanText, QueryJSON: string(stored.QueryJSON),
	}, CreatedAt: stored.CreatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

func normalizeInput(input queryaudit.EventInput) (queryaudit.EventInput, uuid.UUID, string, error) {
	if err := input.Validate(); err != nil {
		return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if input.PrincipalID != strings.TrimSpace(input.PrincipalID) {
		return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: principal id must be trimmed", ErrInvalid)
	}
	if input.RowsReturned < 0 || input.BytesEstimate < 0 || input.DurationMS < 0 || input.QueueWaitMS < 0 || input.PlanningMS < 0 || input.ConnectionWaitMS < 0 || input.DatabaseMS < 0 || input.ExecutionMS < 0 {
		return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: durations and result sizes cannot be negative", ErrInvalid)
	}
	textLimits := []struct {
		name, value string
		limit       int
	}{
		{"principal_id", input.PrincipalID, 255}, {"surface", input.Surface, 128}, {"operation", input.Operation, 256},
		{"query_kind", input.QueryKind, 128}, {"model_id", input.ModelID, 255}, {"target", input.Target, 512},
		{"object_type", input.ObjectType, 128}, {"object_id", input.ObjectID, 512}, {"request_id", input.RequestID, 512},
		{"correlation_id", input.CorrelationID, 512}, {"status", input.Status, 64}, {"execution_state", input.ExecutionState, 64},
	}
	for _, item := range textLimits {
		if !utf8.ValidString(item.value) || len(item.value) > item.limit {
			return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: %s exceeds %d bytes or is not UTF-8", ErrInvalid, item.name, item.limit)
		}
	}
	input.Error = queryaudit.RedactSensitiveText(input.Error)
	input.SQL = queryaudit.RedactSensitiveText(input.SQL)
	input.PlanText = queryaudit.RedactSensitiveText(input.PlanText)
	for _, item := range []struct {
		name, value string
		limit       int
	}{{"error", input.Error, MaxErrorBytes}, {"sql_text", input.SQL, MaxSQLBytes}, {"plan_text", input.PlanText, MaxPlanBytes}} {
		if !utf8.ValidString(item.value) || len(item.value) > item.limit {
			return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: %s exceeds %d bytes or is not UTF-8", ErrInvalid, item.name, item.limit)
		}
	}
	queryJSON := queryaudit.RedactSensitiveText(strings.TrimSpace(input.QueryJSON))
	if queryJSON == "" {
		queryJSON = "{}"
	}
	if len(queryJSON) > MaxQueryJSONBytes {
		return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: query_json exceeds %d bytes", ErrInvalid, MaxQueryJSONBytes)
	}
	var document any
	if err := strictjson.DecodeWithOptions([]byte(queryJSON), &document, strictjson.Options{MaxBytes: MaxQueryJSONBytes, MaxDepth: 32, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil {
		return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: query_json must be strict JSON: %v", ErrInvalid, err)
	}
	if _, ok := document.(map[string]any); !ok {
		return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: query_json must be an object", ErrInvalid)
	}
	canonical, err := json.Marshal(document)
	if err != nil || len(canonical) > MaxQueryJSONBytes {
		return queryaudit.EventInput{}, uuid.Nil, "", fmt.Errorf("%w: query_json cannot be canonicalized", ErrInvalid)
	}
	input.QueryJSON = string(canonical)
	eventID, retryIdentity, err := resolveIdentity(input)
	if err != nil {
		return queryaudit.EventInput{}, uuid.Nil, "", err
	}
	return input, eventID, retryIdentity, nil
}

func resolveIdentity(input queryaudit.EventInput) (uuid.UUID, string, error) {
	eventText := strings.TrimSpace(input.EventID)
	retryIdentity := strings.TrimSpace(input.RetryIdentity)
	if eventText != "" {
		eventID, err := parseUUID(eventText)
		if err != nil {
			return uuid.Nil, "", err
		}
		if retryIdentity == "" {
			retryIdentity = "uuid:" + eventID.String()
		}
		if len(retryIdentity) > 512 || !utf8.ValidString(retryIdentity) || retryIdentity != strings.TrimSpace(retryIdentity) {
			return uuid.Nil, "", fmt.Errorf("%w: retry identity must be trimmed UTF-8 of at most 512 bytes", ErrInvalid)
		}
		return eventID, retryIdentity, nil
	}
	if retryIdentity == "" && strings.TrimSpace(input.RequestID) != "" {
		retryIdentity = "request:" + strings.TrimSpace(input.RequestID)
	}
	if retryIdentity == "" {
		return uuid.Nil, "", fmt.Errorf("%w: caller-supplied EventID or deterministic RetryIdentity is required", ErrInvalid)
	}
	if len(retryIdentity) > 512 || !utf8.ValidString(retryIdentity) || retryIdentity != strings.TrimSpace(retryIdentity) {
		return uuid.Nil, "", fmt.Errorf("%w: retry identity must be trimmed UTF-8 of at most 512 bytes", ErrInvalid)
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview/queryaudit/"+retryIdentity)), retryIdentity, nil
}

func parseUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: event id must be a non-zero UUID", ErrInvalid)
	}
	return parsed, nil
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func conflict(id, field string) error {
	return fmt.Errorf("%w: event %s conflicts on %s", ErrConflict, id, field)
}

func differingField(stored storedEvent, input queryaudit.EventInput) string {
	checks := []struct {
		name string
		a, b any
	}{
		{"project_id", stored.ProjectID, input.ProjectID.String()}, {"principal_id", stored.PrincipalID, input.PrincipalID}, {"surface", stored.Surface, input.Surface}, {"operation", stored.Operation, input.Operation}, {"query_kind", stored.QueryKind, input.QueryKind}, {"model_id", stored.ModelID, input.ModelID}, {"target", stored.Target, input.Target}, {"object_type", stored.ObjectType, input.ObjectType}, {"object_id", stored.ObjectID, input.ObjectID}, {"request_id", stored.RequestID, input.RequestID}, {"correlation_id", stored.CorrelationID, input.CorrelationID}, {"status", stored.Status, input.Status}, {"duration_ms", stored.DurationMS, input.DurationMS}, {"queue_wait_ms", stored.QueueWaitMS, input.QueueWaitMS}, {"planning_ms", stored.PlanningMS, input.PlanningMS}, {"connection_wait_ms", stored.ConnectionWaitMS, input.ConnectionWaitMS}, {"database_ms", stored.DatabaseMS, input.DatabaseMS}, {"execution_ms", stored.ExecutionMS, input.ExecutionMS}, {"execution_state", stored.ExecutionState, input.ExecutionState}, {"rows_returned", stored.RowsReturned, int64(input.RowsReturned)}, {"bytes_estimate", stored.BytesEstimate, input.BytesEstimate}, {"error", stored.Error, input.Error}, {"sql_text", stored.SQL, input.SQL}, {"plan_text", stored.PlanText, input.PlanText}, {"query_json", string(stored.QueryJSON), input.QueryJSON},
	}
	for _, check := range checks {
		if fmt.Sprint(check.a) != fmt.Sprint(check.b) {
			return check.name
		}
	}
	return ""
}

func projectValues(filter queryaudit.Filter) []string {
	values := make([]string, 0, len(filter.ProjectIDs)+1)
	for _, id := range filter.ProjectIDs {
		if id != "" {
			values = append(values, id.String())
		}
	}
	if len(values) == 0 && filter.ProjectID != "" {
		values = append(values, filter.ProjectID.String())
	}
	return uniqueStrings(values)
}

func stringValues(exact string, values []string) []string {
	if len(values) == 0 {
		if value := strings.TrimSpace(exact); value != "" {
			return []string{value}
		}
		return nil
	}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return uniqueStrings(cleaned)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func filterColumn(field string) (string, bool) {
	switch field {
	case "project":
		return "project_id", true
	case "principal":
		return "principal_id", true
	case "surface":
		return "surface", true
	case "kind":
		return "query_kind", true
	case "status":
		return "status", true
	default:
		return "", false
	}
}

func decodePageToken(token string) (string, string, error) {
	decodedBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", "", fmt.Errorf("%w: malformed page token", ErrInvalid)
	}
	parts := strings.SplitN(string(decodedBytes), "\x00", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("%w: malformed page token", ErrInvalid)
	}
	if _, err := parseTime(parts[0]); err != nil {
		return "", "", fmt.Errorf("%w: malformed page token time", ErrInvalid)
	}
	if _, err := parseUUID(parts[1]); err != nil {
		return "", "", fmt.Errorf("%w: malformed page token id", ErrInvalid)
	}
	return parts[0], parts[1], nil
}

func validateFilter(filter queryaudit.Filter) error {
	if len(filter.ProjectIDs) > MaxFilterValues || len(filter.PrincipalIDs) > MaxFilterValues || len(filter.Surfaces) > MaxFilterValues || len(filter.QueryKinds) > MaxFilterValues || len(filter.Statuses) > MaxFilterValues {
		return fmt.Errorf("%w: filter value count exceeds %d", ErrInvalid, MaxFilterValues)
	}
	if err := validateFilterText("principal", filter.PrincipalID, 255, true); err != nil {
		return err
	}
	if err := validateFilterText("surface", filter.Surface, 128, true); err != nil {
		return err
	}
	if err := validateFilterText("operation", filter.Operation, 256, true); err != nil {
		return err
	}
	if err := validateFilterText("query kind", filter.QueryKind, 128, true); err != nil {
		return err
	}
	if err := validateFilterText("model", filter.ModelID, 255, true); err != nil {
		return err
	}
	if err := validateFilterText("target", filter.Target, 512, true); err != nil {
		return err
	}
	if err := validateFilterTextList("principal", filter.PrincipalIDs, 255); err != nil {
		return err
	}
	if err := validateFilterTextList("surface", filter.Surfaces, 128); err != nil {
		return err
	}
	if err := validateFilterTextList("query kind", filter.QueryKinds, 128); err != nil {
		return err
	}
	if err := validateFilterTextList("status", filter.Statuses, 64); err != nil {
		return err
	}
	if filter.From != "" && len(filter.From) > 64 {
		return fmt.Errorf("%w: from time is too long", ErrInvalid)
	}
	if filter.To != "" && len(filter.To) > 64 {
		return fmt.Errorf("%w: to time is too long", ErrInvalid)
	}
	if filter.CursorTime != "" && len(filter.CursorTime) > 64 {
		return fmt.Errorf("%w: cursor time is too long", ErrInvalid)
	}
	if filter.CursorID != "" && len(filter.CursorID) > 64 {
		return fmt.Errorf("%w: cursor id is too long", ErrInvalid)
	}
	if filter.From != "" && filter.To != "" {
		from, err := parseTime(filter.From)
		if err != nil {
			return fmt.Errorf("%w: from time: %v", ErrInvalid, err)
		}
		to, err := parseTime(filter.To)
		if err != nil {
			return fmt.Errorf("%w: to time: %v", ErrInvalid, err)
		}
		if from.After(to) {
			return fmt.Errorf("%w: from time is after to time", ErrInvalid)
		}
	}
	return nil
}

func validateFilterText(name, value string, limit int, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || len(value) > limit {
		return fmt.Errorf("%w: %s filter value is empty, untrimmed, invalid UTF-8, or exceeds %d bytes", ErrInvalid, name, limit)
	}
	return nil
}

func validateFilterTextList(name string, values []string, limit int) error {
	for _, value := range values {
		if err := validateFilterText(name, value, limit, false); err != nil {
			return err
		}
	}
	return nil
}
