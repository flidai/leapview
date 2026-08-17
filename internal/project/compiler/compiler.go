package compiler

import projectartifact "github.com/flidai/leapview/internal/project/artifact"

// Compile loads and validates a project-wide authored graph and emits the
// immutable project artifact. Serving identity and target selection belong
// to deployment/runtime layers, not authoring compilation.
func Compile(projectPath string) (projectartifact.Project, error) {
	return CompileProject(projectPath)
}
