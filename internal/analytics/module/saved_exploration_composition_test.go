package module

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

type savedExplorationModuleProvider struct{}

func (savedExplorationModuleProvider) Acquire(context.Context) (runtimehostmodule.Lease, error) {
	return nil, errors.New("saved exploration module test provider is not executable")
}

type savedExplorationModuleAuthorizer struct{}

func (savedExplorationModuleAuthorizer) Authorize(context.Context, projectruntime.Lease, SavedExplorationAuthorizationRequest) error {
	return nil
}

type savedExplorationModuleExecutor struct{}

func (savedExplorationModuleExecutor) Execute(context.Context, projectruntime.Lease, string, SavedExplorationQuery) (SavedExplorationResult, error) {
	return SavedExplorationResult{}, nil
}

type savedExplorationModuleAuditRecorder struct{}

func (savedExplorationModuleAuditRecorder) RecordAuditIntent(context.Context, transaction.Transaction, accessmodule.AuditIntent) error {
	return nil
}

func TestBuildSavedExplorationServiceRequiresModulePorts(t *testing.T) {
	base := SavedExplorationServiceOptions{
		Database:            &sql.DB{},
		AuditIntentRecorder: savedExplorationModuleAuditRecorder{},
		Authorizer:          savedExplorationModuleAuthorizer{},
		Runtime:             savedExplorationModuleProvider{},
		Executor:            savedExplorationModuleExecutor{},
	}
	if service, err := BuildSavedExplorationService(base); err != nil || service == nil {
		t.Fatalf("complete module composition: service=%v err=%v", service, err)
	}

	tests := []struct {
		name   string
		mutate func(*SavedExplorationServiceOptions)
	}{
		{name: "database", mutate: func(options *SavedExplorationServiceOptions) { options.Database = nil }},
		{name: "audit intent recorder", mutate: func(options *SavedExplorationServiceOptions) { options.AuditIntentRecorder = nil }},
		{name: "authorizer", mutate: func(options *SavedExplorationServiceOptions) { options.Authorizer = nil }},
		{name: "runtime provider", mutate: func(options *SavedExplorationServiceOptions) { options.Runtime = nil }},
		{name: "executor", mutate: func(options *SavedExplorationServiceOptions) { options.Executor = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := base
			test.mutate(&options)
			if service, err := BuildSavedExplorationService(options); err == nil || service != nil {
				t.Fatalf("incomplete module composition: service=%v err=%v", service, err)
			}
		})
	}
}

func TestSavedExplorationRevisionIDUsesOpaqueEntropy(t *testing.T) {
	reader := bytes.NewReader(bytes.Repeat([]byte{0xab}, 16))
	id, err := newSavedExplorationRevisionID(reader)
	if err != nil {
		t.Fatalf("new revision id: %v", err)
	}
	if got, want := id.String(), "revision-"+strings.Repeat("ab", 16); got != want {
		t.Fatalf("revision id = %q, want %q", got, want)
	}
	if _, err := newSavedExplorationRevisionID(nil); err == nil {
		t.Fatal("nil entropy reader unexpectedly succeeded")
	}
}
