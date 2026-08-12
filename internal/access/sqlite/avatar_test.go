package sqlite

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
	"github.com/flidai/leapview/internal/platform"
)

func TestAvatarMetadataRoundTrip(t *testing.T) {
	store, err := platform.Open(t.Context(), t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository := NewRepository(store.SQLDB())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "avatar@example.com", DisplayName: "Avatar"})
	if err != nil {
		t.Fatal(err)
	}

	want := avatar.Metadata{PrincipalID: principal.ID, SHA256: strings.Repeat("a", 64), MediaType: "image/png", SizeBytes: 1234, Width: 256, Height: 256}
	got, err := repository.UpsertAvatar(t.Context(), want)
	if err != nil {
		t.Fatal(err)
	}
	if got.PrincipalID != want.PrincipalID || got.SHA256 != want.SHA256 || got.MediaType != want.MediaType || got.SizeBytes != want.SizeBytes || got.Width != want.Width || got.Height != want.Height || got.UpdatedAt == "" {
		t.Fatalf("UpsertAvatar() = %#v, want %#v", got, want)
	}
	loaded, err := repository.Avatar(t.Context(), principal.ID)
	if err != nil || loaded != got {
		t.Fatalf("Avatar() = %#v, %v; want %#v", loaded, err, got)
	}
	if err := repository.DeleteAvatar(t.Context(), principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Avatar(t.Context(), principal.ID); !errors.Is(err, avatar.ErrNotFound) {
		t.Fatalf("Avatar() after delete error = %v", err)
	}
}
