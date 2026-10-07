package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type trackedResponseBody struct {
	io.Reader
	closed bool
}

func (b *trackedResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestGetRecoversTransientServerErrors(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				const endpoint = "https://api.github.com/repos/owner/repo/actions/runs/123/attempts/2/jobs?per_page=100&page=1"
				var bodies []*trackedResponseBody
				api := client{token: "test-token", http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
					if r.Method != http.MethodGet || r.URL.String() != endpoint || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
						t.Errorf("retry changed the request: %s %s %v", r.Method, r.URL, r.Header)
					}
					if len(bodies) > 0 && !bodies[len(bodies)-1].closed {
						t.Error("previous response body remains open during retry")
					}
					code, payload := status, `{"message":"Server Error"}`
					if len(bodies) == 2 {
						code, payload = http.StatusOK, `{"jobs":[{"name":"CI gate","conclusion":"failure"}]}`
					}
					body := &trackedResponseBody{Reader: strings.NewReader(payload)}
					bodies = append(bodies, body)
					return &http.Response{StatusCode: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code)), Body: body}, nil
				})}}
				jobs, err := api.jobs(context.Background(), "owner/repo", 123, 2)
				if err != nil || !reflect.DeepEqual(jobs, []githubJob{{Name: "CI gate", Conclusion: "failure"}}) {
					t.Fatalf("jobs = %v, %v; want original job failure after API recovery", jobs, err)
				}
				if len(bodies) != 3 || !bodies[2].closed {
					t.Fatalf("requests = %d; successful response closed = %v", len(bodies), bodies[len(bodies)-1].closed)
				}
			})
		})
	}
}

func TestGetStopsAfterThreeTransientFailures(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var bodies []*trackedResponseBody
		api := client{http: &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
			body := &trackedResponseBody{Reader: strings.NewReader(fmt.Sprintf("failure %d", len(bodies)+1))}
			bodies = append(bodies, body)
			return &http.Response{StatusCode: 502, Status: "502 Bad Gateway", Body: body}, nil
		})}}
		data, err := api.get(context.Background(), "https://api.github.com/jobs")
		if len(data) != 0 || err == nil || !strings.Contains(err.Error(), "https://api.github.com/jobs returned 502 Bad Gateway: failure 3") {
			t.Fatalf("get = %q, %v; want final upstream failure", data, err)
		}
		if len(bodies) != 3 {
			t.Fatalf("requests = %d, want 3", len(bodies))
		}
		for _, body := range bodies {
			if !body.closed {
				t.Error("failed response body remains open")
			}
		}
	})
}

func TestGetDoesNotRetryPermanentOrInvalidResponses(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 422, 429, 501} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			calls := 0
			api := client{http: &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(strings.NewReader("invalid"))}, nil
			})}}
			var destination map[string]any
			if err := api.getJSON(context.Background(), "https://api.github.com/jobs", &destination); err == nil || calls != 1 {
				t.Fatalf("getJSON error = %v, requests = %d; want immediate failure", err, calls)
			}
		})
	}
	transportErr := errors.New("transport failure")
	calls := 0
	api := client{http: &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, transportErr
	})}}
	if _, err := api.get(context.Background(), "https://api.github.com/jobs"); !errors.Is(err, transportErr) || calls != 1 {
		t.Fatalf("get error = %v, requests = %d; want original transport failure", err, calls)
	}
}

func TestGetCancellationStopsBackoff(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	body := &trackedResponseBody{Reader: strings.NewReader("Server Error")}
	api := client{http: &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		calls++
		cancel()
		return &http.Response{StatusCode: 502, Status: "502 Bad Gateway", Body: body}, nil
	})}}
	if _, err := api.get(ctx, "https://api.github.com/jobs"); !errors.Is(err, context.Canceled) || calls != 1 || !body.closed {
		t.Fatalf("get error = %v, requests = %d, body closed = %v; want cancellation", err, calls, body.closed)
	}
}

func TestGetDeadlineInterruptsBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		started := time.Now()
		calls := 0
		api := client{http: &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 502, Status: "502 Bad Gateway", Body: io.NopCloser(strings.NewReader("Server Error"))}, nil
		})}}
		if _, err := api.get(ctx, "https://api.github.com/jobs"); !errors.Is(err, context.DeadlineExceeded) || calls != 1 || time.Since(started) != 500*time.Millisecond {
			t.Fatalf("get error = %v, requests = %d, elapsed = %s; want prompt deadline", err, calls, time.Since(started))
		}
	})
}
