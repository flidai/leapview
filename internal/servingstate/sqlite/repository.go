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
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/digest"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	platformdb "github.com/flidai/leapview/internal/servingstate/internal/db"
	servingstatevalidation "github.com/flidai/leapview/internal/servingstate/validation"
)

type Repository struct {
	db *sql.DB
	q  *platformdb.Queries
}

func NewRepository(sqlDB *sql.DB) *Repository {
	return &Repository{db: sqlDB, q: platformdb.New(sqlDB)}
}

func (r *Repository) Create(ctx context.Context, input servingstate.CreateInput) (servingstate.State, error) {
	id := servingstate.ID(newID("state"))
	if err := r.q.CreateServingState(ctx, platformdb.CreateServingStateParams{
		ID:          string(id),
		WorkspaceID: string(input.WorkspaceID),
		ProjectID:   strings.TrimSpace(input.ProjectID),
		Environment: string(servingstate.NormalizeEnvironment(input.Environment)),
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
	return r.q.ListReferencedDuckLakeSnapshots(ctx, environment)
}

func (r *Repository) ActiveDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	return r.q.ListActiveDuckLakeSnapshots(ctx, environment)
}

func (r *Repository) LeasedDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	return r.q.ListLeasedDuckLakeSnapshots(ctx, environment)
}

func (r *Repository) ForeignEnvironmentDuckLakeSnapshots(ctx context.Context, environment string) ([]int64, error) {
	return r.q.ListForeignEnvironmentDuckLakeSnapshots(ctx, environment)
}

func (r *Repository) CreateQuerySnapshotLease(ctx context.Context, input servingstate.SnapshotLeaseInput) (string, error) {
	if input.WorkspaceID == "" {
		return "", fmt.Errorf("workspace id is required")
	}
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
		WorkspaceID:        string(input.WorkspaceID),
		Environment:        string(servingstate.NormalizeEnvironment(input.Environment)),
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
	return r.q.ReleaseExpiredQuerySnapshotLeases(ctx, environment)
}

func (r *Repository) ExpireInactiveServingStates(ctx context.Context, environment string) error {
	return r.q.ExpireInactiveServingStates(ctx, environment)
}

func (r *Repository) ScheduleExpiredServingStateDeletion(ctx context.Context, environment string) error {
	return r.q.ScheduleExpiredServingStateDeletion(ctx, environment)
}

func (r *Repository) MarkDeleteScheduledServingStatesDeleted(ctx context.Context, environment string) error {
	return r.q.MarkDeleteScheduledServingStatesDeleted(ctx, environment)
}

func (r *Repository) ReconcileRetention(ctx context.Context, environment string, now time.Time) error {
	if now.IsZero() {
		now = time.Now()
	}
	if err := r.q.MarkDrainingServingStatesDeleteScheduled(ctx, environment); err != nil {
		return err
	}
	return r.q.MarkDeleteScheduledServingStatesDeleted(ctx, environment)
}

func (r *Repository) SaveValidated(ctx context.Context, servingStateID servingstate.ID, validation servingstate.Validation, artifact servingstate.Artifact) (servingstate.State, error) {
	validation.ProjectID = strings.TrimSpace(validation.ProjectID)
	if validation.ProjectID == "" {
		return servingstate.State{}, fmt.Errorf("validated serving state requires project id")
	}
	if err := digest.ValidateSHA256Identity(validation.ProjectDigest); err != nil {
		return servingstate.State{}, fmt.Errorf("validated serving state requires project digest: %w", err)
	}
	if len(validation.ProjectWorkspaces) == 0 || !sort.StringsAreSorted(validation.ProjectWorkspaces) {
		return servingstate.State{}, fmt.Errorf("validated serving state requires sorted project workspaces")
	}
	projectWorkspacesJSON, err := json.Marshal(validation.ProjectWorkspaces)
	if err != nil {
		return servingstate.State{}, err
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
	artifact.ServingStateID = servingStateID
	current, err := q.GetServingState(ctx, string(servingStateID))
	if err != nil {
		return servingstate.State{}, mapNotFound(err)
	}
	if artifact.WorkspaceID != servingstate.WorkspaceID(current.WorkspaceID) {
		return servingstate.State{}, fmt.Errorf("artifact workspace = %q, want %q", artifact.WorkspaceID, current.WorkspaceID)
	}
	if current.ProjectID != "" && current.ProjectID != validation.ProjectID {
		return servingstate.State{}, fmt.Errorf("artifact project = %q, want %q", validation.ProjectID, current.ProjectID)
	}
	if !containsExact(validation.ProjectWorkspaces, current.WorkspaceID) {
		return servingstate.State{}, fmt.Errorf("project workspaces omit candidate workspace %q", current.WorkspaceID)
	}
	if servingstate.NormalizeEnvironment(artifact.Environment) != servingstate.Environment(current.Environment) {
		return servingstate.State{}, fmt.Errorf("artifact environment = %q, want %q", servingstate.NormalizeEnvironment(artifact.Environment), current.Environment)
	}
	switch servingstate.Status(current.Status) {
	case servingstate.StatusPending:
	case servingstate.StatusValidated:
		existingArtifact, existingErr := q.GetArtifactByServingState(ctx, current.ID)
		if existingErr == nil && current.ProjectID == validation.ProjectID && current.ProjectDigest == validation.ProjectDigest && current.ProjectWorkspacesJson == string(projectWorkspacesJSON) && current.AccessPolicyJson == string(accessPolicyJSON) && current.DashboardPublicationsJson == string(canonicalPublicationsJSON) && current.DashboardAppearancesJson == string(canonicalAppearancesJSON) && current.Digest == validation.Digest && current.ManifestJson == validation.ManifestJSON && sameArtifact(existingArtifact, artifact) {
			return mapServingState(current), nil
		}
		return servingstate.State{}, fmt.Errorf("validated serving state %s is immutable", servingStateID)
	default:
		return servingstate.State{}, fmt.Errorf("serving state %s has status %q, want pending", servingStateID, current.Status)
	}
	if err := servingstatevalidation.ValidateAssetGraph(validation.Graph, current.WorkspaceID, string(servingStateID)); err != nil {
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
	for _, asset := range validation.Graph.Assets {
		if err := q.InsertAsset(ctx, platformdb.InsertAssetParams{
			SnapshotID:           string(asset.SnapshotID),
			LogicalAssetID:       string(asset.ID),
			WorkspaceID:          string(asset.WorkspaceID),
			ServingStateID:       string(asset.ServingStateID),
			AssetType:            string(asset.Type),
			AssetKey:             asset.Key,
			ParentLogicalAssetID: string(asset.ParentID),
			Title:                asset.Title,
			Description:          asset.Description,
			SourceFile:           asset.SourceFile,
			PayloadSchema:        asset.PayloadSchema,
			PayloadJson:          asset.PayloadJSON,
			ContentHash:          asset.ContentHash,
		}); err != nil {
			return servingstate.State{}, err
		}
	}
	for _, edge := range validation.Graph.Edges {
		if err := q.InsertAssetEdge(ctx, platformdb.InsertAssetEdgeParams{
			ID:                 string(edge.ID),
			WorkspaceID:        string(edge.WorkspaceID),
			ServingStateID:     string(edge.ServingStateID),
			FromLogicalAssetID: string(edge.FromAssetID),
			ToLogicalAssetID:   string(edge.ToAssetID),
			EdgeType:           string(edge.Type),
		}); err != nil {
			return servingstate.State{}, err
		}
	}
	if err := q.UpdateServingStateValidated(ctx, platformdb.UpdateServingStateValidatedParams{
		Status:                    string(servingstate.StatusValidated),
		ProjectID:                 validation.ProjectID,
		ProjectDigest:             validation.ProjectDigest,
		ProjectWorkspacesJson:     string(projectWorkspacesJSON),
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
	return existing.ServingStateID == string(candidate.ServingStateID) &&
		existing.WorkspaceID == string(candidate.WorkspaceID) &&
		existing.Environment == string(servingstate.NormalizeEnvironment(candidate.Environment)) &&
		existing.Digest == candidate.Digest && existing.Format == candidate.Format &&
		existing.Path == candidate.Path && existing.ManifestJson == candidate.ManifestJSON &&
		existing.SizeBytes == candidate.SizeBytes
}

func (r *Repository) Activate(ctx context.Context, workspaceID servingstate.WorkspaceID, environment servingstate.Environment, servingStateID servingstate.ID) (servingstate.State, error) {
	return r.activate(ctx, workspaceID, environment, servingStateID)
}

func (r *Repository) activate(ctx context.Context, workspaceID servingstate.WorkspaceID, environment servingstate.Environment, servingStateID servingstate.ID) (servingstate.State, error) {
	environment = servingstate.NormalizeEnvironment(environment)
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
	if current.WorkspaceID != workspaceID {
		return servingstate.State{}, fmt.Errorf("serving state %s is not in workspace %s", servingStateID, workspaceID)
	}
	if current.Environment != environment {
		return servingstate.State{}, fmt.Errorf("serving state %s environment = %q, want %q", servingStateID, current.Environment, environment)
	}
	if !current.CanActivate() {
		return servingstate.State{}, fmt.Errorf("serving state %s has status %q, want validated", servingStateID, current.Status)
	}
	if err := q.MarkOtherServingStatesDraining(ctx, platformdb.MarkOtherServingStatesDrainingParams{
		WorkspaceID: string(workspaceID),
		Environment: string(environment),
		ID:          string(servingStateID),
	}); err != nil {
		return servingstate.State{}, err
	}
	if err := q.MarkServingStateActive(ctx, string(servingStateID)); err != nil {
		return servingstate.State{}, err
	}
	if err := q.SetActiveServingState(ctx, platformdb.SetActiveServingStateParams{
		WorkspaceID:    string(workspaceID),
		Environment:    string(environment),
		ServingStateID: string(servingStateID),
	}); err != nil {
		return servingstate.State{}, err
	}
	if err := tx.Commit(); err != nil {
		return servingstate.State{}, err
	}
	return r.ByID(ctx, servingStateID)
}

func (r *Repository) ActiveArtifact(ctx context.Context, workspaceID servingstate.WorkspaceID, environment servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	row, err := r.q.GetActiveServingState(ctx, platformdb.GetActiveServingStateParams{WorkspaceID: string(workspaceID), Environment: string(servingstate.NormalizeEnvironment(environment))})
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
		out = append(out, servingstate.ActiveScope{WorkspaceID: servingstate.WorkspaceID(row.WorkspaceID), Environment: servingstate.Environment(row.Environment)})
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
	var projectWorkspaces []string
	_ = json.Unmarshal([]byte(row.ProjectWorkspacesJson), &projectWorkspaces)
	out := servingstate.State{
		ID:                        servingstate.ID(row.ID),
		WorkspaceID:               servingstate.WorkspaceID(row.WorkspaceID),
		ProjectID:                 row.ProjectID,
		ProjectDigest:             row.ProjectDigest,
		ProjectWorkspaces:         projectWorkspaces,
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

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func mapArtifact(row platformdb.ServingStateArtifact) servingstate.Artifact {
	return servingstate.Artifact{
		ID:             row.ID,
		ServingStateID: servingstate.ID(row.ServingStateID),
		WorkspaceID:    servingstate.WorkspaceID(row.WorkspaceID),
		Environment:    servingstate.Environment(row.Environment),
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
		WorkspaceID:    string(artifact.WorkspaceID),
		Environment:    string(servingstate.NormalizeEnvironment(artifact.Environment)),
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
	sum := sha256.Sum256([]byte(strings.ToLower(value)))
	return hex.EncodeToString(sum[:])[:32]
}

func formatSQLiteTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
