package schedule

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestRefreshScopeRequiresExactServingIdentity(t *testing.T) {
	identity := projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "prod", GenerationID: "generation_a"}
	if err := ValidateScope(identity); err != nil {
		t.Fatal(err)
	}
	for _, value := range []projectgraph.ServingIdentity{
		{ProjectID: " project_sales", Environment: "prod", GenerationID: "generation_a"},
		{ProjectID: "project_sales", Environment: "prod ", GenerationID: "generation_a"},
		{ProjectID: "project_sales", Environment: "prod", GenerationID: " generation_a"},
	} {
		if err := ValidateScope(value); err == nil {
			t.Fatalf("ValidateScope(%#v) = nil", value)
		}
	}
}

func TestRefreshArtifactDigestIsCanonical(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	if err := ValidateArtifactDigest(valid); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{valid + " ", "sha256:a", "SHA256:" + valid[7:]} {
		if err := ValidateArtifactDigest(value); err == nil {
			t.Fatalf("ValidateArtifactDigest(%q) = nil", value)
		}
	}
}

func TestManualOnlyPipelineDefinitionIsValid(t *testing.T) {
	definition := Definition{ID: "pipeline_manual", SemanticModelID: "semantic_sales"}
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleNextAfterArtifactRoundTrip(t *testing.T) {
	schedule, err := ParseSchedule("0 6 * * *", "Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(schedule)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Schedule
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Next(time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	want := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next() = %s, want %s", got, want)
	}
}

func TestParseScheduleAcceptsGitHubCompatibleCron(t *testing.T) {
	t.Parallel()

	schedule, err := ParseSchedule("0 6 * JAN MON-FRI", "Europe/Copenhagen")
	if err != nil {
		t.Fatalf("ParseSchedule() error = %v", err)
	}
	if schedule.Expression != "0 6 * JAN MON-FRI" {
		t.Fatalf("expression = %q", schedule.Expression)
	}
	if schedule.Timezone != "Europe/Copenhagen" {
		t.Fatalf("timezone = %q", schedule.Timezone)
	}
}

func TestParseScheduleDefaultsTimezoneToUTC(t *testing.T) {
	t.Parallel()

	schedule, err := ParseSchedule("0 6 * * *", "")
	if err != nil {
		t.Fatalf("ParseSchedule() error = %v", err)
	}
	if schedule.Timezone != "UTC" {
		t.Fatalf("timezone = %q, want UTC", schedule.Timezone)
	}
}

func TestParseScheduleRejectsUnsupportedSchedules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cron     string
		timezone string
	}{
		{name: "alias", cron: "@daily"},
		{name: "six fields", cron: "0 0 6 * * *"},
		{name: "too frequent", cron: "*/4 * * * *"},
		{name: "invalid timezone", cron: "0 6 * * *", timezone: "Mars/Olympus_Mons"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseSchedule(tc.cron, tc.timezone); err == nil {
				t.Fatal("ParseSchedule() error = nil")
			}
		})
	}
}

func TestScheduleNextUsesLocalTimezone(t *testing.T) {
	t.Parallel()

	schedule, err := ParseSchedule("0 6 * * *", "Europe/Copenhagen")
	if err != nil {
		t.Fatalf("ParseSchedule() error = %v", err)
	}
	got := schedule.Next(time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC))
	want := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next() = %s, want %s", got, want)
	}
}

func TestScheduleNextAdvancesNonexistentDSTTime(t *testing.T) {
	schedule, err := ParseSchedule("30 2 * * *", "Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	got := schedule.Next(time.Date(2026, 3, 28, 3, 0, 0, 0, time.UTC))
	want := time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next() = %s, want nonexistent 02:30 advanced to %s", got, want)
	}
}

func TestScheduleNextRunsRepeatedDSTTimeOnce(t *testing.T) {
	schedule, err := ParseSchedule("30 2 * * *", "Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	first := schedule.Next(time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC))
	second := schedule.Next(first)
	if first.Equal(second) || second.Before(time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("repeated local time ran more than once: first=%s second=%s", first, second)
	}
}
