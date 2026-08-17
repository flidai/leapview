# LeapView Docker Compose

This is the production operations package for the public LeapView image. It
runs exactly one application process with one named state volume and one
configured environment, and adds hardened defaults, HTTPS, backups, and paired
image-and-state rollback. The included `leapviewctl` is a standalone Go
operations binary for the archive's operating system and architecture.

```sh
cp deployment.env.example deployment.env
./leapviewctl init --admin-email admin@example.com --domain dash.example.com
./leapviewctl start
./leapviewctl first-login
```

Set the released `LEAPVIEW_IMAGE` digest before initialization. HTTPS is
enabled by default through the Caddy overlay. Initialization derives
`LEAPVIEW_PUBLIC_URL=https://<domain>`, the allowed host, and the Caddy domain
from the validated `--domain` hostname. Use `--no-https` only when a trusted
external HTTPS proxy fronts the localhost-bound application port; it disables
the Caddy overlay but preserves the HTTPS public URL and secure cookies.

Pulling and running the public image does not require this package or the
controller; see the installation guide for the localhost evaluation path. For
production, `leapviewctl` provides the supported initialization, backup,
restore, upgrade, and rollback workflow. Run `./leapviewctl help` for its
commands.

The same archive also carries the provider-neutral Ubuntu bootstrap and host
operations assets. VPS adapters use the matching payload embedded in the
immutable application image and delegate installation to `leapviewctl host
install`; they do not maintain a provider-specific Compose lifecycle.

## v0.1.0 migration policy

State created by v0.1.0 is **fresh-install-only** for LeapView v0.2.0-rc.1.
The released image
`ghcr.io/yacobolo/libredash@sha256:677caaf256cb3a0d61efd47b289debbd91984976a5a5c4b372196a5d79ce7153`
uses the `LIBREDASH_*` configuration namespace, `/var/lib/libredash`,
`libredash.db`, and a `libredash-backup.json` archive contract. Do not point
this Compose package at that volume or pass the image to `leapviewctl upgrade`.
The server and controller reject those paths before changing instance state.
The historical package requires authentication and contains only a
`linux/amd64` runtime.

Use the v0.1.0 image's `admin backup` command to preserve the old instance,
then provision a fresh LeapView volume, redeploy project source, reload source
data, and reprovision identities and grants. Keep the old image, archive,
checksum, configuration, and volume until the new instance is accepted. The
full command sequence is in the installed documentation under
`/docs/guides/operate/upgrades#move-from-v010`.

## Qualify the exact installed candidate

Before publishing or adopting a release, follow the bundled
[installed-candidate qualification plan](QUALIFICATION.md). Its executable
journey validates the archive checksums, anonymous immutable image pull,
initialization, browser-approved enterprise authoring and protected publish,
five-minute sample, governed access and denial auditing, restart persistence,
backup, and isolated restore:

```sh
./leapviewctl qualify installed-candidate
```

The controller writes only bounded redacted evidence and removes its isolated
containers, volumes, temporary credentials, and restored instance when it
finishes.

## Verify the release identity

The archive, controller, container labels, running server, and release page
must describe the same build. Before initialization, verify the archive
checksum and compare the packaged identity with the controller:

```sh
sha256sum --check ../leapview-compose-*.tar.gz.sha256
cat release-identity.json
./leapviewctl version --json
```

After pulling the immutable image reference in `image-reference.txt`, inspect
its OCI labels and execute the server's version command:

```sh
LEAPVIEW_IMAGE="$(cat image-reference.txt)"
docker pull "$LEAPVIEW_IMAGE"
docker image inspect "$LEAPVIEW_IMAGE" \
  --format '{{index .Config.Labels "org.opencontainers.image.version"}} {{index .Config.Labels "org.opencontainers.image.revision"}}'
docker run --rm "$LEAPVIEW_IMAGE" version --json
```

The `version` and `revision` values must agree with
`release-identity.json`; the release must also report `"dirty": false` and
`"development": false`. Once the server is running, an API token authorized
to use the evaluation project can verify the authenticated runtime endpoint:

```sh
curl --fail --silent --show-error \
  --header "Authorization: Bearer $LEAPVIEW_API_TOKEN" \
  "$LEAPVIEW_PUBLIC_URL/api/v1/capabilities"
```

Its `buildVersion`, `buildRevision`, `buildTime`, `buildDirty`, and
`buildDevelopment` fields must match the packaged identity. `BuildTime` is the
release commit timestamp, rather than wall-clock packaging time, so rebuilding
the same revision remains reproducible.
