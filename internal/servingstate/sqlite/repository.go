package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	platformdb "github.com/flidai/leapview/internal/servingstate/internal/db"
)

type Repository struct {
	db *sql.DB
	q  *platformdb.Queries
}

func NewRepository(sqlDB *sql.DB) *Repository {
	return &Repository{db: sqlDB, q: platformdb.New(sqlDB)}
}

func requiredEnvironment(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("environment is required")
	}
	environment := servingstate.NormalizeEnvironment(servingstate.Environment(value))
	if err := servingstate.ValidateEnvironment(environment); err != nil {
		return "", err
	}
	return string(environment), nil
}

func (r *Repository) Create(ctx context.Context, input servingstate.CreateInput) (servingstate.State, error) {
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(input.ProjectID.String()))
	if err != nil {
		return servingstate.State{}, fmt.Errorf("project id is required")
	}
	environment := servingstate.NormalizeEnvironment(input.Environment)
	if err := servingstate.ValidateEnvironment(environment); err != nil {
		return servingstate.State{}, err
	}
	id := servingstate.ID(newID("state"))
	if _, err := projectgraph.NewServingIdentity(projectID, string(environment), string(id)); err != nil {
		return servingstate.State{}, err
	}
	if err := r.q.CreateServingState(ctx, platformdb.CreateServingStateParams{
		ID:          string(id),
		ProjectID:   projectID.String(),
		Environment: string(environment),
		Status:      string(servingstate.StatusPending),
		Source:      string(servingstate.NormalizeSource(input.Source)),
		CreatedBy:   input.CreatedBy,
	}); err != nil {
		return servingstate.State{}, err
	}
	return r.ByID(ctx, id)
}

func (r *Repository) ByID(ctx context.Context, id servingstate.ID) (servingstate.State, error) {
	row, err := r.q.GetServingState(ctx, string(id))
	if err != nil {
		return servingstate.State{}, mapNotFound(err)
	}
	return mapServingState(row), nil
}

func (r *Repository) MarkFailed(ctx context.Context, servingStateID servingstate.ID, cause error) error {
	if cause == nil {
		return nil
	}
	return r.q.UpdateServingStateStatus(ctx, platformdb.UpdateServingStateStatusParams{
		Status: string(servingstate.StatusFailed),
		Error:  cause.Error(),
		ID:     string(servingStateID),
	})
}

func (r *Repository) RecordDuckLakeSnapshot(ctx context.Context, servingStateID servingstate.ID, snapshotID int64) error {
	if snapshotID <= 0 {
		return fmt.Errorf("ducklake snapshot id must be positive")
	}
	return r.q.UpdateServingStateDuckLakeSnapshot(ctx, platformdb.UpdateServingStateDuckLakeSnapshotParams{
		DucklakeSnapshotID: snapshotID,
		ID:                 string(servingStateID),
	})
}

func (r *Repository) ReferencedDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	environment, err := requiredEnvironment(environment)
	if err != nil {
		return nil, err
	}
	return r.q.ListReferencedDuckLakeSnapshots(ctx, environment)
}

func (r *Repository) ActiveDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	environment, err := requiredEnvironment(environment)
	if err != nil {
		return nil, err
	}
	return r.q.ListActiveDuckLakeSnapshots(ctx, environment)
}

func (r *Repository) LeasedDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	environment, err := requiredEnvironment(environment)
	if err != nil {
		return nil, err
	}
	return r.q.ListLeasedDuckLakeSnapshots(ctx, environment)
}

func (r *Repository) ForeignEnvironmentDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	environment, err := requiredEnvironment(environment)
	if err != nil {
		return nil, err
	}
	return r.q.ListForeignEnvironmentDuckLakeSnapshots(ctx, environment)
}

func (r *Repository) CreateQuerySnapshotLease(ctx context.Context, input servingstate.SnapshotLeaseInput) (string, error) {
	if input.ServingStateID == "" {
		return "", fmt.Errorf("serving state id is required")
	}
	if input.DuckLakeSnapshotID <= 0 {
		return "", fmt.Errorf("ducklake snapshot id must be positive")
	}
	expiresAt := input.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(5 * time.Minute)
	}
	id := newID("lease")
	if err := r.q.CreateQuerySnapshotLease(ctx, platformdb.CreateQuerySnapshotLeaseParams{
		ID:                 id,
		ServingStateID:     string(input.ServingStateID),
		DucklakeSnapshotID: input.DuckLakeSnapshotID,
		OwnerID:            input.OwnerID,
		ExpiresAt:          sqliteTimestamp(expiresAt),
	}); err != nil {
		return "", err
	}
	return id, nil
}

func (r *Repository) ReleaseQuerySnapshotLease(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return r.q.ReleaseQuerySnapshotLease(ctx, id)
}

func (r *Repository) ExtendQuerySnapshotLease(ctx context.Context, id string, expiresAt time.Time) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if expiresAt.IsZero() {
		return fmt.Errorf("lease expiry is required")
	}
	updated, err := r.q.ExtendQuerySnapshotLease(ctx, platformdb.ExtendQuerySnapshotLeaseParams{
		ID:        id,
		ExpiresAt: sqliteTimestamp(expiresAt),
	})
	if err != nil {
		return err
	}
	if updated != 1 {
		return servingstate.ErrSnapshotLeaseLost
	}
	return nil
}

func (r *Repository) ReleaseExpiredQuerySnapshotLeases(ctx context.Context, environment string) error {
	var err error
	environment, err = requiredEnvironment(environment)
	if err != nil {
		return err
	}
	return r.q.ReleaseExpiredQuerySnapshotLeases(ctx, environment)
}

func (r *Repository) ExpireInactiveServingStates(ctx context.Context, environment string) error {
	var err error
	environment, err = requiredEnvironment(environment)
	if err != nil {
		return err
	}
	return r.q.ExpireInactiveServingStates(ctx, environment)
}

func (r *Repository) ScheduleExpiredServingStateDeletion(ctx context.Context, environment string) error {
	var err error
	environment, err = requiredEnvironment(environment)
	if err != nil {
		return err
	}
	return r.q.ScheduleExpiredServingStateDeletion(ctx, environment)
}

func (r *Repository) MarkDeleteScheduledServingStatesDeleted(ctx context.Context, environment string) error {
	var err error
	environment, err = requiredEnvironment(environment)
	if err != nil {
		return err
	}
	return r.q.MarkDeleteScheduledServingStatesDeleted(ctx, environment)
}

func (r *Repository) ReconcileRetention(ctx context.Context, environment string, now time.Time) error {
	var err error
	environment, err = requiredEnvironment(environment)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := r.q.MarkDrainingServingStatesDeleteScheduled(ctx, environment); err != nil {
		return err
	}
	return r.q.MarkDeleteScheduledServingStatesDeleted(ctx, environment)
}

func (r *Repository) SaveValidated(ctx context.Context, servingStateID servingstate.ID, validation servingstate.Validation, artifact servingstate.Artifact) (servingstate.State, error) {
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(validation.ProjectID.String()))
	if err != nil {
		return servingstate.State{}, fmt.Errorf("validated serving state requires project id")
	}
	if err := digest.ValidateSHA256Identity(validation.ProjectDigest); err != nil {
		return servingstate.State{}, fmt.Errorf("validated serving state requires project digest: %w", err)
	}
	if err := digest.ValidateSHA256Identity(validation.Digest); err != nil {
		return servingstate.State{}, fmt.Errorf("validated serving state requires artifact digest: %w", err)
	}
	if artifact.SizeBytes < 0 {
		return servingstate.State{}, fmt.Errorf("validated serving state artifact size cannot be negative")
	}
	accessPolicyJSON, err := json.Marshal(validation.AccessPolicy)
	if err != nil {
		return servingstate.State{}, err
	}
	publicationsJSON := strings.TrimSpace(validation.DashboardPublicationsJSON)
	if publicationsJSON == "" {
		publicationsJSON = "null"
	}
	var publications map[string]json.RawMessage
	if err := json.Unmarshal([]byte(publicationsJSON), &publications); err != nil {
		return servingstate.State{}, fmt.Errorf("validated serving state dashboard publications: %w", err)
	}
	canonicalPublicationsJSON, err := json.Marshal(publications)
	if err != nil {
		return servingstate.State{}, err
	}
	appearancesJSON := strings.TrimSpace(validation.DashboardAppearancesJSON)
	if appearancesJSON == "" {
		appearancesJSON = "null"
	}
	var appearances map[string]json.RawMessage
	if err := json.Unmarshal([]byte(appearancesJSON), &appearances); err != nil {
		return servingstate.State{}, fmt.Errorf("validated serving state dashboard appearances: %w", err)
	}
	canonicalAppearancesJSON, err := json.Marshal(appearances)
	if err != nil {
		return servingstate.State{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return servingstate.State{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	current, err := q.GetServingState(ctx, string(servingStateID))
	if err != nil {
		return servingstate.State{}, mapNotFound(err)
	}
	if current.ProjectID != projectID.String() {
		return servingstate.State{}, fmt.Errorf("artifact project = %q, want %q", projectID, current.ProjectID)
	}
	identity, err := projectgraph.NewServingIdentity(projectID, current.Environment, string(servingStateID))
	if err != nil {
		return servingstate.State{}, err
	}
	if _, err := projectgraph.NewArtifactEnvelope(identity, validation.Graph); err != nil {
		return servingstate.State{}, err
	}
	if validation.Graph.ProjectID() != projectID {
		return servingstate.State{}, fmt.Errorf("validated graph project = %q, want %q", validation.Graph.ProjectID(), projectID)
	}
	if artifact.Digest != validation.Digest {
		return servingstate.State{}, fmt.Errorf("artifact digest = %q, want %q", artifact.Digest, validation.Digest)
	}
	if artifact.ManifestJSON != validation.ManifestJSON {
		return servingstate.State{}, fmt.Errorf("artifact manifest does not match validated manifest")
	}
	if strings.TrimSpace(artifact.ID) == "" || artifact.ServingStateID != servingStateID {
		return servingstate.State{}, fmt.Errorf("artifact identity does not match serving state %s", servingStateID)
	}
	if strings.TrimSpace(artifact.Format) == "" || strings.TrimSpace(artifact.Path) == "" {
		return servingstate.State{}, fmt.Errorf("validated serving state artifact format and path are required")
	}
	switch servingstate.Status(current.Status) {
	case servingstate.StatusPending:
	case servingstate.StatusValidated:
		existingArtifact, existingErr := q.GetArtifactByServingState(ctx, current.ID)
		if existingErr == nil && current.ProjectID == projectID.String() && current.ProjectDigest == validation.ProjectDigest && current.AccessPolicyJson == string(accessPolicyJSON) && current.DashboardPublicationsJson == string(canonicalPublicationsJSON) && current.DashboardAppearancesJson == string(canonicalAppearancesJSON) && current.Digest == validation.Digest && current.ManifestJson == validation.ManifestJSON && sameArtifact(existingArtifact, artifact) {
			return mapServingState(current), nil
		}
		return servingstate.State{}, fmt.Errorf("validated serving state %s is immutable", servingStateID)
	default:
		return servingstate.State{}, fmt.Errorf("serving state %s has status %q, want pending", servingStateID, current.Status)
	}
	if err := validation.Graph.Validate(); err != nil {
		return servingstate.State{}, err
	}
	if err := q.InsertServingStateArtifact(ctx, mapArtifactParams(artifact)); err != nil {
		return servingstate.State{}, err
	}
	if err := q.ClearAssetEdgesForServingState(ctx, string(servingStateID)); err != nil {
		return servingstate.State{}, err
	}
	if err := q.ClearAssetsForServingState(ctx, string(servingStateID)); err != nil {
		return servingstate.State{}, err
	}
	for _, resource := range validation.Graph.Resources() {
		payload, err := json.Marshal(resource)
		if err != nil {
			return servingstate.State{}, fmt.Errorf("encode resource %s: %w", resource.ID, err)
		}
		if err := q.InsertAsset(ctx, platformdb.InsertAssetParams{
			SnapshotID:     assetSnapshotID(string(servingStateID), resource.ID.String()),
			LogicalAssetID: resource.ID.String(),
			ServingStateID: string(servingStateID),
			AssetType:      string(resource.Kind),
			AssetKey:       resource.Name,
			Title:          resource.Metadata.DisplayName,
			Description:    resource.Metadata.Description,
			SourceFile:     resource.Provenance.Path,
			PayloadSchema:  "project.graph.v1",
			PayloadJson:    string(payload),
			ContentHash:    contentDigest(payload),
		}); err != nil {
			return servingstate.State{}, err
		}
	}
	for _, edge := range validation.Graph.Edges() {
		if err := q.InsertAssetEdge(ctx, platformdb.InsertAssetEdgeParams{
			ID:                 edgeID(string(servingStateID), edge.From.String(), edge.To.String(), edge.Relation),
			ServingStateID:     string(servingStateID),
			FromLogicalAssetID: edge.From.String(),
			ToLogicalAssetID:   edge.To.String(),
			EdgeType:           edge.Relation,
		}); err != nil {
			return servingstate.State{}, err
		}
	}
	if err := q.UpdateServingStateValidated(ctx, platformdb.UpdateServingStateValidatedParams{
		Status:                    string(servingstate.StatusValidated),
		ProjectDigest:             validation.ProjectDigest,
		AccessPolicyJson:          string(accessPolicyJSON),
		DashboardPublicationsJson: string(canonicalPublicationsJSON),
		DashboardAppearancesJson:  string(canonicalAppearancesJSON),
		Digest:                    validation.Digest,
		ManifestJson:              validation.ManifestJSON,
		ID:                        string(servingStateID),
	}); err != nil {
		return servingstate.State{}, err
	}
	if err := tx.Commit(); err != nil {
		return servingstate.State{}, err
	}
	return r.ByID(ctx, servingStateID)
}

func sameArtifact(existing platformdb.ServingStateArtifact, candidate servingstate.Artifact) bool {
	return existing.ID == candidate.ID && existing.ServingStateID == string(candidate.ServingStateID) &&
		existing.Digest == candidate.Digest && existing.Format == candidate.Format &&
		existing.Path == candidate.Path && existing.ManifestJson == candidate.ManifestJSON &&
		existing.SizeBytes == candidate.SizeBytes
}

func (r *Repository) Activate(ctx context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment, servingStateID, expectedActiveID servingstate.ID) (servingstate.State, error) {
	return r.activate(ctx, projectID, environment, servingStateID, expectedActiveID)
}

func (r *Repository) activate(ctx context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment, servingStateID, expectedActiveID servingstate.ID) (servingstate.State, error) {
	canonicalProjectID, err := projectgraph.NewResourceID(strings.TrimSpace(projectID.String()))
	if err != nil {
		return servingstate.State{}, fmt.Errorf("project id is required")
	}
	environment = servingstate.NormalizeEnvironment(environment)
	if err := servingstate.ValidateEnvironment(environment); err != nil {
		return servingstate.State{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return servingstate.State{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	row, err := q.GetServingState(ctx, string(servingStateID))
	if err != nil {
		return servingstate.State{}, mapNotFound(err)
	}
	current := mapServingState(row)
	if current.ProjectID != canonicalProjectID {
		return servingstate.State{}, fmt.Errorf("serving state %s project = %q, want %q", servingStateID, current.ProjectID, canonicalProjectID)
	}
	if current.Environment != environment {
		return servingstate.State{}, fmt.Errorf("serving state %s environment = %q, want %q", servingStateID, current.Environment, environment)
	}
	if !current.CanActivate() {
		return servingstate.State{}, fmt.Errorf("serving state %s has status %q, want validated", servingStateID, current.Status)
	}
	var updated int64
	if expectedActiveID == "" {
		updated, err = q.InsertActiveServingState(ctx, platformdb.InsertActiveServingStateParams{
			ProjectID: canonicalProjectID.String(), Environment: string(environment), ServingStateID: string(servingStateID),
		})
	} else {
		updated, err = q.CompareAndSwapActiveServingState(ctx, platformdb.CompareAndSwapActiveServingStateParams{
			ServingStateID: string(servingStateID), ProjectID: canonicalProjectID.String(), Environment: string(environment), ExpectedActiveID: string(expectedActiveID),
		})
	}
	if err != nil {
		return servingstate.State{}, err
	}
	if updated != 1 {
		return servingstate.State{}, servingstate.ErrActivationConflict
	}
	if err := q.MarkOtherServingStatesDraining(ctx, platformdb.MarkOtherServingStatesDrainingParams{
		ProjectID:   canonicalProjectID.String(),
		Environment: string(environment),
		ID:          string(servingStateID),
	}); err != nil {
		return servingstate.State{}, err
	}
	if err := q.MarkServingStateActive(ctx, string(servingStateID)); err != nil {
		return servingstate.State{}, err
	}
	if err := tx.Commit(); err != nil {
		return servingstate.State{}, err
	}
	return r.ByID(ctx, servingStateID)
}

func (r *Repository) ActiveArtifact(ctx context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	canonicalProjectID, err := projectgraph.NewResourceID(strings.TrimSpace(projectID.String()))
	if err != nil {
		return servingstate.State{}, servingstate.Artifact{}, fmt.Errorf("project id is required")
	}
	canonicalEnvironment := servingstate.NormalizeEnvironment(environment)
	if err := servingstate.ValidateEnvironment(canonicalEnvironment); err != nil {
		return servingstate.State{}, servingstate.Artifact{}, err
	}
	row, err := r.q.GetActiveServingState(ctx, platformdb.GetActiveServingStateParams{ProjectID: canonicalProjectID.String(), Environment: string(canonicalEnvironment)})
	if err != nil {
		return servingstate.State{}, servingstate.Artifact{}, mapNotFound(err)
	}
	artifact, err := r.q.GetArtifactByServingState(ctx, row.ID)
	if err != nil {
		return servingstate.State{}, servingstate.Artifact{}, mapNotFound(err)
	}
	return mapServingState(row), mapArtifact(artifact), nil
}

func (r *Repository) ListActiveScopes(ctx context.Context) ([]servingstate.ActiveScope, error) {
	rows, err := r.q.ListActiveServingStateScopes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]servingstate.ActiveScope, 0, len(rows))
	for _, row := range rows {
		out = append(out, servingstate.ActiveScope{ProjectID: projectgraph.ResourceID(row.ProjectID), Environment: servingstate.Environment(row.Environment)})
	}
	return out, nil
}

func (r *Repository) ArtifactByServingState(ctx context.Context, servingStateID servingstate.ID) (servingstate.Artifact, error) {
	artifact, err := r.q.GetArtifactByServingState(ctx, string(servingStateID))
	if err != nil {
		return servingstate.Artifact{}, mapNotFound(err)
	}
	return mapArtifact(artifact), nil
}

func mapServingState(row platformdb.ServingState) servingstate.State {
	out := servingstate.State{
		ID:                        servingstate.ID(row.ID),
		ProjectID:                 projectgraph.ResourceID(row.ProjectID),
		ProjectDigest:             row.ProjectDigest,
		AccessPolicyJSON:          row.AccessPolicyJson,
		DashboardPublicationsJSON: row.DashboardPublicationsJson,
		DashboardAppearancesJSON:  row.DashboardAppearancesJson,
		Environment:               servingstate.Environment(row.Environment),
		Status:                    servingstate.Status(row.Status),
		Source:                    servingstate.NormalizeSource(servingstate.Source(row.Source)),
		Digest:                    row.Digest,
		ManifestJSON:              row.ManifestJson,
		CreatedBy:                 row.CreatedBy,
		CreatedAt:                 row.CreatedAt,
		Error:                     row.Error,
		DuckLakeSnapshotID:        row.DucklakeSnapshotID,
	}
	if row.ActivatedAt.Valid {
		out.ActivatedAt = row.ActivatedAt.String
	}
	if row.SupersededAt.Valid {
		out.SupersededAt = row.SupersededAt.String
	}
	return out
}

func mapArtifact(row platformdb.ServingStateArtifact) servingstate.Artifact {
	return servingstate.Artifact{
		ID:             row.ID,
		ServingStateID: servingstate.ID(row.ServingStateID),
		Digest:         row.Digest,
		Format:         row.Format,
		Path:           row.Path,
		ManifestJSON:   row.ManifestJson,
		SizeBytes:      row.SizeBytes,
		CreatedAt:      row.CreatedAt,
	}
}

func mapArtifactParams(artifact servingstate.Artifact) platformdb.InsertServingStateArtifactParams {
	return platformdb.InsertServingStateArtifactParams{
		ID:             artifact.ID,
		ServingStateID: string(artifact.ServingStateID),
		Digest:         artifact.Digest,
		Format:         artifact.Format,
		Path:           artifact.Path,
		ManifestJson:   artifact.ManifestJSON,
		SizeBytes:      artifact.SizeBytes,
	}
}

func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return servingstate.ErrNotFound
	}
	return err
}

func sqliteTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05")
}

func newID(prefix string) string {
	return prefix + "_" + newSecret()[:24]
}

func newSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		sum := sha256.Sum256([]byte(time.Now().Format(time.RFC3339Nano)))
		return hex.EncodeToString(sum[:])
	}
	return hex.EncodeToString(b[:])
}

func stableID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:32]
}

func contentDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func assetSnapshotID(servingStateID, assetID string) string {
	return "asset_" + stableID(servingStateID+"|"+assetID)
}

func edgeID(servingStateID, from, to, relation string) string {
	return "edge_" + stableID(servingStateID+"|"+from+"|"+to+"|"+relation)
}

func formatSQLiteTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
