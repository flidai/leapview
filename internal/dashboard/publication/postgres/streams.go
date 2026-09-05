package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	"github.com/flidai/leapview/internal/dashboard/publication"
	publicationdb "github.com/flidai/leapview/internal/dashboard/publication/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const streamLease = 90 * time.Second
const streamHeartbeat = 30 * time.Second

type StreamRegistry struct {
	mu      sync.Mutex
	streams map[string]map[string]*localStream
	db      DBTX
	q       *publicationdb.Queries
}

// MaintenanceDBTX is the intentionally narrow database surface required by
// retention work. Keeping it separate from the runtime registry's DBTX makes
// the destructive capability explicit at construction and review boundaries.
type MaintenanceDBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Maintenance owns destructive retention work and must be constructed with a
// database role granted DELETE on stream state. Runtime stream registries do
// not receive that authority.
type Maintenance struct{ q *publicationdb.Queries }

func NewMaintenance(db MaintenanceDBTX) *Maintenance {
	if db == nil {
		return &Maintenance{}
	}
	return &Maintenance{q: publicationdb.New(db)}
}

func (m *Maintenance) PruneExpired(ctx context.Context, now, _ time.Time, batchLimit int32) error {
	if m == nil || m.q == nil {
		return fmt.Errorf("publication maintenance database is unavailable")
	}
	if batchLimit == 0 {
		batchLimit = 1000
	}
	if batchLimit < 1 || batchLimit > 1000 {
		return fmt.Errorf("publication maintenance batch limit must be between 1 and 1000")
	}
	if _, err := m.q.DeleteExpiredStreams(ctx, publicationdb.DeleteExpiredStreamsParams{Now: now, BatchLimit: batchLimit}); err != nil {
		return err
	}
	return nil
}

type localStream struct {
	cancel         context.CancelFunc
	version        publication.StreamVersion
	registrationID string
}

type streamKey struct {
	publicationID string
	streamID      string
}

func NewStreamRegistry(db DBTX) *StreamRegistry {
	if db == nil {
		return &StreamRegistry{streams: map[string]map[string]*localStream{}}
	}
	return &StreamRegistry{streams: map[string]map[string]*localStream{}, db: db, q: publicationdb.New(db)}
}

// IsNative marks a registry backed by a configured PostgreSQL capability.
func (r *StreamRegistry) IsNative() bool { return r != nil && r.db != nil && r.q != nil }

func (r *StreamRegistry) Register(parent context.Context, publicationID, streamID string, version publication.StreamVersion, initialFilters ...dashboard.Filters) (context.Context, func(), error) {
	ctx, cancel := context.WithCancel(parent)
	registrationID, err := streamRegistrationID()
	if err != nil {
		cancel()
		return ctx, func() {}, err
	}
	filters := dashboard.Filters{}.WithDefaults()
	if len(initialFilters) > 0 {
		filters = initialFilters[0].WithDefaults()
	}
	filtersJSON, err := json.Marshal(filters)
	if err != nil {
		cancel()
		return ctx, func() {}, err
	}
	publicationUUID, err := nativeUUID(publicationID)
	if err != nil {
		cancel()
		return ctx, func() {}, err
	}
	registrationUUID, err := nativeUUID(registrationID)
	if err != nil {
		cancel()
		return ctx, func() {}, err
	}
	// Serialize the durable upsert and local ownership change for this registry.
	// Without one critical section, two concurrent registrations can commit in
	// database order but install the local winner in the opposite order.
	r.mu.Lock()
	if err := r.q.UpsertStream(parent, publicationdb.UpsertStreamParams{
		PublicationID: publicationUUID, StreamID: streamID,
		PublicID: version.PublicID, ServingStateID: version.ServingStateID,
		RegistrationID: registrationUUID, FiltersJson: filtersJSON, ExpiresAt: streamExpiry(),
	}); err != nil {
		r.mu.Unlock()
		cancel()
		return ctx, func() {}, err
	}
	if r.streams[publicationID] == nil {
		r.streams[publicationID] = map[string]*localStream{}
	}
	if previous := r.streams[publicationID][streamID]; previous != nil {
		previous.cancel()
	}
	registration := &localStream{cancel: cancel, version: version, registrationID: registrationID}
	r.streams[publicationID][streamID] = registration
	r.mu.Unlock()
	go r.heartbeat(ctx, publicationID, streamID, version, registrationID)
	return ctx, func() {
		r.mu.Lock()
		if current := r.streams[publicationID][streamID]; current == registration {
			delete(r.streams[publicationID], streamID)
			if len(r.streams[publicationID]) == 0 {
				delete(r.streams, publicationID)
			}
		}
		r.mu.Unlock()
		cancel()
		_, _ = r.q.ExpireStreamRegistration(
			context.WithoutCancel(parent),
			publicationdb.ExpireStreamRegistrationParams{
				PublicationID: publicationUUID, StreamID: streamID, RegistrationID: registrationUUID,
			},
		)
	}, nil
}

func (r *StreamRegistry) PrepareCommand(ctx context.Context, publicationID, streamID string, version publication.StreamVersion, prepare func(dashboard.Filters) (command.PreparedRefresh, error)) (command.PreparedRefresh, uint64, error) {
	if prepare == nil {
		return command.PreparedRefresh{}, 0, fmt.Errorf("publication command preparation is required")
	}
	publicationUUID, err := nativeUUID(publicationID)
	if err != nil {
		return command.PreparedRefresh{}, 0, err
	}
	registrationID, ok := r.currentRegistration(publicationID, streamID, version)
	if !ok {
		return command.PreparedRefresh{}, 0, publication.ErrStreamStateUnavailable
	}
	registrationUUID, err := nativeUUID(registrationID)
	if err != nil {
		return command.PreparedRefresh{}, 0, err
	}
	for attempt := 0; attempt < 8; attempt++ {
		row, err := r.q.GetCommandState(ctx, publicationdb.GetCommandStateParams{
			PublicationID: publicationUUID, StreamID: streamID,
			PublicID: version.PublicID, ServingStateID: version.ServingStateID,
			RegistrationID: registrationUUID,
		})
		if err != nil {
			return command.PreparedRefresh{}, 0, fmt.Errorf("load publication command state: %w", err)
		}
		var filters dashboard.Filters
		if err := json.Unmarshal([]byte(row.FiltersJson), &filters); err != nil {
			return command.PreparedRefresh{}, 0, fmt.Errorf("decode publication command state: %w", err)
		}
		prepared, err := prepare(filters.WithDefaults())
		if err != nil {
			return command.PreparedRefresh{}, 0, err
		}
		nextFilters, err := json.Marshal(prepared.Filters.WithDefaults())
		if err != nil {
			return command.PreparedRefresh{}, 0, err
		}
		nextGeneration := row.Generation + 1
		result, err := r.q.UpdateCommandState(ctx, publicationdb.UpdateCommandStateParams{
			FiltersJson: nextFilters, NextGeneration: nextGeneration, ExpiresAt: streamExpiry(),
			PublicationID: publicationUUID, StreamID: streamID,
			PublicID: version.PublicID, ServingStateID: version.ServingStateID,
			RegistrationID:    registrationUUID,
			CurrentGeneration: row.Generation,
		})
		if err != nil {
			return command.PreparedRefresh{}, 0, err
		}
		changed := result
		if changed == 1 {
			return prepared, uint64(nextGeneration), nil
		}
	}
	return command.PreparedRefresh{}, 0, fmt.Errorf("publication command state changed concurrently")
}

// currentRegistration binds command CAS operations to the local stream
// registration. A durable row with the right public/serving version is not
// sufficient: a newer registration for the same stream must fence this
// process's in-flight command.
func (r *StreamRegistry) currentRegistration(publicationID, streamID string, version publication.StreamVersion) (string, bool) {
	r.mu.Lock()
	stream := r.streams[publicationID][streamID]
	if stream == nil || stream.version != version {
		r.mu.Unlock()
		return "", false
	}
	registrationID := stream.registrationID
	r.mu.Unlock()
	return registrationID, true
}

func (r *StreamRegistry) Active(publicationID, streamID string, version publication.StreamVersion) bool {
	publicationUUID, err := nativeUUID(publicationID)
	if err != nil {
		return false
	}
	registrationID, ok := r.currentRegistration(publicationID, streamID, version)
	if !ok {
		return false
	}
	registrationUUID, err := nativeUUID(registrationID)
	if err != nil {
		return false
	}
	exists, err := r.q.StreamActive(context.Background(), publicationdb.StreamActiveParams{
		PublicationID: publicationUUID, StreamID: streamID,
		PublicID: version.PublicID, ServingStateID: version.ServingStateID,
		RegistrationID: registrationUUID,
	})
	return err == nil && exists
}

func (r *StreamRegistry) Reconcile(ctx context.Context, active map[string]publication.StreamVersion) {
	r.mu.Lock()
	durableRegistrations, durableRegistrationsLoaded := r.loadDurableRegistrations(ctx)
	stale := []context.CancelFunc{}
	for publicationID, streams := range r.streams {
		current, ok := active[publicationID]
		for streamID, stream := range streams {
			// Losing the durable ownership read is fail-closed. The client can
			// reconnect and establish a fresh registration after PostgreSQL
			// recovers; retaining an unverifiable owner could route commands to a
			// superseded node.
			registrationCurrent := durableRegistrationsLoaded && durableRegistrations[streamKey{publicationID: publicationID, streamID: streamID}] == stream.registrationID
			if ok && stream.version == current && registrationCurrent {
				continue
			}
			stale = append(stale, stream.cancel)
			delete(streams, streamID)
		}
		if len(streams) == 0 {
			delete(r.streams, publicationID)
		}
	}
	r.mu.Unlock()
	for _, cancel := range stale {
		cancel()
	}
}

func (r *StreamRegistry) loadDurableRegistrations(ctx context.Context) (map[streamKey]string, bool) {
	rows, err := r.q.ListActiveStreams(ctx)
	if err != nil {
		return nil, false
	}
	registrations := make(map[streamKey]string, len(rows))
	for _, row := range rows {
		registrations[streamKey{publicationID: row.PublicationID, streamID: row.StreamID}] = row.RegistrationID
	}
	return registrations, true
}

func (r *StreamRegistry) ClosePublication(publicationID string) {
	r.mu.Lock()
	streams := r.streams[publicationID]
	delete(r.streams, publicationID)
	// Keep registration and publication expiry in the same registry critical
	// section. A concurrent Register must happen wholly before this close (and
	// be expired/canceled) or wholly after it (and remain active).
	if publicationUUID, err := nativeUUID(publicationID); err == nil {
		_, _ = r.q.ExpirePublicationStreams(context.Background(), publicationUUID)
	}
	r.mu.Unlock()
	for _, stream := range streams {
		stream.cancel()
	}
}

func (r *StreamRegistry) heartbeat(ctx context.Context, publicationID, streamID string, version publication.StreamVersion, registrationID string) {
	publicationUUID, err := nativeUUID(publicationID)
	if err != nil {
		return
	}
	registrationUUID, err := nativeUUID(registrationID)
	if err != nil {
		return
	}
	ticker := time.NewTicker(streamHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := r.q.ExtendStream(ctx, publicationdb.ExtendStreamParams{
				ExpiresAt: streamExpiry(), PublicationID: publicationUUID,
				StreamID: streamID, PublicID: version.PublicID,
				ServingStateID: version.ServingStateID, RegistrationID: registrationUUID,
			})
			if err != nil {
				continue
			}
			if result == 0 {
				return
			}
		}
	}
}

func streamExpiry() time.Time {
	return time.Now().UTC().Add(streamLease)
}

func streamRegistrationID() (string, error) {
	value, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return value.String(), nil
}
