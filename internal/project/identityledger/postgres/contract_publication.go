package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if input.PolicyContext == nil {
		return identityledger.ContractPublication{}, fmt.Errorf("%w: policy context is required for publication", identityledger.ErrPolicyEvidenceInvalid)
	}
	if input.Validation.PolicyEvidence != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("%w: policy evidence is server-owned", identityledger.ErrPolicyEvidenceInvalid)
	}
	// Client-supplied policy fields are never consumed. Keep only the
	// projection checks as input; the classifier result and policy digest below
	// are derived from immutable bytes and the locked live identity.
	prepared, err := identityledger.PrepareContractPublication(input)
	if err != nil {
		return identityledger.ContractPublication{}, err
	}
	if err := input.PolicyContext.ValidateForPublication(prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind); err != nil {
		return identityledger.ContractPublication{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("begin contract publication: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Identity lifecycle transitions take this instance lock first. Keep that
	// order here before the narrower publication lock to prevent a publication
	// from observing a lifecycle transition half-way through restoration.
	if err := lockInstance(ctx, tx, prepared.InstanceID); err != nil {
		return identityledger.ContractPublication{}, err
	}
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
	lifecycle, sequence, err := policyLifecycleTx(ctx, tx, prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind)
	if err != nil {
		return identityledger.ContractPublication{}, err
	}
	if lifecycle.Lifecycle != identityledger.LifecycleActive || lifecycle.ActiveBundleID == "" {
		return identityledger.ContractPublication{}, fmt.Errorf("%w: resource identity is not active: lifecycle=%q bundle=%q", identityledger.ErrPolicyEvidenceConflict, lifecycle.Lifecycle, lifecycle.ActiveBundleID)
	}
	if sequence != input.PolicyContext.ExpectedLifecycleSequence {
		return identityledger.ContractPublication{}, fmt.Errorf("%w: lifecycle sequence changed: expected=%d current=%d", identityledger.ErrPolicyEvidenceConflict, input.PolicyContext.ExpectedLifecycleSequence, sequence)
	}

	baseline, err := resolvePolicyBaselineTx(ctx, tx, prepared, *input.PolicyContext)
	if err != nil {
		return identityledger.ContractPublication{}, err
	}
	existing, err := contractPublicationTx(ctx, tx, prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind, prepared.VersionBaseline)
	if err == nil {
		// Historical v1 rows are replayable immutable history, but cannot be
		// upgraded or treated as a policy decision by this path.
		if existing.Validation.PolicyEvidence == nil {
			if !identityledger.EqualContractPublicationContent(existing, prepared) {
				return identityledger.ContractPublication{}, fmt.Errorf("%w: %s/%s/%s@%s", identityledger.ErrContractPublicationConflict, prepared.InstanceID, prepared.ResourceKind, prepared.AuthoredID, prepared.VersionBaseline)
			}
			if err := tx.Commit(ctx); err != nil {
				return identityledger.ContractPublication{}, mapContractPublicationDatabaseError("commit exact historical replay", err)
			}
			return existing, nil
		}
		finalValidation, err := derivePolicyValidation(prepared, baseline, sequence, lifecycle.ActiveBundleID, *input.PolicyContext)
		if err != nil {
			return identityledger.ContractPublication{}, err
		}
		prepared.Validation = finalValidation
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
	if input.PolicyContext.BaselineKind == identityledger.PolicyBaselineGenesis {
		if _, found, err := latestContractPublicationTx(ctx, tx, prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind); err != nil {
			return identityledger.ContractPublication{}, err
		} else if found {
			return identityledger.ContractPublication{}, fmt.Errorf("%w: genesis publication requires an empty publication history", identityledger.ErrPolicyEvidenceConflict)
		}
	} else if baseline != nil {
		latest, found, err := latestContractPublicationTx(ctx, tx, prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind)
		if err != nil {
			return identityledger.ContractPublication{}, err
		}
		if !found || latest.VersionBaseline != baseline.VersionBaseline {
			return identityledger.ContractPublication{}, fmt.Errorf("%w: policy baseline is not the latest publication", identityledger.ErrPolicyEvidenceConflict)
		}
	}
	finalValidation, err := derivePolicyValidation(prepared, baseline, sequence, lifecycle.ActiveBundleID, *input.PolicyContext)
	if err != nil {
		return identityledger.ContractPublication{}, err
	}
	prepared.Validation = finalValidation
	validationJSON, err := json.Marshal(prepared.Validation)
	if err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("marshal contract validation evidence: %w", err)
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
	decoder := json.NewDecoder(strings.NewReader(validationJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result.Validation); err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("decode stored contract validation evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return identityledger.ContractPublication{}, fmt.Errorf("decode stored contract validation evidence: trailing JSON value")
	}
	if err := result.Validation.Validate(); err != nil {
		return identityledger.ContractPublication{}, fmt.Errorf("validate stored contract validation evidence: %w", err)
	}
	if result.Validation.PolicyEvidence != nil {
		if _, err := result.PolicyDecision(); err != nil {
			return identityledger.ContractPublication{}, fmt.Errorf("validate stored policy evidence binding: %w", err)
		}
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
