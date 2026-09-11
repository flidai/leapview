package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/flidai/leapview/internal/platform/instanceidentity"
	project "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectdb "github.com/flidai/leapview/internal/project/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ResourceUIDActivation identifies the exact target scope admitted by one
// activation. Resource UID allocation is intentionally not exposed here.
type ResourceUIDActivation struct {
	InstanceID   string
	TargetID     string
	ProjectID    string
	Environment  string
	GenerationID string
}

// ResourceUIDRestoreInput is audited authority for restoring one tombstoned
// authored ID into one exact, not-yet-bound generation.
type ResourceUIDRestoreInput struct {
	ResourceUIDActivation
	ResourceUID   project.ResourceUID
	AuthoredID    string
	Kind          projectgraph.Kind
	ActorID       string
	RequestDigest string
}

// AdmitResourceUIDInventoryTx admits a compiler-sealed inventory through the
// caller-owned admission transaction. The inventory exposes only canonical
// JSON, preventing callers from manufacturing individual UID allocations.
func (r *Repository) AdmitResourceUIDInventoryTx(ctx context.Context, tx pgx.Tx, targetID, projectID, environment, generationID string, inventory project.ResourceUIDInventory) error {
	if tx == nil || r == nil {
		return project.ErrInvalidResourceUID
	}
	if !validActivationScope(targetID, targetID, projectID, environment, generationID) {
		return project.ErrInvalidResourceUID
	}
	encoded, err := inventory.JSON()
	if err != nil {
		return err
	}
	if !json.Valid(encoded) {
		return project.ErrInvalidResourceUID
	}
	if inventory.GraphDigest() == "" || inventory.BundleDigest() == "" {
		return project.ErrInvalidResourceUID
	}
	q := projectdb.New(tx)
	admitted, err := q.AdmitResourceUIDInventory(ctx, projectdb.AdmitResourceUIDInventoryParams{
		TargetID: targetID, ProjectID: projectID, Environment: environment,
		GenerationID: dbUUID(parseUUID(generationID)), GraphDigest: inventory.GraphDigest(),
		BundleDigest: inventory.BundleDigest(), GraphBytes: inventory.GraphCanonicalBytes(), InventoryJson: encoded,
	})
	if err != nil {
		return mapResourceUIDError(err)
	}
	if !admitted {
		return project.ErrResourceUIDConflict
	}
	return nil
}

// ResolveResourceUID resolves an authored ID only inside the caller's
// instance/project scope. There is deliberately no unscoped read.
func (r *Repository) ResolveResourceUID(ctx context.Context, instanceID, projectID, authoredID string) (project.ResourceUIDRecord, error) {
	if r == nil || r.db == nil || !resourceUIDScopeValid(instanceID, projectID) || !authoredResourceIDValid(authoredID) {
		return project.ResourceUIDRecord{}, project.ErrInvalidResourceUID
	}
	row, err := projectdb.New(r.db).GetResourceUIDByAuthoredID(ctx, projectdb.GetResourceUIDByAuthoredIDParams{InstanceID: instanceID, ProjectID: projectID, AuthoredResourceID: authoredID})
	if err != nil {
		return project.ResourceUIDRecord{}, mapResourceUIDError(err)
	}
	return resourceUIDRecord(row)
}

// ResolveResourceUIDByUID is still scope-qualified: possession of an opaque
// UID is never a capability and cannot disclose another Project's record.
func (r *Repository) ResolveResourceUIDByUID(ctx context.Context, instanceID, projectID string, uid project.ResourceUID) (project.ResourceUIDRecord, error) {
	parsed, err := project.ParseResourceUID(uid.String())
	if r == nil || r.db == nil || !resourceUIDScopeValid(instanceID, projectID) || err != nil {
		return project.ResourceUIDRecord{}, project.ErrInvalidResourceUID
	}
	uuidValue, _ := uuid.Parse(parsed.String())
	row, err := projectdb.New(r.db).GetResourceUIDByUID(ctx, projectdb.GetResourceUIDByUIDParams{InstanceID: instanceID, ProjectID: projectID, ResourceUid: dbUUID(uuidValue)})
	if err != nil {
		return project.ResourceUIDRecord{}, mapResourceUIDError(err)
	}
	return resourceUIDRecord(row)
}

// ListGenerationResourceUIDs returns immutable, scope-qualified evidence for
// diagnostics and rollback.
func (r *Repository) ListGenerationResourceUIDs(ctx context.Context, instanceID, projectID, generationID string) ([]project.ResourceUIDBinding, error) {
	if r == nil || r.db == nil || !resourceUIDScopeValid(instanceID, projectID) || !canonicalUUID(generationID) {
		return nil, project.ErrInvalidResourceUID
	}
	uuidValue, _ := uuid.Parse(generationID)
	rows, err := projectdb.New(r.db).ListGenerationResourceUIDs(ctx, projectdb.ListGenerationResourceUIDsParams{InstanceID: instanceID, ProjectID: projectID, GenerationID: dbUUID(uuidValue)})
	if err != nil {
		return nil, mapResourceUIDError(err)
	}
	out := make([]project.ResourceUIDBinding, len(rows))
	for i, row := range rows {
		out[i], err = resourceUIDBinding(row.ResourceUid, row.InstanceID, row.ProjectID, row.Environment, row.TargetID, row.GenerationID, row.AuthoredResourceID, row.ResourceKind, row.ContractStatus, textFromDB(row.ContractProfile), textFromDB(row.ContractVersion), textFromDB(row.ContractDigest), row.ContractBytes, row.BoundAt)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AuthorizeResourceUIDRestore records explicit audited restore evidence.
func (r *Repository) AuthorizeResourceUIDRestore(ctx context.Context, input ResourceUIDRestoreInput) (project.ResourceUIDRestoreAuthorization, error) {
	if r == nil || r.db == nil {
		return project.ResourceUIDRestoreAuthorization{}, project.ErrInvalidResourceUID
	}
	return authorizeResourceUIDRestore(ctx, r.db, input)
}

func authorizeResourceUIDRestore(ctx context.Context, db DBTX, input ResourceUIDRestoreInput) (project.ResourceUIDRestoreAuthorization, error) {
	if db == nil || !validActivationScope(input.InstanceID, input.TargetID, input.ProjectID, input.Environment, input.GenerationID) ||
		input.ResourceUID.Validate() != nil || !authoredResourceIDValid(input.AuthoredID) || !authoredKindValid(input.Kind) ||
		!validActorID(input.ActorID) || !resourceUIDDigestValid(input.RequestDigest) {
		return project.ResourceUIDRestoreAuthorization{}, project.ErrInvalidResourceUID
	}
	resourceID, _ := uuid.Parse(input.ResourceUID.String())
	row, err := projectdb.New(db).AuthorizeResourceUIDRestore(ctx, projectdb.AuthorizeResourceUIDRestoreParams{
		InstanceID: input.InstanceID, TargetID: input.TargetID, ProjectID: input.ProjectID, Environment: input.Environment,
		GenerationID: dbUUID(parseUUID(input.GenerationID)), ResourceUid: dbUUID(resourceID), AuthoredResourceID: input.AuthoredID,
		ResourceKind: string(input.Kind), ActorID: input.ActorID, RequestDigest: input.RequestDigest,
	})
	if err != nil {
		return project.ResourceUIDRestoreAuthorization{}, mapResourceUIDError(err)
	}
	return resourceUIDRestore(row)
}

func resourceUIDBinding(uid pgtype.UUID, instanceID, projectID, environment, targetID string, generationID pgtype.UUID, authoredID, kind, status, profile, version, digest string, bytes []byte, boundAt pgtype.Timestamptz) (project.ResourceUIDBinding, error) {
	binding := project.ResourceUIDBinding{ResourceUID: project.ResourceUID(uuidFromDB(uid).String()), InstanceID: instanceID, ProjectID: projectID, Environment: environment, TargetID: targetID, GenerationID: uuidFromDB(generationID).String(), AuthoredID: authoredID, Kind: projectgraph.Kind(kind), ContractStatus: status, ContractProfile: profile, ContractVersion: version, ContractDigest: digest, ContractBytes: append([]byte(nil), bytes...), BoundAt: boundAt.Time}
	if err := binding.Validate(); err != nil {
		return project.ResourceUIDBinding{}, err
	}
	return binding, nil
}

func textFromDB(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func resourceUIDRecord(row projectdb.ProjectResourceUidRegistry) (project.ResourceUIDRecord, error) {
	record := project.ResourceUIDRecord{UID: project.ResourceUID(uuidFromDB(row.ResourceUid).String()), InstanceID: row.InstanceID, ProjectID: row.ProjectID, AuthoredID: row.AuthoredResourceID, Kind: projectgraph.Kind(row.ResourceKind), State: project.ResourceUIDState(row.State), FirstGeneration: uuidFromDB(row.FirstGenerationID).String(), LatestGeneration: uuidFromDB(row.LatestGenerationID).String(), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	if row.CurrentGenerationID.Valid {
		record.CurrentGeneration = uuidFromDB(row.CurrentGenerationID).String()
	}
	if row.RemovedInGenerationID.Valid {
		record.RemovedInGeneration = uuidFromDB(row.RemovedInGenerationID).String()
	}
	return record, record.Validate()
}

func resourceUIDRestore(row projectdb.ProjectResourceUidRestoreAuthorization) (project.ResourceUIDRestoreAuthorization, error) {
	restore := project.ResourceUIDRestoreAuthorization{RestoreID: uuidFromDB(row.RestoreID).String(), ResourceUID: project.ResourceUID(uuidFromDB(row.ResourceUid).String()), InstanceID: row.InstanceID, ProjectID: row.ProjectID, AuthoredID: row.AuthoredResourceID, Kind: projectgraph.Kind(row.ResourceKind), Environment: row.Environment, TargetID: row.TargetID, GenerationID: uuidFromDB(row.GenerationID).String(), ActorID: row.ActorID, RequestDigest: row.RequestDigest, Status: row.Status, CreatedAt: row.CreatedAt.Time}
	if row.ConsumedAt.Valid {
		restore.ConsumedAt = row.ConsumedAt.Time
	}
	if err := restore.Validate(); err != nil {
		return project.ResourceUIDRestoreAuthorization{}, err
	}
	return restore, nil
}

func validActivationScope(instanceID, targetID, projectID, environment, generationID string) bool {
	return instanceidentityValid(instanceID) && instanceID == targetID && boundedResourceID(projectID) && boundedResourceText(environment) && projectgraph.ValidateServingEnvironment(environment) == nil && canonicalUUID(generationID)
}

func instanceidentityValid(value string) bool {
	return instanceidentity.Valid(value)
}

func resourceUIDScopeValid(instanceID, projectID string) bool {
	return instanceidentityValid(instanceID) && boundedResourceID(projectID)
}

func boundedResourceText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 255 && !strings.ContainsAny(value, "\x00\r\n")
}

func boundedResourceID(value string) bool {
	return boundedResourceText(value) && projectgraph.ResourceID(value).Valid()
}

func authoredResourceIDValid(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\x00\r\n") && projectgraph.ResourceID(value).Valid()
}

func authoredKindValid(kind projectgraph.Kind) bool {
	switch kind {
	case projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard:
		return true
	default:
		return false
	}
}

func validActorID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 255 && unicode.IsPrint([]rune(value)[0]) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func resourceUIDDigestValid(value string) bool {
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") && strings.Trim(value[len("sha256:"):], "0123456789abcdef") == ""
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func parseUUID(value string) uuid.UUID { parsed, _ := uuid.Parse(value); return parsed }

func mapResourceUIDError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return project.ErrResourceUIDNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		message := strings.ToLower(pgErr.Message)
		switch {
		case strings.Contains(message, "kind conflict"):
			return fmt.Errorf("%w: %s", project.ErrResourceUIDKindConflict, pgErr.Message)
		case strings.Contains(message, "restore authorization required"):
			return fmt.Errorf("%w: %s", project.ErrResourceUIDRestoreRequired, pgErr.Message)
		case strings.Contains(message, "tombstoned"):
			return fmt.Errorf("%w: %s", project.ErrResourceUIDTombstoned, pgErr.Message)
		case strings.Contains(message, "outside"), strings.Contains(message, "conflict"), strings.Contains(message, "does not match"):
			return fmt.Errorf("%w: %s", project.ErrResourceUIDConflict, pgErr.Message)
		case strings.Contains(message, "invalid"), strings.Contains(message, "unsupported"):
			return fmt.Errorf("%w: %s", project.ErrInvalidResourceUID, pgErr.Message)
		}
	}
	return err
}
