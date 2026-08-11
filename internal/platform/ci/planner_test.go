package ci

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanChanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   Input
		changes []Change
		want    Jobs
		reason  string
		audit   bool
	}{
		{
			name:    "repository markdown",
			input:   Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{Status: "M", Paths: []string{"README.md"}}},
			want:    Jobs{Docs: true},
			reason:  "repository documentation",
		},
		{
			name:    "published documentation",
			input:   Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{Status: "M", Paths: []string{"docs/articles/start/installation.md"}}},
			want:    Jobs{Docs: true, SiteImage: true},
			reason:  "published documentation or site",
		},
		{
			name:  "frontend report source",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"web/components/dashboard/dashboard-page.ts"},
			}},
			want: Jobs{
				FrontendPrepare: true,
				Frontend:        []string{"core", "reports"},
				ProductionImage: true,
			},
			reason: "frontend",
		},
		{
			name:  "shared frontend source",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"web/components/shared/datastar-lit.ts"},
			}},
			want: Jobs{
				Prepare:         true,
				Frontend:        []string{"core", "reports", "chat", "workspace"},
				UIRouteQA:       true,
				Docs:            true,
				SiteImage:       true,
				ProductionImage: true,
			},
			reason: "frontend",
		},
		{
			name:  "frontend test only",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"web/components/chat/chat-page.dom.test.ts"},
			}},
			want: Jobs{
				FrontendPrepare: true,
				Frontend:        []string{"core", "chat"},
			},
			reason: "frontend tests",
		},
		{
			name:  "production go",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"internal/analytics/query/planner.go"},
			}},
			want:   fullGoJobs(),
			reason: "Go/backend",
		},
		{
			name:  "non app go test only",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"internal/analytics/query/planner_test.go"},
			}},
			want: Jobs{
				Prepare:  true,
				GoMatrix: []GoShard{{Name: "packages"}},
			},
			reason: "Go tests",
		},
		{
			name:  "app go test only",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"internal/app/router_test.go"},
			}},
			want: Jobs{
				Prepare:  true,
				GoMatrix: appGoShards(),
			},
			reason: "Go tests",
		},
		{
			name:  "app subpackage go test uses packages shard",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"internal/app/cli/serve_test.go"},
			}},
			want: Jobs{
				Prepare:  true,
				GoMatrix: []GoShard{{Name: "packages"}},
			},
			reason: "Go tests",
		},
		{
			name:  "site app subpackage test includes packages and site lanes",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"internal/app/site/http/server_test.go"},
			}},
			want: Jobs{
				Prepare:   true,
				Docs:      true,
				GoMatrix:  []GoShard{{Name: "packages"}},
				SiteImage: true,
			},
			reason: "Go tests",
		},
		{
			name:    "generator implementation forces full",
			input:   Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{Status: "M", Paths: []string{"internal/app/tools/configgen/main.go"}}},
			want:    FullJobs(),
			reason:  "cross-cutting build or contract input",
		},
		{
			name:    "CI planner CLI forces full",
			input:   Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{Status: "M", Paths: []string{"internal/app/tools/ciplan/main.go"}}},
			want:    FullJobs(),
			reason:  "cross-cutting build or contract input",
		},
		{
			name:    "agent contract generator forces full",
			input:   Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{Status: "M", Paths: []string{"internal/agent/contracts/generate/main.go"}}},
			want:    FullJobs(),
			reason:  "cross-cutting build or contract input",
		},
		{
			name:  "deployment",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"deploy/hetzner/main.tf"},
			}},
			want:   Jobs{DeploymentContracts: true},
			reason: "deployment",
		},
		{
			name:  "compose deployment",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"deploy/compose/qualification/authoring-worker.mjs"},
			}},
			want: Jobs{
				ProductionImage:     true,
				DeploymentContracts: true,
			},
			reason: "production deployment",
		},
		{
			name:  "host deployment",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"deploy/host/bootstrap-ubuntu.sh"},
			}},
			want: Jobs{
				ProductionImage:     true,
				DeploymentContracts: true,
			},
			reason: "production deployment",
		},
		{
			name:  "runtime project",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "M",
				Paths:  []string{"dashboards/workspaces/sales/workspace.yaml"},
			}},
			want: Jobs{
				Prepare:         true,
				GoMatrix:        allGoShards(),
				UIRouteQA:       true,
				ProductionImage: true,
			},
			reason: "runtime project",
		},
		{
			name:  "mixed union",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{
				{Status: "M", Paths: []string{"docs/articles/start/installation.md"}},
				{Status: "M", Paths: []string{"deploy/hetzner/main.tf"}},
			},
			want: Jobs{
				Docs:                true,
				SiteImage:           true,
				DeploymentContracts: true,
			},
			reason: "deployment, published documentation or site",
		},
		{
			name:  "rename classifies old and new paths",
			input: Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{
				Status: "R100",
				Paths: []string{
					"web/components/chat/old.ts",
					"docs/articles/new.md",
				},
			}},
			want: Jobs{
				FrontendPrepare: true,
				Frontend:        []string{"core", "chat"},
				ProductionImage: true,
				Docs:            true,
				SiteImage:       true,
			},
			reason: "frontend, published documentation or site",
		},
		{
			name:    "unknown forces full",
			input:   Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{Status: "M", Paths: []string{"mystery/file.xyz"}}},
			want:    FullJobs(),
			reason:  "unknown path: mystery/file.xyz",
		},
		{
			name:    "workflow forces full",
			input:   Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{Status: "M", Paths: []string{".github/workflows/ci.yml"}}},
			want:    FullJobs(),
			reason:  "cross-cutting build or contract input",
		},
		{
			name:    "label forces full",
			input:   Input{Event: "pull_request", PullRequestNumber: 1, Labels: []string{"ci:full"}},
			changes: []Change{{Status: "M", Paths: []string{"README.md"}}},
			want:    FullJobs(),
			reason:  "ci:full label",
		},
		{
			name:    "main push forces full",
			input:   Input{Event: "push"},
			changes: nil,
			want:    FullJobs(),
			reason:  "push event",
		},
		{
			name:    "manual run forces full",
			input:   Input{Event: "workflow_dispatch"},
			changes: nil,
			want:    FullJobs(),
			reason:  "workflow_dispatch event",
		},
		{
			name:    "deterministic audit forces effective full",
			input:   Input{Event: "pull_request", PullRequestNumber: 10},
			changes: []Change{{Status: "M", Paths: []string{"README.md"}}},
			want:    FullJobs(),
			reason:  "20% deterministic PR audit",
			audit:   true,
		},
		{
			name:    "unusual known filename remains selective",
			input:   Input{Event: "pull_request", PullRequestNumber: 1},
			changes: []Change{{Status: "M", Paths: []string{"docs/articles/file with spaces.md"}}},
			want:    Jobs{Docs: true, SiteImage: true},
			reason:  "published documentation or site",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := PlanChanges(tt.input, tt.changes)
			if !reflect.DeepEqual(got.Effective, tt.want) {
				t.Fatalf("effective jobs = %#v, want %#v", got.Effective, tt.want)
			}
			if got.Reason != tt.reason {
				t.Fatalf("reason = %q, want %q", got.Reason, tt.reason)
			}
			if got.Audit != tt.audit {
				t.Fatalf("audit = %v, want %v", got.Audit, tt.audit)
			}
		})
	}
}

func TestFullJobsIncludesIndependentDocumentationGate(t *testing.T) {
	t.Parallel()

	plan := PlanChanges(Input{Event: "workflow_dispatch"}, nil)
	if !plan.Effective.Docs {
		t.Fatal("full CI omits the independent documentation and public-site gate")
	}
	if !plan.Effective.Prepare || plan.Effective.FrontendPrepare {
		t.Fatalf(
			"full CI generation jobs = prepare %v, frontend-prepare %v; full preparation must subsume the selective frontend preparation job",
			plan.Effective.Prepare,
			plan.Effective.FrontendPrepare,
		)
	}
}

func TestParseNameStatusZ(t *testing.T) {
	t.Parallel()

	input := []byte("M\x00README.md\x00R100\x00old name.md\x00docs/new name.md\x00D\x00site/old.ts\x00")
	got, err := ParseNameStatusZ(input)
	require.NoError(t, err)
	want := []Change{
		{Status: "M", Paths: []string{"README.md"}},
		{Status: "R100", Paths: []string{"old name.md", "docs/new name.md"}},
		{Status: "D", Paths: []string{"site/old.ts"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestParseNameStatusZRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	if _, err := ParseNameStatusZ([]byte("R100\x00only-old\x00")); err == nil {
		t.Fatal("expected malformed rename to fail")
	}
}
