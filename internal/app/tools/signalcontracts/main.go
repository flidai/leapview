package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/Yacobolo/toolbelt/apigen/emit/modelgo"
	"github.com/Yacobolo/toolbelt/apigen/ir"
)

type generationTarget struct {
	name       string
	outputPath string
	tags       []string
}

var generationTargets = []generationTarget{
	{
		name:       "access",
		outputPath: "internal/access/ui/signals/models.gen.go",
		tags:       []string{"login"},
	},
	{
		name:       "admin",
		outputPath: "internal/admin/ui/signals/models.gen.go",
		tags:       []string{"admin"},
	},
	{
		name:       "agent",
		outputPath: "internal/agent/ui/signals/models.gen.go",
		tags:       []string{"agent", "chat"},
	},
	{
		name:       "dashboard",
		outputPath: "internal/dashboard/ui/signals/models.gen.go",
		tags:       []string{"dashboard"},
	},
	{
		name:       "project",
		outputPath: "internal/project/ui/signals/models.gen.go",
		tags:       []string{"catalog", "project", "pipelines", "connections", "data", "resource", "shared"},
	},
}

func main() {
	check := flag.Bool("check", false, "fail when generated signal contracts are stale")
	flag.Parse()

	root := repositoryRoot()
	doc, err := ir.Load(filepath.Join(root, "api/gen/ui-signals-ir.json"))
	if err != nil {
		fatal(err)
	}
	outputs, err := generatedOutputs(root, doc)
	if err != nil {
		fatal(err)
	}
	for path, content := range outputs {
		if *check {
			existing, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(existing, content) {
				fatal(fmt.Errorf("generated signal contract is stale: %s", path))
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			fatal(err)
		}
	}
}

func generatedOutputs(root string, doc ir.Document) (map[string][]byte, error) {
	outputs := make(map[string][]byte, len(generationTargets))
	for _, target := range generationTargets {
		targetDoc := doc
		targetDoc.Contracts = contractsForTarget(doc, target)
		if len(targetDoc.Contracts) == 0 {
			return nil, fmt.Errorf("signal contract target %q has no contract roots", target.name)
		}
		source, err := modelgo.Emit(targetDoc, modelgo.Options{
			PackageName: "signals",
			ContractImports: map[string]modelgo.ContractImport{
				"LeapViewDashboard": {
					GoPackage: "github.com/flidai/leapview/internal/dashboard/document",
					GoAlias:   "dashboarddocument",
				},
				"LeapViewVisualization": {
					GoPackage: "github.com/flidai/leapview/internal/dashboard/visualization/ir",
					GoAlias:   "visualizationir",
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("generate %s signal contracts: %w", target.name, err)
		}
		source, err = format.Source(source)
		if err != nil {
			return nil, fmt.Errorf("format %s signal contracts: %w", target.name, err)
		}
		outputs[filepath.Join(root, target.outputPath)] = source
	}
	return outputs, nil
}

func contractsForTarget(doc ir.Document, target generationTarget) []ir.Contract {
	contracts := make([]ir.Contract, 0, len(doc.Contracts))
	for _, contract := range doc.Contracts {
		if containsAny(contract.Tags, target.tags) {
			contracts = append(contracts, contract)
		}
	}
	return contracts
}

func containsAny(values, candidates []string) bool {
	for _, value := range values {
		if slices.Contains(candidates, value) {
			return true
		}
	}
	return false
}

func repositoryRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		fatal(fmt.Errorf("locate signal contract generator source"))
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, strings.TrimSpace(err.Error()))
	os.Exit(1)
}
