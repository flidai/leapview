package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	publicationdb "github.com/flidai/leapview/internal/dashboard/internal/db"
	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

type Repository struct {
	db    *sql.DB
	q     *publicationdb.Queries
	audit access.AuditIntentRecorder
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db, q: publicationdb.New(db)}
}

// NewRepositoryWithAudit wires publication mutations to Access' narrow
// transaction-scoped audit-intent port. The recorder participates in the
// transaction opened by this repository and never commits or rolls it back.
func NewRepositoryWithAudit(db *sql.DB, audit access.AuditIntentRecorder) *Repository {
	return &Repository{db: db, q: publicationdb.New(db), audit: audit}
}

func mapPublication(row publicationdb.DashboardPublication) (publication.Publication, error) {
	projectID, err := projectgraph.NewResourceID(strings.TrimSpace(row.ProjectID))
	if err != nil {
		return publication.Publication{}, fmt.Errorf("decode publication project ID: %w", err)
	}
	out := publication.Publication{
		ID: row.ID, ProjectID: projectID, Name: row.Name,
		PublicID: row.PublicID, Dashboard: row.Dashboard, DefaultPage: row.DefaultPage,
		ConfigurationDigest: row.ConfigurationDigest, Configured: row.Configured == 1,
		Revision:       row.Revision,
		ServingStateID: row.ActiveServingStateID.String, SuspendedAt: row.SuspendedAt.String,
		SuspendedBy: row.SuspendedBy, ConfiguredAt: row.ConfiguredAt.String,
		DisabledAt: row.DisabledAt.String, RotatedAt: row.RotatedAt.String,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if err := json.Unmarshal([]byte(row.AllowedOriginsJson), &out.AllowedOrigins); err != nil {
		return publication.Publication{}, fmt.Errorf("decode publication origins: %w", err)
	}
	if err := json.Unmarshal([]byte(row.DependencyAssetIdsJson), &out.DependencyAssetIDs); err != nil {
		return publication.Publication{}, fmt.Errorf("decode publication dependencies: %w", err)
	}
	return out, nil
}

func (r *Repository) Get(ctx context.Context, projectID projectgraph.ResourceID, name string) (publication.Publication, error) {
	row, err := r.q.GetDashboardPublication(ctx, publicationdb.GetDashboardPublicationParams{
		ProjectID: strings.TrimSpace(projectID.String()),
		Name:      strings.TrimSpace(name),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return publication.Publication{}, publication.ErrNotFound
	}
	if err != nil {
		return publication.Publication{}, err
	}
	return mapPublication(row)
}

func (r *Repository) GetByPublicID(ctx context.Context, publicID string) (publication.Publication, error) {
	row, err := r.q.GetDashboardPublicationByPublicID(ctx, strings.TrimSpace(publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return publication.Publication{}, publication.ErrNotFound
	}
	if err != nil {
		return publication.Publication{}, err
	}
	return mapPublication(row)
}

func (r *Repository) List(ctx context.Context, projectID projectgraph.ResourceID) ([]publication.Publication, error) {
	rows, err := r.q.ListDashboardPublications(ctx, strings.TrimSpace(projectID.String()))
	return mapPublications(rows, err)
}

func (r *Repository) ListAll(ctx context.Context) ([]publication.Publication, error) {
	rows, err := r.q.ListAllDashboardPublications(ctx)
	return mapPublications(rows, err)
}

func (r *Repository) ListEvents(ctx context.Context, publicationID string) ([]publication.Event, error) {
	rows, err := r.q.ListDashboardPublicationEvents(ctx, strings.TrimSpace(publicationID))
	if err != nil {
		return nil, err
	}
	out := make([]publication.Event, 0, len(rows))
	for _, row := range rows {
		out = append(out, publication.Event{
			Type: row.EventType, ActorID: row.ActorID,
			ServingStateID: row.ServingStateID, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

func mapPublications(rows []publicationdb.DashboardPublication, err error) ([]publication.Publication, error) {
	if err != nil {
		return nil, err
	}
	out := make([]publication.Publication, 0, len(rows))
	for _, row := range rows {
		mapped, err := mapPublication(row)
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func (r *Repository) Suspend(ctx context.Context, projectID projectgraph.ResourceID, name, actorID string, expectedRevision int64) (publication.Publication, error) {
	if expectedRevision <= 0 {
		return publication.Publication{}, publication.ErrConflict
	}
	return r.mutate(ctx, projectID, name, actorID, "suspended", func(q *publicationdb.Queries) (sql.Result, error) {
		return q.SuspendDashboardPublication(ctx, publicationdb.SuspendDashboardPublicationParams{
			ActorID: strings.TrimSpace(actorID), ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name), ExpectedRevision: expectedRevision,
		})
	})
}

func (r *Repository) Resume(ctx context.Context, projectID projectgraph.ResourceID, name, actorID string, expectedRevision int64) (publication.Publication, error) {
	if expectedRevision <= 0 {
		return publication.Publication{}, publication.ErrConflict
	}
	return r.mutate(ctx, projectID, name, actorID, "resumed", func(q *publicationdb.Queries) (sql.Result, error) {
		return q.ResumeDashboardPublication(ctx, publicationdb.ResumeDashboardPublicationParams{
			ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name), ExpectedRevision: expectedRevision,
		})
	})
}

func (r *Repository) Rotate(ctx context.Context, projectID projectgraph.ResourceID, name, actorID string, expectedRevision int64) (publication.Publication, error) {
	if expectedRevision <= 0 {
		return publication.Publication{}, publication.ErrConflict
	}
	publicID, err := newPublicID()
	if err != nil {
		return publication.Publication{}, err
	}
	return r.mutate(ctx, projectID, name, actorID, "rotated", func(q *publicationdb.Queries) (sql.Result, error) {
		return q.RotateDashboardPublication(ctx, publicationdb.RotateDashboardPublicationParams{
			PublicID: publicID, ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name), ExpectedRevision: expectedRevision,
		})
	})
}

func (r *Repository) mutate(
	ctx context.Context,
	projectID projectgraph.ResourceID, name, actorID, eventType string,
	mutation func(*publicationdb.Queries) (sql.Result, error),
) (publication.Publication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return publication.Publication{}, err
	}
	defer tx.Rollback()
	q := r.q.WithTx(tx)
	result, err := mutation(q)
	if err != nil {
		return publication.Publication{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return publication.Publication{}, err
	}
	if changed == 0 {
		_, err := q.GetDashboardPublicationConfiguredState(ctx, publicationdb.GetDashboardPublicationConfiguredStateParams{
			ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name),
		})
		if errors.Is(err, sql.ErrNoRows) {
			return publication.Publication{}, publication.ErrNotFound
		}
		if err != nil {
			return publication.Publication{}, err
		}
		return publication.Publication{}, publication.ErrConflict
	}
	stored, err := q.GetConfiguredDashboardPublication(ctx, publicationdb.GetConfiguredDashboardPublicationParams{
		ProjectID: strings.TrimSpace(projectID.String()), Name: strings.TrimSpace(name),
	})
	if err != nil {
		return publication.Publication{}, err
	}
	row, err := mapPublication(stored)
	if err != nil {
		return publication.Publication{}, err
	}
	if err := insertEvent(ctx, q, row.ID, eventType, actorID, row.ServingStateID); err != nil {
		return publication.Publication{}, err
	}
	if err := r.recordAuditIntent(ctx, tx, row.ID, &row); err != nil {
		return publication.Publication{}, err
	}
	if err := tx.Commit(); err != nil {
		return publication.Publication{}, err
	}
	return r.Get(ctx, projectID, name)
}

func (r *Repository) recordAuditIntent(ctx context.Context, tx *sql.Tx, publicationID string, row *publication.Publication) error {
	intent, ok := publication.AuditIntentFromContext(ctx)
	if !ok {
		return nil
	}
	if r.audit == nil {
		return fmt.Errorf("dashboard publication audit intent recorder is required")
	}
	if row == nil {
		return fmt.Errorf("dashboard publication audit intent publication is required")
	}
	// Publication events are the source-owned aggregate sequence. Counting
	// after inserting the event gives each committed command a deterministic,
	// monotonic sequence while preserving the stable aggregate identity built
	// by the command producer.
	sequence, err := r.q.WithTx(tx).CountDashboardPublicationEvents(ctx, publicationID)
	if err != nil {
		return fmt.Errorf("dashboard publication audit intent sequence: %w", err)
	}
	intent.AggregateSequence = sequence
	return r.audit.RecordAuditIntent(ctx, tx, intent)
}

func (r *Repository) ReconcileTx(
	ctx context.Context,
	tx transaction.Transaction,
	input publication.ReconcileInput,
	activatePrincipal func(context.Context, transaction.Transaction, projectgraph.ResourceID, string) error,
) error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("publication reconciliation requires project: %w", err)
	}
	input.ServingStateID = strings.TrimSpace(input.ServingStateID)
	if input.ServingStateID == "" {
		return fmt.Errorf("publication reconciliation requires serving state")
	}
	if len(input.Publications) > 0 && activatePrincipal == nil {
		return fmt.Errorf("publication reconciliation requires an Access principal activator")
	}
	q := publicationdb.New(tx)
	rows, err := q.ListProjectDashboardPublicationStates(ctx, input.ProjectID.String())
	if err != nil {
		return err
	}
	type existingRow struct {
		id, name, digest, servingStateID string
		configured                       bool
	}
	existing := make(map[string]existingRow, len(rows))
	for _, row := range rows {
		existing[row.Name] = existingRow{
			id: row.ID, name: row.Name, digest: row.ConfigurationDigest,
			servingStateID: row.ActiveServingStateID, configured: row.Configured == 1,
		}
	}

	for name, row := range existing {
		if _, ok := input.Publications[name]; ok || !row.configured {
			continue
		}
		if err := q.DisableDashboardPublication(ctx, row.id); err != nil {
			return err
		}
		if err := insertEvent(ctx, q, row.id, "disabled", input.ActorID, input.ServingStateID); err != nil {
			return err
		}
	}

	names := make([]string, 0, len(input.Publications))
	for name := range input.Publications {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		compiled := input.Publications[name]
		if err := activatePrincipal(ctx, tx, input.ProjectID, name); err != nil {
			return fmt.Errorf("reconcile publication principal %q: %w", name, err)
		}
		origins, err := json.Marshal(compiled.AllowedOrigins)
		if err != nil {
			return err
		}
		dependencies, err := json.Marshal(compiled.DependencyAssetIDs)
		if err != nil {
			return err
		}
		if current, ok := existing[name]; ok {
			eventType := ""
			if !current.configured {
				eventType = "configured"
			} else if current.digest != compiled.ConfigurationDigest {
				eventType = "configuration_changed"
			} else if current.servingStateID != input.ServingStateID {
				eventType = "serving_state_changed"
			}
			if eventType == "" {
				continue
			}
			if err := q.UpdateDashboardPublicationConfiguration(ctx, publicationdb.UpdateDashboardPublicationConfigurationParams{
				Dashboard: compiled.Dashboard, DefaultPage: compiled.DefaultPage,
				ConfigurationDigest: compiled.ConfigurationDigest,
				AllowedOriginsJson:  string(origins), DependencyAssetIdsJson: string(dependencies),
				ActiveServingStateID: sql.NullString{String: input.ServingStateID, Valid: true},
				ID:                   current.id,
			}); err != nil {
				return err
			}
			if err := insertEvent(ctx, q, current.id, eventType, input.ActorID, input.ServingStateID); err != nil {
				return err
			}
			continue
		}
		publicID, err := newPublicID()
		if err != nil {
			return err
		}
		idValue, err := uuid.NewV7()
		if err != nil {
			return err
		}
		id := idValue.String()
		if err := q.CreateDashboardPublication(ctx, publicationdb.CreateDashboardPublicationParams{
			ID: id, ProjectID: input.ProjectID.String(), Name: name, PublicID: publicID,
			Dashboard: compiled.Dashboard, DefaultPage: compiled.DefaultPage,
			ConfigurationDigest: compiled.ConfigurationDigest,
			AllowedOriginsJson:  string(origins), DependencyAssetIdsJson: string(dependencies),
			ActiveServingStateID: sql.NullString{String: input.ServingStateID, Valid: true},
		}); err != nil {
			return err
		}
		if err := insertEvent(ctx, q, id, "configured", input.ActorID, input.ServingStateID); err != nil {
			return err
		}
	}
	return nil
}

func insertEvent(ctx context.Context, q *publicationdb.Queries, id, eventType, actorID, servingStateID string) error {
	return q.InsertDashboardPublicationEvent(ctx, publicationdb.InsertDashboardPublicationEventParams{
		PublicationID: id, EventType: eventType,
		ActorID: strings.TrimSpace(actorID), ServingStateID: servingStateID,
	})
}

func newPublicID() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
