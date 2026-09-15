package httpstore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project/developmentsession"
	developmenthttp "github.com/flidai/leapview/internal/project/developmentsession/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestAuthenticatedStoreRoundTripAndOwnerIsolation(t *testing.T) {
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "checkout_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	memory := developmentsession.NewMemoryStore()
	router := chi.NewRouter()
	developmenthttp.New(developmenthttp.Config{
		Store: memory, Enabled: true, CheckoutID: key.CheckoutID, WorktreeID: key.WorktreeID, TargetID: key.TargetID, Environment: key.Environment,
		CurrentPrincipal: func(r *http.Request) (string, bool) {
			return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.Header.Get("Authorization") != ""
		},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return key.ProjectID, nil },
		ValidateCandidate: func(context.Context, string, string, projectgraph.ResourceID, string, string) (developmenthttp.CandidateValidation, error) {
			return developmenthttp.CandidateValidation{Qualified: true, OwnerID: key.OwnerID, ProjectID: key.ProjectID, TargetID: key.TargetID, Environment: key.Environment, Identity: developmentsession.Identity{CandidateID: "candidate_1", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), PreviewURL: "https://target.example/candidates/candidate_1"}}, nil
		},
	}).Mount(router)
	server := httptest.NewServer(router)
	defer server.Close()
	store, err := New(http.DefaultClient, server.URL, "owner_1", key)
	if err != nil {
		t.Fatal(err)
	}
	record := developmentsession.Record{ID: key.ID(), Key: key, Attempted: developmentsession.Identity{ArtifactDigest: "sha256:" + strings.Repeat("a", 64)}}
	record.LastValid = developmentsession.Identity{CandidateID: "candidate_1", ArtifactDigest: "sha256:" + strings.Repeat("a", 64), PreviewURL: "https://target.example/candidates/candidate_1"}
	updated, err := store.Save(t.Context(), record, 0)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 1 {
		t.Fatalf("revision = %d", updated.Revision)
	}
	loaded, err := store.Resolve(t.Context(), key)
	if err != nil || loaded.LastValid.CandidateID != "candidate_1" {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
	other := key
	other.OwnerID = "owner_2"
	if _, err := store.Resolve(t.Context(), other); err == nil {
		t.Fatal("owner mismatch unexpectedly resolved")
	}
}

func TestStoreRejectsMismatchedResponseScopeAndTrailingJSON(t *testing.T) {
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "checkout_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	valid := developmentsession.Record{ID: key.ID(), Key: key, Revision: 1}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		body string
		want error
	}{
		{name: "mismatched id", body: `{"id":"devsess_wrong"}`, want: developmentsession.ErrOwnerMismatch},
		{name: "trailing json", body: string(encoded) + ` {}`, want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			store, err := New(server.Client(), server.URL, "token", key)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "mismatched id" {
				_, err = store.Save(t.Context(), valid, 0)
			} else {
				_, err = store.Resolve(t.Context(), key)
			}
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("resolve error = %v, want %v", err, test.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "trailing") {
				t.Fatalf("trailing response error = %v", err)
			}
		})
	}
}

func TestStoreBoundsResponseAndDiscardsErrorBody(t *testing.T) {
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "checkout_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("password=secret " + strings.Repeat("x", maxResponseBytes+1)))
	}))
	defer server.Close()
	store, err := New(server.Client(), server.URL, "token", key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Resolve(t.Context(), key)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error body leaked or error absent: %v", err)
	}
}

func TestStoreRequiresRootCanonicalOrigin(t *testing.T) {
	key := developmentsession.Key{OwnerID: "owner_1", CheckoutID: "checkout_1", WorktreeID: "checkout_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
	for _, origin := range []string{"https://example.test/api", "https://example.test/api/", "https://example.test?token=secret", "https://user:pass@example.test"} {
		if _, err := New(http.DefaultClient, origin, "token", key); err == nil {
			t.Errorf("New(%q) unexpectedly accepted non-canonical origin", origin)
		}
	}
}
