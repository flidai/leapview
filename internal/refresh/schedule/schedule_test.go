package schedule

import (
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

func TestScheduledDefinitionRequiresPipelineWidePolicy(t *testing.T) {
	schedule := Schedule{ID: "every-minute", Expression: "* * * * *"}
	cases := []Definition{
		{ID: "pipeline", SemanticModelID: "semantic", Schedules: []Schedule{schedule}},
		{ID: "pipeline", SemanticModelID: "semantic", Timezone: "UTC", ConcurrencyPolicy: ConcurrencyForbid, StartingDeadlineSeconds: -1, Schedules: []Schedule{schedule}},
		{ID: "pipeline", SemanticModelID: "semantic", Timezone: "UTC", ConcurrencyPolicy: "Allow", Schedules: []Schedule{schedule}},
	}
	for _, definition := range cases {
		if err := definition.Validate(); err == nil {
			t.Fatalf("Definition.Validate(%#v) = nil", definition)
		}
	}
	valid := Definition{ID: "pipeline", SemanticModelID: "semantic", Timezone: "UTC", ConcurrencyPolicy: ConcurrencyReplace, StartingDeadlineSeconds: 0, Schedules: []Schedule{schedule}}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleNextAfterCopy(t *testing.T) {
	schedule, err := ParseSchedule("0 6 * * *", "Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	copy := schedule
	got := copy.Next(time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC))
	want := time.Date(2026, 7, 19, 4, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next() = %s, want %s", got, want)
	}
}

func TestParseScheduleAcceptsArgoCronProfile(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{"0 6 * JAN MON-FRI", "* * * * *", "0 0 * * ?", "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly"} {
		schedule, err := ParseSchedule(expression, "Europe/Copenhagen")
		if err != nil {
			t.Fatalf("ParseSchedule(%q) error = %v", expression, err)
		}
		if schedule.Expression != expression {
			t.Fatalf("expression = %q, want %q", schedule.Expression, expression)
		}
	}
}

func TestParseScheduleRequiresTimezone(t *testing.T) {
	t.Parallel()

	if _, err := ParseSchedule("0 6 * * *", ""); err == nil {
		t.Fatal("ParseSchedule() error = nil, want required timezone")
	}
}

func TestParseScheduleRejectsUnsupportedSchedules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cron     string
		timezone string
	}{
		{name: "every descriptor", cron: "@every 1m"},
		{name: "six fields", cron: "0 0 6 * * *"},
		{name: "embedded timezone", cron: "CRON_TZ=UTC 0 6 * * *"},
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

func TestScheduleNextIsIndependentOfHostTimezone(t *testing.T) {
	schedule, err := ParseSchedule("0 6 * * *", "Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	utcAfter := time.Date(2026, 7, 18, 3, 0, 0, 0, time.UTC)
	fixedHost := utcAfter.In(time.FixedZone("host", -8*60*60))
	if got, want := schedule.Next(utcAfter), schedule.Next(fixedHost); !got.Equal(want) {
		t.Fatalf("host timezone changed nominal instant: UTC=%s fixed=%s", got, want)
	}
}

func TestScheduleNextAdvancesNonexistentDSTTime(t *testing.T) {
	schedule, err := ParseSchedule("30 2 * * *", "Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	got := schedule.Next(time.Date(2026, 3, 28, 3, 0, 0, 0, time.UTC))
	want := time.Date(2026, 3, 30, 0, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next() = %s, want nonexistent local match skipped to %s", got, want)
	}
}

func TestScheduleNextRunsRepeatedDSTTimeTwice(t *testing.T) {
	schedule, err := ParseSchedule("30 2 * * *", "Europe/Copenhagen")
	if err != nil {
		t.Fatal(err)
	}
	first := schedule.Next(time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC))
	second := schedule.Next(first)
	wantFirst := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	if !first.Equal(wantFirst) {
		t.Fatalf("first repeated wall time = %s, want earlier instant %s", first, wantFirst)
	}
	wantSecond := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)
	if !second.Equal(wantSecond) {
		t.Fatalf("second repeated wall time = %s, want %s", second, wantSecond)
	}
}
