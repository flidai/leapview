package module

import (
	"testing"

	"github.com/flidai/leapview/pkg/jobs"
)

func TestCreateAgentRunGeneratedExecutionContractMatchesJobRegistration(t *testing.T) {
	execution, err := loadRunExecutionContract()
	if err != nil {
		t.Fatal(err)
	}
	module := &Module{runExecution: execution}
	if err := validateRunJobHandlers(execution, module.JobHandlers(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAgentRunRejectsJobHandlerRegistrationDrift(t *testing.T) {
	execution, err := loadRunExecutionContract()
	if err != nil {
		t.Fatal(err)
	}
	err = validateRunJobHandlers(execution, []jobs.Handler{jobs.HandlerFunc{JobKind: "agent.wrong"}})
	if err == nil {
		t.Fatal("job handler drift was accepted")
	}
}
