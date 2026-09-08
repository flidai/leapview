package module

import projectruntime "github.com/flidai/leapview/internal/project/runtime"

// RuntimeProvider is the project module's active-generation provider port.
// Process composition passes this capability through the project module
// surface instead of importing the runtime implementation contract directly.
type RuntimeProvider = projectruntime.Provider
