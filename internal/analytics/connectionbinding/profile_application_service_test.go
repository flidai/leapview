package connectionbinding

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestProfileApplicationServicePersistsIntentBeforeMutationAndAppliesCompleteSet(t *testing.T) {
	service, store, bindings, request := profileApplicationServiceFixture(t)
	bindings.beforeMutation = func() {
		record, err := store.Application(t.Context(), ProfileApplicationScope{CheckoutID: request.CheckoutID, RuntimeID: request.RuntimeID, ProjectID: request.ProjectID, Environment: request.Environment}, request.TargetID)
		if err != nil || record.Status != ProfileApplicationApplying || len(record.ExpectedConnections) != 2 || len(record.AppliedConnections) != 0 {
			t.Fatalf("pre-mutation checkpoint = %#v, err=%v", record, err)
		}
	}
	record, err := service.Apply(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != ProfileApplicationApplied || len(record.AppliedConnections) != 2 || bindings.mutations == 0 || !bindings.checkedBeforeMutation {
		t.Fatalf("application = %#v mutations=%d checked=%v", record, bindings.mutations, bindings.checkedBeforeMutation)
	}
	checker, _ := NewProfileApplicationAdmissionChecker(store)
	if err := checker.Check(t.Context(), admissionRequestFromApplication(record)); err != nil {
		t.Fatalf("applied admission: %v", err)
	}
}

func TestProfileApplicationServiceRecoversWholeIntentAfterPartialFailure(t *testing.T) {
	service, store, bindings, request := profileApplicationServiceFixture(t)
	bindings.failTestOnce = "connection-b"
	first, err := service.Apply(t.Context(), request)
	if err == nil || first.Status != ProfileApplicationIncomplete || len(first.AppliedConnections) != 1 {
		t.Fatalf("partial application = %#v, err=%v", first, err)
	}
	checker, _ := NewProfileApplicationAdmissionChecker(store)
	if err := checker.Check(t.Context(), admissionRequestFromApplication(profileApplicationFixtureFromRequest(t, request))); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
		t.Fatalf("incomplete admission error = %v", err)
	}
	request.Mode = ProfileApplicationResume
	completed, err := service.Apply(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != ProfileApplicationApplied || len(completed.AppliedConnections) != 2 {
		t.Fatalf("completed application = %#v", completed)
	}
}

func TestProfileApplicationServiceRejectsDifferentCredentialOnResumeBeforeMutation(t *testing.T) {
	service, _, bindings, request := profileApplicationServiceFixture(t)
	bindings.failTestOnce = "connection-b"
	if _, err := service.Apply(t.Context(), request); err == nil {
		t.Fatal("initial partial application unexpectedly succeeded")
	}
	before := bindings.mutations
	service.resolver = profileVersionResolver{"LEAPVIEW_DEV_CONNECTION_A": "provider-a", "LEAPVIEW_DEV_CONNECTION_B": "provider-changed"}
	request.Mode = ProfileApplicationResume
	if _, err := service.Apply(t.Context(), request); !errors.Is(err, ErrProfileApplicationReplacement) {
		t.Fatalf("changed credential resume error = %v", err)
	}
	if bindings.mutations != before {
		t.Fatalf("mutations changed from %d to %d", before, bindings.mutations)
	}
}

func TestProfileApplicationServiceResumeDoesNotReplaceForOrdinarySourceEdit(t *testing.T) {
	service, _, bindings, request := profileApplicationServiceFixture(t)
	current, err := service.Apply(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	before := bindings.mutations
	request.Mode = ProfileApplicationResume
	request.SourceDigest = profileApplicationDigest('d')
	resumed, err := service.Apply(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SourceDigest != current.SourceDigest || resumed.Revision != current.Revision {
		t.Fatalf("resume changed retained observation: current=%#v resumed=%#v", current, resumed)
	}
	if bindings.mutations != before {
		t.Fatalf("ordinary source edit mutated bindings: before=%d after=%d", before, bindings.mutations)
	}
}

func TestProfileApplicationServiceRequiresExplicitReplacement(t *testing.T) {
	service, _, bindings, request := profileApplicationServiceFixture(t)
	current, err := service.Apply(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	replacement := request
	replacement.ApplicationID = "application-2"
	replacement.ProfileDigest = profileApplicationDigest('f')
	replacement.Connections[0].Endpoint.Host = "replacement.internal"
	if _, err := service.Apply(t.Context(), replacement); !errors.Is(err, ErrProfileApplicationReplacement) {
		t.Fatalf("implicit replacement error = %v", err)
	}
	replacement.Mode = ProfileApplicationReplace
	if _, err := service.Apply(t.Context(), replacement); !errors.Is(err, ErrProfileApplicationReplacement) {
		t.Fatalf("uncoordinated replacement error = %v", err)
	}
	service.authorizeReplacement = func(context.Context, ProfileApplicationScope, TargetID) error { return nil }
	sameID := replacement
	sameID.ApplicationID = current.ID
	if _, err := service.Apply(t.Context(), sameID); !errors.Is(err, ErrProfileApplicationReplacement) {
		t.Fatalf("same-ID replacement error = %v", err)
	}
	updated, err := service.Apply(t.Context(), replacement)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID == current.ID || updated.Status != ProfileApplicationApplied || bindings.values["connection-a"].Endpoint.Host != "replacement.internal" {
		t.Fatalf("replacement = %#v binding=%#v", updated, bindings.values["connection-a"])
	}
}

func profileApplicationServiceFixture(t *testing.T) (*ProfileApplicationService, *memoryProfileApplicationStore, *profileBindingAdministration, ProfileApplicationRequest) {
	t.Helper()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	store := newMemoryProfileApplicationStore()
	bindings := &profileBindingAdministration{values: map[projectgraph.ResourceID]TargetBinding{}, versions: map[projectgraph.ResourceID]string{"connection-a": "provider-a", "connection-b": "provider-b"}, now: now}
	nextID := 0
	service, err := NewProfileApplicationService(ProfileApplicationServiceConfig{
		Store: store, Bindings: bindings,
		Resolver:     profileVersionResolver{"LEAPVIEW_DEV_CONNECTION_A": "provider-a", "LEAPVIEW_DEV_CONNECTION_B": "provider-b"},
		NewBindingID: func() (BindingID, error) { nextID++; return BindingID(fmt.Sprintf("binding-%d", nextID)), nil },
		Now:          func() time.Time { now = now.Add(time.Second); bindings.now = now; return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ProfileApplicationRequest{
		Mode: ProfileApplicationNew, ApplicationID: "application-1", CheckoutID: "checkout-1", RuntimeID: "runtime-1",
		TargetID: "target-local", ProjectID: "project-1", Environment: "dev", ProfileName: "local",
		SourceDigest: profileApplicationDigest('c'), GraphDigest: profileApplicationDigest('a'), ProfileDigest: profileApplicationDigest('b'), ActorID: "local-profile",
		Connections: []ProfileApplicationBinding{
			profileApplicationBinding("connection-a", "LEAPVIEW_DEV_CONNECTION_A", "a.internal"),
			profileApplicationBinding("connection-b", "LEAPVIEW_DEV_CONNECTION_B", "b.internal"),
		},
	}
	return service, store, bindings, request
}

func profileApplicationBinding(id, variable, host string) ProfileApplicationBinding {
	return ProfileApplicationBinding{
		ConnectionID: projectgraph.ResourceID(id), ConnectorKind: "postgres", AuthenticationMode: AuthenticationExternalBundle,
		Endpoint:            EndpointConfig{Host: host, Port: 5432, Database: "analytics", TLSMode: "disable"},
		CredentialReference: CredentialReference{ProjectID: "project-1", Environment: "dev", SecretPath: "/", SecretKey: variable},
	}
}

func profileApplicationFixtureFromRequest(t *testing.T, request ProfileApplicationRequest) ProfileApplicationRecord {
	t.Helper()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	record := ProfileApplicationRecord{ID: request.ApplicationID, CheckoutID: request.CheckoutID, RuntimeID: request.RuntimeID, TargetID: request.TargetID, ProjectID: request.ProjectID, Environment: request.Environment, ProfileName: request.ProfileName, SourceDigest: request.SourceDigest, GraphDigest: request.GraphDigest, ProfileDigest: request.ProfileDigest, Status: ProfileApplicationApplied, Revision: 1, CreatedAt: now, UpdatedAt: now}
	for index, input := range request.Connections {
		version := map[string]string{"LEAPVIEW_DEV_CONNECTION_A": "provider-a", "LEAPVIEW_DEV_CONNECTION_B": "provider-b"}[input.CredentialReference.SecretKey]
		expected := profileConnectionFromConfiguration(BindingID(fmt.Sprintf("binding-%d", index+1)), input.ConnectionID, 0, version, TargetBindingConfiguration{ConnectorKind: input.ConnectorKind, AuthenticationMode: input.AuthenticationMode, Endpoint: input.Endpoint, CredentialReference: input.CredentialReference})
		record.RequiredConnections = append(record.RequiredConnections, ProfileApplicationRequiredConnection{ConnectionID: input.ConnectionID, ConnectorKind: input.ConnectorKind})
		record.ExpectedConnections = append(record.ExpectedConnections, expected)
		expected.BindingRevision = 2
		record.AppliedConnections = append(record.AppliedConnections, expected)
	}
	result, err := NewProfileApplication(record)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type profileVersionResolver map[string]string

func (resolver profileVersionResolver) Resolve(_ context.Context, reference CredentialReference) (CredentialSnapshot, error) {
	version, ok := resolver[reference.SecretKey]
	if !ok {
		return CredentialSnapshot{}, ErrCredentialNotFound
	}
	return NewCredentialSnapshot(map[string]string{"password": "local-test"}, version, time.Now().UTC(), time.Now().UTC().Add(time.Minute))
}

type profileBindingAdministration struct {
	values                map[projectgraph.ResourceID]TargetBinding
	versions              map[projectgraph.ResourceID]string
	now                   time.Time
	mutations             int
	beforeMutation        func()
	checkedBeforeMutation bool
	failTestOnce          projectgraph.ResourceID
}

func (admin *profileBindingAdministration) checkMutation() {
	if !admin.checkedBeforeMutation && admin.beforeMutation != nil {
		admin.beforeMutation()
		admin.checkedBeforeMutation = true
	}
	admin.mutations++
}

func (admin *profileBindingAdministration) List(context.Context, string, BindingScope, TargetID) ([]TargetBinding, error) {
	result := make([]TargetBinding, 0, len(admin.values))
	for _, binding := range admin.values {
		result = append(result, binding)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ConnectionID < result[j].ConnectionID })
	return result, nil
}

func (admin *profileBindingAdministration) Get(_ context.Context, _ string, key BindingKey) (TargetBinding, error) {
	binding, ok := admin.values[key.ConnectionID]
	if !ok {
		return TargetBinding{}, ErrBindingNotFound
	}
	return binding, nil
}

func (admin *profileBindingAdministration) Create(_ context.Context, _ string, input TargetBindingInput) (TargetBinding, error) {
	admin.checkMutation()
	input.Now = admin.now
	binding, err := NewTargetBinding(input)
	if err == nil {
		admin.values[binding.ConnectionID] = binding
	}
	return binding, err
}

func (admin *profileBindingAdministration) PlanConfigurationChange(_ context.Context, _ string, key BindingKey, _ TargetBindingConfiguration) (BindingChangePlan, error) {
	binding, err := admin.Get(context.Background(), "", key)
	return BindingChangePlan{BindingID: binding.ID, ExpectedRevision: binding.Revision, ConfirmationToken: "confirmed"}, err
}

func (admin *profileBindingAdministration) UpdateConfiguration(_ context.Context, request UpdateConfigurationRequest) (TargetBinding, error) {
	admin.checkMutation()
	binding, err := admin.Get(context.Background(), "", request.Key)
	if err != nil || binding.Revision != request.ExpectedRevision {
		return TargetBinding{}, ErrIncompatibleBinding
	}
	updated, err := binding.UpdateConfiguration(request.Configuration, admin.now)
	if err == nil {
		admin.values[binding.ConnectionID] = updated
	}
	return updated, err
}

func (admin *profileBindingAdministration) Test(_ context.Context, _ string, key BindingKey) (BindingHealthStatus, error) {
	admin.checkMutation()
	if admin.failTestOnce == key.ConnectionID {
		admin.failTestOnce = ""
		return BindingHealthStatus{}, errors.New("test connection failed")
	}
	binding, err := admin.Get(context.Background(), "", key)
	if err != nil {
		return BindingHealthStatus{}, err
	}
	binding, err = binding.MarkValidated(admin.versions[key.ConnectionID], admin.now)
	if err != nil {
		return BindingHealthStatus{}, err
	}
	admin.values[key.ConnectionID] = binding
	return BindingHealthStatus{BindingID: binding.ID, TargetID: binding.TargetID, ConnectionID: binding.ConnectionID, ConnectorKind: binding.ConnectorKind, Scope: binding.Scope, BindingRevision: binding.Revision, ValidatedVersion: binding.ValidatedVersion, Health: binding.Health, HasActivePool: true, LastValidatedAt: binding.LastValidatedAt}, nil
}

func (admin *profileBindingAdministration) Enable(_ context.Context, _ string, key BindingKey) (TargetBinding, error) {
	admin.checkMutation()
	binding, err := admin.Get(context.Background(), "", key)
	if err != nil {
		return TargetBinding{}, err
	}
	binding, err = binding.Enable(admin.now)
	admin.values[key.ConnectionID] = binding
	return binding, err
}

func (admin *profileBindingAdministration) Disable(_ context.Context, _ string, key BindingKey) (TargetBinding, error) {
	admin.checkMutation()
	binding, err := admin.Get(context.Background(), "", key)
	if err != nil {
		return TargetBinding{}, err
	}
	binding, err = binding.Disable(admin.now)
	admin.values[key.ConnectionID] = binding
	return binding, err
}
