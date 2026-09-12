// Package developmentprofile validates the local analytics connection-profile
// contract. It resolves human-readable names against an already compiled graph
// and emits target-binding inputs; it never applies bindings or reads secrets.
package developmentprofile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/connectionadmin"
	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"gopkg.in/yaml.v3"
)

const (
	Version             = 1
	DefaultProfileName  = "local"
	DefaultRelativePath = ".leapview/profiles.local.yaml"
	MaxDocumentBytes    = 1 << 20
	maxYAMLDepth        = 32
	maxYAMLNodes        = 32768
)

var (
	profileNamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
	environmentPattern   = regexp.MustCompile(`^LEAPVIEW_DEV_CONNECTION_[A-Z0-9_]+$`)
	shellVariablePattern = regexp.MustCompile(`(^|[^\\])\$[A-Za-z_][A-Za-z0-9_]*`)
	templateMarkers      = []string{"{{", "{%", "{#", "${", "$(", "`"}
)

// LogicalConnection is the graph-owned identity used to resolve a profile
// entry. Name matching is exact and case-sensitive.
type LogicalConnection struct {
	ID            projectgraph.ResourceID
	ConnectorKind string
	Access        semanticmodel.ConnectionAccess
}

// CatalogFromManifest projects the compiler's exact name index and logical
// connector contracts. It never derives a graph identity from a name.
func CatalogFromManifest(manifest projectmanifest.ResourceManifest) (map[string]LogicalConnection, error) {
	result := make(map[string]LogicalConnection, len(manifest.NameIndex.Connections))
	indexed := make(map[string]struct{}, len(manifest.NameIndex.Connections))
	for name, rawID := range manifest.NameIndex.Connections {
		if name == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("compiled connection name is invalid")
		}
		id, err := projectgraph.NewResourceID(rawID)
		if err != nil {
			return nil, fmt.Errorf("compiled connection %q has an invalid graph identity", name)
		}
		logical, exists := manifest.Connections[rawID]
		if !exists {
			return nil, fmt.Errorf("compiled connection %q resolves to a missing graph identity", name)
		}
		if _, duplicate := indexed[rawID]; duplicate {
			return nil, fmt.Errorf("compiled connection graph identity is indexed more than once")
		}
		indexed[rawID] = struct{}{}
		result[name] = LogicalConnection{ID: id, ConnectorKind: logical.Kind, Access: logical.Access}
	}
	if len(indexed) != len(manifest.Connections) {
		return nil, fmt.Errorf("compiled connection name index is incomplete")
	}
	return result, nil
}

// LoadOptions are local-only selection inputs. ProfileFile is explicit when
// non-empty and requires a verified SourceRoot; otherwise the checkout-scoped
// default is selected. RemoteTarget exists solely to reject mixing local
// profile flags with remote development.
type LoadOptions struct {
	CheckoutRoot        string
	InvocationDirectory string
	SourceRoot          string
	ProfileFile         string
	ProfileName         string
	RemoteTarget        string
	Connections         map[string]LogicalConnection
}

// CredentialMode is a validated, non-secret credential selection.
type CredentialMode struct {
	EnvironmentVariable string
	None                bool
}

// Connection is one exact graph connection resolved from the selected profile.
type Connection struct {
	Name          string
	ID            projectgraph.ResourceID
	ConnectorKind string
	Endpoint      connectionadmin.EndpointConfig
	Credentials   CredentialMode
}

// Selected is the deterministic result of selecting one file and one profile.
// Connections is sorted by exact logical name.
type Selected struct {
	File        string
	ProfileName string
	Connections []Connection
}

// DiagnosticError is deliberately metadata-only: invalid scalar values are
// never copied into errors because they may contain credentials.
type DiagnosticError struct {
	File    string
	Field   string
	Code    string
	Message string
	Line    int
	Column  int
}

func (err *DiagnosticError) Error() string {
	location := err.File
	if err.Line > 0 {
		location += ":" + strconv.Itoa(err.Line)
		if err.Column > 0 {
			location += ":" + strconv.Itoa(err.Column)
		}
	}
	field := ""
	if err.Field != "" {
		field = " [" + err.Field + "]"
	}
	return fmt.Sprintf("%s: %s%s: %s", location, err.Code, field, err.Message)
}

func diagnostic(file, field, code, message string, node *yaml.Node) error {
	err := &DiagnosticError{File: file, Field: field, Code: code, Message: message}
	if node != nil {
		err.Line, err.Column = node.Line, node.Column
	}
	return err
}

// Load selects and validates one local profile document. It performs no
// network access, credential lookup, binding mutation, or environment scan.
func Load(options LoadOptions) (Selected, error) {
	file, profileName, explicit, err := resolveSelection(options)
	if err != nil {
		return Selected{}, err
	}
	content, canonicalFile, err := readProfileFile(file, options, explicit)
	if err != nil {
		var profileError *DiagnosticError
		if errors.As(err, &profileError) {
			return Selected{}, err
		}
		var sizeError *documentSizeError
		if errors.As(err, &sizeError) {
			return Selected{}, diagnostic(file, "", "profile.size", "profile document exceeds the supported size", nil)
		}
		if !explicit && errors.Is(err, os.ErrNotExist) && !requiresProfile(options.Connections) {
			return Selected{File: file, ProfileName: profileName, Connections: []Connection{}}, nil
		}
		if !explicit && errors.Is(err, os.ErrNotExist) {
			return Selected{}, diagnostic(file, "", "profile.setup_required", "external connections require a local profile; run guided setup", nil)
		}
		return Selected{}, diagnostic(file, "", "profile.file", "selected profile file cannot be read", nil)
	}
	file = canonicalFile
	if !utf8.Valid(content) {
		return Selected{}, diagnostic(file, "", "profile.encoding", "profile document must be valid UTF-8", nil)
	}
	root, err := parseDocument(file, content)
	if err != nil {
		return Selected{}, err
	}
	selected, err := resolveDocument(file, profileName, root, options.Connections)
	if err != nil {
		return Selected{}, err
	}
	selected.File = file
	selected.ProfileName = profileName
	return selected, nil
}

func resolveSelection(options LoadOptions) (string, string, bool, error) {
	checkoutRoot, err := canonicalDirectory(options.CheckoutRoot)
	if err != nil {
		return "", "", false, diagnostic(options.CheckoutRoot, "", "profile.checkout", "selected checkout root is unavailable", nil)
	}
	invocation := options.InvocationDirectory
	if strings.TrimSpace(invocation) == "" {
		invocation = checkoutRoot
	}
	invocation, err = canonicalDirectory(invocation)
	if err != nil {
		return "", "", false, diagnostic(invocation, "", "profile.invocation", "invocation directory is unavailable", nil)
	}
	profileName := options.ProfileName
	if profileName == "" {
		profileName = DefaultProfileName
	}
	if profileName != strings.TrimSpace(profileName) || !profileNamePattern.MatchString(profileName) {
		return "", "", false, diagnostic("profile selection", "profile", "profile.name", "selected profile name is invalid", nil)
	}
	explicit := options.ProfileFile != ""
	if strings.TrimSpace(options.RemoteTarget) != "" && (explicit || options.ProfileName != "") {
		return "", "", false, diagnostic("profile selection", "", "profile.remote_conflict", "local profile flags cannot be combined with remote development", nil)
	}
	if explicit && strings.TrimSpace(options.SourceRoot) == "" {
		return "", "", false, diagnostic("profile selection", "", "profile.source", "portable analytics source root is required for an explicit profile", nil)
	}
	file := filepath.Join(checkoutRoot, DefaultRelativePath)
	if explicit {
		file = options.ProfileFile
		if !filepath.IsAbs(file) {
			file = filepath.Join(invocation, file)
		}
	}
	file, err = filepath.Abs(file)
	if err != nil {
		return "", "", false, diagnostic(file, "", "profile.path", "selected profile path is invalid", nil)
	}
	file = filepath.Clean(file)
	if sourceRoot := strings.TrimSpace(options.SourceRoot); sourceRoot != "" {
		if !filepath.IsAbs(sourceRoot) {
			sourceRoot = filepath.Join(checkoutRoot, sourceRoot)
		}
		sourceRoot, err = filepath.Abs(sourceRoot)
		if err != nil {
			return "", "", false, diagnostic(file, "", "profile.source", "portable analytics source root is invalid", nil)
		}
		if pathWithin(filepath.Clean(sourceRoot), file) {
			return "", "", false, diagnostic(file, "", "profile.portable_source", "profile files cannot be selected from inside the portable analytics source root", nil)
		}
		if _, sourceErr := canonicalDirectory(sourceRoot); sourceErr != nil {
			return "", "", false, diagnostic(file, "", "profile.source", "portable analytics source root cannot be verified", nil)
		}
	}
	return file, profileName, explicit, nil
}

func canonicalDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("directory is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return canonical, nil
}

func readProfileFile(path string, options LoadOptions, explicit bool) ([]byte, string, error) {
	canonical, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return nil, "", err
	}
	checkoutRoot, err := canonicalDirectory(options.CheckoutRoot)
	if err != nil {
		return nil, "", err
	}
	if !explicit && !pathWithin(checkoutRoot, canonical) {
		return nil, "", diagnostic(path, "", "profile.checkout_escape", "default profile file must remain inside the selected checkout", nil)
	}
	if sourceRoot := strings.TrimSpace(options.SourceRoot); sourceRoot != "" {
		if !filepath.IsAbs(sourceRoot) {
			sourceRoot = filepath.Join(checkoutRoot, sourceRoot)
		}
		canonicalSource, sourceErr := filepath.EvalSymlinks(sourceRoot)
		if sourceErr != nil && (explicit || !errors.Is(sourceErr, os.ErrNotExist)) {
			return nil, "", diagnostic(path, "", "profile.source", "portable analytics source root cannot be verified", nil)
		}
		if sourceErr == nil && pathWithin(canonicalSource, canonical) {
			return nil, "", diagnostic(path, "", "profile.portable_source", "profile files cannot be selected from inside the portable analytics source root", nil)
		}
	}
	initialInfo, err := os.Stat(canonical)
	if err != nil {
		return nil, "", err
	}
	if !initialInfo.Mode().IsRegular() {
		return nil, "", diagnostic(path, "", "profile.file_type", "profile path must identify a regular file", nil)
	}
	root, err := os.OpenRoot(filepath.Dir(canonical))
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(canonical))
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(initialInfo, info) {
		return nil, "", diagnostic(path, "", "profile.path_changed", "profile file changed while its path was being validated", nil)
	}
	if info.Size() > MaxDocumentBytes {
		return nil, "", &documentSizeError{}
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(content) > MaxDocumentBytes {
		return nil, "", &documentSizeError{}
	}
	return content, canonical, nil
}

type documentSizeError struct{}

func (*documentSizeError) Error() string { return "profile document exceeds the supported size" }

func pathWithin(root, target string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, target)
	return err == nil && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func requiresProfile(connections map[string]LogicalConnection) bool {
	for _, connection := range connections {
		spec, ok := connectors.LookupConnection(connection.ConnectorKind)
		if !ok || spec.ActivationMode == connectors.TargetBindingActivation {
			return true
		}
	}
	return false
}

func parseDocument(file string, content []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, diagnostic(file, "", "profile.yaml", "profile document is not valid YAML", nil)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		var node *yaml.Node
		if len(document.Content) > 0 {
			node = document.Content[0]
		}
		return nil, diagnostic(file, "", "profile.document", "profile document must contain one mapping", node)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err == nil {
		return nil, diagnostic(file, "", "profile.document", "multiple YAML documents are not supported", &extra)
	} else if !errors.Is(err, io.EOF) {
		return nil, diagnostic(file, "", "profile.yaml", "profile document has invalid trailing content", nil)
	}
	count := 0
	if err := checkNode(file, document.Content[0], 0, &count); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}

func checkNode(file string, node *yaml.Node, depth int, count *int) error {
	if node == nil {
		return diagnostic(file, "", "profile.yaml", "profile document contains an invalid YAML node", nil)
	}
	(*count)++
	if depth > maxYAMLDepth || *count > maxYAMLNodes {
		return diagnostic(file, "", "profile.complexity", "profile document exceeds supported complexity", node)
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode || node.Tag == "!!merge" {
		return diagnostic(file, "", "profile.yaml_feature", "YAML anchors, aliases, and merges are not supported", node)
	}
	if node.Style&yaml.TaggedStyle != 0 {
		return diagnostic(file, "", "profile.yaml_feature", "explicit YAML tags are not supported", node)
	}
	if strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!") {
		return diagnostic(file, "", "profile.yaml_feature", "custom YAML tags are not supported", node)
	}
	if node.Kind == yaml.ScalarNode {
		for _, marker := range templateMarkers {
			if strings.Contains(node.Value, marker) {
				return diagnostic(file, "", "profile.template", "executable or interpolated templates are not supported", node)
			}
		}
		if shellVariablePattern.MatchString(node.Value) {
			return diagnostic(file, "", "profile.template", "executable or interpolated templates are not supported", node)
		}
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]struct{}{}
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return diagnostic(file, "", "profile.key", "mapping keys must be strings", key)
			}
			if _, exists := seen[key.Value]; exists {
				return diagnostic(file, key.Value, "profile.duplicate_key", "duplicate YAML key", key)
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := checkNode(file, child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func resolveDocument(file, profileName string, root *yaml.Node, catalog map[string]LogicalConnection) (Selected, error) {
	fields, err := mapping(file, "", root, "version", "profiles")
	if err != nil {
		return Selected{}, err
	}
	version := fields["version"]
	if version == nil || version.Kind != yaml.ScalarNode || version.Tag != "!!int" || version.Value != "1" {
		return Selected{}, diagnostic(file, "version", "profile.version", "profile version must be integer 1", version)
	}
	profiles := fields["profiles"]
	if profiles == nil || profiles.Kind != yaml.MappingNode {
		return Selected{}, diagnostic(file, "profiles", "profile.profiles", "profiles must be a mapping", profiles)
	}
	result := Selected{Connections: []Connection{}}
	found := false
	for profileIndex := 0; profileIndex < len(profiles.Content); profileIndex += 2 {
		nameNode, profile := profiles.Content[profileIndex], profiles.Content[profileIndex+1]
		name := nameNode.Value
		if !profileNamePattern.MatchString(name) {
			return Selected{}, diagnostic(file, "profiles."+name, "profile.name", "profile name is invalid", nameNode)
		}
		profileFields, mapErr := mapping(file, "profiles."+name, profile, "connections")
		if mapErr != nil {
			return Selected{}, mapErr
		}
		connections := profileFields["connections"]
		if connections == nil || connections.Kind != yaml.MappingNode {
			return Selected{}, diagnostic(file, "profiles."+name+".connections", "profile.connections", "connections must be a mapping", connections)
		}
		resolvedConnections := make([]Connection, 0, len(connections.Content)/2)
		for index := 0; index < len(connections.Content); index += 2 {
			connectionName, entry := connections.Content[index].Value, connections.Content[index+1]
			logical, exists := catalog[connectionName]
			if !exists {
				return Selected{}, diagnostic(file, "profiles."+name+".connections."+connectionName, "profile.connection_unknown", "connection name does not match the compiled graph", connections.Content[index])
			}
			resolved, resolveErr := resolveConnection(file, name, connectionName, entry, logical)
			if resolveErr != nil {
				return Selected{}, resolveErr
			}
			resolvedConnections = append(resolvedConnections, resolved)
		}
		if name == profileName {
			found = true
			result.Connections = resolvedConnections
		}
	}
	if !found {
		return Selected{}, diagnostic(file, "profiles."+profileName, "profile.not_found", "selected profile is not defined", profiles)
	}
	sort.Slice(result.Connections, func(i, j int) bool { return result.Connections[i].Name < result.Connections[j].Name })
	return result, nil
}

func resolveConnection(file, profileName, name string, node *yaml.Node, logical LogicalConnection) (Connection, error) {
	field := "profiles." + profileName + ".connections." + name
	if logical.ID.Validate() != nil || logical.ConnectorKind != strings.TrimSpace(logical.ConnectorKind) {
		return Connection{}, diagnostic(file, field, "profile.graph_identity", "compiled connection identity is invalid", node)
	}
	spec, ok := connectors.LookupConnection(logical.ConnectorKind)
	if !ok || spec.ActivationMode != connectors.TargetBindingActivation {
		return Connection{}, diagnostic(file, field, "profile.connection_managed", "connection uses its existing managed or authored setup contract", node)
	}
	fields, err := mapping(file, field, node, "endpoint", "credentials")
	if err != nil {
		return Connection{}, err
	}
	endpoint, err := decodeEndpoint(file, field+".endpoint", fields["endpoint"], spec)
	if err != nil {
		return Connection{}, err
	}
	credentials, err := decodeCredentials(file, field+".credentials", fields["credentials"], spec, logical.Access)
	if err != nil {
		return Connection{}, err
	}
	return Connection{Name: name, ID: logical.ID, ConnectorKind: logical.ConnectorKind, Endpoint: endpoint, Credentials: credentials}, nil
}

func decodeEndpoint(file, field string, node *yaml.Node, spec connectors.ConnectionSpec) (connectionadmin.EndpointConfig, error) {
	fields, err := mapping(file, field, node, "host", "port", "database", "objectScope", "sourceIdentity", "tlsMode", "options")
	if err != nil {
		return connectionadmin.EndpointConfig{}, err
	}
	endpoint := connectionadmin.EndpointConfig{}
	for name, target := range map[string]*string{"host": &endpoint.Host, "database": &endpoint.Database, "objectScope": &endpoint.ObjectScope, "sourceIdentity": &endpoint.SourceIdentity, "tlsMode": &endpoint.TLSMode} {
		if fields[name] == nil {
			continue
		}
		if fields[name].Kind != yaml.ScalarNode || fields[name].Tag != "!!str" {
			return endpoint, diagnostic(file, field+"."+name, "profile.endpoint_type", "endpoint field must be a string", fields[name])
		}
		*target = fields[name].Value
	}
	if port := fields["port"]; port != nil {
		if port.Kind != yaml.ScalarNode || port.Tag != "!!int" {
			return endpoint, diagnostic(file, field+".port", "profile.endpoint_type", "endpoint port must be an integer", port)
		}
		value, parseErr := strconv.Atoi(port.Value)
		if parseErr != nil {
			return endpoint, diagnostic(file, field+".port", "profile.endpoint_type", "endpoint port is invalid", port)
		}
		endpoint.Port = value
	}
	if options := fields["options"]; options != nil {
		if options.Kind != yaml.MappingNode {
			return endpoint, diagnostic(file, field+".options", "profile.endpoint_type", "endpoint options must be a mapping", options)
		}
		allowed := make(map[string]struct{}, len(spec.AllowedOptions))
		for _, name := range spec.AllowedOptions {
			allowed[name] = struct{}{}
		}
		endpoint.Options = make(map[string]string, len(options.Content)/2)
		for index := 0; index < len(options.Content); index += 2 {
			key, value := options.Content[index], options.Content[index+1]
			if _, ok := allowed[key.Value]; !ok {
				return endpoint, diagnostic(file, field+".options."+key.Value, "profile.option_unknown", "endpoint option is unsupported for the connector", key)
			}
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return endpoint, diagnostic(file, field+".options."+key.Value, "profile.endpoint_type", "endpoint option must be a string", value)
			}
			endpoint.Options[key.Value] = value.Value
		}
	}
	if err := connectionadmin.ValidateEndpointConfig(endpoint); err != nil {
		return endpoint, diagnostic(file, field, "profile.endpoint", "endpoint configuration is invalid", node)
	}
	if hasSecretBearingURL(endpoint) {
		return endpoint, diagnostic(file, field, "profile.endpoint_secret", "endpoint configuration contains a secret-bearing URL", node)
	}
	return endpoint, nil
}

func decodeCredentials(file, field string, node *yaml.Node, spec connectors.ConnectionSpec, access semanticmodel.ConnectionAccess) (CredentialMode, error) {
	fields, err := mapping(file, field, node, "env", "none")
	if err != nil {
		return CredentialMode{}, err
	}
	if (fields["env"] == nil) == (fields["none"] == nil) {
		return CredentialMode{}, diagnostic(file, field, "profile.credential_mode", "exactly one credential mode is required", node)
	}
	if env := fields["env"]; env != nil {
		if env.Kind != yaml.ScalarNode || env.Tag != "!!str" || len(env.Value) > 256 || !environmentPattern.MatchString(env.Value) {
			return CredentialMode{}, diagnostic(file, field+".env", "profile.credential_env", "credential environment reference is invalid", env)
		}
		if access == semanticmodel.ConnectionAccessPublic {
			return CredentialMode{}, diagnostic(file, field, "profile.credential_mode", "public connections must use permitted unauthenticated access", node)
		}
		return CredentialMode{EnvironmentVariable: env.Value}, nil
	}
	none := fields["none"]
	if none.Kind != yaml.ScalarNode || none.Tag != "!!bool" || none.Value != "true" || !spec.AllowPublicAccess || access != semanticmodel.ConnectionAccessPublic {
		return CredentialMode{}, diagnostic(file, field+".none", "profile.credential_none", "unauthenticated access is not permitted for this connection", none)
	}
	return CredentialMode{None: true}, nil
}

func mapping(file, field string, node *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, diagnostic(file, field, "profile.mapping", "field must be a mapping", node)
	}
	known := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		known[name] = struct{}{}
	}
	result := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if _, ok := known[key.Value]; !ok {
			path := key.Value
			if field != "" {
				path = field + "." + key.Value
			}
			return nil, diagnostic(file, path, "profile.field_unknown", "unknown profile field", key)
		}
		result[key.Value] = value
	}
	return result, nil
}

func hasSecretBearingURL(endpoint connectionadmin.EndpointConfig) bool {
	values := []struct {
		value          string
		host           bool
		filesystemPath bool
	}{{value: endpoint.Host, host: true}, {value: endpoint.Database}, {value: endpoint.ObjectScope}, {value: endpoint.SourceIdentity}, {value: endpoint.TLSMode}}
	for name, value := range endpoint.Options {
		values = append(values, struct {
			value          string
			host           bool
			filesystemPath bool
		}{value: value, filesystemPath: name == "path" || name == "data_path"})
	}
	for _, candidate := range values {
		value := candidate.value
		if strings.ContainsAny(value, "\r\n\x00") {
			return true
		}
		if hasSecretAssignment(value) {
			return true
		}
		if candidate.filesystemPath && looksLikeAbsoluteFilesystemPath(value) {
			continue
		}
		uriLike := strings.ContainsAny(value, "?#") || strings.Contains(value, "://") || strings.HasPrefix(value, "//")
		if !uriLike {
			at := strings.LastIndexByte(value, '@')
			if at >= 0 && (candidate.host || strings.Contains(value[:at], ":")) {
				return true
			}
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.User != nil {
			return true
		}
		opaque, err := url.PathUnescape(parsed.Opaque)
		if err != nil {
			return true
		}
		if at := strings.LastIndexByte(opaque, '@'); at >= 0 && strings.Contains(opaque[:at], ":") {
			return true
		}
		if parsed.Scheme == "" && parsed.Host == "" {
			if at := strings.LastIndexByte(parsed.Path, '@'); at >= 0 && strings.Contains(parsed.Path[:at], ":") {
				return true
			}
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return true
		}
		for key := range query {
			normalized := strings.ToLower(key)
			if secretShapedName(normalized) {
				return true
			}
		}
		if secretShapedName(strings.ToLower(parsed.Fragment)) {
			return true
		}
	}
	return false
}

func secretShapedName(value string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(value)
	for _, suffix := range []string{
		"password", "passwd", "secret", "token", "credential", "credentials",
		"privatekey", "accesskey", "accesskeyid", "apikey", "accesstoken",
		"refreshtoken", "connectionstring", "accountkey", "subscriptionkey",
		"signature", "authorization",
	} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	for _, part := range strings.FieldsFunc(value, func(char rune) bool {
		return (char < 'a' || char > 'z') && (char < '0' || char > '9')
	}) {
		switch part {
		case "password", "passwd", "secret", "token", "credential", "credentials", "privatekey", "accesskey", "apikey", "signature", "sig", "authorization", "auth":
			return true
		}
	}
	return false
}

func hasSecretAssignment(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] != '=' && value[index] != ':' {
			continue
		}
		end := index
		for end > 0 && (value[end-1] == ' ' || value[end-1] == '\t' || value[end-1] == '\'' || value[end-1] == '"') {
			end--
		}
		start := end
		for start > 0 && secretKeyCharacter(value[start-1]) {
			start--
		}
		if start < end && secretShapedName(strings.ToLower(value[start:end])) {
			return true
		}
	}
	return false
}

func secretKeyCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_' || value == '-' || value == '.'
}

func looksLikeAbsoluteFilesystemPath(value string) bool {
	if filepath.IsAbs(value) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}
