# Public-site Kamal integration — not yet activated

This replaces production-tag polling with a serialized deployment of an admitted
image. The isolated compatibility/failure experiment is recorded in PR #751.
This integration is a separate draft: its complete controller lifecycle has not
been qualified against a disposable host yet. Do not enable it or write a
handover marker based solely on unit tests.

## Routine flow

`main` -> existing site image build/admission -> explicit repository mode gate ->
private, pinned SSH -> host handover/capacity checks -> unique digest-bound tag ->
Kamal `redeploy --skip-push` -> actual container identity and public acceptance ->
record verified version -> exact stopped-attempt/duplicate cleanup -> native prune.

A missing, invalid or `paused` `LEAPVIEW_SITE_DEPLOYMENT_MODE` prevents activation.
There is no fallback write to the old `production` tag. Builds can still complete.
`kamal` mode additionally requires the host's protected handover record, disabled
legacy timer, inactive legacy service, pinned private proxy, verified active/prior
records and measured filesystem capacity. The code does not create readiness.

All routine requests use the `public-site-production` GitHub concurrency group.
`task site:deploy` dispatches that workflow. `task site:access-check` selects the
read-only connectivity operation; it can run while deployment is paused. The old
`scripts/deploy_site.sh` exits without SSH or mutation. The installed legacy
scripts must be updated during handover so that a readiness marker also blocks
accidental legacy execution. Old checked-out scripts cannot be made safe by a
repository variable: drain existing jobs and inspect active operators explicitly.

Kamal remains responsible for pulls, starting containers, health switching,
rollback and image pruning. `host.py` stores bounded deployment records and guards
those operations. It does not delete Docker storage files or prune volumes.

## Access: reuse existing credentials where possible

Operator SSH using the Infisical site key over Tailscale is already verified.
A read-only inventory of all accessible dev/staging/prod folders found the site
SSH key but no Tailscale CI credential. The old infrastructure workflow identity
`7e92da75-ac4f-49f2-8924-4561c3547902` returned identity-not-found; it is not
reused by this integration.
This is sufficient for read-only inspection and operator-run isolated testing.
It is separate from access for GitHub-hosted runners.

Jacob reports existing GitHub access credentials. The current operator token
cannot list all organization secrets; the repository, production environment and
repository-visible organization-secret APIs returned no names. **Confirm the
existing secret names/location/scope before provisioning replacements.** GitHub
never returns stored secret values through these APIs.

The draft action currently supports `SITE_SSH_PRIVATE_KEY` directly, or an
Infisical OIDC identity via `SITE_INFISICAL_IDENTITY_ID`. The latter must read only
`prod:/hetzner-site/operator` and trust the reviewed production workflow on main.
An infrastructure-admin Infisical identity must not be reused without checking
its claims and scope. The draft network path uses `SITE_TS_CLIENT_ID` and
`SITE_TS_AUDIENCE` for a Tailscale OIDC identity with `tag:leapview-site-ci`, scoped
to `100.73.220.23:22`. If an appropriate existing CI identity uses another auth
method, adapt the action to its confirmed identifiers and test it before merge;
these proposed identifier names are not evidence that credentials exist.

The intended OIDC binding is GitHub issuer `https://token.actions.githubusercontent.com`,
subject `repo:flidai/leapview:environment:leapview-site-production`, with claims
`repository=flidai/leapview`, `ref=refs/heads/main` and
`workflow_ref=flidai/leapview/.github/workflows/site-deploy.yml@refs/heads/main`.
Review effective tailnet grants, including existing broad rules. No new public
SSH firewall access is required. The temporary CI node still uses ordinary
OpenSSH with the reviewed fingerprint in `deploy/hetzner-site/ssh-host-key.sha256`.

## State and failure behavior

Migration must create a root-owned mode-0700 directory
`/var/lib/leapview-site/kamal`. `ready.json` requires schema 1,
`controller: "kamal"`, `handover_verified: true` and a `capacity` entry for each
backing path reported by the read-only inventory. Each capacity entry contains
positive measured `candidate_peak_bytes`, explicit `reserve_bytes`, and
`reserve_inodes`. These are remaining-free-space thresholds before a pull;
separate paths on one filesystem are checked against the same shared free space.
**Do not populate these with guessed constants.** Measure admitted site image
pull/extraction/update peaks and retain reserve for the OS, Caddy and logs.

`state.json` has schema 1, `active`, optional distinct `prior`, optional `pending`,
`maintenance_pending` and a `records` map keyed by full digest-derived version.
Each record binds source revision, admitted index, platform and config digests,
Kamal 2.12.0, runtime contract, release metadata, admission evidence and the actual
local image ID. Only public-accepted records are marked `verified`. Transport
paths and credentials are excluded; a fresh runner generates its own SSH config.
Unknown runtime/tooling contracts are refused rather than silently using new
settings for an old rollback.

An ordinary failed candidate restores the saved active image/configuration and
checks both the container and public routes. Failed recovery leaves `pending`,
blocking new pulls. A lost acceptance reply is resolved by reading actual host
state; the runner refuses to guess a rollback if the committed state changed.
Post-acceptance cleanup failure leaves the new version live and
`maintenance_pending`, blocking another pull until recovery.

Recovery after a killed runner or lost connection is an operator procedure:
pause/drain the workflow, inspect the actual proxy/container and saved records,
verify the prior lock owner is dead, and recover the selected verified version.
Never blindly release a Kamal lock, clear pending state, or run broad pruning.
This draft deliberately does not provide an automatic stale-lock reset.

## Merge and migration gates

1. Finish disposable-host integration tests for this exact adapter, including
   interruptions, public acceptance failures, offline rollback and maintenance
   failures. The earlier trial proves mechanisms, not this new implementation.
2. Complete required CI/review and confirm the existing GitHub credential names
   and scope. Establish the read-only hosted-runner route.
3. Set the repository mode to `paused` and drain every old production workflow
   before merging. Old workflow runs do not honor the new variable. Verify both
   manual and push events cause no activation/tag promotion while paused.
4. Inventory live/rollback/foreign images, Caddy config/volumes and actual physical
   storage. Disable/drain the old controller under its existing deployment locks.
   Remove only explicitly inventoried obsolete legacy site image references;
   preserve all live/rollback/container references. Do not use system/volume prune.
5. Bootstrap pinned Kamal proxy and the production-qualified candidate privately;
   preserve the old Compose app for migration rollback. Rehearse Caddy's network
   and upstream change with its admin API disabled, measuring any interruption.
6. Verify HTTPS, www, build identity, health/readiness, docs, release metadata and
   assets. Rehearse return to Compose and restoration to Kamal. Only then write
   the handover and verified state records. Keep automation paused until ready.
7. Enable `kamal`, verify two routine deployments, rehearse recorded local
   rollback and restoration, and measure retained storage. Remove migration-only
   extra containers/images after acceptance. Keep one application controller.

The manual `rollback` operation uses the same concurrency group and mode/host
gates, skips image publication and registry admission, and selects only the
recorded verified prior version. It uses its saved admission/runtime evidence
and local image; it does not depend on the registry being online.

No production cleanup, migration, credential creation or mode change has been
performed by this PR. Public DNS, CFO demo and application releases are out of scope.

## Validation

Run `python3 -m unittest discover -s deploy/kamal-site -p 'test_*.py'`,
`go test ./deploy/hetzner-site ./internal/app/securitypolicy ./internal/app/tools/securitysource`,
and actionlint on `site-deploy.yml`. `task deploy:check` includes the Python tests.
Kamal configuration parsing and read-only SSH inventory have been checked.
Enabling the mode locally against the unmigrated host was also tested: missing
handover state stopped the command before admission, tagging, pulling or cleanup.

Ruby dependencies are pinned, covered by Dependabot, and scanned by the existing
pinned Trivy source-security tool with a separate vulnerability scan of Ruby lock
roots. These checks must pass in hosted CI before production credentials are used.

Sources: [Kamal redeploy](https://kamal-deploy.org/docs/commands/redeploy/),
[Tailscale GitHub Action](https://tailscale.com/docs/integrations/github/github-action).

Local full CI was attempted after initializing generated outputs. It reached
PostgreSQL conformance and failed because the shared Docker daemon has no
`docker0` bridge. Focused tests passing do not substitute for that required check.
