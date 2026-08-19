package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/spf13/cobra"
)

// DashboardExportCommand constructs the local checkout dashboard export
// command. It lives with the project CLI because fragment layout and source
// boundary resolution are project concerns, while app composition attaches it
// beneath the dashboard command tree.
func DashboardExportCommand(ctx context.Context) *cobra.Command {
	var projectPath, layoutValue, output string
	command := &cobra.Command{
		Use:   "export <dashboard>",
		Short: "Export a dashboard as expanded or fragmented YAML",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			mode, err := parseDashboardExportMode(layoutValue)
			if err != nil {
				return err
			}
			project, err := projectcompiler.LoadProject(projectPath)
			if err != nil {
				return err
			}
			path, err := dashboardSourcePath(project, args[0])
			if err != nil {
				return err
			}
			export, err := projectcompiler.ExportDashboardSource(path, project.BaseDir, mode)
			if err != nil {
				return err
			}
			if mode == projectcompiler.DashboardExportFragmented {
				if strings.TrimSpace(output) == "" {
					return fmt.Errorf("fragmented dashboard export requires --out directory; stdout is ambiguous for multiple files")
				}
				return writeFragmentedExport(output, export)
			}
			content := export.Files[0].Content
			if strings.TrimSpace(output) == "" {
				_, err = command.OutOrStdout().Write(content)
				return err
			}
			return writeExpandedExport(output, content)
		},
	}
	command.Flags().StringVar(&projectPath, "project", filepath.Join("dashboards", "leapview.yaml"), "project path")
	command.Flags().StringVar(&layoutValue, "layout", string(projectcompiler.DashboardExportExpanded), "dashboard export layout: expanded or fragmented")
	command.Flags().StringVar(&output, "out", "", "expanded output file or empty fragmented output directory")
	return command
}

func parseDashboardExportMode(value string) (projectcompiler.DashboardExportMode, error) {
	mode := projectcompiler.DashboardExportMode(strings.TrimSpace(value))
	if err := mode.Validate(); err != nil {
		return "", err
	}
	return mode, nil
}

func dashboardSourcePath(project projectcompiler.Project, reference string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", fmt.Errorf("dashboard reference is required")
	}
	if path, ok := project.DashboardPaths[reference]; ok {
		return path, nil
	}
	for name, id := range project.DashboardIDs {
		if id == reference {
			return project.DashboardPaths[name], nil
		}
	}
	return "", fmt.Errorf("dashboard %q was not found in project", reference)
}

func writeExpandedExport(path string, content []byte) error {
	if parent := filepath.Dir(path); parent != "." {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create export parent: %w", err)
		}
	}
	if err := rejectExistingOutput(path, false); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".dashboard-export-*.tmp")
	if err != nil {
		return fmt.Errorf("create expanded dashboard export: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("prepare expanded dashboard export: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write expanded dashboard export: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close expanded dashboard export: %w", err)
	}
	// A hard-link claim publishes the complete temporary file atomically and
	// fails if another writer claims the destination after the initial Lstat.
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("publish expanded dashboard export without overwrite: %w", err)
	}
	return nil
}

func writeFragmentedExport(path string, export projectcompiler.DashboardSourceExport) error {
	if err := rejectExistingOutput(path, true); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create fragmented export parent: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".dashboard-export-*")
	if err != nil {
		return fmt.Errorf("create fragmented export directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	for _, file := range export.Files {
		target := filepath.Join(temporary, filepath.FromSlash(file.Path))
		if err := ensureRelativeOutput(temporary, target); err != nil {
			return err
		}
		if parent := filepath.Dir(target); parent != temporary {
			if err := os.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("create fragmented export parent: %w", err)
			}
		}
		if err := os.WriteFile(target, file.Content, 0o644); err != nil {
			return fmt.Errorf("write fragmented dashboard source %q: %w", file.Path, err)
		}
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish fragmented dashboard export: %w", err)
	}
	return nil
}

func rejectExistingOutput(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write through symlink output %q", path)
	}
	if directory {
		if info.IsDir() {
			return fmt.Errorf("refusing to overwrite existing fragmented dashboard export directory %q", path)
		}
		return fmt.Errorf("fragmented dashboard export --out %q is not a directory", path)
	}
	if info.IsDir() {
		return fmt.Errorf("expanded dashboard export --out %q is a directory", path)
	}
	return fmt.Errorf("refusing to overwrite existing dashboard export %q", path)
}

func ensureRelativeOutput(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("fragmented dashboard export path %q escapes output directory", target)
	}
	return nil
}
