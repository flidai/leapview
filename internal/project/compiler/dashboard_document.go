package compiler

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

// LoadDashboardDocument is the project compiler's canonical dashboard source
// boundary. YAML/JSON normalization, schema validation, and tagged-union
// dispatch all happen through the generated document DTO.
func LoadDashboardDocument(path string) (document.DashboardDocument, error) {
	return LoadDashboardDocumentWithReader(osProjectReader{}, path)
}

func LoadDashboardDocumentWithReader(reader projectFileReader, path string) (document.DashboardDocument, error) {
	if path == "" {
		return document.DashboardDocument{}, fmt.Errorf("dashboard path is required")
	}
	content, err := reader.ReadFile(path)
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
	return LoadDashboardDocumentForProjectWithReader(path, projectRoot, osProjectReader{})
}

func LoadDashboardDocumentForProjectWithReader(path, projectRoot string, reader projectFileReader) (document.DashboardDocument, error) {
	value, err := LoadDashboardDocumentWithReader(reader, path)
	if err != nil {
		return document.DashboardDocument{}, err
	}
	expanded, err := document.ExpandDashboardFragmentsWithReader(value, path, projectRoot, reader)
	if err != nil {
		return document.DashboardDocument{}, err
	}
	if err := validateExpandedDashboard(expanded.Document, path, expanded.Paths); err != nil {
		return document.DashboardDocument{}, err
	}
	return expanded.Document, nil
}

// validateExpandedDashboard is the one project-compilation seam for the
// generated DTO after source-only fragment expansion. Include paths are added
// to failures so diagnostics remain useful even though schema validation sees
// one canonical expanded document.
func validateExpandedDashboard(value document.DashboardDocument, path string, fragmentPaths []string) error {
	err := document.ValidateSchema(value, path)
	if err == nil || len(fragmentPaths) == 0 {
		return err
	}
	return fmt.Errorf("%s: expanded dashboard sources [%s]: %w", path, strings.Join(fragmentPaths, ", "), err)
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
