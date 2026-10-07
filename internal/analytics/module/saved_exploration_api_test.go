package module

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestStableSavedIDIsIdempotentAndActorScoped(t *testing.T) {
	first, err := stableSavedID("exploration-", "project-1", "principal-1", "request-1", "create")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := stableSavedID("exploration-", "project-1", "principal-1", "request-1", "create")
	if err != nil {
		t.Fatal(err)
	}
	if first != retry {
		t.Fatalf("retry identity = %q, want %q", retry, first)
	}
	otherActor, err := stableSavedID("exploration-", "project-1", "principal-2", "request-1", "create")
	if err != nil {
		t.Fatal(err)
	}
	if first == otherActor {
		t.Fatal("stable ID must be scoped to the authenticated actor")
	}
}

func TestParseRevisionTokenRequiresCompleteExactToken(t *testing.T) {
	token := saved.RevisionToken{RevisionID: "revision-1", Number: 2, ContentHash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	header := revisionETag(token)
	if len(header) < 2 || header[0] != '"' || header[len(header)-1] != '"' {
		t.Fatalf("revision ETag = %q, want one quoted entity-tag", header)
	}
	for _, char := range header[1 : len(header)-1] {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_') {
			t.Fatalf("revision ETag contains non-base64url character %q: %q", char, header)
		}
	}
	parsed, err := parseRevisionToken(header)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != token {
		t.Fatalf("parsed token = %#v, want %#v", parsed, token)
	}
	for _, invalid := range []string{
		``,
		`{"revisionId":"revision-1","number":2}`,
		`W/"` + strings.Trim(header, `"`) + `"`,
		`"not base64url"`,
		`"%%%%"`,
	} {
		if _, err := parseRevisionToken(invalid); !errors.Is(err, saved.ErrInvalidRevision) {
			t.Errorf("invalid token %q error = %v, want invalid revision", invalid, err)
		}
	}

	canonicalJSON, err := json.Marshal(token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseRevisionToken(`"` + base64.RawURLEncoding.EncodeToString(append([]byte(`{"number":2,"revisionId":"revision-1",`), canonicalJSON[len(`{"revisionId":"revision-1","number":2,`):]...)) + `"`); !errors.Is(err, saved.ErrInvalidRevision) {
		t.Fatalf("non-canonical token error = %v, want invalid revision", err)
	}
}

func TestSavedExplorationFailureHidesForbiddenAsNotFound(t *testing.T) {
	classified := classifySavedExplorationFailure(errors.Join(saved.ErrNotFound, errors.New("policy denied")))
	kind, ok := apigenfailure.KindOf(classified)
	if !ok || kind != "not_found" {
		t.Fatalf("failure kind = %q (%v), want not_found", kind, ok)
	}
}

func TestSavedExplorationSummaryIsMetadataOnly(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	lifecycle := saved.Lifecycle{
		ProjectID: projectgraph.ResourceID("project-1"), ID: "exploration-1", OwnerPrincipalID: "principal-1",
		Title: "Sales", Slug: "sales", Visibility: saved.VisibilityPrivate, SemanticModelID: "model-1",
		Status: saved.StatusActive, CreatedAt: now, UpdatedAt: now,
		CurrentRevision: saved.RevisionMetadata{ID: "revision-1", Number: 1, ContentHash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", CreatedAt: now, CreatedBy: "principal-1", ServingIdentity: projectgraph.ServingIdentity{ProjectID: "project-1", Environment: "prod", GenerationID: "generation-1"}},
	}
	response := savedExplorationSummary(lifecycle)
	if response.Id != lifecycle.ID.String() || response.Revision.Number != int64(lifecycle.CurrentRevision.Number) {
		t.Fatalf("summary mapping = %#v", response)
	}
}

func TestBodySlugMatchesDomainAlphabetAndPreservesExplicitValue(t *testing.T) {
	fallback := "exploration-deadbeef"
	if got := bodySlug(nil, "Sales !!! 2026", fallback); got != "sales-2026-deadbeef" {
		t.Fatalf("ASCII slug = %q, want sales-2026-deadbeef", got)
	}
	for _, title := range []string{"日本語", "!!!"} {
		if got := bodySlug(nil, title, fallback); got != "deadbeef" {
			t.Fatalf("fallback slug for %q = %q, want deadbeef", title, got)
		}
	}
	explicit := "Bad Value"
	if got := bodySlug(&explicit, "ignored", fallback); got != explicit {
		t.Fatalf("explicit slug = %q, want unchanged %q", got, explicit)
	}
	empty := ""
	if got := bodySlug(&empty, "ignored", fallback); got != empty {
		t.Fatalf("explicit empty slug = %q, want unchanged empty value", got)
	}
	longTitle := strings.Repeat("a", 200)
	if got := bodySlug(nil, longTitle, "exploration-0123456789abcdef0123456789abcdef"); len(got) > saved.MaxSlugLength {
		t.Fatalf("generated slug length = %d, want <= %d", len(got), saved.MaxSlugLength)
	}
}
