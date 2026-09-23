// devauthsmoke exercises a real local browser session and a reversible group
// role mutation against the worktree-local development target.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/platform/cliapi"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

var csrfPattern = regexp.MustCompile(`name="csrf-token" content="([^"]+)"`)

type accessState struct {
	PolicyRevision  int64  `json:"policyRevision"`
	RedirectTo      string `json:"redirectTo"`
	Error           string `json:"error"`
	RoleAssignments []struct {
		BindingID   string `json:"bindingId"`
		SubjectType string `json:"subjectType"`
		SubjectID   string `json:"subjectId"`
		Status      string `json:"status"`
	} `json:"roleAssignments"`
}

func main() {
	base := flag.String("url", "http://localhost:8169", "local development target")
	credentialsPath := flag.String("credentials", ".tmp/dev-auth/credentials.json", "private development login")
	flag.Parse()
	if err := run(context.Background(), *base, *credentialsPath); err != nil {
		fmt.Fprintln(os.Stderr, "development auth smoke:", err)
		os.Exit(1)
	}
	fmt.Println("Credentialed login, Insights, group role grant, release activation, revocation, and cleanup passed")
}

func run(ctx context.Context, base, credentialsPath string) error {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		return errors.New("smoke target must be loopback HTTP")
	}
	encoded, err := securefs.ReadPrivateFile(credentialsPath)
	if err != nil {
		return err
	}
	var credentials accesspostgres.DevelopmentCredentials
	if err := json.Unmarshal(encoded, &credentials); err != nil || credentials.Email == "" || credentials.Password == "" {
		return errors.New("development login bundle is invalid")
	}
	config, err := config.Load()
	if err != nil {
		return err
	}
	if config.Production || config.DevAuthBypass {
		return errors.New("smoke requires credentialed non-production mode")
	}
	authority, err := cliapi.NewProfileStore(config.ClientConfigPath()).ResolveProjectAuthority("", func(value string) error {
		_, err := projectgraph.NewResourceID(value)
		return err
	})
	if err != nil {
		return err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	readAccessState := func() (accessState, error) {
		var state accessState
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/updates?route=admin&section=groups", nil)
		if err != nil {
			return state, err
		}
		req.Header.Set("Accept", "text/event-stream")
		response, err := client.Do(req)
		if err != nil {
			return state, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return state, fmt.Errorf("admin stream status %d", response.StatusCode)
		}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 64*1024), 2<<20)
		var event strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if line != "" {
				event.WriteString(line)
				event.WriteByte('\n')
				continue
			}
			patches, err := ssetest.DecodePatchSignals(event.String())
			event.Reset()
			if err != nil {
				return state, err
			}
			for _, patch := range patches {
				if patch["adminAccess"] == nil {
					continue
				}
				encoded, err := json.Marshal(patch["adminAccess"])
				if err != nil {
					return state, err
				}
				return state, json.Unmarshal(encoded, &state)
			}
		}
		return state, fmt.Errorf("admin stream did not return access state: %v", scanner.Err())
	}
	request := func(method, path, contentType, operation, key string, body io.Reader, csrf string) ([]byte, int, http.Header, error) {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
		if err != nil {
			return nil, 0, nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if operation != "" {
			req.Header.Set("X-LeapView-Operation-ID", operation)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, 0, nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		return data, response.StatusCode, response.Header, err
	}
	_, status, headers, err := request(http.MethodGet, "/admin/access", "", "", "", nil, "")
	if err != nil || status != http.StatusFound || headers.Get("Location") != "/login" {
		return fmt.Errorf("unauthenticated admin status %d location %q: %v", status, headers.Get("Location"), err)
	}
	loginPage, status, _, err := request(http.MethodGet, "/login", "", "", "", nil, "")
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("login page status %d: %v", status, err)
	}
	match := csrfPattern.FindSubmatch(loginPage)
	if len(match) != 2 {
		return errors.New("login page has no CSRF token")
	}
	form := url.Values{"email": {credentials.Email}, "password": {credentials.Password}, "gorilla.csrf.Token": {string(match[1])}}
	_, status, headers, err = request(http.MethodPost, "/auth/local/login", "application/x-www-form-urlencoded", "", "", strings.NewReader(form.Encode()), "")
	if err != nil || status != http.StatusFound || headers.Get("Location") != "/" && headers.Get("Location") != "/admin/access" {
		return fmt.Errorf("local login status %d location %q: %v", status, headers.Get("Location"), err)
	}
	_, status, _, err = request(http.MethodGet, "/", "", "", "", nil, "")
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("authenticated Insights status %d: %v", status, err)
	}
	adminPage, status, _, err := request(http.MethodGet, "/admin/access", "", "", "", nil, "")
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("authenticated admin status %d: %v", status, err)
	}
	match = csrfPattern.FindSubmatch(adminPage)
	if len(match) != 2 {
		return errors.New("admin page has no CSRF token")
	}
	csrf := string(match[1])
	groupName := "Dev auth smoke " + uuid.NewString()
	command := func(operation string, fields map[string]any, key string) (accessState, error) {
		var state accessState
		payload, err := json.Marshal(map[string]any{"adminAccessCommand": fields})
		if err != nil {
			return state, err
		}
		data, code, _, err := request(http.MethodPost, "/admin/access/command?section=groups", "application/json", operation, key, bytes.NewReader(payload), csrf)
		if err != nil || code != http.StatusOK {
			return state, fmt.Errorf("%s status %d: %v: %s", operation, code, err, data)
		}
		patches, err := ssetest.DecodePatchSignals(string(data))
		if err != nil || len(patches) != 1 {
			return state, fmt.Errorf("%s did not return one state patch: %v", operation, err)
		}
		encoded, err := json.Marshal(patches[0]["adminAccess"])
		if err != nil {
			return state, err
		}
		if err := json.Unmarshal(encoded, &state); err != nil {
			return state, err
		}
		if state.Error != "" {
			return state, fmt.Errorf("%s: %s", operation, state.Error)
		}
		return state, nil
	}
	created, err := command("createGroup", map[string]any{"action": "create_group", "displayName": groupName, "projectId": authority.ProjectUID}, "")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(created.RedirectTo, "/admin/groups/") {
		return errors.New("group creation returned no detail redirect")
	}
	groupID := strings.TrimPrefix(created.RedirectTo, "/admin/groups/")
	cleaned := false
	bindingID := ""
	policyRevision := created.PolicyRevision
	defer func() {
		if !cleaned {
			if bindingID != "" {
				_, _ = command("deleteProjectRoleBinding", map[string]any{"action": "revoke_role", "bindingId": bindingID, "expectedRevision": policyRevision}, uuid.NewString())
			}
			_, _ = command("deleteGroup", map[string]any{"action": "delete_group", "groupId": groupID}, "")
		}
	}()
	key := uuid.NewString()
	bindingID = "role-binding-" + key
	policyRevision = created.PolicyRevision + 1
	granted, err := command("createProjectRoleBinding", map[string]any{"action": "grant_role", "subjectType": "group", "subjectId": groupID, "role": "viewer", "expectedRevision": created.PolicyRevision}, key)
	if err != nil {
		return err
	}
	policyRevision = granted.PolicyRevision
	observedBinding := false
	for _, item := range granted.RoleAssignments {
		if item.SubjectID == groupID && item.SubjectType == "group" && item.BindingID == bindingID {
			observedBinding = true
			break
		}
	}
	if !observedBinding {
		return errors.New("group role grant was not persisted")
	}
	active := false
	for _, assignment := range granted.RoleAssignments {
		if assignment.BindingID == bindingID && assignment.Status == "Active" {
			active = true
		}
	}
	if !active {
		publish := exec.CommandContext(ctx, "task", "dev:publish")
		publish.Env = append(os.Environ(), "LEAPVIEW_DEV_CANDIDATE_KEY=auth-smoke-"+uuid.NewString())
		if _, err := publish.CombinedOutput(); err != nil {
			return fmt.Errorf("publish for role activation failed: %w; run task dev:publish for details", err)
		}
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			state, err := readAccessState()
			if err != nil {
				return err
			}
			for _, assignment := range state.RoleAssignments {
				if assignment.BindingID == bindingID && assignment.Status == "Active" {
					active = true
				}
			}
			if active {
				break
			}
			time.Sleep(time.Second)
		}
	}
	if !active {
		return errors.New("group role did not activate after the authorized release; the candidate may have reused its prior policy snapshot")
	}
	revoked, err := command("deleteProjectRoleBinding", map[string]any{"action": "revoke_role", "bindingId": bindingID, "expectedRevision": granted.PolicyRevision}, uuid.NewString())
	if err != nil {
		return err
	}
	for _, item := range revoked.RoleAssignments {
		if item.BindingID == bindingID {
			return errors.New("group role revocation was not persisted")
		}
	}
	bindingID = ""
	if _, err := command("deleteGroup", map[string]any{"action": "delete_group", "groupId": groupID}, ""); err != nil {
		return fmt.Errorf("smoke group cleanup: %w", err)
	}
	cleaned = true
	return nil
}
