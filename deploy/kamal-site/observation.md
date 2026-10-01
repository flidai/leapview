# Public-site observation and acceptance

These read-only tools make the observation repeatable from an operator machine.
They do not deploy, prune images, reboot the host, change DNS, close PRs, or enable
automatic activation. Use the [deployment runbook](README.md) to establish active
B, retained A, qualified reserves, completed recovery checks, and no unresolved
ownership before starting. A failed/interrupted observation requires investigation
and a fresh full interval after a new start smoke; preserve each failed run.
Restart the interval after any deployment, rollback or
corrective production mutation. A rejected setup/preflight check may be corrected
while the unchanged observer is still running; it does not itself invalidate that
observation.

## Inputs and start smoke

Use protected operator storage and the existing pinned SSH identity, host-key
fingerprint, and self-contained SSH config. The config must explicitly select
the identity, `User root`, `IdentitiesOnly yes`, `BatchMode yes`, strict host-key
checking, port 22, and the reviewed known-host file. `IdentityFile` and
`UserKnownHostsFile` must be absolute literal paths; tilde, environment, percent,
and working-directory-relative paths are rejected so scheduled runs do not
depend on their current directory. Agent credentials, proxy
commands/jumps, `Include`, and `Match` configuration are not supported.

Set these shell variables to verified operator inputs before using the commands:

| Variable | Meaning |
| --- | --- |
| `PRIVATE` | Existing operator-owned directory with permissions `0700`. |
| `SITE_TARGET` | Explicit hostname or IP resolved by the supplied SSH config. |
| `SITE_SSH_CONFIG`, `SITE_HOST_FINGERPRINT` | Protected config and fingerprint files. |
| `RECORD_B` | Protected admitted B record, including the expected public release. |
| `VERSION_A`, `LOCAL_IMAGE_A`, `LOCAL_IMAGE_B` | Verified distinct retained A/B identities. |
| `MIN_FREE_BYTES`, `MIN_FREE_INODES` | Qualified thresholds, no lower than host reserves. |
| `PUBLIC_MANIFEST`, `DESKTOP_MANIFEST` | Exact protected release manifests from B. |
| `BUN` | Explicit path to the tested Bun executable. |

From the repository root, assign new run/bundle paths. Both commands refuse to
reuse existing output directories. Keep logs and artifacts in protected storage,
outside Git. The start smoke must finish within five minutes before observer
start; its file timestamp and exact input hashes are preserved by preparation.
Do not change production between that smoke and observation start.

```sh
set -euo pipefail
umask 077
RUN="$PRIVATE/run-$(date -u +%Y%m%dT%H%M%SZ)"
GATE="${RUN}-acceptance"
SMOKE="$PWD/scripts/public_site_smoke.ts"
OBSERVER="$PWD/deploy/kamal-site/observe.py"

sha256sum "$SMOKE" "$PUBLIC_MANIFEST" "$DESKTOP_MANIFEST" > "${RUN}.start-smoke.sha256"
LEAPVIEW_PUBLIC_RELEASE_MANIFEST="$PUBLIC_MANIFEST" \
LEAPVIEW_DESKTOP_RELEASE_MANIFEST="$DESKTOP_MANIFEST" \
"$BUN" run "$SMOKE" > "${RUN}.start-smoke.log" 2>&1
```

## Observe and verify launch

The schedule is fixed: 24 hours, public probes every 60 seconds, host inspection
every 900 seconds, and a 15-second sample/start-lateness budget. There are no
retries that discard failures. The three public probes run concurrently and
record individual and total durations. A successful run requires 1,441 public
and 97 host samples. Host checks cover exact active/prior images and containers,
restart counts, private proxy/Caddy topology, qualified filesystem reserves,
disabled updater, and absence of pending work/ownership/locks.

Monotonic elapsed time controls cadence and the full 24-hour qualification. The
observer records the precise wall-clock origin and each sample/end wall-clock
reading alongside UTC timestamps rounded down to seconds. Wall-clock corrections
of up to five seconds are allowed and checked against monotonic elapsed time, so
an observed UTC timestamp can fall a few seconds before or after its nominal
schedule without changing the interval qualification. Larger divergence fails
the run.

```sh
nohup python3 -B "$OBSERVER" run \
  --target "$SITE_TARGET" --base-url https://leapview.dev \
  --record "$RECORD_B" --prior-version "$VERSION_A" \
  --active-image-id "$LOCAL_IMAGE_B" --prior-image-id "$LOCAL_IMAGE_A" \
  --min-free-bytes "$MIN_FREE_BYTES" --min-free-inodes "$MIN_FREE_INODES" \
  --ssh-config "$SITE_SSH_CONFIG" --fingerprint-file "$SITE_HOST_FINGERPRINT" \
  --output-dir "$RUN" > "${RUN}.stdout.log" 2>&1 &
task site:observe -- status --output-dir "$RUN"
```

The first status may race initial file creation. Inspect it again after launch
and require `running`, at least one public and host sample, and no failure before
preparing acceptance. `status` validates PID/start tick/boot ID and source pins;
a dead or zombie process is interrupted. PID alone is insufficient. It does not
resume an interrupted run or count unobserved time. Preserve `summary.json`,
`samples.jsonl`, `process.json`, input/source hashes, and boundary smoke logs.
Use `status` regularly; these tools do not configure push notifications.

## Freeze and preflight the final check

Prepare while the observer is running with its first sample recorded. This
validates the start smoke against B's admitted release and pins the observer
source, process, run identity, and operator inputs. It copies the smoke script,
observer, acceptance runner, manifests, start log/hashes, and Bun into a protected
bundle. The scripts are `0600` and Bun is `0700`; a normal `0664` source checkout
therefore works without weakening the execution-input protection. Later edits
to the original checkout do not invalidate these frozen tools.

```sh
task site:acceptance -- prepare \
  --run-dir "$RUN" --observer-source "$OBSERVER" --smoke-script "$SMOKE" \
  --public-manifest "$PUBLIC_MANIFEST" --desktop-manifest "$DESKTOP_MANIFEST" \
  --start-smoke-log "${RUN}.start-smoke.log" \
  --start-smoke-hashes "${RUN}.start-smoke.sha256" \
  --bun "$BUN" --output-dir "$GATE" \
  --alias http://leapview.dev --alias https://www.leapview.dev
python3 -B "$GATE/acceptance.py" preflight --bundle "$GATE"
```

`preflight` creates no receipt, runs no end smoke, and leaves rollout acceptance
pending. Require it to pass in the actual scheduler environment before queuing a
job. Schedule the next command at least 60 seconds after the returned
`expected_end`, allowing the final host sample, summary write, and observer exit
to finish. Check that observer status is `health_storage_passed` before a manual
attempt. An early invocation fails and consumes the one-shot attempt; it does
not retry automatically. Retain the job payload and environment in protected evidence. The
tools intentionally do not install or alter a scheduler.

```sh
python3 -B "$GATE/acceptance.py" run --bundle "$GATE"
python3 -B "$GATE/acceptance.py" status --bundle "$GATE"
```

The end gate independently checks every public/host sample, timing/counts,
source/input pins, process completion, and the entire 24-hour interval before
running the frozen public adoption smoke. It checks current B/host state before
and after that smoke. It uses a minimal child environment and never forwards
GitHub tokens or SSH agent variables. HTTP and `www` aliases are checked by
default for the public site; another HTTPS origin requires explicit aliases.

An attempt is one-shot. Failed checks produce a protected receipt with the failing
stage and a sanitized reason, without copying subprocess stderr or private paths.
A killed attempt is interrupted; keep its artifacts and investigate before
creating a replacement bundle. Preparation requires the unchanged observer to
still be running. If it has finished, a new full interval and start smoke are
required; a failed receipt cannot be overwritten. Even a passed receipt covers
only observation and boundary smokes: final backup integrity, retained-image
inventory, recovery evidence/runbook review, and fallback closure remain required.

## Verification

```sh
task site:observation:test
python3 -B -m unittest discover -s deploy/kamal-site -p 'test_*.py'
```

Fixtures cover concurrent starts, missing/failed public checks, late samples,
clock jumps, reserve and identity changes, SSH/source pins, interrupted process
status, group-writable checkout sources, frozen-source drift, and rejection of
incomplete intervals. No fixture connects to the production host or waits a day.
