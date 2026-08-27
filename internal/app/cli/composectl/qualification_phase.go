package composectl

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const qualificationPhaseFDEnvironment = "LEAPVIEW_QUALIFICATION_PHASE_FD"

type qualificationTransitionPhase struct {
	Operation string `json:"operation"`
	Event     string `json:"event"`
}

func qualificationPhaseObserverFromEnvironment() func(string, string) error {
	raw := strings.TrimSpace(os.Getenv(qualificationPhaseFDEnvironment))
	if raw == "" {
		return nil
	}
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 3 {
		return func(string, string) error {
			return fmt.Errorf("%s must identify an inherited file descriptor", qualificationPhaseFDEnvironment)
		}
	}
	file := os.NewFile(uintptr(fd), "qualification-phase")
	if file == nil {
		return func(string, string) error {
			return fmt.Errorf("%s file descriptor is unavailable", qualificationPhaseFDEnvironment)
		}
	}
	encoder := json.NewEncoder(file)
	return func(operation, event string) error {
		if err := encoder.Encode(qualificationTransitionPhase{Operation: operation, Event: event}); err != nil {
			return fmt.Errorf("publish qualification phase %s/%s: %w", operation, event, err)
		}
		return nil
	}
}

func (c *Controller) runQualificationReadiness(operation string, action func() error) error {
	if c.qualificationPhase != nil {
		if err := c.qualificationPhase(operation, "started"); err != nil {
			return err
		}
	}
	if err := action(); err != nil {
		return err
	}
	if c.qualificationPhase != nil {
		if err := c.qualificationPhase(operation, "completed"); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) withoutQualificationPhases(action func() error) error {
	observer := c.qualificationPhase
	c.qualificationPhase = nil
	defer func() { c.qualificationPhase = observer }()
	return action()
}
