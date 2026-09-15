package http

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project/developmentsession"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestAuthenticatedSessionPointerAndExactHandoff(t *testing.T) {
	store := developmentsession.NewMemoryStore()
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "worktree_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	previewURL := "https://target.example/candidates/candidate_1"
	handler := New(Config{Store: store, Enabled: true, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, TargetID: key.TargetID, Environment: key.Environment, CurrentPrincipal: func(*http.Request) (string, bool) { return "owner_1", true }, ResolveProjectID: func(_ context.Context) (projectgraph.ResourceID, error) { return key.ProjectID, nil }, ValidateCandidate: func(_ context.Context, owner, candidate string, project projectgraph.ResourceID, target, environment string) (CandidateValidation, error) {
		return CandidateValidation{OwnerID: owner, ProjectID: project, TargetID: target, Environment: environment, Qualified: true, Identity: developmentsession.Identity{CandidateID: candidate, ArtifactDigest: "sha256:" + strings.Repeat("a", 64), GraphDigest: "sha256:" + strings.Repeat("b", 64), PreviewURL: previewURL}}, nil
	}})
	router := chi.NewRouter()
	handler.Mount(router)
	body := `{"revision":0,"attempted":{"candidateId":"candidate_1","artifactDigest":"sha256:` + strings.Repeat("a", 64) + `","graphDigest":"sha256:` + strings.Repeat("b", 64) + `"},"lastValid":{"candidateId":"candidate_1","artifactDigest":"sha256:` + strings.Repeat("a", 64) + `","graphDigest":"sha256:` + strings.Repeat("b", 64) + `"},"diagnostics":[{"code":"SYNC","message":"password=secret"}]}`
	request := httptest.NewRequest(http.MethodPut, "/api/v1/projects/project_1/targets/target_1/development-session/", strings.NewReader(body))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", response.Code, response.Body.String())
	}
	var stored developmentsession.Record
	if err := json.Unmarshal(response.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.LastValid.PreviewURL != previewURL {
		t.Fatalf("stored exact candidate URL = %#v", stored.LastValid)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_1/targets/target_1/development-session/candidate", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("handoff status = %d, body=%s", response.Code, response.Body.String())
	}
	var got handoff
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CandidateID != "candidate_1" || got.PreviewURL != previewURL || got.SessionID != key.ID() {
		t.Fatalf("handoff = %#v", got)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_1/targets/target_1/development-session/candidate/preview", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != previewURL {
		t.Fatalf("preview redirect = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestStablePreviewFailsClosedAndMarksExpiredCandidate(t *testing.T) {
	store := developmentsession.NewMemoryStore()
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "worktree_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	identity := developmentsession.Identity{CandidateID: "candidate_1", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), GraphDigest: "sha256:" + strings.Repeat("b", 64), PreviewURL: "https://target.example/candidates/candidate_1"}
	if _, err := store.Save(t.Context(), developmentsession.Record{ID: key.ID(), Key: key, LastValid: identity}, 0); err != nil {
		t.Fatal(err)
	}
	expired := false
	handler := New(Config{
		Store: store, Enabled: true, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, TargetID: key.TargetID, Environment: key.Environment,
		CurrentPrincipal: func(*http.Request) (string, bool) { return key.OwnerID, true },
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return key.ProjectID, nil },
		ValidateCandidate: func(_ context.Context, owner, candidate string, project projectgraph.ResourceID, target, environment string) (CandidateValidation, error) {
			return CandidateValidation{OwnerID: owner, ProjectID: project, TargetID: target, Environment: environment, Qualified: !expired, Expired: expired, Identity: identityForCandidate(identity, candidate)}, nil
		},
	})
	router := chi.NewRouter()
	handler.Mount(router)
	expired = true
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_1/targets/target_1/development-session/candidate/preview", nil))
	if response.Code != http.StatusGone {
		t.Fatalf("expired preview status = %d, body=%s", response.Code, response.Body.String())
	}
	record, err := store.Resolve(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Diagnostics) != 1 || record.Diagnostics[0].Code != "CANDIDATE_EXPIRED" {
		t.Fatalf("expired session diagnostics = %#v", record.Diagnostics)
	}
}

func TestSessionUpdateRejectsGraphIdentityNotProvenByCandidateAuthority(t *testing.T) {
	store := developmentsession.NewMemoryStore()
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "worktree_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	handler := New(Config{
		Store: store, Enabled: true, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, TargetID: key.TargetID, Environment: key.Environment,
		CurrentPrincipal: func(*http.Request) (string, bool) { return key.OwnerID, true },
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return key.ProjectID, nil },
		ValidateCandidate: func(context.Context, string, string, projectgraph.ResourceID, string, string) (CandidateValidation, error) {
			return CandidateValidation{Qualified: true, OwnerID: key.OwnerID, ProjectID: key.ProjectID, TargetID: key.TargetID, Environment: key.Environment, Identity: developmentsession.Identity{CandidateID: "candidate_1", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), PreviewURL: "https://target.example/candidates/candidate_1"}}, nil
		},
	})
	router := chi.NewRouter()
	handler.Mount(router)
	body := `{"revision":0,"lastValid":{"candidateId":"candidate_1","artifactDigest":"sha256:` + strings.Repeat("a", 64) + `","graphDigest":"sha256:` + strings.Repeat("b", 64) + `"}}`
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/projects/project_1/targets/target_1/development-session/", strings.NewReader(body)))
	if response.Code != http.StatusConflict {
		t.Fatalf("unproven graph status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, err := store.Resolve(t.Context(), key); !errors.Is(err, developmentsession.ErrNotFound) {
		t.Fatalf("unproven graph mutated store: %v", err)
	}
}

func identityForCandidate(identity developmentsession.Identity, candidate string) developmentsession.Identity {
	identity.CandidateID = candidate
	return identity
}

func TestSessionEventsReplayAndCloseAfterDurableRevisionAdvance(t *testing.T) {
	store := developmentsession.NewMemoryStore()
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "worktree_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	if _, err := store.Save(t.Context(), developmentsession.Record{ID: key.ID(), Key: key, Attempted: developmentsession.Identity{ArtifactDigest: "sha256:" + strings.Repeat("a", 64)}}, 0); err != nil {
		t.Fatal(err)
	}
	handler := New(Config{
		Store: store, Enabled: true, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, TargetID: key.TargetID, Environment: key.Environment,
		CurrentPrincipal: func(*http.Request) (string, bool) { return key.OwnerID, true },
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return key.ProjectID, nil },
	})
	router := chi.NewRouter()
	handler.Mount(router)
	server := httptest.NewServer(router)
	defer server.Close()
	response, err := server.Client().Get(server.URL + "/api/v1/projects/project_1/targets/target_1/development-session/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("event response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	initial := readSSEData(t, reader)
	if !strings.Contains(initial, `"revision":1`) {
		t.Fatalf("initial event = %s", initial)
	}
	update := `{"revision":1,"attempted":{"artifactDigest":"sha256:` + strings.Repeat("b", 64) + `"},"diagnostics":[{"code":"EDIT","message":"password=secret","path":"dashboards/orders.yaml","line":12}]}`
	request, err := http.NewRequest(http.MethodPut, server.URL+"/api/v1/projects/project_1/targets/target_1/development-session/", strings.NewReader(update))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	updateResponse, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	updateResponse.Body.Close()
	if updateResponse.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d", updateResponse.StatusCode)
	}
	advanced := readSSEData(t, reader)
	if !strings.Contains(advanced, `"revision":2`) || !strings.Contains(advanced, `"path":"dashboards/orders.yaml"`) || strings.Contains(advanced, "password=secret") {
		t.Fatalf("advanced event = %s", advanced)
	}
}

func TestSessionEventPublicationIsScopedToExactSession(t *testing.T) {
	handler := New(Config{})
	streamA := make(chan developmentsession.Record, 1)
	streamB := make(chan developmentsession.Record, 1)
	handler.streams["session-a"] = map[chan developmentsession.Record]struct{}{streamA: {}}
	handler.streams["session-b"] = map[chan developmentsession.Record]struct{}{streamB: {}}

	handler.publish(developmentsession.Record{ID: "session-b", Revision: 2})
	select {
	case record := <-streamB:
		if record.ID != "session-b" || record.Revision != 2 {
			t.Fatalf("published record = %#v", record)
		}
	default:
		t.Fatal("exact session did not receive its update")
	}
	select {
	case record := <-streamA:
		t.Fatalf("unrelated session received update: %#v", record)
	default:
	}
}

func TestSessionEventsRereadAfterSubscriptionClosesResolveRace(t *testing.T) {
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "worktree_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	first := developmentsession.Record{ID: key.ID(), Key: key, Revision: 1, Attempted: developmentsession.Identity{ArtifactDigest: "sha256:" + strings.Repeat("a", 64)}}
	latest := developmentsession.Record{ID: key.ID(), Key: key, Revision: 2, Attempted: developmentsession.Identity{ArtifactDigest: "sha256:" + strings.Repeat("b", 64)}}
	store := &advancingResolveStore{records: []developmentsession.Record{first, latest}}
	handler := New(Config{
		Store: store, Enabled: true, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, TargetID: key.TargetID, Environment: key.Environment,
		CurrentPrincipal: func(*http.Request) (string, bool) { return key.OwnerID, true },
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return key.ProjectID, nil },
	})
	router := chi.NewRouter()
	handler.Mount(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/projects/project_1/targets/target_1/development-session/events", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("events status = %d, body=%s", response.Code, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, `"revision":2`) || strings.Contains(body, `"revision":1`) {
		t.Fatalf("event did not replay post-subscription revision: %s", body)
	}
}

type advancingResolveStore struct {
	records []developmentsession.Record
	next    int
}

func (s *advancingResolveStore) Resolve(context.Context, developmentsession.Key) (developmentsession.Record, error) {
	index := s.next
	if index >= len(s.records) {
		index = len(s.records) - 1
	}
	s.next++
	return s.records[index], nil
}

func (*advancingResolveStore) Save(context.Context, developmentsession.Record, int64) (developmentsession.Record, error) {
	return developmentsession.Record{}, developmentsession.ErrInvalid
}

func readSSEData(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var data string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(fmt.Errorf("read SSE event: %w", err))
		}
		if strings.HasPrefix(line, "data: ") {
			data += strings.TrimPrefix(line, "data: ")
		}
		if line == "\n" {
			return data
		}
	}
}
