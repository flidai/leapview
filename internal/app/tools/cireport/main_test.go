package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	ciadapter "github.com/flidai/leapview/internal/app/tools/ciadapter"
	platformci "github.com/flidai/leapview/internal/platform/ci"
)

func TestJobResultsPreservesHistoricalMatrixFailures(t *testing.T) {
	t.Parallel()

	got := jobResults([]githubJob{
		{Name: "Go tests (packages)", Conclusion: "success"},
		{Name: "Go tests (app 1/4)", Conclusion: "failure"},
		{Name: "Frontend tests (core)", Conclusion: "success"},
		{Name: "CI gate", Conclusion: "failure"},
	})
	want := map[string]string{
		"go-tests/packages": "success", "go-tests/app 1/4": "failure",
		"frontend-tests/core": "success", "ci-gate": "failure",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %#v, want %#v", got, want)
	}
}

func TestDeferredStackRunIsExcludedFromExecutionMetrics(t *testing.T) {
	t.Parallel()

	if !deferredStackRun([]githubJob{
		{Name: "GitHub-hosted PR validation", Conclusion: "skipped"},
		{Name: "CI gate", Conclusion: "success"},
	}) {
		t.Fatal("non-top stack gate was not recognized as deferred")
	}
	if deferredStackRun([]githubJob{
		{Name: "GitHub-hosted PR validation", Conclusion: "success"},
		{Name: "CI gate", Conclusion: "success"},
	}) {
		t.Fatal("executed top-stack preflight was classified as deferred")
	}
}

func TestDecodePlanArchive(t *testing.T) {
	t.Parallel()

	want := platformci.Plan{
		Version:   platformci.PlanVersion,
		Reason:    "test",
		Nominal:   platformci.Jobs{Docs: true},
		Effective: platformci.Jobs{Docs: true},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("ci-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := decodePlanArchive(archive.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %#v, want %#v", got, want)
	}
}

func TestModernLaneNames(t *testing.T) {
	for name, want := range map[string]string{
		"APIGen tests (PR)": "apigen-validation", "Go package tests (merge queue)": "go-packages-validation",
		"Go application tests (nightly)": "go-application-validation", "Frontend tests (PR, site)": "frontend-validation/site",
		"Cross-language quality (PR)":        "quality-validation",
		"PostgreSQL topology isolation (PR)": "postgres-isolation-validation", "Spatial tile benchmarks (PR)": "spatial-tile-benchmarks",
		"dbt physical contract (PR)": "warehouse-validation", "Full merge validation": "full-validation",
		"Nightly dependency security": "security-validation", "JavaScript dependency evidence refresh": "dependency-evidence-refresh",
	} {
		if got := normalizedJobName(name); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
}

func TestMarshalHealthReportPreservesHistoricalPlanAndLaneIDs(t *testing.T) {
	t.Parallel()

	plan := platformci.PlanChanges(platformci.Input{Event: "pull_request", PullRequestNumber: 1}, []platformci.Change{{Status: "M", Paths: []string{"internal/analytics/query/planner.go"}}})
	plan.PR.Head, plan.PR.RunID, plan.PR.Attempt = "candidate", "42", "1"
	results := map[string]string{"prepare": "success"}
	for name, selected := range plan.PR.Effective.Selected() {
		if selected {
			results[name] = "success"
		} else {
			results[name] = "skipped"
		}
	}
	run := platformci.HealthRun{
		ID: 42, Workflow: "ci.yml", Event: "pull_request", Attempt: 1,
		Conclusion: "success", DurationSeconds: 1, QueueSeconds: 1,
		Plan: plan, Results: results,
	}
	report := platformci.AnalyzeHealth([]platformci.HealthRun{run})
	report.Alerts = []string{"warehouse-validation: expected success"}
	report.Runs[0].Problems = []string{"warehouse-validation: expected success"}
	data, err := marshalHealthReport(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"dbt": true`) {
		t.Fatalf("embedded plan lost historical field:\n%s", text)
	}
	if strings.Contains(text, `"warehouse"`) {
		t.Fatalf("neutral plan field leaked into report:\n%s", text)
	}
	if strings.Contains(text, "warehouse-validation") {
		t.Fatalf("neutral lane ID leaked into report:\n%s", text)
	}
	legacyID := ciadapter.WorkflowJobID("warehouse-validation")
	if !strings.Contains(text, `"`+legacyID+`"`) {
		t.Fatalf("historical lane ID missing from report:\n%s", text)
	}
	markdown := renderMarkdown(report, 7)
	if !strings.Contains(markdown, legacyID) {
		t.Fatalf("historical lane ID missing from Markdown:\n%s", markdown)
	}
	if strings.Contains(markdown, "warehouse-validation") {
		t.Fatalf("neutral lane ID leaked into Markdown:\n%s", markdown)
	}
	if !strings.Contains(markdown, legacyID+": expected success") {
		t.Fatalf("historical lane ID missing from Markdown diagnostics:\n%s", markdown)
	}
}

func TestUnknownConclusionCannotDisappear(t *testing.T) {
	if got := combineConclusion("success", "timed_out"); got != "timed_out" {
		t.Fatalf("timeout hidden: %s", got)
	}
}

func TestEmptyReportShowsUnavailable(t *testing.T) {
	text := renderMarkdown(platformci.AnalyzeHealth(nil), 7)
	if !strings.Contains(text, "N/A") || strings.Contains(text, "No CI health thresholds were exceeded") {
		t.Fatalf("empty data reported healthy: %s", text)
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(body string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func TestMissingExpiredAndMalformedPlanArtifacts(t *testing.T) {
	for _, kind := range []string{"missing", "expired", "malformed", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			api := client{http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/jobs") {
					return jsonResponse(`{"jobs":[{"name":"Go package tests (PR)","conclusion":"failure","started_at":"2026-09-08T00:00:10Z","completed_at":"2026-09-08T00:01:00Z"}]}`)
				}
				if strings.Contains(r.URL.Path, "/artifacts") {
					if kind == "missing" {
						return jsonResponse(`{"artifacts":[]}`)
					}
					return jsonResponse(fmt.Sprintf(`{"artifacts":[{"name":"ci-plan","expired":%t,"archive_download_url":"https://example.test/plan"}]}`, kind == "expired"))
				}
				var data bytes.Buffer
				archive := zip.NewWriter(&data)
				file, _ := archive.Create("ci-plan.json")
				if kind == "unsupported" {
					_, _ = file.Write([]byte(`{"version":99,"effective":{"docs":true}}`))
				} else {
					_, _ = file.Write([]byte(`invalid json`))
				}
				_ = archive.Close()
				return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(data.Bytes()))}, nil
			})}}
			start, _ := time.Parse(time.RFC3339, "2026-09-08T00:00:00Z")
			run, err := api.healthRun(context.Background(), "owner/repo", githubRun{ID: 1, Attempt: 1, Workflow: "ci.yml", Event: "pull_request", StartedAt: start, Conclusion: "failure"})
			if err != nil {
				t.Fatal(err)
			}
			report := platformci.AnalyzeHealth([]platformci.HealthRun{run})
			if len(report.Selection) != 0 || report.UnknownSelection != 1 || report.Failures != 1 || report.Runs[0].SelectionConfidence != "unknown" {
				t.Fatalf("untrusted selection: %+v", report)
			}
			if report.Jobs["go-packages-validation"].Executed != 1 {
				t.Fatal("failure disappeared")
			}
		})
	}
}

func TestObservedRunTimestampEdges(t *testing.T) {
	start := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	jobStart := start.Add(5 * time.Second)
	end := start.Add(time.Minute)
	run := githubRun{Attempt: 2, StartedAt: start, CreatedAt: start.Add(-24 * time.Hour), UpdatedAt: end.Add(time.Hour), Conclusion: "cancelled"}
	jobs := []githubJob{{Name: "Go package tests (PR)", Conclusion: "cancelled", StartedAt: &jobStart, CompletedAt: &end}}
	got := observedRun(run, jobs, platformci.Plan{})
	if got.DurationSeconds != 60 || got.QueueSeconds != 5 {
		t.Fatalf("rerun includes previous attempt: %+v", got)
	}
	jobs[0].CompletedAt = nil
	if got := observedRun(run, jobs, platformci.Plan{}); got.DurationSeconds != -1 {
		t.Fatal("missing completion became a sample")
	}
	run.StartedAt = time.Time{}
	if got := observedRun(run, jobs, platformci.Plan{}); got.QueueSeconds != -1 || got.DurationSeconds != -1 {
		t.Fatal("missing start became a sample")
	}
}

func TestJobPaginationAndAttemptIsolation(t *testing.T) {
	calls := 0
	api := client{http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if !strings.Contains(r.URL.Path, "/attempts/2/jobs") {
			t.Fatalf("not attempt scoped: %s", r.URL)
		}
		count := 1
		if r.URL.Query().Get("page") == "1" {
			count = 100
		}
		jobs := make([]githubJob, count)
		for i := range jobs {
			jobs[i] = githubJob{Name: fmt.Sprintf("job %d/%d", calls, i), Conclusion: "success"}
		}
		data, _ := json.Marshal(map[string]any{"jobs": jobs})
		return jsonResponse(string(data))
	})}}
	jobs, err := api.jobs(context.Background(), "owner/repo", 1, 2)
	if err != nil || len(jobs) != 101 || calls != 2 {
		t.Fatalf("pagination: %d jobs, %d calls, %v", len(jobs), calls, err)
	}
}

func TestCollectsNightlyAndIncompleteRuns(t *testing.T) {
	api := client{http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/workflows/nightly.yml/runs") {
			return jsonResponse(`{"workflow_runs":[{"id":3,"event":"schedule","run_attempt":1}]}`)
		}
		if strings.Contains(r.URL.Path, "/workflows/") {
			return jsonResponse(`{"workflow_runs":[]}`)
		}
		if strings.Contains(r.URL.Path, "/jobs") {
			return jsonResponse(`{"jobs":[]}`)
		}
		return jsonResponse(`{"artifacts":[]}`)
	})}}
	runs, err := api.healthRuns(context.Background(), "owner/repo", time.Now())
	if err != nil || len(runs) != 1 || runs[0].Workflow != "nightly.yml" {
		t.Fatalf("nightly or incomplete run lost: %+v, %v", runs, err)
	}
}

func TestDecodePlanRejectsPartialSchemaAndTrailingData(t *testing.T) {
	for _, body := range []string{`{"version":1,"effective":{"new_lane":true}}`, `{"version":1,"effective":{"docs":true}} {}`} {
		var data bytes.Buffer
		writer := zip.NewWriter(&data)
		file, _ := writer.Create("ci-plan.json")
		_, _ = file.Write([]byte(body))
		_ = writer.Close()
		if _, err := decodePlanArchive(data.Bytes()); err == nil {
			t.Fatalf("accepted incomplete schema: %s", body)
		}
	}
}

func TestModernDeferralRequiresCompleteSkippedInventory(t *testing.T) {
	names := []string{"APIGen tests (PR)", "Go package tests (PR)", "Go application tests (PR)", "PostgreSQL topology isolation (PR)", "Spatial tile benchmarks (PR)", "dbt physical contract (PR)"}
	for _, shard := range []string{"core", "reports", "chat", "data", "site"} {
		names = append(names, "Frontend tests (PR, "+shard+")")
	}
	jobs := []githubJob{{Name: "CI gate", Conclusion: "success"}}
	for _, name := range names {
		jobs = append(jobs, githubJob{Name: name, Conclusion: "skipped"})
	}
	if !deferredStackRun(jobs) {
		t.Fatal("modern deferred stack not recognized")
	}
	if deferredStackRun(jobs[:len(jobs)-1]) {
		t.Fatal("incomplete evidence mistaken for deferral")
	}
	jobs[1].Conclusion = "cancelled"
	if deferredStackRun(jobs) {
		t.Fatal("cancellation mistaken for deferral")
	}
}

func TestCurrentPRPlanProvenanceAndMatrixReporting(t *testing.T) {
	plan := platformci.PlanChanges(platformci.Input{Event: "pull_request", PullRequestNumber: 1}, []platformci.Change{{Status: "M", Paths: []string{"README.md"}}})
	plan.PR.Head = "candidate"
	plan.PR.RunID = "123"
	plan.PR.Attempt = "2"
	run := githubRun{ID: 123, Attempt: 2, HeadSHA: "candidate", Workflow: "ci.yml", Event: "pull_request", Conclusion: "success"}
	observed := observedRun(run, nil, plan)
	if observed.PlanIssue != "" {
		t.Fatalf("valid current attempt rejected: %s", observed.PlanIssue)
	}
	observed.DurationSeconds = 10
	observed.Results = map[string]string{"prepare": "success", "docs-validation": "success", "frontend-validation/site": "success", "quality-validation": "success"}
	report := platformci.AnalyzeHealth([]platformci.HealthRun{observed})
	if report.Selective.Count != 1 || report.Runs[0].SelectionConfidence != "verified" || report.Jobs["frontend-validation/site"].Expected != 1 {
		t.Fatalf("current plan not reported correctly: %+v", report)
	}
	plan.PR.Attempt = "1"
	if got := observedRun(run, nil, plan); got.PlanIssue == "" {
		t.Fatal("stale artifact trusted")
	}
}

func TestHostedTestedMergeCandidateProvenance(t *testing.T) {
	const base = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const merge = "cccccccccccccccccccccccccccccccccccccccc"
	for _, tc := range []struct {
		name, event, runHead, planHead, planBase, planRun, planAttempt string
		parents                                                        []string
		missingPR, unavailable, wrongCommit, historical                bool
		wantTrusted                                                    bool
	}{
		{name: "PR merge commit", event: "pull_request", parents: []string{base, head}, wantTrusted: true},
		{name: "historical v2 PR merge commit", event: "pull_request", historical: true, parents: []string{base, head}, wantTrusted: true},
		{name: "stack cumulative diff base differs from merge parent", event: "pull_request", planBase: "stack-ancestor", parents: []string{base, head}, wantTrusted: true},
		{name: "wrong PR head", event: "pull_request", parents: []string{base, base}},
		{name: "wrong target base", event: "pull_request", parents: []string{merge, head}},
		{name: "reversed parents", event: "pull_request", parents: []string{head, base}},
		{name: "single parent", event: "pull_request", parents: []string{head}},
		{name: "missing PR metadata", event: "pull_request", missingPR: true, parents: []string{base, head}},
		{name: "unavailable merge commit", event: "pull_request", unavailable: true},
		{name: "wrong resolved commit", event: "pull_request", wrongCommit: true, parents: []string{base, head}},
		{name: "stale run", event: "pull_request", planRun: "122", parents: []string{base, head}},
		{name: "stale attempt", event: "pull_request", planAttempt: "2", parents: []string{base, head}},
		{name: "merge queue exact SHA", event: "merge_group", runHead: merge, wantTrusted: true},
		{name: "merge queue cannot accept PR parent relationship", event: "merge_group", parents: []string{base, head}},
		{name: "PR direct head checkout remains supported", event: "pull_request", planHead: head, wantTrusted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := platformci.PlanChanges(platformci.Input{Event: "pull_request", PullRequestNumber: 1}, []platformci.Change{{Status: "M", Paths: []string{"README.md"}}})
			if tc.historical {
				plan.Version = platformci.HistoricalPRPlanVersion
				plan.PR.Nominal.Quality = false
				plan.PR.Effective.Quality = false
			}
			plan.PR.Head, plan.PR.Base, plan.PR.RunID, plan.PR.Attempt = merge, base, "123", "1"
			for target, value := range map[*string]string{&plan.PR.Head: tc.planHead, &plan.PR.Base: tc.planBase, &plan.PR.RunID: tc.planRun, &plan.PR.Attempt: tc.planAttempt} {
				if value != "" {
					*target = value
				}
			}
			runHead := head
			if tc.runHead != "" {
				runHead = tc.runHead
			}
			prJSON := fmt.Sprintf(`[{"base":{"sha":%q},"head":{"sha":%q}}]`, base, head)
			if tc.missingPR {
				prJSON = `[]`
			}
			var run githubRun
			if err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":123,"run_attempt":1,"event":%q,"head_sha":%q,"pull_requests":%s}`, tc.event, runHead, prJSON)), &run); err != nil {
				t.Fatal(err)
			}
			run.Workflow = "ci.yml"
			data, _ := ciadapter.MarshalPlan(plan)
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			file, _ := writer.Create("ci-plan.json")
			_, _ = file.Write(data)
			_ = writer.Close()
			api := client{http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/jobs"):
					return jsonResponse(`{"jobs":[]}`)
				case strings.HasSuffix(r.URL.Path, "/artifacts"):
					return jsonResponse(`{"artifacts":[{"name":"ci-plan","archive_download_url":"https://api.github.com/archive"}]}`)
				case r.URL.Path == "/archive":
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(archive.Bytes()))}, nil
				case strings.Contains(r.URL.Path, "/commits/"):
					if tc.event != "pull_request" {
						t.Fatal("non-PR candidate attempted merge-parent fallback")
					}
					if tc.unavailable {
						return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(`{}`))}, nil
					}
					parents := make([]map[string]string, 0, len(tc.parents))
					for _, sha := range tc.parents {
						parents = append(parents, map[string]string{"sha": sha})
					}
					sha := merge
					if tc.wrongCommit {
						sha = head
					}
					body, _ := json.Marshal(map[string]any{"sha": sha, "parents": parents})
					return jsonResponse(string(body))
				default:
					t.Fatalf("unexpected request %s", r.URL)
					return nil, nil
				}
			})}}
			got, err := api.healthRun(context.Background(), "owner/repo", run)
			if err != nil {
				t.Fatal(err)
			}
			if (got.PlanIssue == "") != tc.wantTrusted {
				t.Fatalf("trusted=%v, want %v; issue=%q", got.PlanIssue == "", tc.wantTrusted, got.PlanIssue)
			}
		})
	}
}
