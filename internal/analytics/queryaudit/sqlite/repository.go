package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	db "github.com/flidai/leapview/internal/analytics/internal/db"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Repository struct {
	q *db.Queries
}

func NewRepository(sqlDB *sql.DB) *Repository {
	return &Repository{q: db.New(sqlDB)}
}

func (r *Repository) RecordQueryEvent(ctx context.Context, input queryaudit.EventInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(input.QueryJSON) == "" {
		input.QueryJSON = "{}"
	}
	input.SQL = queryaudit.RedactSensitiveText(input.SQL)
	input.PlanText = queryaudit.RedactSensitiveText(input.PlanText)
	input.QueryJSON = queryaudit.RedactSensitiveText(input.QueryJSON)
	return r.q.InsertQueryEvent(ctx, db.InsertQueryEventParams{
		ID:               newID("queryevent"),
		ProjectID:        input.ProjectID.String(),
		PrincipalID:      input.PrincipalID,
		Surface:          input.Surface,
		Operation:        input.Operation,
		QueryKind:        input.QueryKind,
		ModelID:          input.ModelID,
		Target:           input.Target,
		ObjectType:       input.ObjectType,
		ObjectID:         input.ObjectID,
		RequestID:        input.RequestID,
		CorrelationID:    input.CorrelationID,
		Status:           input.Status,
		DurationMs:       input.DurationMS,
		QueueWaitMs:      input.QueueWaitMS,
		PlanningMs:       input.PlanningMS,
		ConnectionWaitMs: input.ConnectionWaitMS,
		DatabaseMs:       input.DatabaseMS,
		ExecutionMs:      input.ExecutionMS,
		ExecutionState:   input.ExecutionState,
		RowsReturned:     int64(input.RowsReturned),
		BytesEstimate:    input.BytesEstimate,
		Error:            input.Error,
		SqlText:          input.SQL,
		PlanText:         input.PlanText,
		QueryJson:        input.QueryJSON,
	})
}

func (r *Repository) GetQueryEvent(ctx context.Context, id string) (queryaudit.Event, error) {
	row, err := r.q.GetQueryEvent(ctx, strings.TrimSpace(id))
	if err != nil {
		return queryaudit.Event{}, err
	}
	return queryEventFromDB(row)
}

func (r *Repository) ListQueryEvents(ctx context.Context, filter queryaudit.Filter) ([]queryaudit.Event, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if filter.PageToken != "" && filter.CursorTime == "" && filter.CursorID == "" {
		filter.CursorTime, filter.CursorID = decodePageToken(filter.PageToken)
	}
	rows, err := r.q.ListQueryEvents(ctx, db.ListQueryEventsParams{
		ProjectIdsJson:   jsonFilterValues(filter.ProjectID.String(), resourceIDStrings(filter.ProjectIDs)),
		PrincipalIdsJson: jsonFilterValues(filter.PrincipalID, filter.PrincipalIDs),
		SurfacesJson:     jsonFilterValues(filter.Surface, filter.Surfaces),
		Operation:        strings.TrimSpace(filter.Operation),
		QueryKindsJson:   jsonFilterValues(filter.QueryKind, filter.QueryKinds),
		ModelID:          strings.TrimSpace(filter.ModelID), Target: strings.TrimSpace(filter.Target),
		StatusesJson: jsonFilterValues(filter.Status, filter.Statuses),
		FromTime:     sqliteTime(filter.From), ToTime: sqliteTime(filter.To), Search: strings.TrimSpace(filter.Search),
		CursorTime: sqliteTime(filter.CursorTime), CursorID: filter.CursorID, Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	events := make([]queryaudit.Event, 0, len(rows))
	for _, row := range rows {
		event, err := queryEventFromDB(row)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (r *Repository) ListQueryEventFilterOptions(ctx context.Context, field, search string, limit int) ([]queryaudit.FilterOption, error) {
	field = strings.TrimSpace(field)
	_, ok := queryEventFilterOptionColumn(field)
	if !ok {
		return nil, fmt.Errorf("unsupported query event filter option field %q", field)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := r.q.ListQueryEventFilterOptions(ctx, db.ListQueryEventFilterOptionsParams{
		Field: field, Search: strings.TrimSpace(search), Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	options := make([]queryaudit.FilterOption, 0, len(rows))
	for _, row := range rows {
		options = append(options, queryaudit.FilterOption{Value: row.Value, Count: int(row.Count)})
	}
	return options, nil
}

func queryEventFilterOptionColumn(field string) (string, bool) {
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

func cleanValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func jsonFilterValues(exact string, values []string) string {
	values = cleanValues(values)
	if len(values) == 0 {
		values = cleanValues([]string{exact})
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func resourceIDStrings(ids []projectgraph.ResourceID) []string {
	if len(ids) == 0 {
		return nil
	}
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, id.String())
	}
	return values
}

func queryEventFromDB(row db.QueryEvent) (queryaudit.Event, error) {
	projectID, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return queryaudit.Event{}, fmt.Errorf("query event %q has invalid project id: %w", row.ID, err)
	}
	return queryaudit.Event{
		ID: row.ID,
		EventInput: queryaudit.EventInput{
			ProjectID:        projectID,
			PrincipalID:      row.PrincipalID,
			Surface:          row.Surface,
			Operation:        row.Operation,
			QueryKind:        row.QueryKind,
			ModelID:          row.ModelID,
			Target:           row.Target,
			ObjectType:       row.ObjectType,
			ObjectID:         row.ObjectID,
			RequestID:        row.RequestID,
			CorrelationID:    row.CorrelationID,
			Status:           row.Status,
			DurationMS:       row.DurationMs,
			QueueWaitMS:      row.QueueWaitMs,
			PlanningMS:       row.PlanningMs,
			ConnectionWaitMS: row.ConnectionWaitMs,
			DatabaseMS:       row.DatabaseMs,
			ExecutionMS:      row.ExecutionMs,
			ExecutionState:   row.ExecutionState,
			RowsReturned:     int(row.RowsReturned),
			BytesEstimate:    row.BytesEstimate,
			Error:            row.Error,
			SQL:              row.SqlText,
			PlanText:         row.PlanText,
			QueryJSON:        row.QueryJson,
		},
		CreatedAt: row.CreatedAt,
	}, nil
}

func newID(prefix string) string {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return prefix + "_" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return prefix + "_" + hex.EncodeToString(bytes[:])
}

func decodePageToken(token string) (string, string) {
	bytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(string(bytes), "\x00", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func sqliteTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC().Format("2006-01-02 15:04:05")
		}
	}
	return value
}
