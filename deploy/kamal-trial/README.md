# Isolated Kamal public-site trial

This retains the isolated qualification fixtures and September 2026 evidence for
the Kamal-first solution to public-site disk exhaustion. Production deployment
now lives in [`deploy/kamal-site`](../kamal-site/README.md). These fixtures are
not a production deployment controller.

The experimental image publisher is retired. The historical package contents
and admission artifacts are retained in the
[10 October retirement archive](https://github.com/flidai/leapview/releases/tag/archive-kamal-trial-20261010).
The [assessment and deletion receipt](evidence/retirement-20261010.md) record the
dependency audit, archive checks and completed deletion.
Synthetic lifecycle, storage and recovery fixtures remain available; they do not
depend on that registry package.

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
./deploy/kamal-trial/fixture.go`. It is a synthetic non-root HTTP app, not LeapView. Its standalone Go module keeps this deployment fixture outside the application architecture and dependency graph.
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

## Reproduce historical real-image qualification

The removed `site-kamal-trial.yml` published only
`ghcr.io/flidai/leapview-site-kamal-trial`, with `service=leapview-site-trial`, from
the opt-in `ganesh/site-kamal-trial` branch. Its workflow identity remains in
historical admission records. Production continues to reject trial images;
production Kamal uses `ghcr.io/flidai/leapview-site`.

The retirement archive contains the package version/tag inventory, a complete OCI
layout with original content digests, and the available trial admission artifacts.
It preserves the exact images referenced by `evidence/real-site.json` and the
earlier manual experiment in `../kamal-site/evidence/manual-vps.json`.

Download and verify the archive before extraction:

```sh
gh release download archive-kamal-trial-20261010 --repo flidai/leapview \
  --dir "$ARCHIVE_DIRECTORY" --pattern 'trial-package-oci.tar.gz.part-*' \
  --pattern retirement-manifest.json --pattern SHA256SUMS
(cd "$ARCHIVE_DIRECTORY" && sha256sum --check SHA256SUMS && \
  cat trial-package-oci.tar.gz.part-* > trial-package-oci.tar.gz && \
  jq -r '"\(.archiveSha256)  \(.archive)"' retirement-manifest.json | sha256sum --check && \
  tar -xzf trial-package-oci.tar.gz)
```

Extract the selected original admission ZIP from
`trial-package/admission-artifacts/`; `admission-artifacts.json` maps each ZIP to
its source run. Find its `oci-admission.json` (the upload preserves nested
directories), then use the current checkout's preparer to select the image
without contacting GHCR:

```sh
python3 deploy/kamal-trial/prepare_site_image.py \
  --admission "$DOWNLOADED_ADMISSION_JSON" --output "$NEW_IMAGE_DIRECTORY" \
  --archive-layout "$ARCHIVE_DIRECTORY/trial-package/oci"
```

Run the following probe from a checkout at the admission's
`attestation.sourceRevision`, using absolute paths for the prepared artifacts.
That checkout supplies release documentation matching the historical image:

```sh
sudo unshare --mount --net --pid --fork --mount-proc \
  python3 -B deploy/kamal-trial/site_probe.py \
  --state "$NEW_STATE_DIRECTORY" --artifacts "$ARTIFACTS" \
  --gems "$GEM_HOME" --registry "$REGISTRY_BINARY" \
  --site-record "$NEW_IMAGE_DIRECTORY/site-record.json" \
  --site-archive "$NEW_IMAGE_DIRECTORY/site.oci.tar"
```

The archive preserves the entire admitted OCI index. The probe seeds its private
registry, drops the seed image from Docker, waits for content GC, and pulls the
host's platform normally. It verifies index/manifest/config identities, the
running container's platform descriptor, and the HTTPS response. Ruby transport
configuration in these fixtures is deliberately local; production must separate
versioned app settings from the next CI runner's bootstrap/SSH paths.

## Remaining migration gates

The full approved plan is in `implementation-plan.md`; network setup is in
`runner-access.md`. In particular:

- Carry the proven exact admitted-image checks into the production adapter.
- Integrate and verify the tested safeguards in the production adapter,
  including host-local metadata, failure recovery and external CI/operator
  serialization. `storage_edges.py` now covers shared layers/foreign containers
  and public acceptance failure using supported `redeploy` to defer pruning.
  `fresh_runner.py` additionally proves offline rollback from a fresh controller
  mount namespace with the host records/socket hidden; all host access uses SSH.
- Derive production capacity from actual site images and peak physical usage;
  synthetic fixture sizes are not a production sizing recommendation.
- Establish runner-to-host SSH access. The existing workflow has no SSH step;
  the production environment currently restricts inbound SSH to an operator
  CIDR. Operator SSH through Tailscale is verified with the repository host-key pin.
  Hosted-runner network access is not yet verified. Do not widen SSH
  to the internet as a shortcut.
- Deliver/review a separate, default-off integration; drain old jobs, disable
  the old controller, and rehearse controller handover before enabling it.

Sources: [Kamal deployment](https://kamal-deploy.org/docs/commands/deploy/),
[pruning](https://kamal-deploy.org/docs/commands/prune/),
[v2.12 pruning implementation](https://github.com/basecamp/kamal/blob/v2.12.0/lib/kamal/commands/prune.rb),
[GitHub event source identity](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request).
