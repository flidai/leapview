package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
)

const maximumSpatialTileRevisions = 8192
const replacedSpatialTileRevisionGrace = 2 * time.Minute

type spatialTileRevision struct {
	DashboardID string
	PageID      string
	VisualID    string
	PublicID    string
	PrincipalID string
	Filters     dashboard.Filters
	// RawMinimumZoom is computed once from the complete governed coordinate
	// grain. It makes precision revision-wide instead of tile-local.
	RawMinimumZoom int
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type spatialTilePublicationContextKey struct{}

// WithPublicSpatialTiles scopes tile revisions emitted while rendering a
// public or embedded dashboard to one active publication.
func WithPublicSpatialTiles(ctx context.Context, publicID string) context.Context {
	return context.WithValue(ctx, spatialTilePublicationContextKey{}, publicID)
}

func spatialTilePublicationFromContext(ctx context.Context) string {
	value, _ := ctx.Value(spatialTilePublicationContextKey{}).(string)
	return value
}

// spatialTileRegistry is scoped to one immutable serving runtime. Tokens die
// on serving-state cutover when the owning Service closes, and the hard cap
// bounds streams that repeatedly change filters during that lifetime.
type spatialTileRegistry struct {
	mu      sync.Mutex
	entries map[string]spatialTileRevision
	order   []string
}

func newSpatialTileRegistry() *spatialTileRegistry {
	return &spatialTileRegistry{entries: make(map[string]spatialTileRevision)}
}

func (r *spatialTileRegistry) register(entry spatialTileRevision) (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("create spatial tile revision: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for existingToken, existing := range r.entries {
		if existing.DashboardID == entry.DashboardID && existing.PageID == entry.PageID && existing.VisualID == entry.VisualID && existing.PublicID == entry.PublicID && existing.PrincipalID == entry.PrincipalID && existing.ExpiresAt.IsZero() {
			existing.ExpiresAt = now.Add(replacedSpatialTileRevisionGrace)
			r.entries[existingToken] = existing
		}
	}
	for len(r.order) >= maximumSpatialTileRevisions {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.entries, oldest)
	}
	entry.CreatedAt = now
	r.entries[token] = entry
	r.order = append(r.order, token)
	return token, nil
}

func (r *spatialTileRegistry) resolve(token, dashboardID, visualID, publicID, principalID string) (spatialTileRevision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[token]
	if !ok {
		return spatialTileRevision{}, fmt.Errorf("unknown spatial tile revision")
	}
	if !entry.ExpiresAt.IsZero() && !time.Now().Before(entry.ExpiresAt) {
		delete(r.entries, token)
		return spatialTileRevision{}, fmt.Errorf("expired spatial tile revision")
	}
	if entry.DashboardID != dashboardID || entry.VisualID != visualID || entry.PublicID != publicID || entry.PrincipalID != principalID {
		return spatialTileRevision{}, fmt.Errorf("spatial tile revision scope mismatch")
	}
	return entry, nil
}

func spatialTileURL(workspaceID, dashboardID, visualID, token string) string {
	return "/workspaces/" + url.PathEscape(workspaceID) + "/dashboards/" + url.PathEscape(dashboardID) + "/visuals/" + url.PathEscape(visualID) + "/tiles/" + url.PathEscape(token) + "/{z}/{x}/{y}.mvt"
}

func publicSpatialTileURL(publicID, visualID, token string) string {
	return "/public/dashboards/" + url.PathEscape(publicID) + "/visuals/" + url.PathEscape(visualID) + "/tiles/" + url.PathEscape(token) + "/{z}/{x}/{y}.mvt"
}

type SpatialTileResult struct {
	Bytes        []byte
	Features     int
	Precision    string
	CacheOutcome string
}

func (m *Service) QueryVisualizationTile(ctx context.Context, dashboardID, visualID, revision string, zoom, x, y int) (SpatialTileResult, error) {
	if m == nil || m.tiles == nil {
		return SpatialTileResult{}, fmt.Errorf("spatial tile runtime is unavailable")
	}
	entry, err := m.tiles.resolve(revision, dashboardID, visualID, "", dataquery.MetadataFromContext(ctx).PrincipalID)
	if err != nil {
		return SpatialTileResult{}, err
	}
	return m.snapshots.querySpatialTile(ctx, dashboardID, entry.PageID, entry.Filters, visualID, entry.RawMinimumZoom, zoom, x, y)
}

func (m *Service) QueryPublicVisualizationTile(ctx context.Context, publicID, dashboardID, visualID, revision string, zoom, x, y int) (SpatialTileResult, error) {
	if m == nil || m.tiles == nil {
		return SpatialTileResult{}, fmt.Errorf("spatial tile runtime is unavailable")
	}
	entry, err := m.tiles.resolve(revision, dashboardID, visualID, publicID, dataquery.MetadataFromContext(ctx).PrincipalID)
	if err != nil {
		return SpatialTileResult{}, err
	}
	return m.snapshots.querySpatialTile(ctx, dashboardID, entry.PageID, entry.Filters, visualID, entry.RawMinimumZoom, zoom, x, y)
}
