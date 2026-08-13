package avatar

import "testing"

func TestURLBuildsCanonicalContentAddressedPath(t *testing.T) {
	got := URL(Metadata{PrincipalID: " user/one ", SHA256: " ABC123 "})
	want := "/profile/avatars/user%2Fone/abc123"
	if got != want {
		t.Fatalf("URL() = %q, want %q", got, want)
	}
	if got := URL(Metadata{PrincipalID: "user"}); got != "" {
		t.Fatalf("URL() without digest = %q, want empty", got)
	}
	if got := URLForPrincipal("user", Metadata{SHA256: "abc123"}); got != "/profile/avatars/user/abc123" {
		t.Fatalf("URLForPrincipal() = %q", got)
	}
}
