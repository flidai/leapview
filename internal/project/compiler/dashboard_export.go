package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
)

// DashboardExportMode selects the source layout emitted by dashboard export.
// Expanded is one canonical resource document. Fragmented preserves the
// project-managed source file arrangement and bytes; it does not introduce a
// second dashboard representation or any fragment resource identity.
type DashboardExportMode string

const (
	DashboardExportExpanded   DashboardExportMode = "expanded"
	DashboardExportFragmented DashboardExportMode = "fragmented"
)

func (m DashboardExportMode) Validate() error {
	switch m {
	case DashboardExportExpanded, DashboardExportFragmented:
		return nil
	default:
		return fmt.Errorf("unsupported dashboard export mode %q (want expanded or fragmented)", m)
	}
}

// DashboardSourceFile is one authored source file in a fragmented export.
// Path is always slash-separated and relative to the project root. Content is
// detached so callers may safely write it to a destination of their choice.
type DashboardSourceFile struct {
	Path    string
	Content []byte
}

// DashboardSourceExport is the deterministic result of exporting one
// project-managed dashboard source. Document is always the expanded canonical
// DTO and is the only semantic value used by compilation/fingerprinting.
// Files contains either one canonical file (expanded mode) or the original
// dashboard and all transitively included fragment files (fragmented mode).
type DashboardSourceExport struct {
	Mode     DashboardExportMode
	Document document.DashboardDocument
	MainPath string
	Files    []DashboardSourceFile
}

// ExportDashboardSource loads a dashboard source from a project checkout and
// emits either canonical expanded YAML or the original reviewable fragment
// layout. All source paths are resolved and retained inside projectRoot.
func ExportDashboardSource(path, projectRoot string, mode DashboardExportMode) (DashboardSourceExport, error) {
	if err := mode.Validate(); err != nil {
		return DashboardSourceExport{}, err
	}
	if strings.TrimSpace(path) == "" {
		return DashboardSourceExport{}, fmt.Errorf("dashboard path is required")
	}
	if strings.TrimSpace(projectRoot) == "" {
		return DashboardSourceExport{}, fmt.Errorf("project root is required")
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return DashboardSourceExport{}, fmt.Errorf("resolve project root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return DashboardSourceExport{}, fmt.Errorf("resolve project root: %w", err)
	}
	sourcePath, err := filepath.Abs(path)
	if err != nil {
		return DashboardSourceExport{}, fmt.Errorf("resolve dashboard path: %w", err)
	}
	canonicalPath, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return DashboardSourceExport{}, fmt.Errorf("resolve dashboard path: %w", err)
	}
	if info, statErr := os.Stat(canonicalPath); statErr != nil {
		return DashboardSourceExport{}, fmt.Errorf("resolve dashboard path: %w", statErr)
	} else if info.IsDir() {
		return DashboardSourceExport{}, fmt.Errorf("dashboard path %q is a directory", path)
	}
	mainPath, err := projectRelativePathChecked(root, canonicalPath)
	if err != nil {
		return DashboardSourceExport{}, err
	}

	input, err := LoadDashboardDocument(canonicalPath)
	if err != nil {
		return DashboardSourceExport{}, err
	}
	expanded, err := document.ExpandDashboardFragments(input, canonicalPath, root)
	if err != nil {
		return DashboardSourceExport{}, err
	}
	// Fragment decoding is intentionally source-layout-only. Validate the
	// complete expanded DTO through the canonical schema before returning either
	// layout so malformed fragment values cannot be exported successfully.
	if err := validateExpandedDashboard(expanded.Document, canonicalPath, expanded.Paths); err != nil {
		return DashboardSourceExport{}, err
	}
	result := DashboardSourceExport{Mode: mode, Document: expanded.Document, MainPath: mainPath}
	if mode == DashboardExportExpanded {
		content, err := document.EncodeYAML(expanded.Document)
		if err != nil {
			return DashboardSourceExport{}, err
		}
		result.Files = []DashboardSourceFile{{Path: mainPath, Content: append([]byte(nil), content...)}}
		return result, nil
	}

	paths := append([]string{mainPath}, expanded.Paths...)
	sort.Strings(paths)
	paths = uniqueStrings(paths)
	result.Files = make([]DashboardSourceFile, 0, len(paths))
	for _, relative := range paths {
		filePath, err := projectPathChecked(root, relative)
		if err != nil {
			return DashboardSourceExport{}, err
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return DashboardSourceExport{}, fmt.Errorf("read dashboard source %q: %w", relative, err)
		}
		result.Files = append(result.Files, DashboardSourceFile{Path: relative, Content: append([]byte(nil), content...)})
	}
	return result, nil
}

// ExportDashboardForProject is a descriptive alias for callers that prefer
// the project-boundary wording over the source-oriented name.
func ExportDashboardForProject(path, projectRoot string, mode DashboardExportMode) (DashboardSourceExport, error) {
	return ExportDashboardSource(path, projectRoot, mode)
}

func projectRelativePathChecked(root, path string) (string, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("dashboard source path %q resolves outside project boundary", path)
	}
	return filepath.ToSlash(relative), nil
}

func projectPathChecked(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || isWindowsAbsoluteExportPath(relative) {
		return "", fmt.Errorf("dashboard source path %q must be relative to project root", relative)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	for _, part := range strings.Split(filepath.ToSlash(clean), "/") {
		if part == ".." {
			return "", fmt.Errorf("dashboard source path %q escapes project boundary", relative)
		}
	}
	return filepath.Join(root, clean), nil
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func isWindowsAbsoluteExportPath(path string) bool {
	return len(path) >= 3 && ((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

// ExportDashboard emits one deterministic, schema-validated canonical
// Dashboard resource. The generated DashboardDocument is the only accepted
// source: compiled definitions and legacy authoring structs are not
// decompiled or translated at this boundary.
func ExportDashboard(value document.DashboardDocument) ([]byte, error) {
	content, err := document.EncodeYAML(value)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical dashboard: %w", err)
	}
	if err := document.ValidateSchema(value, "dashboard.yaml"); err != nil {
		return nil, err
	}
	return content, nil
}
