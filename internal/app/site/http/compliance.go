package http

import (
	"github.com/flidai/leapview/pkg/pagestream"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// compliancePage is intentionally a reviewed public summary, not an evidence
// registry or a representation of managed-service launch approval.
func compliancePage(metadata sitePageMetadata) g.Node {
	head := append(siteHead(metadata), h.Link(h.Rel("stylesheet"), h.Href("/static/compliance.css")))
	return pagestream.RenderPage(pagestream.PageSpec{
		Title:             metadata.title,
		HTMLAttrs:         siteHTMLAttrs(),
		Head:              head,
		MainAttrs:         []g.Node{h.Class("site-page")},
		DatastarScriptURL: siteDatastarScriptURL,
		UpdatesURL:        "/updates",
		Body: []g.Node{
			h.A(h.Class("skip-link"), h.Href("#main-content"), g.Text("Skip to content")),
			siteHeader(false, metadata.showcase),
			h.Article(h.ID("main-content"), h.Class("site-compliance"),
				h.Header(h.Class("site-compliance-hero"),
					h.P(h.Class("site-compliance-eyebrow"), g.Text("Public assurance / Current status")),
					h.H1(g.Text("Compliance & Security")),
					h.P(h.Class("site-compliance-lede"), g.Text("This page describes LeapView's current security and assurance posture using evidence-backed status categories. It is a transparency resource, not a certification, audit opinion, or statement of universal regulatory compliance.")),
					h.Nav(g.Attr("aria-label", "On this page"), h.Class("site-compliance-jump"),
						h.A(h.Href("#implemented"), g.Text("Implemented")),
						h.A(h.Href("#qualification"), g.Text("Qualification-tested")),
						h.A(h.Href("#pending"), g.Text("Pending")),
						h.A(h.Href("#certifications"), g.Text("Certifications")),
					),
				),
				h.Section(h.Class("site-compliance-state"), g.Attr("aria-labelledby", "assurance-state"),
					h.Div(
						h.P(h.Class("site-compliance-kicker"), g.Text("01 / Assurance state")),
						h.H2(h.ID("assurance-state"), g.Text("Current assurance state")),
						h.P(g.Text("Technical controls and qualification work are progressing. Final service scope, provider, legal and privacy approvals, operational commitments, and management approval remain separate decisions.")),
					),
					h.Div(h.Class("site-compliance-state-value"),
						h.P(g.Text("Managed-service launch")),
						h.Strong(g.Text("Pending approval")),
					),
				),
				h.Section(h.ID("implemented"), h.Class("site-compliance-section"), g.Attr("aria-labelledby", "implemented-heading"),
					complianceSectionIntro("02 / In the product", "implemented-heading", "Implemented capabilities", "These describe repository-level product behavior. They do not establish that a managed deployment has been qualified or approved."),
					h.Div(h.Class("site-compliance-cards"),
						complianceCard("Implemented", "Application access", "LeapView implements authenticated application surfaces and role-, grant-, and policy-based permission checks for supported operations.", "/docs/security/authorization", "Read access documentation"),
						complianceCard("Implemented", "Audit capture", "LeapView records selected administrative and security events, and governed query events, with project and actor context.", "/docs/security/audit", "Read audit documentation"),
						complianceCard("Implemented", "Credential revocation", "LeapView implements controls to revoke principal sessions, API tokens, and service-principal secrets.", "/docs/security/tokens", "Read token documentation"),
					),
				),
				h.Section(h.ID("qualification"), h.Class("site-compliance-section"), g.Attr("aria-labelledby", "qualification-heading"),
					complianceSectionIntro("03 / Bounded exercises", "qualification-heading", "Qualification-tested capabilities", "A test result applies to its tested scenario, not automatically to an operating customer service."),
					h.Div(h.Class("site-compliance-qualification"),
						h.P(h.Class("site-compliance-status"), g.Text("Qualification-tested")),
						h.H3(g.Text("Coordinated recovery handoff")),
						h.P(g.Text("Qualification testing has exercised coordinated PostgreSQL and object-storage restore handoff and a separate downstream consumer after the producing process exits, using bounded test resources. This does not establish a production provider, replacement-host activation, or a recovery-time commitment.")),
					),
				),
				h.Section(h.ID("pending"), h.Class("site-compliance-section site-compliance-pending"), g.Attr("aria-labelledby", "pending-heading"),
					complianceSectionIntro("04 / Remaining work", "pending-heading", "Pending verification & approval", "The following are open qualification or decision areas; their presence here is not a delivery promise or deadline."),
					h.Div(h.Class("site-compliance-pending-grid"),
						compliancePending("Pending approval", "Service scope & providers", "Managed provider, region, service boundary, and contractual scope."),
						compliancePending("Pending verification", "Managed identity", "Managed-service identity, MFA, and privilege enforcement."),
						compliancePending("Pending verification", "Privacy lifecycle", "Retention, customer-rights operation, customer exit, and deletion across the proposed service."),
						compliancePending("Pending verification", "Managed configuration", "Target configuration, egress, and isolation on the proposed deployment."),
						compliancePending("Pending verification", "Operations & assurance", "Incident and support operation, independent penetration testing, and workforce and supplier assurance."),
						compliancePending("Pending approval", "Final review", "Legal, privacy, security, and management launch decisions."),
					),
				),
				h.Section(h.ID("certifications"), h.Class("site-compliance-section"), g.Attr("aria-labelledby", "certifications-heading"),
					complianceSectionIntro("05 / Claims boundary", "certifications-heading", "Certifications & regulatory status", "Standards and laws require scope-specific assessment. This page is not that assessment."),
					h.Div(h.Class("site-compliance-claims"),
						h.P(h.Class("site-compliance-status"), g.Text("Not currently claimed")),
						h.Ul(
							h.Li(g.Text("ISO/IEC 27001 certification is not currently claimed.")),
							h.Li(g.Text("No universal GDPR compliance claim is made.")),
							h.Li(g.Text("No universal NIS2, DORA, or CRA compliance claim is made.")),
							h.Li(g.Text("No CIS certification or compliance claim is made.")),
						),
						h.P(g.Text("Conditional regulatory applicability is determined separately and is not established by this page.")),
					),
				),
				h.Section(h.Class("site-compliance-contact"), g.Attr("aria-labelledby", "contact-heading"),
					h.Div(
						h.P(h.Class("site-compliance-kicker"), g.Text("06 / Further information")),
						h.H2(h.ID("contact-heading"), g.Text("Documentation & security reporting")),
						h.P(g.Text("Explore the public documentation and source repository. Please report a suspected vulnerability privately.")),
					),
					h.Div(h.Class("site-compliance-links"),
						h.A(h.Href("/docs"), g.Text("Documentation")),
						h.A(h.Href("https://github.com/flidai/leapview"), g.Text("Public repository")),
						h.A(h.Href("https://github.com/flidai/leapview/security/advisories/new"), g.Text("Report a security issue privately")),
					),
				),
				h.P(h.Class("site-compliance-reviewed"),
					g.Text("Last reviewed: "),
					g.El("time", g.Attr("datetime", "2026-09-24"), g.Text("24 September 2026")),
					g.Text(". Public statements are reviewed and updated when the underlying evidence, service scope, or approval state changes."),
				),
			),
			siteFooter(),
		},
	})
}

func complianceSectionIntro(kicker, headingID, title, summary string) g.Node {
	return h.Div(h.Class("site-compliance-section-intro"),
		h.P(h.Class("site-compliance-kicker"), g.Text(kicker)),
		h.H2(h.ID(headingID), g.Text(title)),
		h.P(g.Text(summary)),
	)
}

func complianceCard(status, title, description, href, linkLabel string) g.Node {
	return h.Div(h.Class("site-compliance-card"),
		h.P(h.Class("site-compliance-status"), g.Text(status)),
		h.H3(g.Text(title)),
		h.P(g.Text(description)),
		h.A(h.Href(href), g.Text(linkLabel)),
	)
}

func compliancePending(status, title, description string) g.Node {
	return h.Div(h.Class("site-compliance-pending-item"),
		h.P(h.Class("site-compliance-status"), g.Text(status)),
		h.H3(g.Text(title)),
		h.P(g.Text(description)),
	)
}
