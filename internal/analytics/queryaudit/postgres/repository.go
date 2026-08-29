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
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
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
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

type Repository struct{ db DBTX }

var _ queryaudit.Repository = (*Repository)(nil)

func New(db DBTX) *Repository           { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository { return New(db) }

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

const selectEvent = `SELECT event_id,retry_identity,project_id,principal_id,surface,operation,query_kind,model_id,target,object_type,object_id,request_id,correlation_id,status,duration_ms,queue_wait_ms,planning_ms,connection_wait_ms,database_ms,execution_ms,execution_state,rows_returned,bytes_estimate,error,sql_text,plan_text,query_json,created_at FROM audit.query_event`

type rowScanner interface{ Scan(...any) error }

func scanStored(row rowScanner) (storedEvent, error) {
	var event storedEvent
	err := row.Scan(&event.ID, &event.RetryIdentity, &event.ProjectID, &event.PrincipalID, &event.Surface, &event.Operation, &event.QueryKind, &event.ModelID, &event.Target, &event.ObjectType, &event.ObjectID, &event.RequestID, &event.CorrelationID, &event.Status, &event.DurationMS, &event.QueueWaitMS, &event.PlanningMS, &event.ConnectionWaitMS, &event.DatabaseMS, &event.ExecutionMS, &event.ExecutionState, &event.RowsReturned, &event.BytesEstimate, &event.Error, &event.SQL, &event.PlanText, &event.QueryJSON, &event.CreatedAt)
	if err == nil {
		var document any
		if decodeErr := json.Unmarshal(event.QueryJSON, &document); decodeErr != nil {
			return storedEvent{}, fmt.Errorf("%w: persisted query_json: %v", ErrInvalid, decodeErr)
		}
		canonical, marshalErr := json.Marshal(document)
		if marshalErr != nil {
			return storedEvent{}, fmt.Errorf("%w: persisted query_json: %v", ErrInvalid, marshalErr)
		}
		event.QueryJSON = canonical
	}
	return event, err
}

func (r *Repository) getStored(ctx context.Context, id uuid.UUID) (storedEvent, error) {
	return scanStored(r.db.QueryRow(ctx, selectEvent+` WHERE event_id = $1`, id))
}

func (r *Repository) findStored(ctx context.Context, id uuid.UUID, retry string) (storedEvent, error) {
	stored, err := scanStored(r.db.QueryRow(ctx, selectEvent+` WHERE event_id = $1 OR retry_identity = $2 ORDER BY (event_id = $1) DESC LIMIT 1`, id, retry))
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
	var returned uuid.UUID
	err = r.db.QueryRow(ctx, `
INSERT INTO audit.query_event (
 event_id,retry_identity,project_id,principal_id,surface,operation,query_kind,model_id,target,object_type,object_id,request_id,correlation_id,status,
 duration_ms,queue_wait_ms,planning_ms,connection_wait_ms,database_ms,execution_ms,execution_state,rows_returned,bytes_estimate,error,sql_text,plan_text,query_json)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27::jsonb)
ON CONFLICT DO NOTHING RETURNING event_id`,
		eventID, retry, normalized.ProjectID.String(), normalized.PrincipalID, normalized.Surface, normalized.Operation, normalized.QueryKind, normalized.ModelID, normalized.Target, normalized.ObjectType, normalized.ObjectID, normalized.RequestID, normalized.CorrelationID, normalized.Status,
		normalized.DurationMS, normalized.QueueWaitMS, normalized.PlanningMS, normalized.ConnectionWaitMS, normalized.DatabaseMS, normalized.ExecutionMS, normalized.ExecutionState, normalized.RowsReturned, normalized.BytesEstimate, normalized.Error, normalized.SQL, normalized.PlanText, normalized.QueryJSON).Scan(&returned)
	if err == nil {
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
	where := make([]string, 0, 16)
	args := make([]any, 0, 16)
	arg := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
	if values := projectValues(filter); len(values) > 0 {
		where = append(where, "project_id = ANY("+arg(values)+"::text[])")
	}
	if values := stringValues(filter.PrincipalID, filter.PrincipalIDs); len(values) > 0 {
		where = append(where, "principal_id = ANY("+arg(values)+"::text[])")
	}
	if values := stringValues(filter.Surface, filter.Surfaces); len(values) > 0 {
		where = append(where, "surface = ANY("+arg(values)+"::text[])")
	}
	if value := strings.TrimSpace(filter.Operation); value != "" {
		where = append(where, "operation = "+arg(value))
	}
	if values := stringValues(filter.QueryKind, filter.QueryKinds); len(values) > 0 {
		where = append(where, "query_kind = ANY("+arg(values)+"::text[])")
	}
	if value := strings.TrimSpace(filter.ModelID); value != "" {
		where = append(where, "model_id = "+arg(value))
	}
	if value := strings.TrimSpace(filter.Target); value != "" {
		where = append(where, "target = "+arg(value))
	}
	if values := stringValues(filter.Status, filter.Statuses); len(values) > 0 {
		where = append(where, "status = ANY("+arg(values)+"::text[])")
	}
	if value := strings.TrimSpace(filter.Search); value != "" {
		searchArg := arg(value)
		where = append(where, "search_document @@ websearch_to_tsquery('simple', "+searchArg+")")
	}
	if value := strings.TrimSpace(filter.From); value != "" {
		parsed, parseErr := parseTime(value)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: from time: %v", ErrInvalid, parseErr)
		}
		where = append(where, "created_at >= "+arg(parsed))
	}
	if value := strings.TrimSpace(filter.To); value != "" {
		parsed, parseErr := parseTime(value)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: to time: %v", ErrInvalid, parseErr)
		}
		where = append(where, "created_at <= "+arg(parsed))
	}
	if cursorTime := strings.TrimSpace(filter.CursorTime); cursorTime != "" {
		parsed, parseErr := parseTime(cursorTime)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: cursor time: %v", ErrInvalid, parseErr)
		}
		cursorID, parseErr := parseUUID(filter.CursorID)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: cursor id: %v", ErrInvalid, parseErr)
		}
		timeArg := arg(parsed)
		idArg := arg(cursorID)
		where = append(where, "(created_at < "+timeArg+" OR (created_at = "+timeArg+" AND event_id < "+idArg+"))")
	}
	query := selectEvent
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC,event_id DESC LIMIT " + arg(limit)
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]queryaudit.Event, 0, limit)
	for rows.Next() {
		stored, scanErr := scanStored(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		event, convertErr := toEvent(stored)
		if convertErr != nil {
			return nil, convertErr
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
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
	column, ok := filterColumn(strings.TrimSpace(field))
	if !ok {
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
	rows, err := r.db.Query(ctx, `SELECT `+column+` AS value,count(*) AS count FROM audit.query_event WHERE `+column+` <> '' AND ($1 = '' OR `+column+` ILIKE '%' || $1 || '%') GROUP BY `+column+` ORDER BY count DESC,value ASC LIMIT $2`, strings.TrimSpace(search), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := make([]queryaudit.FilterOption, 0, limit)
	for rows.Next() {
		var value string
		var count int64
		if err := rows.Scan(&value, &count); err != nil {
			return nil, err
		}
		options = append(options, queryaudit.FilterOption{Value: value, Count: int(count)})
	}
	return options, rows.Err()
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
