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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func newUUID() (string, error) { id, err := uuid.NewV7(); return id.String(), err }

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
func formatTimePtr(v *time.Time) string {
	if v == nil {
		return ""
	}
	return formatTime(*v)
}

func principalSelect() string {
	return `SELECT id::text, principal_type, status, COALESCE(email,''), display_name, disabled_at, blocked_at, last_seen_at, created_at, updated_at FROM access.principal`
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
	p, err := scanPrincipal(db.QueryRow(ctx, principalSelect()+` WHERE id=$1::uuid AND revoked_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.Principal{}, pgx.ErrNoRows
	}
	return p, err
}

func (r *Repository) ListPrincipals(ctx context.Context, filter access.PrincipalFilter) ([]access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	email, query := strings.TrimSpace(filter.Email), strings.TrimSpace(filter.Query)
	rows, err := db.Query(ctx, principalSelect()+` WHERE revoked_at IS NULL AND ($1='' OR lower(email)=lower($1)) AND ($2='' OR email ILIKE '%'||$2||'%' OR display_name ILIKE '%'||$2||'%') ORDER BY created_at DESC LIMIT $3`, email, query, maxPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.Principal, 0)
	for rows.Next() {
		var p access.Principal
		var kind, status string
		var disabled, blocked, last, created, updated *time.Time
		if err := rows.Scan(&p.ID, &kind, &status, &p.Email, &p.DisplayName, &disabled, &blocked, &last, &created, &updated); err != nil {
			return nil, err
		}
		p.Kind = access.PrincipalKind(kind)
		if kind == "service" {
			p.Kind = access.PrincipalKindServicePrincipal
		}
		p.DisabledAt = formatTimePtr(disabled)
		p.BlockedAt = formatTimePtr(blocked)
		p.LastSeenAt = formatTimePtr(last)
		p.CreatedAt = formatTimePtr(created)
		p.UpdatedAt = formatTimePtr(updated)
		out = append(out, p)
	}
	return out, rows.Err()
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
	rows, err := db.Query(ctx, principalSelect()+` WHERE revoked_at IS NULL AND (email ILIKE '%'||$1||'%' OR display_name ILIKE '%'||$1||'%') ORDER BY display_name LIMIT $2`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []access.Principal{}
	for rows.Next() {
		var p access.Principal
		var kind, status string
		var disabled, blocked, last, created, updated *time.Time
		if err := rows.Scan(&p.ID, &kind, &status, &p.Email, &p.DisplayName, &disabled, &blocked, &last, &created, &updated); err != nil {
			return nil, err
		}
		p.Kind = access.PrincipalKind(kind)
		if kind == "service" {
			p.Kind = access.PrincipalKindServicePrincipal
		}
		p.DisabledAt = formatTimePtr(disabled)
		p.BlockedAt = formatTimePtr(blocked)
		p.LastSeenAt = formatTimePtr(last)
		p.CreatedAt = formatTimePtr(created)
		p.UpdatedAt = formatTimePtr(updated)
		out = append(out, p)
	}
	return out, rows.Err()
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
		sqlKind = "system"
	}
	if existing, lookupErr := r.PrincipalByID(ctx, id); lookupErr == nil && existing.Kind != kind {
		return access.Principal{}, fmt.Errorf("principal kind is immutable")
	}
	tag, err := db.Exec(ctx, `INSERT INTO access.principal(id,principal_type,status,email,display_name) VALUES($1::uuid,$2,'active',$3,$4) ON CONFLICT(id) DO UPDATE SET email=EXCLUDED.email,display_name=EXCLUDED.display_name,updated_at=clock_timestamp() WHERE access.principal.principal_type=EXCLUDED.principal_type AND access.principal.revoked_at IS NULL`, id, sqlKind, email, display)
	if err != nil {
		return access.Principal{}, err
	}
	if tag.RowsAffected() == 0 {
		var existingKind string
		lookupErr := db.QueryRow(ctx, `SELECT principal_type FROM access.principal WHERE id=$1::uuid`, id).Scan(&existingKind)
		if lookupErr != nil {
			return access.Principal{}, lookupErr
		}
		if existingKind != sqlKind {
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
	_, err = tx.Exec(ctx, `INSERT INTO access.platform_role_binding(id,principal_id,role) VALUES($1::uuid,$2::uuid,$3) ON CONFLICT (principal_id,role) WHERE revoked_at IS NULL DO NOTHING`, id, p.ID, string(role))
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
	var yes bool
	err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.principal p JOIN access.platform_role_binding b ON b.principal_id=p.id WHERE p.id=$1::uuid AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL AND b.role='platform_admin' AND b.revoked_at IS NULL)`, id).Scan(&yes)
	return yes, err
}

func (r *Repository) CreateServicePrincipal(ctx context.Context, input access.ServicePrincipalInput) (access.Principal, error) {
	return r.UpsertPrincipal(ctx, access.PrincipalInput{ID: input.ID, Kind: access.PrincipalKindServicePrincipal, DisplayName: input.DisplayName})
}
func (r *Repository) ListServicePrincipals(ctx context.Context) ([]access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, principalSelect()+` WHERE principal_type='service' AND revoked_at IS NULL ORDER BY created_at DESC LIMIT $1`, maxPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]access.Principal, 0)
	for rows.Next() {
		var p access.Principal
		var kind, status string
		var disabled, blocked, last, created, updated *time.Time
		if err := rows.Scan(&p.ID, &kind, &status, &p.Email, &p.DisplayName, &disabled, &blocked, &last, &created, &updated); err != nil {
			return nil, err
		}
		p.Kind = access.PrincipalKindServicePrincipal
		p.DisabledAt, p.BlockedAt = formatTimePtr(disabled), formatTimePtr(blocked)
		p.LastSeenAt, p.CreatedAt, p.UpdatedAt = formatTimePtr(last), formatTimePtr(created), formatTimePtr(updated)
		out = append(out, p)
	}
	return out, rows.Err()
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
	tag, err := tx.Exec(ctx, `UPDATE access.principal SET status='disabled',disabled_at=COALESCE(disabled_at,clock_timestamp()),revoked_at=COALESCE(revoked_at,clock_timestamp()),updated_at=clock_timestamp() WHERE id=$1::uuid AND principal_type='service' AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.service_principal_secret SET revoked_at=clock_timestamp() WHERE service_principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
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
		var existing string
		qerr := db.QueryRow(ctx, `SELECT id::text FROM access.access_group WHERE provider=$1 AND external_id=$2 AND revoked_at IS NULL`, provider, external).Scan(&existing)
		if qerr == nil {
			id = existing
		} else if !errors.Is(qerr, pgx.ErrNoRows) {
			return access.Group{}, qerr
		}
	}
	_, err = db.Exec(ctx, `INSERT INTO access.access_group(id,name,provider,external_id) VALUES($1::uuid,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name`, id, name, provider, external)
	if err != nil {
		return access.Group{}, err
	}
	return r.groupByID(ctx, id, provider, external)
}
func (r *Repository) groupByID(ctx context.Context, id, provider, external string) (access.Group, error) {
	db, _ := r.requireDB()
	var g access.Group
	var created time.Time
	err := db.QueryRow(ctx, `SELECT id::text,provider,external_id,name,created_at FROM access.access_group WHERE revoked_at IS NULL AND (id=$1::uuid OR (provider=$2 AND external_id=$3))`, id, provider, external).Scan(&g.ID, &g.Provider, &g.ExternalID, &g.Name, &created)
	g.CreatedAt = formatTime(created)
	return g, err
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
	clause := ` WHERE revoked_at IS NULL`
	if search {
		clause += ` AND (name ILIKE '%'||$1||'%' OR external_id ILIKE '%'||$1||'%')`
	}
	args := []any{}
	if search {
		args = append(args, q)
	}
	args = append(args, limit)
	rows, err := db.Query(ctx, `SELECT id::text,provider,external_id,name,created_at FROM access.access_group`+clause+` ORDER BY name LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []access.Group{}
	for rows.Next() {
		var g access.Group
		var t time.Time
		if err := rows.Scan(&g.ID, &g.Provider, &g.ExternalID, &g.Name, &t); err != nil {
			return nil, err
		}
		g.CreatedAt = formatTime(t)
		out = append(out, g)
	}
	return out, rows.Err()
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
	tag, err := db.Exec(ctx, `UPDATE access.access_group SET revoked_at=clock_timestamp() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
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
	if add {
		_, err = db.Exec(ctx, `INSERT INTO access.principal_group(group_id,principal_id) SELECT $1::uuid,$2::uuid WHERE EXISTS(SELECT 1 FROM access.access_group WHERE id=$1::uuid AND revoked_at IS NULL) AND EXISTS(SELECT 1 FROM access.principal WHERE id=$2::uuid AND revoked_at IS NULL) AND NOT EXISTS(SELECT 1 FROM access.principal_group WHERE group_id=$1::uuid AND principal_id=$2::uuid AND revoked_at IS NULL) ON CONFLICT DO NOTHING`, gid, pid)
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE access.principal_group SET revoked_at=clock_timestamp() WHERE group_id=$1::uuid AND principal_id=$2::uuid AND revoked_at IS NULL`, gid, pid)
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
	rows, err := db.Query(ctx, `SELECT g.id::text,p.id::text,p.principal_type,COALESCE(p.email,''),p.display_name,pg.created_at FROM access.principal_group pg JOIN access.access_group g ON g.id=pg.group_id JOIN access.principal p ON p.id=pg.principal_id WHERE g.id=$1::uuid AND pg.revoked_at IS NULL AND g.revoked_at IS NULL ORDER BY p.display_name`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []access.GroupMember{}
	for rows.Next() {
		var m access.GroupMember
		var kind string
		var t time.Time
		if err := rows.Scan(&m.GroupID, &m.PrincipalID, &kind, &m.Email, &m.DisplayName, &t); err != nil {
			return nil, err
		}
		m.Kind = access.PrincipalKind(kind)
		if kind == "service" {
			m.Kind = access.PrincipalKindServicePrincipal
		}
		m.CreatedAt = formatTime(t)
		out = append(out, m)
	}
	return out, rows.Err()
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
	rows, err := db.Query(ctx, `SELECT pg.group_id::text FROM access.principal_group pg JOIN access.access_group g ON g.id=pg.group_id WHERE pg.principal_id=$1::uuid AND pg.revoked_at IS NULL AND g.revoked_at IS NULL ORDER BY pg.group_id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
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
	if _, err = tx.Exec(ctx, `INSERT INTO access.principal(id,principal_type,status,email,display_name) VALUES($1,'user','active',$2,$3)`, id, email, firstNonEmpty(input.DisplayName, email)); err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO access.local_credential(principal_id,verifier,must_change,password_changed_at) VALUES($1,$2,$3,clock_timestamp())`, id, verifier, input.MustChange)
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
	var c access.LocalCredential
	var must bool
	var created, updated, changed time.Time
	err = db.QueryRow(ctx, `SELECT principal_id::text,must_change,created_at,updated_at,password_changed_at FROM access.local_credential WHERE principal_id=$1::uuid`, pid).Scan(&c.PrincipalID, &must, &created, &updated, &changed)
	if err != nil {
		return c, err
	}
	c.MustChangePassword = must
	c.CreatedAt = formatTime(created)
	c.UpdatedAt = formatTime(updated)
	c.PasswordChangedAt = formatTime(changed)
	return c, nil
}
func (r *Repository) VerifyLocalPassword(ctx context.Context, email, password string) (access.Principal, access.LocalCredential, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Principal{}, access.LocalCredential{}, err
	}
	var id string
	var verifier []byte
	var disabled, blocked *time.Time
	err = db.QueryRow(ctx, `SELECT p.id::text,c.verifier,p.disabled_at,p.blocked_at FROM access.principal p JOIN access.local_credential c ON c.principal_id=p.id WHERE lower(p.email)=lower($1) AND p.status='active' AND p.revoked_at IS NULL AND c.revoked_at IS NULL`, access.NormalizeEmail(email)).Scan(&id, &verifier, &disabled, &blocked)
	if err != nil {
		return access.Principal{}, access.LocalCredential{}, err
	}
	if disabled != nil || blocked != nil || !verifySecret(password, verifier) {
		return access.Principal{}, access.LocalCredential{}, pgx.ErrNoRows
	}
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
	var old []byte
	if err = tx.QueryRow(ctx, `SELECT verifier FROM access.local_credential WHERE principal_id=$1::uuid FOR UPDATE`, pid).Scan(&old); err != nil {
		return access.LocalCredential{}, err
	}
	if current != "" && !verifySecret(current, old) {
		return access.LocalCredential{}, pgx.ErrNoRows
	}
	tag, err := tx.Exec(ctx, `UPDATE access.local_credential SET verifier=$2,must_change=$3,updated_at=clock_timestamp(),password_changed_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, pid, v, must)
	if err != nil {
		return access.LocalCredential{}, err
	}
	if tag.RowsAffected() == 0 {
		return access.LocalCredential{}, pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, pid); err != nil {
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
	tag, err := db.Exec(ctx, `INSERT INTO access.session(id,principal_id,token_fingerprint,verifier,expires_at,kind) SELECT $1::uuid,$2::uuid,$3,$4,clock_timestamp()+$5::interval,'browser' WHERE EXISTS(SELECT 1 FROM access.principal WHERE id=$2::uuid AND status='active' AND disabled_at IS NULL AND blocked_at IS NULL)`, id, pid, r.secretFingerprint(token), ver, ttl.String())
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
	var id string
	var fp, ver []byte
	err = db.QueryRow(ctx, `SELECT p.id::text,s.token_fingerprint,s.verifier FROM access.session s JOIN access.principal p ON p.id=s.principal_id WHERE s.token_fingerprint=$1 AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL`, r.secretFingerprint(token)).Scan(&id, &fp, &ver)
	if err != nil {
		return access.Principal{}, err
	}
	if !hmac.Equal(fp, r.secretFingerprint(token)) || !verifySecret(token, ver) {
		return access.Principal{}, pgx.ErrNoRows
	}
	_, _ = db.Exec(ctx, `UPDATE access.session SET last_seen_at=clock_timestamp() WHERE token_fingerprint=$1`, fp)
	return r.PrincipalByID(ctx, id)
}
func (r *Repository) DeleteSession(ctx context.Context, token string) error {
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE token_fingerprint=$1 AND revoked_at IS NULL`, r.secretFingerprint(token))
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
	rows, err := db.Query(ctx, `SELECT id::text,principal_id::text,kind,instance_id,profile_id,client_id,expires_at,absolute_expires_at,created_at,last_seen_at,revoked_at FROM access.session WHERE principal_id=$1::uuid ORDER BY created_at DESC LIMIT $2`, pid, maxPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []access.Session{}
	for rows.Next() {
		var s access.Session
		var kind string
		var abs, rev *time.Time
		var exp, created, last time.Time
		if err := rows.Scan(&s.ID, &s.PrincipalID, &kind, &s.InstanceID, &s.ProfileID, &s.ClientID, &exp, &abs, &created, &last, &rev); err != nil {
			return nil, err
		}
		s.Kind = access.SessionKind(kind)
		s.ExpiresAt = formatTime(exp)
		s.AbsoluteExpiresAt = formatTimePtr(abs)
		s.CreatedAt = formatTime(created)
		s.LastSeenAt = formatTime(last)
		s.RevokedAt = formatTimePtr(rev)
		out = append(out, s)
	}
	return out, rows.Err()
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
	tag, err := db.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
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
	tag, err := db.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE id=$1::uuid AND principal_id=$2::uuid AND revoked_at IS NULL`, id, pid)
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
	var valid bool
	err := db.QueryRow(ctx, `SELECT $1::timestamptz > clock_timestamp() AND $1::timestamptz <= clock_timestamp()+interval '365 days'`, expiresAt.UTC()).Scan(&valid)
	return valid, err
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
	tag, err := db.Exec(ctx, `INSERT INTO access.api_token(id,principal_id,name,token_fingerprint,verifier,capabilities,expires_at) SELECT $1::uuid,$2::uuid,$3,$4,$5,$6::jsonb,$7 WHERE $7 > clock_timestamp() AND $7 <= clock_timestamp()+interval '365 days' AND EXISTS(SELECT 1 FROM access.principal WHERE id=$2::uuid AND status='active' AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL)`, id, pid, name, r.secretFingerprint(tok), ver, caps, in.ExpiresAt.UTC())
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
	var t access.APIToken
	var caps []byte
	var exp, created, last, rev *time.Time
	err := db.QueryRow(ctx, `SELECT id::text,principal_id::text,name,capabilities::text,expires_at,created_at,last_used_at,revoked_at FROM access.api_token WHERE id=$1::uuid`, id).Scan(&t.ID, &t.PrincipalID, &t.Name, &caps, &exp, &created, &last, &rev)
	if err != nil {
		return t, err
	}
	if string(caps) != "" && string(caps) != "null" {
		_ = json.Unmarshal(caps, &t.Capabilities)
	}
	t.ExpiresAt = formatTimePtr(exp)
	t.CreatedAt = formatTimePtr(created)
	t.LastUsedAt = formatTimePtr(last)
	t.RevokedAt = formatTimePtr(rev)
	return t, nil
}
func (r *Repository) apiTokenForSecret(ctx context.Context, secret string) (access.APIToken, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.APIToken{}, err
	}
	var id string
	var verifier []byte
	err = db.QueryRow(ctx, `SELECT t.id::text,t.verifier FROM access.api_token t JOIN access.principal p ON p.id=t.principal_id WHERE t.token_fingerprint=$1 AND t.revoked_at IS NULL AND t.expires_at>clock_timestamp() AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL`, r.secretFingerprint(secret)).Scan(&id, &verifier)
	if err != nil {
		return access.APIToken{}, err
	}
	if !verifySecret(secret, verifier) {
		return access.APIToken{}, pgx.ErrNoRows
	}
	_, _ = db.Exec(ctx, `UPDATE access.api_token SET last_used_at=clock_timestamp() WHERE id=$1::uuid`, id)
	return r.apiToken(ctx, id)
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
	rows, err := db.Query(ctx, `SELECT id::text FROM access.api_token WHERE principal_id=$1::uuid ORDER BY created_at DESC LIMIT $2`, pid, maxPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []access.APIToken{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		t, e := r.apiToken(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
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
	tag, e := db.Exec(ctx, `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
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
	tag, e := db.Exec(ctx, `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE id=$1::uuid AND principal_id=$2::uuid AND revoked_at IS NULL`, id, pid)
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
	tag, e := db.Exec(ctx, `INSERT INTO access.service_principal_secret(id,service_principal_id,name,secret_fingerprint,verifier,expires_at) SELECT $1::uuid,$2::uuid,$3,$4,$5,$6 WHERE $6 > clock_timestamp() AND $6 <= clock_timestamp()+interval '365 days' AND EXISTS(SELECT 1 FROM access.principal WHERE id=$2::uuid AND principal_type='service' AND status='active' AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL)`, id, pid, name, r.secretFingerprint(secret), ver, in.ExpiresAt.UTC())
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
	var s access.ServicePrincipalSecret
	var exp, created, rev *time.Time
	err := db.QueryRow(ctx, `SELECT id::text,service_principal_id::text,name,expires_at,created_at,revoked_at FROM access.service_principal_secret WHERE id=$1::uuid`, id).Scan(&s.ID, &s.ServicePrincipalID, &s.Name, &exp, &created, &rev)
	s.ExpiresAt = formatTimePtr(exp)
	s.CreatedAt = formatTimePtr(created)
	s.RevokedAt = formatTimePtr(rev)
	return s, err
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
	tag, e := db.Exec(ctx, `UPDATE access.service_principal_secret SET revoked_at=clock_timestamp() WHERE id=$1::uuid AND service_principal_id=$2::uuid AND revoked_at IS NULL`, sid, pid)
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
	var id string
	var ver []byte
	e = db.QueryRow(ctx, `SELECT s.service_principal_id::text,s.verifier FROM access.service_principal_secret s JOIN access.principal p ON p.id=s.service_principal_id WHERE s.service_principal_id=$1::uuid AND s.secret_fingerprint=$2 AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL`, pid, r.secretFingerprint(secret)).Scan(&id, &ver)
	if e != nil {
		return access.Principal{}, e
	}
	if !verifySecret(secret, ver) {
		return access.Principal{}, pgx.ErrNoRows
	}
	p, e := r.PrincipalByID(ctx, id)
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
	err = db.QueryRow(ctx, `SELECT id::text FROM access.principal WHERE lower(email)=lower($1) AND revoked_at IS NULL ORDER BY created_at LIMIT 1`, email).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = newUUID()
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
	err := tx.QueryRow(ctx, `SELECT principal_id::text FROM access.external_identity WHERE provider=$1 AND tenant_id=$2 AND subject=$3 AND revoked_at IS NULL FOR UPDATE`, provider, tenant, subject).Scan(&pid)
	if errors.Is(err, pgx.ErrNoRows) {
		pid, e = newUUID()
		if e != nil {
			return access.Principal{}, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO access.principal(id,principal_type,status,email,display_name) VALUES($1,'user','active',$2,$3)`, pid, access.NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)); e != nil {
			return access.Principal{}, e
		}
		identity, ie := newUUID()
		if ie != nil {
			return access.Principal{}, ie
		}
		if _, e = tx.Exec(ctx, `INSERT INTO access.external_identity(id,principal_id,provider,tenant_id,subject,user_name,external_id,email,display_name) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, identity, pid, provider, tenant, subject, strings.TrimSpace(in.Email), "", access.NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)); e != nil {
			return access.Principal{}, e
		}
	} else if err != nil {
		return access.Principal{}, err
	} else {
		if _, e = tx.Exec(ctx, `UPDATE access.external_identity SET email=$4,display_name=$5,updated_at=clock_timestamp() WHERE provider=$1 AND tenant_id=$2 AND subject=$3 AND revoked_at IS NULL`, provider, tenant, subject, access.NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)); e != nil {
			return access.Principal{}, e
		}
		if _, e = tx.Exec(ctx, `UPDATE access.principal SET email=CASE WHEN $2='' THEN email ELSE $2 END,display_name=CASE WHEN $3='' THEN display_name ELSE $3 END,updated_at=clock_timestamp() WHERE id=$1::uuid`, pid, access.NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)); e != nil {
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
	tag, e := db.Exec(ctx, `INSERT INTO access.session(id,principal_id,token_fingerprint,verifier,expires_at,absolute_expires_at,kind,instance_id,profile_id,client_id) SELECT $1::uuid,$2::uuid,$3,$4,LEAST(clock_timestamp()+$5::interval,clock_timestamp()+$6::interval),clock_timestamp()+$6::interval,'desktop',$7,$8,'leapview-desktop' WHERE EXISTS(SELECT 1 FROM access.principal WHERE id=$2::uuid AND status='active' AND disabled_at IS NULL AND blocked_at IS NULL)`, id, pid, r.secretFingerprint(tok), ver, ttl.String(), access.DesktopSessionAbsoluteLifetime.String(), instanceID, profileID)
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
	var ver []byte
	if e = tx.QueryRow(ctx, `SELECT s.id::text,s.principal_id::text,s.instance_id,s.profile_id,s.client_id,s.verifier FROM access.session s JOIN access.principal p ON p.id=s.principal_id WHERE s.token_fingerprint=$1 AND s.kind='desktop' AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND s.last_seen_at>clock_timestamp()-$2::interval AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL FOR UPDATE`, r.secretFingerprint(tok), access.DesktopSessionIdleTimeout.String()).Scan(&s.SessionID, &s.PrincipalID, &s.InstanceID, &s.ProfileID, &s.ClientID, &ver); e != nil {
		return access.DesktopSession{}, e
	}
	if !verifySecret(tok, ver) {
		return access.DesktopSession{}, pgx.ErrNoRows
	}
	var exp, abs, created time.Time
	if e = tx.QueryRow(ctx, `UPDATE access.session SET last_seen_at=clock_timestamp(),expires_at=LEAST(absolute_expires_at,clock_timestamp()+$2::interval) WHERE id=$1::uuid AND token_fingerprint=$3 AND revoked_at IS NULL AND expires_at>clock_timestamp() AND last_seen_at>clock_timestamp()-$4::interval RETURNING expires_at,absolute_expires_at,created_at`, s.SessionID, access.DesktopSessionIdleTimeout.String(), r.secretFingerprint(tok), access.DesktopSessionIdleTimeout.String()).Scan(&exp, &abs, &created); e != nil {
		return access.DesktopSession{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return access.DesktopSession{}, e
	}
	s.ExpiresAt = formatTime(exp)
	s.AbsoluteExpiresAt = formatTime(abs)
	s.CreatedAt = formatTime(created)
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
	var id string
	var ver []byte
	if e = tx.QueryRow(ctx, `SELECT s.id::text,s.verifier FROM access.session s JOIN access.principal p ON p.id=s.principal_id WHERE s.token_fingerprint=$1 AND s.kind='desktop' AND s.instance_id=$2 AND s.profile_id=$3 AND s.revoked_at IS NULL AND p.status='active' AND p.revoked_at IS NULL AND p.disabled_at IS NULL AND p.blocked_at IS NULL FOR UPDATE`, r.secretFingerprint(tok), instanceID, profileID).Scan(&id, &ver); e != nil {
		return e
	}
	if !verifySecret(tok, ver) {
		return pgx.ErrNoRows
	}
	tag, e := tx.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
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
