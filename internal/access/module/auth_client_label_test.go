package module

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

func TestBrowserClientLabel(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		userAgent string
		want      string
	}{
		"chrome on windows": {
			userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36",
			want:      "Chrome on Windows",
		},
		"firefox on linux": {
			userAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:142.0) Gecko/20100101 Firefox/142.0",
			want:      "Firefox on Linux",
		},
		"edge before chrome": {
			userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0",
			want:      "Edge on Windows",
		},
		"safari on macos": {
			userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.6 Safari/605.1.15",
			want:      "Safari on macOS",
		},
		"unknown": {userAgent: "", want: "Browser"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := browserClientLabel(test.userAgent); got != test.want {
				t.Fatalf("browserClientLabel(%q) = %q, want %q", test.userAgent, got, test.want)
			}
		})
	}
}

func TestCreateBrowserSessionPassesClassifiedLabel(t *testing.T) {
	t.Parallel()
	repository := &recordingLabelRepository{}
	request := httptest.NewRequest("POST", "/auth/local/login", nil)
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36")

	token, err := createBrowserSession(request, repository, "principal-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if token != "session-token" || repository.label != "Chrome on Windows" {
		t.Fatalf("created session = token %q label %q", token, repository.label)
	}
}

type recordingLabelRepository struct {
	access.Repository
	label string
}

func (repository *recordingLabelRepository) CreateSessionWithClientLabel(_ context.Context, _ string, _ time.Duration, label string) (string, error) {
	repository.label = label
	return "session-token", nil
}
