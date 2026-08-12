package module

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/access/avatar"
	accesshttp "github.com/flidai/leapview/internal/access/http"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/go-chi/chi/v5"
)

func TestAuthenticatedBrowserAvatarRoutesUseProtectedPrincipal(t *testing.T) {
	digest := strings.Repeat("a", 64)
	service := &routeAvatarService{metadata: avatar.Metadata{
		PrincipalID: "dev", SHA256: digest, MediaType: "image/png", SizeBytes: 3,
		Width: 256, Height: 256, UpdatedAt: "2026-08-08 12:00:00",
	}}
	module, err := newSurface(surfaceConfig{Avatar: nil})
	if err != nil {
		t.Fatal(err)
	}
	module.handler.Avatar = service
	router := chi.NewRouter()
	module.MountAuthenticatedBrowser(router)

	upload := httptest.NewRequest(http.MethodPut, "/profile/avatar", bytes.NewBufferString("raw"))
	upload.Header.Set("Content-Type", "image/png")
	upload.Header.Set(uicommand.HeaderOperationID, accessgen.GenUIActionUploadCurrentAvatar().OperationID())
	upload.Header.Set("X-Request-ID", "request-avatar-upload")
	uploadRecorder := httptest.NewRecorder()
	router.ServeHTTP(uploadRecorder, upload)
	if uploadRecorder.Code != http.StatusOK || service.uploadedPrincipal != "dev" {
		t.Fatalf("upload response=%d %s principal=%q", uploadRecorder.Code, uploadRecorder.Body.String(), service.uploadedPrincipal)
	}

	readRecorder := httptest.NewRecorder()
	router.ServeHTTP(readRecorder, httptest.NewRequest(http.MethodGet, "/profile/avatars/dev/"+digest, nil))
	if readRecorder.Code != http.StatusOK || readRecorder.Body.String() != "png" {
		t.Fatalf("read response=%d %q", readRecorder.Code, readRecorder.Body.String())
	}
}

type routeAvatarService struct {
	metadata          avatar.Metadata
	uploadedPrincipal string
}

func (s *routeAvatarService) Upload(_ context.Context, principalID string, _ io.Reader) (avatar.Metadata, error) {
	s.uploadedPrincipal = principalID
	return s.metadata, nil
}

func (s *routeAvatarService) Current(context.Context, string) (avatar.Metadata, error) {
	return s.metadata, nil
}

func (s *routeAvatarService) Open(context.Context, string, string) (io.ReadCloser, avatar.Metadata, error) {
	return io.NopCloser(bytes.NewBufferString("png")), s.metadata, nil
}

func (*routeAvatarService) Delete(context.Context, string) error { return nil }

var _ accesshttp.AvatarService = (*routeAvatarService)(nil)
