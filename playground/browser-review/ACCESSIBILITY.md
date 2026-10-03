# Accessibility review findings

The opt-in sweep reports findings instead of suppressing rules or accepting an
allowlist. These observations from 2026-10-03 need component-level review; they
are not an accessibility certification or a change to repository CI policy.

| Default example | Theme | Axe rule requiring review |
| --- | --- | --- |
| `controls/buttons` | Dark | `color-contrast` |
| `controls/multiselect` | Both | `aria-required-children` |
| `charts/map` | Both | `target-size` |
| `tables/record` | Both | `aria-prohibited-attr` |
| `tables/windowed`, `tables/data-preview`, `tables/data-explore` | Both | `aria-required-parent` |
| `content/code-editor` | Light | `color-contrast` |
| `content/config-viewer` | Both | `aria-required-children` |

The sweep visited all 71 routes in each theme at 1440 × 1000, with no browser
errors or unexpected backend/external requests. Eight examples had violations in
each theme (nine distinct examples across themes). A selector collision in the icon
picker was fixed in the review helper; a focused rerun in both themes produced
zero violations, one incomplete rule, and no browser errors or unexpected requests.

Run the commands in [README.md](README.md) for current results and exact element
targets. The HTML report attaches each route's violations and incomplete rules.
The command exits nonzero while findings remain. No production accessibility
attributes, design tokens, or third-party components were changed in this update.
Keyboard, open overlays, alternate fixture states and screen readers still need
manual review alongside the automated default-state sweep.
