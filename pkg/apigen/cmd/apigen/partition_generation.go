package main

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	aggregategoemit "github.com/Yacobolo/toolbelt/apigen/emit/aggregatego"
	clientgoemit "github.com/Yacobolo/toolbelt/apigen/emit/clientgo"
	cligoemit "github.com/Yacobolo/toolbelt/apigen/emit/cligo"
	requestmodelgoemit "github.com/Yacobolo/toolbelt/apigen/emit/requestmodelgo"
	servergoemit "github.com/Yacobolo/toolbelt/apigen/emit/servergo"
	"github.com/Yacobolo/toolbelt/apigen/ir"
)

type generatedOutputChange struct {
	Path    string
	Content []byte
	Remove  bool
}

func generatePartitionedServer(doc ir.Document, plan goPackagePlan, canonicalOpenAPIPath string, configuredImports map[string]contractImportSpec) error {
	changes, err := renderPartitionedServerDocument(doc, plan, canonicalOpenAPIPath, configuredImports)
	if err != nil {
		return err
	}
	if err := applyGeneratedOutputChanges(changes); err != nil {
		return fmt.Errorf("apply generated outputs: %w", err)
	}
	return nil
}

func generatePartitionedAll(doc ir.Document, plan goPackagePlan, config commandConfig) error {
	changes, err := renderPartitionedServerDocument(doc, plan, config.CanonicalOpenAPIPath, config.ContractImports)
	if err != nil {
		return err
	}
	if config.GenerateCLI {
		cli, err := cligoemit.Emit(doc, cligoemit.Options{PackageName: config.CLIPackage})
		if err != nil {
			return fmt.Errorf("emit global cli: %w", err)
		}
		formattedCLI, err := format.Source(cli)
		if err != nil {
			return fmt.Errorf("format global cli: %w", err)
		}
		changes = append(changes, generatedOutputChange{Path: config.CLIOut, Content: formattedCLI})
	}
	if err := applyGeneratedOutputChanges(changes); err != nil {
		return fmt.Errorf("apply generated outputs: %w", err)
	}
	return nil
}

func renderPartitionedServerDocument(
	doc ir.Document,
	plan goPackagePlan,
	canonicalOpenAPIPath string,
	configuredImports map[string]contractImportSpec,
) ([]generatedOutputChange, error) {
	partitions, err := planGoPackagePartitions(doc, plan, configuredImports)
	if err != nil {
		return nil, fmt.Errorf("plan packages: %w", err)
	}
	projections, err := projectGoPackagePartitions(doc, partitions, configuredImports)
	if err != nil {
		return nil, fmt.Errorf("project packages: %w", err)
	}
	changes, err := renderPartitionedServer(projections, configuredImports)
	if err != nil {
		return nil, err
	}
	embeddedOpenAPI := ""
	if plan.Aggregate != nil {
		embeddedOpenAPI, err = loadOpenAPIAsJSON(canonicalOpenAPIPath)
		if err != nil {
			return nil, fmt.Errorf("load canonical openapi for aggregate: %w", err)
		}
	}
	aggregateChange, ok, err := renderAggregateServer(projections, plan.Aggregate, embeddedOpenAPI)
	if err != nil {
		return nil, err
	}
	if ok {
		changes = append(changes, aggregateChange)
	}
	return changes, nil
}

func renderPartitionedServer(projections []goPackageProjection, configuredImports map[string]contractImportSpec) ([]generatedOutputChange, error) {
	changes := make([]generatedOutputChange, 0, len(projections)*3)
	for _, projection := range projections {
		output := projection.Partition.Output
		imports, err := partitionContractImports(projection, configuredImports)
		if err != nil {
			return nil, fmt.Errorf("resolve imports for %s: %w", output.ImportPath, err)
		}
		models, err := requestmodelgoemit.Emit(projection.Document, requestmodelgoemit.Options{
			PackageName:     output.Package,
			ContractImports: emitterContractImports(imports),
		})
		if err != nil {
			return nil, fmt.Errorf("emit request models for %s: %w", output.ImportPath, err)
		}
		formattedModels, err := format.Source(models)
		if err != nil {
			return nil, fmt.Errorf("format request models for %s: %w", output.ImportPath, err)
		}
		changes = append(changes, generatedOutputChange{
			Path:    filepath.Join(output.Dir, output.RequestModelsFile),
			Content: formattedModels,
		})

		serverPath := filepath.Join(output.Dir, output.ServerFile)
		if len(projection.Document.Endpoints) == 0 {
			changes = append(changes, generatedOutputChange{Path: serverPath, Remove: true})
			if output.ClientFile != "" {
				changes = append(changes, generatedOutputChange{
					Path:   filepath.Join(output.Dir, output.ClientFile),
					Remove: true,
				})
			}
			continue
		}
		if err := servergoemit.ValidateOperationIDs(projection.Document); err != nil {
			return nil, fmt.Errorf("validate operation ids for %s: %w", output.ImportPath, err)
		}
		server, err := servergoemit.Emit(projection.Document, servergoemit.Options{
			PackageName: output.Package,
		})
		if err != nil {
			return nil, fmt.Errorf("emit server for %s: %w", output.ImportPath, err)
		}
		formattedServer, err := format.Source(server)
		if err != nil {
			return nil, fmt.Errorf("format server for %s: %w", output.ImportPath, err)
		}
		changes = append(changes, generatedOutputChange{
			Path:    serverPath,
			Content: formattedServer,
		})
		if output.ClientFile != "" {
			client, err := clientgoemit.Emit(projection.Document, clientgoemit.Options{
				PackageName: output.Package,
			})
			if err != nil {
				return nil, fmt.Errorf("emit client for %s: %w", output.ImportPath, err)
			}
			formattedClient, err := format.Source(client)
			if err != nil {
				return nil, fmt.Errorf("format client for %s: %w", output.ImportPath, err)
			}
			changes = append(changes, generatedOutputChange{
				Path:    filepath.Join(output.Dir, output.ClientFile),
				Content: formattedClient,
			})
		}
	}
	return changes, nil
}

func renderAggregateServer(
	projections []goPackageProjection,
	output *resolvedGoPackageOutput,
	embeddedOpenAPI string,
) (generatedOutputChange, bool, error) {
	if output == nil {
		return generatedOutputChange{}, false, nil
	}
	path := filepath.Join(output.Dir, output.ServerFile)
	packages := make([]aggregategoemit.ServerPackage, 0, len(projections))
	for _, projection := range projections {
		if len(projection.Document.Endpoints) == 0 {
			continue
		}
		packages = append(packages, aggregategoemit.ServerPackage{
			Name:        aggregatePartitionName(projection.Partition),
			ImportPath:  projection.Partition.Output.ImportPath,
			PackageName: projection.Partition.Output.Package,
			HasTools:    projectionHasTools(projection),
		})
	}
	if len(packages) == 0 {
		return generatedOutputChange{Path: path, Remove: true}, true, nil
	}
	content, err := aggregategoemit.Emit(aggregategoemit.Options{
		PackageName:             output.Package,
		EmbeddedOpenAPISpecJSON: embeddedOpenAPI,
		Packages:                packages,
	})
	if err != nil {
		return generatedOutputChange{}, false, fmt.Errorf("emit aggregate server: %w", err)
	}
	formatted, err := format.Source(content)
	if err != nil {
		return generatedOutputChange{}, false, fmt.Errorf("format aggregate server: %w", err)
	}
	return generatedOutputChange{Path: path, Content: formatted}, true, nil
}

func projectionHasTools(projection goPackageProjection) bool {
	for _, endpoint := range projection.Document.Endpoints {
		if endpoint.Tool != nil {
			return true
		}
	}
	return false
}

func aggregatePartitionName(partition goPackagePartition) string {
	if len(partition.Namespaces) == 1 {
		parts := strings.Split(partition.Namespaces[0], ".")
		if name := strings.TrimSpace(parts[len(parts)-1]); name != "" {
			return name
		}
	}
	return partition.Output.Package
}

func applyGeneratedOutputChanges(changes []generatedOutputChange) error {
	ordered := append([]generatedOutputChange(nil), changes...)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].Path < ordered[right].Path
	})
	seen := map[string]struct{}{}
	staged := make(map[string]string, len(ordered))
	cleanup := func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
	}
	defer cleanup()

	for _, change := range ordered {
		path := filepath.Clean(change.Path)
		if path == "." || path == "" {
			return fmt.Errorf("generated output path is required")
		}
		if _, exists := seen[path]; exists {
			return fmt.Errorf("generated output path %s is declared more than once", path)
		}
		seen[path] = struct{}{}
		if change.Remove && len(change.Content) > 0 {
			return fmt.Errorf("generated output %s cannot be written and removed", path)
		}
	}

	for _, change := range ordered {
		path := filepath.Clean(change.Path)
		if change.Remove {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("create output directory for %s: %w", path, err)
		}
		file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
		if err != nil {
			return fmt.Errorf("stage output %s: %w", path, err)
		}
		tempPath := file.Name()
		content := normalizedGeneratedContent(change.Content)
		if _, err := file.Write(content); err != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
			return fmt.Errorf("stage output %s: %w", path, err)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			_ = os.Remove(tempPath)
			return fmt.Errorf("set staged output permissions for %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			_ = os.Remove(tempPath)
			return fmt.Errorf("close staged output %s: %w", path, err)
		}
		staged[path] = tempPath
	}

	for _, change := range ordered {
		path := filepath.Clean(change.Path)
		if change.Remove {
			continue
		}
		tempPath := staged[path]
		if err := os.Rename(tempPath, path); err != nil {
			return fmt.Errorf("replace generated output %s: %w", path, err)
		}
		delete(staged, path)
	}
	for _, change := range ordered {
		if !change.Remove {
			continue
		}
		path := filepath.Clean(change.Path)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale generated output %s: %w", path, err)
		}
	}
	return nil
}

func normalizedGeneratedContent(content []byte) []byte {
	normalized := bytes.TrimSpace(content)
	return append(normalized, '\n')
}
