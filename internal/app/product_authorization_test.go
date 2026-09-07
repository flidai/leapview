package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	product "github.com/flidai/leapview/internal/admin/product"
)

func TestProductAdministrationUsesGeneratedRouteDispatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	principal := testPlatformPrincipal(t, ctx, store, "platform-admin@example.test", "Platform Admin")
	token := testAPIToken(t, ctx, store, principal.ID, "platform-manage")
	productStorage := newProductAuthorizationStorage()
	service, err := product.NewWithStorage(productStorage, productAuthorizationBlobs{})
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Product: service}))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/instance/settings", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"product-1"` {
		t.Fatalf("generated product route status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/instance/settings", strings.NewReader(`{"displayName":"Acme Analytics"}`))
	patch.Header.Set("Authorization", "Bearer "+token)
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("If-Match", response.Header().Get("ETag"))
	patch.Header.Set("X-Request-ID", "req_product_patch")
	patched := httptest.NewRecorder()
	server.Routes().ServeHTTP(patched, patch)
	if patched.Code != http.StatusOK || patched.Header().Get("ETag") != `"product-2"` || !strings.Contains(patched.Body.String(), `"displayName":"Acme Analytics"`) {
		t.Fatalf("generated product patch status=%d etag=%q body=%s", patched.Code, patched.Header().Get("ETag"), patched.Body.String())
	}
	action, metadata := productStorage.lastMutation()
	if action != "product.identity.updated" || !strings.Contains(metadata, `"payloadSchema":"ProductIdentityUpdatedAuditPayload"`) || !strings.Contains(metadata, `"fields":["displayName"]`) {
		t.Fatalf("product command audit action=%q metadata=%s", action, metadata)
	}

	stale := httptest.NewRequest(http.MethodPatch, "/api/v1/instance/settings", strings.NewReader(`{"displayName":"Stale"}`))
	stale.Header.Set("Authorization", "Bearer "+token)
	stale.Header.Set("Content-Type", "application/json")
	stale.Header.Set("If-Match", `"invalid"`)
	staleResponse := httptest.NewRecorder()
	server.Routes().ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusPreconditionFailed || !strings.Contains(staleResponse.Body.String(), `"code":"PRODUCT_IDENTITY_PRECONDITION_FAILED"`) {
		t.Fatalf("generated stale product patch status=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
}

type productAuthorizationBlobs struct{}

type productAuthorizationStorage struct {
	mu        sync.Mutex
	identity  product.Identity
	mutations []product.MutationRequest
}

func newProductAuthorizationStorage() *productAuthorizationStorage {
	return &productAuthorizationStorage{identity: product.Identity{
		DisplayName: product.DefaultDisplayName,
		Revision:    1,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}}
}

func (s *productAuthorizationStorage) Get(context.Context) (product.Identity, error) {
	if s == nil {
		return product.Identity{}, product.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProductIdentity(s.identity), nil
}

func (s *productAuthorizationStorage) Ping(context.Context) error {
	if s == nil {
		return product.ErrInvalid
	}
	return nil
}

func (s *productAuthorizationStorage) Mutate(ctx context.Context, req product.MutationRequest) (product.Identity, error) {
	if s == nil {
		return product.Identity{}, product.ErrInvalid
	}
	if req.ExpectedRevision <= 0 {
		return product.Identity{}, product.ErrPrecondition
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity.Revision != req.ExpectedRevision {
		return product.Identity{}, product.ErrPrecondition
	}
	if req.CheckConcurrency != nil {
		if err := req.CheckConcurrency(ctx, s.identity.Revision); err != nil {
			return product.Identity{}, err
		}
	}
	switch req.Kind {
	case product.MutationDisplayName:
		s.identity.DisplayName = req.DisplayName
	case product.MutationLogo:
		if req.Logo == nil {
			return product.Identity{}, product.ErrInvalid
		}
		logo := *req.Logo
		s.identity.Logo = &logo
	case product.MutationDeleteLogo:
		if s.identity.Logo == nil {
			return product.Identity{}, product.ErrPrecondition
		}
		s.identity.Logo = nil
	case product.MutationReset:
		s.identity.DisplayName = product.DefaultDisplayName
		s.identity.Logo = nil
	default:
		return product.Identity{}, product.ErrInvalid
	}
	s.identity.Revision++
	s.identity.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.mutations = append(s.mutations, req)
	return cloneProductIdentity(s.identity), nil
}

func (s *productAuthorizationStorage) lastMutation() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mutations) == 0 {
		return "", ""
	}
	last := s.mutations[len(s.mutations)-1]
	return last.Action, last.MetadataJSON
}

func cloneProductIdentity(identity product.Identity) product.Identity {
	if identity.Logo != nil {
		logo := *identity.Logo
		identity.Logo = &logo
	}
	return identity
}

func (productAuthorizationBlobs) Put(_ context.Context, expected adminmodule.ProductBlob, body io.Reader) (adminmodule.ProductBlob, error) {
	_, err := io.Copy(io.Discard, body)
	return expected, err
}

func (productAuthorizationBlobs) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, adminmodule.ErrProductLogoNotFound
}
