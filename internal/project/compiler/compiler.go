package compiler

import projectartifact "github.com/flidai/leapview/internal/project/artifact"

// Compile loads and validates a project-wide authored graph and emits the
// immutable project artifact. Serving identity and target selection belong
// to deployment/runtime layers, not authoring compilation.
func Compile(projectPath string) (projectartifact.Project, error) {
	return CompileProject(projectPath)
}

// CompileRetainedProjectManifest exists only to verify and replay snapshots
// captured before ADR-0016 removed the authored Project manifest. New
// authoring paths must call Compile with a conventional source root.
func CompileRetainedProjectManifest(projectPath string) (projectartifact.Project, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return projectartifact.Project{}, err
	}
	return projectartifact.NewProject(project.Graph, project.Manifest)
}
