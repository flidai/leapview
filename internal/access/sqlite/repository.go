package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	platformdb "github.com/flidai/leapview/internal/access/internal/db"
)

type Repository struct {
	root                *sql.DB
	db                  sqlExecutor
	q                   *platformdb.Queries
	auditOutboxCapacity int
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const (
	defaultAPITokenTTL               = 90 * 24 * time.Hour
	defaultServicePrincipalSecretTTL = 180 * 24 * time.Hour
)

var secretRandomReader io.Reader = rand.Reader

func NewRepository(sqlDB *sql.DB) *Repository {
	return &Repository{
		root: sqlDB, db: sqlDB, q: platformdb.New(sqlDB), auditOutboxCapacity: access.MaxUndeliveredAuditIntents,
	}
}

// Initialize reconciles access-owned system roles and the platform securable.
// It is intentionally capability-owned rather than part of opening the shared
// SQLite connection.
func Initialize(ctx context.Context, sqlDB *sql.DB) error {
	// Canonical project-role bundles and platform role templates are seeded by
	// immutable migrations. No runtime role expansion or securable bootstrap is
	// permitted here.
	return nil
}

// InsertPlatformSettingIfMissing participates in the repository's current
// transaction and is used for one-shot instance initialization markers.
func (r *Repository) InsertPlatformSettingIfMissing(ctx context.Context, key, value string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `INSERT INTO platform_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, key, value)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// ListGroupIDsForPrincipal returns the indexed group memberships for one
// principal. Authorization callers need only the IDs, so this deliberately
// avoids loading every group and its members.
func (r *Repository) ListGroupIDsForPrincipal(ctx context.Context, principalID string) ([]string, error) {
	validated, err := access.NewSubjectRef(access.SubjectKindPrincipal, principalID)
	if err != nil {
		return nil, err
	}
	return r.q.ListGroupIDsForPrincipal(ctx, validated.ID)
}

func (r *Repository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	return r.RunAuditedMutationBatch(ctx, func(repo access.Repository) ([]access.AuditEventInput, error) {
		input, err := mutation(repo)
		return []access.AuditEventInput{input}, err
	})
}

func (r *Repository) RunAuditedMutationBatch(ctx context.Context, mutation func(access.Repository) ([]access.AuditEventInput, error)) error {
	if r == nil || r.root == nil {
		return fmt.Errorf("access repository database is required")
	}
	if mutation == nil {
		return fmt.Errorf("audited mutation is required")
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin: %v", access.ErrAuditTransaction, err)
	}
	defer func() { _ = tx.Rollback() }()

	txRepo := &Repository{root: r.root, db: tx, q: r.q.WithTx(tx)}
	inputs, err := mutation(txRepo)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return fmt.Errorf("audited mutation requires at least one audit event")
	}
	for _, input := range inputs {
		if err := txRepo.RecordAuditEvent(ctx, input); err != nil {
			return fmt.Errorf("%w: record event: %v", access.ErrAuditTransaction, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit: %v", access.ErrAuditTransaction, err)
	}
	return nil
}

func mapPrincipal(row platformdb.Principal) access.Principal {
	return access.Principal{
		ID:          row.ID,
		Kind:        access.PrincipalKind(row.Kind),
		Email:       row.Email,
		DisplayName: row.DisplayName,
		DisabledAt:  nullString(row.DisabledAt),
		BlockedAt:   nullString(row.BlockedAt),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func mapGroup(row platformdb.Group) access.Group {
	return access.Group{
		ID:         row.ID,
		Provider:   row.Provider,
		ExternalID: row.ExternalID,
		Name:       row.Name,
		CreatedAt:  row.CreatedAt,
	}
}

func mapSession(row platformdb.Session) access.Session {
	return access.Session{
		ID:          row.ID,
		PrincipalID: row.PrincipalID,
		Kind:        access.SessionKindBrowser,
		ExpiresAt:   row.ExpiresAt,
		CreatedAt:   row.CreatedAt,
		LastSeenAt:  row.LastSeenAt,
		RevokedAt:   nullString(row.RevokedAt),
	}
}

func mapListedSession(row platformdb.ListSessionsByPrincipalRow) access.Session {
	kind := access.SessionKindBrowser
	if row.DesktopClientID != "" {
		kind = access.SessionKindDesktop
	}
	return access.Session{
		ID:                row.ID,
		PrincipalID:       row.PrincipalID,
		Kind:              kind,
		InstanceID:        row.DesktopInstanceID,
		ProfileID:         row.DesktopProfileID,
		ClientID:          row.DesktopClientID,
		ExpiresAt:         row.ExpiresAt,
		AbsoluteExpiresAt: row.DesktopAbsoluteExpiresAt,
		CreatedAt:         row.CreatedAt,
		LastSeenAt:        row.LastSeenAt,
		RevokedAt:         nullString(row.RevokedAt),
	}
}

func mapAPIToken(row platformdb.ApiToken) access.APIToken {
	capabilities := decodeTokenCapabilities(row.CapabilitiesJson)
	return access.APIToken{
		ID:           row.ID,
		PrincipalID:  row.PrincipalID,
		Name:         row.Name,
		Capabilities: capabilities,
		ExpiresAt:    nullString(row.ExpiresAt),
		CreatedAt:    row.CreatedAt,
		LastUsedAt:   nullString(row.LastUsedAt),
		RevokedAt:    nullString(row.RevokedAt),
	}
}

// decodeTokenCapabilities fails closed for malformed persisted data. A nil
// SQL value (the dynamic form) remains nil; malformed or invalid values become
// a non-nil empty allowlist, which cannot authorize any capability.
func decodeTokenCapabilities(value sql.NullString) []access.Capability {
	if !value.Valid {
		return nil
	}
	if strings.TrimSpace(value.String) == "" || strings.TrimSpace(value.String) == "null" {
		return []access.Capability{}
	}
	var raw []string
	if err := json.Unmarshal([]byte(value.String), &raw); err != nil {
		return []access.Capability{}
	}
	capabilities := make([]access.Capability, 0, len(raw))
	seen := make(map[access.Capability]struct{}, len(raw))
	for _, item := range raw {
		capability, err := access.ParseCapability(item)
		if err != nil {
			return []access.Capability{}
		}
		if _, duplicate := seen[capability]; duplicate {
			return []access.Capability{}
		}
		seen[capability] = struct{}{}
		capabilities = append(capabilities, capability)
	}
	return capabilities
}

func nullString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newID(prefix string) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	return prefix + "_" + secret[:24], nil
}

func newSecret() (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(secretRandomReader, b[:]); err != nil {
		return "", fmt.Errorf("read secure random bytes: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func newTemporaryPassword() (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	return secret[:24], nil
}

func stableID(value string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(value)))
	return hex.EncodeToString(sum[:])[:32]
}
