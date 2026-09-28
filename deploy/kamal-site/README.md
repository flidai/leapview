# Manual public-site Kamal deployment

Implementation and qualification are in progress. **Production remains on
Compose/Caddy. Do not write a handover marker from synthetic test evidence.**
The [accepted completion plan](completion-plan.md) defines the remaining gates.
Automatic VPS activation is deferred; `site-deploy.yml` only publishes and
qualifies production images. No registry deployment tags or credentials are used.

## Operator commands

Use the working Infisical site SSH key over the existing Tailscale route. Export
`SITE_SSH_KEY` as the protected key file's path (or provide
`SITE_SSH_PRIVATE_KEY` through the secret manager). The operator verifies the
reviewed SSH fingerprint in `deploy/hetzner-site/ssh-host-key.sha256`.
No fabricated `GITHUB_*` variables or new CI network identity are needed.

Install the locked Ruby dependencies using `BUNDLE_GEMFILE=deploy/kamal-site/Gemfile`
and Bundler 2.6.9. The operator also needs Python 3, Docker/buildx, Go and an
authenticated `gh`; live admission uses the repository's pinned Trivy verifier.
Run from the repository root:

```sh
task site:deploy -- status
task site:deploy -- prepare --image ghcr.io/flidai/leapview-site@sha256:FULL_DIGEST
task site:deploy -- deploy --record /absolute/path/printed-by-prepare.json
task site:deploy -- rollback
task site:deploy -- maintain
```

`prepare` requires the production repository/service label, a successfully
completed main production workflow whose qualification artifact names the exact
image, live OCI provenance/SBOM/vulnerability verification, and exact index,
amd64 manifest and config digests. It fetches release metadata at that immutable
source revision. Its output is a version-specific mode-0600 operator-owned file.
A new deployment repeats live verification and compares the prepared record.
Editing a JSON claim is not an admission bypass. Trial images are rejected.

`deploy` holds both legacy flock locks before preflight or host image mutation.
It persists the pending version, anonymously pulls the exact digest, creates a
host-local `localhost:5555/leapview-site:k<DIGEST>` tag and verifies the selected
platform. This is a naming namespace only: no local registry runs at port 5555.
Kamal's local-registry configuration requires no placeholder credentials; the
adapter rejects registry-login commands. After verification it removes the
redundant source digest alias without force, preserving the same content under
the local identity. This avoids Docker 29's extra `<none>` repository row breaking
Kamal's native pruning.

Supported `kamal app boot` owns boot/readiness switching. Docker boot uses
`--pull never`; the only candidate pull is the explicit supervised step. Actual
container identity, runtime, public build/release metadata, health/readiness,
docs, assets and www redirect must pass before acceptance. Acceptance records
current and distinct verified prior, then exact known stopped duplicates/failed
containers are removed, followed by native `kamal prune all`. The final image
set must equal the recorded current/prior set. Foreign aliases, unknown images
and unknown containers block mutation; no volume/system pruning is used.

An already-active prepared record must exactly match the saved verified record.
It checks identity and public health, then returns without admission downloads,
host pull, restart or pruning. `status` does not require a healthy current app,
ready proxy or handover: missing/broken resources appear as individual errors.

`rollback` selects only the verified prior image, retained container and saved
runtime. It performs no registry admission or pull. A stopped, unhealthy or
missing current container does not block it. Missing/contradictory **prior**
material does block it. After restoring the prior through Kamal, a missing
former-current container can be recreated, stopped, from its exact verified local
image/runtime to preserve future recovery. It is never started by this helper.

## Exclusive ownership and interrupted work

`supervisor.py` runs on the host and holds `/opt/leapview-site/reconcile.lock`
and `/opt/leapview-site/deploy.lock` throughout an attempt. Kamal retains its own
activation lock. The supervisor journals attempt/controller identity, host boot
ID, remote process IDs/start ticks, commands, completion and observed capacity.
Every SSHKit command and upload goes through the same supervisor. Remote work
runs independently of the SSH connection carrying its response.

EOF, termination or loss of the lock connection revokes further commands. The
supervisor waits for its children and retains `owner.json` as unresolved. A
second operator, including `maintain`, cannot take over that journal, even after
all children finish. No age threshold, automatic stale-lock clearing or automatic
Kamal unlock exists. A process or Docker client disappearing does not establish
that a Docker daemon operation stopped.

Explicit recovery, using the pinned operator SSH connection:

1. Record `status`, `owner.json`, `state.json`, Docker image/container inspection,
   Caddy configuration and actual proxy route/public responses in a protected
   incident directory. Preserve the attempt ID and all recorded versions.
2. Prevent the old controller from issuing commands. Stop its local process and
   revoke its host supervisor with SIGTERM, **only after matching the saved boot
   ID and PID to the actual supervisor**. Do not kill a reused PID. The supervisor
   closes its command socket; the old attempt token cannot authorize another
   session. Do not release Kamal's lock yet.
3. Wait for every recorded remote process and its process group to exit. Verify
   PID start ticks, descendants and both flock locks, not just SSH sessions.
   Acquire both locks nonblocking in reconciliation-then-deployment order. If
   any work is active or ownership is uncertain, stop: maintenance remains blocked.
4. Inspect Docker's real image/container state and the actual proxy route.
   A failed/timed-out pull or switch may have continued in the daemon. If its
   completion cannot be established, perform a controlled Docker/containerd
   restart under the locks, then inspect again. Record any public interruption.
   Never infer daemon quiescence from killing a Docker client.
5. Only after fencing, quiescence and actual-state reconciliation, archive the
   ownership journal to the protected incident directory. Preserve it; do not
   delete the evidence. Release a stale Kamal activation lock only after proving
   its recorded owner and all mutating work have ended. Keep pending deployment
   state until the actual verified version/route is established.
6. Run `maintain`. It first verifies the recorded active container and public
   response. It can reconcile a stopped/interrupted candidate and finish cleanup;
   it never activates a candidate. If an unaccepted candidate is still serving,
   or the recorded active cannot be verified, it refuses. Restore the saved
   verified active through Kamal with its saved runtime under exclusive ownership,
   verify identity/public responses, and record that recovery before retrying.

A failed candidate restores the verified active version. Lost acceptance replies
never cause a guessed rollback. A cleanup error after acceptance leaves the
accepted version live and maintenance pending. Resolve ownership as above before
`maintain`; further deployments stay blocked.

## Capacity record

The protected handover directory is `/var/lib/leapview-site/kamal` (root:root,
0700). `ready.json` has `schema: 1`, `controller: "kamal"`,
`handover_verified: true`, `topology` and `capacity`. `state.json` has schema,
active/prior/pending, maintenance_pending and version-keyed records.
Only public-accepted records have `verified: true`; records retain local image ID,
source/release metadata and the version's runtime/tooling contract.

Measure real admitted images on the qualified Docker 29/containerd overlayfs
runtime. Use each distinct filesystem device returned by `status` once, including
Docker and containerd backing paths. Record these fields per device:

- `paths`: all observed backing paths on that filesystem.
- `measured_peak_bytes`, `measured_peak_inodes`: largest measured incremental
  peak over pull/extraction, boot, acceptance and cleanup, including compressed
  content and extracted snapshots.
- `candidate_headroom_bytes`: ceiling of 1.5 times measured peak bytes.
- `reserve_bytes`: at least max(2 GiB, ceiling of 10% filesystem capacity).
- `reserve_inodes`: at least max(10,000, twice measured incremental inode peak).
- `qualified_compressed_bytes`: maximum qualified candidate platform layer/config
  size. Larger images require renewed qualification, not a relaxed check.

Before a pull, require headroom plus byte reserve and measured inode peak plus
inode reserve. Never remove the only verified rollback to meet these checks.
Supervisor attempt journals sample free bytes/inodes on each recorded filesystem;
measurements and policy margins remain separate. Synthetic fixture measurements
are not a production image envelope. On 28 September read-only inventory found
Docker/containerd on the same filesystem, with 34,631,495,680 bytes free (32.3 GiB).

## Permanent Caddy topology and controlled handover

The migration installs [topology/compose.yaml](topology/compose.yaml) and
[topology/Caddyfile](topology/Caddyfile) as the **active** files in
`/opt/leapview-site`. This definition contains only Caddy; it declares `kamal` as
an external network and persistently routes to `kamal-proxy:80`. It preserves
ports 80/443, certificate/config bind volumes, HTTPS, compression and www redirect.
A one-time `docker network connect` is not the permanent topology.

Before changing them, save protected copies of the original Compose definition,
Caddyfile and deployment.env plus exact image/container inspection and restoration
commands. Keep migration recovery files outside the routine Compose directory.
Keep the old Compose app/image until restoration and restart acceptance pass.
Drain existing production workflows, keep the legacy timer/service disabled,
and install the guarded legacy entrypoints before handover. The handover marker
blocks legacy provision/reconcile/deploy entrypoints from restarting the updater.

Bootstrap the persistent `kamal` network and the private, restartable proxy at the
qualified digest `basecamp/kamal-proxy@sha256:826a6f66c6ba26ac26197ac8755804403c9bb617b90cfac25c7972154c5328ab`.
Its runtime version is v0.9.2, its restart policy is `unless-stopped`, and it has no
published ports. If unavailable, restore that pinned local proxy/runtime/network
before switching app traffic; this is distinct from broken-current app rollback.

Privately boot production-admitted image A. Install the Caddy-only files with a
timed restoration safeguard, recreate Caddy, and verify public identity/routes.
Demonstrate restoration using the protected original Compose/env/Caddy files and
return to A. Record SHA256 hashes of the active files as `topology.compose_sha256`
and `topology.caddy_sha256`; readiness verifies hashes, actual Caddy image/mounts,
restart policies, proxy digest and shared network. Only then write handover state.

Routine Caddy maintenance uses only the active Caddy-only definition:

```sh
cd /opt/leapview-site
docker compose --env-file deployment.env -f compose.yaml up -d --force-recreate caddy
```

Deploy distinct admitted B, locally roll back to A and restore B. Verify Caddy
recreation and a controlled **host** restart before deleting migration resources.
A disposable Docker restart is useful evidence but is not a host-reboot result.
Check HTTPS recovery and prove the legacy app was not recreated. Preserve Caddy
data and protected textual recovery/audit records. Report measured interruptions.
Observe public health/storage for 24 hours before closing #748 as superseded.
Neither the fallback controller nor automatic activation is installed by this work.

## Qualification and remaining gates

Run focused tests, workflow lint and `task ci`. The synthetic lifecycle harness
uses #751's fixture at commit `9786a0fd09f922e2e5da3f544bf9d2670d3d7019`, a private
PID/network/mount namespace, a disposable ext4 filesystem and a private Docker 29
engine. It makes test-only source copies; production has no fixture/admission bypass.

```sh
python3 -B -m unittest discover -s deploy/kamal-site -p 'test_*.py'
sudo unshare --mount --net --pid --fork --mount-proc \
  python3 -B deploy/kamal-site/qualification.py \
  --trial /checkout-of-751/deploy/kamal-trial \
  --state /new/private/qualification-directory \
  --artifacts /prepared-fixture-proxy-caddy-archives \
  --gems /locked-gem-home --registry /pinned-registry-binary
```

The synthetic test cannot qualify production provenance or a real host reboot.
Keep #752 draft until the complete matrix in the accepted plan, final hosted
checks and review are satisfied. Then merge in the plan's sequence, qualify two
eligible production main images and perform controlled migration. Update #751's
trial evidence separately; do not label a manual migration as completed CI/CD.
