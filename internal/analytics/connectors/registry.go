package connectors

import (
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
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

// SourceDataIdentityCapability describes the authoritative source-content
// equivalence evidence a connector can supply for result reuse.
type SourceDataIdentityCapability = projectcontracts.SourceDataIdentityCapability

const (
	SourceDataIdentityUnavailable     = projectcontracts.SourceDataIdentityUnavailable
	SourceDataIdentityContentRevision = projectcontracts.SourceDataIdentityContentRevision
)

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
	Name                         string
	Extensions                   []string
	ScanKind                     string
	ScanFunction                 string
	RequiredExtension            string
	AllowsOptions                bool
	SourceSecretType             string
	SourceDataIdentityCapability SourceDataIdentityCapability
	TableLike                    bool
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
	// AllowPublicAccess indicates that the portable `access: public` policy
	// can be lowered to a target binding with no credential snapshot.
	AllowPublicAccess bool
	// SourceDataIdentityCapability states which authoritative source-content
	// equivalence evidence the connector can project. Unavailable connectors
	// must never synthesize an identity from configuration or runtime state.
	SourceDataIdentityCapability SourceDataIdentityCapability
	AttachKind                   string
	ObjectRelation               string
}

var connections = map[string]ConnectionSpec{
	"managed": {
		AllowNoAuth: true,
	},
	"s3": {
		AuthKeys:         []string{"access_key_id", "secret_access_key", "session_token", "region", "endpoint", "url_style", "use_ssl"},
		RequiredAuthSets: [][]string{{"access_key_id", "secret_access_key"}},
	},
	"r2": {
		AuthKeys:         []string{"access_key_id", "secret_access_key", "account_id", "region"},
		RequiredAuthSets: [][]string{{"access_key_id", "secret_access_key", "account_id"}},
	},
	"gcs": {
		AuthKeys:         []string{"access_key_id", "secret_access_key", "endpoint"},
		RequiredAuthSets: [][]string{{"access_key_id", "secret_access_key"}},
	},
	"http": {
		AllowNoAuth: true,
	},
	"azure_blob": {
		AuthKeys:         []string{"connection_string", "account_name", "tenant_id", "client_id", "client_secret"},
		RequiredAuthSets: [][]string{{"connection_string"}, {"account_name", "tenant_id", "client_id", "client_secret"}},
	},
	"postgres": {
		AuthKeys:         []string{"connection_string", "password"},
		RequiredAuthSets: [][]string{{"connection_string"}, {"password"}},
		AttachKind:       AttachDatabase,
		ObjectRelation:   ObjectRelationAttach,
	},
	"mysql": {
		AuthKeys:         []string{"connection_string", "password"},
		RequiredAuthSets: [][]string{{"connection_string"}, {"password"}},
		AttachKind:       AttachDatabase,
		ObjectRelation:   ObjectRelationAttach,
	},
	"sqlite": {
		AllowedOptions:   []string{"path"},
		AuthKeys:         []string{"path"},
		RequiredAuthSets: [][]string{{"path"}},
		AllowNoAuth:      true,
		AttachKind:       AttachDatabase,
		ObjectRelation:   ObjectRelationAttach,
	},
	"ducklake": {
		AllowsPath:       true,
		RequiresPath:     true,
		AllowedOptions:   []string{"data_path"},
		AuthKeys:         []string{"access_key_id", "secret_access_key", "session_token", "region", "endpoint", "url_style", "use_ssl", "account_id", "connection_string", "account_name", "tenant_id", "client_id", "client_secret"},
		RequiredAuthSets: [][]string{{"access_key_id", "secret_access_key"}, {"connection_string"}, {"account_name", "tenant_id", "client_id", "client_secret"}},
		AllowNoAuth:      true,
		AttachKind:       AttachDuckLake,
		ObjectRelation:   ObjectRelationAttach,
	},
	"quack": {
		AuthKeys:         []string{"token"},
		RequiredAuthSets: [][]string{{"token"}},
		AttachKind:       AttachQuack,
		ObjectRelation:   ObjectRelationQuackQuery,
	},
}

func LookupFormat(name string) (Format, bool) {
	if !generatedFormatName(name) {
		return Format{}, false
	}
	for _, profile := range projectcontracts.FormatRegistry {
		if profile.Name != name {
			continue
		}
		return Format{
			Name: profile.Name, Extensions: append([]string(nil), profile.Extensions...),
			ScanKind: profile.ScanKind, ScanFunction: profile.ScanFunction,
			RequiredExtension: profile.RequiredExtension, AllowsOptions: profile.AllowsOptions,
			SourceSecretType:             profile.SourceSecretType,
			SourceDataIdentityCapability: profile.SourceDataIdentityCapability,
			TableLike:                    profile.TableLike,
		}, true
	}
	return Format{}, false
}

func LookupConnection(kind string) (ConnectionSpec, bool) {
	profile, profileOK := projectcontracts.LookupConnector(kind)
	if !profileOK {
		return ConnectionSpec{}, false
	}
	// The generated profile's public key is the lookup identity. Its AdapterKey
	// is the private implementation selector, so these names are intentionally
	// allowed to diverge as the public contract evolves.
	spec, ok := connections[profile.AdapterKey]
	if !ok {
		return ConnectionSpec{}, false
	}
	// Generated APIGen metadata is authoritative for public connector identity,
	// activation, location capabilities, extensions, and access policy. The
	// hand-maintained values above are implementation details (auth key sets,
	// attach/query strategy) keyed by this generated AdapterKey.
	spec.Kind = profile.Key
	spec.ActivationMode = ActivationMode(profile.ActivationMode)
	spec.SecretType = profile.SecretType
	spec.RequiredExtensions = append([]string(nil), profile.ApprovedExtensions...)
	spec.AllowsPathSource = generatedContains(profile.LocationCapabilities, KindPath)
	// TypeSpec calls object locations "relation"; runtime planners use the
	// object terminology for the same capability.
	spec.AllowsObjectSource = generatedContains(profile.LocationCapabilities, KindObject) || generatedContains(profile.LocationCapabilities, "relation")
	spec.AllowPublicAccess = profile.AllowPublicAccess
	spec.SourceDataIdentityCapability = profile.SourceDataIdentityCapability
	return spec, ok
}

func FormatNames() []string {
	return append([]string(nil), projectcontracts.PathFormatNames...)
}

func ConnectionKinds() []string {
	keys := make([]string, 0, len(projectcontracts.ConnectorRegistry))
	for _, profile := range projectcontracts.ConnectorRegistry {
		keys = append(keys, profile.Key)
	}
	sort.Strings(keys)
	return keys
}

func InferFormat(path string) (string, bool) {
	lower := strings.ToLower(path)
	for _, format := range FormatNames() {
		spec, ok := LookupFormat(format)
		if !ok {
			continue
		}
		for _, ext := range spec.Extensions {
			if strings.HasSuffix(lower, ext) {
				return spec.Name, true
			}
		}
	}
	return "", false
}

func generatedFormatName(name string) bool {
	for _, format := range projectcontracts.PathFormatNames {
		if format == name {
			return true
		}
	}
	return false
}

func generatedContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
