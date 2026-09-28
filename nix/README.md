# Nix development environment

This is the first development-toolchain slice. It supports **x86_64 Linux** with
Nix on an existing distribution or NixOS. It does not install an operating system,
start services when entering the shell, or replace the release image builder.
Docker must already be running and accessible to the current user for development
PostgreSQL and integration tests.

## Use

Install Nix using its [official instructions](https://nixos.org/download/) and enable
`nix-command flakes`. From this Git checkout:

```sh
nix develop
# Existing commands continue to own the workflow:
task dev
task ci
```

For one command without an interactive shell:

```sh
nix develop --no-update-lock-file -c task ci
nix develop --no-update-lock-file -c task nix:smoke
nix flake check --no-update-lock-file -L
```

On a fresh checkout, run `nix develop -c task ci:prepare` before `task ci` to
create the ignored generated inputs, as the existing hosted CI does.

`task nix:check` runs the flake check and browser smoke check; `task nix:ci` runs the
existing PR contract through Nix. Stage new Nix files before evaluating them:
Git-backed flakes include tracked/staged files. Avoid `path:` flakes over a working
checkout containing secrets, because that copies ignored files into the store too.

## Ownership

| Concern | Owner |
|---|---|
| Go, Bun, Node, Task, native compiler/libraries and client utilities | Locked Nix development shell |
| Go modules, JavaScript dependencies and generated application inputs | Existing `go.sum`, `bun.lock` and Task commands |
| PostgreSQL development instances and fixture lifecycle | Existing Docker/Task workflow |
| Playwright browser executables and fonts | Nix packages matching npm Playwright |
| App build, dev process, tests and generation order | Existing Taskfile |
| Release images and self-hosting | Existing Dockerfile and public Compose package |
| Managed OS configuration | Managed deployment scaffold in PR #760 |

The shell includes Go, Bun, Node 24, Task, the native compiler, pkg-config, Git,
curl, jq, OpenSSL, Python, Make, procps, PostgreSQL client/server tools, Docker CLI/Compose,
nixfmt and actionlint. PostgreSQL is **not** automatically started as a host service.

Go and Bun source hashes are recorded in `toolchain.nix`; their versions come from
`go.mod` and `package.json`. Nixpkgs' Go build and Bun patching logic are reused.
The explicit `go1.26.7` wrapper supports the repository's sqlc toolchain selection.
Default Go uses `GOTOOLCHAIN=local` so an unsupported module/toolchain change fails
instead of silently downloading a different compiler.

The second Nix input provides only upstream Playwright packaging at the version
used by npm. Chromium and its headless shell are supplied through
`PLAYWRIGHT_BROWSERS_PATH`, including native dependencies and fonts. Browser
installation does not depend on mutable host libraries. The smoke check verifies
the npm/browser version pairing, starts Chromium, and exercises a page interaction.
Desktop/Electron packaging and other platforms still require qualification.

## CI and updates

The `Nix development` workflow runs native toolchain checks, browser checks, and the
existing `task ci` on relevant toolchain changes or manual dispatch. It uses an
ephemeral GitHub-hosted runner and public Nix substitutes, with no deployment secrets
or private cache account. Existing CI remains in place during qualification; this
slice does not migrate every CI lane to Nix.

Update toolchains in a reviewed change. After `nix flake update`, inspect the lock
and version changes. A Go/Bun manifest update needs the corresponding official
source checksum update in `toolchain.nix`. A Playwright npm update needs matching
upstream packaging and a refreshed lock. Run `task nix:check` and `task nix:ci`.

A development shell pins build tools; it does **not** make `task build` a sandboxed
Nix derivation. Go/npm downloads, Docker, test fixtures and generated/downloaded map
assets remain part of the existing workflow. Build metadata and those inputs must
be modeled before providing a canonical `nix build .#leapview-image` output.

## Next slices

1. Qualify this shell and the development workflow on a replacement NixOS dev VPS,
   with reviewed SSH/Tailscale access and independently restored development secrets.
   Reuse managed host modules, adding a dedicated development profile.
2. Package generated assets and the CGO/DuckDB application as Nix derivations.
3. Produce and qualify an OCI image with the existing release identity, runtime
   privileges, health checks and upgrade/rollback contracts. Retire duplicate
   Dockerfile build logic only after that image becomes the canonical release.

Contributors and self-hosters can continue using the existing non-Nix workflows.
No host migration or customer deployment is performed by these commands.
