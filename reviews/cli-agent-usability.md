# CLI agent usability trials

Observed on 6 October 2026. Ganesh requested agents instead of human sessions. Two agents worked independently in private environments; neither changed the shared daemon or repository. These observations cover a source-development binary, not a public release, successful preview, or real deployment.

## Binary identity

The initial binary reported `development=true`, `dirty=false`, revision `4418d05c13fca2e17e0db4272da2d439598f21cf`, with SHA-256 `956fe274e5ffa90f96f358ecb3fd6185c00afcbfa26da7d9b8f6ac262d8678cd`.

The help/guidance recheck used development revision `8e1f0217c0a88bc291daee5d69d01c69d03a409a`, SHA-256 `9e173a53ecdb0b48b5e762ac07f07627e37704b2cb215059ddff6e80b8a83727`. Subsequent changes record evidence and improve the compilation-failure remedy; these identities must not be represented as released artifacts.

## Independent journeys

| Step | New author | Teammate with a fresh checkout |
| --- | --- | --- |
| Discover workflow | Used only the executable’s help and `--llms`; both exited 0 | Used a tracked-file archive and public docs; help/guide exited 0 despite malformed ambient target/profile and empty PATH |
| Initialize sample | Succeeded in a new private directory | Succeeded in a private directory |
| Validate sample | Complete JSON stdout, empty stderr, exit 0 | Complete JSON stdout, empty stderr, exit 0 with `--no-input` |
| Diagnose prerequisites | Doctor reported missing Docker and runtime package, exit 1; dependent checks skipped | Doctor separated passing source/profile/credential checks from failed runtime prerequisites |
| Break authored YAML | Changed `connection` to `conection`; error named source file, line 8, and `spec.connection` | Changed `spec.semanticModel` to `missing_metrics`; error named dashboard file, resource, field, and missing reference |
| Repair | Restored field; validation returned success, exit 0 | Restored reference; validation returned success, exit 0 |
| Attempt preview | `dev` failed before runtime startup with Docker hidden from PATH | `dev --once --no-browser --format json --no-input` failed without prompting; stdout empty, JSON diagnostic on stderr, exit 1 |
| Interpret delivery | Guide distinguished pending confirmation/approval (3), indeterminate (4), and confirmed activation (0) | Same interpretation from guide/help; no target contacted or delivery state created |

Docker absence was simulated using private empty PATH directories. The host has Docker Engine and Compose 2.40.3; root’s full-CI attempt could not provision container networking because `docker0` is unavailable. The source-development candidate genuinely lacks an installed matching `local-runtime` package. Neither trial produced a preview URL or observed pending/active target state.

## Friction patched

* The new author pre-created the empty destination and `init` correctly refused it. Command help, examples, and the overview now explicitly require a directory that does not already exist. The agent checked the revised help and confirmed the ambiguity was resolved.
* The teammate expected every JSON payload on stdout. The approved contract keeps domain results on stdout and other failures on stderr. Offline guidance now explicitly explains JSON error diagnostics, separate stream capture, and exit-status handling. The agent checked the revised guide offline with malformed environment and empty PATH; exit 0 and empty stderr.
* Doctor’s generic compilation failure gave less detail than validation. Its remedy now directs the author to `leapview validate` with the same source root for file and field diagnostics.

## Evidence retained locally

The exact commands, stdout/stderr, exits, and detailed reports remain under `/home/codex/.cache/leapview-cli-work/usability-new-author/` and `/home/codex/.cache/leapview-cli-work/usability-fresh-checkout/`. These local paths are execution artifacts, not portable release evidence. The table records the durable observations for review.

Source discovery, initialization, diagnostic repair, headless framing, and the two guidance fixes were observed. Exact public archive/platform qualification, preview lifecycle and timing measurements, credential-store integration, and real deployment approval/activation remain unobserved in these trials.
