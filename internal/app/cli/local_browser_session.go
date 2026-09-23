package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

const (
	localBrowserCredentialsVersion = 1
	localBrowserCredentialsName    = "local-browser-credentials.json"
	localBrowserSessionCookie      = "lv_session"
	localBrowserHandoffLifetime    = 2 * time.Minute
)

var (
	errInvalidLocalBrowserCredentials = errors.New("invalid local browser credentials")
	csrfMetaPattern                   = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)
	csrfInputPattern                  = regexp.MustCompile(`name="gorilla\.csrf\.Token" value="([^"]+)"`)
)

type localBrowserCredentials struct {
	Version         int    `json:"version"`
	Email           string `json:"email"`
	CurrentPassword string `json:"currentPassword"`
	PendingPassword string `json:"pendingPassword,omitempty"`
}

// establishLocalBrowserSession performs the production local-auth steps on
// behalf of the checkout owner. It uses only the loopback runtime and the
// controller-owned mode-0600 bootstrap material; remote targets never enter
// this path.
func establishLocalBrowserSession(ctx context.Context, request localruntime.SessionRequest, base *http.Client) (*http.Client, *http.Cookie, error) {
	origin, err := localSessionOrigin(request.Origin)
	if err != nil {
		return nil, nil, err
	}
	credentialsPath := strings.TrimSpace(request.CredentialsPath)
	if credentialsPath == "" {
		return nil, nil, errors.New("local initialization credentials path is required")
	}
	statePath := filepath.Join(filepath.Dir(credentialsPath), localBrowserCredentialsName)
	credentials, err := loadOrPrepareLocalBrowserCredentials(credentialsPath, statePath)
	if err != nil {
		return nil, nil, err
	}
	client, err := localSessionHTTPClient(base)
	if err != nil {
		return nil, nil, err
	}

	mustChange, loginErr := localBrowserLogin(ctx, client, origin, credentials.Email, credentials.CurrentPassword)
	if errors.Is(loginErr, errInvalidLocalBrowserCredentials) && credentials.PendingPassword != "" {
		mustChange, loginErr = localBrowserLogin(ctx, client, origin, credentials.Email, credentials.PendingPassword)
		if loginErr == nil {
			credentials.CurrentPassword = credentials.PendingPassword
			credentials.PendingPassword = ""
			if err := saveLocalBrowserCredentials(statePath, credentials); err != nil {
				return nil, nil, fmt.Errorf("commit recovered local browser credential: %w", err)
			}
		}
	}
	if loginErr != nil {
		return nil, nil, fmt.Errorf("sign in local browser session: %w", loginErr)
	}
	if mustChange {
		if credentials.PendingPassword == "" {
			credentials.PendingPassword, err = randomLocalBrowserPassword()
			if err != nil {
				return nil, nil, err
			}
			if err := saveLocalBrowserCredentials(statePath, credentials); err != nil {
				return nil, nil, fmt.Errorf("prepare resumable local password rotation: %w", err)
			}
		}
		if err := changeLocalBrowserPassword(ctx, client, origin, credentials.CurrentPassword, credentials.PendingPassword); err != nil {
			return nil, nil, err
		}
		if _, err := localBrowserLogin(ctx, client, origin, credentials.Email, credentials.PendingPassword); err != nil {
			return nil, nil, fmt.Errorf("verify rotated local browser credential: %w", err)
		}
		credentials.CurrentPassword = credentials.PendingPassword
		credentials.PendingPassword = ""
		if err := saveLocalBrowserCredentials(statePath, credentials); err != nil {
			return nil, nil, fmt.Errorf("commit local browser credential rotation: %w", err)
		}
	}
	cookie := browserSessionCookie(client, origin)
	if cookie == nil {
		return nil, nil, errors.New("local sign-in did not establish a browser session")
	}
	return client, cookie, nil
}

func localSessionOrigin(raw string) (*url.URL, error) {
	origin, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" || origin.Port() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("local session origin must be an explicit http://127.0.0.1:<port> endpoint")
	}
	return origin, nil
}

func localSessionHTTPClient(base *http.Client) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create local session cookie jar: %w", err)
	}
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, nil
}

func loadOrPrepareLocalBrowserCredentials(initialPath, statePath string) (localBrowserCredentials, error) {
	if encoded, err := securefs.ReadPrivateFile(statePath); err == nil {
		var credentials localBrowserCredentials
		if json.Unmarshal(encoded, &credentials) != nil || credentials.Version != localBrowserCredentialsVersion || credentials.Email == "" || credentials.CurrentPassword == "" {
			return localBrowserCredentials{}, errors.New("retained local browser credentials are invalid")
		}
		return credentials, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return localBrowserCredentials{}, fmt.Errorf("read retained local browser credentials: %w", err)
	}
	encoded, err := securefs.ReadPrivateFile(initialPath)
	if err != nil {
		return localBrowserCredentials{}, fmt.Errorf("read local initialization credentials: %w", err)
	}
	initial, err := adminoffline.DecodeInitialCredentials(encoded)
	if err != nil {
		return localBrowserCredentials{}, err
	}
	pending, err := randomLocalBrowserPassword()
	if err != nil {
		return localBrowserCredentials{}, err
	}
	credentials := localBrowserCredentials{
		Version: localBrowserCredentialsVersion, Email: initial.Email,
		CurrentPassword: initial.TemporaryPassword, PendingPassword: pending,
	}
	if err := saveLocalBrowserCredentials(statePath, credentials); err != nil {
		return localBrowserCredentials{}, fmt.Errorf("prepare local browser credentials: %w", err)
	}
	return credentials, nil
}

func saveLocalBrowserCredentials(path string, credentials localBrowserCredentials) error {
	encoded, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	return securefs.WritePrivateFileAtomic(path, append(encoded, '\n'))
}

func randomLocalBrowserPassword() (string, error) {
	var entropy [32]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		return "", fmt.Errorf("generate local browser credential: %w", err)
	}
	return "lv-local-" + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}

func localBrowserLogin(ctx context.Context, client *http.Client, origin *url.URL, email, password string) (bool, error) {
	token, err := localCSRFToken(ctx, client, origin, "/login")
	if err != nil {
		return false, err
	}
	return localBrowserLoginEmail(ctx, client, origin, email, password, token)
}

func localBrowserLoginEmail(ctx context.Context, client *http.Client, origin *url.URL, email, password, token string) (bool, error) {
	response, err := postLocalForm(ctx, client, origin, "/auth/local/login", url.Values{
		"email": {email}, "password": {password}, "gorilla.csrf.Token": {token},
	})
	if err != nil {
		return false, err
	}
	defer discardResponse(response)
	location := response.Header.Get("Location")
	if response.StatusCode == http.StatusSeeOther && location == "/login?error=invalid_credentials" {
		return false, errInvalidLocalBrowserCredentials
	}
	if response.StatusCode != http.StatusFound {
		return false, fmt.Errorf("local login returned HTTP %d", response.StatusCode)
	}
	switch location {
	case "/login":
		return true, nil
	case "/":
		return false, nil
	default:
		return false, fmt.Errorf("local login returned unexpected redirect %q", location)
	}
}

func changeLocalBrowserPassword(ctx context.Context, client *http.Client, origin *url.URL, current, next string) error {
	token, err := localCSRFToken(ctx, client, origin, "/login")
	if err != nil {
		return err
	}
	response, err := postLocalForm(ctx, client, origin, "/auth/local/password", url.Values{
		"currentPassword": {current}, "newPassword": {next}, "gorilla.csrf.Token": {token},
	})
	if err != nil {
		return err
	}
	defer discardResponse(response)
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/login" {
		return fmt.Errorf("rotate local browser credential: HTTP %d redirect %q", response.StatusCode, response.Header.Get("Location"))
	}
	return nil
}

func approveLocalDeviceAuthorization(ctx context.Context, client *http.Client, origin *url.URL, challenge accesscli.DeviceChallenge) error {
	token, err := localCSRFToken(ctx, client, origin, "/device?user_code="+url.QueryEscape(challenge.UserCode))
	if err != nil {
		return err
	}
	response, err := postLocalForm(ctx, client, origin, "/device", url.Values{
		"user_code": {challenge.UserCode}, "decision": {"approve"}, "gorilla.csrf.Token": {token},
	})
	if err != nil {
		return err
	}
	defer discardResponse(response)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("approve local device authorization: HTTP %d", response.StatusCode)
	}
	return nil
}

func localCSRFToken(ctx context.Context, client *http.Client, origin *url.URL, path string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+path, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("load local authentication form: %w", err)
	}
	defer discardResponse(response)
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("load local authentication form: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return "", err
	}
	match := csrfMetaPattern.FindSubmatch(body)
	if len(match) != 2 {
		match = csrfInputPattern.FindSubmatch(body)
	}
	if len(match) != 2 || strings.TrimSpace(string(match[1])) == "" {
		return "", errors.New("local authentication form did not provide a CSRF token")
	}
	return string(match[1]), nil
}

func postLocalForm(ctx context.Context, client *http.Client, origin *url.URL, path string, values url.Values) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin.String()+path, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return client.Do(request)
}

func discardResponse(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

func browserSessionCookie(client *http.Client, origin *url.URL) *http.Cookie {
	for _, cookie := range client.Jar.Cookies(origin) {
		if cookie.Name == localBrowserSessionCookie && cookie.Value != "" {
			copy := *cookie
			copy.Path = "/"
			copy.Domain = ""
			copy.HttpOnly = true
			copy.Secure = false
			copy.SameSite = http.SameSiteLaxMode
			return &copy
		}
	}
	return nil
}

func openLocalBrowserSession(ctx context.Context, origin string, cookie *http.Cookie, open func(string) error) error {
	target, err := localSessionOrigin(origin)
	if err != nil {
		return err
	}
	if cookie == nil || cookie.Name != localBrowserSessionCookie || cookie.Value == "" {
		return errors.New("local browser session cookie is required")
	}
	if open == nil {
		return errors.New("local browser opener is required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for local browser session handoff: %w", err)
	}
	secret, err := randomLocalBrowserPassword()
	if err != nil {
		_ = listener.Close()
		return err
	}
	path := "/session/" + strings.TrimPrefix(secret, "lv-local-")
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.SetCookie(w, cookie)
		http.Redirect(w, r, target.String(), http.StatusFound)
		go func() { _ = server.Shutdown(context.Background()) }()
	})
	go func() { _ = server.Serve(listener) }()
	go func() {
		timer := time.NewTimer(localBrowserHandoffLifetime)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		_ = server.Shutdown(context.Background())
	}()
	handoff := "http://" + listener.Addr().String() + path
	if err := open(handoff); err != nil {
		_ = server.Close()
		return fmt.Errorf("open local dashboard: %w", err)
	}
	return nil
}
