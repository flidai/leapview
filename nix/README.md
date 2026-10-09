# Nix development and builds

The flake defines native Linux application and tool outputs for **x86_64 and
aarch64**. The default development shell and adopted Nix CI contract remain
**x86_64-only**; ARM64 output discovery is not runtime or host
qualification. ARM64 extension-supply bytes are pinned from native discovery;
final image qualification remains separate.
Nix runs on an existing distribution or NixOS. Entering the shell does not install
an operating system or start services. The existing release pipeline remains in
place while Nix outputs are qualified.
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

To inspect the flake's native outputs and build the source-generation tools for
the current Linux machine, run `nix flake show --no-update-lock-file` and
`nix build --no-update-lock-file .#leapview-tools`. The latter selects the
current machine's `x86_64-linux` or `aarch64-linux` package set.

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
The explicit `go1.26.9` wrapper supports the repository's sqlc toolchain selection.
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
platforms keep their existing tool setup. The default development shell and its
adopted CI lane remain x86_64-only; the flake's native ARM64 package outputs do not
change that adoption boundary. Terraform/provider and specialist
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
nix build --no-update-lock-file .#leapview-site
nix build --no-update-lock-file .#leapview-site-image --out-link result-site-image
nix develop -c docker load --input "$(readlink -f result-site-image)"
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
| `leapview-tools` | Native source-generation tools, including the signed extension-supply publisher, for the current Linux ISA |
| `leapviewctl-linux-amd64`, `leapviewctl-linux-arm64` | Standalone development controller, CGO-disabled, deterministic archive and static-link report; no Nix store or host glibc dependency |
| `leapviewctl-compose-linux-amd64`, `leapviewctl-compose-linux-arm64` | Clean-source controller candidates with canonical release metadata for Compose bundle assembly; requires a matching immutable image and separate qualification |
| `leapview-image` | Container archive with the existing entrypoint, UID/GID 999, health check, writable volume paths and deployment bundle |
| `leapview-compose`, `leapview-linux-compose`, `leapview-image-compose` | Native application, portable binaries and image with canonical `VERSION`, exact clean source revision and `release=true`; candidate metadata grants no release admission |
| `leapview-site` | Native Linux public-site binary with embedded CSS/JavaScript and map assets materialized on disk |
| `leapview-site-image` | Minimal native Linux public-site image with canonical `VERSION`, exact revision labels, UID 65532, and read-only files; candidate metadata grants no release admission |
| `leapview-desktop-linux-x64` | Linux x64 Debian package candidate assembled by the existing Electron Forge `MakerDeb` path with the preview distribution marker; no release admission |
| `go-dependencies`, `javascript-dependencies` | Content-addressed dependency inputs for offline compilation |
| `map-assets` | Pinned runtime map assets |
| `extension-supply` | Signed runtime extension assets pinned independently for both Linux ISAs |

The application and tool package sets are declared for both Linux ISAs. Native
[discovery run 37264692296](https://github.com/flidai/leapview/actions/runs/37264692296)
built both application/tool outputs and verified native signed extension LOADs.
The retained ARM64 ZIP digest and recomputed NAR hash agree with the receipt;
`build-hashes.json` pins that exact tree. This does not establish final image or
host qualification. The shared dependency output includes JavaScript packages for
both Linux CPU variants. Each native build selects its TypeScript dependency tree
and prunes mismatched CPU, OS and libc packages before patching executable helpers.

The application derivation runs source generation, TypeSpec and frontend builds,
and Go/CGO compilation inside the Nix sandbox. Only fixed-output dependency and
runtime-asset fetches use the network. Their complete outputs are pinned in
`build-hashes.json`, in addition to the existing module/package locks and asset
integrity checks. The final image assembly also runs without network access.
Use a Nix installation with `sandbox = true`; the image CI job sets it explicitly.

### Linux desktop package candidate

`nix build .#leapview-desktop-linux-x64` builds only the Linux x64 Debian
package. It uses the locked desktop Bun dependency tree, the official Electron
and Node archives matching versions declared by `desktop/release-policy.json`,
and the existing `desktop/scripts/run-electron.mjs make` Forge packaging path.
The recipe passes the hash-verified Electron ZIP through
`LEAPVIEW_DESKTOP_ELECTRON_ZIP_DIR` to Packager's explicit archive input; a download
cache alone still triggers checksum network requests. Desktop CI requires
`sandbox = true` and `sandbox-fallback = false` so a host without the required
kernel namespaces cannot silently supply an online build.
The package carries the preview distribution marker. The manual `Protected Nix
desktop candidate` workflow qualifies the exact Debian bytes on native Ubuntu
22.04 x86_64 using protected verifier code, then retains the package and a
hash-bound receipt with `releaseAdmission: false`. It checks the protected
control-field and dependency contract, package contents, the embedded Electron
sandbox helper's root-owned setuid mode, installed-payload identity, startup,
installer metadata, release evidence and the hostile-instance boundary. The
`native-desktop` development lane exercises this host floor
without publication credentials.

After qualification, a separate protected signer rechecks the original builder
bytes and complete qualification evidence without running the candidate. It
attests the exact Debian archive's provenance and SPDX predicate and the native
qualification receipt. An independent read-only job verifies those attestations
live against the protected workflow, exact main revision and original artifact
IDs, then retains the signed evidence binding. Package metadata remains
`unsigned-candidate` and `productionEligible: false`; GitHub artifact attestations
do not change the Desktop distribution policy.

That candidate qualification records install, reinstall, protocol registration
and removal. Upgrade, rollback, recovery, canonical release identity, profile
observation and exact promotion remain pending. The protected attestation path
requires a successful live run after merge; fixture tests do not establish that
acceptance. It does not establish production adoption or release admission.
Conventional desktop release workflows remain authoritative.
macOS and Windows outputs remain on their existing toolchains.

### Standalone public-site candidates

From a clean committed checkout, `nix build .#leapview-site-image` produces the
native AMD64 or ARM64 site image. Its final Go binary is CGO-disabled and has no
dynamic interpreter or Nix store references. Documentation generation uses the
pinned signed extension supply during the build; those extensions and the
application runtime are not included in the site image. The shared portable Go
SDK preparation also serves the standalone controller build.

The image contains the site executable, map assets and a CA bundle, with UID/GID
65532 and port 8081. Run it with `--read-only`. The `native-site` manual lane in
`nix-development.yml` builds both native architectures and checks health,
readiness, exact served release/build metadata and installation documentation.
It retains the exact archive and a runtime receipt with `releaseAdmission: false`.

The protected site candidate workflow adds independent site inventory, Go and
vulnerability evidence, then binds publication and native runtime checks to the
same immutable image. Site adoption is independent of application or desktop
adoption, and still requires its affected deployment-profile installation,
recovery and observation evidence. See [candidate evidence](CANDIDATE-EVIDENCE.md).

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
version and command discovery in independently pinned Debian 12, Ubuntu 24.04
and Debian 13 fixtures on both native architectures. Debian 12 preserves the
existing authoring-client baseline; Ubuntu 24.04 and Debian 13 come from the
advertised host bootstrap contract. Each fixture's `/etc/os-release` and runtime
identity must match before the full matrix can succeed.
The probe receives no candidate-supplied libraries and runs without network,
write access or root privileges. Reports bind runtime identity, archive and binary
hashes, and every host image digest.

For installation-bundle candidates, `task nix:compose:controllers` builds the same
CGO-disabled controller with canonical `VERSION`, exact clean source revision and
commit timestamp, and `development=false`; the Compose image and controller also
carry the release identity needed to match a conventional image. These metadata
fields are candidate build identity only: they do not authorize publication or
release admission. Dirty source is rejected for this purpose. Standalone
development-controller outputs and their qualifier keep their existing
`VERSION+nix.<revision>` identity.

`scripts/package_compose_bundle.py` is the shared assembler used by the existing
release workflow. It accepts a prebuilt controller, explicit platform, immutable
image reference and matching release identity. The trusted Go reader checks the
controller package, platform and CGO setting without executing it. A separate build receipt binds the exact binary hash to the
producer's declared build identity; the assembler requires that identity to match
the selected image. Go omits linker flags from metadata with `-trimpath`, so this
receipt is a build claim and cannot substitute for native runtime qualification.
The assembler ships the canonical Compose/local-runtime assets,
normalizes modes, reuses the bundle validator, and produces complete inner
checksums and deterministic outer archive/checksum files.

A Nix Compose controller is only an input to that assembler. The completed bundle
can be exercised through the protected [Nix Compose candidate workflow](CANDIDATE-EVIDENCE.md#protected-nix-compose-candidates).
It consumes only a successful `release.yml` `workflow_dispatch` run on `main`,
and requires the release identity and source revision from that run's exact
immutable artifact. Separate platform preflight jobs recheck conventional image
OCI admission and capture image runtime identity before any
Compose controller runs. The workflow builds both Nix controllers from that
clean source and assembles each candidate bundle with the protected shared
packager. Before installed-bundle qualification, separate native jobs extract
each verified controller and collect protected Go vulnerability evidence, a
pinned Syft SPDX inventory and version/help probes in pinned Debian 12, Ubuntu
24.04 and Debian 13 containers. The installed qualifier consumes the exact
same-run evidence artifact without registry credentials. The protected signer
byte-compares these evidence files and inventory
with the original artifacts, verifies all retained receipts, and attests the
unchanged outer archives and their SPDX predicates. The signing job does not
execute candidate code; the read-only verifier checks live provenance and SPDX
attestations against this workflow's protected revision and retained report. The
receipt keeps `releaseAdmission: false`; successful live acceptance remains
pending until the workflow lands on `main` and completes against a fresh
successful release run.

The native application dependency-discovery lane is manual and separate from
release qualification. On a branch with this workflow, run
`gh workflow run nix-development.yml --ref BRANCH -f checks=native-application`
to build `leapview-tools` on native AMD64 and ARM64 runners, execute the signed
extension-supply publisher, and retain the exact output bytes, NAR hash, and
run-bound discovery receipt for 14 days. The receipt records
`releaseAdmission: false`. The checked-in ARM64 pin comes from successful native
discovery; any refresh must repeat that evidence. This lane does not claim final
application-image or host qualification.

These are Nix Compose candidates only. Their receipts explicitly keep
`releaseAdmission: false`. Full systemd/NixOS installation, upgrade, rollback and
recovery, broader host lifecycle gates, and production promotion still require
qualification. No release assets are adopted; conventional release and Darwin
publishers remain selected. The standalone development-controller signature
does not attest a composed bundle.

Controller builds retain Go function symbols (`-w`, without `-s`) so binary-mode
vulnerability analysis can inspect the shipped code. A separate read-only lane
scans both architectures on AMD64 without executing either controller. It retrieves
the same immutable archive artifact, accepts exactly one bounded regular executable
named `leapviewctl`, and retains full scanner reports. Offline verification binds
those reports to the exact binary and compressed archive hashes, expected main
package, architecture, scanner policy and freshness in the CLI candidate manifest.
Duplicate files, links, extra paths and ambiguous tar metadata fail closed.
Failed scans retain diagnostics and cannot produce a successful evidence manifest.
These PR reports are unprivileged qualification evidence. The manual
`Protected Nix controller candidate` workflow rescans exact AMD64 and ARM64
archives with main-owned tools before signing. Its `source_revision` must be the
exact `GITHUB_SHA` from a `workflow_dispatch` on `main` or one exact open PR head
directly based on main. It checks that PR authorization again before signing; a
main dispatch remains bound to its immutable event SHA while the build runs.
Native Linux host matrix probes run without signing credentials; the signing job only
reverifies retained evidence and attests the already-qualified archive bytes.
A separate read-only job verifies live provenance and the exact SPDX predicate
against the protected main workflow revision. Offline verification of PR-generated
reports alone is insufficient. See [the evidence contract](CANDIDATE-EVIDENCE.md#protected-static-controller-candidates).

The standalone `leapviewctl-linux-*` outputs are development candidates derived
from clean source revision and commit timestamp, with `release=false` and a
`VERSION+nix.<revision>` identity. Compose candidates carry canonical release
identity as described above, while their candidate receipts still retain
`releaseAdmission: false`. Container userland command execution does not establish
installation, publication, upgrade, rollback or recovery acceptance. Signed
successful live protected archive provenance/SPDX qualification, complete
systemd/NixOS host qualification, installation payload assembly and protected promotion
remain D05 gates. Conventional release and non-Linux builders remain the published
owners.

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
| Platform matrix | Build and qualify both AMD64 and ARM64, or explicitly review a change to the supported release matrix. The flake defines native packages for both Linux ISAs, but that does not qualify the final image or host lifecycle on either platform. |
| Release identity | Bind the canonical `VERSION`, clean source revision, commit timestamp and release flag consistently in the binaries and OCI labels. Development outputs use development identity; Compose candidates can match canonical release identity but still require final-artifact admission. |
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
- Complete native ARM64 extension-hash discovery and the remaining ARM64 runtime and
  host qualification; qualify desktop/Electron packaging separately.

Contributors and self-hosters can continue using the existing non-Nix workflows.
No host migration or customer deployment is performed by these commands.
