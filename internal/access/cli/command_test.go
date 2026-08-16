package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeAuthService struct {
	loginRequest LoginRequest
	logoutName   string
	challenge    DeviceChallenge
}

func (service *fakeAuthService) Login(_ context.Context, request LoginRequest, notify func(DeviceChallenge)) (LoginResult, error) {
	service.loginRequest = request
	if notify != nil {
		notify(service.challenge)
	}
	return LoginResult{SessionID: "session-1"}, nil
}

func (service *fakeAuthService) Logout(_ context.Context, name string) error {
	service.logoutName = name
	return nil
}

type fakeDiscovery struct{}

func (fakeDiscovery) Discover(_ context.Context, origin string) (TargetMetadata, error) {
	return TargetMetadata{Origin: strings.TrimRight(origin, "/"), InstanceID: "lvinst_prod", Environment: "production"}, nil
}

type fakeProjectResolver struct{}

func (fakeProjectResolver) ProjectID(path string) (string, error) {
	if path != "dashboards/leapview.yaml" {
		return "", nil
	}
	return "analytics", nil
}

func TestLoginCommandDiscoversTargetAndProject(t *testing.T) {
	service := &fakeAuthService{challenge: DeviceChallenge{
		UserCode: "ABCD-EFGH", VerificationURI: "https://prod.example.com/device",
	}}
	command := LoginCommand(context.Background(), service, fakeDiscovery{}, fakeProjectResolver{})
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"https://prod.example.com/", "--no-browser"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	request := service.loginRequest
	if request.Name != "https://prod.example.com" || request.Origin != "https://prod.example.com" ||
		request.InstanceID != "lvinst_prod" || request.ProjectID != "analytics" || !request.Headless {
		t.Fatalf("login request = %+v", request)
	}
	if strings.Join(request.Capabilities, ",") != "RESOURCE_USE,RESOURCE_READ,RESOURCE_EDIT,RESOURCE_PUBLISH" {
		t.Fatalf("capabilities = %v", request.Capabilities)
	}
	if !strings.Contains(output.String(), "ABCD-EFGH") || !strings.Contains(output.String(), "session-1") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestLoginCommandSupportsStableProfileAlias(t *testing.T) {
	service := &fakeAuthService{}
	command := LoginCommand(context.Background(), service, fakeDiscovery{}, fakeProjectResolver{})
	command.SetArgs([]string{"https://prod.example.com", "--name", "prod"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.loginRequest.Name != "prod" {
		t.Fatalf("profile name = %q", service.loginRequest.Name)
	}
}

func TestLoginCommandEmitsVersionedJSONEvents(t *testing.T) {
	service := &fakeAuthService{challenge: DeviceChallenge{
		UserCode: "ABCD-EFGH", VerificationURI: "https://prod.example.com/device",
	}}
	command := LoginCommand(context.Background(), service, fakeDiscovery{}, fakeProjectResolver{})
	var output strings.Builder
	command.SetOut(&output)
	command.SetArgs([]string{"https://prod.example.com", "--no-browser", "--format", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(output.String()))
	var challenge, authenticated struct {
		SchemaVersion int    `json:"schemaVersion"`
		Type          string `json:"type"`
		UserCode      string `json:"userCode"`
		SessionID     string `json:"sessionId"`
	}
	if err := decoder.Decode(&challenge); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&authenticated); err != nil {
		t.Fatal(err)
	}
	if challenge.SchemaVersion != 1 || challenge.Type != "deviceChallenge" ||
		challenge.UserCode != "ABCD-EFGH" ||
		authenticated.SchemaVersion != 1 || authenticated.Type != "authenticated" ||
		authenticated.SessionID != "session-1" {
		t.Fatalf("events = %#v, %#v", challenge, authenticated)
	}
}

func TestLogoutCommandRevokesNamedTarget(t *testing.T) {
	service := &fakeAuthService{}
	command := LogoutCommand(context.Background(), service)
	command.SetArgs([]string{"prod"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if service.logoutName != "prod" {
		t.Fatalf("logout target = %q", service.logoutName)
	}
}
