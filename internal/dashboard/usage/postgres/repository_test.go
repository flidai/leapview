package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/usage"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func usageStore(t *testing.T) *Repository {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	repository, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestRepositoryPostgreSQL18ViewerDaySemanticsAndRetention(t *testing.T) {
	repository := usageStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	views := []usage.View{
		{ProjectID: "project:usage", DashboardID: "dashboard:overview", PageID: "overview", PrincipalID: "principal:a", ViewedAt: base},
		{ProjectID: "project:usage", DashboardID: "dashboard:overview", PageID: "details", PrincipalID: "principal:a", ViewedAt: base.Add(2 * time.Hour)},
		{ProjectID: "project:usage", DashboardID: "dashboard:overview", PageID: "overview", PrincipalID: "principal:b", ViewedAt: base.Add(24 * time.Hour)},
	}
	for _, view := range views {
		if err := repository.RecordView(t.Context(), view); err != nil {
			t.Fatal(err)
		}
	}
	summaries, err := repository.ListSummaries(t.Context(), base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ViewerCount != 2 || summaries[0].ViewerDays != 2 {
		t.Fatalf("summaries = %#v", summaries)
	}
	if !summaries[0].LastViewedAt.Equal(base.Add(24 * time.Hour)) {
		t.Fatalf("last viewed = %v", summaries[0].LastViewedAt)
	}
	old := base.Add(-usage.RetentionWindow - 24*time.Hour)
	if err := repository.RecordView(t.Context(), usage.View{ProjectID: "project:usage", DashboardID: "dashboard:old", PageID: "overview", PrincipalID: "principal:a", ViewedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordView(t.Context(), usage.View{ProjectID: "project:usage", DashboardID: "dashboard:new", PageID: "overview", PrincipalID: "principal:a", ViewedAt: base}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repository.DeleteBefore(t.Context(), base.Add(-usage.RetentionWindow), 10); err != nil || deleted != 1 {
		t.Fatalf("retention deleted = %d (%v), want 1", deleted, err)
	}
	if _, err := repository.DeleteBefore(t.Context(), base.Add(-usage.RetentionWindow), 0); err == nil {
		t.Fatal("unbounded retention batch was accepted")
	}
	if _, err := repository.DeleteBefore(t.Context(), base.Add(-usage.RetentionWindow), 10001); err == nil {
		t.Fatal("oversized retention batch was accepted")
	}
	if _, err := repository.DeleteBefore(t.Context(), time.Time{}, 10); err == nil {
		t.Fatal("zero retention cutoff was accepted")
	}
	if got, err := repository.ListSummaries(t.Context(), old.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	} else {
		for _, summary := range got {
			if summary.DashboardID == "dashboard:old" {
				t.Fatal("retention left old viewer-day row")
			}
		}
	}
}

func TestRepositoryPostgreSQL18RejectsInvalidView(t *testing.T) {
	repository := usageStore(t)
	if err := repository.RecordView(t.Context(), usage.View{}); err == nil {
		t.Fatal("invalid view succeeded")
	}
	if _, err := repository.ListSummaries(t.Context(), time.Now()); errors.Is(err, ErrUnavailable) {
		t.Fatal("unexpected unavailable error")
	}
}
