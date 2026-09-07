package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	publicationdb "github.com/flidai/leapview/internal/dashboard/internal/db"
	"github.com/flidai/leapview/internal/dashboard/publication"
)

const streamLease = 90 * time.Second
const streamHeartbeat = 30 * time.Second

type StreamRegistry struct {
	mu      sync.Mutex
	streams map[string]map[string]*localStream
	db      *sql.DB
	q       *publicationdb.Queries
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

func NewStreamRegistry(db *sql.DB) *StreamRegistry {
	return &StreamRegistry{streams: map[string]map[string]*localStream{}, db: db, q: publicationdb.New(db)}
}

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
	if err := r.q.UpsertDashboardPublicationStream(parent, publicationdb.UpsertDashboardPublicationStreamParams{
		PublicationID: publicationID, StreamID: streamID,
		PublicID: version.PublicID, ServingStateID: version.ServingStateID,
		RegistrationID: registrationID, FiltersJson: string(filtersJSON), ExpiresAt: streamExpiry(),
	}); err != nil {
		cancel()
		return ctx, func() {}, err
	}
	r.mu.Lock()
	if r.streams[publicationID] == nil {
		r.streams[publicationID] = map[string]*localStream{}
	}
	if previous := r.streams[publicationID][streamID]; previous != nil {
		previous.cancel()
	}
	registration := &localStream{cancel: cancel, version: version, registrationID: registrationID}
	r.streams[publicationID][streamID] = registration
	r.mu.Unlock()
	go r.heartbeat(ctx, publicationID, streamID, registrationID)
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
		_ = r.q.DeleteDashboardPublicationStreamRegistration(
			context.WithoutCancel(parent),
			publicationdb.DeleteDashboardPublicationStreamRegistrationParams{
				PublicationID: publicationID, StreamID: streamID, RegistrationID: registrationID,
			},
		)
	}, nil
}

func (r *StreamRegistry) PrepareCommand(ctx context.Context, publicationID, streamID string, version publication.StreamVersion, prepare func(dashboard.Filters) (command.PreparedRefresh, error)) (command.PreparedRefresh, uint64, error) {
	if prepare == nil {
		return command.PreparedRefresh{}, 0, fmt.Errorf("publication command preparation is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		row, err := r.q.GetDashboardPublicationCommandState(ctx, publicationdb.GetDashboardPublicationCommandStateParams{
			PublicationID: publicationID, StreamID: streamID,
			PublicID: version.PublicID, ServingStateID: version.ServingStateID, Now: now,
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
		result, err := r.q.UpdateDashboardPublicationCommandState(ctx, publicationdb.UpdateDashboardPublicationCommandStateParams{
			FiltersJson: string(nextFilters), NextGeneration: nextGeneration, ExpiresAt: streamExpiry(),
			PublicationID: publicationID, StreamID: streamID,
			PublicID: version.PublicID, ServingStateID: version.ServingStateID,
			CurrentGeneration: row.Generation, Now: now,
		})
		if err != nil {
			return command.PreparedRefresh{}, 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return command.PreparedRefresh{}, 0, err
		}
		if changed == 1 {
			return prepared, uint64(nextGeneration), nil
		}
	}
	return command.PreparedRefresh{}, 0, fmt.Errorf("publication command state changed concurrently")
}

func (r *StreamRegistry) Active(publicationID, streamID string, version publication.StreamVersion) bool {
	exists, err := r.q.DashboardPublicationStreamIsActive(context.Background(), publicationdb.DashboardPublicationStreamIsActiveParams{
		PublicationID: publicationID, StreamID: streamID,
		PublicID: version.PublicID, ServingStateID: version.ServingStateID,
		Now: time.Now().UTC().Format(time.RFC3339Nano),
	})
	return err == nil && exists
}

func (r *StreamRegistry) Reconcile(ctx context.Context, active map[string]publication.StreamVersion) {
	now := time.Now().UTC()
	_ = r.q.DeleteExpiredDashboardPublicationStreams(ctx, now.Format(time.RFC3339Nano))
	durableRegistrations, durableRegistrationsLoaded := r.loadDurableRegistrations(ctx)
	r.mu.Lock()
	stale := []context.CancelFunc{}
	for publicationID, streams := range r.streams {
		current, ok := active[publicationID]
		for streamID, stream := range streams {
			registrationCurrent := true
			if durableRegistrationsLoaded {
				registrationCurrent = durableRegistrations[streamKey{publicationID: publicationID, streamID: streamID}] == stream.registrationID
			}
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
	rows, err := r.q.ListActiveDashboardPublicationStreamRegistrations(ctx, time.Now().UTC().Format(time.RFC3339Nano))
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
	r.mu.Unlock()
	for _, stream := range streams {
		stream.cancel()
	}
	_ = r.q.DeleteDashboardPublicationStreams(context.Background(), publicationID)
}

func (r *StreamRegistry) heartbeat(ctx context.Context, publicationID, streamID, registrationID string) {
	ticker := time.NewTicker(streamHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := r.q.ExtendDashboardPublicationStreamRegistration(ctx, publicationdb.ExtendDashboardPublicationStreamRegistrationParams{
				ExpiresAt: streamExpiry(), PublicationID: publicationID,
				StreamID: streamID, RegistrationID: registrationID,
			})
			if err != nil {
				continue
			}
			if changed, err := result.RowsAffected(); err == nil && changed == 0 {
				return
			}
		}
	}
}

func streamExpiry() string {
	return time.Now().UTC().Add(streamLease).Format(time.RFC3339Nano)
}

func streamRegistrationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
