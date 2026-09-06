package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/mod/semver"
)

// PublishContract appends one immutable contract-version row to the existing
// resource identity ledger. An exact retry returns the first row; any drift is
// a conflict. The transaction-scoped advisory lock makes both outcomes stable
// under concurrent publication without adding another identity or store.
func (r *Repository) PublishContract(ctx context.Context, input identityledger.ContractPublicationInput) (identityledger.ContractPublication, error) {
	prepared, err := identityledger.PrepareContractPublication(input)
	if err != nil {
		return identityledger.ContractPublication{}, err
	}
	validationJSON, err := json.Marshal(prepared.Validation)
	if err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("marshal contract validation evidence: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("begin contract publication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A JSON string tuple is unambiguous and PostgreSQL-text safe. NUL-delimited
	// text cannot be sent to PostgreSQL. This key only scopes the advisory lock;
	// it is not persisted identity or publication digest evidence.
	lockKey, err := json.Marshal([]string{prepared.InstanceID, prepared.AuthoredID.String(), string(prepared.ResourceKind)})
	if err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("encode contract publication lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, string(lockKey)); err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("lock contract publication: %w", err)
	}
	existing, err := contractPublicationTx(ctx, tx, prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind, prepared.VersionBaseline)
	if err == nil {
		if !identityledger.EqualContractPublicationContent(existing, prepared) {
			return identityledger.ContractPublication{}, fmt.Errorf("%w: %s/%s/%s@%s", identityledger.ErrContractPublicationConflict, prepared.InstanceID, prepared.ResourceKind, prepared.AuthoredID, prepared.VersionBaseline)
		}
		if err := tx.Commit(ctx); err != nil {
			return identityledger.ContractPublication{}, mapContractPublicationDatabaseError("commit exact contract replay", err)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return identityledger.ContractPublication{}, err
	}
	latest, found, err := latestContractPublicationTx(ctx, tx, prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind)
	if err != nil {
		return identityledger.ContractPublication{}, err
	}
	if found {
		if _, err := contractversion.ValidateVersionTransition(latest.CanonicalBytes, prepared.CanonicalBytes); err != nil {
			return identityledger.ContractPublication{}, fmt.Errorf("%w: version transition from %s to %s: %v", identityledger.ErrContractPublicationConflict, latest.Version, prepared.Version, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO project.contract_publication(
			instance_id,authored_id,resource_kind,version,version_baseline,
			projection_profile,canonical_bytes,canonical_digest,validation_evidence_json)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		prepared.InstanceID, prepared.AuthoredID.String(), string(prepared.ResourceKind), prepared.Version, prepared.VersionBaseline,
		prepared.ProjectionProfile, prepared.CanonicalBytes, prepared.Digest, string(validationJSON)); err != nil {
		return identityledger.ContractPublication{}, mapContractPublicationDatabaseError("insert contract publication", err)
	}
	published, err := contractPublicationTx(ctx, tx, prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind, prepared.VersionBaseline)
	if err != nil {
		return identityledger.ContractPublication{}, err
	}
	if !identityledger.EqualContractPublicationContent(published, prepared) {
		return identityledger.ContractPublication{}, fmt.Errorf("%w: stored contract evidence changed during publication", identityledger.ErrContractPublicationConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.ContractPublication{}, mapContractPublicationDatabaseError("commit contract publication", err)
	}
	return published, nil
}

// ContractPublication replays the first exact evidence row for one SemVer
// baseline. Build metadata is not a distinct lookup identity.
func (r *Repository) ContractPublication(ctx context.Context, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind, version string) (identityledger.ContractPublication, error) {
	if strings.TrimSpace(instanceID) == "" || authoredID.Validate() != nil || !contractPublicationKind(kind) {
		return identityledger.ContractPublication{}, identityledger.ErrContractPublicationInvalid
	}
	baseline, err := contractversion.SemverBaseline(version)
	if err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("%w: %v", identityledger.ErrContractPublicationInvalid, err)
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("begin contract publication replay: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	publication, err := contractPublicationTx(ctx, tx, instanceID, authoredID, kind, strings.TrimPrefix(baseline, "v"))
	if errors.Is(err, pgx.ErrNoRows) {
		return identityledger.ContractPublication{}, identityledger.ErrContractPublicationNotFound
	}
	if err != nil {
		return identityledger.ContractPublication{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("commit contract publication replay: %w", err)
	}
	return publication, nil
}

func contractPublicationTx(ctx context.Context, tx pgx.Tx, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind, baseline string) (identityledger.ContractPublication, error) {
	return scanContractPublication(tx.QueryRow(ctx, `
		SELECT instance_id,authored_id,resource_kind,version,version_baseline,
		       projection_profile,canonical_bytes,canonical_digest,published_at,validation_evidence_json
		FROM project.contract_publication
		WHERE instance_id=$1 AND authored_id=$2 AND resource_kind=$3 AND version_baseline=$4`,
		instanceID, authoredID.String(), string(kind), baseline))
}

func latestContractPublicationTx(ctx context.Context, tx pgx.Tx, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind) (identityledger.ContractPublication, bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT instance_id,authored_id,resource_kind,version,version_baseline,
		       projection_profile,canonical_bytes,canonical_digest,published_at,validation_evidence_json
		FROM project.contract_publication
		WHERE instance_id=$1 AND authored_id=$2 AND resource_kind=$3`, instanceID, authoredID.String(), string(kind))
	if err != nil {
		return identityledger.ContractPublication{}, false, fmt.Errorf("list contract publications: %w", err)
	}
	defer rows.Close()
	var latest identityledger.ContractPublication
	found := false
	for rows.Next() {
		publication, err := scanContractPublication(rows)
		if err != nil {
			return identityledger.ContractPublication{}, false, err
		}
		if !found {
			latest, found = publication, true
			continue
		}
		current, err := contractversion.SemverBaseline(latest.Version)
		if err != nil {
			return identityledger.ContractPublication{}, false, fmt.Errorf("stored contract publication version: %w", err)
		}
		candidate, err := contractversion.SemverBaseline(publication.Version)
		if err != nil {
			return identityledger.ContractPublication{}, false, fmt.Errorf("stored contract publication version: %w", err)
		}
		if semver.Compare(candidate, current) > 0 {
			latest = publication
		}
	}
	if err := rows.Err(); err != nil {
		return identityledger.ContractPublication{}, false, fmt.Errorf("iterate contract publications: %w", err)
	}
	return latest, found, nil
}

type publicationScanner interface {
	Scan(...any) error
}

func scanContractPublication(row publicationScanner) (identityledger.ContractPublication, error) {
	var result identityledger.ContractPublication
	var authoredIDText, resourceKindText, validationJSON string
	err := row.Scan(&result.InstanceID, &authoredIDText, &resourceKindText, &result.Version, &result.VersionBaseline,
		&result.ProjectionProfile, &result.CanonicalBytes, &result.Digest, &result.PublishedAt, &validationJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identityledger.ContractPublication{}, pgx.ErrNoRows
		}
		return identityledger.ContractPublication{}, fmt.Errorf("read contract publication: %w", err)
	}
	result.AuthoredID = projectgraph.ResourceID(authoredIDText)
	result.ResourceKind = projectgraph.Kind(resourceKindText)
	result.PublishedAt = result.PublishedAt.UTC()
	if err := json.Unmarshal([]byte(validationJSON), &result.Validation); err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("decode stored contract validation evidence: %w", err)
	}
	result.CanonicalBytes = append([]byte(nil), result.CanonicalBytes...)
	return result, nil
}

func contractPublicationKind(kind projectgraph.Kind) bool {
	return kind == projectgraph.KindSource || kind == projectgraph.KindModel || kind == projectgraph.KindSemanticModel
}

func mapContractPublicationDatabaseError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23514":
			return fmt.Errorf("%w: %s: %v", identityledger.ErrContractPublicationInvalid, operation, err)
		case "23505", "40001", "40P01":
			return fmt.Errorf("%w: %s: %v", identityledger.ErrContractPublicationConflict, operation, err)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
