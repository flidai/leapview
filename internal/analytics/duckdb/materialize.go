package duckdb

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type SourceRuntime struct {
	db                 analyticsresource.SessionProvider
	resolver           CredentialResolver
	connectionResolver analyticsruntime.ConnectionResolver
}

type extensionProvider interface {
	EnsureExtension(context.Context, string) error
}

type fatalReporter interface {
	MarkFatal(error)
}

type refreshTelemetry interface {
	ObserveSourceAcquisition(connector, outcome string)
	ObserveSecretScopeContention(connector string)
	ObserveRefreshCleanup(success bool)
}

func NewSourceRuntime(db analyticsresource.SessionProvider) *SourceRuntime {
	return &SourceRuntime{db: db, resolver: NonSecretCredentialResolver{}}
}

func NewSourceRuntimeWithCredentials(db analyticsresource.SessionProvider, resolver CredentialResolver) *SourceRuntime {
	if resolver == nil {
		resolver = NonSecretCredentialResolver{}
	}
	return &SourceRuntime{db: db, resolver: resolver}
}

func NewSourceRuntimeWithConnectionResolver(
	db analyticsresource.SessionProvider,
	resolver analyticsruntime.ConnectionResolver,
) *SourceRuntime {
	return &SourceRuntime{
		db: db, resolver: NonSecretCredentialResolver{}, connectionResolver: resolver,
	}
}

var sourceStageSequence atomic.Uint64
var refreshSessionSequence atomic.Uint64
var sourceScopeLocks sync.Map

type PreparedSources struct {
	model     *semanticmodel.Model
	session   analyticsresource.Session
	relations map[string]string
	tables    []string
	once      sync.Once
	closeErr  error
	reporter  fatalReporter
	telemetry refreshTelemetry
}

func (r *SourceRuntime) Prepare(ctx context.Context, model *semanticmodel.Model) (analyticsmaterialize.PreparedSources, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("source preparer is not initialized")
	}
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	session, err := r.db.Session(ctx)
	if err != nil {
		return nil, err
	}
	resolved, err := r.resolveCredentials(ctx, model)
	if err != nil {
		return nil, err
	}
	telemetry, _ := r.db.(refreshTelemetry)
	if extensions, ok := r.db.(extensionProvider); ok {
		for _, extension := range RequiredExtensions(resolved) {
			if err := extensions.EnsureExtension(ctx, extension); err != nil {
				return nil, err
			}
		}
	}
	releaseScopes := lockSourceScopes(resolved, telemetry)
	defer releaseScopes()
	prepared := &PreparedSources{model: resolved, session: session, relations: map[string]string{}, telemetry: telemetry}
	prepared.reporter, _ = r.db.(fatalReporter)
	for _, sourceName := range sortedKeys(resolved.Sources) {
		source := resolved.Sources[sourceName]
		connection := resolved.Connections[source.Connection]
		if connection.Kind == "managed" {
			relation, err := SourceRelation(resolved, source)
			if err != nil {
				_ = prepared.Close()
				return nil, safeSourceError(sourceName, err)
			}
			columns, err := describeRelationSchema(ctx, session, "("+relation+")")
			if err != nil {
				_ = prepared.Close()
				return nil, safeSourceError(sourceName, err)
			}
			source.Schema = semanticmodel.TableSchema{Columns: columns}
			resolved.Sources[sourceName] = source
			original := model.Sources[sourceName]
			original.Schema = source.Schema
			model.Sources[sourceName] = original
			continue
		}
		sourceModel := refreshSourceModel(resolved, sourceName, source)
		attached := map[string]struct{}{}
		if err := prepareRefreshSourceAccess(ctx, session, sourceModel, attached); err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			cleanupErr := cleanupSourceAccess(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, fmt.Errorf("preparing refresh source %q failed", sourceName)
		}
		relation, err := SourceRelation(sourceModel, source)
		if err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			_ = prepared.Close()
			cleanupErr := cleanupSourceAccess(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, safeSourceError(sourceName, err)
		}
		table := fmt.Sprintf("leapview_stage_%d_%s", sourceStageSequence.Add(1), sourceName)
		if err := validateIdentifier(table); err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			_ = prepared.Close()
			cleanupErr := cleanupSourceAccess(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, err
		}
		if _, err := session.ExecContext(ctx, "CREATE TEMP TABLE "+quoteIdentifier(table)+" AS SELECT * FROM ("+relation+")"); err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			_ = prepared.Close()
			cleanupErr := cleanupSourceAccess(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, safeSourceError(sourceName, err)
		}
		prepared.tables = append(prepared.tables, table)
		prepared.relations[sourceName] = quoteIdentifier(table)
		columns, err := describeRelationSchema(ctx, session, quoteIdentifier(table))
		if err != nil {
			observeSource(telemetry, connection.Kind, "failed")
			_ = prepared.Close()
			cleanupErr := cleanupSourceAccess(session, sourceModel, attached)
			reportCleanup(r.db, telemetry, cleanupErr)
			return nil, safeSourceError(sourceName, err)
		}
		source.Schema = semanticmodel.TableSchema{Columns: columns}
		resolved.Sources[sourceName] = source
		original := model.Sources[sourceName]
		original.Schema = source.Schema
		model.Sources[sourceName] = original
		if err := cleanupSourceAccess(session, sourceModel, attached); err != nil {
			reportCleanup(r.db, telemetry, err)
			_ = prepared.Close()
			return nil, fmt.Errorf("cleaning refresh source %q access failed", sourceName)
		}
		reportCleanup(r.db, telemetry, nil)
		observeSource(telemetry, connection.Kind, "succeeded")
	}
	if err := resolved.ValidateDiscoveredSourceSchemas(); err != nil {
		_ = prepared.Close()
		return nil, fmt.Errorf("validating staged source schemas: %w", err)
	}
	return prepared, nil
}

func refreshSourceModel(model *semanticmodel.Model, sourceName string, source semanticmodel.Source) *semanticmodel.Model {
	connection := model.Connections[source.Connection]
	return &semanticmodel.Model{
		Name: model.Name, DefaultConnection: source.Connection,
		Connections: map[string]semanticmodel.Connection{source.Connection: connection},
		Sources:     map[string]semanticmodel.Source{sourceName: source},
	}
}

func lockSourceScopes(model *semanticmodel.Model, telemetry refreshTelemetry) func() {
	keys := map[string]struct{}{}
	for _, source := range model.Sources {
		connection := model.Connections[source.Connection]
		if connection.Kind == "managed" {
			continue
		}
		scope := firstNonEmpty(connection.Scope, connection.Path, connection.Host, source.Connection)
		keys[connection.Kind+"\x00"+scope] = struct{}{}
	}
	ordered := sortedKeys(keys)
	locks := make([]*sync.Mutex, 0, len(ordered))
	for _, key := range ordered {
		value, _ := sourceScopeLocks.LoadOrStore(key, &sync.Mutex{})
		lock := value.(*sync.Mutex)
		if !lock.TryLock() {
			connector, _, _ := strings.Cut(key, "\x00")
			if telemetry != nil {
				telemetry.ObserveSecretScopeContention(connector)
			}
			lock.Lock()
		}
		locks = append(locks, lock)
	}
	return func() {
		for index := len(locks) - 1; index >= 0; index-- {
			locks[index].Unlock()
		}
	}
}

func observeSource(telemetry refreshTelemetry, connector, outcome string) {
	if telemetry != nil {
		telemetry.ObserveSourceAcquisition(connector, outcome)
	}
}

func observeCleanup(telemetry refreshTelemetry, err error) {
	if telemetry != nil {
		telemetry.ObserveRefreshCleanup(err == nil)
	}
}

func reportCleanup(provider analyticsresource.SessionProvider, telemetry refreshTelemetry, err error) {
	observeCleanup(telemetry, err)
	if err != nil {
		if reporter, ok := provider.(fatalReporter); ok {
			reporter.MarkFatal(err)
		}
	}
}

func (r *SourceRuntime) PlanModelTable(ctx context.Context, model *semanticmodel.Model, tableName string, table semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	session, err := r.db.Session(ctx)
	if err != nil {
		return analyticsmaterialize.ModelTablePlan{}, err
	}
	return PlanModelTable(ctx, session, model, tableName, table)
}

func (r *SourceRuntime) ResolveSourcePath(model *semanticmodel.Model, source semanticmodel.Source) (string, error) {
	return ResolveSourcePath(model, source)
}

func (r *SourceRuntime) resolveCredentials(ctx context.Context, model *semanticmodel.Model) (*semanticmodel.Model, error) {
	resolved := *model
	suffix := fmt.Sprintf("_r%d", refreshSessionSequence.Add(1))
	resolved.Connections = make(map[string]semanticmodel.Connection, len(model.Connections))
	connectionNames := make(map[string]string, len(model.Connections))
	for name, connection := range model.Connections {
		if connection.Kind == "managed" {
			// Managed-data roots are already resolved from immutable
			// serving-state bindings by Runtime Host. They must never be
			// replaced by a secret-backed target connection.
		} else if r.connectionResolver != nil {
			var err error
			connection, err = r.connectionResolver.Resolve(ctx, name, connection)
			if err != nil {
				return nil, err
			}
		} else {
			auth, err := r.resolver.Resolve(ctx, name, connection)
			if err != nil {
				return nil, err
			}
			connection.Auth = auth
		}
		resolvedName := name + suffix
		connectionNames[name] = resolvedName
		resolved.Connections[resolvedName] = connection
	}
	if remapped := connectionNames[model.DefaultConnection]; remapped != "" {
		resolved.DefaultConnection = remapped
	}
	resolved.Sources = make(map[string]semanticmodel.Source, len(model.Sources))
	for name, source := range model.Sources {
		if remapped := connectionNames[source.Connection]; remapped != "" {
			source.Connection = remapped
		}
		resolved.Sources[name] = source
	}
	return &resolved, nil
}

func (p *PreparedSources) PlanModelTable(ctx context.Context, _ *semanticmodel.Model, tableName string, table semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	return planModelTable(ctx, p.session, p.model, tableName, table, p.relations)
}

func (p *PreparedSources) Close() error {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for index := len(p.tables) - 1; index >= 0; index-- {
			if _, err := p.session.ExecContext(cleanupCtx, "DROP TABLE IF EXISTS "+quoteIdentifier(p.tables[index])); err != nil {
				p.closeErr = errors.Join(p.closeErr, err)
			}
		}
		if p.closeErr != nil && p.reporter != nil {
			p.reporter.MarkFatal(p.closeErr)
		}
		observeCleanup(p.telemetry, p.closeErr)
	})
	return p.closeErr
}

func cleanupSourceAccess(session analyticsresource.Session, model *semanticmodel.Model, attached map[string]struct{}) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var result error
	connections := make([]string, 0, len(attached))
	for name := range attached {
		connections = append(connections, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(connections)))
	for _, name := range connections {
		alias, err := databaseAlias(name)
		if err == nil {
			_, err = session.ExecContext(cleanupCtx, "DETACH "+alias)
		}
		result = errors.Join(result, err)
	}
	secrets := map[string]struct{}{}
	for name, connection := range model.Connections {
		spec, ok := connectors.LookupConnection(connection.Kind)
		if ok && spec.SecretType != "" && spec.AttachKind != connectors.AttachDatabase {
			if secret, err := connectionSecretName(name); err == nil {
				secrets[secret] = struct{}{}
			}
		}
	}
	for _, source := range model.Sources {
		format, ok := connectors.LookupFormat(source.Format)
		if !ok || format.SourceSecretType == "" {
			continue
		}
		if secret, err := connectionSecretName(source.Connection + "_" + format.SourceSecretType); err == nil {
			secrets[secret] = struct{}{}
		}
	}
	for _, secret := range sortedKeys(secrets) {
		_, err := session.ExecContext(cleanupCtx, "DROP SECRET IF EXISTS "+secret)
		result = errors.Join(result, err)
	}
	return result
}

func safeSourceError(source string, _ error) error {
	return fmt.Errorf("acquiring source %q failed", source)
}

type ProjectRuntimeConfig struct {
	Models                   map[string]*semanticmodel.Model
	Database                 analyticsruntime.ProjectDatabase
	CredentialResolver       CredentialResolver
	ConnectionResolver       analyticsruntime.ConnectionResolver
	SnapshotID               int64
	ServingStateID           string
	ProjectID                projectgraph.ResourceID
	Environment              string
	TargetType               string
	TargetID                 string
	SemanticDigest           string
	ArtifactDigest           string
	SourceDataDigest         string
	CandidateID              string
	AuthorizationFingerprint string
	BindingFingerprint       string
	RequiredExtensions       []string
	SkipInitialRefresh       bool
	QueryCache               *resultcache.Scope
	ResultLimits             dataquery.ResultLimits
}

type ProjectRuntime struct {
	mu                   sync.Mutex
	projectID            projectgraph.ResourceID
	db                   analyticsmaterialize.Database
	sessions             analyticsresource.SessionProvider
	committer            duckLakeCommitter
	sources              *SourceRuntime
	models               map[string]*semanticmodel.Model
	materializationModel *semanticmodel.Model
	views                map[string]*analyticsmaterialize.Runtime
	lastRefresh          time.Time
	lastSnapshotID       int64
	commitMetadata       map[string]string
	cacheScope           *resultcache.Scope
}

type duckLakeCommitter interface {
	CommitTransaction(ctx context.Context, servingStateID string, extra map[string]string, fn func(transaction.Transaction) error) (int64, error)
}

func OpenProjectMaterializeRuntime(ctx context.Context, config ProjectRuntimeConfig) (*ProjectRuntime, error) {
	if len(config.Models) == 0 {
		return nil, fmt.Errorf("project semantic models are required")
	}
	if config.ProjectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	if err := config.ProjectID.Validate(); err != nil {
		return nil, fmt.Errorf("project id: %w", err)
	}
	db := config.Database
	if db == nil {
		return nil, fmt.Errorf("process DuckDB environment is required")
	}
	if config.SnapshotID > 0 {
		if err := db.ValidateSnapshot(ctx, config.SnapshotID); err != nil {
			return nil, err
		}
	}
	sources := NewSourceRuntimeWithCredentials(db, config.CredentialResolver)
	if config.ConnectionResolver != nil {
		sources = NewSourceRuntimeWithConnectionResolver(db, config.ConnectionResolver)
	}
	materializationModel, err := physicalProjectModel(config.Models)
	if err != nil {
		return nil, err
	}
	for modelID, model := range config.Models {
		if err := analyticsmaterialize.ValidateFilesWithResolver(model, sources); err != nil {
			return nil, fmt.Errorf("semantic model %q: %w", modelID, err)
		}
	}
	runtime := &ProjectRuntime{
		projectID:            config.ProjectID,
		db:                   db,
		sessions:             db,
		committer:            db,
		sources:              sources,
		models:               config.Models,
		materializationModel: materializationModel,
		views:                map[string]*analyticsmaterialize.Runtime{},
		commitMetadata:       projectCommitMetadata(config),
		cacheScope:           config.QueryCache,
	}
	for modelID, model := range config.Models {
		var tableRelation semanticquery.TableRelation
		if config.SnapshotID > 0 {
			tableRelation = func(table string) (string, error) {
				return analyticsducklake.QualifiedSnapshotRelation(config.SnapshotID, table)
			}
		}
		view, err := analyticsmaterialize.NewRuntimeView(ctx, analyticsmaterialize.RuntimeConfig{
			ModelID:             modelID,
			Model:               model,
			QueryCacheNamespace: projectQueryCacheNamespace(config),
			Database:            db,
			Sources:             sources,
			Resolver:            sources,
			TableRelation:       tableRelation,
			QueryCache:          config.QueryCache,
			ResultLimits:        config.ResultLimits,
			RequiredExtensions:  config.RequiredExtensions,
		})
		if err != nil {
			return nil, fmt.Errorf("compile semantic model %q runtime: %w", modelID, err)
		}
		runtime.views[modelID] = view
	}
	if config.SnapshotID > 0 {
		runtime.lastSnapshotID = config.SnapshotID
	} else if !config.SkipInitialRefresh {
		if err := runtime.Refresh(ctx); err != nil {
			return nil, err
		}
	}
	return runtime, nil
}

func projectQueryCacheNamespace(config ProjectRuntimeConfig) string {
	return fmt.Sprintf(
		"snapshot=%d;serving=%q;project=%q;environment=%q;semantic=%q;artifact=%q;source=%q;candidate=%q;authorization=%q;bindings=%q",
		config.SnapshotID,
		config.ServingStateID,
		config.ProjectID.String(),
		config.Environment,
		config.SemanticDigest,
		config.ArtifactDigest,
		config.SourceDataDigest,
		config.CandidateID,
		config.AuthorizationFingerprint,
		config.BindingFingerprint,
	)
}

func (r *ProjectRuntime) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if err := r.validateProject(request); err != nil {
		return dataquery.Result{}, err
	}
	if r == nil || r.db == nil {
		return dataquery.Result{}, fmt.Errorf("project runtime is not initialized")
	}
	modelID := strings.TrimSpace(request.ModelID)
	if modelID == "" && len(r.models) == 1 {
		for id := range r.models {
			modelID = id
		}
	}
	_, ok := r.models[modelID]
	if !ok {
		return dataquery.Result{}, fmt.Errorf("unknown semantic model %q", modelID)
	}
	request.ModelID = modelID
	view := r.views[modelID]
	if view == nil {
		return dataquery.Result{}, fmt.Errorf("semantic model %q runtime is not compiled", modelID)
	}
	return view.ExecuteDataQuery(ctx, request)
}

func (r *ProjectRuntime) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if err := r.validateProject(request); err != nil {
		return dataquery.Result{}, err
	}
	if r == nil || r.db == nil {
		return dataquery.Result{}, fmt.Errorf("project runtime is not initialized")
	}
	modelID := strings.TrimSpace(request.ModelID)
	if modelID == "" && len(r.models) == 1 {
		for id := range r.models {
			modelID = id
		}
	}
	if _, ok := r.models[modelID]; !ok {
		return dataquery.Result{}, fmt.Errorf("unknown semantic model %q", modelID)
	}
	request.ModelID = modelID
	view := r.views[modelID]
	if view == nil {
		return dataquery.Result{}, fmt.Errorf("semantic model %q runtime is not compiled", modelID)
	}
	return view.ExecuteDataQueryArrow(ctx, request, sink)
}

func (r *ProjectRuntime) ExecuteDataQueryBundle(ctx context.Context, requests []dataquery.BundleRequest) (dataquery.BundleResult, error) {
	if r == nil {
		return dataquery.BundleResult{}, fmt.Errorf("project runtime is not initialized")
	}
	if len(requests) == 0 {
		return dataquery.BundleResult{}, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("bundle is empty")}
	}
	for _, request := range requests {
		if err := r.validateProject(request.Query); err != nil {
			return dataquery.BundleResult{}, err
		}
	}
	modelID := strings.TrimSpace(requests[0].Query.ModelID)
	if modelID == "" && len(r.models) == 1 {
		for id := range r.models {
			modelID = id
		}
	}
	view := r.views[modelID]
	if view == nil {
		return dataquery.BundleResult{}, fmt.Errorf("semantic model %q runtime is not compiled", modelID)
	}
	for i := range requests {
		if requests[i].Query.ModelID == "" {
			requests[i].Query.ModelID = modelID
		}
		if requests[i].Query.ModelID != modelID {
			return dataquery.BundleResult{}, &dataquery.BundleIncompatibleError{Err: fmt.Errorf("bundle spans semantic models")}
		}
	}
	return view.ExecuteDataQueryBundle(ctx, requests)
}

func (r *ProjectRuntime) validateProject(request dataquery.Query) error {
	if r == nil {
		return fmt.Errorf("project runtime is not initialized")
	}
	if request.ProjectID == "" {
		return fmt.Errorf("project id is required")
	}
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("project id: %w", err)
	}
	if request.ProjectID != r.projectID {
		return fmt.Errorf("project id %q does not match runtime project %q", request.ProjectID, r.projectID)
	}
	return nil
}

func (r *ProjectRuntime) Refresh(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("project runtime is not initialized")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()

	lastRefresh, snapshotID, err := r.refreshModel(ctx, r.materializationModel, nil)
	if err != nil {
		return err
	}
	r.clearQueryCaches()
	if err := r.discoverServingSchemas(ctx, r.materializationModel); err != nil {
		return err
	}
	r.lastRefresh = lastRefresh
	r.lastSnapshotID = snapshotID
	return nil
}

func (r *ProjectRuntime) RefreshModelTables(ctx context.Context, modelID string, tableNames []string) error {
	if r == nil {
		return fmt.Errorf("project runtime is not initialized")
	}
	model, ok := r.models[modelID]
	if !ok {
		return fmt.Errorf("unknown semantic model %q", modelID)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()

	lastRefresh, snapshotID, err := r.refreshModel(ctx, model, tableNames)
	if err != nil {
		return err
	}
	r.clearQueryCaches()
	if err := r.discoverServingSchemas(ctx, model); err != nil {
		return err
	}
	r.lastRefresh = lastRefresh
	r.lastSnapshotID = snapshotID
	return nil
}

func (r *ProjectRuntime) RefreshProjectTables(ctx context.Context, tableNames []string) error {
	if r == nil {
		return fmt.Errorf("project runtime is not initialized")
	}
	if len(tableNames) == 0 {
		return fmt.Errorf("model table refresh plan is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()

	lastRefresh, snapshotID, err := r.refreshModel(ctx, r.materializationModel, tableNames)
	if err != nil {
		return err
	}
	r.clearQueryCaches()
	if err := r.discoverServingSchemas(ctx, r.materializationModel); err != nil {
		return err
	}
	r.lastRefresh = lastRefresh
	r.lastSnapshotID = snapshotID
	return nil
}

func (r *ProjectRuntime) clearQueryCaches() {
	for _, view := range r.views {
		view.ClearQueryCache()
	}
}

func (r *ProjectRuntime) discoverServingSchemas(ctx context.Context, refreshed *semanticmodel.Model) error {
	applyDiscoveredSourceSchemas(refreshed, r.models)
	for modelID, model := range r.models {
		if err := discoverSchemas(ctx, r.sessions, model); err != nil {
			return fmt.Errorf("discovering semantic model %q schemas: %w", modelID, err)
		}
	}
	return nil
}

// applyDiscoveredSourceSchemas carries refresh-scoped source metadata into the
// authored semantic models before serving schema discovery. This is the
// boundary that prevents the post-commit pass from reopening an external
// source after its temporary credentials and attachments have been removed.
func applyDiscoveredSourceSchemas(refreshed *semanticmodel.Model, models map[string]*semanticmodel.Model) {
	if refreshed == nil {
		return
	}
	for sourceName, discovered := range refreshed.Sources {
		if len(discovered.Schema.Columns) == 0 {
			continue
		}
		for _, model := range models {
			if model == nil {
				continue
			}
			source, ok := model.Sources[sourceName]
			if !ok {
				continue
			}
			source.Schema = cloneTableSchema(discovered.Schema)
			model.Sources[sourceName] = source
		}
	}
}

func cloneTableSchema(schema semanticmodel.TableSchema) semanticmodel.TableSchema {
	clone := semanticmodel.TableSchema{Columns: append([]semanticmodel.ColumnSchema(nil), schema.Columns...)}
	for index := range clone.Columns {
		if clone.Columns[index].Nullable == nil {
			continue
		}
		nullable := *clone.Columns[index].Nullable
		clone.Columns[index].Nullable = &nullable
	}
	return clone
}

func ProjectModelTableDependencyOrder(models map[string]*semanticmodel.Model, selectedTable string) ([]string, error) {
	model, err := physicalProjectModel(models)
	if err != nil {
		return nil, err
	}
	return analyticsmaterialize.ModelTableDependencyOrder(model, selectedTable)
}

func (r *ProjectRuntime) refreshModel(ctx context.Context, model *semanticmodel.Model, tableNames []string) (time.Time, int64, error) {
	prepared, err := r.sources.Prepare(ctx, model)
	if err != nil {
		return time.Time{}, 0, err
	}
	if r.committer == nil {
		if len(tableNames) > 0 {
			lastRefresh, err := analyticsmaterialize.RefreshModelTables(ctx, r.db, prepared, model, tableNames)
			return lastRefresh, 0, errors.Join(err, prepared.Close())
		}
		lastRefresh, err := analyticsmaterialize.Refresh(ctx, r.db, prepared, model)
		return lastRefresh, 0, errors.Join(err, prepared.Close())
	}
	metadata := map[string]string{}
	for key, value := range r.commitMetadata {
		metadata[key] = value
	}
	servingStateID := firstNonEmpty(r.commitMetadata["servingStateId"], "project-refresh")
	snapshotID, err := r.committer.CommitTransaction(ctx, servingStateID, metadata, func(tx transaction.Transaction) error {
		executor := txExecutor{tx: tx}
		sources := txPreparedSources{PreparedSources: prepared.(*PreparedSources), tx: tx}
		if len(tableNames) > 0 {
			return analyticsmaterialize.ModelTablesNamed(ctx, executor, sources, model, tableNames)
		}
		return analyticsmaterialize.ModelTables(ctx, executor, sources, model)
	})
	if err != nil {
		_ = prepared.Close()
		return time.Time{}, 0, err
	}
	if err := prepared.Close(); err != nil {
		return time.Time{}, 0, fmt.Errorf("cleaning refresh staging: %w", err)
	}
	return time.Now(), snapshotID, nil
}

func (r *ProjectRuntime) acquireOperation(ctx context.Context) (context.Context, func(), error) {
	provider, ok := r.db.(analyticsresource.Provider)
	if !ok {
		return ctx, func() {}, nil
	}
	lease, err := provider.Acquire(ctx)
	if err != nil {
		return ctx, func() {}, err
	}
	return lease.Context(), lease.Release, nil
}

func projectCommitMetadata(config ProjectRuntimeConfig) map[string]string {
	metadata := map[string]string{}
	addCommitMetadata(metadata, "servingStateId", config.ServingStateID)
	if config.ProjectID != "" {
		addCommitMetadata(metadata, "projectId", config.ProjectID.String())
	}
	addCommitMetadata(metadata, "environment", config.Environment)
	addCommitMetadata(metadata, "targetType", config.TargetType)
	addCommitMetadata(metadata, "targetId", config.TargetID)
	addCommitMetadata(metadata, "semanticModelDigest", config.SemanticDigest)
	addCommitMetadata(metadata, "artifactDigest", config.ArtifactDigest)
	addCommitMetadata(metadata, "sourceDataDigest", config.SourceDataDigest)
	return metadata
}

func addCommitMetadata(metadata map[string]string, key, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		metadata[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (r *ProjectRuntime) Close() error {
	if r == nil {
		return nil
	}
	var errs []error
	for _, view := range r.views {
		if err := view.CloseView(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.cacheScope != nil {
		if err := r.cacheScope.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *ProjectRuntime) LastRefresh() time.Time {
	if r == nil {
		return time.Time{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastRefresh
}

func (r *ProjectRuntime) DBPath() string {
	if r == nil || r.db == nil {
		return ""
	}
	return r.db.Path()
}

func (r *ProjectRuntime) DuckLakeSnapshotID() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastSnapshotID
}

func (r *ProjectRuntime) ReadConcurrency() int {
	if r == nil {
		return 1
	}
	r.mu.Lock()
	snapshotID := r.lastSnapshotID
	r.mu.Unlock()
	if snapshotID <= 0 {
		return 1
	}
	if concurrency, ok := r.db.(interface{ ReadConcurrency() int }); ok {
		return max(1, concurrency.ReadConcurrency())
	}
	return 1
}

type txExecutor struct {
	tx transaction.Transaction
}

func (e txExecutor) Exec(ctx context.Context, statement string) error {
	_, err := e.tx.ExecContext(ctx, statement)
	return err
}

type txPreparedSources struct {
	*PreparedSources
	tx transaction.Transaction
}

func (r txPreparedSources) PlanModelTable(ctx context.Context, _ *semanticmodel.Model, tableName string, table semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	return planModelTable(ctx, r.tx, r.model, tableName, table, r.relations)
}

func physicalProjectModel(models map[string]*semanticmodel.Model) (*semanticmodel.Model, error) {
	projectModel := &semanticmodel.Model{
		Name:              "project",
		DefaultConnection: "",
		Connections:       map[string]semanticmodel.Connection{},
		Sources:           map[string]semanticmodel.Source{},
		Tables:            map[string]semanticmodel.Table{},
		Measures:          map[string]semanticmodel.MetricMeasure{},
	}
	for modelID, model := range models {
		if model == nil {
			return nil, fmt.Errorf("semantic model %q is required", modelID)
		}
		if projectModel.DefaultConnection == "" {
			projectModel.DefaultConnection = model.DefaultConnection
		}
		for name, connection := range model.Connections {
			existing, ok := projectModel.Connections[name]
			if ok && !reflect.DeepEqual(existing, connection) {
				return nil, fmt.Errorf("semantic model %q connection %q conflicts with another project model", modelID, name)
			}
			projectModel.Connections[name] = connection
		}
		for name, source := range model.Sources {
			existing, ok := projectModel.Sources[name]
			if ok && !reflect.DeepEqual(sourcePhysicalSignature(existing), sourcePhysicalSignature(source)) {
				return nil, fmt.Errorf("semantic model %q source %q conflicts with another project model", modelID, name)
			}
			projectModel.Sources[name] = source
		}
		for name, table := range model.Tables {
			existing, ok := projectModel.Tables[name]
			if ok && !reflect.DeepEqual(tablePhysicalSignature(existing), tablePhysicalSignature(table)) {
				return nil, fmt.Errorf("semantic model %q model table %q conflicts with another project model", modelID, name)
			}
			projectModel.Tables[name] = table
		}
	}
	return projectModel, nil
}

func sourcePhysicalSignature(source semanticmodel.Source) semanticmodel.Source {
	source.Description = ""
	source.Fields = nil
	source.Schema = semanticmodel.TableSchema{}
	return source
}

type tablePhysicalSignatureValue struct {
	Source             string
	Sources            []string
	SQL                string
	Transform          semanticmodel.Transform
	Columns            map[string]semanticmodel.ModelColumn
	PrimaryKey         string
	Grain              string
	SourceDependencies []string
	ModelDependencies  []string
}

func tablePhysicalSignature(table semanticmodel.Table) tablePhysicalSignatureValue {
	return tablePhysicalSignatureValue{
		Source:             table.Source,
		Sources:            append([]string{}, table.Sources...),
		SQL:                table.SQL,
		Transform:          table.Transform,
		Columns:            table.Columns,
		PrimaryKey:         table.PrimaryKey,
		Grain:              table.Grain,
		SourceDependencies: append([]string{}, table.SourceDependencies...),
		ModelDependencies:  append([]string{}, table.ModelDependencies...),
	}
}
