package sqlite

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
)

func (r *Repository) RecordAuditEvent(ctx context.Context, input access.AuditEventInput) error {
	if strings.TrimSpace(input.Action) == "" {
		return fmt.Errorf("audit action is required")
	}
	if strings.TrimSpace(input.MetadataJSON) == "" {
		input.MetadataJSON = "{}"
	}
	id, err := newID("audit")
	if err != nil {
		return err
	}
	return r.q.InsertAuditEvent(ctx, platformdb.InsertAuditEventParams{
		ID: id, PrincipalID: nullableString(input.PrincipalID),
		Action: input.Action, ResourceKind: input.ResourceKind, ResourceID: input.ResourceID, Capability: string(input.Capability),
		Status: input.Status, RequestID: input.RequestID, CorrelationID: input.CorrelationID, MetadataJson: input.MetadataJSON,
	})
}

func (r *Repository) ListAuditEvents(ctx context.Context, filter access.AuditEventFilter) ([]access.AuditEvent, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if filter.PageToken != "" && filter.CursorTime == "" && filter.CursorID == "" {
		filter.CursorTime, filter.CursorID = decodeAuditPageToken(filter.PageToken)
	}
	from, to, cursorTime := sqliteAuditTime(filter.From), sqliteAuditTime(filter.To), sqliteAuditTime(filter.CursorTime)
	rows, err := r.q.ListAuditEvents(ctx, platformdb.ListAuditEventsParams{
		Column1: filter.PrincipalID, PrincipalID: nullableString(filter.PrincipalID),
		Column3: filter.Action, Action: filter.Action, Column5: filter.ResourceKind, ResourceKind: filter.ResourceKind,
		Column7: filter.ResourceID, ResourceID: filter.ResourceID, Column9: string(filter.Capability), Capability: string(filter.Capability), Column11: from, CreatedAt: from, Column13: to, CreatedAt_2: to,
		Column15: cursorTime, CreatedAt_3: cursorTime, CreatedAt_4: cursorTime, ID: filter.CursorID, Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	events := make([]access.AuditEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, access.AuditEvent{
			ID: row.ID, PrincipalID: row.PrincipalID.String, Action: row.Action,
			ResourceKind: row.ResourceKind, ResourceID: row.ResourceID, Capability: access.Capability(row.Capability), Status: row.Status,
			RequestID: row.RequestID, CorrelationID: row.CorrelationID, MetadataJSON: row.MetadataJson, CreatedAt: row.CreatedAt,
		})
	}
	return events, nil
}

func auditPageToken(createdAt, id string) string {
	if createdAt == "" || id == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + id))
}

func sqliteAuditTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format("2006-01-02 15:04:05")
		}
	}
	return value
}

func decodeAuditPageToken(token string) (string, string) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", ""
	}
	createdAt, id, ok := strings.Cut(string(raw), "\x00")
	if !ok {
		return "", ""
	}
	return createdAt, id
}
