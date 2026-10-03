# Nix development and builds

The development shell and application/container builds support **x86_64 Linux**
with Nix on an existing distribution or NixOS. Entering the shell does not install
an operating system or start services. The existing release pipeline remains in
place while the Nix image path is qualified.
Docker must already be running and accessible to the current user for development
PostgreSQL and integration tests.

## Use

Install Nix using its [official instructions](https://nixos.org/download/) and enable
`nix-command flakes`. From this Git checkout:

```sh
./scripts/develop.sh
# Existing commands continue to own the workflow:
task dev
task ci
```

For one command without an interactive shell:

```sh
nix develop --no-update-lock-file -c task ci
./scripts/develop.sh task ci:full
./scripts/develop.sh task ci:nightly
nix develop --no-update-lock-file -c task nix:smoke
nix flake check --no-update-lock-file -L
```

On a fresh checkout, run `nix develop -c task ci:prepare` before `task ci` to
create the ignored generated inputs, as the existing hosted CI does.
Local full/nightly contracts also require Terraform 1.13.5 on `PATH`; deployment
validation retains this specialist dependency outside the Nix development shell.

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
| Container candidates | Nix application/image derivations; existing production qualifier |
| Published releases and self-hosting | Existing release pipeline and public Compose package |
| Host provisioning and lifecycle | Existing `deploy/host` bootstrap and `leapviewctl`; OS migration is outside this flake |

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
installation does not depend on mutable host libraries. Font discovery and
configuration use only locked Nix paths, including Playwright's WenQuanYi CJK
fallback: ECharts derives text heights from a CJK glyph even for Latin labels.
The smoke check verifies the npm/browser pairing, starts Chromium, exercises a
page interaction, and checks the chart font metrics without host fonts.
Desktop/Electron packaging and other platforms still require qualification.

## CI and updates

The shared `setup-ci` action defaults to the locked Nix shell on x86_64 Linux.
PR validation (including planning and gating), merge-queue validation and nightly
validation keep their existing Task contracts and use those tools. Compiler flags,
native library paths and pinned browser/font paths are exported into subsequent
workflow steps with an explicit allowlist; runner credentials are never copied.
Browser lanes verify the npm/browser pairing with `task nix:smoke`; a missing or
mismatched locked browser fails without downloading a replacement.

The `CI / Nix development` workflow also runs native toolchain checks, browser checks, and the
existing `task ci` on relevant toolchain changes or manual dispatch. It uses an
ephemeral GitHub-hosted runner and public Nix substitutes, with no deployment secrets
or private cache account. Manual dispatch can select
`checks=image` or `checks=development` for a focused rerun; the default and
pull-request validation run both lanes. Select `contract=full` or `contract=nightly`
with `checks=development` to exercise those contracts in a fresh hosted environment.
These selections install the same pinned Terraform used by deployment validation.
Their ordinary merge-queue/nightly workflows retain their existing gates. Historical
transition utility containers use a portable Docker client from a digest-pinned
fixture image, so they do not depend on the host toolchain's loader or libraries.

Go caches remain bounded by workload, platform, runner image, selected toolchain,
compiler version and locked inputs. Only default-branch jobs publish archives;
candidate jobs restore them and run all selected checks even on a cache miss.

Conventional release builders explicitly select `toolchain: conventional` until
each output passes its compatibility and final-artifact admission gates. Other
platforms keep their existing tool setup. The current flake qualifies x86_64 Linux;
ARM64 development-shell adoption is still pending. Terraform/provider and specialist
dbt/Electron dependencies retain their existing setup until their callers migrate.
See [the caller inventory](CI-CALLERS.md) before removing any installer.

Update toolchains in a reviewed change. After `nix flake update`, inspect the lock
and version changes. A Go/Bun manifest update needs the corresponding official
source checksum update in `toolchain.nix`. A Playwright npm update needs matching
upstream packaging and a refreshed lock. Run `task nix:check` and `task nix:ci`.

## Application and container builds

```sh
nix build --no-update-lock-file .#leapview --out-link result-app
./result-app/bin/leapview version
nix build --no-update-lock-file .#leapview-image --out-link result-image
nix develop -c docker load --input "$(readlink -f result-image)"
# Full production-image qualification using disposable Docker fixtures:
task nix:qualify
```

`task nix:build`, `task nix:image`, and `task nix:qualify` expose the same paths.
The container output is a Docker-loadable archive made by Nixpkgs `dockerTools`;
its loaded image works with Docker, public Compose, and Kamal. No registry or
customer deployment is contacted by a build. Qualification uses a temporary
loopback registry and existing PostgreSQL/browser fixtures.

| Output | Contents |
|---|---|
| `leapview` (default) | Application and deployment CLI, generated contracts, frontend assets and runtime resources |
| `leapview-linux` | Exported Linux CLI binaries for Ubuntu 24.04 or a compatible runtime; no Nix store required |
| `leapviewctl-linux-amd64`, `leapviewctl-linux-arm64` | Standalone CGO-disabled deployment controller, deterministic candidate archive, source/platform identity and static-link report; no Nix store or host glibc dependency |
| `leapview-image` | Container archive with the existing entrypoint, UID/GID 999, health check, writable volume paths and deployment bundle |
| `go-dependencies`, `javascript-dependencies` | Content-addressed dependency inputs for offline compilation |
| `map-assets`, `extension-supply` | Pinned runtime asset trees, using the existing map/extension publishers and integrity checks |

The application derivation runs source generation, TypeSpec and frontend builds,
and Go/CGO compilation inside the Nix sandbox. Only fixed-output dependency and
runtime-asset fetches use the network. Their complete outputs are pinned in
`build-hashes.json`, in addition to the existing module/package locks and asset
integrity checks. The final image assembly also runs without network access.
Use a Nix installation with `sandbox = true`; the image CI job sets it explicitly.

### Runtime compatibility

The native `leapview` output uses the locked Nix runtime and runs on Nix/NixOS.
The image and its exported deployment CLI use ordinary Linux loader paths, with
runtime libraries supplied by the container or the operator's host. Exported Nix
application and native-controller exports currently require **glibc 2.38+, GLIBCXX 3.4.30 and CXXABI 1.3.13**;
Ubuntu 24.04 is the qualification baseline. The build rejects increases to these
ABI requirements. On NixOS, use the native output; the exported conventional Linux
binary needs a compatible loader such as a separately configured `nix-ld`.

The packaged authoring-client fixture uses pinned Ubuntu 24.04 to exercise that
baseline. The existing Dockerfile and its Debian client fixture remain unchanged.
Do not assume these candidate exports support Debian 12. Fully static glibc
binaries are unsuitable here: DuckDB loads native extensions at runtime.

### Standalone deployment controller candidates

`task nix:cli` builds `result-cli-amd64` and `result-cli-arm64`. Each output contains
`bin/leapviewctl`, `leapviewctl-linux-<arch>.tar.gz`, `archive-identity.json` and
`static-compatibility.json`. Generation uses the existing source-generation
contract and locked dependency inputs; controller compilation uses `CGO_ENABLED=0`
and the same target contract as the release installation controller. It does not
build the application image, frontend assets or a complete installation bundle.
The application image's native controller retains its current native commands.

The build copies the locked Go SDK and reverses only its three NixOS data-path
patches for protocol, MIME and timezone files. The controller uses standard host
data paths; compiler/linker fixes remain pinned. Both outputs reject all Nix store
references, including data references that static ELF linkage alone cannot detect.

The build rejects dynamic loaders, dynamic segments, incorrect architectures and
inconsistent Go build metadata. PR CI binds the exact archives through the shared
candidate manifest, extracts them on native AMD64/ARM64 runners, and exercises
version and command discovery in the independently pinned Debian 12 fixture.
The probe receives no candidate-supplied libraries and runs without network,
write access or root privileges. Reports bind the tested binary hash, archive
candidate digest, runtime identity and host image digest.

Controller builds retain Go function symbols (`-w`, without `-s`) so binary-mode
vulnerability analysis can inspect the shipped code. A separate read-only lane
scans both architectures on AMD64 without executing either controller. It retrieves
the same immutable archive artifact, accepts exactly one bounded regular executable
named `leapviewctl`, and retains full scanner reports. Offline verification binds
those reports to the exact binary and compressed archive hashes, expected main
package, architecture, scanner policy and freshness in the CLI candidate manifest.
Duplicate files, links, extra paths and ambiguous tar metadata fail closed.
Failed scans retain diagnostics and cannot produce a successful evidence manifest.
These PR reports are unprivileged qualification evidence; protected signed archive
qualification must rescan the exact output with main-owned tools before signing
or adoption. Offline verification of PR-generated reports alone is insufficient.

These are development candidates derived from canonical `VERSION`, clean source
revision and commit timestamp, with `release=false`. Compatibility receipts retain
`releaseAdmission: false`. Basic Debian command execution does not establish
installation, publication, upgrade, rollback or recovery acceptance. Signed
archive provenance/SPDX, protected Go vulnerability admission, complete supported-host
qualification, installation payload assembly and protected promotion remain D05
gates. Conventional release and non-Linux builders remain the published owners.

These are candidate builds, not a replacement for signed release publication.
Clean Git revisions receive a `+nix.<revision>` development version, source
revision and commit timestamp. Dirty worktrees remain marked as development.
Release attestation/admission and supported upgrade policy are still owned by the
existing release process. The Dockerfile is retained during qualification.

### Updating dependency and asset hashes

After changing dependencies or publisher inputs, update the corresponding hash
in `build-hashes.json`. Temporarily use `pkgs.lib.fakeHash`'s value
(`sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=`), build that output, review
the changed inputs and reported hash, then record it and rebuild. Never accept a
new hash without reviewing the lockfile, asset pins or publisher change that
caused it. Application-only changes do not need new dependency hashes.

The JavaScript input contains unmodified package-manager output; platform-native
helpers are patched in the sandboxed application build. The Go input is a local
module proxy, including deterministic version lists needed by sqlc. Neither
input contains a developer's credentials or caches. Build from a Git flake so
ignored local secrets do not enter the store.

## Release adoption gates

The application image is usable by Docker on a NixOS host. The reusable
`deploy/host/nixos.nix` module configures Docker, the host controller wrapper
and its loader prerequisites; it does not install the application. Nix image
qualification does not replace the documented first-install and host recovery
journeys. Any Kamal integration needs its own lifecycle qualification.

Before replacing the builder in `.github/workflows/release.yml`, satisfy the
existing release contract:

| Gate | Required integration |
|---|---|
| Platform matrix | Build and qualify both AMD64 and ARM64, or explicitly review a change to the supported release matrix. This flake currently supplies AMD64 only. |
| Release identity | Bind the canonical `VERSION`, clean source revision, commit timestamp and release flag consistently in the binaries and OCI labels. Current Nix outputs intentionally carry development identity. |
| Supply-chain admission | Publish by digest, attach trusted GitHub provenance and an SPDX SBOM discoverable by the existing OCI admission verifier, and pass its pinned vulnerability policy. A Docker-loadable archive alone does not supply these attestations. |
| Installation package | Assemble the exported controller and Compose payload from the same build; qualify their loader/ABI compatibility on every advertised host platform. |
| Upgrade and recovery | Run historical transition qualification against that exact clean image; run installed-candidate and host recovery journeys. Local candidate evidence does not replace final-artifact admission. |
| Promotion | Preserve the existing pre-publication gates and publish only the digest that passed them. |

Scanner coverage must include the runtime libraries as well as Go dependencies.
Do not interpret a zero-finding report with no OS inventory as complete coverage:
[Nix is not listed in Trivy's OS coverage](https://trivy.dev/docs/latest/coverage/os/).
[Syft can inventory Nix packages](https://oss.anchore.com/docs/capabilities/nix/),
and the [runtime qualification](RUNTIME-SECURITY.md) now tests inventory and
upstream vulnerability matching with pinned Syft/Grype and synthetic controls.
The candidate scan applies exact-package OpenVEX assessments while retaining the
raw findings and enforcing new unassessed vulnerabilities. Production admission
still requires review of those assessments and integration with the published
digest and other release gates. The build checks that Go module
metadata remains readable after native fixups and conventional Linux export.

The qualification workflow also records [common candidate evidence](CANDIDATE-EVIDENCE.md):
the exact archive, config/layer content, source, locked input and runtime report
hashes. Verification rejects substituted or incomplete evidence. This unsigned
record always leaves release admission false; protected final-artifact admission
and each output's compatibility and lifecycle gates remain required.

The historical fixture uses a private synthetic CA. Its Python publication client
receives `DEMO_GENERATION_CA_CERT` explicitly, because Nix OpenSSL's default trust
store can prefer `NIX_SSL_CERT_FILE` over `SSL_CERT_FILE`. Certificate and hostname
verification remain enabled.

## Follow-up work

- Qualify the development workflow on a replacement NixOS dev VPS, with reviewed
  SSH/Tailscale access and independently restored development secrets.
- Adopt the Nix image in protected release publication after qualification and
  attestation/admission integration; then retire duplicate Dockerfile build logic.
- Qualify other architectures and desktop/Electron packaging separately.

Contributors and self-hosters can continue using the existing non-Nix workflows.
No host migration or customer deployment is performed by these commands.
