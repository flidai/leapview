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
		"chromium on linux": {
			userAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chromium/140.0.0.0 Safari/537.36",
			want:      "Chromium on Linux",
		},
		"samsung internet on android": {
			userAgent: "Mozilla/5.0 (Linux; Android 15; SM-S938B) AppleWebKit/537.36 Chrome/140.0.0.0 Mobile Safari/537.36 SamsungBrowser/28.0",
			want:      "Samsung Internet on Android",
		},
		"vivaldi on windows": {
			userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36 Vivaldi/7.5.3735.66",
			want:      "Vivaldi on Windows",
		},
		"yandex on android": {
			userAgent: "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/140.0.0.0 YaBrowser/25.8.0.0 Mobile Safari/537.36",
			want:      "Yandex Browser on Android",
		},
		"duckduckgo on ios": {
			userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1 DuckDuckGo/7",
			want:      "DuckDuckGo on iOS",
		},
		"silk on android": {
			userAgent: "Mozilla/5.0 (Linux; Android 9; KFKAWI) AppleWebKit/537.36 (KHTML, like Gecko) Silk/130.4.1 like Chrome/130.0.0.0 Safari/537.36",
			want:      "Silk on Android",
		},
		"uc browser on android": {
			userAgent: "Mozilla/5.0 (Linux; U; Android 14; en-US; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 UCBrowser/15.0.0.0 Mobile Safari/537.36",
			want:      "UC Browser on Android",
		},
		"huawei browser on android": {
			userAgent: "Mozilla/5.0 (Linux; Android 14; HUAWEI P60) AppleWebKit/537.36 Chrome/130.0.0.0 Mobile Safari/537.36 HuaweiBrowser/16.0.0.0",
			want:      "Huawei Browser on Android",
		},
		"xiaomi browser on android": {
			userAgent: "Mozilla/5.0 (Linux; Android 14; 23127PN0CG) AppleWebKit/537.36 Chrome/130.0.0.0 Mobile Safari/537.36 MiuiBrowser/20.0.0",
			want:      "Xiaomi Browser on Android",
		},
		"brave on ios": {
			userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_7 like Mac OS X) AppleWebKit/605.1.15 Version/26.5 Mobile/15E148 Safari/604.1 Brave",
			want:      "Brave on iOS",
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

func TestBrowserClientLabelFromRequestBraveHint(t *testing.T) {
	request := httptest.NewRequest("POST", "/auth/local/login", nil)
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36")
	request.Header.Set("Sec-CH-UA", `"Brave";v="140", "Chromium";v="140", "Not.A/Brand";v="99"`)
	if got := browserClientLabelFromRequest(request); got != "Brave on Windows" {
		t.Fatalf("browser label = %q, want Brave on Windows", got)
	}
}

func TestBrowserClientLabelFromRequestClientHints(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		userAgent  string
		clientHint string
		platform   string
		want       string
	}{
		"edge brand takes priority over chromium brands": {
			userAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36",
			clientHint: `"Not.A/Brand";v="99", "Chromium";v="140", "Microsoft Edge";v="140"`,
			want:       "Edge on Windows",
		},
		"opera brand takes priority over chrome": {
			userAgent:  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36",
			clientHint: `"Opera";v="120", "Chromium";v="140"`,
			want:       "Opera on Linux",
		},
		"chrome brand remains chrome": {
			userAgent:  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36",
			clientHint: `"Not.A/Brand";v="99", "Chromium";v="140", "Google Chrome";v="140"`,
			want:       "Chrome on macOS",
		},
		"platform hint fills reduced user agent": {
			userAgent:  "Mozilla/5.0 Chrome/140.0.0.0",
			clientHint: `"Chromium";v="140", "Google Chrome";v="140"`,
			platform:   `"Android"`,
			want:       "Chrome on Android",
		},
		"generic chromium hint does not hide a specific user agent brand": {
			userAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36 Vivaldi/7.5.3735.66",
			clientHint: `"Chromium";v="140"`,
			want:       "Vivaldi on Windows",
		},
		"unknown hint falls back to user agent": {
			userAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36",
			clientHint: `"New Browser";v="1"`,
			want:       "Chrome on Windows",
		},
		"malformed hint falls back to user agent": {
			userAgent:  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36",
			clientHint: `"Microsoft Edge"`,
			want:       "Chrome on Windows",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest("POST", "/auth/local/login", nil)
			request.Header.Set("User-Agent", test.userAgent)
			request.Header.Set("Sec-CH-UA", test.clientHint)
			request.Header.Set("Sec-CH-UA-Platform", test.platform)
			if got := browserClientLabelFromRequest(request); got != test.want {
				t.Fatalf("browserClientLabelFromRequest() = %q, want %q", got, test.want)
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
