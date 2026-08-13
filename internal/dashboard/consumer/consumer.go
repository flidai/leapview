package consumer

import (
	"context"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type Kind string

const (
	KindVisual Kind = "visual"
	KindWindow Kind = "visual_window"
)

type Target struct {
	Kind          Kind
	ID            string
	WindowRequest dashboard.TableRequest
	// ExactCardinality is resolved from the authored table contract. The
	// default bounded mode never schedules a separate COUNT(*) query.
	ExactCardinality bool
}

// Key is the renderer-neutral identity used by status, audit, and
// observability surfaces. Kind remains internal execution metadata.
func (t Target) Key() string {
	if t.Kind == KindVisual || t.Kind == KindWindow {
		return "visual:" + t.ID
	}
	return string(t.Kind) + ":" + t.ID
}

type Request struct {
	DashboardID string
	PageID      string
	ModelID     string
	Command     string
	Filters     dashboard.Filters
	Targets     []Target
	Concurrency int
	Progress    ProgressPublisher
}

type Progress struct {
	Completed            int
	Total                int
	WorkDuration         time.Duration
	CriticalPathDuration time.Duration
}

type ProgressPublisher func(Progress)

type Result struct {
	Target         Target
	Envelope       visualizationir.VisualizationEnvelope
	Metadata       bool
	Err            error
	Duration       time.Duration
	Queries        int
	StageTimingsMs map[string]float64
}

type Publisher func(Result) bool

type Executor interface {
	ExecuteConsumersPage(context.Context, Request, Publisher) error
}
