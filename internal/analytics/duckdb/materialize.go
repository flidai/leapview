package duckdb

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
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
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	extensiondomain "github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type SourceRuntime struct {
	db                 analyticsresource.SessionProvider
	resolver           CredentialResolver
	connectionResolver analyticsruntime.ConnectionResolver
	extensionAdmission ExtensionAdmission
	sourceWork         *sourcework.Gate
}

// AdmittedExtension is immutable evidence for one exact, already verified
// extension artifact. The path is target-owned and never enters authored
// resources or fingerprints.
type AdmittedExtension = extensiondomain.AdmittedExtension

// ExtensionAdmission is supplied by the deployment/runtime host after it has
// verified the pinned extension artifact. Source preparation never acquires or
// installs extensions; it loads only the exact path returned here.
type ExtensionAdmission = extensiondomain.Admission

type fatalReporter interface {
	MarkFatal(error)
}

var errSourceCleanupFailed = analyticsruntime.ErrConnectionCleanupFailed

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
	model           *semanticmodel.Model
	session         analyticsresource.Session
	relations       map[string]stagedRelation
	relationQueries map[string]string
	tables          []string
	once            sync.Once
	closeErr        error
	reporter        fatalReporter
	telemetry       refreshTelemetry
}

func (r *SourceRuntime) Prepare(ctx context.Context, model *semanticmodel.Model) (result analyticsmaterialize.PreparedSources, resultErr error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("source preparer is not initialized")
	}
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	var sourceWorkLease *sourcework.Lease
	if r.sourceWork != nil {
		var err error
		sourceWorkLease, err = r.sourceWork.Acquire(ctx)
		if err != nil {
			return nil, err
		}
	}
	var session analyticsresource.Session
	closeSession := false
	quarantineLease := false
	cleanupUncertain := false
	var prepared *PreparedSources
	var activeSourceModel *semanticmodel.Model
	var activeAttached map[string]struct{}
	activeSourceAccess := false
	var telemetry refreshTelemetry
	operationPanicked := false
	defer func() {
		panicking := recover() != nil || operationPanicked
		if panicking {
			quarantineLease = true
			markSourceCleanupFatal(r.db)
			result = nil
			resultErr = errSourceCleanupFailed
		}

		// A panic can interrupt preparation after a secret or attachment has
		// been created. Make one best-effort synchronous cleanup attempt before
		// closing the session; any uncertainty quarantines the admission lease.
		if activeSourceAccess {
			activeSourceAccess = false
			cleanupErr, cleanupPanic := tryCleanupSourceAccess(session, activeSourceModel, activeAttached)
			if cleanupPanic {
				cleanupUncertain = true
				reportCleanup(r.db, telemetry, errSourceCleanupFailed)
				panicking = true
				result = nil
				resultErr = errSourceCleanupFailed
			} else {
				reportCleanup(r.db, telemetry, cleanupErr)
				if cleanupErr != nil {
					cleanupUncertain = true
					if !panicking {
						result = nil
						resultErr = joinSourceCleanupFailure(resultErr)
					}
				}
			}
		}

		if resultErr != nil && prepared != nil {
			closeErr, closePanic := tryCloseSourceSession(prepared)
			if closePanic {
				cleanupUncertain = true
				quarantineLease = true
				reportCleanup(r.db, telemetry, errSourceCleanupFailed)
				result = nil
				resultErr = errSourceCleanupFailed
			} else if closeErr != nil {
				cleanupUncertain = true
				result = nil
				resultErr = joinSourceCleanupFailure(resultErr)
			}
		}
		if closeSession && session != nil {
			if closer, ok := session.(interface{ Close() error }); ok {
				closeErr, closePanic := tryCloseSourceSession(closer)
				if closePanic {
					quarantineLease = true
					markSourceCleanupFatal(r.db)
					panicking = true
					result = nil
					resultErr = errSourceCleanupFailed
				} else if closeErr != nil {
					quarantineLease = true
					markSourceCleanupFatal(r.db)
					if !panicking {
						result = nil
						resultErr = joinSourceCleanupFailure(resultErr)
					}
				}
			}
		}
		if cleanupUncertain || panicking {
			quarantineLease = true
		}
		if sourceWorkLease != nil && !quarantineLease {
			sourceWorkLease.Release()
		}
	}()

	if err := sourcework.Revalidate(ctx); err != nil {
		return nil, fmt.Errorf("revalidating source authority before session acquisition: %w", err)
	}
	session, err := r.db.Session(ctx)
	closeSession = session != nil
	if err != nil {
		return nil, err
	}
	telemetry, _ = r.db.(refreshTelemetry)
	resolved := sourcePreparationModel(model)
	prepared = &PreparedSources{model: resolved, session: session, relations: map[string]stagedRelation{}, relationQueries: map[string]string{}, telemetry: telemetry}
	prepared.reporter, _ = r.db.(fatalReporter)
	finishSourceAccess := func() error {
		if !activeSourceAccess {
			return nil
		}
		// Mark the attempt before entering DuckDB so a panic cannot cause a
		// second cleanup attempt from the deferred finalizer.
		activeSourceAccess = false
		cleanupErr, cleanupPanic := tryCleanupSourceAccess(session, activeSourceModel, activeAttached)
		activeSourceModel = nil
		activeAttached = nil
		if cleanupPanic {
			cleanupUncertain = true
			operationPanicked = true
			reportCleanup(r.db, telemetry, errSourceCleanupFailed)
			return errSourceCleanupFailed
		}
		reportCleanup(r.db, telemetry, cleanupErr)
		if cleanupErr != nil {
			cleanupUncertain = true
			return errSourceCleanupFailed
		}
		return nil
	}
	loadedExtensions := map[string]struct{}{}
	for _, sourceName := range sortedKeys(resolved.Sources) {
		source := resolved.Sources[sourceName]
		connection, ok := resolved.Connections[source.Connection]
		if !ok {
			return nil, fmt.Errorf("source %q references an unknown connection", sourceName)
		}
		if connection.Kind == "managed" {
			sourceModel := refreshSourceModel(resolved, sourceName, source, source.Connection, connection)
			if err := r.loadSourceExtensions(ctx, session, sourceModel, loadedExtensions); err != nil {
				return nil, err
			}
			relation, err := SourceRelation(sourceModel, sourceModel.Sources[sourceName])
			if err != nil {
				return nil, safeSourceError(sourceName, err)
			}
			columns, err := describeRelationSchema(ctx, session, "("+relation+")")
			if err != nil {
				return nil, safeSourceError(sourceName, err)
			}
			source.Schema = semanticmodel.TableSchema{Columns: columns}
			resolved.Sources[sourceName] = source
			// Keep the resolved relation on the live prepared session so the
			// freshness observation seam can query it before Close releases the
			// target-owned connection.
			prepared.relations[sourceName] = stagedRelation{value: relation, kind: stagedRelationQuery}
			prepared.relationQueries[sourceName] = "(" + relation + ")"
			original := model.Sources[sourceName]
			original.Schema = source.Schema
			model.Sources[sourceName] = original
			continue
		}
		if err := sourcework.Revalidate(ctx); err != nil {
			return nil, fmt.Errorf("revalidating source authority before source %q connection resolution: %w", sourceName, err)
		}
		sourceKind := connection.Kind
		if err := r.withResolvedConnection(ctx, source.Connection, connection, func(resolvedConnection semanticmodel.Connection) (sourceErr error) {
			sourceKind = resolvedConnection.Kind
			defer func() {
				if panicked := recover(); panicked != nil {
					operationPanicked = true
					_ = finishSourceAccess()
					panic(panicked)
				}
			}()
			resolvedConnectionName := fmt.Sprintf("%s_r%d", source.Connection, refreshSessionSequence.Add(1))
			sourceModel := refreshSourceModel(resolved, sourceName, source, resolvedConnectionName, resolvedConnection)
			if err := r.loadSourceExtensions(ctx, session, sourceModel, loadedExtensions); err != nil {
				return err
			}

			releaseSourceScope := lockSourceScope(resolvedConnection, source.Connection, telemetry)
			defer releaseSourceScope()
			if err := sourcework.Revalidate(ctx); err != nil {
				return fmt.Errorf("revalidating source authority before source %q access: %w", sourceName, err)
			}

			attached := map[string]struct{}{}
			activeSourceModel = sourceModel
			activeAttached = attached
			activeSourceAccess = true
			defer func() {
				if cleanupErr := finishSourceAccess(); cleanupErr != nil {
					sourceErr = joinSourceCleanupFailureIfNeeded(sourceErr, cleanupErr)
				}
			}()

			if err := prepareRefreshSourceAccess(ctx, session, sourceModel, attached); err != nil {
				return fmt.Errorf("preparing refresh source %q failed", sourceName)
			}
			relation, err := SourceRelation(sourceModel, sourceModel.Sources[sourceName])
			if err != nil {
				return safeSourceError(sourceName, err)
			}
			table := fmt.Sprintf("leapview_stage_%d_%s", sourceStageSequence.Add(1), sourceName)
			if err := validateIdentifier(table); err != nil {
				return err
			}
			if _, err := session.ExecContext(ctx, "CREATE TEMP TABLE "+quoteIdentifier(table)+" AS SELECT * FROM ("+relation+")"); err != nil {
				return safeSourceError(sourceName, err)
			}
			prepared.tables = append(prepared.tables, table)
			prepared.relations[sourceName] = stagedRelation{value: quoteIdentifier(table), kind: stagedRelationTable}
			prepared.relationQueries[sourceName] = quoteIdentifier(table)
			columns, err := describeRelationSchema(ctx, session, quoteIdentifier(table))
			if err != nil {
				return safeSourceError(sourceName, err)
			}
			source.Schema = semanticmodel.TableSchema{Columns: columns}
			resolved.Sources[sourceName] = source
			original := model.Sources[sourceName]
			original.Schema = source.Schema
			model.Sources[sourceName] = original
			return nil
		}); err != nil {
			if errors.Is(err, errSourceCleanupFailed) {
				if !cleanupUncertain {
					reportCleanup(r.db, telemetry, errSourceCleanupFailed)
				}
				cleanupUncertain = true
			}
			observeSource(telemetry, sourceKind, "failed")
			return nil, err
		}
		observeSource(telemetry, sourceKind, "succeeded")
	}
	if err := resolved.ValidateDiscoveredSourceSchemas(); err != nil {
		return nil, fmt.Errorf("validating staged source schemas: %w", err)
	}
	closeSession = false
	return prepared, nil
}

func (r *SourceRuntime) loadSourceExtensions(ctx context.Context, session analyticsresource.Session, model *semanticmodel.Model, loaded map[string]struct{}) error {
	required := RequiredExtensions(model)
	for _, extension := range required {
		if _, ok := loaded[extension]; ok {
			continue
		}
		if r.extensionAdmission == nil {
			return fmt.Errorf("source preparation requires extension admission for %s", strings.Join(required, ", "))
		}
		admitted, err := r.extensionAdmission.AdmitExtension(ctx, extension)
		if err != nil {
			return fmt.Errorf("extension %s was not admitted: %w", extension, err)
		}
		if err := validateAdmittedExtension(extension, admitted); err != nil {
			return err
		}
		if _, err := session.ExecContext(ctx, loadExtensionStatement(admitted.Path)); err != nil {
			return fmt.Errorf("loading admitted extension %s: %w", extension, err)
		}
		loaded[extension] = struct{}{}
	}
	return nil
}

func tryCleanupSourceAccess(session analyticsresource.Session, model *semanticmodel.Model, attached map[string]struct{}) (err error, panicked bool) {
	returned := false
	func() {
		defer func() {
			if !returned {
				_ = recover()
				panicked = true
			}
		}()
		err = cleanupSourceAccess(session, model, attached)
		returned = true
	}()
	return err, panicked
}

func tryCloseSourceSession(closer interface{ Close() error }) (err error, panicked bool) {
	returned := false
	func() {
		defer func() {
			if !returned {
				_ = recover()
				panicked = true
			}
		}()
		err = closer.Close()
		returned = true
	}()
	return err, panicked
}

func joinSourceCleanupFailure(primary error) error {
	if primary == nil {
		return errSourceCleanupFailed
	}
	return errors.Join(primary, errSourceCleanupFailed)
}

func joinSourceCleanupFailureIfNeeded(primary, cleanupErr error) error {
	if cleanupErr == nil {
		return primary
	}
	return joinSourceCleanupFailure(primary)
}

func markSourceCleanupFatal(provider analyticsresource.SessionProvider) {
	if reporter, ok := provider.(fatalReporter); ok {
		reporter.MarkFatal(errSourceCleanupFailed)
	}
}

func validateAdmittedExtension(requested string, admitted AdmittedExtension) error {
	if strings.TrimSpace(admitted.Name) != requested {
		return fmt.Errorf("extension admission returned %q for requested %q", admitted.Name, requested)
	}
	if strings.TrimSpace(admitted.Identity) == "" || strings.TrimSpace(admitted.Version) == "" || strings.TrimSpace(admitted.Platform) == "" {
		return fmt.Errorf("extension %s admission is missing immutable identity, version, or platform", requested)
	}
	digest := strings.TrimSpace(admitted.Digest)
	if len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") {
		return fmt.Errorf("extension %s admission digest must be sha256:<64 hex characters>", requested)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:")); err != nil {
		return fmt.Errorf("extension %s admission digest is not canonical sha256: %w", requested, err)
	}
	base := filepath.Base(admitted.Path)
	if strings.ContainsAny(admitted.Path, "\x00\r\n") {
		return fmt.Errorf("extension %s admission path contains a control character", requested)
	}
	stem := strings.TrimSuffix(base, ".duckdb_extension")
	expectedStem := extensiondomain.ArtifactFilenameStem(requested)
	if !filepath.IsAbs(admitted.Path) || filepath.Clean(admitted.Path) != admitted.Path || !strings.HasSuffix(base, ".duckdb_extension") || stem != expectedStem && !strings.HasPrefix(stem, expectedStem+"-") {
		return fmt.Errorf("extension %s admission path must be absolute", requested)
	}
	return nil
}

func loadExtensionStatement(path string) string {
	return "LOAD '" + strings.ReplaceAll(filepath.ToSlash(path), "'", "''") + "'"
}

func sourcePreparationModel(model *semanticmodel.Model) *semanticmodel.Model {
	resolved := *model
	resolved.Connections = make(map[string]semanticmodel.Connection, len(model.Connections))
	for name, connection := range model.Connections {
		connection.Auth = nil
		resolved.Connections[name] = connection
	}
	resolved.Sources = maps.Clone(model.Sources)
	return &resolved
}

func refreshSourceModel(model *semanticmodel.Model, sourceName string, source semanticmodel.Source, connectionName string, connection semanticmodel.Connection) *semanticmodel.Model {
	source.Connection = connectionName
	return &semanticmodel.Model{
		Name: model.Name, DefaultConnection: connectionName,
		Connections: map[string]semanticmodel.Connection{connectionName: connection},
		Sources:     map[string]semanticmodel.Source{sourceName: source},
	}
}

func lockSourceScope(connection semanticmodel.Connection, logicalName string, telemetry refreshTelemetry) func() {
	if connection.Kind == "managed" {
		return func() {}
	}
	key := sourceScopeLockKey(connection, logicalName)
	value, _ := sourceScopeLocks.LoadOrStore(key, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		connector, _, _ := strings.Cut(key, "\x00")
		if telemetry != nil {
			telemetry.ObserveSecretScopeContention(connector)
		}
		lock.Lock()
	}
	return lock.Unlock
}

func sourceScopeLockKey(connection semanticmodel.Connection, logicalName string) string {
	scope := firstNonEmpty(connection.Scope, connection.Path, connection.Host, logicalName)
	return connection.Kind + "\x00" + scope
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
		markSourceCleanupFatal(provider)
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

func (r *SourceRuntime) withResolvedConnection(
	ctx context.Context,
	name string,
	connection semanticmodel.Connection,
	consume func(semanticmodel.Connection) error,
) error {
	if consume == nil {
		return fmt.Errorf("source connection consumer is required")
	}
	connection.Auth = nil
	if connection.Kind == "managed" || connection.Access == semanticmodel.ConnectionAccessPublic && r.connectionResolver == nil {
		return consume(connection)
	}
	if r.connectionResolver != nil {
		return r.connectionResolver.WithConnection(ctx, name, connection, func(target semanticmodel.Connection) error {
			target.Auth = maps.Clone(target.Auth)
			if target.Access == semanticmodel.ConnectionAccessPublic {
				clear(target.Auth)
				target.Auth = nil
			}
			defer clear(target.Auth)
			return consume(target)
		})
	}
	if connection.Access == semanticmodel.ConnectionAccessPublic {
		return consume(connection)
	}
	if r.resolver == nil {
		return fmt.Errorf("source credential resolver is unavailable")
	}
	auth, err := r.resolver.Resolve(ctx, name, connection)
	if err != nil {
		return err
	}
	connection.Auth = maps.Clone(auth)
	defer clear(connection.Auth)
	return consume(connection)
}

func (p *PreparedSources) PlanModelTable(ctx context.Context, _ *semanticmodel.Model, tableName string, table semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	return planModelTable(ctx, p.session, p.model, tableName, table, p.relations)
}

func (p *PreparedSources) PlanModelTableInNamespace(ctx context.Context, _ *semanticmodel.Model, tableName string, table semanticmodel.Table, relationNamespace string) (analyticsmaterialize.ModelTablePlan, error) {
	return planModelTableInNamespace(ctx, p.session, p.model, tableName, table, p.relations, relationNamespace)
}

func sourceObservationFailure(ctx context.Context, budget analyticsmaterialize.ObservationBudget, err error) analyticsmaterialize.ObservationFailure {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || budget.MaxMillis > 0 && ctx.Err() != nil {
		return analyticsmaterialize.ObservationTimeout
	}
	return analyticsmaterialize.ObservationUnavailable
}

func sourceObservationTime(value any) time.Time {
	switch value := value.(type) {
	case time.Time:
		return value.UTC()
	case *time.Time:
		if value != nil {
			return value.UTC()
		}
	case string:
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.UTC()
			}
		}
	}
	return time.Time{}
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
			_, err = session.ExecContext(cleanupCtx, "DETACH DATABASE IF EXISTS "+alias)
		}
		result = errors.Join(result, err)
	}
	secrets := map[string]struct{}{}
	for name, connection := range model.Connections {
		spec, ok := connectors.LookupConnection(connection.Kind)
		if ok && spec.SecretType != "" {
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
	Models             map[string]*semanticmodel.Model
	ModelTables        map[string]semanticmodel.Table
	Database           analyticsruntime.ProjectDatabase
	SourceWork         *sourcework.Gate
	CredentialResolver CredentialResolver
	ConnectionResolver analyticsruntime.ConnectionResolver
	ExtensionAdmission ExtensionAdmission
	SnapshotID         int64
	ServingStateID     string
	ProjectID          projectgraph.ResourceID
	CandidateID        string
	Environment        string
	// RelationNamespace scopes materialization DDL for an isolated candidate.
	// Empty retains the legacy model schema for non-native callers; candidate
	// runtimes must provide a validated namespace.
	RelationNamespace   string
	TargetType          string
	TargetID            string
	SemanticDigest      string
	ArtifactDigest      string
	SourceDataDigest    string
	RequiredExtensions  []string
	SkipInitialRefresh  bool
	MaterializationOnly bool
	ResultPartition     resultidentity.Partition
	QueryResultCache    *resultcache.Scope
	ImmutableByteCache  *resultcache.Scope
	ExecutionScope      *resultcache.ExecutionScope
	ResultLimits        dataquery.ResultLimits
	DependencyEvidence  map[string]resultidentity.Evidence
}

type ProjectRuntime struct {
	mu                      sync.Mutex
	projectID               projectgraph.ResourceID
	db                      analyticsmaterialize.Database
	sessions                analyticsresource.SessionProvider
	committer               duckLakeCommitter
	sources                 *SourceRuntime
	models                  map[string]*semanticmodel.Model
	materializationModel    *semanticmodel.Model
	views                   map[string]*analyticsmaterialize.Runtime
	lastRefresh             time.Time
	lastSnapshotID          int64
	sourceObservations      []analyticsmaterialize.SourceObservation
	commitMetadata          map[string]string
	queryResultCacheScope   *resultcache.Scope
	immutableByteCacheScope *resultcache.Scope
	executionScope          *resultcache.ExecutionScope
	viewConfig              ProjectRuntimeConfig
}

// Planner returns the activation-owned planner for one semantic model. The
// returned pointer is immutable after construction and is shared by dashboard
// consumers and authorization rather than recompiled per request.
func (r *ProjectRuntime) Planner(modelID string) (*semanticquery.Planner, bool) {
	if r == nil {
		return nil, false
	}
	view, ok := r.views[modelID]
	if !ok || view == nil {
		return nil, false
	}
	planner := view.Planner()
	return planner, planner != nil
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
	if config.CandidateID != "" {
		if config.RelationNamespace == "" {
			return nil, fmt.Errorf("candidate relation namespace is required")
		}
		if config.RelationNamespace == "model" || config.RelationNamespace == "source" {
			return nil, fmt.Errorf("candidate relation namespace cannot use reserved schema %q", config.RelationNamespace)
		}
		if config.RelationNamespace != strings.TrimSpace(config.RelationNamespace) {
			return nil, fmt.Errorf("candidate relation namespace must be canonical")
		}
	}
	if config.RelationNamespace == "" {
		config.RelationNamespace = "model"
	}
	if err := validateRelationNamespace(config.RelationNamespace); err != nil {
		return nil, fmt.Errorf("relation namespace: %w", err)
	}
	dependencyEvidence := make(map[string]resultidentity.Evidence, len(config.DependencyEvidence))
	for modelID, evidence := range config.DependencyEvidence {
		dependencyEvidence[modelID] = evidence
	}
	config.DependencyEvidence = dependencyEvidence
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
	if config.ExtensionAdmission != nil {
		sources.extensionAdmission = config.ExtensionAdmission
	}
	if config.ConnectionResolver != nil {
		sources = NewSourceRuntimeWithConnectionResolver(db, config.ConnectionResolver)
		if config.ExtensionAdmission != nil {
			sources.extensionAdmission = config.ExtensionAdmission
		}
	}
	sources.sourceWork = config.SourceWork
	materializationModel, err := physicalProjectModel(config.Models, config.ModelTables)
	if err != nil {
		return nil, err
	}
	if config.SnapshotID == 0 {
		for modelID, model := range config.Models {
			if err := analyticsmaterialize.ValidateFilesWithResolver(model, sources); err != nil {
				return nil, fmt.Errorf("semantic model %q: %w", modelID, err)
			}
		}
	}
	executionScope := config.ExecutionScope
	if executionScope == nil {
		executionScope = resultcache.NewExecutionScope()
	}
	config.ExecutionScope = executionScope
	runtime := &ProjectRuntime{
		projectID:               config.ProjectID,
		db:                      db,
		sessions:                db,
		committer:               db,
		sources:                 sources,
		models:                  config.Models,
		materializationModel:    materializationModel,
		views:                   map[string]*analyticsmaterialize.Runtime{},
		commitMetadata:          projectCommitMetadata(config),
		queryResultCacheScope:   config.QueryResultCache,
		immutableByteCacheScope: config.ImmutableByteCache,
		executionScope:          executionScope,
		viewConfig:              config,
	}
	if config.SnapshotID > 0 {
		if err := discoverSnapshotModelSchemas(ctx, db, config.Models, config.SnapshotID, config.RelationNamespace); err != nil {
			return nil, errors.Join(err, runtime.Close())
		}
		runtime.lastSnapshotID = config.SnapshotID
	} else if !config.SkipInitialRefresh {
		if err := runtime.Refresh(ctx); err != nil {
			return nil, errors.Join(err, runtime.Close())
		}
	}
	if len(runtime.views) == 0 {
		if err := runtime.rebuildViews(ctx); err != nil {
			return nil, errors.Join(err, runtime.Close())
		}
	}
	return runtime, nil
}

func (r *ProjectRuntime) rebuildViews(ctx context.Context) error {
	if r == nil || r.viewConfig.MaterializationOnly {
		return nil
	}
	config := r.viewConfig
	config.Models = r.models
	if r.lastSnapshotID > 0 {
		config.SnapshotID = r.lastSnapshotID
	}
	next := make(map[string]*analyticsmaterialize.Runtime, len(r.models))
	for modelID, model := range r.models {
		dependencyEvidence := config.DependencyEvidence[modelID]
		tableRelation := func(table string) (string, error) {
			physical := strings.TrimSpace(table)
			if err := validateIdentifier(physical); err != nil {
				return "", fmt.Errorf("physical table %q: %w", physical, err)
			}
			if config.SnapshotID > 0 {
				return analyticsducklake.QualifiedSnapshotRelationInNamespace(config.SnapshotID, config.RelationNamespace, physical)
			}
			return "model." + physical, nil
		}
		view, err := analyticsmaterialize.NewRuntimeView(ctx, analyticsmaterialize.RuntimeConfig{
			ServingStateID: config.ServingStateID,
			ModelID:        modelID, Model: model, ResultPartition: config.ResultPartition,
			Database: r.db, Sources: r.sources, Resolver: r.sources,
			SnapshotOnly: config.SnapshotID > 0, TableRelation: tableRelation,
			QueryResultCache: config.QueryResultCache, ImmutableByteCache: config.ImmutableByteCache,
			ExecutionScope:     config.ExecutionScope,
			ResultLimits:       config.ResultLimits,
			DependencyEvidence: dependencyEvidence,
			RequiredExtensions: config.RequiredExtensions,
		})
		if err != nil {
			for _, opened := range next {
				_ = opened.CloseView()
			}
			return fmt.Errorf("compile semantic model %q runtime: %w", modelID, err)
		}
		next[modelID] = view
	}
	previous := r.views
	r.views = next
	for _, view := range previous {
		if err := view.CloseView(); err != nil {
			return err
		}
	}
	return nil
}

// discoverSnapshotModelSchemas populates executable table schemas from the
// immutable DuckLake snapshot. Snapshot activation deliberately does not
// inspect authored sources: source connections and credentials may no longer
// be available when a committed serving generation is reopened.
func discoverSnapshotModelSchemas(ctx context.Context, provider analyticsresource.SessionProvider, models map[string]*semanticmodel.Model, snapshotID int64, relationNamespace string) error {
	if provider == nil {
		return fmt.Errorf("snapshot schema discovery requires a DuckDB database")
	}
	if snapshotID <= 0 {
		return fmt.Errorf("snapshot schema discovery requires a positive snapshot id")
	}
	leaseProvider, ok := provider.(analyticsresource.Provider)
	if !ok {
		return fmt.Errorf("snapshot schema discovery requires an analytical lease provider")
	}
	lease, err := leaseProvider.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("snapshot schema discovery: acquire database lease: %w", err)
	}
	defer lease.Release()
	queryCtx := lease.Context()
	session, err := provider.Session(queryCtx)
	if err != nil {
		return fmt.Errorf("snapshot schema discovery: open database session: %w", err)
	}
	for _, modelID := range sortedKeys(models) {
		model := models[modelID]
		if model == nil {
			return fmt.Errorf("snapshot schema discovery: semantic model %q is required", modelID)
		}
		tableNames := make([]string, 0, len(model.Tables))
		for tableName := range model.Tables {
			tableNames = append(tableNames, tableName)
		}
		sort.Strings(tableNames)
		for _, tableName := range tableNames {
			physical, err := physicalTableName(model, tableName)
			if err != nil {
				return fmt.Errorf("snapshot schema discovery semantic model %q table %q: %w", modelID, tableName, err)
			}
			relation, err := analyticsducklake.QualifiedSnapshotRelationInNamespace(snapshotID, relationNamespace, physical)
			if err != nil {
				return fmt.Errorf("snapshot schema discovery semantic model %q table %q: %w", modelID, tableName, err)
			}
			columns, err := describeRelationSchema(queryCtx, session, relation)
			if err != nil {
				return fmt.Errorf("snapshot schema discovery semantic model %q table %q: %w", modelID, tableName, err)
			}
			if len(columns) == 0 {
				return fmt.Errorf("snapshot schema discovery semantic model %q table %q: relation has no columns", modelID, tableName)
			}
			for index := range columns {
				columns[index].PrimaryKey = false
			}
			table := model.Tables[tableName]
			table.Schema = semanticmodel.TableSchema{Columns: columns}
			model.Tables[tableName] = table
		}
		// Reopened snapshots are intentionally source-free: source credentials and
		// files may no longer exist. Validate the resolved materialized model
		// contract against an execution snapshot, which retains materialized-table facts
		// while omitting authored source state.
		if err := model.ResolveDiscoveredModelFields(); err != nil {
			return fmt.Errorf("snapshot schema discovery semantic model %q: %w", modelID, err)
		}
		if err := model.ExecutionSnapshot().ValidateDiscoveredSchemas(); err != nil {
			return fmt.Errorf("snapshot schema discovery semantic model %q: %w", modelID, err)
		}
	}
	return nil
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
	return r.rebuildViews(ctx)
}

func (r *ProjectRuntime) RefreshModelTables(ctx context.Context, modelID string, tableNames []string) error {
	if r == nil {
		return fmt.Errorf("project runtime is not initialized")
	}
	model, ok := r.models[modelID]
	if !ok {
		return fmt.Errorf("unknown semantic model %q", modelID)
	}
	physicalNames, err := physicalTableNames(model, tableNames)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()

	lastRefresh, snapshotID, err := r.refreshModel(ctx, r.materializationModel, physicalNames)
	if err != nil {
		return err
	}
	r.clearQueryCaches()
	if err := r.discoverServingSchemas(ctx, model); err != nil {
		return err
	}
	r.lastRefresh = lastRefresh
	r.lastSnapshotID = snapshotID
	return r.rebuildViews(ctx)
}

// VerifySemantic prepares representative governed plans and proves entity
// claims for one model against this immutable project runtime.
func (r *ProjectRuntime) VerifySemantic(ctx context.Context, modelID string) error {
	if r == nil {
		return fmt.Errorf("project runtime is not initialized")
	}
	modelID = strings.TrimSpace(modelID)
	view, ok := r.views[modelID]
	if !ok || view == nil {
		return fmt.Errorf("semantic model %q is not available", modelID)
	}
	return view.VerifySemantic(ctx)
}

func (r *ProjectRuntime) RefreshProjectTables(ctx context.Context, tableNames []string) error {
	if r == nil {
		return fmt.Errorf("project runtime is not initialized")
	}
	if len(tableNames) == 0 {
		return fmt.Errorf("Model materialization refresh plan is empty")
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
	return r.rebuildViews(ctx)
}

// RefreshProjectTablesWithObservationWriter is the native-build variant of
// RefreshProjectTables. The writer is called inside CommitTransaction after
// table materialization and before DuckLake acknowledges the external commit.
// Its error is returned from the transaction callback and therefore aborts the
// DuckLake commit. Observations are published to the runtime only after that
// commit succeeds.
func (r *ProjectRuntime) RefreshProjectTablesWithObservationWriter(ctx context.Context, tableNames []string, writer analyticsmaterialization.ObservationWriter) error {
	if r == nil {
		return fmt.Errorf("project runtime is not initialized")
	}
	if len(tableNames) == 0 {
		return fmt.Errorf("Model refresh plan is empty")
	}
	if writer == nil {
		return fmt.Errorf("source observation writer is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, release, err := r.acquireOperation(ctx)
	if err != nil {
		return err
	}
	defer release()

	lastRefresh, snapshotID, err := r.refreshModelWithObservationWriter(ctx, r.materializationModel, tableNames, writer)
	if err != nil {
		return err
	}
	r.clearQueryCaches()
	if err := r.discoverServingSchemas(ctx, r.materializationModel); err != nil {
		return err
	}
	r.lastRefresh = lastRefresh
	r.lastSnapshotID = snapshotID
	return r.rebuildViews(ctx)
}

func (r *ProjectRuntime) clearQueryCaches() {
	for _, view := range r.views {
		view.ClearQueryCache()
	}
}

func (r *ProjectRuntime) discoverServingSchemas(ctx context.Context, refreshed *semanticmodel.Model) error {
	applyDiscoveredSourceSchemas(refreshed, r.models)
	for modelID, model := range r.models {
		if err := discoverSchemasInNamespace(ctx, r.sessions, model, r.viewConfig.RelationNamespace); err != nil {
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

func cloneSourceObservations(observations []analyticsmaterialize.SourceObservation) []analyticsmaterialize.SourceObservation {
	if observations == nil {
		return nil
	}
	result := make([]analyticsmaterialize.SourceObservation, len(observations))
	for i, observation := range observations {
		result[i] = observation
		result[i].Schema = append([]semanticmodel.ColumnSchema(nil), observation.Schema...)
		result[i].CheckEvidence = append(result[i].CheckEvidence[:0:0], observation.CheckEvidence...)
		for column := range result[i].Schema {
			if result[i].Schema[column].Nullable != nil {
				nullable := *result[i].Schema[column].Nullable
				result[i].Schema[column].Nullable = &nullable
			}
		}
	}
	return result
}

func ProjectModelTableDependencyOrder(models map[string]*semanticmodel.Model, selectedTable string) ([]string, error) {
	model, err := physicalProjectModel(models, nil)
	if err != nil {
		return nil, err
	}
	selectedTable = strings.TrimSpace(selectedTable)
	if _, ok := model.Tables[selectedTable]; !ok {
		matches := map[string]struct{}{}
		for _, modelID := range sortedKeys(models) {
			semantic := models[modelID]
			if semantic == nil {
				continue
			}
			compiled, compileErr := semanticquery.CompileDatasetBindings(semantic)
			if compileErr != nil {
				return nil, fmt.Errorf("semantic model %q dataset bindings: %w", modelID, compileErr)
			}
			if dataset, ok := compiled.Dataset(selectedTable); ok && strings.TrimSpace(dataset.ModelName()) != "" {
				matches[strings.TrimSpace(dataset.ModelName())] = struct{}{}
			}
		}
		if len(matches) == 1 {
			for physical := range matches {
				selectedTable = physical
			}
		} else if len(matches) > 1 {
			return nil, fmt.Errorf("semantic dataset alias %q resolves to multiple Model materializations", selectedTable)
		}
	}
	return analyticsmaterialize.ModelTableDependencyOrder(model, selectedTable)
}

// physicalTableName resolves a semantic dataset alias to the authored backing
// Model that is materialized in the project catalog. Missing dataset
// bindings are activation errors and are never inferred at runtime.
func physicalTableName(model *semanticmodel.Model, table string) (string, error) {
	if model == nil {
		return "", fmt.Errorf("semantic model is required")
	}
	table = strings.TrimSpace(table)
	if table == "" {
		return "", fmt.Errorf("Model is required")
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		return "", err
	}
	if dataset, ok := compiled.Dataset(table); ok {
		return dataset.ModelName(), nil
	}
	return "", fmt.Errorf("unknown semantic dataset %q", table)
}

func physicalTableNames(model *semanticmodel.Model, tables []string) ([]string, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(tables))
	result := make([]string, 0, len(tables))
	for _, table := range tables {
		physical, err := physicalTableName(model, table)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[physical]; ok {
			continue
		}
		seen[physical] = struct{}{}
		result = append(result, physical)
	}
	return result, nil
}

func (r *ProjectRuntime) refreshModel(ctx context.Context, model *semanticmodel.Model, tableNames []string) (time.Time, int64, error) {
	return r.refreshModelWithObservationWriter(ctx, model, tableNames, nil)
}

func (r *ProjectRuntime) refreshModelWithObservationWriter(ctx context.Context, model *semanticmodel.Model, tableNames []string, observationWriter analyticsmaterialization.ObservationWriter) (time.Time, int64, error) {
	prepared, err := r.sources.Prepare(ctx, model)
	if err != nil {
		return time.Time{}, 0, err
	}
	if observationWriter != nil && r.committer == nil {
		_ = prepared.Close()
		return time.Time{}, 0, fmt.Errorf("source observation writer requires a DuckLake commit transaction")
	}
	if r.committer == nil {
		if len(tableNames) > 0 {
			lastRefresh, err := refreshModelTablesInNamespace(ctx, r.db, prepared, model, tableNames, r.viewConfig.RelationNamespace)
			observations, observationErr := captureSourceObservations(ctx, prepared)
			if err == nil && observationErr == nil {
				r.sourceObservations = observations
			}
			return lastRefresh, 0, errors.Join(err, observationErr, prepared.Close())
		}
		lastRefresh, err := refreshModelInNamespace(ctx, r.db, prepared, model, r.viewConfig.RelationNamespace)
		observations, observationErr := captureSourceObservations(ctx, prepared)
		if err == nil && observationErr == nil {
			r.sourceObservations = observations
		}
		return lastRefresh, 0, errors.Join(err, observationErr, prepared.Close())
	}
	metadata := map[string]string{}
	for key, value := range r.commitMetadata {
		metadata[key] = value
	}
	servingStateID := firstNonEmpty(r.commitMetadata["servingStateId"], "project-refresh")
	var callbackObservations []analyticsmaterialize.SourceObservation
	snapshotID, err := r.committer.CommitTransaction(ctx, servingStateID, metadata, func(tx transaction.Transaction) error {
		executor := txExecutor{tx: tx}
		sources := txPreparedSources{PreparedSources: prepared.(*PreparedSources), tx: tx}
		var materializeErr error
		if len(tableNames) > 0 {
			materializeErr = analyticsmaterialize.ModelTablesNamedInNamespace(ctx, executor, sources, model, tableNames, r.viewConfig.RelationNamespace)
		} else {
			materializeErr = analyticsmaterialize.ModelTablesInNamespace(ctx, executor, sources, model, r.viewConfig.RelationNamespace)
		}
		if materializeErr != nil {
			return materializeErr
		}
		if observationWriter == nil {
			return nil
		}
		observations, observationErr := captureSourceObservations(ctx, prepared)
		if observationErr != nil {
			return observationErr
		}
		callbackObservations = cloneSourceObservations(observations)
		return observationWriter(ctx, cloneSourceObservations(observations))
	})
	if err != nil {
		_ = prepared.Close()
		return time.Time{}, 0, err
	}
	observations := callbackObservations
	if observationWriter == nil {
		var observationErr error
		observations, observationErr = captureSourceObservations(ctx, prepared)
		if observationErr != nil {
			_ = prepared.Close()
			return time.Time{}, 0, observationErr
		}
	}
	r.sourceObservations = observations
	if err := prepared.Close(); err != nil {
		return time.Time{}, 0, fmt.Errorf("cleaning refresh staging: %w", err)
	}
	return time.Now(), snapshotID, nil
}

func refreshModelTablesInNamespace(ctx context.Context, executor analyticsmaterialize.Executor, sources analyticsmaterialize.ModelTablePlanner, model *semanticmodel.Model, tableNames []string, relationNamespace string) (time.Time, error) {
	if relationNamespace == "model" {
		return analyticsmaterialize.RefreshModelTables(ctx, executor, sources, model, tableNames)
	}
	if err := analyticsmaterialize.ModelTablesNamedInNamespace(ctx, executor, sources, model, tableNames, relationNamespace); err != nil {
		return time.Time{}, err
	}
	return time.Now(), nil
}

func refreshModelInNamespace(ctx context.Context, executor analyticsmaterialize.Executor, sources analyticsmaterialize.ModelTablePlanner, model *semanticmodel.Model, relationNamespace string) (time.Time, error) {
	if relationNamespace == "model" {
		return analyticsmaterialize.Refresh(ctx, executor, sources, model)
	}
	if err := analyticsmaterialize.ModelTablesInNamespace(ctx, executor, sources, model, relationNamespace); err != nil {
		return time.Time{}, err
	}
	return time.Now(), nil
}

func captureSourceObservations(ctx context.Context, prepared analyticsmaterialize.PreparedSources) ([]analyticsmaterialize.SourceObservation, error) {
	provider, ok := prepared.(analyticsmaterialize.SourceObservationProvider)
	if !ok {
		return nil, nil
	}
	return provider.SourceObservations(ctx)
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
	if r.executionScope != nil {
		if err := r.executionScope.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, view := range r.views {
		if err := view.CloseView(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.immutableByteCacheScope != nil && r.immutableByteCacheScope != r.queryResultCacheScope {
		if err := r.immutableByteCacheScope.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.queryResultCacheScope != nil {
		if err := r.queryResultCacheScope.Close(); err != nil {
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

// SourceObservations returns source evidence captured during the latest live
// refresh. It never resolves authored paths or opens a new credential session.
func (r *ProjectRuntime) SourceObservations() []analyticsmaterialize.SourceObservation {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneSourceObservations(r.sourceObservations)
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

func (r txPreparedSources) PlanModelTableInNamespace(ctx context.Context, _ *semanticmodel.Model, tableName string, table semanticmodel.Table, relationNamespace string) (analyticsmaterialize.ModelTablePlan, error) {
	return planModelTableInNamespace(ctx, r.tx, r.model, tableName, table, r.relations, relationNamespace)
}

func physicalProjectModel(models map[string]*semanticmodel.Model, authoredTables map[string]semanticmodel.Table) (*semanticmodel.Model, error) {
	projectModel := &semanticmodel.Model{
		Name:              "project",
		DefaultConnection: "",
		Connections:       map[string]semanticmodel.Connection{},
		Sources:           map[string]semanticmodel.Source{},
		Tables:            map[string]semanticmodel.Table{},
		Metrics:           map[string]semanticmodel.Metric{},
	}
	modelIDs := sortedKeys(models)
	for _, modelID := range modelIDs {
		model := models[modelID]
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
	}
	for _, name := range sortedKeys(authoredTables) {
		table := authoredTables[name]
		table.ModelName = name
		projectModel.Tables[name] = table
	}
	for _, modelID := range modelIDs {
		model := models[modelID]
		// A semantic model exposes dataset aliases, but project materialization
		// must emit one physical table per authored Model. Resolve aliases before
		// merging so two semantic datasets can safely reuse one Model materialization.
		compiled, err := semanticquery.CompileDatasetBindings(model)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q dataset bindings: %w", modelID, err)
		}
		tableNames := compiled.DatasetNames()
		for _, name := range tableNames {
			dataset, _ := compiled.Dataset(name)
			table := dataset.Table()
			physicalName := dataset.ModelName()
			if _, catalogued := authoredTables[physicalName]; catalogued {
				// The project catalog is authoritative for physical execution and
				// includes dependencies that need not be semantic datasets.
				continue
			}
			table.ModelDependencies = append([]string(nil), table.ModelDependencies...)
			for index, dependency := range table.ModelDependencies {
				physicalDependency, err := compiled.ResolvePhysicalModelName(dependency)
				if err != nil {
					return nil, fmt.Errorf("semantic model %q table %q dependency %q: %w", modelID, name, dependency, err)
				}
				table.ModelDependencies[index] = physicalDependency
			}
			existing, ok := projectModel.Tables[physicalName]
			if ok && !reflect.DeepEqual(tablePhysicalSignature(existing), tablePhysicalSignature(table)) {
				return nil, fmt.Errorf("semantic model %q materialization for Model %q conflicts with another project Model", modelID, physicalName)
			}
			projectModel.Tables[physicalName] = table
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
	Execution          semanticmodel.ExecutionDefinition
	Columns            map[string]semanticmodel.ModelColumn
	Entities           map[string]semanticmodel.EntityDefinition
	GrainEntity        string
	SourceDependencies []string
	ModelDependencies  []string
}

func tablePhysicalSignature(table semanticmodel.Table) tablePhysicalSignatureValue {
	return tablePhysicalSignatureValue{
		Execution:          table.Execution,
		Columns:            table.Columns,
		Entities:           table.Entities,
		GrainEntity:        table.GrainEntity,
		SourceDependencies: append([]string{}, table.SourceDependencies...),
		ModelDependencies:  append([]string{}, table.ModelDependencies...),
	}
}
