# Desktop remote-content security proof

This package is the framework-independent hostile LeapView instance used to
test the desktop client's browser-equivalent authority boundary.

## Framework decision

Electron 43.2.0 is the selected desktop framework.

The security proof may certify the next Electron candidate before the desktop
runtime migrates. Its exact candidate version is pinned in
`electron/package.json`; merging that proof does not by itself change the
desktop runtime.

The remote LeapView window is an unprivileged Electron `BrowserWindow` with:

- no preload script, Node integration, Electron API, or other native bridge;
- context isolation, renderer sandboxing, and web security enabled;
- an exact configured-origin main-frame navigation policy;
- popups, webviews, downloads, permissions, and device selection denied;
- one persistent Electron session partition per LeapView profile; and
- storage deletion through the profile's isolated session.

The trusted local connection/settings surface must use a different window and
must never share a preload, IPC handler, or session partition with remote
content.

Unmodified Wails v3 and Tauri v2 were rejected because they inject framework
runtime/IPC bootstrap code into remote pages. Raw Wry avoids that bootstrap but
does not currently provide the required released, cross-platform permission
policy without owned native patches.

The framework decision is made; release certification is not. The complete
manifest must still pass in signed Windows, macOS, and Linux artifacts. A
failure of any hard invariant reopens the decision.

## Local tests

Install the pinned Electron dependency:

```sh
cd internal/app/testing/maliciousinstance/electron
bun install --frozen-lockfile
node node_modules/electron/install.js
```

Run the pure policy and manifest-completeness tests:

```sh
cd internal/app/testing/maliciousinstance/electron
bun run test
```

Run the macOS integration proof from the repository root:

```sh
LEAPVIEW_ELECTRON_BINARY="$PWD/internal/app/testing/maliciousinstance/electron/node_modules/electron/dist/Electron.app/Contents/MacOS/Electron" \
  go test -count=1 -run TestElectronPolicyIntegrationPreservesBrowserEquivalentAuthority -v \
  ./internal/app/testing/maliciousinstance
```

Run the Linux integration proof with Chromium's namespace sandbox enabled:

```sh
docker build \
  -f internal/app/testing/maliciousinstance/electron/Dockerfile.linux \
  -t leapview-electron-security-proof:44.0.0 .
docker run --rm --cap-add=SYS_ADMIN \
  leapview-electron-security-proof:44.0.0
```

Do not substitute Electron's `--no-sandbox` flag. It invalidates the proof.

The Go integration test skips unless `LEAPVIEW_ELECTRON_BINARY` points to an
explicit Electron binary, keeping ordinary unit-test runs fast and deterministic.
The `Electron security proof` workflow runs the same complete 20-invariant
manifest on macOS Intel, Windows x64, and sandboxed Linux x64. Proof output
contains only bounded enum-like observations and framework versions; it must
not contain credentials or tenant data.
