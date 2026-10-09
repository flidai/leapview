package managedrecovery

import (
	"context"
	"errors"
	"strings"

	jobpostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ManagedAuthorityInitializationRequest struct {
	SystemIdentifier string
	Database         string
	OwnerRole        string
	OperatorRole     string
	OperatorPassword string
}

type ManagedAuthorityInitializationReceipt struct {
	SchemaVersion    int    `json:"schemaVersion"`
	SystemIdentifier string `json:"systemIdentifier"`
	Database         string `json:"database"`
	OwnerRole        string `json:"ownerRole"`
	OperatorRole     string `json:"operatorRole"`
	SchemaDigest     string `json:"schemaDigest"`
	Status           string `json:"status"`
}

// InitializeManagedAuthority installs maintained capability schemas in one
// explicit empty independent database. It creates a dedicated non-login owner
// and a narrowly granted operator, without altering application runtime roles.
func InitializeManagedAuthority(ctx context.Context, pool *pgxpool.Pool, request ManagedAuthorityInitializationRequest) (ManagedAuthorityInitializationReceipt, error) {
	if pool == nil || request.SystemIdentifier == "" || request.Database == "" || !dedicatedRecoveryRole(request.OwnerRole, "leapview_recovery_owner") || !dedicatedRecoveryRole(request.OperatorRole, "leapview_recovery_operator") || request.OperatorPassword == "" || strings.ContainsRune(request.OperatorPassword, 0) {
		return ManagedAuthorityInitializationReceipt{}, errors.New("explicit dedicated recovery authority roles and credentials required")
	}
	receipt := authorityInitializationReceipt(request)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return receipt, errors.New("recovery authority initialization transaction unavailable")
	}
	defer tx.Rollback(context.Background())
	// Serialize this database's initialization without locking an application instance.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(710924613)"); err != nil {
		return receipt, errors.New("recovery authority initialization lock unavailable")
	}
	var systemID, database, bootstrap string
	var version int
	if err := tx.QueryRow(ctx, "SELECT system_identifier::text,current_database(),session_user,current_setting('server_version_num')::integer FROM pg_control_system()").Scan(&systemID, &database, &bootstrap, &version); err != nil || systemID != request.SystemIdentifier || database != request.Database || version < 180000 || version >= 190000 {
		return receipt, errors.New("exact independent PostgreSQL18 authority identity required")
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='managed_recovery_authority')").Scan(&exists); err != nil {
		return receipt, errors.New("recovery authority schema identity unavailable")
	}
	if exists {
		var stored ManagedAuthorityInitializationReceipt
		if err := tx.QueryRow(ctx, "SELECT schema_version,system_identifier,database_name,owner_role,operator_role,schema_digest,status FROM managed_recovery_authority.identity WHERE singleton").Scan(&stored.SchemaVersion, &stored.SystemIdentifier, &stored.Database, &stored.OwnerRole, &stored.OperatorRole, &stored.SchemaDigest, &stored.Status); err != nil || stored != receipt {
			return receipt, errors.New("existing authority differs from exact initialization identity")
		}
		if err := verifyDedicatedAuthorityRoles(ctx, tx, request); err != nil {
			return receipt, err
		}
		return receipt, tx.Commit(ctx)
	}
	var occupied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('public','information_schema') AND nspname NOT LIKE 'pg_%')
 OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public')
 OR EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public')
 OR EXISTS(SELECT 1 FROM pg_roles WHERE rolname IN ($1,$2))`, request.OwnerRole, request.OperatorRole).Scan(&occupied); err != nil || occupied {
		return receipt, errors.New("authority initialization requires an empty database and unclaimed dedicated roles")
	}
	owner, operator, admin, db := pgx.Identifier{request.OwnerRole}.Sanitize(), pgx.Identifier{request.OperatorRole}.Sanitize(), pgx.Identifier{bootstrap}.Sanitize(), pgx.Identifier{database}.Sanitize()
	statements := []string{
		"SET LOCAL standard_conforming_strings=on",
		"CREATE ROLE " + owner + " NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
		"CREATE ROLE " + operator + " LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + strings.ReplaceAll(request.OperatorPassword, "'", "''") + "'",
		"GRANT " + owner + " TO " + admin + " WITH INHERIT FALSE, SET TRUE",
		"GRANT CREATE ON DATABASE " + db + " TO " + owner,
		"SET LOCAL ROLE " + owner,
	}
	for _, sql := range statements {
		if _, err := tx.Exec(ctx, sql); err != nil {
			return receipt, errors.New("dedicated recovery authority roles could not be initialized")
		}
	}
	if err := jobpostgres.ApplySchema(ctx, tx); err != nil {
		return receipt, errors.New("maintained authority job-history dependency could not be installed")
	}
	if err := refreshpostgres.ApplySchema(ctx, tx); err != nil {
		return receipt, errors.New("maintained authority ledger schema could not be installed")
	}
	if err := recoverypostgres.ApplySchema(ctx, tx); err != nil {
		return receipt, errors.New("maintained recovery frontier schema could not be installed")
	}
	// Existing capability schemas conditionally grant standard application roles.
	// This clean independent database retains only its dedicated authority grants.
	rows, err := tx.Query(ctx, `SELECT DISTINCT pg_get_userbyid(a.grantee) FROM pg_namespace n CROSS JOIN LATERAL aclexplode(n.nspacl) a
 WHERE n.nspname IN ('jobs','refresh','recovery') AND a.grantee<>0 AND a.grantee<>n.nspowner`)
	if err != nil {
		return receipt, errors.New("authority grant inventory unavailable")
	}
	var grantees []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			rows.Close()
			return receipt, errors.New("authority grant inventory invalid")
		}
		grantees = append(grantees, role)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return receipt, errors.New("authority grant inventory unavailable")
	}
	rows.Close()
	grantees = append(grantees, "PUBLIC")
	for _, role := range grantees {
		identity := pgx.Identifier{role}.Sanitize()
		if role == "PUBLIC" {
			identity = "PUBLIC"
		}
		for _, sql := range []string{"REVOKE ALL ON SCHEMA jobs,refresh,recovery FROM " + identity, "REVOKE ALL ON ALL TABLES IN SCHEMA jobs,refresh,recovery FROM " + identity, "REVOKE ALL ON ALL SEQUENCES IN SCHEMA jobs,refresh,recovery FROM " + identity, "REVOKE ALL ON ALL FUNCTIONS IN SCHEMA jobs,refresh,recovery FROM " + identity} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return receipt, errors.New("authority grants could not be restricted")
			}
		}
	}
	for _, sql := range []string{
		"CREATE SCHEMA managed_recovery_authority AUTHORIZATION " + owner,
		"REVOKE ALL ON SCHEMA managed_recovery_authority FROM PUBLIC",
		`CREATE TABLE managed_recovery_authority.identity(singleton boolean PRIMARY KEY CHECK(singleton),schema_version integer NOT NULL,system_identifier text NOT NULL,database_name text NOT NULL,owner_role text NOT NULL,operator_role text NOT NULL,schema_digest text NOT NULL,status text NOT NULL)`,
		"REVOKE ALL ON managed_recovery_authority.identity FROM PUBLIC",
		"GRANT USAGE ON SCHEMA recovery,refresh,managed_recovery_authority TO " + operator,
		"GRANT SELECT,INSERT,UPDATE ON recovery.recovery_set,recovery.validation_attempt,refresh.recovery_qualification_occurrence,refresh.recovery_qualification_attempt,refresh.recovery_qualification_evidence_attempt TO " + operator,
		"GRANT SELECT,INSERT ON recovery.recovery_cluster_point,recovery.recovery_object_root,recovery.validation_result TO " + operator,
		"GRANT EXECUTE ON FUNCTION recovery.canonical_json_string(text) TO " + operator,
		"GRANT SELECT ON managed_recovery_authority.identity TO " + operator + "," + admin,
		"GRANT USAGE ON SCHEMA managed_recovery_authority TO " + admin,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			return receipt, errors.New("exact authority operator grants could not be installed")
		}
	}
	if _, err := tx.Exec(ctx, "INSERT INTO managed_recovery_authority.identity VALUES(true,$1,$2,$3,$4,$5,$6,$7)", receipt.SchemaVersion, receipt.SystemIdentifier, receipt.Database, receipt.OwnerRole, receipt.OperatorRole, receipt.SchemaDigest, receipt.Status); err != nil {
		return receipt, errors.New("authority identity could not be persisted")
	}
	for _, sql := range []string{"RESET ROLE", "REVOKE CREATE ON SCHEMA public FROM PUBLIC", "REVOKE CREATE ON DATABASE " + db + " FROM PUBLIC", "GRANT CONNECT ON DATABASE " + db + " TO " + operator, "GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO " + operator, "REVOKE CREATE ON DATABASE " + db + " FROM " + owner, "REVOKE " + owner + " FROM " + admin} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			return receipt, errors.New("authority initialization privilege boundary could not be finalized")
		}
	}
	if err := verifyDedicatedAuthorityRoles(ctx, tx, request); err != nil {
		return receipt, err
	}
	if err := tx.Commit(ctx); err != nil {
		return receipt, errors.New("authority initialization commit unconfirmed; retry exact input")
	}
	return receipt, nil
}

func dedicatedRecoveryRole(value, prefix string) bool {
	if value != prefix && !strings.HasPrefix(value, prefix+"_") {
		return false
	}
	if len(value) > 63 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func verifyDedicatedAuthorityRoles(ctx context.Context, tx pgx.Tx, request ManagedAuthorityInitializationRequest) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls
 AND rolcanlogin=(rolname=$2)) AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member IN (SELECT oid FROM pg_roles WHERE rolname IN ($1,$2)))
 FROM pg_roles WHERE rolname IN ($1,$2)`, request.OwnerRole, request.OperatorRole).Scan(&valid)
	if err != nil || !valid {
		return errors.New("dedicated recovery authority roles gained ambient privilege or membership")
	}
	return nil
}

func authorityInitializationReceipt(request ManagedAuthorityInitializationRequest) ManagedAuthorityInitializationReceipt {
	return ManagedAuthorityInitializationReceipt{SchemaVersion: 1, SystemIdentifier: request.SystemIdentifier, Database: request.Database, OwnerRole: request.OwnerRole, OperatorRole: request.OperatorRole, SchemaDigest: digestBytes([]byte(jobpostgres.SchemaSQL() + "\n" + refreshpostgres.SchemaSQL() + "\n" + recoverypostgres.SchemaSQL())), Status: "initialized"}
}
