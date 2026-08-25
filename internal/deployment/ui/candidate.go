package ui

import (
	"net/url"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	g "maragu.dev/gomponents"
	c "maragu.dev/gomponents/components"
	h "maragu.dev/gomponents/html"
)

func CandidatePage(candidate deployment.Candidate, providers ...webpage.Provider) g.Node {
	return candidatePage(candidate, true, false, providers...)
}

// CandidateReviewPage renders the bounded evidence handoff available to a
// reviewer. It intentionally omits the owner-only preview link: reviewers can
// inspect status and diagnostic evidence through the review operation, while
// governed candidate data remains available only to the candidate owner.
func CandidateReviewPage(candidate deployment.Candidate, providers ...webpage.Provider) g.Node {
	return candidatePage(candidate, false, true, providers...)
}

func candidatePage(candidate deployment.Candidate, includePreview, includeReviewEvidence bool, providers ...webpage.Provider) g.Node {
	layout := webpage.Resolve(firstProvider(providers), webpage.Context{
		Active: "dashboards", SectionTitle: "Development", PageTitle: "Private candidate",
	})
	return c.HTML5(c.HTML5Props{
		Title: "Private candidate · " + layout.Presentation.ProductName, Language: "en",
		Head: g.Group{
			h.Link(h.Rel("icon"), h.Href(layout.Assets.URL(layout.Presentation.FaviconPath)), h.Type("image/svg+xml")),
			h.Link(h.Rel("stylesheet"), h.Href(layout.Assets.URL("/static/app.css"))),
			g.If(candidate.Status == deployment.CandidatePreparing,
				h.Meta(g.Attr("http-equiv", "refresh"), h.Content("1")),
			),
		},
		Body: g.Group{
			h.Main(h.Class("min-h-svh bg-app text-fg-default flex items-center justify-center p-6"),
				h.Section(h.Class("w-full max-w-lg rounded-xl border border-border-default bg-canvas-default p-6 shadow-lg"),
					g.Attr("aria-live", "polite"),
					h.H1(h.Class("text-xl font-semibold"), g.Text("Private candidate")),
					h.P(h.Class("mt-3 text-sm text-fg-muted"), g.Text(candidateStatusMessage(candidate.Status))),
					h.Dl(h.Class("mt-5 grid gap-3 text-sm"),
						h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Candidate")), h.Dd(h.Class("font-mono"), g.Text(candidate.ID))),
						h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Status")), h.Dd(g.Text(string(candidate.Status)))),
						g.If(candidate.Revision > 0,
							h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Revision")), h.Dd(h.Class("font-mono"), g.Textf("%d", candidate.Revision))),
						),
						g.If(!candidate.CreatedAt.IsZero(),
							h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Created")), h.Dd(g.Text(candidate.CreatedAt.UTC().Format(time.RFC3339)))),
						),
						g.If(!candidate.UpdatedAt.IsZero(),
							h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Updated")), h.Dd(g.Text(candidate.UpdatedAt.UTC().Format(time.RFC3339)))),
						),
						g.If(!candidate.ExpiresAt.IsZero(),
							h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Expires")), h.Dd(g.Text(candidate.ExpiresAt.UTC().Format(time.RFC3339)))),
						),
						g.If(candidate.FailureReason != "",
							h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Diagnostic code")), h.Dd(h.Class("font-mono"), g.Text(candidate.FailureReason))),
						),
					),
					g.If(includeReviewEvidence, candidateReviewEvidence(candidate)),
					h.Hr(h.Class("my-5 border-border-default")),
					h.P(h.Class("text-sm text-fg-muted"), g.Text(reviewHandoffMessage(includeReviewEvidence))),
					g.If(includeReviewEvidence,
						h.P(h.Class("mt-3 text-sm text-fg-muted"), g.Text(candidateReviewNextAction(candidate.Status))),
					),
					g.If(includePreview,
						h.Div(h.Class("mt-4 flex flex-wrap gap-3"),
							h.A(h.Class("inline-flex items-center rounded-md border border-border-default px-3 py-2 text-sm font-medium hover:bg-canvas-subtle"), h.Href(candidatePreviewHref(candidate.ID)), g.Text("Open exact preview")),
							h.A(h.Class("inline-flex items-center rounded-md border border-border-default px-3 py-2 text-sm font-medium hover:bg-canvas-subtle"), h.Href(candidateReviewHref(candidate.ID)), g.Text("Reviewer handoff")),
						),
					),
				),
			),
		},
	})
}

func candidateReviewEvidence(candidate deployment.Candidate) g.Node {
	return h.Dl(h.Class("mt-5 grid gap-3 border-t border-border-default pt-5 text-sm"),
		h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Project")), h.Dd(h.Class("font-mono"), g.Text(candidate.Scope.ProjectID.String()))),
		h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Environment")), h.Dd(h.Class("font-mono"), g.Text(candidate.Scope.Environment))),
		g.If(candidate.ArtifactDigest != "",
			h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Artifact digest")), h.Dd(h.Class("font-mono break-all"), g.Text(candidate.ArtifactDigest))),
		),
		g.If(candidate.Status == deployment.CandidateReady && candidate.ProvenanceDigest != "",
			h.Div(h.Dt(h.Class("text-fg-subtle"), g.Text("Ready provenance digest")), h.Dd(h.Class("font-mono break-all"), g.Text(candidate.ProvenanceDigest))),
		),
	)
}

func reviewHandoffMessage(review bool) string {
	if review {
		return "Review the exact candidate evidence below. Data preview remains owner-only; production approval and activation remain CLI/API-only."
	}
	return "Share the exact candidate identity and status with a reviewer. Production approval and activation remain CLI/API-only."
}

func candidateReviewNextAction(status deployment.CandidateStatus) string {
	switch status {
	case deployment.CandidatePreparing:
		return "Next step: wait for preparation to finish, then refresh this handoff."
	case deployment.CandidateReady:
		return "Next step: record this evidence and approve the exact candidate through the CLI/API workflow if policy allows."
	case deployment.CandidateFailed:
		return "Next step: resolve the diagnostic code and create a new candidate; this candidate cannot be approved."
	case deployment.CandidateCancelled:
		return "Next step: create a new candidate; this candidate cannot be approved."
	case deployment.CandidateExpired:
		return "Next step: create a new candidate; this candidate cannot be approved."
	default:
		return "Next step: wait for a supported candidate status before taking action."
	}
}

// candidatePreviewHref is deliberately derived only from the opaque candidate
// identity. Candidate status pages must never put owner, project, target, or
// artifact evidence in a shareable URL.
func candidatePreviewHref(candidateID string) string {
	return "/candidates/" + url.PathEscape(strings.TrimSpace(candidateID))
}

func candidateReviewHref(candidateID string) string {
	return candidatePreviewHref(candidateID) + "/review"
}

func candidateStatusMessage(status deployment.CandidateStatus) string {
	switch status {
	case deployment.CandidatePreparing:
		return "LeapView is preparing this private preview. This page will become the governed dashboard runtime when preparation completes."
	case deployment.CandidateReady:
		return "This private candidate is ready. The governed runtime is being attached."
	case deployment.CandidateFailed:
		return "LeapView could not prepare this candidate. Your active dashboards were not changed."
	case deployment.CandidateCancelled:
		return "This private candidate was cancelled and cannot affect active dashboards."
	case deployment.CandidateExpired:
		return "This private candidate expired and cannot affect active dashboards."
	default:
		return "This private candidate is unavailable."
	}
}

func firstProvider(providers []webpage.Provider) webpage.Provider {
	if len(providers) == 0 {
		return nil
	}
	return providers[0]
}
