package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *Repository) RecordAuditEvent(ctx context.Context, input access.AuditEventInput) error {
	if strings.TrimSpace(input.Action) == "" {
		return errors.New("audit action is required")
	}
	metadata := strings.TrimSpace(input.MetadataJSON)
	if metadata == "" {
		metadata = "{}"
	}
	var metadataObject map[string]json.RawMessage
	if len(metadata) > maxAuditMetadataBytes || json.Unmarshal([]byte(metadata), &metadataObject) != nil || metadataObject == nil {
		return errors.New("audit metadata is invalid")
	}
	aggregateKey := strings.TrimSpace(input.ResourceKind)
	if resourceID := strings.TrimSpace(input.ResourceID); resourceID != "" {
		if aggregateKey == "" {
			aggregateKey = resourceID
		} else {
			aggregateKey += ":" + resourceID
		}
	}
	if aggregateKey == "" {
		aggregateKey = strings.TrimSpace(input.Action)
	}
	intentDigest := auditInputDigest(input, metadata)
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	auditIDString, err := newUUID()
	if err != nil {
		return err
	}
	auditID, err := pgUUID(auditIDString)
	if err != nil {
		return err
	}
	return accessdb.New(db).InsertAccessAuditEvent(ctx, accessdb.InsertAccessAuditEventParams{AuditID: auditID, PrincipalID: input.PrincipalID,
		Action: input.Action, ResourceKind: auditText(input.ResourceKind), ResourceID: auditText(input.ResourceID), Capability: input.Capability.String(), Status: input.Status,
		RequestID: input.RequestID, CorrelationID: input.CorrelationID, AggregateKey: aggregateKey, IntentDigest: intentDigest, Metadata: []byte(metadata)})
}

func auditText(value string) *string { return &value }

func auditInputDigest(input access.AuditEventInput, metadata string) string {
	payload, _ := json.Marshal(struct {
		PrincipalID, Action, ResourceKind, ResourceID, Capability, Status string
		RequestID, CorrelationID, MetadataJSON                            string
	}{
		PrincipalID: input.PrincipalID, Action: input.Action, ResourceKind: input.ResourceKind,
		ResourceID: input.ResourceID, Capability: input.Capability.String(), Status: input.Status,
		RequestID: input.RequestID, CorrelationID: input.CorrelationID, MetadataJSON: metadata,
	})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (r *Repository) ListAuditEvents(ctx context.Context, filter access.AuditEventFilter) ([]access.AuditEvent, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > maxAuditReadRows {
		limit = maxAuditReadRows
	}
	if filter.PageToken != "" && filter.CursorTime == "" && filter.CursorID == "" {
		if raw, decodeErr := base64.RawURLEncoding.DecodeString(filter.PageToken); decodeErr == nil {
			parts := strings.SplitN(string(raw), "\x00", 2)
			if len(parts) == 2 {
				filter.CursorTime, filter.CursorID = parts[0], parts[1]
			}
		}
	}
	var cursorID pgtype.UUID
	if strings.TrimSpace(filter.CursorID) != "" {
		cursorID, err = pgUUID(filter.CursorID)
		if err != nil {
			return nil, err
		}
	}
	rows, err := accessdb.New(db).ListAccessAuditEvents(ctx, accessdb.ListAccessAuditEventsParams{PrincipalID: filter.PrincipalID, Action: filter.Action, ResourceKind: filter.ResourceKind, ResourceID: filter.ResourceID,
		Capability: filter.Capability.String(), FromTime: filter.From, ToTime: filter.To, CursorTime: filter.CursorTime, CursorID: cursorID, PageSize: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]access.AuditEvent, 0, len(rows))
	for _, row := range rows {
		value := access.AuditEvent{ID: row.AuditID, PrincipalID: principalUUID(row.PrincipalID), Action: row.Action, ResourceKind: row.ResourceKind, ResourceID: row.ResourceID,
			Capability: access.Capability(row.Capability), Status: row.Outcome, RequestID: principalUUID(row.RequestID), CorrelationID: principalUUID(row.CorrelationID), MetadataJSON: row.MetadataJson, CreatedAt: principalTimestamp(row.OccurredAt)}
		out = append(out, value)
	}
	return out, nil
}

func (r *Repository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	return r.RunAuditedMutationBatch(ctx, func(repo access.Repository) ([]access.AuditEventInput, error) {
		value, err := mutation(repo)
		if err != nil {
			return nil, err
		}
		return []access.AuditEventInput{value}, nil
	})
}

func (r *Repository) RunAuditedMutationBatch(ctx context.Context, mutation func(access.Repository) ([]access.AuditEventInput, error)) error {
	if mutation == nil {
		return errors.New("audited mutation is required")
	}
	tx, err := r.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("%w: begin: %v", access.ErrAuditTransaction, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	transactional := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	inputs, err := mutation(transactional)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return errors.New("audited mutation requires at least one audit event")
	}
	for _, input := range inputs {
		if err := transactional.RecordAuditEvent(ctx, input); err != nil {
			return fmt.Errorf("%w: record event: %v", access.ErrAuditTransaction, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: commit: %v", access.ErrAuditTransaction, err)
	}
	return nil
}

func (r *Repository) BootstrapAPITokenEvidence(ctx context.Context, principalID, tokenID string, now time.Time) (access.APIToken, error) {
	pid, err := uuidID("principal id", principalID)
	if err != nil {
		return access.APIToken{}, err
	}
	tid, err := uuidID("api token id", tokenID)
	if err != nil {
		return access.APIToken{}, err
	}
	db, err := r.requireDB()
	if err != nil {
		return access.APIToken{}, err
	}
	parsedPrincipalID, err := pgUUID(pid)
	if err != nil {
		return access.APIToken{}, err
	}
	parsedTokenID, err := pgUUID(tid)
	if err != nil {
		return access.APIToken{}, err
	}
	exists, err := accessdb.New(db).HasBootstrapAPITokenEvidence(ctx, accessdb.HasBootstrapAPITokenEvidenceParams{TokenID: parsedTokenID, PrincipalID: parsedPrincipalID})
	if err != nil {
		return access.APIToken{}, err
	}
	if !exists {
		return access.APIToken{}, pgx.ErrNoRows
	}
	return r.apiToken(ctx, tid)
}
