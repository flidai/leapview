# Isolated Kamal public-site trial

This evaluates the approved Kamal-first solution to public-site disk exhaustion.
It is **not a production deployment controller or authorization to migrate**.
The existing site workflows, host controller, Caddy and image package are unchanged.
PR #748 remains the unmerged fallback.

## Isolation and tooling

- Kamal 2.12.0, dependencies locked in `Gemfile.lock`; tested with Ruby 3.3.8.
- Docker 29.1.3 with a private containerd image store. The default snapshotter
  is overlayfs, matching production. Earlier evidence used `--snapshotter native`
  and is identified separately.
- Proxy v0.9.2 (Kamal's supported default), upstream index
  `sha256:826a6f66c6ba26ac26197ac8755804403c9bb617b90cfac25c7972154c5328ab`.
- Caddy 2.10.2-alpine, matching provisioning's upstream index
  `sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d`.
- Private network, PID and mount namespaces, Docker/containerd directories,
  registry and SSH daemon. The harness refuses to run outside an isolated PID
  namespace whose network contains only loopback. It never uses the default
  Docker socket. Run on Linux with root namespace/cgroup capabilities.
- Ephemeral SSH keys and strict client host-key checking. The isolated **test
  sshd only** disables ownership-path checking because the root-owned fixture is
  beneath a developer-owned directory; production SSH configuration is untouched.

Build `fixture.go` with `CGO_ENABLED=0 go build -o "$ARTIFACTS/fixture"
./deploy/kamal-trial/fixture.go`. It is a synthetic non-root HTTP app, not LeapView.
Download the pinned amd64 images:

```sh
skopeo copy --override-arch amd64 \
  docker://docker.io/basecamp/kamal-proxy@sha256:826a6f66c6ba26ac26197ac8755804403c9bb617b90cfac25c7972154c5328ab \
  "docker-archive:$ARTIFACTS/proxy.tar:basecamp/kamal-proxy:v0.9.2"
skopeo copy --override-arch amd64 \
  docker://docker.io/library/caddy@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d \
  "docker-archive:$ARTIFACTS/caddy.tar:caddy:2.10.2-alpine"
```

Install Bundler 2.6.9 and run `bundle install` with this directory's Gemfile.
Use a private `GEM_HOME`/`GEM_PATH`; `--gems` must point to that location.
Supply a Distribution Registry 2.8.3 binary via `--registry`.
Choose a **new nonexistent state directory on the disk with free space**, not
`/tmp` when it is a small tmpfs.

```sh
sudo unshare --mount --net --pid --fork --mount-proc \
  python3 deploy/kamal-trial/lifecycle.py \
  --state "$NEW_STATE_DIRECTORY" --artifacts "$ARTIFACTS" \
  --gems "$GEM_HOME" --registry "$REGISTRY_BINARY"
```

The native test records its outcome, including a failing acceptance criterion,
without claiming Kamal is production-qualified. Repeat with another empty state
path and `--mitigate` to evaluate exact failed-attempt container removal and
rollback using the selected prior version's identity. This is test code; it does
not yet supply a production-ready failure cleanup or durable metadata protocol.
Add `--disk-mib 1536 --mitigate` to test real ENOSPC on a disposable ext4
loop filesystem, without filling the workspace disk. This requires loop-device
and mount access. Logs and `report.json` remain under the state directory. Namespace exit stops all
fixture processes. Inspect/report evidence before deleting disposable state.
Never upload generated private SSH keys or registry/Docker configuration.

## Experimental image qualification

`site-kamal-trial.yml` is branch-only and requires exactly one open, same-repo PR
at the pushed SHA carrying the `kamal-trial` label. Create/label the PR, then
rerun the initial workflow or push an update. It publishes only
`ghcr.io/flidai/leapview-site-kamal-trial`, with `service=leapview-site-trial`.
It preserves the native platform builds, SBOM/provenance, OCI admission and site
runtime checks from `site-image.yml`. It has no production environment, host
credentials, production package writes or promotion step. Trial images do not
satisfy the production workflow identity policy.

This corrects one mechanism in the approved plan: a normal `pull_request` event
binds GitHub's source identity to its merge commit. Checking out the PR head does
not change that event identity. A push on the dedicated branch plus the opt-in
PR lookup keeps the attested source and checked-out source identical, without
weakening admission or using `pull_request_target`.

The published trial image still must be exercised with Kamal and all failure
scenarios. Standalone image qualification is not the full trial gate.

## Remaining migration gates

The full approved plan is in `implementation-plan.md`. In particular:

- Prove exact admitted image identity at pull, boot and after proxy switching.
- Complete corrupt metadata, registry outage/retry, disk-full, interruptions,
  concurrent operator, cleanup-error, foreign-alias and shared-layer tests.
- Derive production capacity from actual site images and peak physical usage;
  synthetic fixture sizes are not a production sizing recommendation.
- Establish runner-to-host SSH access. The existing workflow has no SSH step;
  the production environment currently restricts inbound SSH to an operator
  CIDR. No runner route or deployment identity is yet verified. Do not widen SSH
  to the internet as a shortcut.
- Deliver/review a separate, default-off integration; drain old jobs, disable
  the old controller, and rehearse controller handover before enabling it.

Sources: [Kamal deployment](https://kamal-deploy.org/docs/commands/deploy/),
[pruning](https://kamal-deploy.org/docs/commands/prune/),
[v2.12 pruning implementation](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/commands/prune.rb),
[GitHub event source identity](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request).
