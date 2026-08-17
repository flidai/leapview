// Package session owns dashboard-view state independently from page routes.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/dashboard/filter"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrNotFound = errors.New("dashboard session not found")
	ErrConflict = errors.New("dashboard session changed concurrently")
)

type Key struct {
	ProjectID         projectgraph.ResourceID `json:"projectId,omitempty"`
	PublicationID     string                  `json:"publicationId,omitempty"`
	PrincipalOrClient string                  `json:"principalOrClient"`
	DashboardID       projectgraph.ResourceID `json:"dashboardID"`
	ServingStateID    string                  `json:"servingStateID"`
	StreamInstanceID  string                  `json:"streamInstanceID"`
}

func (key Key) Validate() error {
	hasProject := key.ProjectID != ""
	hasPublication := strings.TrimSpace(key.PublicationID) != ""
	if hasProject == hasPublication {
		return fmt.Errorf("dashboard session requires exactly one project or publication")
	}
	if hasProject {
		if err := key.ProjectID.Validate(); err != nil {
			return fmt.Errorf("dashboard session project ID is invalid: %w", err)
		}
	}
	if err := key.DashboardID.Validate(); err != nil {
		return fmt.Errorf("dashboard session dashboard ID is invalid: %w", err)
	}
	for name, value := range map[string]string{
		"principal or client": key.PrincipalOrClient,
		"dashboard ID":        key.DashboardID.String(),
		"serving-state ID":    key.ServingStateID,
		"stream instance ID":  key.StreamInstanceID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("dashboard session %s is required", name)
		}
	}
	return nil
}

func (key Key) ID() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		key.ProjectID.String(), key.PublicationID, key.PrincipalOrClient, key.DashboardID.String(),
		key.ServingStateID, key.StreamInstanceID,
	}, "\x00")))
	return "dvs_" + base64.RawURLEncoding.EncodeToString(sum[:])
}

type State struct {
	ActivePage            string                 `json:"activePage"`
	Filters               filter.MachineSnapshot `json:"filters"`
	InteractionSelections []map[string]any       `json:"interactionSelections"`
	SpatialSelections     []map[string]any       `json:"spatialSelections"`
	StreamGeneration      uint64                 `json:"streamGeneration"`
	NavigationMutationIDs []string               `json:"navigationMutationIDs,omitempty"`
}

func NewState(activePage string, filters filter.MachineSnapshot) State {
	return State{
		ActivePage: activePage, Filters: filters,
		InteractionSelections: []map[string]any{}, SpatialSelections: []map[string]any{},
		StreamGeneration: 1, NavigationMutationIDs: []string{},
	}
}

type Record struct {
	Key       Key       `json:"key"`
	Version   uint64    `json:"version"`
	State     State     `json:"state"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Store interface {
	Create(context.Context, Key, State) (Record, error)
	Load(context.Context, Key) (Record, error)
	CompareAndSwap(context.Context, Key, uint64, State) (Record, error)
	Touch(context.Context, Key) error
	DeleteExpired(context.Context) error
}

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	ttl     time.Duration
	clock   func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return NewMemoryStoreWithTTL(5 * time.Minute)
}

func NewMemoryStoreWithTTL(ttl time.Duration) *MemoryStore {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &MemoryStore{records: map[string]Record{}, ttl: ttl, clock: time.Now}
}

func (store *MemoryStore) Create(_ context.Context, key Key, state State) (Record, error) {
	if err := key.Validate(); err != nil {
		return Record{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id := key.ID()
	if current, ok := store.records[id]; ok && current.ExpiresAt.After(store.clock()) {
		return cloneRecord(current), ErrConflict
	}
	record := Record{Key: key, Version: 1, State: cloneState(state), ExpiresAt: store.expiry()}
	store.records[id] = record
	return cloneRecord(record), nil
}

func (store *MemoryStore) Load(_ context.Context, key Key) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.records[key.ID()]
	if !ok || !record.ExpiresAt.After(store.clock()) {
		delete(store.records, key.ID())
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (store *MemoryStore) CompareAndSwap(_ context.Context, key Key, version uint64, state State) (Record, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	id := key.ID()
	record, ok := store.records[id]
	if !ok || !record.ExpiresAt.After(store.clock()) {
		delete(store.records, id)
		return Record{}, ErrNotFound
	}
	if record.Version != version {
		return cloneRecord(record), ErrConflict
	}
	record.Version++
	record.State = cloneState(state)
	record.ExpiresAt = store.expiry()
	store.records[id] = record
	return cloneRecord(record), nil
}

func (store *MemoryStore) Touch(_ context.Context, key Key) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	id := key.ID()
	record, ok := store.records[id]
	if !ok || !record.ExpiresAt.After(store.clock()) {
		delete(store.records, id)
		return ErrNotFound
	}
	record.ExpiresAt = store.expiry()
	store.records[id] = record
	return nil
}

func (store *MemoryStore) DeleteExpired(_ context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.clock()
	for id, record := range store.records {
		if !record.ExpiresAt.After(now) {
			delete(store.records, id)
		}
	}
	return nil
}

func (store *MemoryStore) expiry() time.Time {
	return store.clock().UTC().Add(store.ttl)
}

func cloneRecord(record Record) Record {
	record.State = cloneState(record.State)
	return record
}

func cloneState(state State) State {
	data, err := json.Marshal(state)
	if err != nil {
		panic(fmt.Sprintf("clone dashboard session state: %v", err))
	}
	var result State
	if err := json.Unmarshal(data, &result); err != nil {
		panic(fmt.Sprintf("clone dashboard session state: %v", err))
	}
	return result
}

type Service struct {
	Store           Store
	ApplicationMode filter.ApplicationMode
	Bindings        map[string]filter.BindingSpec
	AnchorResolver  filter.AnchorResolver
}

type FilterCommandResult struct {
	FilterState      filter.State `json:"filterState"`
	StreamGeneration uint64       `json:"streamGeneration"`
	Duplicate        bool         `json:"duplicate,omitempty"`
}

type NavigationCommand struct {
	PageID             string `json:"pageID"`
	BaseFilterRevision uint64 `json:"baseFilterRevision"`
	ClientMutationID   string `json:"clientMutationID"`
}

type NavigationResult struct {
	ActivePage       string `json:"activePage"`
	StreamGeneration uint64 `json:"streamGeneration"`
	Duplicate        bool   `json:"duplicate,omitempty"`
}

const maxNavigationMutationIDs = 512

func (service Service) Navigate(ctx context.Context, key Key, command NavigationCommand) (NavigationResult, error) {
	if service.Store == nil {
		return NavigationResult{}, fmt.Errorf("dashboard session store is required")
	}
	if strings.TrimSpace(command.PageID) == "" || strings.TrimSpace(command.ClientMutationID) == "" {
		return NavigationResult{}, fmt.Errorf("navigation page ID and client mutation ID are required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		record, err := service.Store.Load(ctx, key)
		if err != nil {
			return NavigationResult{}, err
		}
		for _, mutationID := range record.State.NavigationMutationIDs {
			if mutationID == command.ClientMutationID {
				return NavigationResult{
					ActivePage: record.State.ActivePage, StreamGeneration: record.State.StreamGeneration, Duplicate: true,
				}, nil
			}
		}
		if command.BaseFilterRevision != record.State.Filters.State.Revision {
			return NavigationResult{
				ActivePage: record.State.ActivePage, StreamGeneration: record.State.StreamGeneration,
			}, fmt.Errorf("%w: base %d current %d", filter.ErrStaleRevision, command.BaseFilterRevision, record.State.Filters.State.Revision)
		}
		next := record.State
		next.ActivePage = command.PageID
		next.StreamGeneration++
		next.NavigationMutationIDs = append(next.NavigationMutationIDs, command.ClientMutationID)
		if len(next.NavigationMutationIDs) > maxNavigationMutationIDs {
			next.NavigationMutationIDs = next.NavigationMutationIDs[len(next.NavigationMutationIDs)-maxNavigationMutationIDs:]
		}
		saved, err := service.Store.CompareAndSwap(ctx, key, record.Version, next)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return NavigationResult{}, err
		}
		return NavigationResult{ActivePage: saved.State.ActivePage, StreamGeneration: saved.State.StreamGeneration}, nil
	}
	return NavigationResult{}, ErrConflict
}

func (service Service) UpdateSelections(
	ctx context.Context,
	key Key,
	interactionSelections []map[string]any,
	spatialSelections []map[string]any,
) (State, error) {
	if service.Store == nil {
		return State{}, fmt.Errorf("dashboard session store is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		record, err := service.Store.Load(ctx, key)
		if err != nil {
			return State{}, err
		}
		next := record.State
		next.InteractionSelections = interactionSelections
		next.SpatialSelections = spatialSelections
		next.StreamGeneration++
		saved, err := service.Store.CompareAndSwap(ctx, key, record.Version, next)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return State{}, err
		}
		return saved.State, nil
	}
	return State{}, ErrConflict
}

func (service Service) ExecuteFilterCommand(ctx context.Context, key Key, command filter.Command) (FilterCommandResult, error) {
	if service.Store == nil {
		return FilterCommandResult{}, fmt.Errorf("dashboard session store is required")
	}
	for attempt := 0; attempt < 8; attempt++ {
		record, err := service.Store.Load(ctx, key)
		if err != nil {
			return FilterCommandResult{}, err
		}
		machine, err := filter.RestoreMachine(service.ApplicationMode, service.Bindings, record.State.Filters)
		if err != nil {
			return FilterCommandResult{}, err
		}
		machine.SetAnchorResolver(service.AnchorResolver)
		result, err := machine.Execute(command)
		if err != nil {
			return FilterCommandResult{FilterState: result.State, StreamGeneration: record.State.StreamGeneration}, err
		}
		if result.Duplicate {
			return FilterCommandResult{
				FilterState: result.State, StreamGeneration: record.State.StreamGeneration, Duplicate: true,
			}, nil
		}
		next := record.State
		next.Filters = machine.Snapshot()
		next.StreamGeneration++
		saved, err := service.Store.CompareAndSwap(ctx, key, record.Version, next)
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return FilterCommandResult{}, err
		}
		return FilterCommandResult{
			FilterState: result.State, StreamGeneration: saved.State.StreamGeneration,
		}, nil
	}
	return FilterCommandResult{}, ErrConflict
}
