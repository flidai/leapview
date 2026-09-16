package connectionbinding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func profileApplicationDigest(marker byte) string {
	return "sha256:" + strings.Repeat(string(marker), 64)
}

func profileApplicationFixture(t *testing.T, status ProfileApplicationStatus) ProfileApplication {
	t.Helper()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	application, err := NewProfileApplication(ProfileApplication{
		ID: "application-local-1", CheckoutID: "checkout-1", RuntimeID: "runtime-1",
		TargetID: "target-local", ProjectID: projectgraph.ResourceID("project-1"), Environment: "dev",
		ProfileName: "local", SourceDigest: profileApplicationDigest('c'), GraphDigest: profileApplicationDigest('a'), ProfileDigest: profileApplicationDigest('b'),
		RequiredConnections: []ProfileApplicationRequiredConnection{
			{ConnectionID: "connection-a", ConnectorKind: "postgres"},
		},
		ExpectedConnections: []ProfileApplicationConnection{
			{BindingID: "binding-a", ConnectionID: "connection-a", ConnectorKind: "postgres", AuthenticationMode: AuthenticationExternalBundle, Endpoint: EndpointConfig{Host: "127.0.0.1", Port: 5432, Database: "analytics", TLSMode: "disable"}, CredentialReference: CredentialReference{ProjectID: "project-1", Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_A"}, BindingRevision: 3, ProviderVersion: "provider-a"},
		},
		AppliedConnections: []ProfileApplicationConnection{
			{BindingID: "binding-a", ConnectionID: "connection-a", ConnectorKind: "postgres", AuthenticationMode: AuthenticationExternalBundle, Endpoint: EndpointConfig{Host: "127.0.0.1", Port: 5432, Database: "analytics", TLSMode: "disable"}, CredentialReference: CredentialReference{ProjectID: "project-1", Environment: "dev", SecretPath: "/", SecretKey: "LEAPVIEW_DEV_CONNECTION_A"}, BindingRevision: 4, ProviderVersion: "provider-a"},
		},
		Status: status, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("fixture application: %v", err)
	}
	return application
}

func admissionRequestFromApplication(application ProfileApplication) ProfileApplicationAdmissionRequest {
	return ProfileApplicationAdmissionRequest{
		CheckoutID: application.CheckoutID, RuntimeID: application.RuntimeID, TargetID: application.TargetID,
		ProjectID: application.ProjectID, Environment: application.Environment, ProfileName: application.ProfileName,
		GraphDigest: application.GraphDigest, ProfileDigest: application.ProfileDigest,
		EligibleConnections: append([]ProfileApplicationConnection(nil), application.AppliedConnections...),
	}
}

func saveProfileApplication(t *testing.T, store *memoryProfileApplicationStore, application ProfileApplication) ProfileApplication {
	t.Helper()
	saved, err := store.Save(context.Background(), application, 0)
	if err != nil {
		t.Fatalf("save application: %v", err)
	}
	return saved
}

func TestProfileApplicationAdmissionRejectsOmittedRetainedBinding(t *testing.T) {
	store := newMemoryProfileApplicationStore()
	application := profileApplicationFixture(t, ProfileApplicationApplied)
	saveProfileApplication(t, store, application)
	checker, err := NewProfileApplicationAdmissionChecker(store)
	if err != nil {
		t.Fatal(err)
	}
	request := admissionRequestFromApplication(application)
	request.EligibleConnections = append(request.EligibleConnections, ProfileApplicationConnection{
		BindingID: "binding-retained", ConnectionID: "connection-retained", ConnectorKind: "postgres",
		AuthenticationMode: AuthenticationNone, Endpoint: EndpointConfig{Host: "127.0.0.1", Port: 5432}, BindingRevision: 2, ProviderVersion: NoAuthProviderVersion,
	})
	if err := checker.Check(context.Background(), request); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
		t.Fatalf("omitted retained binding error = %v", err)
	}
}

func TestProfileApplicationAdmissionRejectsGraphExpansion(t *testing.T) {
	store := newMemoryProfileApplicationStore()
	application := profileApplicationFixture(t, ProfileApplicationApplied)
	saveProfileApplication(t, store, application)
	checker, _ := NewProfileApplicationAdmissionChecker(store)
	request := admissionRequestFromApplication(application)
	request.GraphDigest = profileApplicationDigest('d')
	request.EligibleConnections = append(request.EligibleConnections, ProfileApplicationConnection{
		BindingID: "binding-new", ConnectionID: "connection-new", ConnectorKind: "postgres",
		AuthenticationMode: AuthenticationNone, Endpoint: EndpointConfig{Host: "127.0.0.1", Port: 5432}, BindingRevision: 1, ProviderVersion: NoAuthProviderVersion,
	})
	if err := checker.Admit(context.Background(), request); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
		t.Fatalf("expanded graph error = %v", err)
	}
}

func TestProfileApplicationAdmissionRequiresAppliedState(t *testing.T) {
	for _, status := range []ProfileApplicationStatus{ProfileApplicationApplying, ProfileApplicationIncomplete} {
		t.Run(string(status), func(t *testing.T) {
			store := newMemoryProfileApplicationStore()
			application := profileApplicationFixture(t, status)
			saveProfileApplication(t, store, application)
			checker, _ := NewProfileApplicationAdmissionChecker(store)
			if err := checker.Check(context.Background(), admissionRequestFromApplication(application)); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
				t.Fatalf("status %s admission error = %v", status, err)
			}
		})
	}
}

func TestProfileApplicationAdmissionAcceptsNewerRevisionOnlyForExactEvidence(t *testing.T) {
	store := newMemoryProfileApplicationStore()
	application := profileApplicationFixture(t, ProfileApplicationApplied)
	saveProfileApplication(t, store, application)
	checker, _ := NewProfileApplicationAdmissionChecker(store)

	newer := admissionRequestFromApplication(application)
	newer.EligibleConnections[0].BindingRevision++
	if err := checker.Check(t.Context(), newer); err != nil {
		t.Fatalf("newer exact binding evidence was rejected: %v", err)
	}

	drifted := newer
	drifted.EligibleConnections = cloneConnections(newer.EligibleConnections)
	drifted.EligibleConnections[0].ProviderVersion = "provider-changed"
	if err := checker.Check(t.Context(), drifted); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
		t.Fatalf("provider drift error = %v", err)
	}

	older := admissionRequestFromApplication(application)
	older.EligibleConnections[0].BindingRevision--
	if err := checker.Check(t.Context(), older); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
		t.Fatalf("older binding evidence error = %v", err)
	}
}

func TestProfileApplicationPersistsIntentBeforeBindingEvidence(t *testing.T) {
	application := profileApplicationFixture(t, ProfileApplicationApplying)
	application.AppliedConnections = nil
	application, err := NewProfileApplication(application)
	if err != nil {
		t.Fatalf("new applying application: %v", err)
	}
	store := newMemoryProfileApplicationStore()
	saved := saveProfileApplication(t, store, application)
	if saved.Status != ProfileApplicationApplying || len(saved.RequiredConnections) != 1 || len(saved.AppliedConnections) != 0 {
		t.Fatalf("saved application = %#v", saved)
	}
	checker, _ := NewProfileApplicationAdmissionChecker(store)
	request := admissionRequestFromApplication(profileApplicationFixture(t, ProfileApplicationApplied))
	if err := checker.Check(context.Background(), request); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
		t.Fatalf("applying admission error = %v", err)
	}
}

func TestProfileApplicationAdmissionRejectsOlderRevisionAndProviderVersion(t *testing.T) {
	store := newMemoryProfileApplicationStore()
	application := profileApplicationFixture(t, ProfileApplicationApplied)
	saveProfileApplication(t, store, application)
	checker, _ := NewProfileApplicationAdmissionChecker(store)
	for _, test := range []struct {
		name   string
		mutate func(*ProfileApplicationAdmissionRequest)
	}{
		{name: "older binding revision", mutate: func(request *ProfileApplicationAdmissionRequest) { request.EligibleConnections[0].BindingRevision-- }},
		{name: "stale provider version", mutate: func(request *ProfileApplicationAdmissionRequest) {
			request.EligibleConnections[0].ProviderVersion = "provider-stale"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := admissionRequestFromApplication(application)
			test.mutate(&request)
			if err := checker.Check(context.Background(), request); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
				t.Fatalf("%s error = %v", test.name, err)
			}
		})
	}
}

func TestProfileApplicationAdmissionRejectsWrongTargetAndScope(t *testing.T) {
	store := newMemoryProfileApplicationStore()
	application := profileApplicationFixture(t, ProfileApplicationApplied)
	saveProfileApplication(t, store, application)
	checker, _ := NewProfileApplicationAdmissionChecker(store)
	for _, test := range []struct {
		name   string
		mutate func(*ProfileApplicationAdmissionRequest)
	}{
		{name: "target", mutate: func(request *ProfileApplicationAdmissionRequest) { request.TargetID = "other-target" }},
		{name: "project", mutate: func(request *ProfileApplicationAdmissionRequest) { request.ProjectID = "other-project" }},
		{name: "environment", mutate: func(request *ProfileApplicationAdmissionRequest) { request.Environment = "other" }},
		{name: "checkout", mutate: func(request *ProfileApplicationAdmissionRequest) { request.CheckoutID = "other-checkout" }},
		{name: "runtime", mutate: func(request *ProfileApplicationAdmissionRequest) { request.RuntimeID = "other-runtime" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := admissionRequestFromApplication(application)
			test.mutate(&request)
			if err := checker.Check(context.Background(), request); !errors.Is(err, ErrProfileApplicationNotAdmitted) {
				t.Fatalf("%s mismatch error = %v", test.name, err)
			}
		})
	}
}

func TestProfileApplicationStoreDistinguishesExactRetryFromReplacementDrift(t *testing.T) {
	store := newMemoryProfileApplicationStore()
	application := profileApplicationFixture(t, ProfileApplicationApplying)
	saved := saveProfileApplication(t, store, application)
	retry, err := store.Save(context.Background(), saved, saved.Revision)
	if err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if retry.Revision != saved.Revision {
		t.Fatalf("exact retry advanced revision from %d to %d", saved.Revision, retry.Revision)
	}
	completed := saved
	completed.Status = ProfileApplicationApplied
	completed.AppliedConnections[0].BindingRevision++
	completed.UpdatedAt = completed.UpdatedAt.Add(time.Minute)
	completed, err = store.Save(context.Background(), completed, saved.Revision)
	if err != nil || completed.Revision != saved.Revision+1 {
		t.Fatalf("complete application = %#v, err=%v", completed, err)
	}
	drift := completed
	drift.GraphDigest = profileApplicationDigest('e')
	if _, err := store.Save(context.Background(), drift, completed.Revision); !errors.Is(err, ErrProfileApplicationReplacement) {
		t.Fatalf("graph drift error = %v", err)
	}
	drift = completed
	drift.SourceDigest = profileApplicationDigest('d')
	if _, err := store.Save(context.Background(), drift, completed.Revision); !errors.Is(err, ErrProfileApplicationReplacement) {
		t.Fatalf("source drift error = %v", err)
	}
	drift = completed
	drift.ProfileDigest = profileApplicationDigest('f')
	if _, err := store.Save(context.Background(), drift, completed.Revision); !errors.Is(err, ErrProfileApplicationReplacement) {
		t.Fatalf("profile drift error = %v", err)
	}
}

func TestProfileApplicationFormattingDoesNotLeakDigestsOrProviderVersions(t *testing.T) {
	application := profileApplicationFixture(t, ProfileApplicationApplied)
	formatted := fmt.Sprintf("%v %#v %v %#v", application, application, application.AppliedConnections[0], application.Identity())
	for _, forbidden := range []string{application.SourceDigest, application.GraphDigest, application.ProfileDigest, application.AppliedConnections[0].ProviderVersion} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("formatted application leaked %q: %s", forbidden, formatted)
		}
	}
	if err := application.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := application
	invalid.GraphDigest = "not-a-digest"
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidProfileApplication) || strings.Contains(err.Error(), "not-a-digest") {
		t.Fatalf("invalid digest error = %v", err)
	}
}

func TestProfileApplicationStoreUsesOptimisticRevision(t *testing.T) {
	store := newMemoryProfileApplicationStore()
	application := saveProfileApplication(t, store, profileApplicationFixture(t, ProfileApplicationApplying))
	application.Status = ProfileApplicationIncomplete
	application.UpdatedAt = application.UpdatedAt.Add(time.Minute)
	if _, err := store.Save(context.Background(), application, application.Revision-1); !errors.Is(err, ErrProfileApplicationConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
}

func TestProfileApplicationStoreRequiresExplicitReplacement(t *testing.T) {
	store := newMemoryProfileApplicationStore()
	current := saveProfileApplication(t, store, profileApplicationFixture(t, ProfileApplicationApplied))
	replacement := profileApplicationFixture(t, ProfileApplicationApplying)
	replacement.ID = "application-local-2"
	replacement.SourceDigest = profileApplicationDigest('d')
	replacement.AppliedConnections = nil
	replacement.UpdatedAt = replacement.UpdatedAt.Add(time.Minute)
	if _, err := store.Save(context.Background(), replacement, current.Revision); !errors.Is(err, ErrProfileApplicationReplacement) {
		t.Fatalf("implicit replacement error = %v", err)
	}
	saved, err := store.Replace(context.Background(), replacement, current.Revision)
	if err != nil {
		t.Fatalf("explicit replacement: %v", err)
	}
	if saved.ID != replacement.ID || saved.Status != ProfileApplicationApplying || saved.Revision != current.Revision+1 || len(saved.AppliedConnections) != 0 {
		t.Fatalf("replacement = %#v", saved)
	}
}

// memoryProfileApplicationStore is deliberately test-only. Production code
// must provide durable CAS-backed persistence for this interface.
type memoryProfileApplicationStore struct {
	mu     sync.Mutex
	values map[string]ProfileApplicationRecord
}

func newMemoryProfileApplicationStore() *memoryProfileApplicationStore {
	return &memoryProfileApplicationStore{values: make(map[string]ProfileApplicationRecord)}
}

func (store *memoryProfileApplicationStore) Application(ctx context.Context, scope ProfileApplicationScope, targetID TargetID) (ProfileApplicationRecord, error) {
	if store == nil || ctx == nil {
		return ProfileApplicationRecord{}, ErrProfileApplicationNotFound
	}
	if err := ctx.Err(); err != nil {
		return ProfileApplicationRecord{}, err
	}
	if _, err := ParseTargetID(targetID.String()); err != nil || validateScope(scope) != nil {
		return ProfileApplicationRecord{}, ErrProfileApplicationNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.values[scopeKey(scope, targetID)]
	if !ok {
		return ProfileApplicationRecord{}, ErrProfileApplicationNotFound
	}
	return cloneRecord(record), nil
}

func (store *memoryProfileApplicationStore) Save(ctx context.Context, record ProfileApplicationRecord, expectedRevision int64) (ProfileApplicationRecord, error) {
	if store == nil || ctx == nil {
		return ProfileApplicationRecord{}, ErrProfileApplicationConflict
	}
	if err := ctx.Err(); err != nil {
		return ProfileApplicationRecord{}, err
	}
	record, err := NewProfileApplication(record)
	if err != nil || expectedRevision < 0 {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	key := scopeKey(record.scope(), record.TargetID)
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.values[key]
	if !exists {
		if expectedRevision != 0 {
			return ProfileApplicationRecord{}, ErrProfileApplicationConflict
		}
		record.Revision = 1
		store.values[key] = cloneRecord(record)
		return cloneRecord(record), nil
	}
	if !sameApplicationIntent(current, record) {
		return ProfileApplicationRecord{}, ErrProfileApplicationReplacement
	}
	if current.Revision != expectedRevision {
		return ProfileApplicationRecord{}, ErrProfileApplicationConflict
	}
	if sameApplicationPayload(current, record) {
		return cloneRecord(current), nil
	}
	if current.Status == ProfileApplicationApplied && (!sameApplicationEvidence(current, record) || record.Status != ProfileApplicationApplied) {
		return ProfileApplicationRecord{}, ErrProfileApplicationReplacement
	}
	if !validStatusTransition(current.Status, record.Status) {
		return ProfileApplicationRecord{}, ErrProfileApplicationConflict
	}
	record.Revision = current.Revision + 1
	if record.UpdatedAt.Before(current.UpdatedAt) {
		return ProfileApplicationRecord{}, ErrProfileApplicationConflict
	}
	record.CreatedAt = current.CreatedAt
	store.values[key] = cloneRecord(record)
	return cloneRecord(record), nil
}

func (store *memoryProfileApplicationStore) Replace(ctx context.Context, record ProfileApplicationRecord, expectedRevision int64) (ProfileApplicationRecord, error) {
	if store == nil || ctx == nil {
		return ProfileApplicationRecord{}, ErrProfileApplicationConflict
	}
	if err := ctx.Err(); err != nil {
		return ProfileApplicationRecord{}, err
	}
	record, err := NewProfileApplication(record)
	if err != nil || expectedRevision < 1 || record.Status != ProfileApplicationApplying || len(record.AppliedConnections) != 0 {
		return ProfileApplicationRecord{}, ErrInvalidProfileApplication
	}
	key := scopeKey(record.scope(), record.TargetID)
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.values[key]
	if !exists {
		return ProfileApplicationRecord{}, ErrProfileApplicationNotFound
	}
	if current.Revision != expectedRevision {
		return ProfileApplicationRecord{}, ErrProfileApplicationConflict
	}
	record.Revision = current.Revision + 1
	record.CreatedAt = current.CreatedAt
	store.values[key] = cloneRecord(record)
	return cloneRecord(record), nil
}

func validStatusTransition(from, to ProfileApplicationStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case ProfileApplicationApplying:
		return to == ProfileApplicationIncomplete || to == ProfileApplicationApplied
	case ProfileApplicationIncomplete:
		return to == ProfileApplicationApplying || to == ProfileApplicationApplied
	default:
		return false
	}
}
