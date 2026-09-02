package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
)

var _ access.PrincipalIdentityManagementRepository = (*Repository)(nil)

// PrincipalIdentityManagement reports the durable source of profile
// authority. External principals may still have a local password credential.
func (r *Repository) PrincipalIdentityManagement(ctx context.Context, id string) (access.PrincipalIdentityManagement, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.PrincipalIdentityManagement{}, err
	}
	id, err = uuidID("principal id", id)
	if err != nil {
		return access.PrincipalIdentityManagement{}, err
	}
	if _, err = r.PrincipalByID(ctx, id); err != nil {
		return access.PrincipalIdentityManagement{}, err
	}
	var provider string
	var kind string
	var local bool
	err = db.QueryRow(ctx, `SELECT COALESCE((SELECT provider FROM access.external_identity WHERE principal_id=p.id AND revoked_at IS NULL ORDER BY created_at LIMIT 1),''),p.principal_type,EXISTS(SELECT 1 FROM access.local_credential c WHERE c.principal_id=p.id AND c.revoked_at IS NULL) FROM access.principal p WHERE p.id=$1::uuid`, id).Scan(&provider, &kind, &local)
	if err != nil {
		return access.PrincipalIdentityManagement{}, err
	}
	if provider != "" {
		return access.PrincipalIdentityManagement{Source: access.IdentityManagementExternal, Provider: provider, HasLocalPassword: local}, nil
	}
	if kind == "service" {
		return access.PrincipalIdentityManagement{Source: access.IdentityManagementSystem, HasLocalPassword: local}, nil
	}
	return access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: local}, nil
}

func (r *Repository) UpsertSCIMUser(ctx context.Context, in access.SCIMUserInput) (access.SCIMUser, error) {
	if strings.TrimSpace(in.ExternalID) == "" && strings.TrimSpace(in.UserName) == "" {
		return access.SCIMUser{}, fmt.Errorf("scim user requires external id or userName")
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.SCIMUser{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	subject := firstNonEmpty(strings.TrimSpace(in.ExternalID), strings.TrimSpace(in.UserName))
	var pid string
	err = tx.QueryRow(ctx, `SELECT principal_id::text FROM access.external_identity WHERE provider='scim' AND tenant_id='' AND subject=$1 AND revoked_at IS NULL FOR UPDATE`, subject).Scan(&pid)
	if err == pgx.ErrNoRows {
		if strings.TrimSpace(in.ID) != "" {
			pid, err = uuidID("principal id", in.ID)
		} else {
			pid, err = newUUID()
		}
		if err != nil {
			return access.SCIMUser{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO access.principal(id,principal_type,status,email,display_name) VALUES($1,'user',$2,$3,$4) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,email=EXCLUDED.email,display_name=EXCLUDED.display_name,disabled_at=CASE WHEN EXCLUDED.status='disabled' THEN COALESCE(access.principal.disabled_at,clock_timestamp()) ELSE NULL END,updated_at=clock_timestamp()`, pid, scimStatus(in.Active), access.NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)); err != nil {
			return access.SCIMUser{}, err
		}
		identity, e := newUUID()
		if e != nil {
			return access.SCIMUser{}, e
		}
		if _, err = tx.Exec(ctx, `INSERT INTO access.external_identity(id,principal_id,provider,tenant_id,subject,user_name,external_id,email,display_name) VALUES($1,$2,'scim','',$3,$4,$5,$6,$7)`, identity, pid, subject, strings.TrimSpace(in.UserName), strings.TrimSpace(in.ExternalID), access.NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)); err != nil {
			return access.SCIMUser{}, err
		}
	} else if err != nil {
		return access.SCIMUser{}, err
	} else {
		if _, err = tx.Exec(ctx, `UPDATE access.external_identity SET user_name=$4,external_id=$5,email=$6,display_name=$7,updated_at=clock_timestamp() WHERE provider='scim' AND tenant_id='' AND subject=$1 AND revoked_at IS NULL`, subject, "", "", strings.TrimSpace(in.UserName), strings.TrimSpace(in.ExternalID), access.NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)); err != nil {
			return access.SCIMUser{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE access.principal SET status=$2,email=CASE WHEN $3='' THEN email ELSE $3 END,display_name=CASE WHEN $4='' THEN display_name ELSE $4 END,disabled_at=CASE WHEN $2='disabled' THEN COALESCE(disabled_at,clock_timestamp()) ELSE NULL END,updated_at=clock_timestamp() WHERE id=$1::uuid`, pid, scimStatus(in.Active), access.NormalizeEmail(in.Email), strings.TrimSpace(in.DisplayName)); err != nil {
			return access.SCIMUser{}, err
		}
	}
	if ownTx {
		if err = tx.Commit(ctx); err != nil {
			return access.SCIMUser{}, err
		}
	}
	var p access.Principal
	if ownTx {
		p, err = r.PrincipalByID(ctx, pid)
	} else {
		p, err = (&Repository{db: tx, fingerprintKey: r.fingerprintKey}).PrincipalByID(ctx, pid)
	}
	return access.SCIMUser{Principal: p, ExternalID: in.ExternalID}, err
}

func scimStatus(active bool) string {
	if active {
		return "active"
	}
	return "disabled"
}

func (r *Repository) ListSCIMUsers(ctx context.Context, f access.SCIMUserFilter) ([]access.SCIMUser, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT p.id::text,p.principal_type,p.status,COALESCE(p.email,''),p.display_name,p.disabled_at,p.blocked_at,p.last_seen_at,p.created_at,p.updated_at,ei.external_id FROM access.external_identity ei JOIN access.principal p ON p.id=ei.principal_id WHERE ei.provider='scim' AND ei.revoked_at IS NULL AND ($1='' OR p.id::text=$1) AND ($2='' OR ei.external_id=$2) AND ($3='' OR ei.user_name=$3) ORDER BY p.created_at LIMIT $4`, strings.TrimSpace(f.ID), strings.TrimSpace(f.ExternalID), strings.TrimSpace(f.UserName), maxPageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []access.SCIMUser{}
	for rows.Next() {
		var p access.Principal
		var kind, status, external string
		var disabled, blocked, last, created, updated *time.Time
		if err := rows.Scan(&p.ID, &kind, &status, &p.Email, &p.DisplayName, &disabled, &blocked, &last, &created, &updated, &external); err != nil {
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
		out = append(out, access.SCIMUser{Principal: p, ExternalID: external})
	}
	return out, rows.Err()
}

func (r *Repository) DisableSCIMUser(ctx context.Context, id string) (access.SCIMUser, error) {
	id, err := uuidID("principal id", id)
	if err != nil {
		return access.SCIMUser{}, err
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.SCIMUser{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.external_identity WHERE principal_id=$1::uuid AND provider='scim' AND revoked_at IS NULL)`, id).Scan(&exists); err != nil {
		return access.SCIMUser{}, err
	}
	if !exists {
		return access.SCIMUser{}, pgx.ErrNoRows
	}
	if _, err = tx.Exec(ctx, `UPDATE access.principal SET status='disabled',disabled_at=COALESCE(disabled_at,clock_timestamp()),updated_at=clock_timestamp() WHERE id=$1::uuid`, id); err != nil {
		return access.SCIMUser{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.session SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return access.SCIMUser{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return access.SCIMUser{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.service_principal_secret SET revoked_at=clock_timestamp() WHERE service_principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return access.SCIMUser{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.local_credential SET revoked_at=clock_timestamp() WHERE principal_id=$1::uuid AND revoked_at IS NULL`, id); err != nil {
		return access.SCIMUser{}, err
	}
	if ownTx {
		if err = tx.Commit(ctx); err != nil {
			return access.SCIMUser{}, err
		}
	}
	var p access.Principal
	if ownTx {
		p, err = r.PrincipalByID(ctx, id)
	} else {
		p, err = (&Repository{db: tx, fingerprintKey: r.fingerprintKey}).PrincipalByID(ctx, id)
	}
	return access.SCIMUser{Principal: p}, err
}

func (r *Repository) UpsertSCIMGroup(ctx context.Context, in access.SCIMGroupInput) (access.Group, error) {
	external := strings.TrimSpace(in.ExternalID)
	if len(external) > 512 {
		return access.Group{}, fmt.Errorf("external id exceeds bounds")
	}
	name, e := bounded(strings.TrimSpace(in.Name), "group name", 255)
	if e != nil {
		return access.Group{}, e
	}
	memberIDs := make([]string, 0, len(in.MemberIDs))
	for _, member := range in.MemberIDs {
		pid, e := uuidID("principal id", member)
		if e != nil {
			return access.Group{}, e
		}
		memberIDs = append(memberIDs, pid)
	}
	tx, ownTx, err := r.txOrBegin(ctx)
	if err != nil {
		return access.Group{}, err
	}
	defer func() {
		if ownTx {
			_ = tx.Rollback(ctx)
		}
	}()
	id := strings.TrimSpace(in.ID)
	if id == "" {
		id, err = newUUID()
	} else {
		id, err = uuidID("group id", id)
	}
	if err != nil {
		return access.Group{}, err
	}
	var gid string
	if external != "" {
		_ = tx.QueryRow(ctx, `SELECT id::text FROM access.access_group WHERE provider='scim' AND external_id=$1 AND revoked_at IS NULL FOR UPDATE`, external).Scan(&gid)
	}
	if gid == "" {
		gid = id
		if _, err = tx.Exec(ctx, `INSERT INTO access.access_group(id,name,provider,external_id) VALUES($1,$2,'scim',$3)`, gid, name, external); err != nil {
			return access.Group{}, err
		}
	} else if _, err = tx.Exec(ctx, `UPDATE access.access_group SET name=$2 WHERE id=$1::uuid AND revoked_at IS NULL`, gid, name); err != nil {
		return access.Group{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE access.principal_group SET revoked_at=clock_timestamp() WHERE group_id=$1::uuid AND revoked_at IS NULL AND NOT (principal_id = ANY($2::uuid[]))`, gid, memberIDs); err != nil {
		return access.Group{}, err
	}
	for _, pid := range memberIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO access.principal_group(group_id,principal_id) SELECT $1::uuid,$2::uuid WHERE NOT EXISTS(SELECT 1 FROM access.principal_group WHERE group_id=$1::uuid AND principal_id=$2::uuid AND revoked_at IS NULL) ON CONFLICT (principal_id,group_id) WHERE revoked_at IS NULL DO NOTHING`, gid, pid); err != nil {
			return access.Group{}, err
		}
	}
	if ownTx {
		if err = tx.Commit(ctx); err != nil {
			return access.Group{}, err
		}
	}
	if ownTx {
		return r.groupByID(ctx, gid, "scim", external)
	}
	return (&Repository{db: tx, fingerprintKey: r.fingerprintKey}).groupByID(ctx, gid, "scim", external)
}

func (r *Repository) ListSCIMGroups(ctx context.Context, f access.SCIMGroupFilter) ([]access.Group, error) {
	db, e := r.requireDB()
	if e != nil {
		return nil, e
	}
	rows, e := db.Query(ctx, `SELECT id::text,provider,external_id,name,created_at FROM access.access_group WHERE provider='scim' AND revoked_at IS NULL AND ($1='' OR id::text=$1) AND ($2='' OR external_id=$2) AND ($3='' OR name ILIKE '%'||$3||'%') ORDER BY name LIMIT $4`, strings.TrimSpace(f.ID), strings.TrimSpace(f.ExternalID), strings.TrimSpace(f.DisplayName), maxPageSize)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []access.Group{}
	for rows.Next() {
		var g access.Group
		var t time.Time
		if e := rows.Scan(&g.ID, &g.Provider, &g.ExternalID, &g.Name, &t); e != nil {
			return nil, e
		}
		g.CreatedAt = formatTime(t)
		out = append(out, g)
	}
	return out, rows.Err()
}
func (r *Repository) DeleteSCIMGroup(ctx context.Context, id string) error {
	db, e := r.requireDB()
	if e != nil {
		return e
	}
	id, e = uuidID("group id", id)
	if e != nil {
		return e
	}
	tag, e := db.Exec(ctx, `UPDATE access.access_group SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1::uuid AND provider='scim' AND revoked_at IS NULL`, id)
	if e == nil && tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return e
}
func (r *Repository) AddSCIMGroupMember(ctx context.Context, gid, pid string) error {
	return r.scimMember(ctx, gid, pid, true)
}
func (r *Repository) RemoveSCIMGroupMember(ctx context.Context, gid, pid string) error {
	return r.scimMember(ctx, gid, pid, false)
}
func (r *Repository) scimMember(ctx context.Context, gid, pid string, add bool) error {
	db, e := r.requireDB()
	if e != nil {
		return e
	}
	gid, e = uuidID("group id", gid)
	if e != nil {
		return e
	}
	pid, e = uuidID("principal id", pid)
	if e != nil {
		return e
	}
	var provider string
	if e = db.QueryRow(ctx, `SELECT provider FROM access.access_group WHERE id=$1::uuid AND revoked_at IS NULL`, gid).Scan(&provider); e != nil {
		return e
	}
	if provider != "scim" {
		return fmt.Errorf("group is not SCIM-managed")
	}
	return r.groupMember(ctx, gid, pid, add)
}
func (r *Repository) ListSCIMGroupMembers(ctx context.Context, gid string) ([]access.GroupMember, error) {
	return r.listMembers(ctx, gid)
}
