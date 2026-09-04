package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/platform"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
)

func testStoreOptions(store *platform.Store, options assemblyConfig) assemblyConfig {
	options.Database = store.SQLDB()
	options.PlatformHealth = store
	options.AgentSettings = store
	if options.AccessRepo == nil {
		options.AccessRepo = testAccessRepository(store)
	}
	if options.AccessModule == nil && options.Auth != nil {
		publicURL := options.PublicURL
		if publicURL == "" {
			publicURL = options.MCPOAuth.PublicURL
		}
		module, err := accessmodule.Build(context.Background(), accessmodule.Config{
			ExistingAuth: options.Auth, PublicURL: publicURL,
			MCPIssuerURL: options.MCPOAuth.IssuerURL,
		})
		if err != nil {
			panic(err)
		}
		options.AccessModule = module
	}
	if options.ServingStateRepo == nil {
		options.ServingStateRepo = servingstatesqlite.NewRepository(store.SQLDB())
	}
	if options.RuntimeHost == nil {
		environment := servingstate.NormalizeEnvironment(servingstate.Environment(options.DefaultEnvironment))
		states, ok := options.ServingStateRepo.(testServingStateRepository)
		if !ok {
			panic("test serving-state repository does not expose mutation fixture capabilities")
		}
		host, err := ensureTestRuntimeHost(context.Background(), store, states, testProjectID, environment)
		if err != nil {
			panic(err)
		}
		options.RuntimeHost = host
		options.ProjectID = testProjectID
		options.DefaultEnvironment = string(environment)
	}
	if options.RuntimeHost != nil {
		host := options.RuntimeHost
		activeIdentity := func(ctx context.Context) (projectgraph.ServingIdentity, error) {
			lease, err := host.Acquire(ctx)
			if err != nil {
				return projectgraph.ServingIdentity{}, err
			}
			defer lease.Release()
			identity := lease.Identity()
			if err := identity.Validate(); err != nil {
				return projectgraph.ServingIdentity{}, err
			}
			return identity, nil
		}
		if options.ProjectIDResolver == nil {
			options.ProjectIDResolver = func(ctx context.Context) (projectgraph.ResourceID, error) {
				identity, err := activeIdentity(ctx)
				if err != nil {
					return "", err
				}
				return identity.ProjectID, nil
			}
		}
		if options.ServingSnapshotResolver == nil {
			options.ServingSnapshotResolver = func(ctx context.Context) (string, error) {
				identity, err := activeIdentity(ctx)
				if err != nil {
					return "", err
				}
				return identity.GenerationID, nil
			}
		}
	}
	if options.ProjectGraph == nil {
		if graph, ok := options.ServingStateRepo.(interface {
			ActiveServingStateGraph(context.Context, projectgraph.ResourceID, string) (servingstate.AssetGraph, bool, error)
		}); ok {
			options.ProjectGraph = graph
		}
	}
	if options.ProjectCatalog == nil && options.AccessModule != nil && options.RuntimeHost != nil {
		catalog, err := projectcatalog.NewService(
			projectCatalogLeaseProvider{provider: options.RuntimeHost.Provider()},
			projectCatalogSubjectResolver{resolve: options.AccessModule.AuthorizationSubjects},
		)
		if err != nil {
			panic(err)
		}
		options.ProjectCatalog = catalog
	}
	if options.QueryAudit == nil && (options.AnalyticsModule == nil || options.AnalyticsModule.QueryAuditReader() == nil) {
		options.QueryAudit = analyticsmodule.BuildQueryAuditSurface(newTestQueryAuditRepository())
	}
	return options
}

// testQueryAuditRepository keeps app-package tests independent of a concrete
// persistence adapter. Production query-audit storage is PostgreSQL-owned;
// this bounded in-memory implementation exercises the same reader/recorder
// contract for SQLite-backed application fixtures.
type testQueryAuditRepository struct {
	mu     sync.RWMutex
	nextID uint64
	events []queryaudit.Event
}

var _ queryaudit.Repository = (*testQueryAuditRepository)(nil)

func newTestQueryAuditRepository() *testQueryAuditRepository {
	return &testQueryAuditRepository{}
}

func (r *testQueryAuditRepository) RecordQueryEvent(_ context.Context, input queryaudit.EventInput) error {
	if r == nil {
		return fmt.Errorf("query audit repository is unavailable")
	}
	if err := input.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(input.QueryJSON) == "" {
		input.QueryJSON = "{}"
	}
	input.SQL = queryaudit.RedactSensitiveText(input.SQL)
	input.PlanText = queryaudit.RedactSensitiveText(input.PlanText)
	input.QueryJSON = queryaudit.RedactSensitiveText(input.QueryJSON)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := strings.TrimSpace(input.EventID)
	if id == "" {
		id = fmt.Sprintf("queryevent_%08d", r.nextID)
	}
	// The app fixture only needs stable first-write identity. PostgreSQL conflict
	// semantics are covered by the native repository tests.
	for _, event := range r.events {
		if event.ID == id {
			return nil
		}
	}
	r.events = append(r.events, queryaudit.Event{
		ID: id, EventInput: input, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	return nil
}

func (r *testQueryAuditRepository) GetQueryEvent(_ context.Context, id string) (queryaudit.Event, error) {
	if r == nil {
		return queryaudit.Event{}, fmt.Errorf("query audit repository is unavailable")
	}
	id = strings.TrimSpace(id)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, event := range r.events {
		if event.ID == id {
			return event, nil
		}
	}
	return queryaudit.Event{}, fmt.Errorf("query audit event %q not found", id)
}

func (r *testQueryAuditRepository) ListQueryEvents(_ context.Context, filter queryaudit.Filter) ([]queryaudit.Event, error) {
	if r == nil {
		return nil, fmt.Errorf("query audit repository is unavailable")
	}
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	if filter.PageToken != "" && filter.CursorTime == "" && filter.CursorID == "" {
		filter.CursorTime, filter.CursorID = decodeTestQueryAuditCursor(filter.PageToken)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	r.mu.RLock()
	events := append([]queryaudit.Event(nil), r.events...)
	r.mu.RUnlock()
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].CreatedAt == events[j].CreatedAt {
			return events[i].ID > events[j].ID
		}
		return events[i].CreatedAt > events[j].CreatedAt
	})

	filtered := events[:0]
	for _, event := range events {
		if !testQueryAuditMatches(event, filter) {
			continue
		}
		filtered = append(filtered, event)
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func (r *testQueryAuditRepository) ListQueryEventFilterOptions(_ context.Context, field, search string, limit int) ([]queryaudit.FilterOption, error) {
	if r == nil {
		return nil, fmt.Errorf("query audit repository is unavailable")
	}
	field = strings.TrimSpace(field)
	if field != "project" && field != "principal" && field != "surface" && field != "kind" && field != "status" {
		return nil, fmt.Errorf("unsupported query event filter option field %q", field)
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	search = strings.ToLower(strings.TrimSpace(search))
	r.mu.RLock()
	counts := make(map[string]int)
	for _, event := range r.events {
		value := testQueryAuditOptionValue(event, field)
		if value != "" && (search == "" || strings.Contains(strings.ToLower(value), search)) {
			counts[value]++
		}
	}
	r.mu.RUnlock()
	options := make([]queryaudit.FilterOption, 0, len(counts))
	for value, count := range counts {
		options = append(options, queryaudit.FilterOption{Value: value, Count: count})
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Count == options[j].Count {
			return options[i].Value < options[j].Value
		}
		return options[i].Count > options[j].Count
	})
	if len(options) > limit {
		options = options[:limit]
	}
	return options, nil
}

func testQueryAuditMatches(event queryaudit.Event, filter queryaudit.Filter) bool {
	if filter.ProjectID != "" && event.ProjectID != filter.ProjectID {
		return false
	}
	if len(filter.ProjectIDs) > 0 && !testQueryAuditContainsProject(filter.ProjectIDs, event.ProjectID) {
		return false
	}
	if filter.PrincipalID != "" && event.PrincipalID != filter.PrincipalID {
		return false
	}
	if len(filter.PrincipalIDs) > 0 && !testQueryAuditContainsString(filter.PrincipalIDs, event.PrincipalID) {
		return false
	}
	if filter.Surface != "" && event.Surface != filter.Surface {
		return false
	}
	if len(filter.Surfaces) > 0 && !testQueryAuditContainsString(filter.Surfaces, event.Surface) {
		return false
	}
	if filter.Operation != "" && event.Operation != filter.Operation {
		return false
	}
	if filter.QueryKind != "" && event.QueryKind != filter.QueryKind {
		return false
	}
	if len(filter.QueryKinds) > 0 && !testQueryAuditContainsString(filter.QueryKinds, event.QueryKind) {
		return false
	}
	if filter.ModelID != "" && event.ModelID != filter.ModelID {
		return false
	}
	if filter.Target != "" && event.Target != filter.Target {
		return false
	}
	if filter.Status != "" && event.Status != filter.Status {
		return false
	}
	if len(filter.Statuses) > 0 && !testQueryAuditContainsString(filter.Statuses, event.Status) {
		return false
	}
	if search := strings.ToLower(strings.TrimSpace(filter.Search)); search != "" {
		text := strings.ToLower(strings.Join([]string{event.Target, event.SQL, event.QueryJSON}, "\n"))
		if !strings.Contains(text, search) {
			return false
		}
	}
	if filter.From != "" && event.CreatedAt < filter.From {
		return false
	}
	if filter.To != "" && event.CreatedAt > filter.To {
		return false
	}
	if filter.CursorTime != "" && (event.CreatedAt > filter.CursorTime || (event.CreatedAt == filter.CursorTime && event.ID >= filter.CursorID)) {
		return false
	}
	return true
}

func testQueryAuditContainsProject(values []projectgraph.ResourceID, value projectgraph.ResourceID) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func testQueryAuditContainsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func testQueryAuditOptionValue(event queryaudit.Event, field string) string {
	switch field {
	case "project":
		return event.ProjectID.String()
	case "principal":
		return event.PrincipalID
	case "surface":
		return event.Surface
	case "kind":
		return event.QueryKind
	case "status":
		return event.Status
	default:
		return ""
	}
}

func decodeTestQueryAuditCursor(token string) (string, string) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", ""
	}
	parts := strings.SplitN(string(decoded), "\x00", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func queryAuditRepositoryForTest(t *testing.T, server *appTestHarness) queryaudit.Repository {
	t.Helper()
	if server.runtime.queryAuditProvider == nil {
		t.Fatal("query audit provider is not configured")
	}
	reader, err := server.runtime.queryAuditProvider()
	if err != nil {
		t.Fatal(err)
	}
	repository, ok := reader.(queryaudit.Repository)
	if !ok || repository == nil {
		t.Fatal("query audit repository is not configured")
	}
	return repository
}
