// Package main provides the apigen CLI entrypoint.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	clientgoemit "github.com/Yacobolo/toolbelt/apigen/emit/clientgo"
	cligoemit "github.com/Yacobolo/toolbelt/apigen/emit/cligo"
	"github.com/Yacobolo/toolbelt/apigen/emit/contractimport"
	failuretsemit "github.com/Yacobolo/toolbelt/apigen/emit/failurets"
	jsonschemaemit "github.com/Yacobolo/toolbelt/apigen/emit/jsonschema"
	modelgoemit "github.com/Yacobolo/toolbelt/apigen/emit/modelgo"
	modeltsemit "github.com/Yacobolo/toolbelt/apigen/emit/modelts"
	openapiemit "github.com/Yacobolo/toolbelt/apigen/emit/openapi"
	requestmodelgoemit "github.com/Yacobolo/toolbelt/apigen/emit/requestmodelgo"
	servergoemit "github.com/Yacobolo/toolbelt/apigen/emit/servergo"
	"github.com/Yacobolo/toolbelt/apigen/ir"
	typespecbundle "github.com/Yacobolo/toolbelt/apigen/typespec"
	"go.yaml.in/yaml/v4"
)

type commandConfig struct {
	Kind                 string
	IRPath               string
	IROut                string
	OpenAPIOut           string
	CanonicalOpenAPIPath string
	TypeSpecDir          string
	TypeSpecEntrypoint   string
	StrictOperationKinds bool
	ServerOut            string
	ServerPackage        string
	RequestModelsOut     string
	RequestModelsPackage string
	ClientOut            string
	CLIOut               string
	CLIPackage           string
	GenerateCLI          bool
	GoModelsOut          string
	GoModelsPackage      string
	TSOut                string
	FailureTSOut         string
	JSONSchemaOut        string
	ContractImports      map[string]contractImportSpec
	GoPackagePlan        *goPackagePlan
}

type targetManifest struct {
	Targets []targetSpec `yaml:"targets"`
}

type goOutputSpec struct {
	Dir               string                         `yaml:"dir"`
	Package           string                         `yaml:"package"`
	ServerFile        string                         `yaml:"server_file"`
	RequestModelsFile string                         `yaml:"request_models_file"`
	ClientFile        string                         `yaml:"client_file"`
	Default           *goPackageOutputSpec           `yaml:"default"`
	Aggregate         *goPackageOutputSpec           `yaml:"aggregate"`
	Packages          map[string]goPackageOutputSpec `yaml:"packages"`
	Unmatched         string                         `yaml:"unmatched"`
}

type goPackageOutputSpec struct {
	Dir               string `yaml:"dir"`
	Package           string `yaml:"package"`
	ImportPath        string `yaml:"import_path"`
	ServerFile        string `yaml:"server_file"`
	RequestModelsFile string `yaml:"request_models_file"`
	ClientFile        string `yaml:"client_file"`
}

type cliOutputSpec struct {
	Dir     string `yaml:"dir"`
	Package string `yaml:"package"`
	File    string `yaml:"file"`
}

type contractImportSpec struct {
	GoPackage        string `yaml:"go_package"`
	GoAlias          string `yaml:"go_alias"`
	TypeScriptModule string `yaml:"typescript_module"`
	ExactNamespace   bool   `yaml:"-"`
}

type targetSpec struct {
	Name                 string                        `yaml:"name"`
	Kind                 string                        `yaml:"kind"`
	TypeSpecDir          string                        `yaml:"typespec_dir"`
	TypeSpecEntrypoint   string                        `yaml:"typespec_entrypoint"`
	StrictOperationKinds bool                          `yaml:"strict_operation_kinds"`
	IROut                string                        `yaml:"ir_out"`
	OpenAPIOut           string                        `yaml:"openapi_out"`
	ServerOut            string                        `yaml:"server_out"`
	ServerPackage        string                        `yaml:"server_package"`
	RequestModelsOut     string                        `yaml:"request_models_out"`
	RequestModelsPackage string                        `yaml:"request_models_package"`
	CompatTypesOut       string                        `yaml:"compat_types_out"`
	CompatTypesPackage   string                        `yaml:"compat_types_package"`
	CLIOut               string                        `yaml:"-"`
	CLIPackage           string                        `yaml:"cli_package"`
	GenerateCLI          *bool                         `yaml:"generate_cli"`
	GoOut                *goOutputSpec                 `yaml:"go_out"`
	CLIOutGroup          *cliOutputSpec                `yaml:"-"`
	GoModelsOut          string                        `yaml:"go_models_out"`
	GoModelsPackage      string                        `yaml:"go_models_package"`
	TSOut                string                        `yaml:"ts_out"`
	FailureTSOut         string                        `yaml:"failure_ts_out"`
	JSONSchemaOut        string                        `yaml:"json_schema_out"`
	ContractImports      map[string]contractImportSpec `yaml:"contract_imports"`
}

var goPackagePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
var goKeywords = map[string]struct{}{
	"break": {}, "default": {}, "func": {}, "interface": {}, "select": {},
	"case": {}, "defer": {}, "go": {}, "map": {}, "struct": {},
	"chan": {}, "else": {}, "goto": {}, "package": {}, "switch": {},
	"const": {}, "fallthrough": {}, "if": {}, "range": {}, "type": {},
	"continue": {}, "for": {}, "import": {}, "return": {}, "var": {},
}

const typeSpecPackageDirEnv = "APIGEN_TYPESPEC_PACKAGE_DIR"

type typeSpecPackage struct {
	Dir     string
	Managed bool
}

func (target *targetSpec) UnmarshalYAML(unmarshal func(any) error) error {
	type rawTargetSpec struct {
		Name                 string                        `yaml:"name"`
		Kind                 string                        `yaml:"kind"`
		TypeSpecDir          string                        `yaml:"typespec_dir"`
		TypeSpecEntrypoint   string                        `yaml:"typespec_entrypoint"`
		StrictOperationKinds bool                          `yaml:"strict_operation_kinds"`
		IROut                string                        `yaml:"ir_out"`
		OpenAPIOut           string                        `yaml:"openapi_out"`
		ServerOut            string                        `yaml:"server_out"`
		ServerPackage        string                        `yaml:"server_package"`
		RequestModelsOut     string                        `yaml:"request_models_out"`
		RequestModelsPackage string                        `yaml:"request_models_package"`
		CompatTypesOut       string                        `yaml:"compat_types_out"`
		CompatTypesPackage   string                        `yaml:"compat_types_package"`
		CLIOut               any                           `yaml:"cli_out"`
		CLIPackage           string                        `yaml:"cli_package"`
		GenerateCLI          *bool                         `yaml:"generate_cli"`
		GoOut                *goOutputSpec                 `yaml:"go_out"`
		GoModelsOut          string                        `yaml:"go_models_out"`
		GoModelsPackage      string                        `yaml:"go_models_package"`
		TSOut                string                        `yaml:"ts_out"`
		FailureTSOut         string                        `yaml:"failure_ts_out"`
		JSONSchemaOut        string                        `yaml:"json_schema_out"`
		ContractImports      map[string]contractImportSpec `yaml:"contract_imports"`
	}

	var raw rawTargetSpec
	if err := unmarshal(&raw); err != nil {
		return err
	}

	*target = targetSpec{
		Name:                 raw.Name,
		Kind:                 raw.Kind,
		TypeSpecDir:          raw.TypeSpecDir,
		TypeSpecEntrypoint:   raw.TypeSpecEntrypoint,
		StrictOperationKinds: raw.StrictOperationKinds,
		IROut:                raw.IROut,
		OpenAPIOut:           raw.OpenAPIOut,
		ServerOut:            raw.ServerOut,
		ServerPackage:        raw.ServerPackage,
		RequestModelsOut:     raw.RequestModelsOut,
		RequestModelsPackage: raw.RequestModelsPackage,
		CompatTypesOut:       raw.CompatTypesOut,
		CompatTypesPackage:   raw.CompatTypesPackage,
		CLIPackage:           raw.CLIPackage,
		GenerateCLI:          raw.GenerateCLI,
		GoOut:                raw.GoOut,
		GoModelsOut:          raw.GoModelsOut,
		GoModelsPackage:      raw.GoModelsPackage,
		TSOut:                raw.TSOut,
		FailureTSOut:         raw.FailureTSOut,
		JSONSchemaOut:        raw.JSONSchemaOut,
		ContractImports:      raw.ContractImports,
	}

	if raw.CLIOut == nil {
		return nil
	}

	switch value := raw.CLIOut.(type) {
	case map[string]any:
		var grouped cliOutputSpec
		encoded, err := yaml.Marshal(value)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(encoded, &grouped); err != nil {
			return err
		}
		target.CLIOutGroup = &grouped
	case string:
		if strings.TrimSpace(value) != "" {
			return fmt.Errorf("cli_out must be a mapping")
		}
	default:
		return fmt.Errorf("cli_out must be a mapping")
	}

	return nil
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		writeTopLevelUsage(stderr)
		return 1
	}
	if isTopLevelHelp(args[0]) {
		writeTopLevelUsage(stdout)
		return 0
	}

	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", "optional APIGen target manifest path")
	targetName := fs.String("target", "", "manifest target name")
	kind := fs.String("kind", "http", "target kind: http or contracts")
	irPath := fs.String("ir", "gen/json-ir.json", "input JSON IR path")
	irOut := fs.String("ir-out", "gen/json-ir.json", "output JSON IR path for TypeSpec compilation")
	openapiOut := fs.String("openapi-out", "gen/openapi.yaml", "output OpenAPI YAML path for optional debug/compat emission")
	canonicalOpenAPIPath := fs.String("canonical-openapi", "gen/openapi.yaml", "canonical OpenAPI YAML path to embed into generated server code")
	typeSpecDir := fs.String("typespec-dir", "api/typespec", "input TypeSpec API source directory")
	strictOperationKinds := fs.Bool("strict-operation-kinds", false, "require explicit command/query classification for non-read operations")
	serverOut := fs.String("server-out", "internal/api/server.apigen.gen.go", "output server Go path")
	serverPackage := fs.String("server-package", "api", "generated server Go package name")
	requestModelsOut := fs.String("request-models-out", "internal/api/gen_request_models.gen.go", "output APIGen request models Go path")
	requestModelsPackage := fs.String("request-models-package", "api", "generated request models Go package name")
	cliOut := fs.String("cli-out", "pkg/cli/gen/apigen_registry.gen.go", "output CLI Go path")
	cliPackage := fs.String("cli-package", "gen", "generated CLI Go package name")
	goModelsOut := fs.String("go-models-out", "", "output data-contract Go models path")
	goModelsPackage := fs.String("go-models-package", "contracts", "generated data-contract Go package name")
	tsOut := fs.String("ts-out", "", "output data-contract TypeScript path")
	failureTSOut := fs.String("failure-ts-out", "", "output HTTP command failure TypeScript path")
	jsonSchemaOut := fs.String("json-schema-out", "", "output data-contract JSON Schema path")
	if err := fs.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return failf(stderr, "parse flags: %v", err)
	}

	config, err := resolveCommandConfig(command, *manifestPath, *targetName, commandConfig{
		Kind:                 *kind,
		IRPath:               *irPath,
		IROut:                *irOut,
		OpenAPIOut:           *openapiOut,
		CanonicalOpenAPIPath: *canonicalOpenAPIPath,
		TypeSpecDir:          *typeSpecDir,
		StrictOperationKinds: *strictOperationKinds,
		ServerOut:            *serverOut,
		ServerPackage:        *serverPackage,
		RequestModelsOut:     *requestModelsOut,
		RequestModelsPackage: *requestModelsPackage,
		CLIOut:               *cliOut,
		CLIPackage:           *cliPackage,
		GenerateCLI:          true,
		GoModelsOut:          *goModelsOut,
		GoModelsPackage:      *goModelsPackage,
		TSOut:                *tsOut,
		FailureTSOut:         *failureTSOut,
		JSONSchemaOut:        *jsonSchemaOut,
	})
	if err != nil {
		return failf(stderr, "resolve command config: %v", err)
	}

	switch command {
	case "openapi":
		doc, err := loadDocument(config.IRPath)
		if err != nil {
			return failf(stderr, "load ir: %v", err)
		}
		if err := generateOpenAPI(doc, config.OpenAPIOut); err != nil {
			return failf(stderr, "generate openapi: %v", err)
		}
	case "typespec-compile":
		if err := compileTypeSpecWithOptions(config.TypeSpecDir, config.IROut, config.OpenAPIOut, config.TypeSpecEntrypoint, config.StrictOperationKinds); err != nil {
			return failf(stderr, "compile typespec: %v", err)
		}
	case "server":
		doc, err := loadDocument(config.IRPath)
		if err != nil {
			return failf(stderr, "load ir: %v", err)
		}
		if config.GoPackagePlan != nil {
			if err := generatePartitionedServer(doc, *config.GoPackagePlan, config.CanonicalOpenAPIPath, config.ContractImports); err != nil {
				return failf(stderr, "generate partitioned server: %v", err)
			}
		} else if err := generateHTTPPackage(doc, config.ServerOut, config.ServerPackage, config.RequestModelsOut, config.RequestModelsPackage, config.ClientOut, config.CanonicalOpenAPIPath, config.ContractImports); err != nil {
			return failf(stderr, "generate server: %v", err)
		}
	case "cli":
		if !config.GenerateCLI {
			return failf(stderr, "generate cli: target %q has generate_cli=false", *targetName)
		}
		doc, err := loadDocument(config.IRPath)
		if err != nil {
			return failf(stderr, "load ir: %v", err)
		}
		if err := generateCLI(doc, config.CLIOut, config.CLIPackage); err != nil {
			return failf(stderr, "generate cli: %v", err)
		}
	case "all":
		doc, err := loadDocument(config.IRPath)
		if err != nil {
			return failf(stderr, "load ir: %v", err)
		}
		if config.Kind == "contracts" {
			if err := generateContracts(doc, config); err != nil {
				return failf(stderr, "generate contracts: %v", err)
			}
			return 0
		}
		if config.GoPackagePlan != nil {
			if err := generatePartitionedAll(doc, *config.GoPackagePlan, config); err != nil {
				return failf(stderr, "generate partitioned artifacts: %v", err)
			}
		} else {
			if err := generateHTTPPackage(doc, config.ServerOut, config.ServerPackage, config.RequestModelsOut, config.RequestModelsPackage, config.ClientOut, config.CanonicalOpenAPIPath, config.ContractImports); err != nil {
				return failf(stderr, "generate server: %v", err)
			}
			if config.GenerateCLI {
				if err := generateCLI(doc, config.CLIOut, config.CLIPackage); err != nil {
					return failf(stderr, "generate cli: %v", err)
				}
			}
		}
		if config.FailureTSOut != "" {
			if err := generateFailureTypeScript(doc, config.FailureTSOut); err != nil {
				return failf(stderr, "generate TypeScript failures: %v", err)
			}
		}
	default:
		return failf(stderr, "unsupported command %q\n\n%s", command, topLevelUsage())
	}

	return 0
}

func resolveCommandConfig(command string, manifestPath string, targetName string, defaults commandConfig) (commandConfig, error) {
	if strings.TrimSpace(manifestPath) == "" {
		return defaults, nil
	}

	target, err := loadTargetSpec(manifestPath, targetName)
	if err != nil {
		return commandConfig{}, err
	}

	config := defaults
	config.Kind = target.kind()
	config.TypeSpecDir = target.TypeSpecDir
	config.TypeSpecEntrypoint = target.TypeSpecEntrypoint
	config.StrictOperationKinds = target.StrictOperationKinds
	config.IRPath = target.IROut
	config.IROut = target.IROut
	config.OpenAPIOut = target.OpenAPIOut
	config.CanonicalOpenAPIPath = target.OpenAPIOut
	config.GoModelsOut = target.GoModelsOut
	config.GoModelsPackage = coalesceString(target.GoModelsPackage, defaults.GoModelsPackage)
	config.TSOut = target.TSOut
	config.FailureTSOut = target.FailureTSOut
	config.JSONSchemaOut = target.JSONSchemaOut
	config.ContractImports = target.ContractImports
	if config.Kind == "http" && target.GoOut.usesSinglePackageForm() {
		config.ServerOut = filepath.Join(target.GoOut.Dir, coalesceString(target.GoOut.ServerFile, "server.apigen.gen.go"))
		config.ServerPackage, err = inferOrValidateManifestPackage("go_out", target.GoOut.Package, target.GoOut.Dir)
		if err != nil {
			return commandConfig{}, err
		}
		config.RequestModelsOut = filepath.Join(target.GoOut.Dir, coalesceString(target.GoOut.RequestModelsFile, "request_models.gen.go"))
		config.RequestModelsPackage = config.ServerPackage
		if strings.TrimSpace(target.GoOut.ClientFile) != "" {
			config.ClientOut = filepath.Join(target.GoOut.Dir, target.GoOut.ClientFile)
		}
	} else if config.Kind == "http" && target.GoOut.usesPackagePlanForm() {
		config.GoPackagePlan, err = normalizeGoPackagePlan(*target.GoOut)
		if err != nil {
			return commandConfig{}, err
		}
	} else if config.Kind == "http" {
		return commandConfig{}, fmt.Errorf("target %q must declare go_out", target.Name)
	}
	if target.usesGroupedCLIOut() {
		config.CLIOut = filepath.Join(target.CLIOutGroup.Dir, coalesceString(target.CLIOutGroup.File, "apigen_registry.gen.go"))
		config.CLIPackage, err = inferOrValidateManifestPackage("cli_out", target.CLIOutGroup.Package, target.CLIOutGroup.Dir)
		if err != nil {
			return commandConfig{}, err
		}
		config.GenerateCLI = true
	} else {
		config.CLIOut = ""
		config.CLIPackage = defaults.CLIPackage
		config.GenerateCLI = false
	}

	if err := validateCommandConfig(command, config); err != nil {
		return commandConfig{}, err
	}

	return config, nil
}

func loadTargetSpec(manifestPath string, targetName string) (targetSpec, error) {
	if strings.TrimSpace(targetName) == "" {
		return targetSpec{}, fmt.Errorf("-target is required when -manifest is set")
	}

	content, err := os.ReadFile(filepath.Clean(manifestPath))
	if err != nil {
		return targetSpec{}, fmt.Errorf("read manifest: %w", err)
	}

	var manifest targetManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return targetSpec{}, fmt.Errorf("decode manifest: %w", err)
	}

	manifestDir := filepath.Dir(filepath.Clean(manifestPath))
	for _, target := range manifest.Targets {
		if target.Name != targetName {
			continue
		}
		if err := validateTargetSpec(target); err != nil {
			return targetSpec{}, err
		}
		return resolveTargetPaths(target, manifestDir), nil
	}

	return targetSpec{}, fmt.Errorf("target %q not found in manifest", targetName)
}

func resolveTargetPaths(target targetSpec, baseDir string) targetSpec {
	if target.GoOut != nil {
		target.GoOut.Dir = resolveManifestPath(baseDir, target.GoOut.Dir)
		if target.GoOut.Default != nil {
			target.GoOut.Default.Dir = resolveManifestPath(baseDir, target.GoOut.Default.Dir)
		}
		if target.GoOut.Aggregate != nil {
			target.GoOut.Aggregate.Dir = resolveManifestPath(baseDir, target.GoOut.Aggregate.Dir)
		}
		for namespace, output := range target.GoOut.Packages {
			output.Dir = resolveManifestPath(baseDir, output.Dir)
			target.GoOut.Packages[namespace] = output
		}
	}
	if target.CLIOutGroup != nil {
		target.CLIOutGroup.Dir = resolveManifestPath(baseDir, target.CLIOutGroup.Dir)
	}
	target.TypeSpecDir = resolveManifestPath(baseDir, target.TypeSpecDir)
	target.IROut = resolveManifestPath(baseDir, target.IROut)
	target.OpenAPIOut = resolveManifestPath(baseDir, target.OpenAPIOut)
	target.GoModelsOut = resolveManifestPath(baseDir, target.GoModelsOut)
	target.TSOut = resolveManifestPath(baseDir, target.TSOut)
	target.FailureTSOut = resolveManifestPath(baseDir, target.FailureTSOut)
	target.JSONSchemaOut = resolveManifestPath(baseDir, target.JSONSchemaOut)
	return target
}

func resolveManifestPath(baseDir string, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(baseDir, value)
}

func validateCommandConfig(command string, config commandConfig) error {
	if config.Kind == "" {
		config.Kind = "http"
	}
	switch config.Kind {
	case "http", "contracts":
	default:
		return fmt.Errorf("unsupported target kind %q", config.Kind)
	}
	switch command {
	case "typespec-compile":
		if config.Kind == "contracts" {
			if config.TypeSpecDir == "" || config.IROut == "" {
				return fmt.Errorf("manifest target must declare typespec_dir and ir_out")
			}
			return nil
		}
		if config.TypeSpecDir == "" || config.IROut == "" || config.OpenAPIOut == "" {
			return fmt.Errorf("manifest target must declare typespec_dir, ir_out, and openapi_out")
		}
	case "openapi":
		if config.Kind != "http" {
			return fmt.Errorf("openapi command requires an http target")
		}
		if config.IRPath == "" || config.OpenAPIOut == "" {
			return fmt.Errorf("manifest target must declare ir_out and openapi_out")
		}
	case "server":
		if config.Kind != "http" {
			return fmt.Errorf("server command requires an http target")
		}
		if config.IRPath == "" || config.OpenAPIOut == "" ||
			(config.GoPackagePlan == nil && (config.ServerOut == "" || config.RequestModelsOut == "")) {
			return fmt.Errorf("manifest target must declare ir_out, openapi_out, server_out, and request_models_out")
		}
	case "cli":
		if config.Kind != "http" {
			return fmt.Errorf("cli command requires an http target")
		}
		if config.IRPath == "" || config.CLIOut == "" {
			return fmt.Errorf("manifest target must declare ir_out and cli_out")
		}
	case "all":
		if config.Kind == "contracts" {
			if config.IRPath == "" {
				return fmt.Errorf("manifest target must declare ir_out")
			}
			if config.GoModelsOut == "" && config.TSOut == "" && config.JSONSchemaOut == "" {
				return fmt.Errorf("contracts target must declare at least one of go_models_out, ts_out, or json_schema_out")
			}
			return nil
		}
		if config.IRPath == "" || config.OpenAPIOut == "" ||
			(config.GoPackagePlan == nil && (config.ServerOut == "" || config.RequestModelsOut == "")) {
			return fmt.Errorf("manifest target must declare ir_out, openapi_out, server_out, and request_models_out")
		}
		if config.GenerateCLI && config.CLIOut == "" {
			return fmt.Errorf("manifest target with generate_cli=true must declare cli_out")
		}
	}
	return nil
}

func coalesceString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (target targetSpec) usesGroupedGoOut() bool {
	return target.GoOut != nil
}

func (target targetSpec) usesGroupedCLIOut() bool {
	return target.CLIOutGroup != nil
}

func (target targetSpec) kind() string {
	if strings.TrimSpace(target.Kind) == "" {
		return "http"
	}
	return strings.TrimSpace(target.Kind)
}

func (target targetSpec) usesLegacyGoOut() bool {
	return strings.TrimSpace(target.ServerOut) != "" ||
		strings.TrimSpace(target.ServerPackage) != "" ||
		strings.TrimSpace(target.RequestModelsOut) != "" ||
		strings.TrimSpace(target.RequestModelsPackage) != "" ||
		strings.TrimSpace(target.CompatTypesOut) != "" ||
		strings.TrimSpace(target.CompatTypesPackage) != ""
}

func (target targetSpec) usesLegacyCLIOut() bool {
	return strings.TrimSpace(target.CLIOut) != "" ||
		strings.TrimSpace(target.CLIPackage) != "" ||
		target.GenerateCLI != nil
}

func validateTargetSpec(target targetSpec) error {
	switch target.kind() {
	case "http", "contracts":
	default:
		return fmt.Errorf("target %q has unsupported kind %q", target.Name, target.Kind)
	}
	if target.usesLegacyGoOut() || target.usesLegacyCLIOut() {
		return fmt.Errorf("target %q uses legacy flat manifest fields that are not supported", target.Name)
	}
	if strings.TrimSpace(target.TypeSpecDir) == "" {
		return fmt.Errorf("target %q typespec_dir is required", target.Name)
	}
	if entrypoint := filepath.Clean(strings.TrimSpace(target.TypeSpecEntrypoint)); entrypoint != "." && (filepath.IsAbs(entrypoint) || entrypoint == ".." || strings.HasPrefix(entrypoint, ".."+string(filepath.Separator))) {
		return fmt.Errorf("target %q typespec_entrypoint must stay within typespec_dir", target.Name)
	}
	aliases := map[string]string{}
	for namespace, binding := range target.ContractImports {
		if strings.TrimSpace(namespace) == "" {
			return fmt.Errorf("target %q contract_imports namespace is required", target.Name)
		}
		if strings.TrimSpace(binding.GoPackage) != "" && strings.TrimSpace(binding.GoAlias) == "" {
			return fmt.Errorf("target %q contract import %q requires go_alias", target.Name, namespace)
		}
		if previous, ok := aliases[binding.GoAlias]; binding.GoAlias != "" && ok {
			return fmt.Errorf("target %q contract imports %q and %q share Go alias %q", target.Name, previous, namespace, binding.GoAlias)
		}
		if binding.GoAlias != "" {
			aliases[binding.GoAlias] = namespace
		}
	}
	if target.kind() == "contracts" {
		if strings.TrimSpace(target.IROut) == "" {
			return fmt.Errorf("target %q ir_out is required", target.Name)
		}
		if strings.TrimSpace(target.GoModelsOut) == "" && strings.TrimSpace(target.TSOut) == "" && strings.TrimSpace(target.JSONSchemaOut) == "" {
			return fmt.Errorf("target %q must declare at least one of go_models_out, ts_out, or json_schema_out", target.Name)
		}
		if target.usesGroupedGoOut() || target.usesGroupedCLIOut() || strings.TrimSpace(target.OpenAPIOut) != "" {
			return fmt.Errorf("target %q kind=contracts must not declare go_out, cli_out, or openapi_out", target.Name)
		}
		return nil
	}
	if strings.TrimSpace(target.IROut) == "" || strings.TrimSpace(target.OpenAPIOut) == "" {
		return fmt.Errorf("target %q ir_out and openapi_out are required", target.Name)
	}
	if !target.usesGroupedGoOut() {
		return fmt.Errorf("target %q must declare go_out", target.Name)
	}
	if _, err := normalizeGoPackagePlan(*target.GoOut); err != nil {
		return fmt.Errorf("target %q %w", target.Name, err)
	}
	if target.usesGroupedCLIOut() && strings.TrimSpace(target.CLIOutGroup.Dir) == "" {
		return fmt.Errorf("target %q cli_out.dir is required", target.Name)
	}
	return nil
}

func emitterContractImports(values map[string]contractImportSpec) contractimport.Bindings {
	out := make(contractimport.Bindings, len(values))
	for namespace, value := range values {
		out[namespace] = contractimport.Binding{
			GoPackage: value.GoPackage, GoAlias: value.GoAlias,
			TypeScriptModule: value.TypeScriptModule, ExactNamespace: value.ExactNamespace,
		}
	}
	return out
}

func inferOrValidateManifestPackage(fieldName string, explicit string, dir string) (string, error) {
	packageName := strings.TrimSpace(explicit)
	if packageName == "" {
		packageName = filepath.Base(filepath.Clean(dir))
	}
	if _, keyword := goKeywords[packageName]; !goPackagePattern.MatchString(packageName) || keyword {
		return "", fmt.Errorf("%s: invalid inferred go package %q", fieldName, packageName)
	}
	return packageName, nil
}

func compileTypeSpec(typeSpecDir string, irOutPath string, openAPIOutPath string, configuredEntrypoint ...string) error {
	entrypoint := ""
	if len(configuredEntrypoint) > 0 {
		entrypoint = configuredEntrypoint[0]
	}
	return compileTypeSpecWithOptions(typeSpecDir, irOutPath, openAPIOutPath, entrypoint, false)
}

func compileTypeSpecWithOptions(typeSpecDir string, irOutPath string, openAPIOutPath string, configuredEntrypoint string, strictOperationKinds bool) error {
	absTypeSpecDir, err := filepath.Abs(typeSpecDir)
	if err != nil {
		return fmt.Errorf("resolve typespec dir: %w", err)
	}
	absIROutPath, err := filepath.Abs(irOutPath)
	if err != nil {
		return fmt.Errorf("resolve ir output path: %w", err)
	}
	absOpenAPIOutPath := ""
	if strings.TrimSpace(openAPIOutPath) != "" {
		absOpenAPIOutPath, err = filepath.Abs(openAPIOutPath)
		if err != nil {
			return fmt.Errorf("resolve openapi output path: %w", err)
		}
	}

	pkg, err := resolveTypeSpecPackage()
	if err != nil {
		return err
	}
	if err := ensureTypeSpecToolchain(pkg); err != nil {
		return err
	}
	stagedTypeSpecDir, cleanup, err := stageTypeSpecProject(absTypeSpecDir, pkg)
	if err != nil {
		return err
	}
	defer cleanup()
	compileTarget := stagedTypeSpecDir
	if strings.TrimSpace(configuredEntrypoint) != "" {
		entrypoint := filepath.Clean(configuredEntrypoint)
		if filepath.IsAbs(entrypoint) || entrypoint == ".." || strings.HasPrefix(entrypoint, ".."+string(filepath.Separator)) {
			return fmt.Errorf("typespec_entrypoint must stay within typespec_dir")
		}
		compileTarget = filepath.Join(stagedTypeSpecDir, entrypoint)
		if _, err := os.Stat(compileTarget); err != nil {
			return fmt.Errorf("stat typespec entrypoint: %w", err)
		}
	}

	tempIRPath, err := tempOutputPath(absIROutPath)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tempIRPath) }()
	tempOpenAPIPath := ""
	if absOpenAPIOutPath != "" {
		tempOpenAPIPath, err = tempOutputPath(absOpenAPIOutPath)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(tempOpenAPIPath) }()
	}

	tsp := filepath.Join(pkg.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js")
	args := []string{
		tsp,
		"compile",
		compileTarget,
		"--import",
		pkg.Dir,
		"--emit",
		pkg.Dir,
		"--option",
		"@yacobolo/apigen.output-file=" + tempIRPath,
		"--option",
		"@yacobolo/apigen.base-path=/",
	}
	if strictOperationKinds {
		args = append(args, "--option", "@yacobolo/apigen.require-explicit-operation-kind=true")
	}
	cmd := exec.Command(
		"node",
		args...,
	)
	cmd.Dir = pkg.Dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run tsp compile: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	doc, err := loadDocument(tempIRPath)
	if err != nil {
		return err
	}
	if err := writeJSONDocument(tempIRPath, doc); err != nil {
		return err
	}
	if tempOpenAPIPath != "" {
		if err := generateOpenAPI(doc, tempOpenAPIPath); err != nil {
			return err
		}
	}
	if err := replaceFile(tempIRPath, absIROutPath); err != nil {
		return err
	}
	if tempOpenAPIPath != "" {
		if err := replaceFile(tempOpenAPIPath, absOpenAPIOutPath); err != nil {
			return err
		}
	}
	return nil
}

func stageTypeSpecProject(typeSpecDir string, pkg typeSpecPackage) (string, func(), error) {
	info, err := os.Stat(typeSpecDir)
	if err != nil {
		return "", nil, fmt.Errorf("stat typespec dir: %w", err)
	}
	if !info.IsDir() {
		return "", nil, fmt.Errorf("typespec dir %s is not a directory", typeSpecDir)
	}
	tempDir, err := os.MkdirTemp("", "apigen-typespec-project-*")
	if err != nil {
		return "", nil, fmt.Errorf("create staged typespec project: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	stagedDir := filepath.Join(tempDir, "project")
	if err := copyTypeSpecProject(typeSpecDir, stagedDir); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := linkTypeSpecPackage(stagedDir, filepath.Join("@typespec", "http"), filepath.Join(pkg.Dir, "node_modules", "@typespec", "http")); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := linkTypeSpecPackage(stagedDir, filepath.Join("@typespec", "openapi"), filepath.Join(pkg.Dir, "node_modules", "@typespec", "openapi")); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := linkTypeSpecPackage(stagedDir, filepath.Join("@yacobolo", "apigen"), pkg.Dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return stagedDir, cleanup, nil
}

func copyTypeSpecProject(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("resolve staged typespec path: %w", err)
		}
		if rel != "." && entry.IsDir() && entry.Name() == "node_modules" {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				return fmt.Errorf("create staged typespec directory: %w", err)
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read staged typespec symlink: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return fmt.Errorf("create staged typespec symlink directory: %w", err)
			}
			if err := os.Symlink(linkTarget, target); err != nil {
				return fmt.Errorf("create staged typespec symlink: %w", err)
			}
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read staged typespec file: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("create staged typespec file directory: %w", err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return fmt.Errorf("write staged typespec file: %w", err)
		}
		return nil
	})
}

func linkTypeSpecPackage(projectDir string, modulePath string, packageDir string) error {
	if _, err := os.Stat(packageDir); err != nil {
		return fmt.Errorf("stat bundled typespec package %s: %w", modulePath, err)
	}
	target := filepath.Join(projectDir, "node_modules", modulePath)
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("create staged node_modules for %s: %w", modulePath, err)
	}
	if err := os.Symlink(packageDir, target); err != nil {
		return fmt.Errorf("link staged typespec package %s: %w", modulePath, err)
	}
	return nil
}

func resolveTypeSpecPackage() (typeSpecPackage, error) {
	if override := strings.TrimSpace(os.Getenv(typeSpecPackageDirEnv)); override != "" {
		dir, err := filepath.Abs(override)
		if err != nil {
			return typeSpecPackage{}, fmt.Errorf("resolve %s: %w", typeSpecPackageDirEnv, err)
		}
		return typeSpecPackage{Dir: dir}, nil
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return typeSpecPackage{}, fmt.Errorf("resolve user cache dir: %w", err)
	}
	return installBundledTypeSpecPackage(cacheRoot)
}

func installBundledTypeSpecPackage(cacheRoot string) (typeSpecPackage, error) {
	hash, err := bundledTypeSpecPackageHash()
	if err != nil {
		return typeSpecPackage{}, err
	}
	packageDir := filepath.Join(cacheRoot, "apigen", "typespec", hash)
	marker := filepath.Join(packageDir, ".apigen-bundle-sha256")
	if content, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(content)) == hash {
		return typeSpecPackage{Dir: packageDir, Managed: true}, nil
	}
	if err := os.RemoveAll(packageDir); err != nil {
		return typeSpecPackage{}, fmt.Errorf("clear typespec package cache: %w", err)
	}
	if err := fs.WalkDir(typespecbundle.Package, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		target := filepath.Join(packageDir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		content, err := typespecbundle.Package.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read bundled typespec package file %q: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("create bundled typespec package directory: %w", err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return fmt.Errorf("write bundled typespec package file %q: %w", path, err)
		}
		return nil
	}); err != nil {
		return typeSpecPackage{}, fmt.Errorf("install bundled typespec package: %w", err)
	}
	if err := os.WriteFile(marker, []byte(hash+"\n"), 0o600); err != nil {
		return typeSpecPackage{}, fmt.Errorf("write typespec package cache marker: %w", err)
	}
	return typeSpecPackage{Dir: packageDir, Managed: true}, nil
}

func bundledTypeSpecPackageHash() (string, error) {
	hash := sha256.New()
	if err := fs.WalkDir(typespecbundle.Package, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := typespecbundle.Package.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read bundled typespec package file %q: %w", path, err)
		}
		hash.Write([]byte(path))
		hash.Write([]byte{0})
		hash.Write(content)
		hash.Write([]byte{0})
		return nil
	}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}

func ensureTypeSpecToolchain(pkg typeSpecPackage) error {
	tsp := filepath.Join(pkg.Dir, "node_modules", "@typespec", "compiler", "cmd", "tsp.js")
	if _, err := os.Stat(tsp); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat typespec compiler: %w", err)
		}
		if !pkg.Managed {
			return fmt.Errorf("typespec compiler not found in %s; run npm ci or unset %s to use the bundled cache", pkg.Dir, typeSpecPackageDirEnv)
		}
		if err := runTypeSpecPackageCommand(pkg.Dir, "npm", "ci", "--omit=dev"); err != nil {
			return err
		}
	}
	dist := filepath.Join(pkg.Dir, "dist", "src", "index.js")
	if _, err := os.Stat(dist); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat apigen typespec emitter: %w", err)
		}
		return fmt.Errorf("apigen typespec emitter not found in %s; run npm run build before using %s", pkg.Dir, typeSpecPackageDirEnv)
	}
	return nil
}

func runTypeSpecPackageCommand(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func tempOutputPath(finalPath string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(finalPath), "."+filepath.Base(finalPath)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp output for %s: %w", finalPath, err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close temp output for %s: %w", finalPath, err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("prepare temp output for %s: %w", finalPath, err)
	}
	return path, nil
}

func replaceFile(tempPath string, finalPath string) error {
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("replace output %s: %w", finalPath, err)
	}
	return nil
}

func generateOpenAPI(doc ir.Document, outPath string) error {
	b, err := openapiemit.EmitYAML(doc, openapiemit.Options{})
	if err != nil {
		return fmt.Errorf("emit openapi: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(outPath, b, 0o600); err != nil {
		return fmt.Errorf("write openapi output: %w", err)
	}
	return nil
}

func generateServer(doc ir.Document, outPath string, serverPackage string, requestModelsOutPath string, requestModelsPackage string, canonicalOpenAPIPath string, configuredImports ...map[string]contractImportSpec) error {
	var imports map[string]contractImportSpec
	if len(configuredImports) > 0 {
		imports = configuredImports[0]
	}
	return generateHTTPPackage(doc, outPath, serverPackage, requestModelsOutPath, requestModelsPackage, "", canonicalOpenAPIPath, imports)
}

func generateHTTPPackage(
	doc ir.Document,
	serverOutPath string,
	serverPackage string,
	requestModelsOutPath string,
	requestModelsPackage string,
	clientOutPath string,
	canonicalOpenAPIPath string,
	configuredImports map[string]contractImportSpec,
) error {
	if err := servergoemit.ValidateOperationIDs(doc); err != nil {
		return fmt.Errorf("validate operation ids: %w", err)
	}
	embeddedSpecJSON, err := loadOpenAPIAsJSON(canonicalOpenAPIPath)
	if err != nil {
		return fmt.Errorf("load canonical openapi: %w", err)
	}
	b, err := servergoemit.Emit(doc, servergoemit.Options{
		PackageName:             serverPackage,
		EmbeddedOpenAPISpecJSON: embeddedSpecJSON,
	})
	if err != nil {
		return fmt.Errorf("emit server go: %w", err)
	}
	formatted, err := format.Source(b)
	if err != nil {
		return fmt.Errorf("format server go output: %w", err)
	}
	requestModels, err := requestmodelgoemit.Emit(doc, requestmodelgoemit.Options{
		PackageName: requestModelsPackage, ContractImports: emitterContractImports(configuredImports),
	})
	if err != nil {
		return fmt.Errorf("emit request models go: %w", err)
	}
	formattedRequestModels, err := format.Source(requestModels)
	if err != nil {
		return fmt.Errorf("format request models go output: %w", err)
	}
	changes := []generatedOutputChange{
		{Path: serverOutPath, Content: formatted},
		{Path: requestModelsOutPath, Content: formattedRequestModels},
	}
	if clientOutPath != "" {
		client, err := clientgoemit.Emit(doc, clientgoemit.Options{PackageName: serverPackage})
		if err != nil {
			return fmt.Errorf("emit client go: %w", err)
		}
		formattedClient, err := format.Source(client)
		if err != nil {
			return fmt.Errorf("format client go output: %w", err)
		}
		changes = append(changes, generatedOutputChange{Path: clientOutPath, Content: formattedClient})
	}
	if err := applyGeneratedOutputChanges(changes); err != nil {
		return fmt.Errorf("apply generated outputs: %w", err)
	}
	return nil
}

func generateCLI(doc ir.Document, outPath string, packageName string) error {
	b, err := cligoemit.Emit(doc, cligoemit.Options{PackageName: packageName})
	if err != nil {
		return fmt.Errorf("emit cli go: %w", err)
	}
	formatted, err := format.Source(b)
	if err != nil {
		return fmt.Errorf("format cli go output: %w", err)
	}
	if err := writeFile(outPath, formatted); err != nil {
		return err
	}
	return nil
}

func generateFailureTypeScript(doc ir.Document, outPath string) error {
	b, err := failuretsemit.Emit(doc)
	if err != nil {
		return fmt.Errorf("emit TypeScript failures: %w", err)
	}
	if err := writeFile(outPath, b); err != nil {
		return err
	}
	return nil
}

func generateContracts(doc ir.Document, config commandConfig) error {
	if len(doc.Contracts) == 0 {
		return fmt.Errorf("document does not declare contracts")
	}
	if config.GoModelsOut != "" {
		b, err := modelgoemit.Emit(doc, modelgoemit.Options{PackageName: config.GoModelsPackage, ContractImports: emitterContractImports(config.ContractImports)})
		if err != nil {
			return fmt.Errorf("emit go models: %w", err)
		}
		formatted, err := format.Source(b)
		if err != nil {
			return fmt.Errorf("format go models output: %w", err)
		}
		if err := writeFile(config.GoModelsOut, formatted); err != nil {
			return err
		}
	}
	if config.TSOut != "" {
		b, err := modeltsemit.Emit(doc, modeltsemit.Options{ContractImports: emitterContractImports(config.ContractImports)})
		if err != nil {
			return fmt.Errorf("emit typescript models: %w", err)
		}
		if err := writeFile(config.TSOut, b); err != nil {
			return err
		}
	}
	if config.JSONSchemaOut != "" {
		b, err := jsonschemaemit.Emit(doc, jsonschemaemit.Options{})
		if err != nil {
			return fmt.Errorf("emit json schema: %w", err)
		}
		if err := writeFile(config.JSONSchemaOut, b); err != nil {
			return err
		}
	}
	return nil
}

func loadDocument(path string) (ir.Document, error) {
	doc, err := ir.Load(path)
	if err != nil {
		return ir.Document{}, fmt.Errorf("load ir document: %w", err)
	}
	return doc, nil
}

func writeJSONDocument(path string, doc ir.Document) error {
	content, err := json.MarshalIndent(documentJSON(doc), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ir document: %w", err)
	}
	return writeFile(path, content)
}

func documentJSON(doc ir.Document) map[string]any {
	out := map[string]any{
		"schema_version": doc.SchemaVersion,
		"api":            doc.API,
		"info":           doc.Info,
	}
	if !openAPIEmpty(doc.OpenAPI) {
		out["openapi"] = doc.OpenAPI
	}
	if len(doc.Servers) > 0 {
		out["servers"] = doc.Servers
	}
	if len(doc.Tags) > 0 {
		out["tags"] = doc.Tags
	}
	if len(doc.Schemas) > 0 {
		out["schemas"] = doc.Schemas
	}
	if len(doc.Contracts) > 0 {
		out["contracts"] = doc.Contracts
	}
	if len(doc.Endpoints) > 0 {
		out["endpoints"] = doc.Endpoints
	}
	if doc.TransportErrors != nil {
		out["transport_errors"] = doc.TransportErrors
	}
	if len(doc.Extensions) > 0 {
		out["extensions"] = doc.Extensions
	}
	return out
}

func openAPIEmpty(value ir.OpenAPI) bool {
	return value.Version == "" &&
		len(value.TagOrder) == 0 &&
		len(value.Security) == 0 &&
		len(value.SecuritySchemes) == 0
}

func writeFile(outPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	content = bytes.TrimSpace(content)
	content = append(content, '\n')
	if err := os.WriteFile(outPath, content, 0o600); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func loadOpenAPIAsJSON(path string) (string, error) {
	//nolint:gosec // Path comes from the checked-in generation pipeline inputs.
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read openapi file: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return "", fmt.Errorf("decode openapi yaml: %w", err)
	}
	marshaled, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal openapi json: %w", err)
	}
	return string(marshaled), nil
}

func topLevelUsage() string {
	return `Usage:
  apigen <command> [flags]

Commands:
  typespec-compile TypeSpec -> JSON IR + OpenAPI
  openapi        JSON IR -> OpenAPI
  server         JSON IR -> server + request models
  cli            JSON IR -> Cobra registry
  all            JSON IR -> all Go outputs

Examples:
  apigen typespec-compile -typespec-dir api/typespec -ir-out gen/json-ir.json -openapi-out gen/openapi.yaml
  apigen all -ir gen/json-ir.json -canonical-openapi gen/openapi.yaml -server-out internal/api/server.apigen.gen.go

Use "apigen <command> -h" for command-specific flags.
`
}

func writeTopLevelUsage(w io.Writer) {
	_, _ = io.WriteString(w, topLevelUsage())
}

func isTopLevelHelp(value string) bool {
	switch strings.TrimSpace(value) {
	case "-h", "--help", "help":
		return true
	default:
		return false
	}
}

func failf(w io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(w, format+"\n", args...)
	return 1
}
