package composectl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualificationHistoricalBrowser struct {
	client *http.Client
}

func newQualificationHistoricalBrowser(t *testing.T, proxyURL, caPath string) *qualificationHistoricalBrowser {
	t.Helper()
	proxy, err := url.Parse(proxyURL)
	require.NoError(t, err)
	caContents, err := os.ReadFile(caPath)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caContents))
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &qualificationHistoricalBrowser{client: &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		Jar:       jar, Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (browser *qualificationHistoricalBrowser) login(ctx context.Context, email, password string) error {
	loginURL := qualificationHistoricalOrigin + "/login"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	response, err := browser.client.Do(request)
	if err != nil {
		return fmt.Errorf("open predecessor login: %w", err)
	}
	page, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		return fmt.Errorf("predecessor login form returned HTTP %d", response.StatusCode)
	}
	token, err := qualificationHistoricalCSRFToken(page)
	if err != nil {
		return err
	}
	login := url.Values{"email": {email}, "password": {password}, "gorilla.csrf.Token": {token}}
	response, err = browser.post(ctx, qualificationHistoricalOrigin+"/auth/local/login", login)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/login" {
		return fmt.Errorf("predecessor first-login response was HTTP %d to %q, want redirect to /login for required password change", response.StatusCode, response.Header.Get("Location"))
	}
	change := url.Values{
		"currentPassword": {password}, "newPassword": {qualificationHistoricalBrowserPassword},
		"gorilla.csrf.Token": {token},
	}
	response, err = browser.post(ctx, qualificationHistoricalOrigin+"/auth/local/password", change)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/login" {
		return fmt.Errorf("predecessor password activation response was HTTP %d to %q, want redirect to /login after session expiry", response.StatusCode, response.Header.Get("Location"))
	}
	return browser.loginExisting(ctx, email, qualificationHistoricalBrowserPassword)
}

// loginExisting authenticates a viewer that already completed the required
// password change. It is used after a same-image server replacement so the
// subsequent deployment check exercises a fresh browser session.
func (browser *qualificationHistoricalBrowser) loginExisting(ctx context.Context, email, password string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, qualificationHistoricalOrigin+"/login", nil)
	if err != nil {
		return err
	}
	response, err := browser.client.Do(request)
	if err != nil {
		return fmt.Errorf("open candidate login: %w", err)
	}
	page, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		return fmt.Errorf("candidate login form returned HTTP %d", response.StatusCode)
	}
	token, err := qualificationHistoricalCSRFToken(page)
	if err != nil {
		return err
	}
	form := url.Values{"email": {email}, "password": {password}, "gorilla.csrf.Token": {token}}
	response, err = browser.post(ctx, qualificationHistoricalOrigin+"/auth/local/login", form)
	if err != nil {
		return err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/" {
		return fmt.Errorf("candidate viewer login response was HTTP %d to %q, want successful redirect to /", response.StatusCode, response.Header.Get("Location"))
	}
	return nil
}

func (browser *qualificationHistoricalBrowser) checkDashboard(ctx context.Context, address string) error {
	response, err := browser.get(ctx, address)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("viewer dashboard access returned HTTP %d to %q", response.StatusCode, response.Header.Get("Location"))
	}
	return nil
}

func qualificationHistoricalDashboardOverviewURL(dashboardID string) string {
	return qualificationHistoricalOrigin + "/dashboards/" + url.PathEscape(dashboardID) + "/pages/overview"
}

func (browser *qualificationHistoricalBrowser) get(ctx context.Context, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	return browser.client.Do(request)
}

func (browser *qualificationHistoricalBrowser) post(ctx context.Context, address string, form url.Values) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", qualificationHistoricalOrigin)
	request.Header.Set("Referer", qualificationHistoricalOrigin+"/login")
	request.Header.Set("X-CSRF-Token", form.Get("gorilla.csrf.Token"))
	return browser.client.Do(request)
}

func qualificationHistoricalCSRFToken(page []byte) (string, error) {
	for _, marker := range []string{
		`name="gorilla.csrf.Token" value="`, `name="gorilla.csrf.Token" value='`,
		`name="csrf-token" content="`, `name="csrf-token" content='`,
	} {
		if _, remaining, ok := strings.Cut(string(page), marker); ok {
			quote := marker[len(marker)-1]
			value, _, found := strings.Cut(remaining, string(quote))
			if found && value != "" {
				return html.UnescapeString(value), nil
			}
		}
	}
	return "", errors.New("predecessor login form omitted its CSRF token")
}
