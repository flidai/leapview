package module

import "github.com/flidai/leapview/internal/dashboard/queryruntime"

// Metrics is the dashboard capability's runtime query surface. Composition
// refers to the capability-owned name while the implementation contract
// remains shared by dashboard transports and runtime factories.
type Metrics = queryruntime.Metrics

type ProjectMetrics = queryruntime.ProjectMetrics
