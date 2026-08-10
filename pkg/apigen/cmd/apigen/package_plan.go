package main

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type unmatchedNamespacePolicy string

const (
	unmatchedNamespaceDefault unmatchedNamespacePolicy = "default"
	unmatchedNamespaceError   unmatchedNamespacePolicy = "error"
)

type resolvedGoPackageOutput struct {
	Dir               string
	Package           string
	ImportPath        string
	ServerFile        string
	RequestModelsFile string
	ClientFile        string
}

type namespaceGoPackageOutput struct {
	Namespace string
	Output    resolvedGoPackageOutput
}

type goPackagePlan struct {
	Default   *resolvedGoPackageOutput
	Aggregate *resolvedGoPackageOutput
	Packages  []namespaceGoPackageOutput
	Unmatched unmatchedNamespacePolicy
}

func (spec *goOutputSpec) usesSinglePackageForm() bool {
	return spec != nil && (strings.TrimSpace(spec.Dir) != "" ||
		strings.TrimSpace(spec.Package) != "" ||
		strings.TrimSpace(spec.ServerFile) != "" ||
		strings.TrimSpace(spec.RequestModelsFile) != "" ||
		strings.TrimSpace(spec.ClientFile) != "")
}

func (spec *goOutputSpec) usesPackagePlanForm() bool {
	return spec != nil && (spec.Default != nil ||
		spec.Aggregate != nil ||
		len(spec.Packages) > 0 ||
		strings.TrimSpace(spec.Unmatched) != "")
}

func normalizeGoPackagePlan(spec goOutputSpec) (*goPackagePlan, error) {
	singlePackage := spec.usesSinglePackageForm()
	packagePlan := spec.usesPackagePlanForm()
	if singlePackage && packagePlan {
		return nil, fmt.Errorf("go_out cannot mix dir/package/file fields with default/aggregate/packages/unmatched")
	}
	if singlePackage {
		if strings.TrimSpace(spec.Dir) == "" {
			return nil, fmt.Errorf("go_out.dir is required")
		}
		if _, err := resolveGoPackageOutput("go_out", goPackageOutputSpec{
			Dir:               spec.Dir,
			Package:           spec.Package,
			ServerFile:        spec.ServerFile,
			RequestModelsFile: spec.RequestModelsFile,
			ClientFile:        spec.ClientFile,
		}, false); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if !packagePlan {
		return nil, fmt.Errorf("go_out must declare dir or a package plan")
	}
	if len(spec.Packages) == 0 {
		return nil, fmt.Errorf("go_out.packages must declare at least one namespace")
	}

	policy := unmatchedNamespacePolicy(strings.TrimSpace(spec.Unmatched))
	if policy != unmatchedNamespaceDefault && policy != unmatchedNamespaceError {
		return nil, fmt.Errorf("go_out.unmatched must be one of default or error")
	}
	if policy == unmatchedNamespaceDefault && spec.Default == nil {
		return nil, fmt.Errorf("go_out.unmatched=default requires go_out.default")
	}
	if policy == unmatchedNamespaceError && spec.Default != nil {
		return nil, fmt.Errorf("go_out.default requires go_out.unmatched=default")
	}

	plan := &goPackagePlan{Unmatched: policy}
	outputPackages := map[string]resolvedGoPackageOutput{}
	outputImportPaths := map[string]string{}
	recordImportPath := func(output resolvedGoPackageOutput) error {
		dir := filepath.Clean(output.Dir)
		if previousDir, exists := outputImportPaths[output.ImportPath]; exists && previousDir != dir {
			return fmt.Errorf("go_out import_path %q resolves to multiple directories", output.ImportPath)
		}
		outputImportPaths[output.ImportPath] = dir
		return nil
	}
	if spec.Default != nil {
		output, err := resolveGoPackageOutput("go_out.default", *spec.Default, true)
		if err != nil {
			return nil, err
		}
		if err := recordImportPath(output); err != nil {
			return nil, err
		}
		plan.Default = &output
		outputPackages[filepath.Clean(output.Dir)] = output
	}

	namespaces := make([]string, 0, len(spec.Packages))
	normalizedNamespaces := make(map[string]string, len(spec.Packages))
	for authoredNamespace := range spec.Packages {
		namespace := strings.TrimSpace(authoredNamespace)
		if namespace == "" {
			return nil, fmt.Errorf("go_out.packages namespace is required")
		}
		if previous, exists := normalizedNamespaces[namespace]; exists {
			return nil, fmt.Errorf("go_out.packages namespaces %q and %q normalize to the same value", previous, authoredNamespace)
		}
		normalizedNamespaces[namespace] = authoredNamespace
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)

	for _, namespace := range namespaces {
		outputSpec := spec.Packages[normalizedNamespaces[namespace]]
		output, err := resolveGoPackageOutput(fmt.Sprintf("go_out.packages[%q]", namespace), outputSpec, true)
		if err != nil {
			return nil, err
		}
		dir := filepath.Clean(output.Dir)
		if previous, exists := outputPackages[dir]; exists {
			if previous.Package != output.Package {
				return nil, fmt.Errorf("go_out packages resolve to the same directory with different package names")
			}
			if previous != output {
				return nil, fmt.Errorf("go_out packages resolve to the same directory with inconsistent output settings")
			}
		}
		if err := recordImportPath(output); err != nil {
			return nil, err
		}
		outputPackages[dir] = output
		plan.Packages = append(plan.Packages, namespaceGoPackageOutput{
			Namespace: namespace,
			Output:    output,
		})
	}

	if spec.Aggregate != nil {
		output, err := resolveGoPackageOutput("go_out.aggregate", *spec.Aggregate, false)
		if err != nil {
			return nil, err
		}
		if output.ClientFile != "" {
			return nil, fmt.Errorf("go_out.aggregate.client_file is not supported; configure typed clients on package outputs")
		}
		if _, exists := outputPackages[filepath.Clean(output.Dir)]; exists {
			return nil, fmt.Errorf("go_out.aggregate must use a directory separate from package outputs")
		}
		plan.Aggregate = &output
	}
	return plan, nil
}

func resolveGoPackageOutput(fieldName string, spec goPackageOutputSpec, requireImportPath bool) (resolvedGoPackageOutput, error) {
	if strings.TrimSpace(spec.Dir) == "" {
		return resolvedGoPackageOutput{}, fmt.Errorf("%s.dir is required", fieldName)
	}
	packageName, err := inferOrValidateManifestPackage(fieldName, spec.Package, spec.Dir)
	if err != nil {
		return resolvedGoPackageOutput{}, err
	}
	importPath, err := resolveGoImportPath(fieldName, spec.ImportPath, requireImportPath)
	if err != nil {
		return resolvedGoPackageOutput{}, err
	}
	serverFile, err := resolveGeneratedFileName(fieldName+".server_file", spec.ServerFile, "server.apigen.gen.go")
	if err != nil {
		return resolvedGoPackageOutput{}, err
	}
	requestModelsFile, err := resolveGeneratedFileName(fieldName+".request_models_file", spec.RequestModelsFile, "request_models.gen.go")
	if err != nil {
		return resolvedGoPackageOutput{}, err
	}
	clientFile := ""
	if spec.ClientFile != "" {
		clientFile, err = resolveGeneratedFileName(fieldName+".client_file", spec.ClientFile, "")
		if err != nil {
			return resolvedGoPackageOutput{}, err
		}
	}
	if serverFile == requestModelsFile {
		return resolvedGoPackageOutput{}, fmt.Errorf("%s server_file and request_models_file must be different", fieldName)
	}
	if clientFile != "" && serverFile == clientFile {
		return resolvedGoPackageOutput{}, fmt.Errorf("%s server_file and client_file must be different", fieldName)
	}
	if clientFile != "" && requestModelsFile == clientFile {
		return resolvedGoPackageOutput{}, fmt.Errorf("%s request_models_file and client_file must be different", fieldName)
	}
	return resolvedGoPackageOutput{
		Dir:               filepath.Clean(spec.Dir),
		Package:           packageName,
		ImportPath:        importPath,
		ServerFile:        serverFile,
		RequestModelsFile: requestModelsFile,
		ClientFile:        clientFile,
	}, nil
}

func resolveGeneratedFileName(fieldName, authored, fallback string) (string, error) {
	name := coalesceString(authored, fallback)
	if filepath.IsAbs(name) || filepath.Clean(name) != name || filepath.Base(name) != name || name == "." || name == ".." {
		return "", fmt.Errorf("%s must be a filename within its output directory", fieldName)
	}
	return name, nil
}

func resolveGoImportPath(fieldName, authored string, required bool) (string, error) {
	if authored == "" {
		if required {
			return "", fmt.Errorf("%s.import_path is required", fieldName)
		}
		return "", nil
	}
	if strings.TrimSpace(authored) != authored ||
		authored == "." ||
		authored == ".." ||
		path.IsAbs(authored) ||
		strings.Contains(authored, `\`) ||
		strings.HasSuffix(authored, "/") ||
		path.Clean(authored) != authored {
		return "", fmt.Errorf("%s.import_path must be a canonical Go import path", fieldName)
	}
	for _, character := range authored {
		if character == '/' || unicode.IsLetter(character) || unicode.IsDigit(character) ||
			strings.ContainsRune("-._~+", character) {
			continue
		}
		return "", fmt.Errorf("%s.import_path must be a canonical Go import path", fieldName)
	}
	return authored, nil
}
