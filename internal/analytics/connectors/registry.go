package connectors

import (
	"net/url"
	pathpkg "path"
	"sort"
	"strings"
)

const (
	KindPath   = "path"
	KindObject = "object"

	ScanTableFunction = "table_function"
	ScanReplacement   = "replacement"

	AttachDatabase = "database"
	AttachDuckLake = "ducklake"
	AttachQuack    = "quack"

	ObjectRelationAttach     = "attach"
	ObjectRelationQuackQuery = "quack_query"
)

type ActivationMode string

const (
	// ManagedActivation resolves immutable data revisions through managed-data
	// bindings rather than through a secret-backed connection.
	ManagedActivation ActivationMode = "managed"
	// AuthoredActivation is limited to connectors whose production runtime can
	// safely use the compiled, non-secret logical configuration as-is.
	AuthoredActivation ActivationMode = "authored"
	// TargetBindingActivation requires target-owned endpoint configuration and
	// version-pinned credentials before candidate preparation can succeed.
	TargetBindingActivation ActivationMode = "target_binding"
)

type Format struct {
	Name              string
	Extensions        []string
	ScanKind          string
	ScanFunction      string
	RequiredExtension string
	AllowsOptions     bool
	SourceSecretType  string
	TableLike         bool
}

type ConnectionSpec struct {
	Kind               string
	ActivationMode     ActivationMode
	SecretType         string
	RequiredExtensions []string
	AllowsPathSource   bool
	AllowsObjectSource bool
	AllowsPath         bool
	RequiresPath       bool
	AllowedOptions     []string
	AuthKeys           []string
	RequiredAuthSets   [][]string
	AllowNoAuth        bool
	AttachKind         string
	ObjectRelation     string
}

var formats = map[string]Format{
	"csv": {
		Name:          "csv",
		Extensions:    []string{".csv", ".csv.gz"},
		ScanKind:      ScanTableFunction,
		ScanFunction:  "read_csv",
		AllowsOptions: true,
	},
	"json": {
		Name:          "json",
		Extensions:    []string{".json", ".jsonl", ".ndjson"},
		ScanKind:      ScanTableFunction,
		ScanFunction:  "read_json",
		AllowsOptions: true,
	},
	"parquet": {
		Name:          "parquet",
		Extensions:    []string{".parquet"},
		ScanKind:      ScanTableFunction,
		ScanFunction:  "read_parquet",
		AllowsOptions: true,
	},
	"excel": {
		Name:              "excel",
		Extensions:        []string{".xlsx"},
		ScanKind:          ScanTableFunction,
		ScanFunction:      "read_xlsx",
		RequiredExtension: "excel",
		AllowsOptions:     true,
	},
	"text": {
		Name:          "text",
		Extensions:    []string{".txt"},
		ScanKind:      ScanTableFunction,
		ScanFunction:  "read_text",
		AllowsOptions: true,
	},
	"blob": {
		Name:          "blob",
		Extensions:    []string{".blob"},
		ScanKind:      ScanTableFunction,
		ScanFunction:  "read_blob",
		AllowsOptions: true,
	},
	"vortex": {
		Name:              "vortex",
		Extensions:        []string{".vortex"},
		ScanKind:          ScanTableFunction,
		ScanFunction:      "read_vortex",
		RequiredExtension: "vortex",
		AllowsOptions:     true,
	},
	"delta": {
		Name:              "delta",
		ScanKind:          ScanTableFunction,
		ScanFunction:      "delta_scan",
		RequiredExtension: "delta",
		AllowsOptions:     true,
		TableLike:         true,
	},
	"iceberg": {
		Name:              "iceberg",
		ScanKind:          ScanTableFunction,
		ScanFunction:      "iceberg_scan",
		RequiredExtension: "iceberg",
		AllowsOptions:     true,
		TableLike:         true,
	},
	"lance": {
		Name:              "lance",
		Extensions:        []string{".lance"},
		ScanKind:          ScanReplacement,
		RequiredExtension: "lance",
		SourceSecretType:  "lance",
		TableLike:         true,
	},
}

var connections = map[string]ConnectionSpec{
	"managed": {
		Kind:             "managed",
		ActivationMode:   ManagedActivation,
		AllowsPathSource: true,
		AllowNoAuth:      true,
	},
	"s3": {
		Kind:               "s3",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "s3",
		RequiredExtensions: []string{"httpfs"},
		AllowsPathSource:   true,
		AuthKeys:           []string{"access_key_id", "secret_access_key", "session_token", "region", "endpoint", "url_style", "use_ssl"},
		RequiredAuthSets:   [][]string{{"access_key_id", "secret_access_key"}},
	},
	"r2": {
		Kind:               "r2",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "r2",
		RequiredExtensions: []string{"httpfs"},
		AllowsPathSource:   true,
		AuthKeys:           []string{"access_key_id", "secret_access_key", "account_id", "region"},
		RequiredAuthSets:   [][]string{{"access_key_id", "secret_access_key", "account_id"}},
	},
	"gcs": {
		Kind:               "gcs",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "gcs",
		RequiredExtensions: []string{"httpfs"},
		AllowsPathSource:   true,
		AuthKeys:           []string{"access_key_id", "secret_access_key", "endpoint"},
		RequiredAuthSets:   [][]string{{"access_key_id", "secret_access_key"}},
	},
	"http": {
		Kind:               "http",
		ActivationMode:     AuthoredActivation,
		SecretType:         "http",
		RequiredExtensions: []string{"httpfs"},
		AllowsPathSource:   true,
		AllowNoAuth:        true,
	},
	"azure_blob": {
		Kind:               "azure_blob",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "azure",
		RequiredExtensions: []string{"azure"},
		AllowsPathSource:   true,
		AuthKeys:           []string{"connection_string", "account_name", "tenant_id", "client_id", "client_secret"},
		RequiredAuthSets:   [][]string{{"connection_string"}, {"account_name", "tenant_id", "client_id", "client_secret"}},
	},
	"postgres": {
		Kind:               "postgres",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "postgres",
		RequiredExtensions: []string{"postgres"},
		AllowsObjectSource: true,
		AuthKeys:           []string{"connection_string", "password"},
		RequiredAuthSets:   [][]string{{"connection_string"}, {"password"}},
		AttachKind:         AttachDatabase,
		ObjectRelation:     ObjectRelationAttach,
	},
	"mysql": {
		Kind:               "mysql",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "mysql",
		RequiredExtensions: []string{"mysql"},
		AllowsObjectSource: true,
		AuthKeys:           []string{"connection_string", "password"},
		RequiredAuthSets:   [][]string{{"connection_string"}, {"password"}},
		AttachKind:         AttachDatabase,
		ObjectRelation:     ObjectRelationAttach,
	},
	"sqlite": {
		Kind:               "sqlite",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "sqlite",
		RequiredExtensions: []string{"sqlite"},
		AllowsObjectSource: true,
		AllowedOptions:     []string{"path"},
		AuthKeys:           []string{"path"},
		RequiredAuthSets:   [][]string{{"path"}},
		AllowNoAuth:        true,
		AttachKind:         AttachDatabase,
		ObjectRelation:     ObjectRelationAttach,
	},
	"ducklake": {
		Kind:               "ducklake",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "ducklake",
		RequiredExtensions: []string{"ducklake"},
		AllowsObjectSource: true,
		AllowsPath:         true,
		RequiresPath:       true,
		AllowedOptions:     []string{"data_path"},
		AuthKeys:           []string{"access_key_id", "secret_access_key", "session_token", "region", "endpoint", "url_style", "use_ssl", "account_id", "connection_string", "account_name", "tenant_id", "client_id", "client_secret"},
		RequiredAuthSets:   [][]string{{"access_key_id", "secret_access_key"}, {"connection_string"}, {"account_name", "tenant_id", "client_id", "client_secret"}},
		AllowNoAuth:        true,
		AttachKind:         AttachDuckLake,
		ObjectRelation:     ObjectRelationAttach,
	},
	"quack": {
		Kind:               "quack",
		ActivationMode:     TargetBindingActivation,
		SecretType:         "quack",
		RequiredExtensions: []string{"httpfs", "quack"},
		AllowsObjectSource: true,
		AuthKeys:           []string{"token"},
		RequiredAuthSets:   [][]string{{"token"}},
		AttachKind:         AttachQuack,
		ObjectRelation:     ObjectRelationQuackQuery,
	},
}

func LookupFormat(name string) (Format, bool) {
	spec, ok := formats[name]
	return spec, ok
}

func LookupConnection(kind string) (ConnectionSpec, bool) {
	spec, ok := connections[kind]
	return spec, ok
}

func FormatNames() []string {
	return sortedKeys(formats)
}

func ConnectionKinds() []string {
	return sortedKeys(connections)
}

func InferFormat(path string) (string, bool) {
	lower := strings.ToLower(path)
	for _, format := range FormatNames() {
		spec := formats[format]
		for _, ext := range spec.Extensions {
			if strings.HasSuffix(lower, ext) {
				return spec.Name, true
			}
		}
	}
	return "", false
}

func IsLocalPath(path string) bool {
	for _, prefix := range []string{"s3://", "r2://", "gcs://", "gs://", "az://", "azure://", "abfss://", "http://", "https://", "file://"} {
		if strings.HasPrefix(path, prefix) {
			return false
		}
	}
	return !strings.Contains(path, "://")
}

func JoinScope(scope, path string) string {
	return strings.TrimRight(scope, "/") + "/" + strings.TrimLeft(path, "/")
}

func WithinScope(scope, path string) bool {
	scopeURL, scopeErr := url.Parse(scope)
	pathURL, pathErr := url.Parse(path)
	if scopeErr == nil && pathErr == nil && (scopeURL.Scheme != "" || pathURL.Scheme != "") {
		if scopeURL.Scheme == "" || pathURL.Scheme == "" || !strings.EqualFold(scopeURL.Scheme, pathURL.Scheme) || !strings.EqualFold(scopeURL.Host, pathURL.Host) {
			return false
		}
		scopePath := cleanRemotePath(scopeURL.Path)
		candidatePath := cleanRemotePath(pathURL.Path)
		if scopePath == "/" {
			return true
		}
		return candidatePath == scopePath || strings.HasPrefix(candidatePath, scopePath+"/")
	}
	scope = strings.TrimRight(scope, "/")
	path = strings.TrimRight(path, "/")
	return path == scope || strings.HasPrefix(path, scope+"/")
}

func cleanRemotePath(value string) string {
	if value == "" {
		return "/"
	}
	return pathpkg.Clean("/" + strings.TrimLeft(value, "/"))
}

func StorageExtension(path string) (string, bool) {
	switch {
	case strings.HasPrefix(path, "s3://"), strings.HasPrefix(path, "r2://"), strings.HasPrefix(path, "gcs://"), strings.HasPrefix(path, "gs://"), strings.HasPrefix(path, "http://"), strings.HasPrefix(path, "https://"):
		return "httpfs", true
	case strings.HasPrefix(path, "az://"), strings.HasPrefix(path, "azure://"), strings.HasPrefix(path, "abfss://"):
		return "azure", true
	default:
		return "", false
	}
}

func sortedKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
