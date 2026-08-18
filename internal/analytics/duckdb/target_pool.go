package duckdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"

	duckdbdriver "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
)

type TargetRuntimeSession interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	Close() error
}

type TargetRuntimeSessionOpener func(context.Context) (TargetRuntimeSession, error)

func NewIsolatedTargetRuntimeOpener() TargetRuntimeSessionOpener {
	return func(ctx context.Context) (TargetRuntimeSession, error) {
		connector, err := duckdbdriver.NewConnector(":memory:", func(driver.ExecerContext) error {
			return nil
		})
		if err != nil {
			return nil, err
		}
		database := sql.OpenDB(connector)
		database.SetMaxOpenConns(1)
		database.SetMaxIdleConns(1)
		connection, err := database.Conn(ctx)
		if err != nil {
			_ = database.Close()
			return nil, err
		}
		return &isolatedTargetRuntimeSession{connection: connection, database: database}, nil
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
}

type TargetRuntimePoolFactory struct {
	open               TargetRuntimeSessionOpener
	limits             TargetRuntimeLimits
	requireTLS         bool
	extensionAdmission ExtensionAdmission
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
	}, nil
}

func (factory *TargetRuntimePoolFactory) Prepare(
	ctx context.Context,
	binding connectionbinding.TargetBinding,
	snapshot connectionbinding.CredentialSnapshot,
) (connectionbinding.RuntimePool, error) {
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
	connection, err := ApplyTargetBinding(
		logical,
		binding,
		snapshot,
	)
	if err != nil {
		return nil, err
	}
	defer clear(connection.Auth)

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
		return nil, err
	}
	closeOnFailure := true
	defer func() {
		if closeOnFailure {
			_ = session.Close()
		}
	}()
	statements := []string{
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET memory_limit = '" + strconv.FormatInt(factory.limits.MemoryMaxBytes, 10) + "B'",
		"SET max_temp_directory_size = '" + strconv.FormatInt(factory.limits.TempMaxBytes, 10) + "B'",
		"SET threads = " + strconv.Itoa(factory.limits.MaxThreads),
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
	statements = append(statements, "SET lock_configuration = true")
	for _, statement := range statements {
		if _, err := session.ExecContext(ctx, statement); err != nil {
			return nil, err
		}
	}
	closeOnFailure = false
	return &targetRuntimePool{
		session: session, connection: cloneTargetConnection(connection), healthStatement: healthStatement,
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
	mu              sync.Mutex
	session         TargetRuntimeSession
	connection      semanticmodel.Connection
	healthStatement string
}

var _ analyticsruntime.ConnectionResolver = (*targetRuntimePool)(nil)

type isolatedTargetRuntimeSession struct {
	connection *sql.Conn
	database   *sql.DB
	once       sync.Once
	err        error
}

func (session *isolatedTargetRuntimeSession) ExecContext(
	ctx context.Context,
	statement string,
	args ...any,
) (sql.Result, error) {
	if session == nil || session.connection == nil {
		return nil, connectionbinding.ErrProviderUnavailable
	}
	return session.connection.ExecContext(ctx, statement, args...)
}

func (session *isolatedTargetRuntimeSession) Close() error {
	if session == nil {
		return nil
	}
	session.once.Do(func() {
		var errs []error
		if session.connection != nil {
			errs = append(errs, session.connection.Close())
			session.connection = nil
		}
		if session.database != nil {
			errs = append(errs, session.database.Close())
			session.database = nil
		}
		session.err = errors.Join(errs...)
	})
	return session.err
}

func (pool *targetRuntimePool) HealthCheck(ctx context.Context) error {
	if pool == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.session == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	statement := pool.healthStatement
	if statement == "" {
		statement = "SELECT 1"
	}
	_, err := pool.session.ExecContext(ctx, statement)
	return err
}

func (pool *targetRuntimePool) Resolve(
	ctx context.Context,
	name string,
	logical semanticmodel.Connection,
) (semanticmodel.Connection, error) {
	if err := ctx.Err(); err != nil {
		return semanticmodel.Connection{}, err
	}
	if pool == nil {
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.session == nil || pool.connection.Kind == "" {
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	if strings.TrimSpace(logical.Kind) != pool.connection.Kind {
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	}
	resolved := logical
	resolved.Host = pool.connection.Host
	resolved.Port = pool.connection.Port
	resolved.Database = pool.connection.Database
	resolved.Username = pool.connection.Username
	resolved.SSLMode = pool.connection.SSLMode
	resolved.Scope = pool.connection.Scope
	resolved.Credentials = semanticmodel.ConnectionCredentials{}
	resolved.Options = maps.Clone(logical.Options)
	if resolved.Options == nil && len(pool.connection.Options) > 0 {
		resolved.Options = make(map[string]any, len(pool.connection.Options))
	}
	for key, value := range pool.connection.Options {
		resolved.Options[key] = value
	}
	resolved.Auth = maps.Clone(pool.connection.Auth)
	validated, err := resolved.Validate(strings.TrimSpace(name))
	if err != nil {
		clear(resolved.Auth)
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	}
	return validated, nil
}

func (pool *targetRuntimePool) Close() error {
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	session := pool.session
	pool.session = nil
	clear(pool.connection.Auth)
	pool.connection = semanticmodel.Connection{}
	pool.healthStatement = ""
	pool.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

func cloneTargetConnection(connection semanticmodel.Connection) semanticmodel.Connection {
	connection.Auth = maps.Clone(connection.Auth)
	connection.Options = maps.Clone(connection.Options)
	connection.Defaults.Options = maps.Clone(connection.Defaults.Options)
	if connection.ReaderDefaults != nil {
		connection.ReaderDefaults = make(map[string]map[string]any, len(connection.ReaderDefaults))
		for format, options := range connection.ReaderDefaults {
			connection.ReaderDefaults[format] = maps.Clone(options)
		}
	}
	return connection
}
