package compiler

import projectartifact "github.com/flidai/leapview/internal/project/artifact"

// Compile loads and validates one conventional analytics source root and
// emits an immutable, unbound source bundle. Serving identity and target
// selection belong to deployment/runtime layers, not authoring compilation.
func Compile(sourceRoot string) (projectartifact.SourceBundle, error) {
	project, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		return projectartifact.SourceBundle{}, err
	}
	return projectartifact.NewSourceBundle(project.Graph, project.Manifest)
}
