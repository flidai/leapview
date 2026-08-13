# Homepage design QA

## Evidence

- Source visual truth: `/var/folders/d7/cn8mp80d2ts5hxhgdq4jtp_40000gn/T/codex-clipboard-34418247-e5c4-4534-a612-33b4270cc454.png`
- Source pixels: 267 × 221. The image is a composition and atmosphere reference, not an exact LeapView content mock.
- Browser-rendered implementation: `http://127.0.0.1:8081/`
- Requested adjustment: remove the border, surface fill, shadow, radius, clipping, and padding from the outer desktop section while preserving the internal stage and platform cards.
- Implementation stage screenshot: `.tmp/desktop-stage-crop.png`
- Full comparison input: `.tmp/desktop-stage-comparison.png`
- Pre-change container capture: `.tmp/desktop-section-implementation.png`
- Unboxed implementation capture: `.tmp/desktop-section-unboxed-final.jpg`
- Container comparison input: `.tmp/desktop-section-container-comparison.jpg`
- Simplified notice capture: `.tmp/desktop-notice-simplified-detail.jpg`
- Eyebrow-free desktop capture: `.tmp/homepage-eyebrows-removed-desktop.jpg`
- Eyebrow-free workflow capture: `.tmp/homepage-eyebrows-removed-flow.jpg`
- Eyebrow-free stack capture: `.tmp/homepage-eyebrows-removed-stack.jpg`
- Grouped desktop heading and stage: `.tmp/desktop-grouped-preview-top-final.jpg`
- Grouped stage and platform cards: `.tmp/desktop-grouped-preview-downloads.jpg`
- Download-card detail: `.tmp/desktop-download-cards.png`
- Mobile implementation: `.tmp/mobile-desktop-section.png`
- Desktop viewport: 1440 × 1000 CSS px at browser density 1; stage capture normalized to 1054 × 659 px.
- Unboxed desktop viewport: 1280 × 720 CSS px at browser density 1; final capture normalized to 1054 × 658 px for the pre-change/final comparison.
- Mobile viewport: 390 × 844 CSS px at browser density 1.
- State: homepage, dark color scheme, unsigned alpha preview.

## Full-view comparison

The implementation preserves the reference's key composition: a restrained blue-violet desktop wallpaper, a dark application window inset from the edges, strong depth separation, and no decorative UI competing with the application. LeapView intentionally uses its real packaged Electron capture and its existing site typography rather than reproducing Codex product content.

The requested follow-up removes the parent card around the entire section. The heading, release status, wallpaper stage, and platform cards now sit directly on the homepage canvas. The stage and individual download cards remain contained where their boundaries communicate function.

## Focused comparison

- Fonts and typography: existing LeapView Inter hierarchy remains consistent; the application window uses the real Electron capture, so in-window typography is authentic rather than recreated.
- Spacing and layout rhythm: the application window has balanced wallpaper exposure, a compact title bar, and a 16 px transition into the three-platform download grid. The heading remains 48 px above this grouped download cluster, and the section no longer adds a redundant outer boundary.
- Colors and tokens: all interface styling uses existing semantic tokens. The blue-violet wallpaper is a dedicated generated raster asset rather than a CSS approximation.
- Image quality and assets: the wallpaper is a 1440 × 900 WebP; the Electron capture remains a sharp 1440 × 900 PNG. Apple, Windows, and Linux marks use Font Awesome Free 7.2.0 brand SVGs with their embedded attribution.
- Copy and content: a compact yellow “Early preview” label sits beside the title. The subtitle discloses the unsigned state, names the macOS and Windows publisher warning, and retains the release-evidence link without a separate callout.
- Responsive detail: the 390 px layout has no horizontal overflow, preserves the application-stage composition, and stacks download cards in reading order.
- Accessibility: decorative wallpaper and OS marks have empty alternative text; the application screenshot retains a descriptive alternative; platform actions remain named links with 44 px minimum targets.

## Comparison history

### Pass 1

- P0/P1/P2 findings: none.
- P3: the real LeapView connection form occupies less of its application window than the Codex reference content. This is accepted because preserving an authentic packaged-app screenshot is more valuable than cropping or reconstructing the product UI.
- Browser console errors or warnings: none.
- Primary interaction checks: all three primary download links, the Intel Mac link, and the release-evidence link were present, enabled, and exposed with distinct accessible names. External preview downloads were not activated because the release tag is intentionally unpublished until the protected release workflow runs.

### Pass 2 — outer container removal

- P0/P1/P2 findings: none.
- The pre-change/final comparison confirms that the outer border, panel fill, radius, shadow, clipping, and padding are gone without altering the visual hierarchy inside the section.
- Browser-computed final section styles: transparent background, zero border width, zero radius, no shadow, visible overflow, and zero padding.
- No JavaScript or interaction behavior changed; the existing link and accessibility checks remain valid.

### Pass 3 — simplified preview notice

- P0/P1/P2 findings: none.
- Removed the competing “Unsigned alpha” badge and heading-level verification link.
- Replaced the warning-styled two-column callout with one neutral, single-line notice: “Early preview,” a concise unsigned-installer explanation, and one release-evidence link.
- The notice remains clearly associated with all three platform cards without dominating the download actions.

### Pass 4 — redundant eyebrow removal

- P0/P1/P2 findings: none.
- Removed six repeated eyebrow labels from the homepage and the duplicate “LeapView Desktop” eyebrow from the download page.
- The primary headings and supporting copy preserve section identity without the extra labels, and the existing spacing remains balanced across the desktop, workflow, stack, governance, and final CTA sections.

### Pass 5 — unified desktop download cluster

- P0/P1/P2 findings: none.
- Moved “Early preview” into a compact yellow semantic-warning label aligned with the primary title.
- Folded the unsigned-installer guidance and release-evidence link into the subtitle and removed the standalone notice component.
- Grouped the application stage and platform cards with a 16 px gap, while preserving a 48 px separation from the introductory heading. Browser-computed alignment confirms the title and label share the same vertical center at the desktop viewport.

## Implementation checklist

- [x] Generated wallpaper asset is present and loaded.
- [x] Real Electron screenshot is framed on the wallpaper.
- [x] Apple, Windows, and Linux marks are visible.
- [x] Desktop and mobile layouts have no horizontal overflow.
- [x] Download and verification links retain their release-contract URLs.
- [x] Desktop section sits directly on the homepage canvas without an outer card treatment.
- [x] Unsigned-install guidance appears once in the subtitle, paired with a compact yellow preview label.
- [x] Primary section headings stand on their own without redundant eyebrow labels.
- [x] No actionable P0/P1/P2 differences remain.

final result: passed

---

# Group detail cleanup design QA

## Comparison target

- Source visual truth: `/home/codex/.codex/attachments/79f0a96d-3f44-4d0f-beb1-5daa6ab6708a/codex-clipboard-2886dfac-61c8-4654-aa71-d64ff1635275.png`
- Source dimensions: 1600 × 1542 pixels at density 1.
- Browser-rendered implementation: `/home/codex/.codex/worktrees/a431/leapview/.tmp/design-qa/group-detail-clean-desktop.png`
- Rename interaction: `/home/codex/.codex/worktrees/a431/leapview/.tmp/design-qa/group-detail-rename-modal.png`
- Add-member interaction: `/home/codex/.codex/worktrees/a431/leapview/.tmp/design-qa/group-detail-add-member-modal.png`
- Mobile implementation: `/home/codex/.codex/worktrees/a431/leapview/.tmp/design-qa/group-detail-clean-mobile.png`
- Side-by-side comparison: `/home/codex/.codex/worktrees/a431/leapview/.tmp/design-qa/group-detail-comparison.png`
- Desktop viewport and implementation pixels: 1600 × 1542 CSS pixels at device scale factor 1.
- Mobile viewport and implementation pixels: 390 × 844 CSS pixels at device scale factor 1.
- State: dark theme, editable local `analysts` group with one member and multiple eligible member candidates.

## Evidence review

- Full view: the source captures the pre-cleanup state. The implementation applies the requested delta by removing the duplicate route title, description, and five metric cards, leaving one group identity header and the borderless Overview and Members document.
- Focused interactions: Rename group is now a prefilled modal reached from More actions. Add member is now a section-level action with a focused user-selection modal. Both provide Cancel and close controls, use native dialog semantics, and return to the read-only detail page after dismissal or submission.
- Fonts and typography: existing LeapView Primer-backed typography is unchanged. Removing the duplicate title and metric type removes competing hierarchy while retaining the established page, section, label, and body recipes.
- Spacing and layout rhythm: the detail shell now begins at the route's content origin and uses whitespace plus hairline separators. No route-level metric cards or detail cards remain. Section actions align with their owning headings.
- Colors and visual tokens: the cleanup uses existing panel, text, border, accent-button, backdrop, and focus tokens; no raw colors or new visual vocabulary were introduced.
- Image quality and assets: the screen has no product imagery. Rename, add-member, and close affordances use the existing Lucide icon library and render sharply at both tested densities.
- Copy and content: group identity, provider, workspace, created date, member count, IDs, and membership remain visible. The modal copy states the consequence and object of each action without repeating page-level guidance.
- Responsive behavior: at 390px the identity and header action stack, facts become one-column rows, the Add member action remains beside Members, and the complete member row—including Remove—fits without horizontal scrolling.

## Comparison history

1. The first implementation comparison confirmed the requested desktop cleanup and modal behaviors, but mobile evidence showed the Remove action outside the initially visible member-table columns. This was a P2 action-discoverability issue.
2. The member table gained a responsive intrinsic layout, an end-aligned action column, and breakable email content. The revised 390 × 844 capture shows Member, Email, and Remove together with no component or document overflow.
3. The post-fix combined desktop comparison and focused modal captures found no remaining P0, P1, or P2 differences.

## Interaction and implementation checks

- Route-level page headers: 0.
- Route-level metric cards: 0.
- Detail cards: 0.
- Inline detail forms: 0.
- Tested actions: open/dismiss Rename group, preserve current name, open/dismiss Add member, populate eligible users, and verify the typed rename/add command payloads in DOM tests.
- Desktop and mobile horizontal overflow: none.
- Browser console errors: none.
- Focused TypeScript, Primer alignment, component DOM, admin-page DOM, Go UI, and production asset builds: passed.

## Findings

No actionable P0, P1, or P2 differences remain.

final result: passed

---

# Administration detail shell design QA

## Comparison target

- Source visual truth: `/home/codex/.codex/generated_images/019ffaad-bbad-77e2-8678-e356a9837b3a/exec-551561ad-c7c6-4aa0-9fa5-b4007b47e917.png`
- Source dimensions: 1487 × 1058 pixels.
- User detail implementation: `/tmp/leapview-detail-option1-desktop-v2.png`
- Mobile implementation: `/tmp/leapview-detail-option1-mobile-scrolled.png`
- Group detail implementation: `/tmp/leapview-group-detail-option1-desktop.png`
- Full-view comparison: `/tmp/leapview-detail-comparison.png`
- Focused header and overview comparison: `/tmp/leapview-detail-comparison-focus-v2.png`
- Desktop viewport: 1440 × 1024 CSS pixels at device scale factor 1; captured detail surface: 1104 × 823 pixels.
- Mobile viewport: 390 × 844 CSS pixels at device scale factor 1.
- State: dark theme, local system-managed user and managed group detail routes.

## Evidence review

- Full view: the implementation follows the selected editorial-ledger direction: identity first, then a slim source notice and a continuous document of divided sections. No detail cards remain.
- Focused view: the header is borderless and uses the application surface, a 64px initials avatar, concise source/status badges, and capability-aware actions. The overview facts use a responsive horizontal definition grid.
- Typography: the reference hierarchy is retained while using LeapView's existing Primer-backed type scale and weight tokens. This makes the production view intentionally more compact than the stylized reference.
- Spacing and layout: section hierarchy comes from whitespace and hairline dividers. Desktop facts flow across columns; mobile facts, actions, tables, and sections stack without document overflow.
- Colors and tokens: surfaces, text, borders, accent notice, status badges, and controls all use existing application tokens. No new raw color system was introduced.
- Images and icons: no raster assets were needed. Initials avatars are product UI, while informational and blocking actions use the existing Lucide icon library.
- Copy and content: notices explain whether LeapView or an external identity source owns the record. Local records retain editable operations; managed records remain read-only where appropriate.
- Reuse: users and groups render through the same `renderAdministrationDetailShell` composition, with domain-specific sections and operations supplied as content.

## Comparison history

1. Initial comparison found redundant section descriptions and repeated empty-state labels in Access. These created more visual noise than the selected minimal reference.
2. Removed the redundant descriptions and render Access subheadings only when their tables contain data.
3. The revised combined comparison found no remaining P0, P1, or P2 differences.

## Interaction and responsive checks

- User and group routes load through the shared administration navigation and preserve their back links.
- Capability-aware actions remain wired to the existing update, block/unblock, member, and delete command paths.
- Desktop user detail renders four divided sections; group detail renders two divided sections; both render zero `.detail-card` elements.
- At 390px, the identity header and actions stack, the security section remains reachable, and neither the component nor document has horizontal overflow.
- Browser console errors: none during the final desktop and mobile checks.

## Accepted P3 differences

- LeapView's established typography is denser than the generated design reference; retaining the product tokens keeps this screen consistent with the rest of settings.
- The captured user is the signed-in system user, so unsafe self-blocking is intentionally absent even though the reference illustrates a Block access action.

final result: passed
---

# Personal API token design QA

## Comparison target

- Source visual truth: `/home/codex/.codex/attachments/a3cf84e3-0799-47fa-a22b-0db340ec39e4/codex-clipboard-df1232fa-df9c-463c-bada-d4935712edb5.png`
- Browser-rendered implementation: `/home/codex/.codex/worktrees/e397/leapview/.tmp/design-qa/api-tokens-page-final.png`
- Focused implementation region: `/home/codex/.codex/worktrees/e397/leapview/.tmp/design-qa/api-token-focused-final.png`
- Mobile implementation: `/home/codex/.codex/worktrees/e397/leapview/.tmp/design-qa/api-tokens-mobile-final.png`
- Side-by-side evidence: `/home/codex/.codex/worktrees/e397/leapview/.tmp/design-qa/api-token-comparison-normalized-final.png`
- State: dark theme, workspace scope selected, permission picker open.

## Capture normalization

- Source pixels: 1594 × 810.
- Desktop browser viewport and screenshot: 1440 × 1000 CSS pixels at device scale factor 1.
- Focused implementation crop: 640 × 800 pixels at device scale factor 1.
- Mobile browser viewport and screenshot: 390 × 844 CSS pixels at device scale factor 1.
- The source is a wide GitHub permission panel, while LeapView intentionally preserves its established 640-pixel settings column. The comparison therefore normalizes the interaction region rather than forcing GitHub's full-page width onto LeapView.

## Evidence review

- Full view: the LeapView settings hierarchy remains centered and compact, with token basics, resource access, permissions, and existing credentials in a clear sequence. The open picker stays within the desktop viewport and does not expand the document.
- Focused region: the implementation matches the source pattern of an explicit permission count, an Add permissions trigger, a searchable floating picker, grouped native checkboxes, and concise permission descriptions.
- Typography: all settings and permission labels resolve to the required 14px Primer-backed type recipe. Weight, line height, and muted supporting text preserve the existing LeapView hierarchy.
- Spacing and layout: Primer base-size tokens provide consistent 6/8/12/16/20px rhythm, compact control sizing, panel borders, and restrained radii. The narrower LeapView column is an intentional product constraint.
- Colors and tokens: surfaces, borders, focus, foregrounds, backdrop, and accent states use LeapView aliases backed by Primer primitives; no new raw colors were introduced.
- Images and icons: the reference contains no product imagery. Add, search, remove, and close actions use the application's existing icon system rather than simulated assets.
- Copy and content: repository-specific wording was translated to LeapView's workspace and product scopes. Permission names and descriptions are server-owned and reflect the capabilities the signed-in user may delegate.
- Primary interactions tested: choose scope, search permissions, select and remove permissions, create typed command payload, Escape dismissal with focus return, outside-pointer dismissal, mobile close action, and mobile viewport containment.
- Browser console errors: none during the final desktop capture.

## Comparison history

1. Initial mobile capture found a P2 dismissal issue: the nearly full-height picker relied on tapping a narrow outside margin or using a keyboard Escape key.
2. The picker gained a visible close action and a Primer-backed modal backdrop on compact viewports. The revised 390 × 844 capture shows the close control, bounded panel, background separation, and no horizontal overflow.
3. Final desktop and mobile comparison found no remaining P0, P1, or P2 issues.
4. An in-app browser annotation on 2026-08-12 exposed a P2 desktop overflow issue: implicit grid rows let a long permission list retain its content height, while the panel maximum did not account for its actual vertical position. The panel now uses `auto minmax(0, 1fr)` rows, calculates the available height below its trigger, keeps a 16px viewport gap, and confines scrolling to the permission list. A browser DOM regression at 1440 × 700 with 13 permissions verifies the panel remains inside the viewport, the list has `scrollHeight > clientHeight`, and computed vertical overflow is `auto`.

## Findings

No actionable P0, P1, or P2 visual differences remain. The implementation follows the GitHub interaction model while retaining LeapView's narrower settings layout and Primer visual language.

## Follow-up polish

- P3: a future iteration could replace the native datetime-local field with predefined expiration choices, but this is outside the workspace-and-permissions problem addressed here.

Typography, tokens, icons, and copy are unchanged by the viewport-containment fix. Mobile retains its fixed, viewport-bounded sheet behavior.

final result: passed
