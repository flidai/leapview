# Install LeapView Desktop

End users install LeapView Desktop from the official [download page](/download). They do not clone the repository, run build commands, or need source code and development tools.

> **Availability:** Desktop downloads are temporarily unavailable. The previously advertised preview release is not available on GitHub, so the download page no longer offers its installers. The instructions below apply once a verified preview is published. Signed production installers remain a later release milestone.

## Choose the right installer

| System | Supported version | Architecture | Installer |
| --- | --- | --- | --- |
| macOS | macOS 13 Ventura or newer | Intel or Apple silicon | preview DMG |
| Windows | Windows 10 or newer | x64 | preview per-user Squirrel Setup EXE |
| Ubuntu | Ubuntu 22.04 LTS or newer | x64 | preview DEB |

The download page and its linked GitHub release identify the version, architecture, minimum operating-system version, immutable installer names, SHA-256 checksums, SBOMs, provenance, and signing state. Unsupported system and architecture combinations do not receive a generic or best-effort download.

The production formats remain a signed and notarized DMG, a signed per-user Squirrel Setup EXE, and the signed LeapView APT repository. Those stable artifacts appear on the same download page after qualification.

## macOS

1. Download the DMG matching the Mac architecture.
2. Open it and drag LeapView to Applications.
3. Open LeapView from Applications.
4. For the unsigned preview, use **Control-click → Open** or approve the application in **Privacy & Security** only after verifying the release evidence. A future stable release must identify the verified LeapView publisher without this exception.

The DMG is the human installation format. A matching ZIP exists only for the built-in Squirrel.Mac updater and is not the normal installation choice.

To uninstall, quit LeapView and move it from Applications to the Bin. Removing the application does not automatically erase saved profiles and isolated web sessions from `~/Library/Application Support/LeapView`. Delete that directory only if you intentionally want to remove all local LeapView Desktop state.

## Windows

1. Download the x64 Setup EXE.
2. Run the installer as the intended user.
3. The unsigned preview may show Microsoft Defender SmartScreen. Continue only after checking that the installer came from the linked LeapView prerelease and its SHA-256 matches `SHA256SUMS`.
4. Launch LeapView from the Start menu or desktop shortcut.

The Squirrel installer is per-user and does not require an administrator for routine installation or updates. Its NUPKG and `RELEASES` companions are updater inputs, not separate installers.

Use **Settings > Apps > Installed apps > LeapView > Uninstall** to remove it. The application uninstaller removes its shortcuts and protocol registration. Saved profiles and isolated sessions under `%APPDATA%\\LeapView` are retained unless you remove that directory separately.

## Ubuntu

Download the preview DEB from the linked LeapView prerelease and verify it against `SHA256SUMS`, then install it with the operating-system package manager. The stable Linux release will move to the signed LeapView APT repository.

Use the operating-system package manager to uninstall:

```sh
sudo apt remove leapview
```

Package removal retains user data under `~/.config/LeapView`. Use `apt purge` for package-owned system configuration; delete the user directory separately only when you intend to erase profiles and isolated sessions.

## First launch

The first window asks for the canonical URL of a deployed LeapView instance. Continue with [Connect an instance and manage profiles](/docs/desktop/connect-profiles).

## Verify the installation

Open **Diagnostics** and confirm that the application version, platform, architecture, and package-verification state match the release record. Then follow [Verify a desktop release](/docs/desktop/release-verification) if you need to independently check the artifact checksum, code signature, SBOM, or provenance.
