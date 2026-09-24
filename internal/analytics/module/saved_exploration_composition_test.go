package module

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
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

type savedExplorationModuleRepository struct{}

var _ SavedExplorationRepository = (*savedExplorationModuleRepository)(nil)

func (*savedExplorationModuleRepository) Create(context.Context, saved.CreateInput) (saved.MutationResult, error) {
	return saved.MutationResult{}, saved.ErrUnavailable
}
func (*savedExplorationModuleRepository) LookupMutation(context.Context, saved.MutationLookupInput) (saved.MutationReplayMetadata, bool, error) {
	return saved.MutationReplayMetadata{}, false, saved.ErrUnavailable
}
func (*savedExplorationModuleRepository) GetLifecycle(context.Context, saved.ReadInput) (saved.Lifecycle, error) {
	return saved.Lifecycle{}, saved.ErrUnavailable
}
func (*savedExplorationModuleRepository) GetRevision(context.Context, saved.RevisionReadInput) (saved.Revision, error) {
	return saved.Revision{}, saved.ErrUnavailable
}
func (*savedExplorationModuleRepository) ListPage(context.Context, saved.ListInput) (saved.ListPage, error) {
	return saved.ListPage{}, saved.ErrUnavailable
}
func (*savedExplorationModuleRepository) UpdateVersion(context.Context, saved.UpdateVersionInput) (saved.MutationResult, error) {
	return saved.MutationResult{}, saved.ErrUnavailable
}
func (*savedExplorationModuleRepository) Duplicate(context.Context, saved.DuplicateInput) (saved.MutationResult, error) {
	return saved.MutationResult{}, saved.ErrUnavailable
}
func (*savedExplorationModuleRepository) List(context.Context, saved.ListInput) ([]saved.Lifecycle, error) {
	return nil, saved.ErrUnavailable
}
func (*savedExplorationModuleRepository) Archive(context.Context, saved.ArchiveInput) (saved.MutationResult, error) {
	return saved.MutationResult{}, saved.ErrUnavailable
}

func TestBuildSavedExplorationServiceRequiresModulePorts(t *testing.T) {
	base := SavedExplorationServiceOptions{
		Repository: &savedExplorationModuleRepository{},
		Authorizer: savedExplorationModuleAuthorizer{},
		Runtime:    savedExplorationModuleProvider{},
		Executor:   savedExplorationModuleExecutor{},
	}
	if service, err := BuildSavedExplorationService(base); err != nil || service == nil {
		t.Fatalf("complete module composition: service=%v err=%v", service, err)
	}

	tests := []struct {
		name   string
		mutate func(*SavedExplorationServiceOptions)
	}{
		{name: "repository", mutate: func(options *SavedExplorationServiceOptions) { options.Repository = nil }},
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
