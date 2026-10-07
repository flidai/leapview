# Accessibility review results

The opt-in sweep reports findings without suppressing rules or accepting an
allowlist. On 2026-10-05, all 71 default examples passed in light and dark themes:
142 scans with no violations, browser errors, or unexpected backend/external
requests. The existing eight screenshot comparisons also passed unchanged.

The follow-up fixes live in shared production components: record-table icon and
nested-control semantics, windowed-table structure and virtual row positions,
checkbox-group and configuration-disclosure semantics, danger-button contrast,
editor syntax contrast, and map range targets. Existing theme tokens and
supported native controls supply their styling and behavior.

Keyboard regressions cover nested table actions and links, checkbox selection,
configuration expansion/filtering, and map range endpoints/dragging. Danger
buttons were also checked at rest, hover and keyboard focus in both themes.

An earlier concurrent run was interrupted by Chromium `ERR_NETWORK_CHANGED`
while backend-service tests changed the host network. The complete sweep passed
after those tests stopped; request/error guards and timeout limits were retained.

Run the commands in [README.md](README.md) for current results and attached
per-route reports, including incomplete rules. These results cover rendered
default DOM in the repository's Chromium runner. Hidden overlays, alternate
states, canvas/WebGL meaning, screen-reader usability, and other browser engines
still need manual review; this is not an accessibility certification.
