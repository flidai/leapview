package credential

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/flidai/leapview/internal/credential/encryption"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestDraftSaveDeniesBeforeReadingOrEncryptingSecrets(t *testing.T) {
	denied := errors.New("denied")
	auth := &testAuthorizer{err: denied}
	service := &Service{authorizer: auth}
	_, err := service.SaveDraft(context.Background(), "actor", Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}, map[string]string{"password": "must-not-be-read"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("save error = %v, want forbidden", err)
	}
	if len(auth.pairs) != 1 || auth.pairs[0].Action != access.ActionConnectionManage {
		t.Fatalf("authorization = %#v", auth.pairs)
	}
}

type testAuthorizer struct {
	err   error
	pairs []access.PermissionPair
}

func (a *testAuthorizer) RequirePermission(_ context.Context, _ string, pair access.PermissionPair) error {
	a.pairs = append(a.pairs, pair)
	return a.err
}

func TestSaveDraftEncryptsOnceAndAuditsOnlyMetadata(t *testing.T) {
	service, store, keys, scopes, auth := newTestService(t)
	secret := "  sentinel-password\n"
	first, err := service.SaveDraft(t.Context(), "actor", connectionResource(), map[string]string{"password": secret})
	if err != nil {
		t.Fatal(err)
	}
	if store.reservations != 1 || len(store.saved) != 1 || len(keys.plaintexts) != 1 {
		t.Fatal("draft save did not reserve/encrypt/persist exactly once")
	}
	var decoded map[string]string
	if err := json.Unmarshal(keys.plaintexts[0], &decoded); err != nil || decoded["password"] != secret {
		t.Fatal("secret bytes changed")
	}
	if first.Binding.OwnerID != scopes.scope.OwnerID || first.Binding.Destination != scopes.scope.Destination {
		t.Fatal("authoritative scope was not preserved")
	}
	for _, pair := range auth.pairs {
		if pair.Action != access.ActionConnectionManage || string(pair.Target.ProjectID) != "project" || string(pair.Target.ResourceID) != "connection" {
			t.Fatal("wrong permission pair")
		}
	}
	serialized, err := json.Marshal(struct {
		Metadata Metadata
		Audit    string
	}{first, fmt.Sprintf("%+v", store.audits[0])})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(keys.plaintexts[0])
	if bytes.Contains(serialized, []byte("sentinel-password")) || bytes.Contains(serialized, []byte(hex.EncodeToString(digest[:]))) || store.audits[0].RequestDigest != "" {
		t.Fatal("secret or raw-body digest leaked into metadata/audit")
	}
	second, err := service.SaveDraft(t.Context(), "actor", connectionResource(), map[string]string{"password": secret})
	if err != nil {
		t.Fatal(err)
	}
	if first.Binding.VersionID == second.Binding.VersionID {
		t.Fatal("independent saves unexpectedly overwrite a version")
	}
	metadata, err := service.GetDraft(t.Context(), "actor", connectionResource(), first.Binding.VersionID)
	if err != nil || metadata.Binding.VersionID != first.Binding.VersionID {
		t.Fatalf("read metadata: %v", err)
	}
	if auth.pairs[len(auth.pairs)-1].Action != access.ActionConnectionRead {
		t.Fatal("metadata read did not authorize read")
	}
}

func TestDraftSaveRejectsInvalidFieldsAndScopeSubstitution(t *testing.T) {
	for name, fields := range map[string]map[string]string{
		"unknown":      {"password": "valid", "unexpected": "must-not-leak"},
		"missing":      {},
		"empty":        {"password": ""},
		"oversize":     {"password": strings.Repeat("x", encryption.MaxPlaintextSize)},
		"invalid_utf8": {"password": string([]byte{0xff})},
	} {
		t.Run(name, func(t *testing.T) {
			service, store, keys, _, _ := newTestService(t)
			if _, err := service.SaveDraft(t.Context(), "actor", connectionResource(), fields); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
			if store.reservations != 0 || len(store.saved) != 0 || len(keys.plaintexts) != 0 {
				t.Fatal("invalid input caused encryption or persistence")
			}
		})
	}
	service, store, _, scopes, _ := newTestService(t)
	scopes.scope.Resource.Environment = "other-environment"
	if _, err := service.SaveDraft(t.Context(), "actor", connectionResource(), map[string]string{"password": "p"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("scope substitution = %v", err)
	}
	if store.reservations != 0 {
		t.Fatal("scope substitution consumed encryption")
	}
}

func TestDraftSaveRechecksAuthorityAndKeepsFailedReservation(t *testing.T) {
	service, store, keys, _, auth := newTestService(t)
	keys.after = func() { auth.err = errors.New("revoked") }
	if _, err := service.SaveDraft(t.Context(), "actor", connectionResource(), map[string]string{"password": "p"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revocation = %v", err)
	}
	if store.reservations != 1 || len(store.saved) != 0 {
		t.Fatal("revoked save persisted, or reclaimed reservation")
	}
	service, store, _, _, _ = newTestService(t)
	store.saveErr = errors.New("backend error that might contain submitted value")
	if _, err := service.SaveDraft(t.Context(), "actor", connectionResource(), map[string]string{"password": "p"}); err != ErrUnavailable {
		t.Fatal("storage detail escaped safe error boundary")
	}
	if store.reservations != 1 {
		t.Fatal("failed save reclaimed encryption budget")
	}
}

func TestAgentDraftUsesInstanceAuthorityAndRejectsForeignRead(t *testing.T) {
	service, store, _, scopes, auth := newTestService(t)
	resource := Resource{ScopeKind: "agent", ResourceID: "instance-a"}
	scopes.scope.Resource = resource
	scopes.scope.Provider = "openai"
	scopes.scope.Purpose = "agent-provider"
	metadata, err := service.SaveDraft(t.Context(), "actor", resource, map[string]string{"api_key": "sentinel-agent-key"})
	if err != nil {
		t.Fatal(err)
	}
	pair := auth.pairs[0]
	if pair.Action != access.ActionPlatformSettingsUpdate || pair.Target.InstanceID != "instance-a" {
		t.Fatal("agent write did not use exact instance authority")
	}
	// Even a broken repository returning another tenant's row must not leak metadata.
	store.saved[0].Metadata.Binding.OwnerID = "foreign-owner"
	if _, err := service.GetDraft(t.Context(), "actor", resource, metadata.Binding.VersionID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign metadata read = %v", err)
	}
}

func TestDraftListIsBoundedAuthorizedAndPreservesHistoricalScopeMetadata(t *testing.T) {
	service, store, _, scopes, auth := newTestService(t)
	resource := connectionResource()
	first, err := service.SaveDraft(t.Context(), "actor", resource, map[string]string{"password": "first-secret"})
	if err != nil {
		t.Fatal(err)
	}
	store.saved[0].Metadata.CreatedAt = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	oldDestination := first.Binding.Destination
	scopes.scope.Destination = "sha256:" + strings.Repeat("b", 64)
	scopes.scope.Provider = "mysql"
	second, err := service.SaveDraft(t.Context(), "actor", resource, map[string]string{"password": "second-secret"})
	if err != nil {
		t.Fatal(err)
	}
	store.saved[1].Metadata.CreatedAt = store.saved[0].Metadata.CreatedAt.Add(time.Minute)

	page, err := service.ListDrafts(t.Context(), "actor", resource, 1, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Binding.VersionID != second.Binding.VersionID || page.NextBeforeVersionID != second.Binding.VersionID {
		t.Fatalf("first page = %#v, %v", page, err)
	}
	page, err = service.ListDrafts(t.Context(), "actor", resource, 1, page.NextBeforeVersionID)
	if err != nil || len(page.Items) != 1 || page.Items[0].Binding.VersionID != first.Binding.VersionID || page.NextBeforeVersionID != "" {
		t.Fatalf("second page = %#v, %v", page, err)
	}
	if page.Items[0].Binding.Destination != oldDestination || page.Items[0].Binding.Provider != "postgres" {
		t.Fatal("draft listing replaced historical provider/destination metadata with the current scope")
	}
	if auth.pairs[len(auth.pairs)-1].Action != access.ActionConnectionRead {
		t.Fatal("draft listing did not require exact-resource read permission")
	}
	if _, err := service.ListDrafts(t.Context(), "actor", resource, MaxDraftPageSize+1, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized page limit error = %v", err)
	}
	if _, err := service.ListDrafts(t.Context(), "actor", resource, 1, "not-a-uuid"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("malformed cursor error = %v", err)
	}
	serialized, err := json.Marshal(page.Items)
	if err != nil || bytes.Contains(serialized, []byte("first-secret")) || bytes.Contains(serialized, []byte("second-secret")) {
		t.Fatal("draft metadata page exposed secret input")
	}
}

func TestNewServiceRequiresEveryDependency(t *testing.T) {
	service, _, _, _, _ := newTestService(t)
	var typedNil *testRepository
	for _, test := range []struct {
		repo   Repository
		keys   Encryptor
		scopes ScopeResolver
		auth   Authorizer
	}{
		{nil, service.keys, service.scopes, service.authorizer},
		{typedNil, service.keys, service.scopes, service.authorizer},
		{service.repository, nil, service.scopes, service.authorizer},
		{service.repository, service.keys, nil, service.authorizer},
		{service.repository, service.keys, service.scopes, nil},
	} {
		if _, err := NewService(test.repo, test.keys, test.scopes, test.auth); !errors.Is(err, ErrUnavailable) {
			t.Fatal("missing dependency accepted")
		}
	}
}

func connectionResource() Resource {
	return Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}
}

type testScopeResolver struct{ scope Scope }

func (s *testScopeResolver) ResolveCredentialScope(context.Context, Resource) (Scope, error) {
	return s.scope, nil
}

type testRepository struct {
	reservations int
	saved        []StoredVersion
	audits       []access.AuditIntent
	saveErr      error
}

func (r *testRepository) ReserveEncryption(context.Context, string, string, encryption.KeyCommitment) error {
	r.reservations++
	return nil
}
func (r *testRepository) SaveDraft(_ context.Context, version StoredVersion, audit access.AuditIntent) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.saved = append(r.saved, version)
	r.audits = append(r.audits, audit)
	return nil
}
func (r *testRepository) GetDraftMetadata(_ context.Context, deploymentID, ownerID string, resource Resource, id string) (Metadata, error) {
	for _, v := range r.saved {
		if metadataMatchesScope(v.Metadata, deploymentID, ownerID, resource) && v.Metadata.Binding.VersionID == id {
			return v.Metadata, nil
		}
	}
	return Metadata{}, ErrNotFound
}
func (r *testRepository) ListDrafts(_ context.Context, deploymentID, ownerID string, resource Resource, limit int, beforeVersionID string) (DraftPage, error) {
	items := make([]Metadata, 0, len(r.saved))
	for _, version := range r.saved {
		if metadataMatchesScope(version.Metadata, deploymentID, ownerID, resource) {
			items = append(items, version.Metadata)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].Binding.VersionID > items[j].Binding.VersionID
	})
	start := 0
	if beforeVersionID != "" {
		for index, item := range items {
			if item.Binding.VersionID == beforeVersionID {
				start = index + 1
				break
			}
		}
		if start == 0 {
			return DraftPage{}, ErrInvalidCursor
		}
	}
	end := min(start+limit, len(items))
	page := DraftPage{Items: append([]Metadata(nil), items[start:end]...)}
	if end < len(items) && len(page.Items) > 0 {
		page.NextBeforeVersionID = page.Items[len(page.Items)-1].Binding.VersionID
	}
	return page, nil
}

type testEncryptor struct {
	plaintexts [][]byte
	after      func()
}

func (*testEncryptor) DeploymentID() string { return "deployment-a" }
func (e *testEncryptor) Encrypt(ctx context.Context, budget encryption.Budget, binding encryption.Binding, plaintext []byte) (encryption.Envelope, error) {
	if err := budget.ReserveEncryption(ctx, binding.DeploymentID, "key-a", encryption.KeyCommitment(sha256.Sum256([]byte("test-key-a")))); err != nil {
		return encryption.Envelope{}, err
	}
	e.plaintexts = append(e.plaintexts, bytes.Clone(plaintext))
	if e.after != nil {
		e.after()
	}
	return encryption.Envelope{Format: "test-only", KeyID: "key-a", Ciphertext: []byte("opaque ciphertext")}, nil
}
func newTestService(t *testing.T) (*Service, *testRepository, *testEncryptor, *testScopeResolver, *testAuthorizer) {
	t.Helper()
	repo := &testRepository{}
	keys := &testEncryptor{}
	auth := &testAuthorizer{}
	scopes := &testScopeResolver{Scope{Resource: connectionResource(), OwnerID: "customer-a", Purpose: "connection", Provider: "postgres", Destination: "sha256:" + strings.Repeat("a", 64)}}
	service, err := NewService(repo, keys, scopes, auth)
	if err != nil {
		t.Fatal(err)
	}
	return service, repo, keys, scopes, auth
}
