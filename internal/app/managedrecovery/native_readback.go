package managedrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strconv"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	bootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/jackc/pgx/v5"
)

type NativePostgresReadback struct {
	Set            recoveryset.RecoverySet
	MetadataSchema string
	ControlURL     string
	DuckLakeURL    string
	RootCA         string
	Roles          RuntimeRoles
	Credentials    *ManagedCredentials
}

type NativePostgresEvidence struct {
	ControlDigest  string
	DuckLakeDigest string
	Catalog        recoveryset.CatalogCommit
}

// Verify reads the real native delivery and DuckLake metadata schemas as the
// configured TLS runtime accounts. It does not inspect fixture tables, select
// a latest publication, mint credentials, or grant traffic admission.
func (readback NativePostgresReadback) Verify(ctx context.Context) (NativePostgresEvidence, error) {
	return readback.verifyWithDial(ctx, nil)
}

func (readback NativePostgresReadback) verifyWithDial(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) (NativePostgresEvidence, error) {
	if readback.Set.Validate() != nil || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`).MatchString(readback.MetadataSchema) {
		return NativePostgresEvidence{}, errors.New("exact valid native recovery set and metadata schema required")
	}
	controlConfig, err := managedConnectionConfig(readback.ControlURL, readback.Roles.Control, readback.RootCA)
	if err != nil {
		return NativePostgresEvidence{}, err
	}
	duckConfig, err := managedConnectionConfig(readback.DuckLakeURL, readback.Roles.DuckLake, readback.RootCA)
	if err != nil {
		return NativePostgresEvidence{}, err
	}
	for _, point := range readback.Set.ClusterPoints {
		if (point.DatabaseRole == recoveryset.DatabaseControl && point.DatabaseIdentity != controlConfig.Database) || (point.DatabaseRole == recoveryset.DatabaseDuckLake && point.DatabaseIdentity != duckConfig.Database) {
			return NativePostgresEvidence{}, errors.New("runtime database differs from the selected recovery set")
		}
	}
	if dial != nil {
		controlConfig.DialFunc, duckConfig.DialFunc = dial, dial
		// The selected staging listener is loopback; preserve the admitted host
		// only as TLS server name, without resolving the original endpoint.
		lookup := func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
		controlConfig.LookupFunc, duckConfig.LookupFunc = lookup, lookup
	}
	control, err := pgx.ConnectConfig(ctx, controlConfig)
	if err != nil {
		return NativePostgresEvidence{}, errors.New("managed control TLS runtime connection failed")
	}
	defer control.Close(context.Background())
	duck, err := pgx.ConnectConfig(ctx, duckConfig)
	if err != nil {
		return NativePostgresEvidence{}, errors.New("managed DuckLake TLS runtime connection failed")
	}
	defer duck.Close(context.Background())
	return readback.verifyConnections(ctx, control, duck)
}

func verifyRuntimeConnection(ctx context.Context, connection *pgx.Conn, role, database string) error {
	var user, observedDatabase string
	var ssl, privileged bool
	if err := connection.QueryRow(ctx, `SELECT current_user,current_database(),
COALESCE((SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()),false),
EXISTS (SELECT 1 FROM pg_roles r WHERE
 (r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls)
 AND pg_has_role(current_user,r.oid,'MEMBER'))
OR EXISTS (SELECT 1 FROM pg_database d WHERE d.datname=current_database()
 AND (pg_has_role(current_user,d.datdba,'MEMBER') OR has_database_privilege(current_user,d.oid,'CREATE')))
OR EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema'
 AND (pg_has_role(current_user,n.nspowner,'MEMBER') OR has_schema_privilege(current_user,n.oid,'CREATE')))
OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND pg_has_role(current_user,c.relowner,'MEMBER'))
OR EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname !~ '^pg_' AND n.nspname<>'information_schema' AND pg_has_role(current_user,p.proowner,'MEMBER'))`).Scan(&user, &observedDatabase, &ssl, &privileged); err != nil || user != role || observedDatabase != database || !ssl || privileged {
		return errors.New("restored database did not authenticate the exact unprivileged TLS runtime role")
	}
	return nil
}

func (readback NativePostgresReadback) verifyConnections(ctx context.Context, control, duck *pgx.Conn) (NativePostgresEvidence, error) {
	var controlDatabase string
	for _, point := range readback.Set.ClusterPoints {
		if point.DatabaseRole == recoveryset.DatabaseControl {
			controlDatabase = point.DatabaseIdentity
		}
	}
	if err := verifyRuntimeConnection(ctx, control, readback.Roles.Control, controlDatabase); err != nil {
		return NativePostgresEvidence{}, err
	}
	if err := verifyRuntimeConnection(ctx, duck, readback.Roles.DuckLake, readback.Set.Catalog.CatalogDatabase); err != nil {
		return NativePostgresEvidence{}, err
	}
	if readback.Credentials != nil {
		owner := bootstrappostgres.New(control)
		instance, err := owner.ExistingInstanceID(ctx)
		if err != nil || instance != readback.Credentials.InstanceID || VerifyManagedKeyring(ctx, *readback.Credentials, owner) != nil {
			return NativePostgresEvidence{}, errors.New("restored native instance/customer owner or retained keyring differs")
		}
	}
	// A repeatable read-only transaction prevents publication/registry rows
	// changing between checks. Physical restore and live admission additionally
	// hold the existing primary fence; this reader itself never fences writers.
	tx, err := control.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return NativePostgresEvidence{}, errors.New("native control readback transaction unavailable")
	}
	defer tx.Rollback(context.Background())
	sealJSON, err := json.Marshal(readback.Set.Serving)
	if err != nil {
		return NativePostgresEvidence{}, err
	}
	var markerJSON []byte
	var project, environment, attemptID, metadataSchema string
	// sqlc-exception:managed-recovery-readback -- a read-only composition proof
	// joins exact retained native identities without adding another repository
	// or mutation authority. Every selected identity is a SQL parameter.
	err = tx.QueryRow(ctx, `SELECT b.commit_marker,t.project_id::text,t.environment,b.attempt_id::text,c.metadata_schema
FROM delivery.delivery_publication p
JOIN delivery.delivery_generation g ON g.generation_id=p.generation_id
JOIN delivery.delivery_snapshot_seal s ON s.seal_id=p.snapshot_seal_id
JOIN delivery.delivery_build_attempt b ON b.attempt_id=s.attempt_id
JOIN delivery.delivery_target t ON t.target_id=p.target_id
JOIN delivery.delivery_active_pointer a ON a.target_id=t.target_id
JOIN ducklake.catalog_identity c ON c.physical_pool_id=s.physical_pool_id
WHERE p.publication_id=$1::uuid AND p.target_id=$2 AND p.generation_id=$3::uuid
AND p.snapshot_seal_id=$4::uuid AND p.state='committed' AND p.result_target_revision=$5
AND t.target_revision=$5 AND a.publication_id=p.publication_id AND a.generation_id=g.generation_id
AND g.target_id=t.target_id AND g.snapshot_seal_id=s.seal_id AND g.candidate_id=p.candidate_id
AND g.plan_digest=s.plan_digest AND g.artifact_root=s.artifact_root AND g.artifact_root_digest=s.artifact_root_digest
AND g.serving_artifact_digest=s.serving_artifact_digest AND g.compiled_graph_digest=s.compiled_graph_digest
AND g.compiled_config_digest=s.compiled_config_digest AND g.security_domain_fingerprint=s.security_domain_fingerprint
AND b.state='committed' AND b.snapshot_id=s.ducklake_snapshot_id AND b.physical_pool_id=s.physical_pool_id
AND b.catalog_id=s.catalog_id AND b.request_digest=s.request_digest AND b.plan_digest=s.plan_digest
AND c.catalog_id=s.catalog_id AND c.catalog_uuid=s.catalog_uuid AND c.catalog_database=s.catalog_database
AND to_jsonb(s) @> $6::jsonb`, readback.Set.Delivery.PublicationID, readback.Set.Delivery.TargetID, readback.Set.Delivery.GenerationID, readback.Set.Serving.SealID, readback.Set.Delivery.TargetRevision, sealJSON).Scan(&markerJSON, &project, &environment, &attemptID, &metadataSchema)
	if err != nil || metadataSchema != readback.MetadataSchema {
		return NativePostgresEvidence{}, errors.New("native publication, seal, generation or catalog identity differs from the selected frontier")
	}
	var marker catalogartifact.CommitMarker
	if json.Unmarshal(markerJSON, &marker) != nil {
		return NativePostgresEvidence{}, errors.New("native control commit marker malformed")
	}
	canonicalMarker, err := marker.CanonicalJSON()
	if err != nil || marker.AttemptID != attemptID || marker.GenerationID != readback.Set.Delivery.GenerationID || marker.Project != project || marker.Environment != environment || marker.PhysicalPoolID != readback.Set.Serving.PhysicalPoolID || marker.RequestDigest != readback.Set.Serving.RequestDigest || marker.PlanDigest != readback.Set.Serving.PlanDigest {
		return NativePostgresEvidence{}, errors.New("native commit marker differs from retained publication")
	}
	duckTx, err := duck.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return NativePostgresEvidence{}, errors.New("native DuckLake readback transaction unavailable")
	}
	defer duckTx.Rollback(context.Background())
	schema := pgx.Identifier{readback.MetadataSchema}.Sanitize()
	var observedMarker, version string
	var matches int64
	// sqlc-exception:dynamic-identifier -- exact metadata schema is validated and
	// checked against the immutable control registration before interpolation.
	err = duckTx.QueryRow(ctx, `SELECT value,count(*) OVER () FROM `+schema+`.ducklake_metadata WHERE key='version' AND scope IS NULL AND scope_id IS NULL LIMIT 1`).Scan(&version, &matches)
	if err != nil || matches != 1 || version != strconv.FormatInt(readback.Set.Catalog.CatalogVersion, 10) || version != readback.Set.Serving.CatalogSchemaVersion {
		return NativePostgresEvidence{}, errors.New("native DuckLake catalog format differs from the selected seal")
	}
	// sqlc-exception:dynamic-identifier -- same authenticated metadata schema;
	// the exact sealed snapshot is a parameter and never a latest lookup.
	err = duckTx.QueryRow(ctx, `SELECT c.commit_extra_info FROM `+schema+`.ducklake_snapshot s JOIN `+schema+`.ducklake_snapshot_changes c ON c.snapshot_id=s.snapshot_id WHERE s.snapshot_id=$1`, readback.Set.Catalog.SnapshotID).Scan(&observedMarker)
	if err != nil || observedMarker != canonicalMarker {
		return NativePostgresEvidence{}, errors.New("native DuckLake snapshot lacks the exact retained control commit marker")
	}
	if err := duckTx.Commit(ctx); err != nil {
		return NativePostgresEvidence{}, errors.New("native DuckLake readback could not complete")
	}
	if err := tx.Commit(ctx); err != nil {
		return NativePostgresEvidence{}, errors.New("native control readback could not complete")
	}
	controlValue, _ := json.Marshal(struct {
		Delivery recoveryset.DeliveryPointer  `json:"delivery"`
		Seal     recoveryset.SnapshotSeal     `json:"seal"`
		Marker   catalogartifact.CommitMarker `json:"marker"`
	}{readback.Set.Delivery, readback.Set.Serving, marker})
	duckValue, _ := json.Marshal(struct {
		Catalog        recoveryset.CatalogCommit `json:"catalog"`
		MetadataSchema string                    `json:"metadataSchema"`
		Marker         string                    `json:"marker"`
	}{readback.Set.Catalog, metadataSchema, canonicalMarker})
	return NativePostgresEvidence{ControlDigest: digestBytes(controlValue), DuckLakeDigest: digestBytes(duckValue), Catalog: readback.Set.Catalog}, nil
}
