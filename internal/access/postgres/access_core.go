package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	maxSessionTTL = 30 * 24 * time.Hour
	maxPageSize   = 1000
)

var verifierParams = &argon2id.Params{Memory: 19 * 1024, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func (r *Repository) requireDB() (DBTX, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("access PostgreSQL database is required")
	}
	return r.db, nil
}

func (r *Repository) beginTx(ctx context.Context) (pgx.Tx, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	b, ok := db.(beginner)
	if !ok {
		return nil, errors.New("access PostgreSQL connection must support transactions")
	}
	return b.Begin(ctx)
}

// txOrBegin returns the caller-owned transaction when the repository was
// composed inside RunAuditedMutation.  The bool reports whether this method
// owns the transaction and therefore may commit it.
func (r *Repository) txOrBegin(ctx context.Context) (pgx.Tx, bool, error) {
	if tx, ok := r.db.(pgx.Tx); ok {
		return tx, false, nil
	}
	tx, err := r.beginTx(ctx)
	return tx, true, err
}

func newUUID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate UUIDv7 identity: %w", err)
	}
	return id.String(), nil
}

func uuidID(label, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	id, err := uuid.Parse(v)
	if err != nil {
		return "", fmt.Errorf("%s must be a UUID: %w", label, err)
	}
	return id.String(), nil
}

func bounded(value, label string, max int) (string, error) {
	if value != strings.TrimSpace(value) || value == "" || len(value) > max || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("%s is invalid", label)
	}
	return value, nil
}

func tokenSecret(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func (r *Repository) secretFingerprint(value string) []byte {
	m := hmac.New(sha256.New, r.fingerprintKey)
	_, _ = m.Write([]byte(value))
	return m.Sum(nil)
}

func secretVerifier(value string) ([]byte, error) {
	h, err := argon2id.CreateHash(value, verifierParams)
	if err != nil {
		return nil, err
	}
	return []byte(h), nil
}
func verifySecret(value string, verifier []byte) bool {
	ok, err := argon2id.ComparePasswordAndHash(value, string(verifier))
	return err == nil && ok
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func scanPrincipal(row pgx.Row) (access.Principal, error) {
	var p access.Principal
	var kind, status string
	var email, display string
	var disabled, blocked, last, created, updated *time.Time
	err := row.Scan(&p.ID, &kind, &status, &email, &display, &disabled, &blocked, &last, &created, &updated)
	if err != nil {
		return access.Principal{}, err
	}
	p.Kind = access.PrincipalKind(kind)
	if kind == "service" {
		p.Kind = access.PrincipalKindServicePrincipal
	} else if kind == "dashboard_publication" {
		p.Kind = access.PrincipalKindDashboardPublication
	}
	p.Email = email
	p.DisplayName = display
	p.DisabledAt = formatTimePtr(disabled)
	p.BlockedAt = formatTimePtr(blocked)
	p.LastSeenAt = formatTimePtr(last)
	p.CreatedAt = formatTimePtr(created)
	p.UpdatedAt = formatTimePtr(updated)
	return p, nil
}

func principalFromGenerated(row accessdb.GetPrincipalRow) access.Principal {
	p := access.Principal{
		ID:          principalUUID(row.ID),
		Email:       row.Email,
		DisplayName: row.DisplayName,
		Kind:        access.PrincipalKind(row.PrincipalType),
		DisabledAt:  principalTimestamp(row.DisabledAt),
		BlockedAt:   principalTimestamp(row.BlockedAt),
		LastSeenAt:  principalTimestamp(row.LastSeenAt),
		CreatedAt:   principalTimestamp(row.CreatedAt),
		UpdatedAt:   principalTimestamp(row.UpdatedAt),
	}
	if row.PrincipalType == "service" {
		p.Kind = access.PrincipalKindServicePrincipal
	} else if row.PrincipalType == "dashboard_publication" {
		p.Kind = access.PrincipalKindDashboardPublication
	}
	return p
}

func principalUUID(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return uuid.UUID(value.Bytes).String()
}

func principalTimestamp(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return formatTime(value.Time)
}

func pgUUID(value string) (pgtype.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return pgtype.UUID{Bytes: id, Valid: true}, nil
}

func pgTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func pgInterval(value time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: int64(value / time.Microsecond), Valid: true}
}

func principalFromListGenerated(row accessdb.ListPrincipalsRow) access.Principal {
	return principalFromGenerated(accessdb.GetPrincipalRow{
		ID: row.ID, PrincipalType: row.PrincipalType, Status: row.Status,
		Email: row.Email, DisplayName: row.DisplayName, DisabledAt: row.DisabledAt,
		BlockedAt: row.BlockedAt, LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	})
}

func formatTimePtr(v *time.Time) string {
	if v == nil {
		return ""
	}
	return formatTime(*v)
}

func (r *Repository) PrincipalByID(ctx context.Context, id string) (access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Principal{}, err
	}
	id, err = uuidID("principal id", id)
	if err != nil {
		return access.Principal{}, err
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return access.Principal{}, err
	}
	row, err := accessdb.New(db).GetPrincipal(ctx, pgtype.UUID{Bytes: parsed, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return access.Principal{}, pgx.ErrNoRows
	}
	if err != nil {
		return access.Principal{}, err
	}
	return principalFromGenerated(row), nil
}

func (r *Repository) ListPrincipals(ctx context.Context, filter access.PrincipalFilter) ([]access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	email, query := strings.TrimSpace(filter.Email), strings.TrimSpace(filter.Query)
	rows, err := accessdb.New(db).ListPrincipals(ctx, accessdb.ListPrincipalsParams{Email: email, Query: query, PageSize: maxPageSize})
	if err != nil {
		return nil, err
	}
	out := make([]access.Principal, 0, len(rows))
	for _, row := range rows {
		out = append(out, principalFromListGenerated(row))
	}
	return out, nil
}

func (r *Repository) SearchPrincipals(ctx context.Context, query string, limit int) ([]access.Principal, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []access.Principal{}, nil
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).SearchPrincipals(ctx, accessdb.SearchPrincipalsParams{Query: query, PageSize: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]access.Principal, 0, len(rows))
	for _, row := range rows {
		out = append(out, principalFromGenerated(accessdb.GetPrincipalRow{
			ID: row.ID, PrincipalType: row.PrincipalType, Status: row.Status,
			Email: row.Email, DisplayName: row.DisplayName, DisabledAt: row.DisabledAt,
			BlockedAt: row.BlockedAt, LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		}))
	}
	return out, nil
}

func (r *Repository) UpsertPrincipal(ctx context.Context, input access.PrincipalInput) (access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Principal{}, err
	}
	kind := input.Kind
	if kind == "" {
		kind = access.PrincipalKindUser
	}
	if kind != access.PrincipalKindUser && kind != access.PrincipalKindServicePrincipal && kind != access.PrincipalKindDashboardPublication {
		return access.Principal{}, fmt.Errorf("invalid principal kind")
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id, err = newUUID()
	} else {
		id, err = uuidID("principal id", id)
	}
	if err != nil {
		return access.Principal{}, err
	}
	email := access.NormalizeEmail(input.Email)
	display := strings.TrimSpace(input.DisplayName)
	if len(email) > 320 || len(display) > 512 {
		return access.Principal{}, fmt.Errorf("principal fields exceed bounds")
	}
	sqlKind := string(kind)
	if kind == access.PrincipalKindServicePrincipal {
		sqlKind = "service"
	}
	if kind == access.PrincipalKindDashboardPublication {
		sqlKind = "dashboard_publication"
	}
	if existing, lookupErr := r.PrincipalByID(ctx, id); lookupErr == nil && existing.Kind != kind {
		return access.Principal{}, fmt.Errorf("principal kind is immutable")
	}
	parsedID, err := pgUUID(id)
	if err != nil {
		return access.Principal{}, err
	}
	tag, err := accessdb.New(db).UpsertPrincipal(ctx, accessdb.UpsertPrincipalParams{ID: parsedID, Kind: sqlKind, Email: email, DisplayName: display})
	if err != nil {
		return access.Principal{}, err
	}
	if tag.RowsAffected() == 0 {
		existingType, lookupErr := accessdb.New(db).PrincipalKind(ctx, parsedID)
		if lookupErr != nil {
			return access.Principal{}, lookupErr
		}
		if existingType != sqlKind {
			return access.Principal{}, fmt.Errorf("principal kind is immutable")
		}
		return access.Principal{}, fmt.Errorf("principal is revoked")
	}
	return r.PrincipalByID(ctx, id)
}

func (r *Repository) SetPlatformRole(ctx context.Context, input access.PlatformRoleInput) (access.Principal, error) {
	role, err := access.ParsePlatformRole(string(input.Role))
	if err != nil {
		return access.Principal{}, err
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.Principal{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	txRepo := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	p, err := txRepo.UpsertPrincipal(ctx, access.PrincipalInput{ID: input.PrincipalID, Email: input.Email, DisplayName: input.DisplayName, Kind: access.PrincipalKindUser})
	if err != nil {
		return access.Principal{}, err
	}
	id, e := newUUID()
	if e != nil {
		return access.Principal{}, e
	}
	roleID, err := pgUUID(id)
	if err != nil {
		return access.Principal{}, err
	}
	principalID, err := pgUUID(p.ID)
	if err != nil {
		return access.Principal{}, err
	}
	err = accessdb.New(tx).InsertPlatformRole(ctx, accessdb.InsertPlatformRoleParams{ID: roleID, PrincipalID: principalID, Role: string(role)})
	if err != nil {
		return access.Principal{}, err
	}
	if ownTx {
		if err := tx.Commit(ctx); err != nil {
			return access.Principal{}, err
		}
	}
	if !ownTx {
		return txRepo.PrincipalByID(ctx, p.ID)
	}
	return r.PrincipalByID(ctx, p.ID)
}

func (r *Repository) IsPlatformAdmin(ctx context.Context, principalID string) (bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return false, err
	}
	id, e := uuidID("principal id", principalID)
	if e != nil {
		return false, nil
	}
	parsed, parseErr := pgUUID(id)
	if parseErr != nil {
		return false, nil
	}
	return accessdb.New(db).IsPlatformAdmin(ctx, parsed)
}

func (r *Repository) CreateServicePrincipal(ctx context.Context, input access.ServicePrincipalInput) (access.Principal, error) {
	return r.UpsertPrincipal(ctx, access.PrincipalInput{ID: input.ID, Kind: access.PrincipalKindServicePrincipal, DisplayName: input.DisplayName})
}
func (r *Repository) ListServicePrincipals(ctx context.Context) ([]access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListServicePrincipals(ctx, maxPageSize)
	if err != nil {
		return nil, err
	}
	out := make([]access.Principal, 0, len(rows))
	for _, row := range rows {
		p := principalFromListGenerated(accessdb.ListPrincipalsRow{
			ID: row.ID, PrincipalType: row.PrincipalType, Status: row.Status,
			Email: row.Email, DisplayName: row.DisplayName, DisabledAt: row.DisabledAt,
			BlockedAt: row.BlockedAt, LastSeenAt: row.LastSeenAt, CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
		out = append(out, p)
	}
	return out, nil
}
func (r *Repository) UpdateServicePrincipal(ctx context.Context, id string, input access.ServicePrincipalInput) (access.Principal, error) {
	input.ID = id
	return r.CreateServicePrincipal(ctx, input)
}
func (r *Repository) DeleteServicePrincipal(ctx context.Context, id string) error {
	id, err := uuidID("service principal id", id)
	if err != nil {
		return err
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	parsedID, err := pgUUID(id)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(tx).DisableServicePrincipal(ctx, parsedID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if err = accessdb.New(tx).RevokePrincipalSessions(ctx, parsedID); err != nil {
		return err
	}
	if err = accessdb.New(tx).RevokePrincipalTokens(ctx, parsedID); err != nil {
		return err
	}
	if err = accessdb.New(tx).RevokePrincipalSecrets(ctx, parsedID); err != nil {
		return err
	}
	if ownTx {
		return tx.Commit(ctx)
	}
	return nil
}

func (r *Repository) UpsertGroup(ctx context.Context, input access.GroupInput) (access.Group, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Group{}, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id, err = newUUID()
	} else {
		id, err = uuidID("group id", id)
	}
	if err != nil {
		return access.Group{}, err
	}
	name, e := bounded(strings.TrimSpace(input.Name), "group name", 255)
	if e != nil {
		return access.Group{}, e
	}
	provider := strings.TrimSpace(input.Provider)
	external := strings.TrimSpace(input.ExternalID)
	if len(provider) > 255 || len(external) > 512 {
		return access.Group{}, fmt.Errorf("group identity exceeds bounds")
	}
	if external != "" {
		gid, qerr := accessdb.New(db).FindGroupByExternal(ctx, accessdb.FindGroupByExternalParams{Provider: provider, ExternalID: external})
		if qerr == nil {
			id = principalUUID(gid)
		} else if !errors.Is(qerr, pgx.ErrNoRows) {
			return access.Group{}, qerr
		}
	}
	parsedID, err := pgUUID(id)
	if err != nil {
		return access.Group{}, err
	}
	err = accessdb.New(db).UpsertGroup(ctx, accessdb.UpsertGroupParams{ID: parsedID, Name: name, Provider: provider, ExternalID: external})
	if err != nil {
		return access.Group{}, err
	}
	return r.groupByID(ctx, id, provider, external)
}
func (r *Repository) groupByID(ctx context.Context, id, provider, external string) (access.Group, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Group{}, err
	}
	parsedID, err := pgUUID(id)
	if err != nil {
		return access.Group{}, err
	}
	row, err := accessdb.New(db).GetGroup(ctx, accessdb.GetGroupParams{ID: parsedID, Provider: provider, ExternalID: external})
	if err != nil {
		return access.Group{}, err
	}
	return access.Group{ID: principalUUID(row.ID), Provider: row.Provider, ExternalID: row.ExternalID,
		Name: row.Name, CreatedAt: principalTimestamp(row.CreatedAt)}, nil
}
func (r *Repository) ListGroups(ctx context.Context) ([]access.Group, error) {
	return r.listGroups(ctx, "", false, maxPageSize)
}
func (r *Repository) ListAllGroups(ctx context.Context) ([]access.Group, error) {
	return r.ListGroups(ctx)
}
func (r *Repository) SearchGroups(ctx context.Context, q string, limit int) ([]access.Group, error) {
	if limit <= 0 {
		limit = 8
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	return r.listGroups(ctx, strings.TrimSpace(q), true, limit)
}
func (r *Repository) listGroups(ctx context.Context, q string, search bool, limit int) ([]access.Group, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	var rows []accessdb.ListGroupsRow
	if search {
		found, qerr := accessdb.New(db).SearchGroups(ctx, accessdb.SearchGroupsParams{Query: q, PageSize: int32(limit)})
		if qerr != nil {
			return nil, qerr
		}
		for _, row := range found {
			rows = append(rows, accessdb.ListGroupsRow{ID: row.ID, Provider: row.Provider, ExternalID: row.ExternalID, Name: row.Name, CreatedAt: row.CreatedAt})
		}
	} else {
		rows, err = accessdb.New(db).ListGroups(ctx, int32(limit))
		if err != nil {
			return nil, err
		}
	}
	out := make([]access.Group, 0, len(rows))
	for _, row := range rows {
		out = append(out, access.Group{ID: principalUUID(row.ID), Provider: row.Provider, ExternalID: row.ExternalID,
			Name: row.Name, CreatedAt: principalTimestamp(row.CreatedAt)})
	}
	return out, nil
}
func (r *Repository) DeleteGroup(ctx context.Context, id string) error {
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	id, err = uuidID("group id", id)
	if err != nil {
		return err
	}
	parsedID, err := pgUUID(id)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(db).RevokeGroup(ctx, parsedID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
func (r *Repository) AddGroupMember(ctx context.Context, groupID, principalID string) error {
	return r.groupMember(ctx, groupID, principalID, true)
}
func (r *Repository) RemoveGroupMember(ctx context.Context, groupID, principalID string) error {
	return r.groupMember(ctx, groupID, principalID, false)
}
func (r *Repository) groupMember(ctx context.Context, gid, pid string, add bool) error {
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	gid, err = uuidID("group id", gid)
	if err != nil {
		return err
	}
	pid, err = uuidID("principal id", pid)
	if err != nil {
		return err
	}
	pgid, err := pgUUID(gid)
	if err != nil {
		return err
	}
	ppid, err := pgUUID(pid)
	if err != nil {
		return err
	}
	if add {
		return accessdb.New(db).AddGroupMember(ctx, accessdb.AddGroupMemberParams{GroupID: pgid, PrincipalID: ppid})
	}
	tag, err := accessdb.New(db).RemoveGroupMember(ctx, accessdb.RemoveGroupMemberParams{GroupID: pgid, PrincipalID: ppid})
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}
func (r *Repository) ListGroupMembersByGroup(ctx context.Context, gid string) ([]access.GroupMember, error) {
	return r.listMembers(ctx, gid)
}
func (r *Repository) ListGroupMembers(ctx context.Context, gid string) ([]access.GroupMember, error) {
	return r.listMembers(ctx, gid)
}
func (r *Repository) listMembers(ctx context.Context, gid string) ([]access.GroupMember, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	gid, err = uuidID("group id", gid)
	if err != nil {
		return nil, err
	}
	groupID, err := pgUUID(gid)
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListGroupMembers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := make([]access.GroupMember, 0, len(rows))
	for _, row := range rows {
		var m access.GroupMember
		m.GroupID, m.PrincipalID = principalUUID(row.ID), principalUUID(row.ID_2)
		m.Email, m.DisplayName, m.Kind = row.Email, row.DisplayName, access.PrincipalKind(row.PrincipalType)
		if row.PrincipalType == "service" {
			m.Kind = access.PrincipalKindServicePrincipal
		}
		m.CreatedAt = principalTimestamp(row.CreatedAt)
		out = append(out, m)
	}
	return out, nil
}
func (r *Repository) ListGroupIDsForPrincipal(ctx context.Context, pid string) ([]string, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	pid, err = uuidID("principal id", pid)
	if err != nil {
		return nil, err
	}
	principalID, err := pgUUID(pid)
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListGroupIDs(ctx, principalID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		out = append(out, principalUUID(id))
	}
	return out, nil
}

func (r *Repository) CreateLocalUser(ctx context.Context, input access.LocalUserInput) (access.LocalPasswordReset, error) {
	_, err := r.requireDB()
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	email := access.NormalizeEmail(input.Email)
	if email == "" || len(email) > 320 {
		return access.LocalPasswordReset{}, fmt.Errorf("email is required")
	}
	password := input.Password
	if password == "" {
		password, err = tokenSecret("lv_tmp_")
		if err != nil {
			return access.LocalPasswordReset{}, err
		}
	}
	if err := access.ValidateLocalPassword(password); err != nil {
		return access.LocalPasswordReset{}, err
	}
	verifier, err := secretVerifier(password)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	id, err := newUUID()
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	txRepo := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	parsedID, err := pgUUID(id)
	if err == nil {
		err = accessdb.New(tx).InsertPrincipal(ctx, accessdb.InsertPrincipalParams{ID: parsedID, Email: email, DisplayName: firstNonEmpty(input.DisplayName, email)})
	}
	if err == nil {
		err = accessdb.New(tx).InsertLocalCredential(ctx, accessdb.InsertLocalCredentialParams{PrincipalID: parsedID, Verifier: verifier, MustChange: input.MustChange})
	}
	if err == nil && ownTx {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	var p access.Principal
	if ownTx {
		p, err = r.PrincipalByID(ctx, id)
	} else {
		p, err = txRepo.PrincipalByID(ctx, id)
	}
	return access.LocalPasswordReset{Principal: p, Password: password}, err
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (r *Repository) LocalCredential(ctx context.Context, pid string) (access.LocalCredential, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.LocalCredential{}, err
	}
	pid, err = uuidID("principal id", pid)
	if err != nil {
		return access.LocalCredential{}, err
	}
	parsedID, err := pgUUID(pid)
	if err != nil {
		return access.LocalCredential{}, err
	}
	row, err := accessdb.New(db).GetLocalCredential(ctx, parsedID)
	if err != nil {
		return access.LocalCredential{}, err
	}
	return access.LocalCredential{PrincipalID: principalUUID(row.PrincipalID), MustChangePassword: row.MustChange,
		CreatedAt: principalTimestamp(row.CreatedAt), UpdatedAt: principalTimestamp(row.UpdatedAt),
		PasswordChangedAt: principalTimestamp(row.PasswordChangedAt)}, nil
}
func (r *Repository) VerifyLocalPassword(ctx context.Context, email, password string) (access.Principal, access.LocalCredential, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Principal{}, access.LocalCredential{}, err
	}
	row, err := accessdb.New(db).FindLocalCredentialByEmail(ctx, access.NormalizeEmail(email))
	if err != nil {
		return access.Principal{}, access.LocalCredential{}, err
	}
	if row.DisabledAt.Valid || row.BlockedAt.Valid || !verifySecret(password, row.Verifier) {
		return access.Principal{}, access.LocalCredential{}, pgx.ErrNoRows
	}
	id := principalUUID(row.ID)
	p, err := r.PrincipalByID(ctx, id)
	if err != nil {
		return access.Principal{}, access.LocalCredential{}, err
	}
	c, err := r.LocalCredential(ctx, id)
	return p, c, err
}
func (r *Repository) ResetLocalPassword(ctx context.Context, pid string) (access.LocalPasswordReset, error) {
	password, err := tokenSecret("lv_tmp_")
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	return r.setPassword(ctx, pid, password, true)
}
func (r *Repository) ChangeLocalPassword(ctx context.Context, pid, current, newPassword string) (access.LocalCredential, error) {
	return r.setPasswordCredential(ctx, pid, current, newPassword, false)
}
func (r *Repository) setPassword(ctx context.Context, pid, password string, must bool) (access.LocalPasswordReset, error) {
	if err := access.ValidateLocalPassword(password); err != nil {
		return access.LocalPasswordReset{}, err
	}
	_, err := r.setPasswordCredential(ctx, pid, "", password, must)
	if err != nil {
		return access.LocalPasswordReset{}, err
	}
	p, err := r.PrincipalByID(ctx, pid)
	return access.LocalPasswordReset{Principal: p, Password: password}, err
}
func (r *Repository) setPasswordCredential(ctx context.Context, pid, current, newPassword string, must bool) (access.LocalCredential, error) {
	if _, requireErr := r.requireDB(); requireErr != nil {
		return access.LocalCredential{}, requireErr
	}
	var err error
	pid, err = uuidID("principal id", pid)
	if err != nil {
		return access.LocalCredential{}, err
	}
	if err = access.ValidateLocalPassword(newPassword); err != nil {
		return access.LocalCredential{}, err
	}
	if current != "" && current == newPassword {
		return access.LocalCredential{}, fmt.Errorf("%w: new password must differ from the current password", access.ErrLocalPasswordPolicy)
	}
	v, err := secretVerifier(newPassword)
	if err != nil {
		return access.LocalCredential{}, err
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.LocalCredential{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	txRepo := &Repository{db: tx, fingerprintKey: r.fingerprintKey}
	parsedID, err := pgUUID(pid)
	if err != nil {
		return access.LocalCredential{}, err
	}
	var old []byte
	if old, err = accessdb.New(tx).LockLocalVerifier(ctx, parsedID); err != nil {
		return access.LocalCredential{}, err
	}
	if current != "" && !verifySecret(current, old) {
		return access.LocalCredential{}, pgx.ErrNoRows
	}
	tag, err := accessdb.New(tx).UpdateLocalCredential(ctx, accessdb.UpdateLocalCredentialParams{PrincipalID: parsedID, Verifier: v, MustChange: must})
	if err != nil {
		return access.LocalCredential{}, err
	}
	if tag.RowsAffected() == 0 {
		return access.LocalCredential{}, pgx.ErrNoRows
	}
	if err = accessdb.New(tx).RevokePrincipalSessions(ctx, parsedID); err != nil {
		return access.LocalCredential{}, err
	}
	if ownTx {
		if err = tx.Commit(ctx); err != nil {
			return access.LocalCredential{}, err
		}
	}
	if !ownTx {
		return txRepo.LocalCredential(ctx, pid)
	}
	return r.LocalCredential(ctx, pid)
}

func (r *Repository) CreateSession(ctx context.Context, pid string, ttl time.Duration) (string, error) {
	db, err := r.requireDB()
	if err != nil {
		return "", err
	}
	pid, err = uuidID("principal id", pid)
	if err != nil {
		return "", err
	}
	if ttl <= 0 || ttl > maxSessionTTL {
		return "", fmt.Errorf("session ttl must be between 0 and %s", maxSessionTTL)
	}
	token, err := tokenSecret("lv_sess_")
	if err != nil {
		return "", err
	}
	ver, err := secretVerifier(token)
	if err != nil {
		return "", err
	}
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	parsedID, err := pgUUID(id)
	if err != nil {
		return "", err
	}
	principalID, err := pgUUID(pid)
	if err != nil {
		return "", err
	}
	tag, err := accessdb.New(db).CreateBrowserSession(ctx, accessdb.CreateBrowserSessionParams{ID: parsedID, PrincipalID: principalID,
		TokenFingerprint: r.secretFingerprint(token), Verifier: ver, Ttl: pgInterval(ttl)})
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", pgx.ErrNoRows
	}
	return token, nil
}
func (r *Repository) PrincipalForToken(ctx context.Context, token string) (access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Principal{}, err
	}
	row, err := accessdb.New(db).FindBrowserSession(ctx, r.secretFingerprint(token))
	if err != nil {
		return access.Principal{}, err
	}
	if !hmac.Equal(row.TokenFingerprint, r.secretFingerprint(token)) || !verifySecret(token, row.Verifier) {
		return access.Principal{}, pgx.ErrNoRows
	}
	_ = accessdb.New(db).TouchBrowserSession(ctx, row.TokenFingerprint)
	return r.PrincipalByID(ctx, principalUUID(row.ID))
}
func (r *Repository) DeleteSession(ctx context.Context, token string) error {
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	tag, err := accessdb.New(db).RevokeBrowserSession(ctx, r.secretFingerprint(token))
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}
func (r *Repository) ListSessions(ctx context.Context, pid string) ([]access.Session, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	pid, err = uuidID("principal id", pid)
	if err != nil {
		return nil, err
	}
	principalID, err := pgUUID(pid)
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListSessions(ctx, accessdb.ListSessionsParams{PrincipalID: principalID, PageSize: maxPageSize})
	if err != nil {
		return nil, err
	}
	out := make([]access.Session, 0, len(rows))
	for _, row := range rows {
		s := access.Session{ID: principalUUID(row.ID), PrincipalID: principalUUID(row.PrincipalID), Kind: access.SessionKind(row.Kind),
			InstanceID: row.InstanceID, ProfileID: row.ProfileID, ClientID: row.ClientID,
			ExpiresAt: principalTimestamp(row.ExpiresAt), AbsoluteExpiresAt: principalTimestamp(row.AbsoluteExpiresAt),
			CreatedAt: principalTimestamp(row.CreatedAt), LastSeenAt: principalTimestamp(row.LastSeenAt), RevokedAt: principalTimestamp(row.RevokedAt)}
		out = append(out, s)
	}
	return out, nil
}
func (r *Repository) RevokeSession(ctx context.Context, id string) error {
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	id, err = uuidID("session id", id)
	if err != nil {
		return err
	}
	parsedID, err := pgUUID(id)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(db).RevokeSession(ctx, parsedID)
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}
func (r *Repository) RevokeSessionForPrincipal(ctx context.Context, pid, id string) error {
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	pid, err = uuidID("principal id", pid)
	if err != nil {
		return err
	}
	id, err = uuidID("session id", id)
	if err != nil {
		return err
	}
	sessionID, err := pgUUID(id)
	if err != nil {
		return err
	}
	principalID, err := pgUUID(pid)
	if err != nil {
		return err
	}
	tag, err := accessdb.New(db).RevokeSessionForPrincipal(ctx, accessdb.RevokeSessionForPrincipalParams{ID: sessionID, PrincipalID: principalID})
	if err == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return err
}

func capabilitiesJSON(caps []access.Capability) ([]byte, error) {
	if err := access.ValidateTokenCapabilities(caps, access.CanonicalCapabilities()); err != nil {
		return nil, err
	}
	if caps == nil {
		return nil, nil
	}
	return json.Marshal(caps)
}

func databaseExpiryValid(ctx context.Context, db DBTX, expiresAt time.Time) (bool, error) {
	valid, err := accessdb.New(db).CheckExpiry(ctx, pgTimestamp(expiresAt))
	if err != nil || valid == nil {
		return false, err
	}
	return *valid, nil
}

func (r *Repository) CreateAPIToken(ctx context.Context, pid, name string) (string, error) {
	t, _, e := r.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: pid, Name: name})
	return t, e
}
func (r *Repository) CreateAPITokenWithMetadata(ctx context.Context, in access.APITokenInput) (string, access.APIToken, error) {
	db, err := r.requireDB()
	if err != nil {
		return "", access.APIToken{}, err
	}
	pid, err := uuidID("principal id", in.PrincipalID)
	if err != nil {
		return "", access.APIToken{}, err
	}
	name, err := bounded(strings.TrimSpace(in.Name), "token name", 255)
	if err != nil {
		return "", access.APIToken{}, err
	}
	caps, err := capabilitiesJSON(in.Capabilities)
	if err != nil {
		return "", access.APIToken{}, err
	}
	tok, err := tokenSecret("lv_pat_")
	if err != nil {
		return "", access.APIToken{}, err
	}
	ver, err := secretVerifier(tok)
	if err != nil {
		return "", access.APIToken{}, err
	}
	id, err := newUUID()
	if err != nil {
		return "", access.APIToken{}, err
	}
	tokenID, err := pgUUID(id)
	if err != nil {
		return "", access.APIToken{}, err
	}
	principalID, err := pgUUID(pid)
	if err != nil {
		return "", access.APIToken{}, err
	}
	tag, err := accessdb.New(db).CreateAPIToken(ctx, accessdb.CreateAPITokenParams{ID: tokenID, PrincipalID: principalID, Name: name,
		TokenFingerprint: r.secretFingerprint(tok), Verifier: ver, Capabilities: caps, ExpiresAt: pgTimestamp(in.ExpiresAt)})
	if err != nil {
		return "", access.APIToken{}, err
	}
	if tag.RowsAffected() == 0 {
		valid, checkErr := databaseExpiryValid(ctx, db, in.ExpiresAt)
		if checkErr != nil {
			return "", access.APIToken{}, checkErr
		}
		if !valid {
			return "", access.APIToken{}, fmt.Errorf("api token expiry is invalid")
		}
		return "", access.APIToken{}, pgx.ErrNoRows
	}
	row, e := r.apiToken(ctx, id)
	return tok, row, e
}
func (r *Repository) apiToken(ctx context.Context, id string) (access.APIToken, error) {
	db, _ := r.requireDB()
	parsedID, err := pgUUID(id)
	if err != nil {
		return access.APIToken{}, err
	}
	row, err := accessdb.New(db).GetAPIToken(ctx, parsedID)
	if err != nil {
		return access.APIToken{}, err
	}
	t := access.APIToken{ID: principalUUID(row.ID), PrincipalID: principalUUID(row.PrincipalID), Name: row.Name,
		ExpiresAt: principalTimestamp(row.ExpiresAt), CreatedAt: principalTimestamp(row.CreatedAt),
		LastUsedAt: principalTimestamp(row.LastUsedAt), RevokedAt: principalTimestamp(row.RevokedAt)}
	if len(row.Capabilities) > 0 && string(row.Capabilities) != "null" {
		_ = json.Unmarshal(row.Capabilities, &t.Capabilities)
	}
	return t, nil
}
func (r *Repository) apiTokenForSecret(ctx context.Context, secret string) (access.APIToken, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.APIToken{}, err
	}
	row, err := accessdb.New(db).FindAPITokenByFingerprint(ctx, r.secretFingerprint(secret))
	if err != nil {
		return access.APIToken{}, err
	}
	if !verifySecret(secret, row.Verifier) {
		return access.APIToken{}, pgx.ErrNoRows
	}
	_ = accessdb.New(db).TouchAPIToken(ctx, row.ID)
	return r.apiToken(ctx, principalUUID(row.ID))
}
func (r *Repository) PrincipalForAPIToken(ctx context.Context, tok string) (access.Principal, error) {
	c, e := r.CredentialForAPIToken(ctx, tok)
	return c.Principal, e
}
func (r *Repository) CredentialForAPIToken(ctx context.Context, tok string) (access.APICredential, error) {
	t, e := r.apiTokenForSecret(ctx, tok)
	if e != nil {
		return access.APICredential{}, e
	}
	p, e := r.PrincipalByID(ctx, t.PrincipalID)
	if e != nil || p.AccessDisabled() {
		return access.APICredential{}, pgx.ErrNoRows
	}
	return access.APICredential{Principal: p, Token: t}, nil
}
func (r *Repository) ListAPITokens(ctx context.Context, pid string) ([]access.APIToken, error) {
	pid, e := uuidID("principal id", pid)
	if e != nil {
		return nil, e
	}
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	principalID, err := pgUUID(pid)
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListAPITokenIDs(ctx, accessdb.ListAPITokenIDsParams{PrincipalID: principalID, PageSize: maxPageSize})
	if err != nil {
		return nil, err
	}
	out := make([]access.APIToken, 0, len(rows))
	for _, id := range rows {
		t, e := r.apiToken(ctx, principalUUID(id))
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, nil
}
func (r *Repository) RevokeAPIToken(ctx context.Context, id string) error {
	db, e := r.requireDB()
	if e != nil {
		return e
	}
	id, e = uuidID("api token id", id)
	if e != nil {
		return e
	}
	parsedID, e := pgUUID(id)
	if e != nil {
		return e
	}
	tag, e := accessdb.New(db).RevokeAPIToken(ctx, parsedID)
	if e == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return e
}
func (r *Repository) RevokeAPITokenForPrincipal(ctx context.Context, pid, id string) error {
	db, e := r.requireDB()
	if e != nil {
		return e
	}
	pid, e = uuidID("principal id", pid)
	if e != nil {
		return e
	}
	id, e = uuidID("api token id", id)
	if e != nil {
		return e
	}
	tokenID, e := pgUUID(id)
	if e != nil {
		return e
	}
	principalID, e := pgUUID(pid)
	if e != nil {
		return e
	}
	tag, e := accessdb.New(db).RevokeAPITokenForPrincipal(ctx, accessdb.RevokeAPITokenForPrincipalParams{ID: tokenID, PrincipalID: principalID})
	if e == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return e
}

func (r *Repository) CreateServicePrincipalSecret(ctx context.Context, pid string, in access.ServicePrincipalSecretInput) (string, access.ServicePrincipalSecret, error) {
	db, e := r.requireDB()
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	pid, e = uuidID("service principal id", pid)
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	name, e := bounded(strings.TrimSpace(in.Name), "secret name", 255)
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	secret, e := tokenSecret("lv_sp_")
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	ver, e := secretVerifier(secret)
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	id, e := newUUID()
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	secretID, e := pgUUID(id)
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	principalID, e := pgUUID(pid)
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	tag, e := accessdb.New(db).CreateServiceSecret(ctx, accessdb.CreateServiceSecretParams{ID: secretID, PrincipalID: principalID, Name: name,
		SecretFingerprint: r.secretFingerprint(secret), Verifier: ver, ExpiresAt: pgTimestamp(in.ExpiresAt)})
	if e != nil {
		return "", access.ServicePrincipalSecret{}, e
	}
	if tag.RowsAffected() == 0 {
		valid, checkErr := databaseExpiryValid(ctx, db, in.ExpiresAt)
		if checkErr != nil {
			return "", access.ServicePrincipalSecret{}, checkErr
		}
		if !valid {
			return "", access.ServicePrincipalSecret{}, fmt.Errorf("secret expiry is invalid")
		}
		return "", access.ServicePrincipalSecret{}, pgx.ErrNoRows
	}
	row, e := r.serviceSecret(ctx, id)
	return secret, row, e
}
func (r *Repository) serviceSecret(ctx context.Context, id string) (access.ServicePrincipalSecret, error) {
	db, _ := r.requireDB()
	parsedID, err := pgUUID(id)
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	row, err := accessdb.New(db).GetServiceSecret(ctx, parsedID)
	if err != nil {
		return access.ServicePrincipalSecret{}, err
	}
	return access.ServicePrincipalSecret{ID: principalUUID(row.ID), ServicePrincipalID: principalUUID(row.ServicePrincipalID), Name: row.Name,
		ExpiresAt: principalTimestamp(row.ExpiresAt), CreatedAt: principalTimestamp(row.CreatedAt), RevokedAt: principalTimestamp(row.RevokedAt)}, nil
}
func (r *Repository) RevokeServicePrincipalSecret(ctx context.Context, pid, sid string) error {
	db, e := r.requireDB()
	if e != nil {
		return e
	}
	pid, e = uuidID("service principal id", pid)
	if e != nil {
		return e
	}
	sid, e = uuidID("secret id", sid)
	if e != nil {
		return e
	}
	secretID, e := pgUUID(sid)
	if e != nil {
		return e
	}
	principalID, e := pgUUID(pid)
	if e != nil {
		return e
	}
	tag, e := accessdb.New(db).RevokeServiceSecret(ctx, accessdb.RevokeServiceSecretParams{ID: secretID, PrincipalID: principalID})
	if e == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return e
}
func (r *Repository) PrincipalForServicePrincipalSecret(ctx context.Context, pid, secret string) (access.Principal, error) {
	db, e := r.requireDB()
	if e != nil {
		return access.Principal{}, e
	}
	pid, e = uuidID("service principal id", pid)
	if e != nil {
		return access.Principal{}, e
	}
	principalID, e := pgUUID(pid)
	if e != nil {
		return access.Principal{}, e
	}
	row, e := accessdb.New(db).FindServiceSecretByFingerprint(ctx, accessdb.FindServiceSecretByFingerprintParams{PrincipalID: principalID, SecretFingerprint: r.secretFingerprint(secret)})
	if e != nil {
		return access.Principal{}, e
	}
	if !verifySecret(secret, row.Verifier) {
		return access.Principal{}, pgx.ErrNoRows
	}
	p, e := r.PrincipalByID(ctx, principalUUID(row.ServicePrincipalID))
	if e != nil || p.AccessDisabled() {
		return access.Principal{}, pgx.ErrNoRows
	}
	return p, nil
}
func (r *Repository) BootstrapAdmin(ctx context.Context, email string) error {
	email = access.NormalizeEmail(email)
	if email == "" {
		return nil
	}
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	var id string
	rowID, lookupErr := accessdb.New(db).FindPrincipalByEmail(ctx, email)
	err = lookupErr
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = newUUID()
	} else if err == nil {
		id = principalUUID(rowID)
	}
	if err != nil {
		return err
	}
	_, err = r.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: id, Email: email, DisplayName: email, Role: access.PlatformRoleAdmin})
	return err
}
func (r *Repository) ResolveExternalPrincipal(ctx context.Context, in access.ExternalIdentityInput) (access.Principal, error) {
	provider, e := bounded(strings.TrimSpace(in.Provider), "provider", 128)
	if e != nil {
		return access.Principal{}, e
	}
	tenant := strings.TrimSpace(in.TenantID)
	if tenant != in.TenantID || len(tenant) > 255 || strings.ContainsAny(tenant, "\x00\r\n") {
		return access.Principal{}, fmt.Errorf("tenant id is invalid")
	}
	subject, e := bounded(strings.TrimSpace(in.Subject), "subject", 512)
	if e != nil {
		return access.Principal{}, e
	}
	tx, e := r.beginTx(ctx)
	if e != nil {
		return access.Principal{}, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var pid string
	identityRow, err := accessdb.New(tx).FindExternalIdentity(ctx, accessdb.FindExternalIdentityParams{Provider: provider, TenantID: tenant, Subject: subject})
	if err == nil {
		pid = principalUUID(identityRow)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		pid, e = newUUID()
		if e != nil {
			return access.Principal{}, e
		}
		principalID, parseErr := pgUUID(pid)
		if parseErr != nil {
			return access.Principal{}, parseErr
		}
		if e = accessdb.New(tx).InsertExternalPrincipal(ctx, accessdb.InsertExternalPrincipalParams{ID: principalID, Email: access.NormalizeEmail(in.Email), DisplayName: strings.TrimSpace(in.DisplayName)}); e != nil {
			return access.Principal{}, e
		}
		identity, ie := newUUID()
		if ie != nil {
			return access.Principal{}, ie
		}
		identityID, parseErr := pgUUID(identity)
		if parseErr != nil {
			return access.Principal{}, parseErr
		}
		if e = accessdb.New(tx).InsertExternalIdentity(ctx, accessdb.InsertExternalIdentityParams{ID: identityID, PrincipalID: principalID,
			Provider: provider, TenantID: tenant, Subject: subject, UserName: strings.TrimSpace(in.Email), ExternalID: "",
			Email: access.NormalizeEmail(in.Email), DisplayName: strings.TrimSpace(in.DisplayName)}); e != nil {
			return access.Principal{}, e
		}
	} else if err != nil {
		return access.Principal{}, err
	} else {
		if e = accessdb.New(tx).UpdateExternalIdentity(ctx, accessdb.UpdateExternalIdentityParams{Provider: provider, TenantID: tenant,
			Subject: subject, Email: access.NormalizeEmail(in.Email), DisplayName: strings.TrimSpace(in.DisplayName)}); e != nil {
			return access.Principal{}, e
		}
		principalID, parseErr := pgUUID(pid)
		if parseErr != nil {
			return access.Principal{}, parseErr
		}
		if e = accessdb.New(tx).UpdateExternalPrincipal(ctx, accessdb.UpdateExternalPrincipalParams{ID: principalID, Email: access.NormalizeEmail(in.Email), DisplayName: strings.TrimSpace(in.DisplayName)}); e != nil {
			return access.Principal{}, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return access.Principal{}, e
	}
	return r.PrincipalByID(ctx, pid)
}

func (r *Repository) CreateDesktopSession(ctx context.Context, pid, instanceID, profileID string, ttl time.Duration) (string, error) {
	db, e := r.requireDB()
	if e != nil {
		return "", e
	}
	if !strings.HasPrefix(instanceID, "instance_") || !strings.HasPrefix(profileID, "profile_") || len(instanceID) > 128 || len(profileID) > 128 {
		return "", fmt.Errorf("desktop session binding is invalid")
	}
	pid, e = uuidID("principal id", pid)
	if e != nil {
		return "", e
	}
	if ttl <= 0 || ttl > access.DesktopSessionAbsoluteLifetime {
		return "", fmt.Errorf("desktop session ttl must be between 0 and %s", access.DesktopSessionAbsoluteLifetime)
	}
	tok, e := tokenSecret("lv_desktop_")
	if e != nil {
		return "", e
	}
	ver, e := secretVerifier(tok)
	if e != nil {
		return "", e
	}
	id, e := newUUID()
	if e != nil {
		return "", e
	}
	sessionID, e := pgUUID(id)
	if e != nil {
		return "", e
	}
	principalID, e := pgUUID(pid)
	if e != nil {
		return "", e
	}
	tag, e := accessdb.New(db).CreateDesktopSession(ctx, accessdb.CreateDesktopSessionParams{ID: sessionID, PrincipalID: principalID,
		TokenFingerprint: r.secretFingerprint(tok), Verifier: ver, Ttl: pgInterval(ttl), AbsoluteTtl: pgInterval(access.DesktopSessionAbsoluteLifetime),
		InstanceID: instanceID, ProfileID: profileID})
	if e == nil && tag.RowsAffected() == 0 {
		return "", pgx.ErrNoRows
	}
	return tok, e
}
func (r *Repository) DesktopSessionForToken(ctx context.Context, tok string) (access.DesktopSession, error) {
	if _, e := r.requireDB(); e != nil {
		return access.DesktopSession{}, e
	}
	tx, e := r.beginTx(ctx)
	if e != nil {
		return access.DesktopSession{}, e
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var s access.DesktopSession
	row, e := accessdb.New(tx).FindDesktopSession(ctx, accessdb.FindDesktopSessionParams{TokenFingerprint: r.secretFingerprint(tok), IdleTtl: pgInterval(access.DesktopSessionIdleTimeout)})
	if e != nil {
		return access.DesktopSession{}, e
	}
	s.SessionID, s.PrincipalID, s.InstanceID, s.ProfileID, s.ClientID = principalUUID(row.ID), principalUUID(row.PrincipalID), row.InstanceID, row.ProfileID, row.ClientID
	if !verifySecret(tok, row.Verifier) {
		return access.DesktopSession{}, pgx.ErrNoRows
	}
	touched, e := accessdb.New(tx).TouchDesktopSession(ctx, accessdb.TouchDesktopSessionParams{IdleTtl: pgInterval(access.DesktopSessionIdleTimeout), ID: row.ID,
		TokenFingerprint: r.secretFingerprint(tok), IdleTtlCheck: pgInterval(access.DesktopSessionIdleTimeout)})
	if e != nil {
		return access.DesktopSession{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return access.DesktopSession{}, e
	}
	s.ExpiresAt = principalTimestamp(touched.ExpiresAt)
	s.AbsoluteExpiresAt = principalTimestamp(touched.AbsoluteExpiresAt)
	s.CreatedAt = principalTimestamp(touched.CreatedAt)
	return s, nil
}
func (r *Repository) RevokeDesktopSession(ctx context.Context, tok, instanceID, profileID string) error {
	tx, ownTx, e := r.txOrBegin(ctx)
	if e != nil {
		return e
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	row, e := accessdb.New(tx).FindDesktopSessionForRevoke(ctx, accessdb.FindDesktopSessionForRevokeParams{TokenFingerprint: r.secretFingerprint(tok), InstanceID: instanceID, ProfileID: profileID})
	if e != nil {
		return e
	}
	if !verifySecret(tok, row.Verifier) {
		return pgx.ErrNoRows
	}
	tag, e := accessdb.New(tx).RevokeDesktopSession(ctx, row.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if ownTx {
		return tx.Commit(ctx)
	}
	return nil
}

var _ access.PlatformAdminReader = (*Repository)(nil)
var _ access.DesktopSessionRepository = (*Repository)(nil)
