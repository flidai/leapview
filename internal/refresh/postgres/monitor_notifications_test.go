package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMonitorRunsFiltersBeforePaginationAndKeepsActiveOutsideRange(t *testing.T) {
	_, db := refreshTestDB(t)
	repository := New(db)
	for _, item := range []struct{ id, pipeline, status string }{
		{"monitor-a", "pipe-a", "failed"}, {"monitor-b", "pipe-a", "succeeded"},
		{"monitor-c", "pipe-a", "queued"}, {"monitor-other", "pipe-b", "failed"},
	} {
		seedRefreshJob(t, db, "job-"+item.id, item.id, "monitor-project", "dev", "principal")
		_, err := repository.CreateRun(t.Context(), RunInput{RunID: item.id, ProjectID: "monitor-project", Environment: "dev", GenerationID: "generation",
			PipelineID: item.pipeline, SemanticModelID: "model", TargetType: "refresh_pipeline", TargetID: item.pipeline,
			TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("c", 64),
			ArtifactDigest: "sha256:" + strings.Repeat("a", 64), PrincipalID: "principal", JobID: "job-" + item.id})
		if err != nil {
			t.Fatal(err)
		}
		if item.status != "queued" {
			if _, err := db.Exec(t.Context(), `UPDATE refresh.run SET status=$2, started_at=clock_timestamp(), finished_at=clock_timestamp(), error=CASE WHEN $2='failed' THEN 'test failure' ELSE '' END WHERE run_id=$1`, item.id, item.status); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now().UTC()
	filter := MonitorFilter{Since: now.Add(-time.Hour), Until: now.Add(time.Hour), AllowedPipelineIDs: []string{"pipe-a"}, Limit: 1}
	page, err := repository.MonitorRuns(t.Context(), Scope{ProjectID: "monitor-project", Environment: "dev"}, filter)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || page.Failed != 1 || page.Completed != 1 || page.Active != 1 || len(page.Runs) != 1 {
		t.Fatalf("monitor page = %#v", page)
	}
	firstID := page.Runs[0].RunID
	filter.Offset = 1
	page, err = repository.MonitorRuns(t.Context(), Scope{ProjectID: "monitor-project", Environment: "dev"}, filter)
	if err != nil || page.Total != 3 || len(page.Runs) != 1 || page.Runs[0].RunID == firstID {
		t.Fatalf("second page = %#v, %v", page, err)
	}
	filter.Offset = 0
	filter.Status = "failed"
	page, err = repository.MonitorRuns(t.Context(), Scope{ProjectID: "monitor-project", Environment: "dev"}, filter)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Runs) != 1 || page.Runs[0].RunID != "monitor-a" || page.Completed != 0 || page.Failed != 1 {
		t.Fatalf("filtered page = %#v", page)
	}
	filter.Status = ""
	filter.Search = "Sales title"
	filter.PipelineIDs = []string{"pipe-a"}
	page, err = repository.MonitorRuns(t.Context(), Scope{ProjectID: "monitor-project", Environment: "dev"}, filter)
	if err != nil || page.Total != 3 {
		t.Fatalf("title-matched page = %#v, %v", page, err)
	}
	filter.Since = now.Add(time.Minute)
	page, err = repository.MonitorRuns(t.Context(), Scope{ProjectID: "monitor-project", Environment: "dev"}, filter)
	if err != nil || page.Total != 0 || page.Active != 1 {
		t.Fatalf("out-of-range page = %#v, %v", page, err)
	}
}

func TestRootRunNotificationsFollowCommittedTransitions(t *testing.T) {
	_, admin := refreshTestDB(t)
	ctx := t.Context()
	listener, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(ctx, `LISTEN leapview_refresh_changed`); err != nil {
		t.Fatal(err)
	}
	seedRefreshJob(t, admin, "notify-job", "notify-run", "notify-project", "prod", "principal")
	r := New(admin)
	_, err = r.CreateRun(ctx, RunInput{RunID: "notify-run", ProjectID: "notify-project", Environment: "prod", GenerationID: "generation", PipelineID: "notify-pipeline", SemanticModelID: "model", TargetType: "refresh_pipeline", TargetID: "notify-pipeline", TriggerType: "manual", InvocationSource: "manual", PlanDigest: "sha256:" + strings.Repeat("c", 64), ArtifactDigest: "sha256:" + strings.Repeat("a", 64), PrincipalID: "principal", JobID: "notify-job"})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	change, err := listener.Conn().WaitForNotification(waitCtx)
	if err != nil {
		t.Fatalf("committed root insert did not notify: %v", err)
	}
	var scope struct {
		ProjectID   string `json:"projectId"`
		Environment string `json:"environment"`
	}
	if err := json.Unmarshal([]byte(change.Payload), &scope); err != nil || scope.ProjectID != "notify-project" || scope.Environment != "prod" {
		t.Fatalf("root notification scope = %q, decode error %v", change.Payload, err)
	}
	cancel()
	if _, err := r.ClaimAttempt(ctx, "notify-run", "worker", 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	if _, err := listener.Conn().WaitForNotification(waitCtx); err != nil {
		t.Fatalf("committed queued-to-running transition did not notify: %v", err)
	}
	cancel()
	// A status-preserving lease update must not wake every monitoring page.
	if _, err := admin.Exec(ctx, `UPDATE refresh.run SET lease_expires_at=clock_timestamp()+interval '2 minutes' WHERE run_id='notify-run'`); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rolledBack.Exec(ctx, `UPDATE refresh.run SET status='failed', error='rolled back', finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL WHERE run_id='notify-run'`); err != nil {
		_ = rolledBack.Rollback(ctx)
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	quietCtx, quietCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	if notification, err := listener.Conn().WaitForNotification(quietCtx); err == nil {
		t.Fatalf("status-preserving or rolled-back update unexpectedly notified: %v", notification)
	}
	quietCancel()
	terminalListener, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer terminalListener.Release()
	if _, err := terminalListener.Exec(ctx, `LISTEN leapview_refresh_changed`); err != nil {
		t.Fatal(err)
	}
	// A runtime-role transition must be able to execute the database-owned
	// trigger even though it has no direct EXECUTE grant on the trigger function.
	runtimeTx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeTx.Exec(ctx, `SET LOCAL ROLE leapview_control_runtime`); err != nil {
		_ = runtimeTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := runtimeTx.Exec(ctx, `UPDATE refresh.run SET status='failed', error='test', finished_at=clock_timestamp(), lease_owner='', lease_expires_at=NULL WHERE run_id='notify-run'`); err != nil {
		_ = runtimeTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := runtimeTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	if _, err := terminalListener.Conn().WaitForNotification(waitCtx); err != nil {
		t.Fatalf("committed terminal transition did not notify: %v", err)
	}
	cancel()
}

func TestScheduleNotificationsTrackNextRunWithoutRun(t *testing.T) {
	_, admin := refreshTestDB(t)
	ctx := t.Context()
	listener, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(ctx, `LISTEN leapview_refresh_changed`); err != nil {
		t.Fatal(err)
	}
	r := New(admin)
	schedule, err := r.PutSchedule(ctx, ScheduleInput{
		ProjectID: "schedule-project", Environment: "prod", PipelineID: "pipeline", ScheduleID: "daily",
		SemanticModelID: "model", GenerationID: "generation", ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
		Cron: "0 6 * * *", Timezone: "UTC", ConcurrencyPolicy: "Forbid", NextRunAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	wait := func() {
		t.Helper()
		waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		change, err := listener.Conn().WaitForNotification(waitCtx)
		if err != nil {
			t.Fatalf("schedule did not notify: %v", err)
		}
		var scope struct {
			ProjectID   string `json:"projectId"`
			Environment string `json:"environment"`
		}
		if err := json.Unmarshal([]byte(change.Payload), &scope); err != nil || scope.ProjectID != "schedule-project" || scope.Environment != "prod" {
			t.Fatalf("schedule notification scope = %q, decode error %v", change.Payload, err)
		}
	}
	wait() // initial schedule revision
	if _, err := admin.Exec(ctx, `UPDATE refresh.schedule_revision SET next_run_at=next_run_at+interval '1 day' WHERE schedule_revision_id=$1`, schedule.ScheduleRevisionID); err != nil {
		t.Fatal(err)
	}
	wait() // cursor advanced, no refresh.run row exists
}
