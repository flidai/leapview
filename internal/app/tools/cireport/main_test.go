package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ciadapter "github.com/flidai/leapview/internal/app/tools/ciadapter"
	platformci "github.com/flidai/leapview/internal/platform/ci"
)

func currentWorkflowContract(t *testing.T, workflow string, plan platformci.Plan) *ciadapter.HealthWorkflowContract {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..")
	filename := ".github/workflows/" + workflow
	contents, err := os.ReadFile(filepath.Join(root, filename))
	if err != nil {
		t.Fatal(err)
	}
	calls, err := ciadapter.LocalHealthWorkflowCalls(contents)
	if err != nil {
		t.Fatal(err)
	}
	reusable := make(map[string][]byte, len(calls))
	for _, call := range calls {
		data, err := os.ReadFile(filepath.Join(root, strings.TrimPrefix(call, "./")))
		if err != nil {
			t.Fatal(err)
		}
		reusable[call] = data
	}
	contract, err := ciadapter.BuildHealthWorkflow(workflow, contents, reusable, plan)
	if err != nil {
		t.Fatal(err)
	}
	return &contract
}

func withWorkflowContract(run platformci.HealthRun, contract *ciadapter.HealthWorkflowContract) platformci.HealthRun {
	run.WorkflowSHA = strings.Repeat("a", 40)
	run.WorkflowJobs = contract.WorkflowJobs
	run.WorkflowRequiredJobs = contract.RequiredJobs
	run.PlanIndependentJobs = contract.PlanIndependentJobs
	return run
}

func platformciExpectedPlanJobs(plan platformci.Plan) []string {
	if plan.PR != nil {
		return append([]string{"prepare"}, plan.PR.Effective.ExpectedJobs()...)
	}
	var jobs []string
	for name, selected := range plan.Effective.Selected() {
		if selected {
			jobs = append(jobs, name)
		}
	}
	sort.Strings(jobs)
	return jobs
}

func TestJobResultsPreservesHistoricalMatrixFailures(t *testing.T) {
	t.Parallel()
	workflow := []byte(`jobs:
  go-tests:
    name: Go tests (${{ matrix.shard }})
    strategy:
      matrix:
        shard: [packages, app 1/4]
  frontend-tests:
    name: Frontend tests (${{ matrix.shard }})
    strategy:
      matrix:
        shard: [core]
  ci-gate:
    name: CI gate
    needs: [go-tests, frontend-tests]
`)
	contract, err := ciadapter.BuildHealthWorkflow("ci.yml", workflow, nil, platformci.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	got := jobResults([]githubJob{
		{Name: "Go tests (packages)", Conclusion: "success"},
		{Name: "Go tests (app 1/4)", Conclusion: "failure"},
		{Name: "Frontend tests (core)", Conclusion: "success"},
		{Name: "CI gate", Conclusion: "failure"},
	}, &contract)
	want := map[string]string{
		"go-tests/packages": "success", "go-tests/app 1/4": "failure",
		"frontend-tests/core": "success", "ci-gate": "failure",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %#v, want %#v", got, want)
	}
}

func TestMarkdownP95RequiresTwentySamples(t *testing.T) {
	for _, test := range []struct {
		count       int
		wantMetric  string
		wantAlert   string
		wantNoAlert bool
	}{
		{count: 19, wantMetric: "Exhaustive merge p50 / p95 (samples) | 13m20s / N/A (<20 samples) (19) |", wantNoAlert: true},
		{count: 20, wantMetric: "Exhaustive merge p50 / p95 (samples) | 13m20s / 13m20s (20) |", wantAlert: "full CI p95 is 13m20s (limit 12m0s)"},
	} {
		t.Run(fmt.Sprintf("%d samples", test.count), func(t *testing.T) {
			contract := currentWorkflowContract(t, "merge-validation.yml", platformci.Plan{})
			runs := make([]platformci.HealthRun, test.count)
			for i := range runs {
				results := map[string]string{}
				for _, job := range contract.RequiredJobs {
					results[job] = "success"
				}
				runs[i] = withWorkflowContract(platformci.HealthRun{
					Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "success",
					DurationSeconds: 800, QueueSeconds: 140, Results: results,
				}, contract)
			}
			markdown := renderMarkdown(platformci.AnalyzeHealth(runs), 7)
			if !strings.Contains(markdown, test.wantMetric) {
				t.Errorf("markdown does not contain metric %q:\n%s", test.wantMetric, markdown)
			}
			if test.wantAlert != "" && !strings.Contains(markdown, test.wantAlert) {
				t.Errorf("markdown does not contain alert %q", test.wantAlert)
			}
			if test.wantNoAlert && (strings.Contains(markdown, "## Alerts") || !strings.Contains(markdown, "p95 requires at least 20 samples")) {
				t.Errorf("small-sample p95 was alerted or not explained:\n%s", markdown)
			}
		})
	}
}

func TestRevisionContractNamesAndReusableChildrenControlResults(t *testing.T) {
	contract := currentWorkflowContract(t, "merge-validation.yml", platformci.Plan{})
	if len(contract.ReusableChildren) != 1 {
		t.Fatalf("unexpected reusable contract: %#v", contract.ReusableChildren)
	}
	var parent string
	var children []string
	for id, names := range contract.ReusableChildren {
		parent, children = id, names
	}
	if len(children) != 2 {
		t.Fatalf("reusable child contract = %v", children)
	}
	display := func(canonical string) string {
		for name, id := range contract.JobNames {
			if id == canonical {
				return name
			}
		}
		return ""
	}
	parentName, firstName, secondName := display(parent), display(children[0]), display(children[1])
	if parentName == "" || firstName == "" || secondName == "" {
		t.Fatalf("revision contract lacks display mappings: %v", contract.JobNames)
	}
	observed := jobResults([]githubJob{
		{Name: firstName, Conclusion: "success"},
		{Name: secondName, Conclusion: "failure"},
	}, contract)
	if observed[children[0]] != "success" || observed[children[1]] != "failure" || observed[parent] != "failure" {
		t.Fatalf("reusable child results did not aggregate from source names: %v", observed)
	}
	for _, conclusion := range []string{"failure", "cancelled", "skipped"} {
		got := jobResults([]githubJob{
			{Name: firstName, Conclusion: "success"},
			{Name: secondName, Conclusion: conclusion},
		}, contract)
		if got[parent] != conclusion {
			t.Errorf("child conclusion %q aggregated to %q", conclusion, got[parent])
		}
	}

	allJobs := make([]githubJob, 0, len(contract.RequiredJobs))
	for _, id := range contract.RequiredJobs {
		allJobs = append(allJobs, githubJob{Name: display(id), Conclusion: "success"})
	}
	for index := range allJobs {
		if allJobs[index].Name == secondName {
			allJobs[index].Conclusion = "failure"
		}
	}
	results := jobResults(allJobs, contract)
	complete := platformci.AnalyzeHealth([]platformci.HealthRun{withWorkflowContract(platformci.HealthRun{
		Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "failure", Results: results,
	}, contract)})
	if complete.Runs[0].ExpectedSource != "workflow_contract" || results[parent] != "failure" ||
		!strings.Contains(strings.Join(complete.Runs[0].Problems, "\n"), "expected "+parent+": failure") {
		t.Fatalf("failed reusable child did not fail required source lane: %+v", complete.Runs[0])
	}

	missing := jobResults([]githubJob{{Name: firstName, Conclusion: "success"}}, contract)
	if _, present := missing[parent]; present {
		t.Fatalf("one observed child synthesized a complete parent result: %v", missing)
	}
	missingReport := platformci.AnalyzeHealth([]platformci.HealthRun{withWorkflowContract(platformci.HealthRun{
		Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "success", Results: missing,
	}, contract)})
	if missingReport.Incomplete != 1 || !strings.Contains(strings.Join(missingReport.Runs[0].UnknownJobs, "\n"), children[1]) {
		t.Fatalf("missing reusable child was accepted as complete evidence: %+v", missingReport.Runs[0])
	}
}

func TestValidatedPlanMarksDeferredRun(t *testing.T) {
	const candidate = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	plan := platformci.Plan{Version: platformci.PRPlanVersion, PR: &platformci.PRPlan{
		Head: candidate, RunID: "123", Attempt: "1", Deferred: true,
	}}
	contract := currentWorkflowContract(t, "ci.yml", plan)
	run := githubRun{ID: 123, Attempt: 1, HeadSHA: candidate, TestedSHA: candidate,
		WorkflowSHA: candidate, Workflow: "ci.yml", Event: "pull_request"}
	if observed := observedRun(run, nil, plan, contract, ""); !observed.Deferred || observed.PlanIssue != "" {
		t.Fatalf("validated deferred plan was not preserved: %+v", observed)
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

func TestJobResultsRequiresRevisionSpecificNames(t *testing.T) {
	workflow := []byte(`jobs:
  contract-check:
    name: Candidate contract
  ci-gate:
    name: CI gate
    needs: [contract-check]
`)
	contract, err := ciadapter.BuildHealthWorkflow("ci.yml", workflow, nil, platformci.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	got := jobResults([]githubJob{
		{Name: "Candidate contract", Conclusion: "success"},
		{Name: "Old contract alias", Conclusion: "failure"},
	}, &contract)
	if got["contract-check"] != "success" || got["unknown/Old contract alias"] != "failure" {
		t.Fatalf("result mapping did not use only source names: %v", got)
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
	const workflowID = "dbt-warehouse-boundary-validation"
	run := platformci.HealthRun{
		ID: 42, Workflow: "ci.yml", Event: "pull_request", Attempt: 1,
		Conclusion: "success", DurationSeconds: 1, QueueSeconds: 1,
		WorkflowSHA:  strings.Repeat("a", 40),
		WorkflowJobs: []string{"warehouse-validation"}, WorkflowRequiredJobs: []string{"warehouse-validation"},
		PlanIndependentJobs: []string{"warehouse-validation"},
		Plan:                plan, Results: results,
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
	wireID := ciadapter.WorkflowJobID("warehouse-validation")
	if !strings.Contains(text, `"`+wireID+`"`) {
		t.Fatalf("workflow lane ID missing from report:\n%s", text)
	}
	var serialized struct {
		Runs []struct {
			WorkflowJobs         []string `json:"workflow_jobs"`
			WorkflowRequiredJobs []string `json:"workflow_required_jobs"`
			PlanIndependentJobs  []string `json:"plan_independent_jobs"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(data, &serialized); err != nil || len(serialized.Runs) != 1 {
		t.Fatalf("decode serialized workflow evidence: %v", err)
	}
	for label, values := range map[string][]string{
		"workflow jobs":         serialized.Runs[0].WorkflowJobs,
		"required jobs":         serialized.Runs[0].WorkflowRequiredJobs,
		"plan-independent jobs": serialized.Runs[0].PlanIndependentJobs,
	} {
		if len(values) != 1 || values[0] != workflowID {
			t.Errorf("%s serialized as %v, want %s", label, values, workflowID)
		}
	}
	markdown := renderMarkdown(report, 7)
	if !strings.Contains(markdown, wireID) {
		t.Fatalf("historical lane ID missing from Markdown:\n%s", markdown)
	}
	if strings.Contains(markdown, "warehouse-validation") {
		t.Fatalf("neutral lane ID leaked into Markdown:\n%s", markdown)
	}
	if !strings.Contains(markdown, wireID+": expected success") {
		t.Fatalf("workflow lane ID missing from Markdown diagnostics:\n%s", markdown)
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

func TestWorkflowRevisionUsesValidatedCandidateForPullRequests(t *testing.T) {
	const source = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const candidate = "cccccccccccccccccccccccccccccccccccccccc"
	plan := platformci.PlanChanges(platformci.Input{Event: "pull_request", PullRequestNumber: 1}, []platformci.Change{{Status: "M", Paths: []string{"README.md"}}})
	plan.PR.Head, plan.PR.RunID, plan.PR.Attempt = candidate, "123", "2"
	run := githubRun{ID: 123, Attempt: 2, Event: "pull_request", HeadSHA: source, TestedSHA: candidate}
	if got, err := workflowRevision(run, plan); err != nil || got != candidate {
		t.Fatalf("PR workflow revision = %q, %v; want tested candidate", got, err)
	}
	run.Event, run.TestedSHA = "merge_group", ""
	if got, err := workflowRevision(run, plan); err != nil || got != source {
		t.Fatalf("non-PR workflow revision = %q, %v; want run head", got, err)
	}
	run.Event, run.TestedSHA, plan.PR.Attempt = "pull_request", candidate, "1"
	if _, err := workflowRevision(run, plan); err == nil {
		t.Fatal("stale plan was accepted as source revision evidence")
	}
}

func TestWorkflowSourceSingleFlightReleasesSuccessAndErrors(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "error"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			transport := testTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				if failure {
					return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader("missing")), Header: make(http.Header)}, nil
				}
				body, _ := json.Marshal(map[string]string{
					"path": ".github/workflows/ci.yml", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("jobs: {}\n")),
				})
				return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
			})
			api := client{http: &http.Client{Transport: transport}}
			const readers = 12
			start := make(chan struct{})
			var group sync.WaitGroup
			group.Add(readers)
			errCh := make(chan error, readers)
			for range readers {
				go func() {
					defer group.Done()
					<-start
					data, err := api.workflowSource(context.Background(), "owner/repo", strings.Repeat("a", 40), ".github/workflows/ci.yml")
					if failure {
						if err == nil {
							errCh <- fmt.Errorf("expected unavailable source, got %q", data)
							return
						}
						errCh <- nil
						return
					}
					if err != nil || string(data) != "jobs: {}\n" {
						errCh <- fmt.Errorf("source = %q, %v", data, err)
						return
					}
					errCh <- nil
				}()
			}
			close(start)
			<-entered
			close(release)
			group.Wait()
			close(errCh)
			for err := range errCh {
				if err != nil {
					t.Error(err)
				}
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("source API calls = %d, want 1", got)
			}
		})
	}
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
			if report.Jobs["unknown/Go package tests (PR)"].Executed != 1 {
				t.Fatal("failure disappeared")
			}
		})
	}
}

func TestDraftSkipWithoutPlanRemainsUnknown(t *testing.T) {
	jobs := []githubJob{
		{Name: "CI gate", Conclusion: "skipped"},
		{Name: "Plan PR validation", Conclusion: "skipped"},
	}
	api := client{http: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/jobs"):
			body, err := json.Marshal(map[string]any{"jobs": jobs})
			if err != nil {
				t.Fatal(err)
			}
			return jsonResponse(string(body))
		case strings.Contains(r.URL.Path, "/artifacts"):
			return jsonResponse(`{"artifacts":[]}`)
		default:
			t.Fatalf("unexpected API request: %s", r.URL)
			return nil, nil
		}
	})}}
	run, err := api.healthRun(context.Background(), "owner/repo", githubRun{ID: 1, Attempt: 1, Workflow: "ci.yml", Event: "pull_request", Conclusion: "success"})
	if err != nil {
		t.Fatal(err)
	}
	report := platformci.AnalyzeHealth([]platformci.HealthRun{run})
	if report.Incomplete != 1 || report.UnknownSelection != 1 || len(report.Alerts) == 0 {
		t.Fatalf("draft skip without source was treated as healthy: %+v", report)
	}
	if report.Runs[0].Category != "unknown" || report.Runs[0].WorkflowSHA != "" {
		t.Fatalf("draft source uncertainty was lost: %+v", report.Runs[0])
	}
}

func TestObservedRunTimestampEdges(t *testing.T) {
	start := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	jobStart := start.Add(5 * time.Second)
	end := start.Add(time.Minute)
	run := githubRun{Attempt: 2, StartedAt: start, CreatedAt: start.Add(-24 * time.Hour), UpdatedAt: end.Add(time.Hour), Conclusion: "cancelled"}
	jobs := []githubJob{{Name: "Go package tests (PR)", Conclusion: "cancelled", StartedAt: &jobStart, CompletedAt: &end}}
	got := observedRun(run, jobs, platformci.Plan{}, nil, "no contract")
	if got.DurationSeconds != 60 || got.QueueSeconds != 5 {
		t.Fatalf("rerun includes previous attempt: %+v", got)
	}
	jobs[0].CompletedAt = nil
	if got := observedRun(run, jobs, platformci.Plan{}, nil, "no contract"); got.DurationSeconds != -1 {
		t.Fatal("missing completion became a sample")
	}
	run.StartedAt = time.Time{}
	if got := observedRun(run, jobs, platformci.Plan{}, nil, "no contract"); got.QueueSeconds != -1 || got.DurationSeconds != -1 {
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
	if runs[0].WorkflowSHA != "" || runs[0].ContractIssue == "" || len(runs[0].WorkflowRequiredJobs) != 0 {
		t.Fatalf("missing source revision was replaced with current inventory: %+v", runs[0])
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

func TestCurrentPRPlanProvenanceAndMatrixReporting(t *testing.T) {
	const candidate = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	plan := platformci.PlanChanges(platformci.Input{Event: "pull_request", PullRequestNumber: 1}, []platformci.Change{{Status: "M", Paths: []string{"README.md"}}})
	plan.PR.Head = candidate
	plan.PR.RunID = "123"
	plan.PR.Attempt = "2"
	contract := currentWorkflowContract(t, "ci.yml", plan)
	run := githubRun{ID: 123, Attempt: 2, HeadSHA: candidate, TestedSHA: candidate, WorkflowSHA: candidate, Workflow: "ci.yml", Event: "pull_request", Conclusion: "success"}
	observed := observedRun(run, nil, plan, contract, "")
	if observed.PlanIssue != "" {
		t.Fatalf("valid current attempt rejected: %s", observed.PlanIssue)
	}
	observed.DurationSeconds = 10
	observed.Results = map[string]string{}
	for _, job := range append(platformciExpectedPlanJobs(plan), contract.PlanIndependentJobs...) {
		observed.Results[job] = "success"
	}
	report := platformci.AnalyzeHealth([]platformci.HealthRun{observed})
	if report.Selective.Count != 1 || report.Runs[0].SelectionConfidence != "verified" || report.Jobs["frontend-validation/site"].Expected != 1 {
		t.Fatalf("current plan not reported correctly: %+v", report)
	}
	for _, required := range contract.PlanIndependentJobs {
		if !strings.Contains(strings.Join(report.Runs[0].ExpectedJobs, "\n"), required) {
			t.Errorf("observed PR plan omitted workflow-required job %s: %v", required, report.Runs[0].ExpectedJobs)
		}
	}
	if len(contract.PlanIndependentJobs) == 0 {
		t.Fatal("current PR contract lost plan-independent reusable children")
	}
	delete(observed.Results, contract.PlanIndependentJobs[len(contract.PlanIndependentJobs)-1])
	incomplete := platformci.AnalyzeHealth([]platformci.HealthRun{observed})
	if incomplete.Incomplete != 1 || incomplete.Runs[0].SelectionConfidence != "incomplete" {
		t.Fatalf("missing source-required PR child was accepted: %+v", incomplete.Runs[0])
	}
	plan.PR.Attempt = "1"
	if got := observedRun(run, nil, plan, contract, ""); got.PlanIssue == "" {
		t.Fatal("stale artifact trusted")
	}
}

func TestHostedTestedMergeCandidateProvenance(t *testing.T) {
	const base = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const merge = "cccccccccccccccccccccccccccccccccccccccc"
	const advancedBase = "dddddddddddddddddddddddddddddddddddddddd"
	for _, tc := range []struct {
		name, event, runHead, planHead, planBase, planRun, planAttempt string
		parents                                                        []string
		missingPR, unavailable, wrongCommit, historical                bool
		wantTrusted                                                    bool
	}{
		{name: "PR merge commit", event: "pull_request", parents: []string{base, head}, wantTrusted: true},
		{name: "historical v2 PR merge commit", event: "pull_request", historical: true, parents: []string{base, head}, wantTrusted: true},
		{name: "stack cumulative diff base differs from merge parent", event: "pull_request", planBase: "stack-ancestor", parents: []string{base, head}, wantTrusted: true},
		{name: "target branch advanced beyond event base", event: "pull_request", parents: []string{advancedBase, head}, wantTrusted: true},
		{name: "wrong PR head", event: "pull_request", parents: []string{base, base}},
		{name: "wrong target base", event: "pull_request", parents: []string{merge, head}},
		{name: "reversed parents", event: "pull_request", parents: []string{head, base}},
		{name: "single parent", event: "pull_request", parents: []string{head}},
		{name: "missing mutable PR metadata", event: "pull_request", missingPR: true, parents: []string{base, head}, wantTrusted: true},
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
				case strings.Contains(r.URL.Path, "/contents/"):
					body, _ := json.Marshal(map[string]string{
						"path": ".github/workflows/ci.yml", "encoding": "base64",
						"content": base64.StdEncoding.EncodeToString([]byte("jobs:\n  prepare:\n    name: Plan PR validation\n  ci-gate:\n    name: CI gate\n    needs: [prepare]\n")),
					})
					return jsonResponse(string(body))
				case strings.HasSuffix(r.URL.Path, "/jobs"):
					return jsonResponse(`{"jobs":[]}`)
				case strings.HasSuffix(r.URL.Path, "/artifacts"):
					return jsonResponse(`{"artifacts":[{"name":"ci-plan","archive_download_url":"https://api.github.com/archive"}]}`)
				case r.URL.Path == "/archive":
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(archive.Bytes()))}, nil
				case strings.Contains(r.URL.Path, "/compare/"):
					ancestor := plan.PR.Base
					descendant := ""
					if len(tc.parents) > 0 {
						descendant = tc.parents[0]
					}
					if (ancestor == "stack-ancestor" && descendant == base) || (ancestor == base && descendant == advancedBase) {
						return jsonResponse(fmt.Sprintf(`{"status":"ahead","merge_base_commit":{"sha":%q}}`, ancestor))
					}
					return jsonResponse(`{"status":"diverged","merge_base_commit":{"sha":"other"}}`)
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

func TestMarkdownExplainsIncompleteEvidenceWithoutHidingCancellations(t *testing.T) {
	report := platformci.AnalyzeHealth([]platformci.HealthRun{
		{ID: 11, Workflow: "ci.yml", Event: "pull_request", Conclusion: "cancelled", DurationSeconds: -1, PlanIssue: "missing, expired or invalid ci-plan artifact"},
		{ID: 12, Workflow: "ci.yml", Event: "pull_request", Conclusion: "cancelled", DurationSeconds: -1, PlanIssue: "missing, expired or invalid ci-plan artifact"},
		{ID: 13, Workflow: "ci.yml", Event: "pull_request", Conclusion: "failure", DurationSeconds: -1, PlanIssue: "missing, expired or invalid ci-plan artifact"},
	})
	markdown := renderMarkdown(report, 7)
	for _, want := range []string{
		"| missing, expired or invalid ci-plan artifact | cancelled | 2 | 11, 12 |",
		"| missing, expired or invalid ci-plan artifact | failure | 1 | 13 |",
		"| duration unavailable | cancelled | 2 | 11, 12 |",
		"3 runs have incomplete reporting evidence; health is not established",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("report missing %q:\n%s", want, markdown)
		}
	}
}

func TestMarkdownDiagnosticsBoundAndEscapeExamples(t *testing.T) {
	report := platformci.HealthReport{}
	for _, id := range []int64{5, 2, 4, 1, 3} {
		report.Runs = append(report.Runs, platformci.HealthRun{ID: id, Conclusion: "failure", Problems: []string{"bad|name\nvalue", "bad|name\nvalue"}})
	}
	markdown := renderMarkdown(report, 7)
	if !strings.Contains(markdown, "| bad&#124;name value | failure | 5 | 1, 2, 3 |") {
		t.Fatalf("diagnostics must count each run once, escape cells and bound sorted examples:\n%s", markdown)
	}
}
