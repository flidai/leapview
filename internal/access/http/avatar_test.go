package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/access/avatar"
)

func TestUploadCurrentAvatarReturnsContentAddressedMetadata(t *testing.T) {
	digest := strings.Repeat("a", 64)
	service := &fakeAvatarService{metadata: avatar.Metadata{
		PrincipalID: "principal_1", SHA256: digest, MediaType: "image/png",
		SizeBytes: 3, Width: 256, Height: 256, UpdatedAt: "2026-08-08 12:00:00",
	}}
	handler := Handler{Avatar: service, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
		return Principal{ID: "principal_1", Kind: access.PrincipalKindUser}, true
	}}
	request := httptest.NewRequest(stdhttp.MethodPut, "/api/v1/me/avatar", bytes.NewBufferString("raw"))
	recorder := httptest.NewRecorder()
	handler.UploadCurrentAvatar(recorder, request, "image/jpeg")
	if recorder.Code != stdhttp.StatusOK || service.uploadedPrincipal != "principal_1" {
		t.Fatalf("response = %d %s, uploaded principal=%q", recorder.Code, recorder.Body.String(), service.uploadedPrincipal)
	}
	var response accessgen.AvatarResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Sha256 != digest || response.Url != "/profile/avatars/principal_1/"+digest || response.MediaType != "image/png" {
		t.Fatalf("response = %#v", response)
	}
}

func TestUploadCurrentAvatarRejectsUnsupportedMediaTypeBeforeStorage(t *testing.T) {
	service := &fakeAvatarService{}
	handler := Handler{Avatar: service, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
		return Principal{ID: "principal_1", Kind: access.PrincipalKindUser}, true
	}}
	recorder := httptest.NewRecorder()
	handler.UploadCurrentAvatar(recorder, httptest.NewRequest(stdhttp.MethodPut, "/api/v1/me/avatar", bytes.NewBufferString("svg")), "image/svg+xml")
	if recorder.Code != stdhttp.StatusUnsupportedMediaType || service.uploadedPrincipal != "" {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetPrincipalAvatarServesImmutablePrivateResponseAndRevalidates(t *testing.T) {
	digest := strings.Repeat("b", 64)
	service := &fakeAvatarService{metadata: avatar.Metadata{PrincipalID: "principal_1", SHA256: digest, MediaType: "image/png", SizeBytes: 3, Width: 256, Height: 256}, body: []byte("png")}
	handler := Handler{Avatar: service}

	request := httptest.NewRequest(stdhttp.MethodGet, "/profile/avatars/principal_1/"+digest, nil)
	recorder := httptest.NewRecorder()
	handler.GetPrincipalAvatar(recorder, request, "principal_1", digest)
	if recorder.Code != stdhttp.StatusOK || recorder.Body.String() != "png" || recorder.Header().Get("ETag") != `"`+digest+`"` || recorder.Header().Get("Cache-Control") != avatarCacheControl {
		t.Fatalf("response = %d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}

	revalidated := httptest.NewRequest(stdhttp.MethodGet, "/profile/avatars/principal_1/"+digest, nil)
	revalidated.Header.Set("If-None-Match", `"`+digest+`"`)
	revalidatedRecorder := httptest.NewRecorder()
	handler.GetPrincipalAvatar(revalidatedRecorder, revalidated, "principal_1", digest)
	if revalidatedRecorder.Code != stdhttp.StatusNotModified || revalidatedRecorder.Body.Len() != 0 {
		t.Fatalf("revalidated response = %d body=%q", revalidatedRecorder.Code, revalidatedRecorder.Body.String())
	}
}

func TestDeleteCurrentAvatarIsIdempotent(t *testing.T) {
	service := &fakeAvatarService{deleteErr: avatar.ErrNotFound}
	handler := Handler{Avatar: service, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
		return Principal{ID: "principal_1", Kind: access.PrincipalKindUser}, true
	}}
	recorder := httptest.NewRecorder()
	handler.DeleteCurrentAvatar(recorder, httptest.NewRequest(stdhttp.MethodDelete, "/api/v1/me/avatar", nil))
	if recorder.Code != stdhttp.StatusNoContent || service.deletedPrincipal != "principal_1" {
		t.Fatalf("response = %d body=%q deleted=%q", recorder.Code, recorder.Body.String(), service.deletedPrincipal)
	}
}

type fakeAvatarService struct {
	metadata          avatar.Metadata
	body              []byte
	uploadErr         error
	openErr           error
	deleteErr         error
	uploadedPrincipal string
	deletedPrincipal  string
}

func (s *fakeAvatarService) Upload(_ context.Context, principalID string, _ io.Reader) (avatar.Metadata, error) {
	s.uploadedPrincipal = principalID
	return s.metadata, s.uploadErr
}

func (s *fakeAvatarService) Current(context.Context, string) (avatar.Metadata, error) {
	if s.openErr != nil {
		return avatar.Metadata{}, s.openErr
	}
	return s.metadata, nil
}

func (s *fakeAvatarService) Open(context.Context, string, string) (io.ReadCloser, avatar.Metadata, error) {
	if s.openErr != nil {
		return nil, avatar.Metadata{}, s.openErr
	}
	return io.NopCloser(bytes.NewReader(s.body)), s.metadata, nil
}

func (s *fakeAvatarService) Delete(_ context.Context, principalID string) error {
	s.deletedPrincipal = principalID
	return s.deleteErr
}
