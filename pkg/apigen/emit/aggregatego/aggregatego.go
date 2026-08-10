// Package aggregatego emits optional Go route composition for independently
// generated APIGen server packages.
package aggregatego

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"
)

// ServerPackage describes one generated APIGen server package.
type ServerPackage struct {
	// Name is a stable, language-level logical name used for exported fields.
	Name string
	// ImportPath is the canonical Go import path of the generated server.
	ImportPath string
	// PackageName is the declared Go package name of the generated server.
	PackageName string
	// HasTools reports whether the generated package exposes agent-tool contracts.
	HasTools bool
}

// Options configures aggregate Go route composition.
type Options struct {
	PackageName             string
	EmbeddedOpenAPISpecJSON string
	Packages                []ServerPackage
}

type resolvedServerPackage struct {
	ServerPackage
	Alias string
	Field string
}

const chiRuntimeImportPath = "github.com/Yacobolo/toolbelt/apigen/runtime/chi"

var goKeywords = map[string]struct{}{
	"break": {}, "default": {}, "func": {}, "interface": {}, "select": {},
	"case": {}, "defer": {}, "go": {}, "map": {}, "struct": {},
	"chan": {}, "else": {}, "goto": {}, "package": {}, "switch": {},
	"const": {}, "fallthrough": {}, "if": {}, "range": {}, "type": {},
	"continue": {}, "for": {}, "import": {}, "return": {}, "var": {},
}

// Emit renders typed loose and strict route composition for generated servers.
func Emit(opts Options) ([]byte, error) {
	if !validGoIdentifier(opts.PackageName) {
		return nil, fmt.Errorf("invalid aggregate Go package %q", opts.PackageName)
	}
	if len(opts.Packages) == 0 {
		return nil, fmt.Errorf("aggregate requires at least one server package")
	}
	if opts.EmbeddedOpenAPISpecJSON == "" || !json.Valid([]byte(opts.EmbeddedOpenAPISpecJSON)) {
		return nil, fmt.Errorf("aggregate requires valid embedded canonical OpenAPI JSON")
	}
	packages, err := resolveServerPackages(opts.Packages)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", opts.PackageName)
	b.WriteString("import (\n")
	b.WriteString("\t\"encoding/json\"\n\n")
	b.WriteString("\tapigenagenttool \"github.com/Yacobolo/toolbelt/apigen/runtime/agenttool\"\n")
	b.WriteString("\tapigencommand \"github.com/Yacobolo/toolbelt/apigen/runtime/command\"\n")
	fmt.Fprintf(&b, "\tapigenchi %q\n", chiRuntimeImportPath)
	for _, serverPackage := range packages {
		fmt.Fprintf(&b, "\t%s %q\n", serverPackage.Alias, serverPackage.ImportPath)
	}
	b.WriteString(")\n\n")

	b.WriteString("// Servers contains one loose generated server per composed package.\n")
	b.WriteString("type Servers struct {\n")
	for _, serverPackage := range packages {
		fmt.Fprintf(&b, "\t%s %s.GenServerInterface\n", serverPackage.Field, serverPackage.Alias)
	}
	b.WriteString("}\n\n")

	b.WriteString("// RegisterAPIGenRoutes mounts every composed generated server.\n")
	b.WriteString("func RegisterAPIGenRoutes(router apigenchi.Router, servers Servers) {\n")
	for _, serverPackage := range packages {
		fmt.Fprintf(&b, "\t%s.RegisterAPIGenRoutes(router, servers.%s)\n", serverPackage.Alias, serverPackage.Field)
	}
	b.WriteString("}\n\n")

	b.WriteString("// StrictServers contains one strict generated handler per composed package.\n")
	b.WriteString("type StrictServers struct {\n")
	for _, serverPackage := range packages {
		fmt.Fprintf(&b, "\t%s %s.GenStrictServerInterface\n", serverPackage.Field, serverPackage.Alias)
	}
	b.WriteString("}\n\n")

	b.WriteString("// TransportErrorResponders contains one transport error responder per composed package.\n")
	b.WriteString("type TransportErrorResponders struct {\n")
	for _, serverPackage := range packages {
		fmt.Fprintf(&b, "\t%s %s.GenTransportErrorResponder\n", serverPackage.Field, serverPackage.Alias)
	}
	b.WriteString("}\n\n")

	b.WriteString("// RegisterAPIGenStrictRoutes mounts every composed strict generated server.\n")
	b.WriteString("func RegisterAPIGenStrictRoutes(router apigenchi.Router, servers StrictServers, responders TransportErrorResponders) {\n")
	for _, serverPackage := range packages {
		fmt.Fprintf(
			&b,
			"\t%s.RegisterAPIGenStrictRoutes(router, servers.%s, responders.%s)\n",
			serverPackage.Alias,
			serverPackage.Field,
			serverPackage.Field,
		)
	}
	b.WriteString("}\n")
	emitProtocolMetadata(&b, packages, opts.EmbeddedOpenAPISpecJSON)
	return []byte(b.String()), nil
}

func emitProtocolMetadata(b *strings.Builder, packages []resolvedServerPackage, embeddedOpenAPI string) {
	b.WriteString("\nconst embeddedOpenAPISpecJSON = ")
	fmt.Fprintf(b, "%q\n\n", embeddedOpenAPI)
	b.WriteString("// GetEmbeddedOpenAPISpec returns the canonical OpenAPI document as a generic JSON map.\n")
	b.WriteString("func GetEmbeddedOpenAPISpec() (map[string]any, error) {\n")
	b.WriteString("\tvar doc map[string]any\n")
	b.WriteString("\tif err := json.Unmarshal([]byte(embeddedOpenAPISpecJSON), &doc); err != nil { return nil, err }\n")
	b.WriteString("\treturn doc, nil\n")
	b.WriteString("}\n\n")

	b.WriteString("// GenOperationContract captures APIGen-owned contract metadata for one operation.\n")
	b.WriteString("type GenOperationKind string\n\n")
	b.WriteString("const (\n\tGenOperationKindCommand GenOperationKind = \"command\"\n\tGenOperationKindQuery GenOperationKind = \"query\"\n)\n\n")
	b.WriteString("type GenOperationSurface string\n\n")
	b.WriteString("const (\n\tGenOperationSurfaceUI GenOperationSurface = \"ui\"\n\tGenOperationSurfaceAgent GenOperationSurface = \"agent\"\n\tGenOperationSurfaceAutomation GenOperationSurface = \"automation\"\n)\n\n")
	b.WriteString("type GenAuditPolicy struct { Required bool; SuccessAction string; Guarantee string }\n\n")
	b.WriteString("type GenAsyncExecutionContract struct { Mode string; JobKind string; ResourceKind string; InitialEvent string; InitialState string; StatusOperation string; EventsOperation string; Cancellation string }\n\n")
	b.WriteString("type GenOperationTarget struct { Parameter string; Type string }\n\n")
	b.WriteString("type GenCommandContract struct {\n")
	b.WriteString("\tOwner string\n\tAudit GenAuditPolicy\n\tExecution *GenAsyncExecutionContract\n\tAdditionalExposures []GenOperationSurface\n\tTarget *GenOperationTarget\n")
	b.WriteString("\tIdempotency string\n\tConcurrency string\n\tAuthzMode string\n\tPrivilege string\n")
	b.WriteString("}\n\n")
	b.WriteString("type GenOperationContract struct {\n")
	b.WriteString("\tOperationID string\n\tKind GenOperationKind\n\tNamespace string\n\tMethod string\n\tPath string\n\tTags []string\n\tDocumentedStatusCodes []int\n")
	b.WriteString("\tRequestBodyRequired bool\n\tAuthzMode string\n\tProtected bool\n\tManual bool\n\tCommand *GenCommandContract\n\tExtensions map[string]any\n")
	b.WriteString("}\n\n")
	b.WriteString("var genOperationContracts = func() map[string]GenOperationContract {\n")
	b.WriteString("\tout := map[string]GenOperationContract{}\n")
	for _, serverPackage := range packages {
		fmt.Fprintf(b, "\tfor operationID, contract := range %s.GetAPIGenOperationContracts() {\n", serverPackage.Alias)
		b.WriteString("\t\tif _, exists := out[operationID]; exists { panic(\"duplicate aggregate APIGen operation \" + operationID) }\n")
		b.WriteString("\t\tvar command *GenCommandContract\n")
		b.WriteString("\t\tif contract.Command != nil { command = &GenCommandContract{Owner: contract.Command.Owner, Audit: GenAuditPolicy{Required: contract.Command.Audit.Required, SuccessAction: contract.Command.Audit.SuccessAction, Guarantee: contract.Command.Audit.Guarantee}, AdditionalExposures: make([]GenOperationSurface, len(contract.Command.AdditionalExposures)), Idempotency: contract.Command.Idempotency, Concurrency: contract.Command.Concurrency, AuthzMode: contract.Command.AuthzMode, Privilege: contract.Command.Privilege}; for index, exposure := range contract.Command.AdditionalExposures { command.AdditionalExposures[index] = GenOperationSurface(exposure) }; if contract.Command.Target != nil { command.Target = &GenOperationTarget{Parameter: contract.Command.Target.Parameter, Type: contract.Command.Target.Type} }; if contract.Command.Execution != nil { source := contract.Command.Execution; command.Execution = &GenAsyncExecutionContract{Mode: source.Mode, JobKind: source.JobKind, ResourceKind: source.ResourceKind, InitialEvent: source.InitialEvent, InitialState: source.InitialState, StatusOperation: source.StatusOperation, EventsOperation: source.EventsOperation, Cancellation: source.Cancellation} } }\n")
		b.WriteString("\t\tout[operationID] = GenOperationContract{\n")
		b.WriteString("\t\t\tOperationID: contract.OperationID, Kind: GenOperationKind(contract.Kind), Namespace: contract.Namespace, Method: contract.Method, Path: contract.Path,\n")
		b.WriteString("\t\t\tTags: append([]string(nil), contract.Tags...), DocumentedStatusCodes: append([]int(nil), contract.DocumentedStatusCodes...),\n")
		b.WriteString("\t\t\tRequestBodyRequired: contract.RequestBodyRequired, AuthzMode: contract.AuthzMode,\n")
		b.WriteString("\t\t\tProtected: contract.Protected, Manual: contract.Manual, Command: command, Extensions: cloneAPIGenAnyMap(contract.Extensions),\n")
		b.WriteString("\t\t}\n\t}\n")
	}
	b.WriteString("\treturn out\n}()\n\n")
	b.WriteString("// GetAPIGenOperationContracts returns a defensive copy of the aggregate contract registry.\n")
	b.WriteString("func GetAPIGenOperationContracts() map[string]GenOperationContract {\n")
	b.WriteString("\tout := make(map[string]GenOperationContract, len(genOperationContracts))\n")
	b.WriteString("\tfor operationID, contract := range genOperationContracts { out[operationID] = cloneAPIGenOperationContract(contract) }\n")
	b.WriteString("\treturn out\n}\n\n")
	b.WriteString("// GetAPIGenOperationContract returns aggregate contract metadata for one operation.\n")
	b.WriteString("func GetAPIGenOperationContract(operationID string) (GenOperationContract, bool) {\n")
	b.WriteString("\tcontract, ok := genOperationContracts[operationID]\n")
	b.WriteString("\tif !ok { return GenOperationContract{}, false }\n")
	b.WriteString("\treturn cloneAPIGenOperationContract(contract), true\n}\n\n")
	b.WriteString("// GetAPIGenCommandRuntimeContract returns the normalized runtime audit contract for an aggregate command.\n")
	b.WriteString("func GetAPIGenCommandRuntimeContract(operationID string) (apigencommand.Contract, bool) {\n")
	b.WriteString("\tcontract, ok := genOperationContracts[operationID]\n")
	b.WriteString("\tif !ok || contract.Command == nil || !contract.Command.Audit.Required { return apigencommand.Contract{}, false }\n")
	b.WriteString("\tvar execution *apigencommand.AsyncExecutionContract\n")
	b.WriteString("\tif contract.Command.Execution != nil { source := contract.Command.Execution; execution = &apigencommand.AsyncExecutionContract{Mode: source.Mode, JobKind: source.JobKind, ResourceKind: source.ResourceKind, InitialEvent: source.InitialEvent, InitialState: source.InitialState, StatusOperation: source.StatusOperation, EventsOperation: source.EventsOperation, Cancellation: source.Cancellation} }\n")
	b.WriteString("\treturn apigencommand.Contract{OperationID: contract.OperationID, Owner: contract.Command.Owner, AuditAction: contract.Command.Audit.SuccessAction, Guarantee: apigencommand.Guarantee(contract.Command.Audit.Guarantee), Execution: execution}, true\n")
	b.WriteString("}\n\n")
	b.WriteString("// APIGenOperationAllowsStatus reports whether a status is documented for an operation.\n")
	b.WriteString("//nolint:revive // exported generated helper name matches the APIGen contract registry namespace.\n")
	b.WriteString("func APIGenOperationAllowsStatus(operationID string, statusCode int) bool {\n")
	b.WriteString("\tcontract, ok := genOperationContracts[operationID]\n\tif !ok { return false }\n")
	b.WriteString("\tfor _, documented := range contract.DocumentedStatusCodes { if documented == statusCode { return true } }\n")
	b.WriteString("\treturn false\n}\n\n")
	b.WriteString("func cloneAPIGenOperationContract(contract GenOperationContract) GenOperationContract {\n")
	b.WriteString("\tcontract.Tags = append([]string(nil), contract.Tags...)\n")
	b.WriteString("\tcontract.DocumentedStatusCodes = append([]int(nil), contract.DocumentedStatusCodes...)\n")
	b.WriteString("\tif contract.Command != nil { command := *contract.Command; command.AdditionalExposures = append([]GenOperationSurface(nil), contract.Command.AdditionalExposures...); if contract.Command.Target != nil { target := *contract.Command.Target; command.Target = &target }; if contract.Command.Execution != nil { execution := *contract.Command.Execution; command.Execution = &execution }; contract.Command = &command }\n")
	b.WriteString("\tcontract.Extensions = cloneAPIGenAnyMap(contract.Extensions)\n\treturn contract\n}\n\n")
	b.WriteString("func cloneAPIGenAnyMap(in map[string]any) map[string]any {\n")
	b.WriteString("\tif in == nil { return nil }\n\tout := make(map[string]any, len(in))\n")
	b.WriteString("\tfor key, value := range in { out[key] = cloneAPIGenAny(value) }\n\treturn out\n}\n\n")
	b.WriteString("func cloneAPIGenAny(value any) any {\n\tswitch typed := value.(type) {\n")
	b.WriteString("\tcase map[string]any:\n\t\treturn cloneAPIGenAnyMap(typed)\n")
	b.WriteString("\tcase []any:\n\t\tout := make([]any, len(typed))\n\t\tfor i, item := range typed { out[i] = cloneAPIGenAny(item) }\n\t\treturn out\n")
	b.WriteString("\tdefault:\n\t\treturn typed\n\t}\n}\n\n")

	b.WriteString("var genAPIGenToolContracts = func() map[string]apigenagenttool.Contract {\n")
	b.WriteString("\tout := map[string]apigenagenttool.Contract{}\n")
	for _, serverPackage := range packages {
		if !serverPackage.HasTools {
			continue
		}
		fmt.Fprintf(b, "\tfor name, contract := range %s.GetAPIGenToolContracts() {\n", serverPackage.Alias)
		b.WriteString("\t\tif _, exists := out[name]; exists { panic(\"duplicate aggregate APIGen tool \" + name) }\n")
		b.WriteString("\t\tout[name] = apigenagenttool.CloneContract(contract)\n\t}\n")
	}
	b.WriteString("\treturn out\n}()\n\n")
	b.WriteString("// GetAPIGenToolContracts returns defensive copies of aggregate endpoint-derived tools.\n")
	b.WriteString("func GetAPIGenToolContracts() map[string]apigenagenttool.Contract {\n")
	b.WriteString("\tout := make(map[string]apigenagenttool.Contract, len(genAPIGenToolContracts))\n")
	b.WriteString("\tfor name, contract := range genAPIGenToolContracts { out[name] = apigenagenttool.CloneContract(contract) }\n")
	b.WriteString("\treturn out\n}\n\n")
	b.WriteString("// GetAPIGenToolContract returns one aggregate endpoint-derived tool.\n")
	b.WriteString("func GetAPIGenToolContract(name string) (apigenagenttool.Contract, bool) {\n")
	b.WriteString("\tcontract, ok := genAPIGenToolContracts[name]\n")
	b.WriteString("\tif !ok { return apigenagenttool.Contract{}, false }\n")
	b.WriteString("\treturn apigenagenttool.CloneContract(contract), true\n}\n")
}

func resolveServerPackages(authored []ServerPackage) ([]resolvedServerPackage, error) {
	packages := append([]ServerPackage(nil), authored...)
	sort.Slice(packages, func(left, right int) bool {
		if packages[left].ImportPath != packages[right].ImportPath {
			return packages[left].ImportPath < packages[right].ImportPath
		}
		if packages[left].PackageName != packages[right].PackageName {
			return packages[left].PackageName < packages[right].PackageName
		}
		return packages[left].Name < packages[right].Name
	})

	packageCounts := map[string]int{}
	fieldCounts := map[string]int{}
	fields := make([]string, len(packages))
	importPaths := map[string]struct{}{}
	for index, serverPackage := range packages {
		if strings.TrimSpace(serverPackage.Name) == "" {
			return nil, fmt.Errorf("aggregate server package name is required")
		}
		if !canonicalGoImportPath(serverPackage.ImportPath) {
			return nil, fmt.Errorf("aggregate server package %q requires a canonical Go import path", serverPackage.Name)
		}
		if serverPackage.ImportPath == chiRuntimeImportPath {
			return nil, fmt.Errorf("aggregate server package %q conflicts with the APIGen Chi runtime import", serverPackage.Name)
		}
		if !validGoIdentifier(serverPackage.PackageName) {
			return nil, fmt.Errorf("aggregate server package %q has invalid Go package %q", serverPackage.Name, serverPackage.PackageName)
		}
		if _, exists := importPaths[serverPackage.ImportPath]; exists {
			return nil, fmt.Errorf("aggregate server import path %q is declared more than once", serverPackage.ImportPath)
		}
		importPaths[serverPackage.ImportPath] = struct{}{}
		field := exportedIdentifier(serverPackage.Name)
		fields[index] = field
		packageCounts[serverPackage.PackageName]++
		fieldCounts[field]++
	}

	usedAliases := map[string]string{"apigenchi": chiRuntimeImportPath}
	usedFields := map[string]string{}
	for index, serverPackage := range packages {
		if packageCounts[serverPackage.PackageName] == 1 && serverPackage.PackageName != "apigenchi" {
			usedAliases[serverPackage.PackageName] = serverPackage.ImportPath
		}
		if fieldCounts[fields[index]] == 1 {
			usedFields[fields[index]] = serverPackage.ImportPath
		}
	}
	resolved := make([]resolvedServerPackage, 0, len(packages))
	for index, serverPackage := range packages {
		alias := serverPackage.PackageName
		if packageCounts[serverPackage.PackageName] > 1 || alias == "apigenchi" {
			var err error
			alias, err = allocateHashedIdentifier(alias+"_", serverPackage.ImportPath, false, usedAliases)
			if err != nil {
				return nil, err
			}
		}
		usedAliases[alias] = serverPackage.ImportPath

		field := fields[index]
		if fieldCounts[field] > 1 {
			var err error
			field, err = allocateHashedIdentifier(field, serverPackage.ImportPath, true, usedFields)
			if err != nil {
				return nil, err
			}
		}
		usedFields[field] = serverPackage.ImportPath
		resolved = append(resolved, resolvedServerPackage{
			ServerPackage: serverPackage,
			Alias:         alias,
			Field:         field,
		})
	}
	return resolved, nil
}

func allocateHashedIdentifier(prefix, identity string, uppercase bool, used map[string]string) (string, error) {
	sum := sha256.Sum256([]byte(identity))
	digest := hex.EncodeToString(sum[:])
	if uppercase {
		digest = strings.ToUpper(digest)
	}
	for length := 8; length <= len(digest); length += 2 {
		candidate := prefix + digest[:length]
		if previous, exists := used[candidate]; !exists || previous == identity {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot allocate a unique aggregate Go identifier for %q", identity)
}

func canonicalGoImportPath(importPath string) bool {
	if !(importPath != "" &&
		strings.TrimSpace(importPath) == importPath &&
		importPath != "." &&
		importPath != ".." &&
		!strings.HasPrefix(importPath, "../") &&
		!path.IsAbs(importPath) &&
		!strings.Contains(importPath, `\`) &&
		!strings.HasSuffix(importPath, "/") &&
		path.Clean(importPath) == importPath) {
		return false
	}
	for _, character := range importPath {
		if character == '/' || unicode.IsLetter(character) || unicode.IsDigit(character) ||
			strings.ContainsRune("-._~+", character) {
			continue
		}
		return false
	}
	return true
}

func validGoIdentifier(value string) bool {
	if value == "" || value == "_" {
		return false
	}
	if _, keyword := goKeywords[value]; keyword {
		return false
	}
	for index, character := range value {
		if character == '_' || unicode.IsLetter(character) || (index > 0 && unicode.IsDigit(character)) {
			continue
		}
		return false
	}
	return true
}

func exportedIdentifier(value string) string {
	var out []rune
	upperNext := true
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			upperNext = true
			continue
		}
		if len(out) == 0 && unicode.IsDigit(character) {
			out = append(out, []rune("Package")...)
		}
		if upperNext {
			character = unicode.ToUpper(character)
			upperNext = false
		}
		out = append(out, character)
	}
	if len(out) == 0 {
		return "Package"
	}
	return string(out)
}
