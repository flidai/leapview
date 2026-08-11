package duckdb

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/analytics/connectors"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func prepareRefreshSourceAccess(ctx context.Context, db analyticsresource.Session, model *semanticmodel.Model, attachedConnections map[string]struct{}) error {
	for _, name := range sortedKeys(model.Connections) {
		stmt, ok, err := compileConnectionSecret(name, model.Connections[name])
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("creating DuckDB secret for connection %s: %w", name, err)
		}
	}
	sourceSecrets, err := compileSourceSecretStatements(model)
	if err != nil {
		return err
	}
	for _, stmt := range sourceSecrets {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("creating DuckDB source secret: %w", err)
		}
	}
	if attachedConnections == nil {
		attachedConnections = map[string]struct{}{}
	}
	for _, sourceName := range sortedKeys(model.Sources) {
		source := model.Sources[sourceName]
		if source.Kind() != connectors.KindObject {
			continue
		}
		if _, ok := attachedConnections[source.Connection]; ok {
			continue
		}
		connection := model.Connections[source.Connection]
		connectionSpec, ok := connectors.LookupConnection(connection.Kind)
		if !ok {
			return fmt.Errorf("unsupported connection kind %q", connection.Kind)
		}
		if !connectionRequiresObjectAttach(connectionSpec) {
			continue
		}
		stmt, err := compileObjectAttach(model, source.Connection, connection)
		if err != nil {
			return err
		}
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("attaching source connection %s: %w", source.Connection, err)
		}
		attachedConnections[source.Connection] = struct{}{}
	}
	return nil
}

type sourcePlan struct {
	kind             string
	format           string
	path             string
	connection       string
	connectionConfig semanticmodel.Connection
	connectionSpec   connectors.ConnectionSpec
	object           string
	fields           []string
	columns          []sourceReadColumn
	rowPresenceOnly  bool
	options          map[string]any
}

type sourceReadPlan struct {
	Source          string
	Fields          []string
	Columns         []sourceReadColumn
	RowPresenceOnly bool
}

type sourceReadColumn struct {
	SourceField string
	OutputField string
}

func SourceRelation(model *semanticmodel.Model, source semanticmodel.Source) (string, error) {
	return SourceReadRelation(model, source, nil, nil, false)
}

func SourceReadRelation(model *semanticmodel.Model, source semanticmodel.Source, fields []string, columns []sourceReadColumn, rowPresenceOnly bool) (string, error) {
	plan, err := ResolveSourcePlan(model, source)
	if err != nil {
		return "", err
	}
	plan.fields = append([]string{}, fields...)
	plan.columns = append([]sourceReadColumn{}, columns...)
	plan.rowPresenceOnly = rowPresenceOnly
	return compileSourceRelation(plan)
}

func ResolveSourcePlan(model *semanticmodel.Model, source semanticmodel.Source) (sourcePlan, error) {
	plan := sourcePlan{
		kind:       source.Kind(),
		format:     source.Format,
		connection: source.Connection,
		object:     source.Object,
		options:    source.Options,
	}
	if connection, ok := model.Connections[source.Connection]; ok {
		plan.connectionConfig = connection
		if spec, ok := connectors.LookupConnection(connection.Kind); ok {
			plan.connectionSpec = spec
		}
	}
	if source.Path == "" {
		return plan, nil
	}
	path, err := ResolveSourcePath(model, source)
	if err != nil {
		return plan, err
	}
	plan.path = path
	return plan, nil
}

func ResolveSourcePath(model *semanticmodel.Model, source semanticmodel.Source) (string, error) {
	return analyticsmaterialize.ResolveSourcePath(model, source)
}

func compileSourceRelation(plan sourcePlan) (string, error) {
	adapter, err := sourceAdapterForPlan(plan)
	if err != nil {
		return "", err
	}
	return adapter.CompileRead(plan)
}

type sourceAdapter interface {
	CompileRead(sourcePlan) (string, error)
	Discover(ctx context.Context, db queryContext, model *semanticmodel.Model, source semanticmodel.Source) ([]semanticmodel.ColumnSchema, error)
}

type pathSourceAdapter struct{}

func (pathSourceAdapter) CompileRead(plan sourcePlan) (string, error) {
	format, ok := connectors.LookupFormat(plan.format)
	if !ok {
		return "", fmt.Errorf("unsupported source format %q", plan.format)
	}
	if !format.AllowsOptions && len(plan.options) > 0 {
		return "", fmt.Errorf("%s source cannot set options", plan.format)
	}
	switch format.ScanKind {
	case connectors.ScanTableFunction:
		source, err := scanRelationSource(format.ScanFunction, plan.path, plan.options)
		if err != nil {
			return "", err
		}
		return projectedRelation(source, plan.fields, plan.columns, plan.rowPresenceOnly)
	case connectors.ScanReplacement:
		return projectedRelation(replacementScanSource(plan.path), plan.fields, plan.columns, plan.rowPresenceOnly)
	default:
		return "", fmt.Errorf("unsupported source scan kind %q", format.ScanKind)
	}
}

type attachedObjectSourceAdapter struct{}

func (attachedObjectSourceAdapter) CompileRead(plan sourcePlan) (string, error) {
	object, err := qualifiedSQLName(plan.object)
	if err != nil {
		return "", err
	}
	alias, err := databaseAlias(plan.connection)
	if err != nil {
		return "", err
	}
	return projectedRelation(fmt.Sprintf("%s.%s", alias, object), plan.fields, plan.columns, plan.rowPresenceOnly)
}

type quackObjectSourceAdapter struct{}

func (quackObjectSourceAdapter) CompileRead(plan sourcePlan) (string, error) {
	object, err := qualifiedSQLName(plan.object)
	if err != nil {
		return "", err
	}
	uri, err := connectors.QuackURI(plan.connectionConfig.Host, plan.connectionConfig.Port)
	if err != nil {
		return "", err
	}
	remoteQuery := "SELECT * FROM " + object
	source := fmt.Sprintf("quack_query('%s', '%s')", sqlString(uri), sqlString(remoteQuery))
	return projectedRelation(source, plan.fields, plan.columns, plan.rowPresenceOnly)
}

func sourceAdapterForPlan(plan sourcePlan) (sourceAdapter, error) {
	switch plan.kind {
	case connectors.KindPath:
		return pathSourceAdapter{}, nil
	case connectors.KindObject:
		switch plan.connectionSpec.ObjectRelation {
		case connectors.ObjectRelationAttach:
			return attachedObjectSourceAdapter{}, nil
		case connectors.ObjectRelationQuackQuery:
			return quackObjectSourceAdapter{}, nil
		default:
			return nil, fmt.Errorf("unsupported object relation mode %q", plan.connectionSpec.ObjectRelation)
		}
	default:
		return nil, fmt.Errorf("unsupported source kind %q", plan.kind)
	}
}

func connectionRequiresObjectAttach(connection connectors.ConnectionSpec) bool {
	return connection.ObjectRelation == connectors.ObjectRelationAttach
}

func replacementScanRelation(path string) string {
	relation, _ := projectedRelation(replacementScanSource(path), nil, nil, false)
	return relation
}

func replacementScanSource(path string) string {
	return fmt.Sprintf("'%s'", sqlString(path))
}

func scanRelation(function, location string, options map[string]any) (string, error) {
	source, err := scanRelationSource(function, location, options)
	if err != nil {
		return "", err
	}
	return projectedRelation(source, nil, nil, false)
}

func scanRelationSource(function, location string, options map[string]any) (string, error) {
	optionSQL, err := sqlOptions(options)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s('%s'%s)", function, sqlString(location), optionSQL), nil
}

func projectedRelation(source string, fields []string, columns []sourceReadColumn, rowPresenceOnly bool) (string, error) {
	projection, err := projectionSQL(fields, columns, rowPresenceOnly)
	if err != nil {
		return "", err
	}
	return "SELECT " + projection + " FROM " + source, nil
}

func projectionSQL(fields []string, columns []sourceReadColumn, rowPresenceOnly bool) (string, error) {
	if rowPresenceOnly {
		return "1 AS " + rowPresenceColumn, nil
	}
	if len(fields) == 0 && len(columns) == 0 {
		return "*", nil
	}
	projected := make([]string, 0, len(fields)+len(columns))
	for _, field := range fields {
		sqlName, err := qualifiedSQLName(field)
		if err != nil {
			return "", err
		}
		if err := validateIdentifier(field); err != nil {
			return "", err
		}
		projected = append(projected, sqlName)
	}
	for _, column := range columns {
		sourceField := column.SourceField
		if sourceField == "" {
			sourceField = column.OutputField
		}
		if err := validateIdentifier(sourceField); err != nil {
			return "", err
		}
		outputField := column.OutputField
		if outputField == "" {
			outputField = sourceField
		}
		if err := validateIdentifier(outputField); err != nil {
			return "", err
		}
		sqlName, err := qualifiedSQLName(sourceField)
		if err != nil {
			return "", err
		}
		if sourceField == outputField {
			projected = append(projected, sqlName)
			continue
		}
		projected = append(projected, sqlName+" AS "+outputField)
	}
	return strings.Join(projected, ", "), nil
}

func sqlOptions(options map[string]any) (string, error) {
	if len(options) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(options))
	for key := range options {
		if err := validateIdentifier(key); err != nil {
			return "", fmt.Errorf("invalid source option %q: %w", key, err)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(", ")
		builder.WriteString(key)
		builder.WriteString(" = ")
		builder.WriteString(sqlLiteral(options[key]))
	}
	return builder.String(), nil
}

func sqlLiteral(value any) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case string:
		return "'" + sqlString(v) + "'"
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			values = append(values, sqlLiteral(item))
		}
		return "[" + strings.Join(values, ", ") + "]"
	case []string:
		values := make([]string, 0, len(v))
		for _, item := range v {
			values = append(values, sqlLiteral(item))
		}
		return "[" + strings.Join(values, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fields := make([]string, 0, len(keys))
		for _, key := range keys {
			fields = append(fields, "'"+sqlString(key)+"': "+sqlLiteral(v[key]))
		}
		return "{" + strings.Join(fields, ", ") + "}"
	default:
		return "'" + sqlString(fmt.Sprint(v)) + "'"
	}
}

func RequiredExtensions(model *semanticmodel.Model) []string {
	extensions := map[string]struct{}{}
	addConnection := func(kind string) {
		if connection, ok := connectors.LookupConnection(kind); ok {
			for _, extension := range connection.RequiredExtensions {
				extensions[extension] = struct{}{}
			}
		}
	}
	addPath := func(path string) {
		if extension, ok := connectors.StorageExtension(path); ok {
			extensions[extension] = struct{}{}
		}
	}
	for _, name := range sortedKeys(model.Connections) {
		connection := model.Connections[name]
		addConnection(connection.Kind)
		addPath(connection.Path)
		if dataPath, ok := connection.Options["data_path"]; ok {
			addPath(fmt.Sprint(dataPath))
		}
	}
	for _, name := range sortedKeys(model.Sources) {
		source := model.Sources[name]
		addPath(source.Path)
		switch source.Kind() {
		case connectors.KindPath:
			if format, ok := connectors.LookupFormat(source.Format); ok && format.RequiredExtension != "" {
				extensions[format.RequiredExtension] = struct{}{}
			}
		case connectors.KindObject:
			connection := model.Connections[source.Connection]
			addConnection(connection.Kind)
		}
	}
	return sortedKeys(extensions)
}

func duckDBConnectionType(kind string) string {
	if connection, ok := connectors.LookupConnection(kind); ok && connection.SecretType != "" {
		return connection.SecretType
	}
	return kind
}

func compileSourceSecretStatements(model *semanticmodel.Model) ([]string, error) {
	statements := map[string]string{}
	for _, sourceName := range sortedKeys(model.Sources) {
		source := model.Sources[sourceName]
		if source.Kind() != connectors.KindPath {
			continue
		}
		format, ok := connectors.LookupFormat(source.Format)
		if !ok || format.SourceSecretType == "" {
			continue
		}
		connection := model.Connections[source.Connection]
		if !semanticmodel.ConnectionCredentialsConfigured(connection) {
			continue
		}
		stmt, ok, err := compileTypedConnectionSecret(source.Connection+"_"+format.SourceSecretType, connection, format.SourceSecretType)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		statements[source.Connection] = stmt
	}
	result := make([]string, 0, len(statements))
	for _, name := range sortedKeys(statements) {
		result = append(result, statements[name])
	}
	return result, nil
}

func compileConnectionSecret(name string, connection semanticmodel.Connection) (string, bool, error) {
	connectionSpec, ok := connectors.LookupConnection(connection.Kind)
	if !ok || connectionSpec.SecretType == "" {
		return "", false, nil
	}
	if connectionSpec.AttachKind == connectors.AttachQuack {
		return compileQuackConnectionSecret(name, connection)
	}
	if connectionSpec.AttachKind == connectors.AttachDatabase {
		auth, err := semanticmodel.ResolveConnectionAuth(connection)
		if err != nil {
			return "", false, err
		}
		if _, ok := auth["connection_string"]; ok {
			return "", false, nil
		}
		if _, ok := auth["path"]; ok {
			return "", false, nil
		}
	}
	return compileTypedConnectionSecret(name, connection, connectionSpec.SecretType)
}

func compileQuackConnectionSecret(name string, connection semanticmodel.Connection) (string, bool, error) {
	auth, err := semanticmodel.ResolveConnectionAuth(connection)
	if err != nil {
		return "", false, err
	}
	token, ok := auth["token"]
	if !ok || strings.TrimSpace(fmt.Sprint(token)) == "" {
		return "", false, nil
	}
	if len(auth) != 1 {
		return "", false, fmt.Errorf("Quack connections accept only token auth")
	}
	secret, err := connectionSecretName(name)
	if err != nil {
		return "", false, err
	}
	uri, err := connectors.QuackURI(connection.Host, connection.Port)
	if err != nil {
		return "", false, err
	}
	return fmt.Sprintf(
		"CREATE OR REPLACE TEMPORARY SECRET %s (TYPE quack, TOKEN %s, SCOPE '%s')",
		secret, sqlLiteral(token), sqlString(uri),
	), true, nil
}

func compileTypedConnectionSecret(name string, connection semanticmodel.Connection, secretType string) (string, bool, error) {
	auth, err := semanticmodel.ResolveConnectionAuth(connection)
	if err != nil {
		return "", false, err
	}
	ambient := connection.Credentials.Provider == "ambient"
	if len(auth) == 0 && !ambient {
		return "", false, nil
	}
	secret, err := connectionSecretName(name)
	if err != nil {
		return "", false, err
	}
	parts := []string{"TYPE " + secretType}
	provider := duckDBSecretProvider(secretType, auth)
	if ambient {
		provider = "credential_chain"
	}
	parts = append(parts, "PROVIDER "+provider)
	connectionSpec, _ := connectors.LookupConnection(connection.Kind)
	if connectionSpec.AttachKind == connectors.AttachDatabase {
		parts = appendDatabaseSecretEndpoint(parts, connection)
	}
	for _, key := range sortedKeys(auth) {
		if err := validateIdentifier(key); err != nil {
			return "", false, fmt.Errorf("invalid auth param %q: %w", key, err)
		}
		parts = append(parts, duckDBAuthParameter(key)+" "+sqlLiteral(auth[key]))
	}
	if scope := duckDBSecretScope(secretType, connection); scope != "" {
		parts = append(parts, "SCOPE '"+sqlString(scope)+"'")
	}
	return fmt.Sprintf("CREATE OR REPLACE TEMPORARY SECRET %s (%s)", secret, strings.Join(parts, ", ")), true, nil
}

func appendDatabaseSecretEndpoint(parts []string, connection semanticmodel.Connection) []string {
	if connection.Host != "" {
		parts = append(parts, "HOST '"+sqlString(connection.Host)+"'")
	}
	if connection.Port > 0 {
		parts = append(parts, "PORT "+strconv.Itoa(connection.Port))
	}
	if connection.Database != "" {
		parts = append(parts, "DATABASE '"+sqlString(connection.Database)+"'")
	}
	if connection.Username != "" {
		parts = append(parts, "USER '"+sqlString(connection.Username)+"'")
	}
	if connection.SSLMode != "" {
		parts = append(parts, "SSLMODE '"+sqlString(connection.SSLMode)+"'")
	}
	return parts
}

func duckDBSecretScope(secretType string, connection semanticmodel.Connection) string {
	if connection.Scope != "" {
		return connection.Scope
	}
	return ""
}

func duckDBSecretProvider(secretType string, auth semanticmodel.ConnectionAuth) string {
	if secretType == "azure" {
		if _, hasTenant := auth["tenant_id"]; hasTenant {
			if _, hasClient := auth["client_id"]; hasClient {
				if _, hasSecret := auth["client_secret"]; hasSecret {
					return "service_principal"
				}
			}
		}
	}
	return "config"
}

func duckDBAuthParameter(key string) string {
	switch key {
	case "access_key_id":
		return "KEY_ID"
	case "secret_access_key":
		return "SECRET"
	case "account_name":
		return "ACCOUNT_NAME"
	case "account_id":
		return "ACCOUNT_ID"
	case "client_id":
		return "CLIENT_ID"
	case "client_secret":
		return "CLIENT_SECRET"
	case "connection_string":
		return "CONNECTION_STRING"
	case "session_token":
		return "SESSION_TOKEN"
	case "tenant_id":
		return "TENANT_ID"
	case "token":
		return "TOKEN"
	case "url_style":
		return "URL_STYLE"
	case "use_ssl":
		return "USE_SSL"
	default:
		return strings.ToUpper(key)
	}
}

func compileObjectAttach(model *semanticmodel.Model, connectionName string, connection semanticmodel.Connection) (string, error) {
	connectionSpec, ok := connectors.LookupConnection(connection.Kind)
	if !ok {
		return "", fmt.Errorf("unsupported connection kind %q", connection.Kind)
	}
	if connectionSpec.AttachKind == connectors.AttachDuckLake {
		return compileDuckLakeAttach(model, connectionName, connection)
	}
	if connectionSpec.AttachKind == "" {
		return "", nil
	}
	return compileDatabaseAttach(connectionName, connection)
}

func compileDatabaseAttach(connectionName string, connection semanticmodel.Connection) (string, error) {
	alias, err := databaseAlias(connectionName)
	if err != nil {
		return "", err
	}
	connectionString, err := connectionStringOption(connection)
	if err != nil {
		return "", err
	}
	parts := []string{"TYPE " + duckDBConnectionType(connection.Kind), "READ_ONLY"}
	if secret, ok, err := databaseAttachSecret(connectionName, connection); err != nil {
		return "", err
	} else if ok {
		parts = append(parts, "SECRET "+secret)
	}
	return fmt.Sprintf("ATTACH '%s' AS %s (%s)", sqlString(connectionString), alias, strings.Join(parts, ", ")), nil
}

func compileDuckLakeAttach(model *semanticmodel.Model, connectionName string, connection semanticmodel.Connection) (string, error) {
	alias, err := databaseAlias(connectionName)
	if err != nil {
		return "", err
	}
	path, err := resolveConnectionPath(model, connection)
	if err != nil {
		return "", err
	}
	attachPath := path
	if !strings.HasPrefix(attachPath, "ducklake:") {
		attachPath = "ducklake:" + attachPath
	}
	parts := []string{}
	if dataPath, ok := connection.Options["data_path"]; ok {
		resolved, err := resolvePathInConnectionScope(model, connection, fmt.Sprint(dataPath))
		if err != nil {
			return "", err
		}
		parts = append(parts, "DATA_PATH '"+sqlString(resolved)+"'")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("ATTACH '%s' AS %s", sqlString(attachPath), alias), nil
	}
	return fmt.Sprintf("ATTACH '%s' AS %s (%s)", sqlString(attachPath), alias, strings.Join(parts, ", ")), nil
}

func resolveConnectionPath(model *semanticmodel.Model, connection semanticmodel.Connection) (string, error) {
	return resolvePathInConnectionScope(model, connection, connection.Path)
}

func resolvePathInConnectionScope(_ *semanticmodel.Model, connection semanticmodel.Connection, path string) (string, error) {
	if connection.Scope != "" {
		if connectors.IsLocalPath(path) {
			return connectors.JoinScope(connection.Scope, path), nil
		}
		if !connectors.WithinScope(connection.Scope, path) {
			return "", fmt.Errorf("path %q is outside connection scope %q", path, connection.Scope)
		}
		return path, nil
	}
	return path, nil
}

func databaseAttachSecret(connectionName string, connection semanticmodel.Connection) (string, bool, error) {
	auth, err := semanticmodel.ResolveConnectionAuth(connection)
	if err != nil {
		return "", false, err
	}
	if len(auth) == 0 {
		return "", false, nil
	}
	if _, ok := auth["connection_string"]; ok {
		return "", false, nil
	}
	if _, ok := auth["path"]; ok {
		return "", false, nil
	}
	secret, err := connectionSecretName(connectionName)
	if err != nil {
		return "", false, err
	}
	return secret, true, nil
}

func connectionSecretName(name string) (string, error) {
	if err := validateIdentifier(name); err != nil {
		return "", fmt.Errorf("invalid connection %q: %w", name, err)
	}
	return "leapview_" + name, nil
}

func connectionStringOption(connection semanticmodel.Connection) (string, error) {
	for key := range connection.Options {
		connectionSpec, _ := connectors.LookupConnection(connection.Kind)
		if !connectionAllowsOption(connectionSpec, key) {
			return "", fmt.Errorf("unsupported database connection option %q", key)
		}
	}
	auth, err := semanticmodel.ResolveConnectionAuth(connection)
	if err != nil {
		return "", err
	}
	if len(auth) > 0 {
		if value, ok := auth["connection_string"]; ok {
			return fmt.Sprint(value), nil
		}
		if connection.Kind == "sqlite" {
			if value, ok := auth["path"]; ok {
				return fmt.Sprint(value), nil
			}
		}
		return "", nil
	}
	if connection.Kind == "sqlite" {
		if value, ok := connection.Options["path"]; ok {
			return fmt.Sprint(value), nil
		}
	}
	return "", nil
}

func connectionAllowsOption(connection connectors.ConnectionSpec, option string) bool {
	for _, allowed := range connection.AllowedOptions {
		if option == allowed {
			return true
		}
	}
	return false
}

func databaseAlias(connection string) (string, error) {
	if err := validateIdentifier(connection); err != nil {
		return "", fmt.Errorf("invalid database connection %q: %w", connection, err)
	}
	return "conn_" + connection, nil
}

func qualifiedSQLName(name string) (string, error) {
	parts := strings.Split(name, ".")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		if err := validateIdentifier(part); err != nil {
			return "", fmt.Errorf("invalid database object %q: %w", name, err)
		}
		quoted = append(quoted, part)
	}
	return strings.Join(quoted, "."), nil
}

func validateIdentifier(value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("invalid identifier %q", value)
	}
	return nil
}

func SQLString(path string) string {
	return sqlString(path)
}

func sqlString(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(path), "'", "''")
}

func sortedKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
