package duckdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	duckdbdriver "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/connectors"
	"github.com/flidai/leapview/internal/analytics/duckdbsession"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/platform/outbound"
	"github.com/flidai/leapview/internal/platform/typednil"
)

type TargetRuntimeSession interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	Close() error
}

type TargetRuntimeSessionOpener func(context.Context) (TargetRuntimeSession, error)

// ErrTargetPoolCleanupFailed reports that a failed target-pool preparation
// could not confirm closure of the isolated session it opened.
var ErrTargetPoolCleanupFailed = errors.New("target runtime pool cleanup failed")

func NewIsolatedTargetRuntimeOpener() TargetRuntimeSessionOpener {
	return func(ctx context.Context) (TargetRuntimeSession, error) {
		connector, err := duckdbdriver.NewConnector(":memory:", func(driver.ExecerContext) error {
			return nil
		})
		if err != nil {
			return nil, err
		}
		session, err := duckdbsession.OpenPinned(ctx, connector)
		if err != nil {
			return nil, err
		}
		return session, nil
	}
}

type TargetRuntimeLimits struct {
	MemoryMaxBytes int64
	TempMaxBytes   int64
	MaxThreads     int
}

type TargetRuntimePoolFactoryConfig struct {
	Open               TargetRuntimeSessionOpener
	Limits             TargetRuntimeLimits
	RequireTLS         bool
	ExtensionAdmission ExtensionAdmission
	DestinationPolicy  *outbound.Policy
	HTTPProxyURL       string
	HTTPProxyUser      string
	HTTPProxyPassword  string
}

type TargetRuntimePoolFactory struct {
	open               TargetRuntimeSessionOpener
	limits             TargetRuntimeLimits
	requireTLS         bool
	extensionAdmission ExtensionAdmission
	destinationPolicy  *outbound.Policy
	httpProxyURL       string
	httpProxyUser      string
	httpProxyPassword  string
}

var _ connectionbinding.RuntimePoolFactory = (*TargetRuntimePoolFactory)(nil)

func NewTargetRuntimePoolFactory(config TargetRuntimePoolFactoryConfig) (*TargetRuntimePoolFactory, error) {
	if config.Open == nil || config.Limits.MemoryMaxBytes <= 0 ||
		config.Limits.TempMaxBytes <= 0 || config.Limits.MaxThreads <= 0 {
		return nil, fmt.Errorf(
			"%w: target runtime opener and positive resource limits are required",
			connectionbinding.ErrInvalidBinding,
		)
	}
	return &TargetRuntimePoolFactory{
		open: config.Open, limits: config.Limits, requireTLS: config.RequireTLS, extensionAdmission: config.ExtensionAdmission,
		destinationPolicy: config.DestinationPolicy,
		httpProxyURL:      config.HTTPProxyURL,
		httpProxyUser:     config.HTTPProxyUser,
		httpProxyPassword: config.HTTPProxyPassword,
	}, nil
}

func (factory *TargetRuntimePoolFactory) Prepare(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
) (connectionbinding.RuntimePool, error) {
	if identity := snapshot.Identity(); identity.Validate() != nil || identity.CredentialVersionID != "" {
		return nil, connectionbinding.ErrIncompatibleBinding
	}
	return factory.prepare(ctx, binding, snapshot)
}

// PrepareLocal retains a local credential version on the concrete pool. The
// snapshot's version is an identity tag, not proof of runtime authority or
// destination scope. The caller must obtain the exact authorized reference,
// compare its scope to the serving identity and binding, and hold admission
// through the pool's entire lifetime. No serving caller is wired to this path
// until that authority and admission integration is complete.
//
// TargetBinding supplies endpoint policy. This path does not resolve its
// provider reference or change its validated provider version.
func (factory *TargetRuntimePoolFactory) PrepareLocal(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
) (connectionbinding.RuntimePool, error) {
	if identity := snapshot.Identity(); identity.Validate() != nil || identity.CredentialVersionID == "" ||
		binding.ConnectorKind != "postgres" || binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle {
		return nil, connectionbinding.ErrIncompatibleBinding
	}
	return factory.prepare(ctx, binding, snapshot)
}

func (factory *TargetRuntimePoolFactory) prepare(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
) (pool connectionbinding.RuntimePool, resultErr error) {
	if factory == nil || factory.open == nil {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	if err := validateTargetProbeBinding(binding, factory.requireTLS); err != nil {
		return nil, err
	}
	logical := semanticmodel.Connection{Kind: binding.ConnectorKind}
	if binding.AuthenticationMode == connectionbinding.AuthenticationNone {
		logical.Access = semanticmodel.ConnectionAccessPublic
	}
	connection, err := applyTargetBinding(
		logical,
		binding,
		snapshot,
	)
	if err != nil {
		return nil, err
	}
	defer clear(connection.Auth)
	if factory.destinationPolicy != nil && strings.TrimSpace(connection.Host) != "" {
		addresses, err := factory.destinationPolicy.ResolveHost(ctx, connection.Host)
		if err != nil {
			return nil, fmt.Errorf("%w: outbound target destination denied", connectionbinding.ErrInvalidBinding)
		}
		if connection.Kind == "postgres" {
			// DuckDB's PostgreSQL extension passes HOST and HOSTADDR separately to
			// libpq: HOST retains TLS identity while HOSTADDR binds the socket to
			// the policy-validated answer, preventing a second DNS lookup.
			connection.ResolvedHost = addresses[0].String()
		}
	}

	secret, ok, err := compileConnectionSecret(binding.ConnectionID.String(), connection)
	if err != nil || !ok {
		return nil, connectionbinding.ErrInvalidCredentialBundle
	}
	spec, _ := connectors.LookupConnection(binding.ConnectorKind)
	healthStatement := "SELECT 1"
	activationStatements := make([]string, 0, 2)
	switch spec.AttachKind {
	case connectors.AttachDatabase:
		attach, err := compileDatabaseAttach(binding.ConnectionID.String(), connection)
		if err != nil {
			return nil, connectionbinding.ErrInvalidCredentialBundle
		}
		activationStatements = append(activationStatements, attach)
	case connectors.AttachQuack:
		uri, err := connectors.QuackURI(connection.Host, connection.Port)
		if err != nil {
			return nil, connectionbinding.ErrInvalidCredentialBundle
		}
		healthStatement = fmt.Sprintf("SELECT * FROM quack_query('%s', 'SELECT 1')", sqlString(uri))
		activationStatements = append(activationStatements, healthStatement)
	default:
		// Path-backed target bindings still use the isolated pool as a
		// bounded activation/health gate. Source reads happen later through
		// the governed relation compiler, so there is no attach statement.
	}
	session, err := factory.open(ctx)
	if err != nil {
		if !isNilTargetRuntimeSession(session) {
			if session.Close() != nil {
				return nil, errors.Join(err, ErrTargetPoolCleanupFailed)
			}
		}
		return nil, err
	}
	if isNilTargetRuntimeSession(session) {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	closeOnFailure := true
	defer func() {
		if closeOnFailure {
			if session.Close() != nil {
				resultErr = errors.Join(resultErr, ErrTargetPoolCleanupFailed)
				pool = nil
			}
		}
	}()
	statements, err := (duckdbsession.ResourcePolicy{
		MemoryMaxBytes:    factory.limits.MemoryMaxBytes,
		TempMaxBytes:      factory.limits.TempMaxBytes,
		MaxThreads:        factory.limits.MaxThreads,
		HTTPProxyURL:      factory.httpProxyURL,
		HTTPProxyUser:     factory.httpProxyUser,
		HTTPProxyPassword: factory.httpProxyPassword,
	}).BoundedStatements()
	if err != nil {
		return nil, fmt.Errorf("build bounded DuckDB target runtime policy: %w", err)
	}
	for _, extension := range spec.RequiredExtensions {
		if factory.extensionAdmission == nil {
			return nil, fmt.Errorf("extension %s is required but has no admission", extension)
		}
		admitted, err := factory.extensionAdmission.AdmitExtension(ctx, extension)
		if err != nil {
			return nil, fmt.Errorf("extension %s was not admitted: %w", extension, err)
		}
		if err := validateAdmittedExtension(extension, admitted); err != nil {
			return nil, err
		}
		statements = append(statements, loadExtensionStatement(admitted.Path))
	}
	statements = append(statements, secret)
	statements = append(statements, activationStatements...)
	security, err := (duckdbsession.ResourcePolicy{LockConfiguration: true}).SecurityStatements()
	if err != nil {
		return nil, fmt.Errorf("build target runtime security policy: %w", err)
	}
	statements = append(statements, security...)
	for _, statement := range statements {
		if _, err := session.ExecContext(ctx, statement); err != nil {
			return nil, err
		}
	}
	closeOnFailure = false
	return &targetRuntimePool{
		session: session, connection: cloneTargetConnection(connection), healthStatement: healthStatement,
		credentialIdentity: snapshot.Identity(),
	}, nil
}

func validateTargetProbeBinding(binding connectionbinding.TargetBinding, requireTLS bool) error {
	if err := binding.Validate(); err != nil {
		return err
	}
	spec, ok := connectors.LookupConnection(binding.ConnectorKind)
	if !ok {
		return fmt.Errorf("%w: connector does not expose a bounded probe", connectionbinding.ErrInvalidBinding)
	}
	switch spec.AttachKind {
	case connectors.AttachDatabase:
		return validateDatabaseProbeEndpoint(binding, requireTLS)
	case connectors.AttachQuack:
		return validateQuackProbeEndpoint(binding)
	default:
		return fmt.Errorf("%w: connector does not expose a bounded probe", connectionbinding.ErrInvalidBinding)
	}
}

func validateDatabaseProbeEndpoint(binding connectionbinding.TargetBinding, requireTLS bool) error {
	if binding.ConnectorKind != "postgres" && binding.ConnectorKind != "mysql" {
		return fmt.Errorf("%w: connector does not expose a bounded database probe", connectionbinding.ErrInvalidBinding)
	}
	if strings.TrimSpace(binding.Endpoint.Host) == "" || binding.Endpoint.Port <= 0 ||
		strings.TrimSpace(binding.Endpoint.Database) == "" ||
		strings.TrimSpace(binding.Endpoint.SourceIdentity) == "" {
		return fmt.Errorf("%w: database endpoint, port, database, and source identity are required", connectionbinding.ErrInvalidBinding)
	}
	if requireTLS && !secureDatabaseTLSMode(binding.ConnectorKind, binding.Endpoint.TLSMode) {
		return fmt.Errorf("%w: production database probes require verified transport", connectionbinding.ErrInvalidBinding)
	}
	return nil
}

func validateQuackProbeEndpoint(binding connectionbinding.TargetBinding) error {
	if _, err := connectors.QuackURI(binding.Endpoint.Host, binding.Endpoint.Port); err != nil {
		return fmt.Errorf("%w: Quack host and port are required", connectionbinding.ErrInvalidBinding)
	}
	if binding.Endpoint.TLSMode != "require" {
		return fmt.Errorf("%w: Quack probes require verified transport", connectionbinding.ErrInvalidBinding)
	}
	if binding.Endpoint.Database != "" || binding.Endpoint.SourceIdentity != "" ||
		binding.Endpoint.ObjectScope != "" || len(binding.Endpoint.Options) != 0 {
		return fmt.Errorf("%w: Quack probes accept only host, port, and TLS mode", connectionbinding.ErrInvalidBinding)
	}
	return nil
}

func secureDatabaseTLSMode(kind, mode string) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch kind {
	case "postgres":
		return mode == "require" || mode == "verify-ca" || mode == "verify-full"
	case "mysql":
		return mode == "required" || mode == "verify_ca" || mode == "verify_identity"
	default:
		return false
	}
}

type targetRuntimePool struct {
	credentialIdentity connectionbinding.CredentialIdentity
	mu                 sync.Mutex
	session            TargetRuntimeSession
	connection         semanticmodel.Connection
	healthStatement    string
	nextOperation      uint64
	active             map[uint64]context.CancelFunc
	activeWork         sync.WaitGroup
	poisoned           bool
	closeDone          chan struct{}
	closeErr           error
}

var _ analyticsruntime.ConnectionResolver = (*targetRuntimePool)(nil)

// CredentialIdentity is immutable non-secret evidence retained after Close.
// It identifies this pool, not durable activation or retirement completion.
func (pool *targetRuntimePool) CredentialIdentity() connectionbinding.CredentialIdentity {
	if pool == nil {
		return connectionbinding.CredentialIdentity{}
	}
	return pool.credentialIdentity
}

func (pool *targetRuntimePool) HealthCheck(ctx context.Context) error {
	if pool == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	pool.mu.Lock()
	if pool.poisoned {
		pool.mu.Unlock()
		return analyticsruntime.ErrConnectionCleanupFailed
	}
	if pool.session == nil {
		pool.mu.Unlock()
		return connectionbinding.ErrProviderUnavailable
	}
	session := pool.session
	statement := pool.healthStatement
	if statement == "" {
		statement = "SELECT 1"
	}
	operationContext, cancel := context.WithCancel(ctx)
	pool.nextOperation++
	operationID := pool.nextOperation
	if pool.active == nil {
		pool.active = map[uint64]context.CancelFunc{}
	}
	pool.active[operationID] = cancel
	pool.activeWork.Add(1)
	pool.mu.Unlock()
	defer func() {
		cancel()
		pool.mu.Lock()
		delete(pool.active, operationID)
		pool.mu.Unlock()
		pool.activeWork.Done()
	}()
	_, err := session.ExecContext(operationContext, statement)
	return err
}

func (pool *targetRuntimePool) WithConnection(
	ctx context.Context,
	name string,
	logical semanticmodel.Connection,
	consume func(semanticmodel.Connection) error,
) (resultErr error) {
	return pool.withConnection(ctx, name, logical, nil, consume)
}

func (pool *targetRuntimePool) withLocalConnection(
	ctx context.Context,
	name string,
	logical semanticmodel.Connection,
	expected connectionbinding.CredentialIdentity,
	consume func(semanticmodel.Connection) error,
) error {
	return pool.withConnection(ctx, name, logical, &expected, consume)
}

func (pool *targetRuntimePool) withConnection(
	ctx context.Context,
	name string,
	logical semanticmodel.Connection,
	expectedLocalIdentity *connectionbinding.CredentialIdentity,
	consume func(semanticmodel.Connection) error,
) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pool == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	if consume == nil {
		return connectionbinding.ErrIncompatibleBinding
	}
	pool.mu.Lock()
	if pool.poisoned {
		pool.mu.Unlock()
		return analyticsruntime.ErrConnectionCleanupFailed
	}
	if pool.session == nil || pool.connection.Kind == "" {
		pool.mu.Unlock()
		return connectionbinding.ErrProviderUnavailable
	}
	if expectedLocalIdentity == nil {
		if pool.credentialIdentity.CredentialVersionID != "" {
			pool.mu.Unlock()
			return connectionbinding.ErrProviderUnavailable
		}
	} else if expectedLocalIdentity.Validate() != nil || expectedLocalIdentity.CredentialVersionID == "" ||
		pool.credentialIdentity != *expectedLocalIdentity {
		pool.mu.Unlock()
		return connectionbinding.ErrProviderUnavailable
	}
	if strings.TrimSpace(logical.Kind) != pool.connection.Kind {
		pool.mu.Unlock()
		return connectionbinding.ErrIncompatibleBinding
	}
	resolved := logical
	resolved.Host = pool.connection.Host
	resolved.ResolvedHost = pool.connection.ResolvedHost
	resolved.Port = pool.connection.Port
	resolved.Database = pool.connection.Database
	resolved.Username = pool.connection.Username
	resolved.SSLMode = pool.connection.SSLMode
	resolved.Scope = pool.connection.Scope
	resolved.Credentials = semanticmodel.ConnectionCredentials{}
	resolved.RuntimeOptions = logical.RuntimeOptions
	if pool.connection.RuntimeOptions.Path != "" {
		resolved.RuntimeOptions.Path = pool.connection.RuntimeOptions.Path
	}
	if pool.connection.RuntimeOptions.DataPath != "" {
		resolved.RuntimeOptions.DataPath = pool.connection.RuntimeOptions.DataPath
	}
	resolved.Auth = maps.Clone(pool.connection.Auth)
	validated, err := resolved.Validate(strings.TrimSpace(name))
	// Validate builds its own auth map; wipe the intermediate before handing
	// the validated copy to the synchronous consumer.
	clear(resolved.Auth)
	if err != nil {
		clear(validated.Auth)
		pool.mu.Unlock()
		return connectionbinding.ErrIncompatibleBinding
	}
	if err := ctx.Err(); err != nil {
		clear(validated.Auth)
		pool.mu.Unlock()
		return err
	}
	// Close fences new operations with session=nil under this mutex. Register
	// before unlocking so Close's Wait cannot race a later Add. The callback
	// runs after unlocking and must finish all use before it returns.
	pool.activeWork.Add(1)
	pool.mu.Unlock()

	defer func() {
		clear(validated.Auth)
		pool.activeWork.Done()
	}()
	resultErr, panicked := invokeTargetConnectionConsumer(consume, validated)
	cleanupFailed := panicked || errors.Is(resultErr, analyticsruntime.ErrConnectionCleanupFailed)
	if cleanupFailed {
		resultErr = analyticsruntime.ErrConnectionCleanupFailed
		pool.mu.Lock()
		pool.poisoned = true
		pool.mu.Unlock()
	}
	return resultErr
}

func invokeTargetConnectionConsumer(
	consume func(semanticmodel.Connection) error,
	connection semanticmodel.Connection,
) (resultErr error, panicked bool) {
	returned := false
	func() {
		defer func() {
			if !returned {
				// Callback panics can carry credential-bearing driver diagnostics.
				// Retain only the fact of panic and let this frame finish unwinding.
				_ = recover()
				panicked = true
			}
		}()
		resultErr = consume(connection)
		returned = true
	}()
	return resultErr, panicked
}

func (pool *targetRuntimePool) Close() error {
	return pool.CloseContext(context.Background())
}

func (pool *targetRuntimePool) CloseContext(ctx context.Context) error {
	if pool == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pool.mu.Lock()
	if pool.closeDone == nil {
		pool.closeDone = make(chan struct{})
		session := pool.session
		// Fence new health operations before starting the one cleanup task.
		// Existing operations keep their accounting until they actually exit.
		pool.session = nil
		active := make([]context.CancelFunc, 0, len(pool.active))
		for _, cancel := range pool.active {
			active = append(active, cancel)
		}
		clear(pool.connection.Auth)
		pool.connection = semanticmodel.Connection{}
		pool.healthStatement = ""
		go func() {
			for _, cancel := range active {
				cancel()
			}
			closeErr := closeTargetRuntimeSession(session)
			pool.activeWork.Wait()
			pool.mu.Lock()
			if pool.poisoned {
				closeErr = analyticsruntime.ErrConnectionCleanupFailed
			}
			pool.closeErr = closeErr
			pool.mu.Unlock()
			close(pool.closeDone)
		}()
	}
	done := pool.closeDone
	pool.mu.Unlock()
	// A caller's deadline bounds only its wait. Keep the final driver result
	// and completion signal available to concurrent callers and later retries.
	select {
	case <-done:
		return pool.closeErr
	default:
	}
	select {
	case <-done:
		return pool.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func closeTargetRuntimeSession(session TargetRuntimeSession) (closeErr error) {
	if isNilTargetRuntimeSession(session) {
		return nil
	}
	defer func() {
		if recover() != nil {
			closeErr = ErrTargetPoolCleanupFailed
		}
	}()
	return session.Close()
}

func cloneTargetConnection(connection semanticmodel.Connection) semanticmodel.Connection {
	connection.Auth = maps.Clone(connection.Auth)
	return connection
}

func isNilTargetRuntimeSession(session TargetRuntimeSession) bool {
	return typednil.IsNil(session)
}
