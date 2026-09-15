# Install the LeapView authoring CLI

This archive contains the released `leapview` host command and its exact
version-matched local runtime package. It does not require a LeapView source
checkout, Go, Bun, Task, or a separately installed DuckDB runtime.

## Supported hosts

| Host | Architecture | Local container runtime |
| --- | --- | --- |
| Ubuntu 24.04 LTS | amd64, arm64 | Docker Engine with Compose 2.17 or newer |
| macOS 15 | Intel, Apple Silicon | Docker Desktop with Compose 2.17 or newer |

Windows is not a supported authoring-CLI host in this release. LeapView Desktop
artifacts do not imply a Windows CLI or local-runtime support contract. Use a
listed Linux or macOS host; do not route local development through a remote or
forwarded Docker daemon.

## Verify and install

Download the archive and its adjacent `.sha256` file from the same GitHub
release. On Linux:

```sh
sha256sum --check leapview-cli-<tag>-<os>-<arch>.tar.gz.sha256
```

On macOS:

```sh
shasum -a 256 --check leapview-cli-<tag>-<os>-<arch>.tar.gz.sha256
```

Extract the archive without moving the `leapview` executable away from its
`local-runtime` sibling. A user-local installation can use a versioned directory
and a symlink:

```sh
mkdir -p "$HOME/.local/lib/leapview" "$HOME/.local/bin"
tar -xzf leapview-cli-<tag>-<os>-<arch>.tar.gz -C "$HOME/.local/lib/leapview"
ln -s "$HOME/.local/lib/leapview/leapview-cli-<tag>-<os>-<arch>/leapview" "$HOME/.local/bin/leapview"
```

If the symlink already exists, inspect it before replacing it. Add
`$HOME/.local/bin` to `PATH`, then verify the embedded build identity:

```sh
leapview version --json
leapview --help
```

The `version`, `revision`, and `buildTime` fields must match
`release-identity.json`; its `image` must match `image-reference.txt` and
`local-runtime/runtime-package.json`. `SHA256SUMS` covers every file inside the
extracted package.

`leapview dev` selects the verified local Docker path. An explicit
`leapview dev --target <name>` uses an existing authorized LeapView target and
does not start local containers. The CLI never rewrites the global Docker
configuration.
