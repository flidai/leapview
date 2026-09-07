package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/cliapi"
)

func TestBootstrapProjectPersistsAuthorityBeforeTargetRequests(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if _, err := os.Stat(configPath); err != nil {
			t.Errorf("target request %d arrived before issuer state was saved: %v", requestCount, err)
		}
		if r.URL.Path == "/api/v1/instance" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"lvinst_test","canonicalOrigin":"http://` + r.Host + `","environment":"production"}`))
			return
		}
		if r.URL.Path != "/api/v1/instance/project-claim" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projectUid":"project:issued","environment":"production","claimedBy":"admin","claimedAt":"2026-09-06T00:00:00Z"}`))
	}))
	defer server.Close()

	var output strings.Builder
	command := bootstrapProjectCommand(context.Background(), &rootOptions{token: "instance-admin"})
	command.SetOut(&output)
	command.SetArgs([]string{server.URL, "--token", "instance-admin", "--project-uid", "project:issued", "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var event struct {
		SchemaVersion int    `json:"schemaVersion"`
		Type          string `json:"type"`
		Target        string `json:"target"`
		ProjectUID    string `json:"projectUid"`
		Environment   string `json:"environment"`
	}
	if err := json.Unmarshal([]byte(output.String()), &event); err != nil {
		t.Fatal(err)
	}
	if event.SchemaVersion != 1 || event.Type != "projectBootstrapped" || event.Target != server.URL || event.ProjectUID != "project:issued" || event.Environment != "production" {
		t.Fatalf("bootstrap event = %#v", event)
	}
	authority, err := cliapi.NewProfileStore(configPath).ResolveProjectAuthority("project:issued", validateProjectAuthorityResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if authority.ProjectUID != event.ProjectUID {
		t.Fatalf("authority ProjectUID = %q, event = %q", authority.ProjectUID, event.ProjectUID)
	}
}

func TestBootstrapProjectRejectsMismatchedTargetResponse(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/instance" {
			_, _ = w.Write([]byte(`{"id":"lvinst_test","canonicalOrigin":"http://` + r.Host + `","environment":"production"}`))
			return
		}
		_, _ = w.Write([]byte(`{"projectUid":"project:replacement","environment":"production","claimedBy":"admin","claimedAt":"2026-09-06T00:00:00Z"}`))
	}))
	defer server.Close()

	command := bootstrapProjectCommand(context.Background(), &rootOptions{token: "instance-admin"})
	command.SetArgs([]string{server.URL, "--token", "instance-admin", "--project-uid", "project:issued"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "returned ProjectUID") {
		t.Fatalf("bootstrap error = %v, want mismatched ProjectUID rejection", err)
	}
	authority, err := cliapi.NewProfileStore(configPath).ResolveProjectAuthority("project:issued", validateProjectAuthorityResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if authority.ProjectUID != "project:issued" {
		t.Fatalf("durable authority changed to %q", authority.ProjectUID)
	}
}

func TestBootstrapProjectReusesAuthorityAcrossTargetsAndRejectsReplacementBeforeHTTP(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "cli.json")
	t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
	type claim struct {
		ProjectUID  string `json:"projectUid"`
		IssuerID    string `json:"issuerId"`
		Environment string `json:"environment"`
	}
	newTarget := func(environment string) (*httptest.Server, *claim) {
		var received claim
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/instance" {
				_, _ = w.Write([]byte(`{"id":"lvinst_` + environment + `","canonicalOrigin":"http://` + r.Host + `","environment":"` + environment + `"}`))
				return
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"projectUid":"` + received.ProjectUID + `","environment":"` + received.Environment + `","claimedBy":"admin","claimedAt":"2026-09-06T00:00:00Z"}`))
		}))
		return server, &received
	}
	development, developmentClaim := newTarget("development")
	defer development.Close()
	production, productionClaim := newTarget("production")
	defer production.Close()

	for _, target := range []string{development.URL, production.URL} {
		command := bootstrapProjectCommand(context.Background(), &rootOptions{})
		command.SetArgs([]string{target, "--token", "instance-admin"})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	if developmentClaim.ProjectUID == "" || developmentClaim.ProjectUID != productionClaim.ProjectUID || developmentClaim.IssuerID == "" || developmentClaim.IssuerID != productionClaim.IssuerID || developmentClaim.Environment != "development" || productionClaim.Environment != "production" {
		t.Fatalf("claims = %#v, %#v", *developmentClaim, *productionClaim)
	}

	var replacementCalls int
	replacement := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { replacementCalls++ }))
	defer replacement.Close()
	command := bootstrapProjectCommand(context.Background(), &rootOptions{})
	command.SetArgs([]string{replacement.URL, "--token", "instance-admin", "--project-uid", "project:replacement"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "conflicts with durable state") {
		t.Fatalf("replacement error = %v", err)
	}
	if replacementCalls != 0 {
		t.Fatalf("replacement target received %d HTTP calls", replacementCalls)
	}
}

func TestBootstrapProjectRejectsMalformedIdentityResponses(t *testing.T) {
	tests := []struct {
		name, instanceID, instanceEnvironment, projectUID, responseEnvironment, claimedBy, claimedAt, want string
	}{
		{name: "environment mismatch", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "development", claimedBy: "admin", claimedAt: "2026-09-06T00:00:00Z", want: "returned environment"},
		{name: "incomplete instance id", instanceID: "", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "admin", claimedAt: "2026-09-06T00:00:00Z", want: "incomplete bootstrap identity"},
		{name: "incomplete instance environment", instanceID: "lvinst_test", instanceEnvironment: "", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "admin", claimedAt: "2026-09-06T00:00:00Z", want: "incomplete bootstrap identity"},
		{name: "blank claimed by", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "", claimedAt: "2026-09-06T00:00:00Z", want: "non-canonical claimedBy"},
		{name: "noncanonical claimed by", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: " admin ", claimedAt: "2026-09-06T00:00:00Z", want: "non-canonical claimedBy"},
		{name: "invalid claimed at", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "admin", claimedAt: "not-a-time", want: "invalid claimedAt"},
		{name: "zero claimed at", instanceID: "lvinst_test", instanceEnvironment: "production", projectUID: "project:issued", responseEnvironment: "production", claimedBy: "admin", claimedAt: "0001-01-01T00:00:00Z", want: "invalid claimedAt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "cli.json")
			t.Setenv("LEAPVIEW_CLI_CONFIG", configPath)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/instance" {
					_, _ = w.Write([]byte(`{"id":"` + test.instanceID + `","canonicalOrigin":"http://` + r.Host + `","environment":"` + test.instanceEnvironment + `"}`))
					return
				}
				_, _ = w.Write([]byte(`{"projectUid":"` + test.projectUID + `","environment":"` + test.responseEnvironment + `","claimedBy":"` + test.claimedBy + `","claimedAt":"` + test.claimedAt + `"}`))
			}))
			defer server.Close()

			command := bootstrapProjectCommand(context.Background(), &rootOptions{})
			command.SetArgs([]string{server.URL, "--token", "instance-admin", "--project-uid", "project:issued"})
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("bootstrap error = %v, want %q", err, test.want)
			}
		})
	}
}
