package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	ciadapter "github.com/flidai/leapview/internal/app/tools/ciadapter"
	platformci "github.com/flidai/leapview/internal/platform/ci"
)

type githubRun struct {
	TestedSHA    string `json:"-"`
	PullRequests []struct {
		Base githubRevision `json:"base"`
		Head githubRevision `json:"head"`
	} `json:"pull_requests"`
	HeadSHA    string    `json:"head_sha"`
	ID         int64     `json:"id"`
	Workflow   string    `json:"-"`
	StartedAt  time.Time `json:"run_started_at"`
	Event      string    `json:"event"`
	Attempt    int       `json:"run_attempt"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Conclusion string    `json:"conclusion"`
}

type githubRevision struct {
	SHA string `json:"sha"`
}

type githubJob struct {
	Name        string     `json:"name"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type githubArtifact struct {
	Name               string `json:"name"`
	ArchiveDownloadURL string `json:"archive_download_url"`
	Expired            bool   `json:"expired"`
}

type client struct {
	http  *http.Client
	token string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	repo := flag.String("repo", os.Getenv("GITHUB_REPOSITORY"), "owner/repository")
	token := flag.String("token", os.Getenv("GITHUB_TOKEN"), "GitHub token")
	days := flag.Int("days", 7, "reporting window in days")
	output := flag.String("output", "ci-health.json", "health report JSON")
	summary := flag.String("summary", "", "Markdown summary output")
	flag.Parse()
	if *repo == "" || *token == "" {
		return errors.New("--repo and --token are required")
	}
	if *days < 1 || *days > 30 {
		return errors.New("--days must be between 1 and 30")
	}

	since := time.Now().UTC().Add(-time.Duration(*days) * 24 * time.Hour)
	api := &client{http: &http.Client{Timeout: 30 * time.Second}, token: *token}
	runs, err := api.healthRuns(context.Background(), *repo, since)
	if err != nil {
		return err
	}
	report := platformci.AnalyzeHealth(runs)
	reportJSON, err := marshalHealthReport(report)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, append(reportJSON, '\n'), 0o644); err != nil {
		return err
	}
	markdown := renderMarkdown(report, *days)
	if *summary != "" {
		if err := os.WriteFile(*summary, []byte(markdown), 0o644); err != nil {
			return err
		}
	}
	fmt.Print(markdown)
	return nil
}

func (c *client) healthRuns(ctx context.Context, repo string, since time.Time) ([]platformci.HealthRun, error) {
	var listedRuns []githubRun
	for _, workflow := range []string{"ci.yml", "merge-validation.yml", "nightly.yml"} {
		for page := 1; page <= 10; page++ {
			endpoint := fmt.Sprintf(
				"https://api.github.com/repos/%s/actions/workflows/%s/runs?per_page=100&page=%d&created=%%3E%%3D%s",
				repo,
				workflow,
				page,
				since.Format("2006-01-02"),
			)
			var response struct {
				Runs []githubRun `json:"workflow_runs"`
			}
			if err := c.getJSON(ctx, endpoint, &response); err != nil {
				return nil, fmt.Errorf("list %s runs page %d: %w", workflow, page, err)
			}
			for i := range response.Runs {
				response.Runs[i].Workflow = workflow
			}
			listedRuns = append(listedRuns, response.Runs...)
			if len(response.Runs) < 100 {
				break
			}
			if page == 10 {
				return nil, fmt.Errorf("%s run listing reached API search cap; refusing a truncated report", workflow)
			}
		}
	}

	var candidates []githubRun
	for _, run := range listedRuns {
		if !run.CreatedAt.IsZero() && run.CreatedAt.Before(since) {
			continue
		}
		candidates = append(candidates, run)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan githubRun)
	var (
		runs     []platformci.HealthRun
		firstErr error
		mutex    sync.Mutex
		group    sync.WaitGroup
	)
	workers := 8
	if len(candidates) < workers {
		workers = len(candidates)
	}
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for run := range work {
				healthRun, err := c.healthRun(ctx, repo, run)
				mutex.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				if err == nil {
					runs = append(runs, healthRun)
				}
				mutex.Unlock()
			}
		}()
	}
	for _, run := range candidates {
		select {
		case work <- run:
		case <-ctx.Done():
			break
		}
	}
	close(work)
	group.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, nil
}

func (c *client) healthRun(ctx context.Context, repo string, run githubRun) (platformci.HealthRun, error) {
	if run.Attempt > 1 {
		var attempt githubRun
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d/attempts/%d", repo, run.ID, run.Attempt)
		if err := c.getJSON(ctx, endpoint, &attempt); err != nil {
			return platformci.HealthRun{}, fmt.Errorf("get run attempt: %w", err)
		}
		run.StartedAt = attempt.StartedAt
	}
	jobs, err := c.jobs(ctx, repo, run.ID, run.Attempt)
	if err != nil {
		return platformci.HealthRun{}, err
	}
	plan, err := c.plan(ctx, repo, run.ID)
	if err != nil {
		return platformci.HealthRun{}, err
	}
	run.TestedSHA = c.testedCandidate(ctx, repo, run, plan)
	return observedRun(run, jobs, plan), nil
}

// PR run metadata names the source head, while checkout tests GitHub's merge
// commit. Verify the immutable candidate's two parents against the PR metadata
// attached to this run; never resolve today's mutable refs/pull/N/merge.
func (c *client) testedCandidate(ctx context.Context, repo string, run githubRun, plan platformci.Plan) string {
	if plan.PR == nil || run.Event != "pull_request" || plan.PR.Head == run.HeadSHA {
		return run.HeadSHA
	}
	if run.HeadSHA == "" || len(run.PullRequests) == 0 || platformci.ValidatePRPlan(plan) != nil || plan.PR.RunID != fmt.Sprint(run.ID) || plan.PR.Attempt != fmt.Sprint(run.Attempt) {
		return run.HeadSHA
	}
	var commit struct {
		SHA     string           `json:"sha"`
		Parents []githubRevision `json:"parents"`
	}
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/commits/%s", repo, url.PathEscape(plan.PR.Head))
	if err := c.getJSON(ctx, endpoint, &commit); err != nil {
		// Unavailable candidate evidence leaves the plan unverified, without
		// discarding observed job results or inventing a selection.
		return run.HeadSHA
	}
	if commit.SHA != plan.PR.Head || len(commit.Parents) != 2 || commit.Parents[1].SHA != run.HeadSHA {
		return run.HeadSHA
	}
	for _, pr := range run.PullRequests {
		if pr.Head.SHA == run.HeadSHA && pr.Base.SHA != "" && pr.Base.SHA == commit.Parents[0].SHA {
			// plan.PR.Base is a diff base, potentially a cumulative stack
			// ancestor; it need not be this merge commit's first parent.
			return commit.SHA
		}
	}
	return run.HeadSHA
}

func observedRun(run githubRun, jobs []githubJob, plan platformci.Plan) platformci.HealthRun {
	queue, elapsed := int64(-1), int64(-1)
	if started := earliestStart(jobs); !run.StartedAt.IsZero() && !started.IsZero() && !started.Before(run.StartedAt) {
		queue = int64(started.Sub(run.StartedAt).Seconds())
	}
	var completed time.Time
	timestampsComplete := len(jobs) > 0
	for _, job := range jobs {
		if job.Conclusion == "skipped" {
			continue
		}
		if job.CompletedAt == nil || job.CompletedAt.IsZero() || job.StartedAt == nil || job.StartedAt.IsZero() || job.CompletedAt.Before(*job.StartedAt) || (!run.StartedAt.IsZero() && job.StartedAt.Before(run.StartedAt)) {
			timestampsComplete = false
			continue
		}
		if job.CompletedAt.After(completed) {
			completed = *job.CompletedAt
		}
	}
	if run.Conclusion != "" && timestampsComplete && !run.StartedAt.IsZero() && !completed.IsZero() && !completed.Before(run.StartedAt) {
		elapsed = int64(completed.Sub(run.StartedAt).Seconds())
	}
	planIssue := ""
	if plan.Version == 0 {
		planIssue = "missing, expired or invalid ci-plan artifact"
	}
	// Run artifacts are not attempt-scoped. Do not attest a rerun with an artifact
	// potentially uploaded by a different attempt.
	if run.Attempt > 1 && plan.Version != 0 && plan.PR == nil {
		planIssue = "plan artifact is not bound to the latest attempt"
	}
	candidate := run.HeadSHA
	if run.Event == "pull_request" && run.TestedSHA != "" {
		candidate = run.TestedSHA
	}
	if plan.PR != nil && (plan.PR.Head != candidate || plan.PR.RunID != fmt.Sprint(run.ID) || plan.PR.Attempt != fmt.Sprint(run.Attempt)) {
		planIssue = "plan provenance does not match run/attempt/candidate"
	}
	deferred := run.Event == "pull_request" && deferredStackRun(jobs)
	if plan.PR != nil && platformci.ValidatePRPlan(plan) == nil && planIssue == "" {
		deferred = run.Event == "pull_request" && plan.PR.Deferred
	}
	return platformci.HealthRun{
		ID: run.ID, Workflow: run.Workflow, Event: run.Event, Attempt: run.Attempt,
		Conclusion: run.Conclusion, DurationSeconds: elapsed, QueueSeconds: queue,
		Deferred: deferred,
		Plan:     plan, PlanIssue: planIssue, Results: jobResults(jobs),
	}
}

func deferredStackRun(jobs []githubJob) bool {
	results := jobResults(jobs)
	if results["ci-gate"] != "success" {
		return false
	}
	if results["legacy-pr-validation"] == "skipped" && len(results) == 2 {
		return true
	}
	expected := platformci.ExpectedHealthJobs("ci.yml")
	if _, ok := results["prepare"]; !ok {
		var legacy []string
		for _, job := range expected {
			if job != "prepare" && job != "docs-validation" {
				legacy = append(legacy, job)
			}
		}
		expected = legacy
	}
	if len(results) != len(expected) {
		return false
	}
	for _, job := range expected {
		if job != "ci-gate" && results[job] != "skipped" {
			return false
		}
	}
	return true
}

func (c *client) jobs(ctx context.Context, repo string, runID int64, attempt int) ([]githubJob, error) {
	var jobs []githubJob
	path := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d", repo, runID)
	if attempt > 0 {
		path += fmt.Sprintf("/attempts/%d", attempt)
	}
	for page := 1; ; page++ {
		var response struct {
			Jobs []githubJob `json:"jobs"`
		}
		if err := c.getJSON(ctx, fmt.Sprintf("%s/jobs?per_page=100&page=%d", path, page), &response); err != nil {
			return nil, fmt.Errorf("list jobs for run %d: %w", runID, err)
		}
		jobs = append(jobs, response.Jobs...)
		if len(response.Jobs) < 100 {
			return jobs, nil
		}
	}
}

func (c *client) plan(ctx context.Context, repo string, runID int64) (platformci.Plan, error) {
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs/%d/artifacts?per_page=100&page=%d", repo, runID, page)
		var response struct {
			Artifacts []githubArtifact `json:"artifacts"`
		}
		if err := c.getJSON(ctx, endpoint, &response); err != nil {
			return platformci.Plan{}, fmt.Errorf("list artifacts for run %d: %w", runID, err)
		}
		for _, artifact := range response.Artifacts {
			if artifact.Name != "ci-plan" || artifact.Expired {
				continue
			}
			data, err := c.get(ctx, artifact.ArchiveDownloadURL)
			if err != nil {
				return platformci.Plan{}, fmt.Errorf("download plan for run %d: %w", runID, err)
			}
			plan, err := decodePlanArchive(data)
			if err != nil {
				return platformci.Plan{}, nil
			}
			return plan, nil
		}
		if len(response.Artifacts) < 100 {
			return platformci.Plan{}, nil
		}
	}
}

func (c *client) getJSON(ctx context.Context, endpoint string, destination any) error {
	data, err := c.get(ctx, endpoint)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, destination)
}

func (c *client) get(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned %s: %s", endpoint, response.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func decodePlanArchive(data []byte) (platformci.Plan, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return platformci.Plan{}, err
	}
	for _, file := range archive.File {
		if file.Name != "ci-plan.json" {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return platformci.Plan{}, err
		}
		defer reader.Close()
		return ciadapter.DecodePlan(reader)
	}
	return platformci.Plan{}, errors.New("ci-plan.json missing from artifact")
}

func jobResults(jobs []githubJob) map[string]string {
	results := map[string]string{}
	for _, job := range jobs {
		name := normalizedJobName(job.Name)
		if name == "" {
			continue
		}
		if prior, ok := results[name]; ok {
			results[name] = combineConclusion(prior, job.Conclusion)
		} else {
			results[name] = job.Conclusion
		}
	}
	return results
}

func normalizedJobName(name string) string {
	return ciadapter.HealthJobName(name)
}

func combineConclusion(current, next string) string {
	rank := func(value string) int {
		switch value {
		case "success":
			return 1
		case "skipped":
			return 2
		case "cancelled":
			return 4
		case "failure", "timed_out", "action_required", "startup_failure":
			return 5
		default:
			return 3
		}
	}
	if rank(next) > rank(current) {
		return next
	}
	return current
}

func earliestStart(jobs []githubJob) time.Time {
	var earliest time.Time
	for _, job := range jobs {
		if job.StartedAt == nil || job.StartedAt.IsZero() || job.Conclusion == "skipped" {
			continue
		}
		if earliest.IsZero() || job.StartedAt.Before(earliest) {
			earliest = *job.StartedAt
		}
	}
	return earliest
}

func renderMarkdown(report platformci.HealthReport, days int) string {
	var output strings.Builder
	fmt.Fprintf(&output, "# CI health — trailing %d days\n\n", days)
	fmt.Fprintf(&output, "| Metric | Value |\n|---|---:|\n| Runs | %d |\n", report.RunCount)
	fmt.Fprintf(&output, "| Success / failure / cancelled / skipped / unknown conclusion | %d / %d / %d / %d / %d |\n", report.Successes, report.Failures, report.Cancellations, report.Skipped, report.UnknownConclusions)
	fmt.Fprintf(&output, "| Deferred stack layers | %d |\n| Incomplete evidence | %d |\n| Missing durations | %d |\n", report.Deferred, report.Incomplete, report.MissingDurations)
	for _, row := range []struct {
		name   string
		metric platformci.DurationMetric
	}{
		{"Selective PR", report.Selective}, {"Exhaustive merge", report.Merge}, {"Nightly", report.Nightly}, {"Full PR / audit", report.FullPR}, {"Unknown category", report.Unknown}, {"Queue", report.Queue},
	} {
		fmt.Fprintf(&output, "| %s p50 / p95 (samples) | %s |\n", row.name, formatMetric(row.metric))
	}
	fmt.Fprintf(&output, "| Reruns | %d (%.1f%%) |\n| Supported plans / unknown selection | %d / %d |\n| Audit samples / misses | %d / %d |\n", report.Reruns, report.RerunPercent, report.PlannedRuns, report.UnknownSelection, report.AuditSamples, report.AuditMisses)
	output.WriteString("\nLatency uses latest-attempt timestamps, including failed and cancelled attempts with complete timestamps. Unknown evidence is not proof of success.\n")
	output.WriteString("\n## Planned selection\n\nRates use supported plans only; they are not execution rates.\n\n| Job | Planned | Rate |\n|---|---:|---:|\n")
	names := make([]string, 0, len(report.Selection))
	for name := range report.Selection {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m := report.Selection[name]
		fmt.Fprintf(&output, "| %s | %d | %.1f%% |\n", markdownCell(ciadapter.WorkflowJobID(name)), m.Selected, m.Percent)
	}
	if report.PlannedRuns == 0 {
		output.WriteString("\nN/A — no supported planning evidence.\n")
	}
	output.WriteString("\n## Expected and observed jobs\n\nCounts are per run and matrix member. Expected jobs come from a supported plan or the documented exhaustive workflow registry; observed skips do not prove intent. Unknown counts include missing expected jobs and unknown names/conclusions.\n\n| Job | Expected | Executed | Skipped | Unknown |\n|---|---:|---:|---:|---:|\n")
	names = nil
	for name := range report.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m := report.Jobs[name]
		fmt.Fprintf(&output, "| %s | %d | %d | %d | %d |\n", markdownCell(ciadapter.WorkflowJobID(name)), m.Expected, m.Executed, m.Skipped, m.Unknown)
	}
	if len(report.Alerts) > 0 {
		output.WriteString("\n## Alerts\n\n")
		for _, alert := range report.Alerts {
			fmt.Fprintf(&output, "- %s\n", wireJobText(alert))
		}
	} else {
		output.WriteString("\nNo measured thresholds exceeded. Audit coverage and sample counts are reported above.\n")
	}
	return output.String()
}

func markdownCell(value string) string {
	return strings.NewReplacer("|", "&#124;", "\n", " ", "\r", " ").Replace(value)
}
func formatMetric(metric platformci.DurationMetric) string {
	if metric.Count == 0 {
		return "N/A (0)"
	}
	return fmt.Sprintf("%s / %s (%d)", formatSeconds(metric.P50Seconds), formatSeconds(metric.P95Seconds), metric.Count)
}
func formatSeconds(seconds int64) string { return (time.Duration(seconds) * time.Second).String() }
