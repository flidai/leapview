# LeapView Desktop

LeapView Desktop is an end-user client for deployed LeapView instances. It does
not host or configure LeapView. The connection screen verifies the server's
public discovery document, saves only non-secret instance metadata, and opens
the real LeapView UI in an isolated persistent browser session.

## Connect to a deployed instance

1. Build the application with `task desktop:make`.
2. Open the `.dmg` on macOS, run the per-user Squirrel `.exe` on Windows,
   or install the `.deb` on Ubuntu from `desktop/out/make/`.
3. Enter the deployed instance's canonical HTTPS URL, for example
   `https://analytics.company.com`.
4. Complete authentication in the system browser.

The application accepts any deployed instance that returns a valid LeapView
desktop discovery document. It does not accept arbitrary websites, redirects,
or an instance whose identity changes after it has been saved.

The public response and safe failure taxonomy are authored once in
`api/desktop-discovery/main.tsp`. Generation produces the Go response model used
by the server, the TypeScript model used by the desktop client, and a JSON
Schema at `schemas/desktop/discovery.schema.json`. A schema, protocol,
authentication mode, capability, canonical origin, or immutable instance
identity mismatch fails closed before any remote content is opened.

## Networking

- Discovery and remote windows use Electron's Chromium network stack, so they
  inherit the platform/session proxy configuration and platform certificate
  trust behavior.
- A certificate authority must be present in the operating-system trust store.
  LeapView does not bundle additional CAs, disable certificate verification,
  or offer a click-through bypass.
- Client-certificate authentication is not part of the version-one desktop
  capability contract.
- The trusted shell distinguishes DNS, proxy, TLS, timeout, generic network,
  redirect, malformed response, and compatibility failures without displaying
  raw Chromium errors or network details.
- The cross-platform Electron workflow builds and launches candidates on
  Windows, macOS, and Linux. Ordinary network loss, DNS/VPN transition, system
  proxy behavior, and recovery are qualified on those platform families.

## Consumer installation and updates

The v1 release channels are:

- macOS uses a signed and notarized `.dmg`; the accompanying `.zip` is the
  Squirrel.Mac update payload;
- Windows uses a signed per-user Squirrel Setup `.exe`, with `.nupkg` and
  `RELEASES` update artifacts;
- Ubuntu uses a signed `.deb`, with upgrades delivered through the signed
  LeapView APT repository.

Users do not build the application from source. Windows/macOS routine
installation and updates use user-scoped application state. Linux delegates
installation and updates to the operating-system package manager.

Installed Windows and macOS builds use Electron's platform Squirrel updater.
The trusted main process compiles the stable vendor feed
`https://releases.leapview.dev/desktop/v1/stable/{platform}/{architecture}/{current-version}`;
connected instances and remote renderers cannot configure or invoke it.
LeapView checks shortly after launch and then at a bounded interval. Users can
also choose **Check for Updates…** from the native application/help menu.

An available update downloads through Squirrel, which applies only the
platform package identity accepted by the installed application. LeapView
also rejects malformed, equal, or older target versions before enabling its
trusted **Restart now** action. **Later** safely leaves the update staged for
the next application restart. Release notes, HTML, URLs, instance data, and
native error strings are never rendered in update dialogs or recorded in
diagnostics. Unsigned test candidates that cannot initialize Squirrel remain
usable but fail closed with automatic updates disabled.

Linux never downloads or installs an application update itself. Its native
menu directs users to the signed LeapView APT channel through their ordinary
system package manager.

The packages register `leapview-desktop` with one validated URL argument.
Squirrel lifecycle events own the per-user Windows shortcut and protocol
registration. The macOS bundle and Linux desktop entry own their platform
registration.

Machine-wide MSI/PKG, MSIX, MDM, managed allowlists, private update mirrors,
and offline enterprise deployment are deferred until a validated customer
use case exists. Previously completed managed-policy and machine-installer
work remains preserved but is not a v1 support promise or release gate.

## Release candidates and supply-chain evidence

The cross-platform Electron proof produces a native installer candidate for
each qualified target together with:

- an SPDX 2.3 JSON SBOM covering the complete Bun lock graph, the embedded
  Electron, Chromium, and Node runtimes, and every file in the packaged
  application;
- SHA-256 checksums for the installer, every updater companion, and SBOM;
- release metadata binding the candidate to the source commit, workflow
  revision and run, lockfile, package document, release policy, runtime
  versions, support floor, fixed updater origin/channel/product identity,
  ASAR-only package, and exact Electron fuse state;
- a standalone Node verifier that recomputes the bundle hashes and rejects
  unsupported runtime, target, hardening, privacy, or publication state.

Pull-request candidates are retained for seven days and are explicitly marked
`unsigned-candidate`; they are suitable for testing, not distribution. Main
branch candidates additionally receive GitHub build-provenance and SBOM
attestations. Production publication remains fail-closed until the platform
code-signing identities and installer signing gate are implemented.

The manual **Desktop unsigned preview release** workflow can publish a reviewed
default-branch commit as a GitHub prerelease for early evaluation. It requires
an explicit unsigned-release confirmation and the protected `desktop-preview`
environment. The four installers use immutable versioned names and ship with
SHA-256 checksums, SPDX SBOMs, release manifests, and GitHub attestations. A
packaged preview marker disables the production updater; preview users install
a later signed release manually. The production signing requirement and
release workflow remain unchanged.

After downloading one candidate artifact from GitHub Actions, verify the local
bundle from its root:

```bash
artifact="$(find out/make -type f \\( -name '*.dmg' -o -name '*.exe' -o -name '*.deb' \\) -print -quit)"
manifest="$(find out/evidence -type f -name '*.release.json' -print -quit)"
sbom="$(find out/evidence -type f -name '*.spdx.json' -print -quit)"
update_args=()
if [[ "$artifact" == *.dmg ]]; then
  update_args+=(--update-artifact "$(find out/make -type f -name '*.zip' -print -quit)")
elif [[ "$artifact" == *.exe ]]; then
  update_args+=(--update-artifact "$(find out/make -type f -name '*.nupkg' -print -quit)")
  update_args+=(--update-artifact "$(find out/make -type f -name 'RELEASES' -print -quit)")
fi
node out/evidence/verify-release-evidence.mjs \
  --artifact "$artifact" \
  --checksums out/evidence/checksums.txt \
  --manifest "$manifest" \
  --policy release-policy.json \
  --sbom "$sbom" \
  "${update_args[@]}"
gh attestation verify "$artifact" --repo flidai/leapview
gh attestation verify "$manifest" --repo flidai/leapview
```

The `gh attestation` checks apply to main-branch candidates; pull-request
candidates intentionally have no privileged attestation job.

`desktop/release-policy.json` is the reviewed source of truth for consumer
formats, update mechanisms, scope, runtime, hardening, and support. The current
candidate support floor is macOS 13 on Intel and Apple silicon, Windows 10 on
x64, and Ubuntu 22.04 LTS on x64. macOS Intel and Apple-silicon candidates are
built natively in CI with equivalent package, installation, launch, evidence,
and malicious-instance proofs. Windows x64 and Linux x64 are built natively on
their corresponding hosted runners.

Evidence timestamps derive from the source commit time, and every toolchain
input is exactly pinned, so repeated candidates expose input drift instead of
silently accepting it. An emergency Electron update must change the exact
package pin, lockfile, runtime policy, and expected Chromium/Node versions in
one reviewed change, then pass all package and malicious-instance proofs. It
cannot bypass the supported-major check. Before production release, signing
identity rotation must preserve verification metadata for already-published
artifacts, record the new identity in release evidence, and prove both the
normal and emergency rotation paths.

## Bounded lifecycle recovery

- LeapView persists only the last validated same-origin main-frame GET route
  for each profile. Query strings, fragments, credentials, foreign origins,
  oversized values, and ambiguous network-path references are never stored.
- A failed or crashed remote renderer is destroyed immediately. Recovery
  always creates a fresh hardened `BrowserWindow`; it never calls reload on
  stale `webContents`.
- Automatic recovery uses one coalesced sequence per profile with jittered
  delays of approximately one, three, and eight seconds. It pauses while the
  system is suspended, Chromium reports the machine offline, or the trusted
  shell is hidden or minimized.
- Every attempt re-runs public discovery, requires the exact saved origin and
  immutable instance identity, and verifies the existing desktop session
  before opening the saved route. It never launches system-browser
  authentication automatically.
- Recovery performs only discovery and a fresh GET navigation. It does not
  replay POST requests, Datastar commands, downloads, or interrupted actions.
  After the attempt budget is exhausted, the trusted shell requires an
  explicit reopen.
- Network-online status is only a scheduling hint. A positive result is not
  treated as proof that the instance, proxy, DNS, VPN, or private network is
  reachable; the real Chromium discovery request remains authoritative.

## Try it locally

1. Start LeapView with `task dev`.
2. Read the worktree-local URL with `task dev:status`.
3. In another terminal, run `task desktop:start`.
4. Enter the reported `http://localhost:<port>` URL.

Unpackaged development builds allow loopback HTTP. Packaged builds require
HTTPS and an exact canonical origin.

## Authentication and profile lifecycle

- Authentication happens in the system browser. The desktop client binds an
  ephemeral `127.0.0.1` callback to a short-lived, single-use authorization
  code with S256 PKCE, state, instance ID, profile ID, client ID, and exact
  redirect URI checks.
- The first release supports only the OAuth loopback callback. The operating
  system assigns an ephemeral port, binding is exclusive, and a bind failure
  aborts authentication; no custom-protocol authentication fallback is
  registered.
- The callback accepts one bounded request. Provider rejection, malformed or
  duplicate callbacks, timeout, disconnect, removal, closing every window, and
  application quit cancel the transaction and close the listener. Retrying
  creates new state, verifier, callback, and server-side claim material.
- Electron redeems the code through the saved profile's isolated session. The
  server sets an eight-hour, Secure, HttpOnly, SameSite cookie; no bearer token
  is returned to JavaScript or stored in the profile file.
- Desktop sessions have a 30-minute idle timeout and an eight-hour absolute
  lifetime. An authorized request advances only `lastSeenAt`; it does not
  extend the absolute lifetime or rotate credential material. Version one has
  no silent refresh or rotation endpoint. Idle, expiry, sign-out, and
  revocation require a complete new system-browser authorization with new
  claim, PKCE, and cookie material.
- **Sign out** in LeapView revokes the current server session and clears its
  HttpOnly cookie. The non-secret saved profile remains. Before the next
  authorization, Electron's status preflight observes the invalid session and
  clears that profile partition's storage, cache, and authentication cache.
- **Disconnect** revokes that exact server-side desktop session, closes its
  remote window, and clears its Electron storage, cache, and authentication
  cache. The non-secret saved instance remains.
- **Remove** disconnects first, then deletes the saved instance metadata from
  this device. If server revocation cannot be confirmed because the instance
  is offline, removal still atomically drops the local mapping and clears the
  partition so it cannot be reopened; the trusted shell warns that the
  unreachable server session may remain until its eight-hour ceiling or
  administrator revocation.
- **Administrator revocation** invalidates only the selected server session.
  It cannot reach into a running endpoint; the next authorized request is
  rejected, and the next desktop open/status preflight clears the matching
  profile partition before re-authentication.
- Opening a disconnected, expired, signed-out, or revoked profile starts fresh
  system-browser authentication. Existing valid sessions open without another
  prompt. Browser sessions and other desktop profiles use different server
  sessions and Electron partitions and are not cleared by these actions.

## Saved profile identity and migration

- `profiles.json` schema version 2 stores only the opaque profile ID, canonical
  origin, immutable instance ID, server display name, optional user label, safe
  route, and partition version. Unknown fields fail closed, so credential-like
  additions are never silently retained.
- A user rename changes only the optional local label in the trusted packaged
  shell. Rediscovery can update the separate server-controlled display name
  without overwriting that label.
- Version-one documents migrate atomically to version 2 while retaining the
  exact profile ID and `persist:leapview-profile-<opaque-id>` partition.
  Writes fsync a private temporary file, atomically replace the document, and
  fsync its directory on POSIX. An interrupted temporary file is not a profile
  mapping.
- Newer document or partition versions and unknown fields fail closed. A
  downgrade to a client that understands only version 1 is therefore blocked;
  rollback requires restoring the prior application and compatible profile
  document together rather than guessing a downgrade.
- If a verified origin reports a different immutable instance, or a known
  instance is entered at a new canonical origin, LeapView shows a native
  confirmation containing both exact origins and identities. Confirmation
  atomically replaces the mapping with a new opaque profile ID and therefore a
  new partition, clears the old partition, and resets its route to `/`.
  Cancellation changes nothing.
- `clearStorageData()` clears the selected profile partition's cookies, DOM
  storage, IndexedDB, Cache Storage, and service workers; cache and HTTP
  authentication state are then cleared explicitly. No operation addresses a
  different profile partition or Electron's default session.

## Desktop links

Packaged applications register the separate `leapview-desktop` operating-system
scheme. The initial contract is intentionally narrow:

```text
leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fdashboards%2Frevenue
```

- `origin` must be a canonical HTTPS origin. Unpackaged development builds also
  accept explicit loopback HTTP origins.
- `path` may target `/`, the dashboard catalog, one dashboard,
  or one dashboard page. Query strings, fragments, admin/data/asset routes,
  encoded separators, and traversal are rejected.
- A link for an exact saved origin re-verifies the server identity before
  opening the route in that profile's isolated session.
- A cold-start or macOS `open-url` link for an unknown origin requires native
  confirmation before discovery and onboarding. A secondary process can never
  onboard an unknown origin.
- LeapView holds the Electron single-instance lock. Valid secondary-launch
  links are bounded, serialized, and forwarded to the primary process; invalid
  or ambiguous arguments fail closed in the trusted shell.

The external scheme is deliberately different from the privileged
`leapview://app` connection shell. No operating-system input is loaded as
trusted application content.

## Native desktop behavior

- LeapView restores the connection shell and each saved instance window to
  their last normal bounds and maximized state. Saved bounds are validated and
  clamped to the current monitor work area, including after a display is
  removed or its work area changes.
- `window-state.json` contains only integer window geometry, a maximized flag,
  and opaque local profile IDs. It is written atomically with private
  permissions. URLs, page paths, titles, fullscreen state, and renderer
  content are never persisted there.
- **File → Manage Instances…** (`CmdOrCtrl+Shift+L`) always returns to the
  trusted connection shell.
- Native edit, reload, zoom, fullscreen, minimize, close, and quit roles follow
  platform conventions. The application menu intentionally exposes neither
  DevTools nor force reload.

## Accessibility and display behavior

- The trusted shell uses native HTML landmarks, headings, labels, controls,
  lists, and assertive or polite live regions. Repeated profile actions include
  the local instance name in their accessible name.
- Initial focus is deterministic without renderer script: the URL field owns
  focus in open mode, while a trusted error receives focus before another
  action. A managed-policy lock is assertively announced; Chromium keeps focus
  on either the alert or application document according to platform behavior.
- All keyboard focus uses the LeapView focus token. Profile metadata wraps
  instead of truncating at high zoom, the layout collapses to one column when
  the effective viewport narrows, and forced-colors and reduced-motion
  preferences have explicit contracts.
- Native zoom roles remain available, and saved window bounds are clamped
  after display removal or scaling/work-area changes.
- Package verification reads Chromium's accessibility tree from the actual
  hardened candidate and fails unless required landmarks, names, state, and
  initial focus are exposed. The same proof runs on Linux, macOS, and Windows
  package jobs.
- Final release qualification still requires keyboard and screen-reader
  journeys with VoiceOver, NVDA, and Orca, plus real high-DPI, multi-display,
  display-removal, native-dialog focus-return, and update/restart coverage.

## Managed provisioning

Packaged LeapView reads an optional administrator-installed
`desktop-policy.json` from one fixed system location:

- macOS: `/Library/Application Support/LeapView/desktop-policy.json`
- Linux: `/etc/leapview/desktop-policy.json`
- Windows: `C:\ProgramData\LeapView\desktop-policy.json`

On macOS and Linux the file must be owned by root and must not be writable by
group or other users. On Windows the managed installer must grant write access
only to Administrators and SYSTEM. A missing file selects open mode. An
existing but unreadable, oversized, permissive, or invalid file locks every
connection action instead of reverting to open mode.

The version-one document has an exact schema and contains no credentials:

```json
{
  "schemaVersion": 1,
  "allowUserAddedInstances": false,
  "diagnosticsEnabled": false,
  "preconfiguredOrigins": ["https://analytics.company.com"]
}
```

- Origins must be unique canonical HTTPS origins. Each remains untrusted until
  its normal discovery document and immutable instance identity are verified.
- When user-added instances are disabled, only managed origins and matching
  saved profiles are visible and openable. Existing personal profiles remain
  dormant for policy rollback and cannot be reached through desktop links.
- Managed profiles can be disconnected but not removed locally.
- Disabling diagnostics prevents the journal from being read or written. A
  reviewed local report can still describe the packaged environment and the
  non-secret derived policy revision without event history.
- Packaged builds ignore `LEAPVIEW_DESKTOP_POLICY_PATH`. Unpackaged development
  builds accept it only when it names an absolute local policy file.

## Privacy-safe diagnostics

- LeapView keeps a private, seven-day journal of at most 256 allowlisted
  lifecycle outcomes. Repeated outcomes are coalesced and the file is capped at
  128 KiB.
- The journal never accepts free-form remote strings. Instance origins and
  names, routes, dashboard data, credentials, cookies, authorization values,
  filenames, renderer console output, and crash dumps are not collected.
- **Help → Save Diagnostic Report…** shows the exact included and excluded
  categories before opening a native save dialog. The resulting JSON document
  has a fixed manifest and is written with private permissions for the user to
  review.
- Reports are saved locally and are never uploaded automatically. Electron
  crash collection and upload remain disabled.

## Security boundary

- The trusted connection screen is served from `leapview://app` with no
  preload, Node integration, or IPC bridge.
- Each saved instance has its own persistent Electron session partition.
- Remote LeapView content gets no preload or native API.
- Popups, downloads, device access, permissions, webviews, and cross-origin
  top-level navigation are denied.
- Discovery uses a separate non-persistent session with credentials omitted,
  redirects rejected, an 8-second timeout, and a 64 KiB response limit.
- Production packages enable cookie encryption; enable embedded ASAR integrity
  on Electron's supported macOS and Windows platforms; disable
  Electron-as-Node, `NODE_OPTIONS`, Node inspector arguments, loose-app
  fallback, and privileged `file:` behavior; and contain compiled application
  files only.

Run `task desktop:test` for the desktop contracts and `task
electron-security-proof` for the malicious-instance runtime proof. `task
desktop:package` also reads the fuses back from the produced binary, inspects
the ASAR allowlist, launches the packaged trusted shell, and verifies its
privacy-safe startup journal as a smoke test.
