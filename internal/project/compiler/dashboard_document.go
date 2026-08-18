package compiler

import (
	"fmt"
	"os"

	"github.com/flidai/leapview/internal/dashboard/document"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

// LoadDashboardDocument is the project compiler's canonical dashboard source
// boundary. YAML/JSON normalization, schema validation, and tagged-union
// dispatch all happen through the generated document DTO.
func LoadDashboardDocument(path string) (document.DashboardDocument, error) {
	if path == "" {
		return document.DashboardDocument{}, fmt.Errorf("dashboard path is required")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return document.DashboardDocument{}, err
	}
	var value document.DashboardDocument
	if err := configschema.DecodeResource(configschema.KindDashboard, path, content, &value); err != nil {
		return document.DashboardDocument{}, err
	}
	return value, nil
}

// LoadDashboardDocumentForProject performs the canonical source decode and
// expands dashboard-local fragment includes inside the project boundary. The
// plain loader remains useful for source-level tests and callers that only
// need the authored DTO; project compilation must use this entry point so an
// include-bearing document cannot silently compile with missing visuals/pages.
func LoadDashboardDocumentForProject(path, projectRoot string) (document.DashboardDocument, error) {
	value, err := LoadDashboardDocument(path)
	if err != nil {
		return document.DashboardDocument{}, err
	}
	expanded, err := document.ExpandDashboardFragments(value, path, projectRoot)
	if err != nil {
		return document.DashboardDocument{}, err
	}
	return expanded.Document, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func valueOrStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return append([]string(nil), (*value)...)
}
