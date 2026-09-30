package module

import "github.com/flidai/leapview/internal/analytics/sourcework"

// SourceWorkPause is the analytics-module control surface for one source-work
// pause. It proves active work completion, not successful resource retirement.
type SourceWorkPause = sourcework.Pause

// PauseSourceWork fences new source preparation and shared pool refreshes.
// Authorization snapshot reads and isolated credential validation stay available.
// This is an internal lifecycle control, not an authorized activation command:
// the coordinator must separately establish authority, durable operation state,
// resource cleanup, and runtime readiness before it can resume or report in_use.
func (m *Module) PauseSourceWork() (*SourceWorkPause, error) {
	if m == nil {
		return nil, sourcework.ErrClosed
	}
	return m.sourceWork.Pause()
}

func (m *Module) sourceWorkGate() *sourcework.Gate {
	if m == nil {
		return nil
	}
	return &m.sourceWork
}
