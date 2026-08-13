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
