package module

import (
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

type ID = servingstate.ID
type Environment = servingstate.Environment
type ProjectID = projectgraph.ResourceID
type PreparedRuntime = servingstate.PreparedRuntime
type ActiveScope = servingstate.ActiveScope

const DefaultEnvironment = servingstate.DefaultEnvironment

func NormalizeEnvironment(environment Environment) Environment {
	return servingstate.NormalizeEnvironment(environment)
}
