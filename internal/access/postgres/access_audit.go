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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	_, err = db.Exec(ctx, `INSERT INTO audit.audit_event(audit_id,principal_id,source,operation,action,resource_kind,resource_id,capability,outcome,request_id,correlation_id,aggregate_key,aggregate_sequence,intent_digest,metadata) VALUES($1::uuid,NULLIF($2,'')::uuid,'access','repository',$3,$4,$5,$6,CASE WHEN $7='' THEN 'success' ELSE $7 END,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,$10,0,$11,$12::jsonb)`, uuid.New(), input.PrincipalID, input.Action, input.ResourceKind, input.ResourceID, input.Capability.String(), input.Status, input.RequestID, input.CorrelationID, aggregateKey, intentDigest, metadata)
	return err
}

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
	args := []any{filter.PrincipalID, filter.Action, filter.ResourceKind, filter.ResourceID, filter.Capability.String(), filter.From, filter.To, filter.CursorTime, filter.CursorID, limit}
	rows, err := db.Query(ctx, `SELECT audit_id::text,COALESCE(principal_id::text,''),action,COALESCE(resource_kind,''),COALESCE(resource_id,''),capability,outcome,COALESCE(request_id::text,''),COALESCE(correlation_id::text,''),metadata::text,occurred_at FROM audit.audit_event WHERE ($1='' OR principal_id=$1::uuid) AND ($2='' OR action=$2) AND ($3='' OR resource_kind=$3) AND ($4='' OR resource_id=$4) AND ($5='' OR capability=$5) AND ($6='' OR occurred_at >= $6::timestamptz) AND ($7='' OR occurred_at < $7::timestamptz) AND ($8='' OR (occurred_at,audit_id) < ($8::timestamptz,$9::uuid)) ORDER BY occurred_at DESC,audit_id DESC LIMIT $10`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.AuditEvent, 0)
	for rows.Next() {
		var value access.AuditEvent
		var created time.Time
		if err := rows.Scan(&value.ID, &value.PrincipalID, &value.Action, &value.ResourceKind, &value.ResourceID, &value.Capability, &value.Status, &value.RequestID, &value.CorrelationID, &value.MetadataJSON, &created); err != nil {
			return nil, err
		}
		value.CreatedAt = formatTime(created)
		out = append(out, value)
	}
	return out, rows.Err()
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
	var exists bool
	if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.api_token t JOIN access.principal p ON p.id=t.principal_id JOIN access.platform_role_binding b ON b.principal_id=p.id WHERE t.id=$1::uuid AND t.principal_id=$2::uuid AND t.revoked_at IS NULL AND t.expires_at>clock_timestamp() AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL AND b.role='platform_admin' AND b.revoked_at IS NULL)`, tid, pid).Scan(&exists); err != nil {
		return access.APIToken{}, err
	}
	if !exists {
		return access.APIToken{}, pgx.ErrNoRows
	}
	return r.apiToken(ctx, tid)
}
