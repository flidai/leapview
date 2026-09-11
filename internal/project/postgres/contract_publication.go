package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/project/contractpublication"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectdb "github.com/flidai/leapview/internal/project/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/mod/semver"
)

// ErrContractPublicationNotFound is returned by the caller-owned replay
// methods when a publication baseline has not been persisted.
var ErrContractPublicationNotFound = errors.New("contract publication not found")

// PublicationAdmission is the caller-owned, typed admission context. Policy
// evidence is derived from the locked immutable baseline; approval is checked
// only for a new insert at Now. Historical replay validates approval structure
// and binding but intentionally does not re-check expiry.
type PublicationAdmission struct {
	PolicyContext contractpublication.PolicyContext
	Approval      *contractpublication.WideningApprovalEvidence
	Now           time.Time
}

// ContractPublicationAdmission is the descriptive alias used by callers that
// prefer the full domain term.
type ContractPublicationAdmission = PublicationAdmission

// PublishContractTx appends one immutable contract publication in tx. The
// caller owns tx lifetime and commit/rollback. A transaction-scoped advisory
// lock serializes attempts for one instance/authored-id/kind tuple, making
// exact replay and divergent attempts deterministic under concurrency.
func (r *Repository) PublishContractTx(ctx context.Context, tx Tx, input contractpublication.ContractPublicationInput, admission PublicationAdmission) (contractpublication.ContractPublication, error) {
	if tx == nil {
		return contractpublication.ContractPublication{}, contractpublication.ErrInvalidPublication
	}
	prepared, err := contractpublication.PrepareContractPublication(input)
	if err != nil {
		return contractpublication.ContractPublication{}, err
	}
	lockKey, err := json.Marshal([]string{prepared.InstanceID, prepared.AuthoredID.String(), string(prepared.ResourceKind)})
	if err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("encode contract publication lock: %w", err)
	}
	queries := projectdb.New(tx)
	if err := queries.LockContractPublication(ctx, string(lockKey)); err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("lock contract publication: %w", err)
	}
	policyContext, approval := admission.PolicyContext, admission.Approval

	existing, err := queries.GetContractPublication(ctx, projectdb.GetContractPublicationParams{
		InstanceID: prepared.InstanceID, AuthoredID: prepared.AuthoredID.String(),
		ResourceKind: string(prepared.ResourceKind), VersionBaseline: prepared.VersionBaseline,
	})
	if err == nil {
		row, rowErr := contractPublicationFromModel(existing)
		if rowErr != nil {
			return contractpublication.ContractPublication{}, rowErr
		}
		if rowErr := validateStoredPublicationTx(ctx, tx, row); rowErr != nil {
			return contractpublication.ContractPublication{}, rowErr
		}
		if !sameUnqualifiedPublicationContent(row, prepared) {
			return contractpublication.ContractPublication{}, fmt.Errorf("%w: %s/%s/%s@%s", contractpublication.ErrPublicationConflict, prepared.InstanceID, prepared.ResourceKind, prepared.AuthoredID, prepared.VersionBaseline)
		}
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return contractpublication.ContractPublication{}, fmt.Errorf("read existing contract publication: %w", err)
	}

	latest, found, err := latestContractPublication(ctx, queries, prepared.InstanceID, prepared.AuthoredID, prepared.ResourceKind)
	if err != nil {
		return contractpublication.ContractPublication{}, err
	}
	if found {
		if err := validateStoredPublicationTx(ctx, tx, latest); err != nil {
			return contractpublication.ContractPublication{}, err
		}
		suppliedBaseline := policyContext.Existing
		if suppliedBaseline == nil {
			suppliedBaseline = policyContext.Baseline
		}
		if policyContext.Existing != nil && policyContext.Baseline != nil {
			return contractpublication.ContractPublication{}, fmt.Errorf("%w: publication baseline supplied twice", contractpublication.ErrInvalidPolicy)
		}
		if policyContext.BaselineKind != contractpublication.BaselineExisting || suppliedBaseline == nil {
			return contractpublication.ContractPublication{}, fmt.Errorf("%w: publication baseline is not the latest immutable baseline", contractpublication.ErrInvalidPolicy)
		}
		if !contractpublication.EqualPublicationIdentity(suppliedBaseline.Identity(), latest.Identity()) {
			return contractpublication.ContractPublication{}, fmt.Errorf("%w: publication baseline is stale", contractpublication.ErrPublicationConflict)
		}
		policyContext.Existing = &latest
		policyContext.Baseline = nil
	} else if policyContext.BaselineKind != contractpublication.BaselineGenesis || policyContext.Existing != nil || policyContext.Baseline != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: genesis publication requires an empty publication history", contractpublication.ErrInvalidPolicy)
	}
	policy, err := contractpublication.DerivePolicyEvidence(policyContext, prepared)
	if err != nil {
		return contractpublication.ContractPublication{}, err
	}
	if admission.Now.IsZero() {
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: admission time is required", contractpublication.ErrInvalidPolicy)
	}
	if err := contractpublication.ValidateAdmission(policyContext, prepared, policy, approval, admission.Now.UTC()); err != nil {
		return contractpublication.ContractPublication{}, err
	}
	prepared, err = contractpublication.AttachPolicyEvidence(policyContext, prepared, policy, approval)
	if err != nil {
		return contractpublication.ContractPublication{}, err
	}
	validationJSON, err := json.Marshal(prepared.Validation)
	if err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("marshal contract validation evidence: %w", err)
	}

	if err := queries.InsertContractPublication(ctx, projectdb.InsertContractPublicationParams{
		InstanceID: prepared.InstanceID, AuthoredID: prepared.AuthoredID.String(),
		ResourceKind: string(prepared.ResourceKind), Version: prepared.Version,
		VersionBaseline: prepared.VersionBaseline, ProjectionProfile: prepared.ProjectionProfile,
		CanonicalBytes: prepared.CanonicalBytes, CanonicalDigest: prepared.Digest,
		ValidationEvidenceJson: string(validationJSON),
	}); err != nil {
		return contractpublication.ContractPublication{}, mapContractPublicationDatabaseError("insert contract publication", err)
	}
	published, err := queries.GetContractPublication(ctx, projectdb.GetContractPublicationParams{
		InstanceID: prepared.InstanceID, AuthoredID: prepared.AuthoredID.String(),
		ResourceKind: string(prepared.ResourceKind), VersionBaseline: prepared.VersionBaseline,
	})
	if err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("read inserted contract publication: %w", err)
	}
	row, err := contractPublicationFromModel(published)
	if err != nil {
		return contractpublication.ContractPublication{}, err
	}
	if !contractpublication.EqualContractPublicationContent(row, prepared) {
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: stored contract evidence changed during publication", contractpublication.ErrPublicationConflict)
	}
	return row, nil
}

// ContractPublicationTx replays one immutable baseline from a caller-owned
// transaction. Build metadata is not a distinct lookup identity.
func (r *Repository) ContractPublicationTx(ctx context.Context, tx Tx, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind, version string) (contractpublication.ContractPublication, error) {
	if tx == nil {
		return contractpublication.ContractPublication{}, contractpublication.ErrInvalidPublication
	}
	if instanceID == "" || instanceID != strings.TrimSpace(instanceID) || authoredID.Validate() != nil || !contractPublicationKind(kind) {
		return contractpublication.ContractPublication{}, contractpublication.ErrInvalidPublication
	}
	baseline, err := contractversion.SemverBaseline(version)
	if err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: %v", contractpublication.ErrInvalidPublication, err)
	}
	row, err := projectdb.New(tx).GetContractPublication(ctx, projectdb.GetContractPublicationParams{
		InstanceID: instanceID, AuthoredID: authoredID.String(), ResourceKind: string(kind),
		VersionBaseline: strings.TrimPrefix(baseline, "v"),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return contractpublication.ContractPublication{}, ErrContractPublicationNotFound
	}
	if err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("read contract publication: %w", err)
	}
	publication, err := contractPublicationFromModel(row)
	if err != nil {
		return contractpublication.ContractPublication{}, err
	}
	if err := validateStoredPublicationTx(ctx, tx, publication); err != nil {
		return contractpublication.ContractPublication{}, err
	}
	return publication, nil
}

// ReplayContractPublicationTx is the explicit exact-replay alias.
func (r *Repository) ReplayContractPublicationTx(ctx context.Context, tx Tx, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind, version string) (contractpublication.ContractPublication, error) {
	return r.ContractPublicationTx(ctx, tx, instanceID, authoredID, kind, version)
}

func latestContractPublication(ctx context.Context, queries *projectdb.Queries, instanceID string, authoredID projectgraph.ResourceID, kind projectgraph.Kind) (contractpublication.ContractPublication, bool, error) {
	rows, err := queries.ListContractPublications(ctx, projectdb.ListContractPublicationsParams{
		InstanceID: instanceID, AuthoredID: authoredID.String(), ResourceKind: string(kind),
	})
	if err != nil {
		return contractpublication.ContractPublication{}, false, fmt.Errorf("list contract publications: %w", err)
	}
	var latest contractpublication.ContractPublication
	found := false
	for _, stored := range rows {
		publication, err := contractPublicationFromModel(stored)
		if err != nil {
			return contractpublication.ContractPublication{}, false, err
		}
		if !found {
			latest, found = publication, true
			continue
		}
		current := "v" + latest.VersionBaseline
		candidate := "v" + publication.VersionBaseline
		if semver.Compare(candidate, current) > 0 {
			latest = publication
		}
	}
	return latest, found, nil
}

func contractPublicationFromModel(stored projectdb.ProjectContractPublication) (contractpublication.ContractPublication, error) {
	if !stored.PublishedAt.Valid {
		return contractpublication.ContractPublication{}, errors.New("stored contract publication timestamp is invalid")
	}
	authoredID, err := projectgraph.NewResourceID(stored.AuthoredID)
	if err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("stored contract publication authored id: %w", err)
	}
	kind := projectgraph.Kind(stored.ResourceKind)
	if !contractPublicationKind(kind) {
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: stored contract publication kind %q", contractpublication.ErrInvalidPublication, kind)
	}
	var validation contractpublication.ValidationEvidence
	decoder := json.NewDecoder(strings.NewReader(stored.ValidationEvidenceJson))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&validation); err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: decode stored contract validation evidence: %v", contractpublication.ErrInvalidPolicy, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return contractpublication.ContractPublication{}, fmt.Errorf("%w: stored contract validation evidence has trailing JSON", contractpublication.ErrInvalidPolicy)
		}
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: decode stored contract validation evidence: %v", contractpublication.ErrInvalidPolicy, err)
	}
	encodedValidation, err := json.Marshal(validation)
	if err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: encode stored contract validation evidence: %v", contractpublication.ErrInvalidPolicy, err)
	}
	if !bytes.Equal(encodedValidation, []byte(stored.ValidationEvidenceJson)) {
		return contractpublication.ContractPublication{}, fmt.Errorf("%w: stored contract validation evidence is not normalized", contractpublication.ErrInvalidPolicy)
	}
	publication := contractpublication.ContractPublication{
		InstanceID: stored.InstanceID, AuthoredID: authoredID, ResourceKind: kind,
		Version: stored.Version, VersionBaseline: stored.VersionBaseline,
		ProjectionProfile: stored.ProjectionProfile, CanonicalBytes: append([]byte(nil), stored.CanonicalBytes...),
		Digest: stored.CanonicalDigest, PublishedAt: stored.PublishedAt.Time.UTC(), Validation: validation,
	}
	if err := publication.Validate(); err != nil {
		return contractpublication.ContractPublication{}, fmt.Errorf("stored contract publication validation: %w", err)
	}
	return publication, nil
}

func sameUnqualifiedPublicationContent(left, right contractpublication.ContractPublication) bool {
	if !contractpublication.EqualPublicationIdentity(left.Identity(), right.Identity()) || !bytes.Equal(left.CanonicalBytes, right.CanonicalBytes) {
		return false
	}
	leftValidation := left.Validation
	leftValidation.PolicyEvidence = nil
	leftValidation.ApprovalEvidence = nil
	left = left.Clone()
	left.Validation = leftValidation
	return contractpublication.EqualContractPublicationContent(left, right)
}

func validateStoredPublicationTx(ctx context.Context, tx Tx, publication contractpublication.ContractPublication) error {
	if publication.Validation.PolicyEvidence == nil {
		return fmt.Errorf("%w: stored publication is not qualified", contractpublication.ErrInvalidPolicy)
	}
	policy := *publication.Validation.PolicyEvidence
	policyContext := contractpublication.PolicyContext{BaselineKind: policy.BaselineKind}
	if policy.BaselineKind == contractpublication.BaselineExisting {
		baselineRow, err := projectdb.New(tx).GetContractPublication(ctx, projectdb.GetContractPublicationParams{
			InstanceID: policy.Baseline.InstanceID, AuthoredID: policy.Baseline.AuthoredID.String(),
			ResourceKind: string(policy.Baseline.ResourceKind), VersionBaseline: policy.Baseline.VersionBaseline,
		})
		if err != nil {
			return fmt.Errorf("%w: stored policy baseline is unavailable: %v", contractpublication.ErrInvalidPolicy, err)
		}
		baseline, err := contractPublicationFromModel(baselineRow)
		if err != nil {
			return err
		}
		policyContext.Existing = &baseline
	}
	if err := contractpublication.ValidateHistoricalPublication(policyContext, publication); err != nil {
		return fmt.Errorf("%w: stored qualified publication: %v", contractpublication.ErrInvalidPolicy, err)
	}
	return nil
}

func contractPublicationKind(kind projectgraph.Kind) bool {
	return kind == projectgraph.KindSource || kind == projectgraph.KindModel || kind == projectgraph.KindSemanticModel
}

func mapContractPublicationDatabaseError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23514":
			return fmt.Errorf("%w: %s: %v", contractpublication.ErrInvalidPublication, operation, err)
		case "23505", "40001", "40P01":
			return fmt.Errorf("%w: %s: %v", contractpublication.ErrPublicationConflict, operation, err)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
