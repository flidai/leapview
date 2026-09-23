package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type fakeOriginResolver struct {
	calls int
}

type originResolverFunc func(context.Context, string, string) (accesscli.ResolvedCredential, error)

func (resolve originResolverFunc) ResolveOrigin(ctx context.Context, origin, token string) (accesscli.ResolvedCredential, error) {
	return resolve(ctx, origin, token)
}

func (resolver *fakeOriginResolver) ResolveOrigin(_ context.Context, origin, token string) (accesscli.ResolvedCredential, error) {
	resolver.calls++
	return accesscli.ResolvedCredential{AccessToken: "lv_cli_access_refreshed"}, nil
}

func TestAuthoringRetryTransportReusesRotatedTokenForLaterRequests(t *testing.T) {
	resolver := &fakeOriginResolver{}
	var sent []string
	transport := &authoringRetryTransport{
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
			sent = append(sent, token)
			status := http.StatusUnauthorized
			if token == "lv_cli_access_refreshed" {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response")), Request: request}, nil
		}), credentials: resolver,
	}
	for range 2 {
		request, err := http.NewRequest(http.MethodGet, "https://prod.example.com/api/v1/projects/analytics", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer lv_cli_access_expired")
		response, err := transport.RoundTrip(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
	require.Equal(t, []string{"lv_cli_access_expired", "lv_cli_access_refreshed", "lv_cli_access_refreshed"}, sent)
	require.Equal(t, 1, resolver.calls)
}

type rotatedProfileResolver struct {
	profile      cliapi.TargetProfile
	currentToken string
	byOrigin     int
	byName       int
}

func (resolver *rotatedProfileResolver) ResolveOrigin(context.Context, string, string) (accesscli.ResolvedCredential, error) {
	resolver.byOrigin++
	return accesscli.ResolvedCredential{}, cliapi.ErrProfileNotFound
}

func (resolver *rotatedProfileResolver) ResolveName(_ context.Context, name string) (accesscli.ResolvedCredential, error) {
	resolver.byName++
	if name != "local" {
		return accesscli.ResolvedCredential{}, cliapi.ErrProfileNotFound
	}
	return accesscli.ResolvedCredential{Profile: resolver.profile, AccessToken: resolver.currentToken}, nil
}

func TestAuthoringRetryTransportSurvivesCredentialRotationInAnotherProcess(t *testing.T) {
	const origin = "http://127.0.0.1:7090"
	const stale = "lv_cli_access_original"
	const current = "lv_cli_access_rotated_elsewhere"
	profile := cliapi.TargetProfile{Origin: origin, InstanceID: "instance-1", ProjectID: "project-1", CredentialAccount: "account-1"}
	resolver := &rotatedProfileResolver{profile: profile, currentToken: current}
	transport := &authoringRetryTransport{
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			status := http.StatusUnauthorized
			if request.Header.Get("Authorization") == "Bearer "+current {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response")), Request: request}, nil
		}), credentials: resolver,
	}
	transport.bindCredential(origin, stale, "local", profile)
	for range 2 {
		request, err := http.NewRequest(http.MethodGet, origin+"/api/v1/projects/project-1", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+stale)
		response, err := transport.RoundTrip(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.NoError(t, response.Body.Close())
	}
	require.Equal(t, 0, resolver.byOrigin)
	require.Equal(t, 1, resolver.byName)

	resolver.profile.ProjectID = "different-project"
	changed := &authoringRetryTransport{
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unauthorized")), Request: request}, nil
		}), credentials: resolver,
	}
	changed.bindCredential(origin, stale, "local", profile)
	request, err := http.NewRequest(http.MethodGet, origin+"/api/v1/projects/project-1", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+stale)
	response, err := changed.RoundTrip(request)
	require.ErrorContains(t, err, "target profile changed")
	require.Nil(t, response)
}

func TestAuthoringRetryTransportConcurrentExpiredRequestsRotateOnce(t *testing.T) {
	var refreshes atomic.Int32
	transport := &authoringRetryTransport{
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			status := http.StatusUnauthorized
			if request.Header.Get("Authorization") == "Bearer lv_cli_access_refreshed" {
				status = http.StatusOK
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response")), Request: request}, nil
		}),
		credentials: originResolverFunc(func(_ context.Context, _, token string) (accesscli.ResolvedCredential, error) {
			refreshes.Add(1)
			if token != "lv_cli_access_expired" {
				return accesscli.ResolvedCredential{}, cliapi.ErrProfileNotFound
			}
			return accesscli.ResolvedCredential{AccessToken: "lv_cli_access_refreshed"}, nil
		}),
	}
	var group sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:7090/api/v1/projects/analytics", nil)
			if err != nil {
				errors <- err
				return
			}
			request.Header.Set("Authorization", "Bearer lv_cli_access_expired")
			response, err := transport.RoundTrip(request)
			if err != nil {
				errors <- err
				return
			}
			if response.StatusCode != http.StatusOK {
				errors <- fmt.Errorf("response status %d", response.StatusCode)
			}
			_ = response.Body.Close()
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), refreshes.Load())
}

func TestAuthoringRetryTransportRefreshesOnceAfterMidSyncExpiry(t *testing.T) {
	requests := 0
	base := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		status := http.StatusUnauthorized
		if token == "lv_cli_access_refreshed" {
			status = http.StatusOK
		}
		return &http.Response{
			StatusCode: status, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(http.StatusText(status))), Request: request,
		}, nil
	})
	resolver := &fakeOriginResolver{}
	transport := authoringRetryTransport{base: base, credentials: resolver}
	request, err := http.NewRequest(http.MethodPost, "https://prod.example.com/api/v1/projects/analytics/releases", strings.NewReader(`{}`))
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer lv_cli_access_expired")
	response, err := transport.RoundTrip(request)
	require.NoError(t, err)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || requests != 2 || resolver.calls != 1 {
		t.Fatalf("status=%d requests=%d refreshes=%d", response.StatusCode, requests, resolver.calls)
	}
}

func TestAuthoringRetryTransportNeverSubstitutesForPATOrWorkloadCredential(t *testing.T) {
	for _, token := range []string{"legacy-pat", "lv_workload_access_ephemeral"} {
		t.Run(token, func(t *testing.T) {
			resolver := &fakeOriginResolver{}
			transport := authoringRetryTransport{
				base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusUnauthorized, Header: make(http.Header),
						Body: io.NopCloser(strings.NewReader("unauthorized")), Request: request,
					}, nil
				}),
				credentials: resolver,
			}
			request, _ := http.NewRequest(http.MethodGet, "https://prod.example.com/api/v1/instance", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response, err := transport.RoundTrip(request)
			require.NoError(t, err)
			response.Body.Close()
			if resolver.calls != 0 {
				t.Fatalf("resolver calls = %d", resolver.calls)
			}
		})
	}
}
