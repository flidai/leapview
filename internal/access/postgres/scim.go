package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	parsedID, err := pgUUID(id)
	if err != nil {
		return access.PrincipalIdentityManagement{}, err
	}
	row, err := accessdb.New(db).GetPrincipalIdentityManagement(ctx, parsedID)
	if err != nil {
		return access.PrincipalIdentityManagement{}, err
	}
	if row.Provider != "" {
		return access.PrincipalIdentityManagement{Source: access.IdentityManagementExternal, Provider: row.Provider, HasLocalPassword: row.HasLocalPassword}, nil
	}
	if row.PrincipalType == "service" {
		return access.PrincipalIdentityManagement{Source: access.IdentityManagementSystem, HasLocalPassword: row.HasLocalPassword}, nil
	}
	return access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: row.HasLocalPassword}, nil
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
	if err = accessdb.New(tx).LockSCIMSubject(ctx, subject); err != nil {
		return access.SCIMUser{}, err
	}
	var principalID pgtype.UUID
	principalID, err = accessdb.New(tx).FindSCIMPrincipalBySubject(ctx, subject)
	pid := ""
	if err == nil {
		pid = principalUUID(principalID)
	} else if err == pgx.ErrNoRows {
		if strings.TrimSpace(in.ID) != "" {
			pid, err = uuidID("principal id", in.ID)
		} else {
			pid, err = newUUID()
		}
		if err != nil {
			return access.SCIMUser{}, err
		}
		principalID, err = pgUUID(pid)
		if err != nil {
			return access.SCIMUser{}, err
		}
		if err = accessdb.New(tx).InsertSCIMPrincipal(ctx, accessdb.InsertSCIMPrincipalParams{ID: principalID, Status: scimStatus(in.Active), Email: access.NormalizeEmail(in.Email), DisplayName: strings.TrimSpace(in.DisplayName)}); err != nil {
			return access.SCIMUser{}, err
		}
		identity, e := newUUID()
		if e != nil {
			return access.SCIMUser{}, e
		}
		identityID, parseErr := pgUUID(identity)
		if parseErr != nil {
			return access.SCIMUser{}, parseErr
		}
		if err = accessdb.New(tx).InsertSCIMExternalIdentity(ctx, accessdb.InsertSCIMExternalIdentityParams{ID: identityID, PrincipalID: principalID, Subject: subject, UserName: strings.TrimSpace(in.UserName), ExternalID: strings.TrimSpace(in.ExternalID), Email: access.NormalizeEmail(in.Email), DisplayName: strings.TrimSpace(in.DisplayName)}); err != nil {
			return access.SCIMUser{}, err
		}
	} else if err != nil {
		return access.SCIMUser{}, err
	} else {
		if err = accessdb.New(tx).UpdateSCIMExternalIdentity(ctx, accessdb.UpdateSCIMExternalIdentityParams{UserName: strings.TrimSpace(in.UserName), ExternalID: strings.TrimSpace(in.ExternalID), Email: access.NormalizeEmail(in.Email), DisplayName: strings.TrimSpace(in.DisplayName), Subject: subject}); err != nil {
			return access.SCIMUser{}, err
		}
		if err = accessdb.New(tx).UpdateSCIMPrincipal(ctx, accessdb.UpdateSCIMPrincipalParams{ID: principalID, Status: scimStatus(in.Active), Email: access.NormalizeEmail(in.Email), DisplayName: strings.TrimSpace(in.DisplayName)}); err != nil {
			return access.SCIMUser{}, err
		}
	}
	if !in.Active {
		if err = revokeSCIMPrincipalCredentials(ctx, tx, principalID); err != nil {
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

// revokeSCIMPrincipalCredentials applies the same durable deactivation
// cascade whether SCIM disabled a user during reconciliation or through the
// explicit disable endpoint. Each leaf is generated SQLC and runs on the
// caller's transaction.
func revokeSCIMPrincipalCredentials(ctx context.Context, db DBTX, principalID pgtype.UUID) error {
	queries := accessdb.New(db)
	if err := queries.RevokePrincipalSessions(ctx, principalID); err != nil {
		return err
	}
	if err := queries.RevokePrincipalTokens(ctx, principalID); err != nil {
		return err
	}
	if err := queries.RevokePrincipalSecrets(ctx, principalID); err != nil {
		return err
	}
	if err := queries.RevokePrincipalLocalCredential(ctx, principalID); err != nil {
		return err
	}
	return queries.RevokePrincipalAuthoringSessions(ctx, principalID)
}

func (r *Repository) ListSCIMUsers(ctx context.Context, f access.SCIMUserFilter) ([]access.SCIMUser, error) {
	db, err := r.requireDB()
	if err != nil {
		return nil, err
	}
	rows, err := accessdb.New(db).ListSCIMUsers(ctx, accessdb.ListSCIMUsersParams{ID: strings.TrimSpace(f.ID), ExternalID: strings.TrimSpace(f.ExternalID), UserName: strings.TrimSpace(f.UserName), PageSize: maxPageSize})
	if err != nil {
		return nil, err
	}
	out := make([]access.SCIMUser, 0, len(rows))
	for _, row := range rows {
		p := access.Principal{ID: principalUUID(row.ID), Kind: access.PrincipalKind(row.PrincipalType), Email: row.Email, DisplayName: row.DisplayName}
		if row.PrincipalType == "service" {
			p.Kind = access.PrincipalKindServicePrincipal
		}
		p.DisabledAt = principalTimestamp(row.DisabledAt)
		p.BlockedAt = principalTimestamp(row.BlockedAt)
		p.LastSeenAt = principalTimestamp(row.LastSeenAt)
		p.CreatedAt = principalTimestamp(row.CreatedAt)
		p.UpdatedAt = principalTimestamp(row.UpdatedAt)
		out = append(out, access.SCIMUser{Principal: p, ExternalID: row.ExternalID})
	}
	return out, nil
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
	principalID, err := pgUUID(id)
	if err != nil {
		return access.SCIMUser{}, err
	}
	exists, err := accessdb.New(tx).HasSCIMIdentity(ctx, principalID)
	if err != nil {
		return access.SCIMUser{}, err
	}
	if !exists {
		return access.SCIMUser{}, pgx.ErrNoRows
	}
	if _, err = accessdb.New(tx).DisablePrincipal(ctx, principalID); err != nil {
		return access.SCIMUser{}, err
	}
	if err = revokeSCIMPrincipalCredentials(ctx, tx, principalID); err != nil {
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
	memberPGIDs := make([]pgtype.UUID, 0, len(in.MemberIDs))
	for _, member := range in.MemberIDs {
		pid, e := uuidID("principal id", member)
		if e != nil {
			return access.Group{}, e
		}
		parsed, parseErr := pgUUID(pid)
		if parseErr != nil {
			return access.Group{}, parseErr
		}
		memberPGIDs = append(memberPGIDs, parsed)
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
	var groupID pgtype.UUID
	var gid string
	if external != "" {
		if err = accessdb.New(tx).LockSCIMGroupExternal(ctx, external); err != nil {
			return access.Group{}, err
		}
		groupID, err = accessdb.New(tx).FindSCIMGroupByExternal(ctx, external)
		if err == nil {
			gid = principalUUID(groupID)
		} else if err != pgx.ErrNoRows {
			return access.Group{}, err
		}
	}
	if gid == "" {
		gid = id
		groupID, err = pgUUID(gid)
		if err != nil {
			return access.Group{}, err
		}
		if err = accessdb.New(tx).InsertSCIMGroup(ctx, accessdb.InsertSCIMGroupParams{ID: groupID, Name: name, ExternalID: external}); err != nil {
			return access.Group{}, err
		}
	} else if err = accessdb.New(tx).UpdateSCIMGroup(ctx, accessdb.UpdateSCIMGroupParams{ID: groupID, Name: name}); err != nil {
		return access.Group{}, err
	}
	if err = accessdb.New(tx).RevokeSCIMGroupMembersExcept(ctx, accessdb.RevokeSCIMGroupMembersExceptParams{GroupID: groupID, MemberIds: memberPGIDs}); err != nil {
		return access.Group{}, err
	}
	for _, pid := range memberPGIDs {
		if err = accessdb.New(tx).AddGroupMember(ctx, accessdb.AddGroupMemberParams{GroupID: groupID, PrincipalID: pid}); err != nil {
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
	rows, e := accessdb.New(db).ListSCIMGroups(ctx, accessdb.ListSCIMGroupsParams{ID: strings.TrimSpace(f.ID), ExternalID: strings.TrimSpace(f.ExternalID), Name: strings.TrimSpace(f.DisplayName), PageSize: maxPageSize})
	if e != nil {
		return nil, e
	}
	out := make([]access.Group, 0, len(rows))
	for _, row := range rows {
		out = append(out, access.Group{ID: principalUUID(row.ID), Provider: row.Provider, ExternalID: row.ExternalID, Name: row.Name, CreatedAt: principalTimestamp(row.CreatedAt)})
	}
	return out, nil
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
	parsedID, parseErr := pgUUID(id)
	if parseErr != nil {
		return parseErr
	}
	tag, e := accessdb.New(db).RevokeSCIMGroup(ctx, parsedID)
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
	parsedGroupID, parseErr := pgUUID(gid)
	if parseErr != nil {
		return parseErr
	}
	var provider string
	if provider, e = accessdb.New(db).GetSCIMGroupProvider(ctx, parsedGroupID); e != nil {
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
