package module

import "github.com/flidai/leapview/internal/project/manifest"

// RuntimeSourceAlias exposes the compiler's stable source alias through the
// project module boundary for composition-root consumers.
func RuntimeSourceAlias(sourceName string) string {
	return manifest.RuntimeSourceAlias(sourceName)
}
