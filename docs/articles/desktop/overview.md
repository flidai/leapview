# LeapView Desktop

LeapView Desktop is an end-user client for people who consume dashboards from a deployed LeapView instance. It does not host LeapView, compile projects, or turn a laptop into a server.

Use the desktop client when your organization already operates LeapView at an HTTPS address such as `https://analytics.company.com` and you want a dedicated application instead of another browser tab. Administrators continue to deploy, secure, back up, and upgrade the server independently.

## What the client owns

The installed application owns a small trusted shell around the deployed web experience:

- saved, non-secret instance profiles;
- an isolated browser partition for each profile;
- system-browser authentication;
- native menus, safe external-link handling, and bounded recovery;
- signed application updates on macOS and Windows, or a signed APT channel on Ubuntu;
- privacy-safe diagnostics about the client itself.

The deployed instance remains authoritative for users, groups, roles, data policies, sessions, dashboards, and data. A server cannot replace the trusted shell, configure its update source, invoke native Electron capabilities, or turn arbitrary web content into a desktop profile.

## Version-one scope

Consumer v1 targets macOS 13 or newer on Intel and Apple silicon, Windows 10 or newer on x64, and Ubuntu 22.04 LTS or newer on x64. The [download page](/download) is the single end-user distribution entrypoint. Desktop downloads are temporarily unavailable while the preview release is prepared. The page will offer installers only after the release and its download links are verified; signed stable artifacts also require signing and release qualification.

Version one is intentionally a consumer application. It does not promise machine-wide installation, MDM deployment, private update mirrors, client-certificate authentication, offline enterprise bundles, or dashboard authoring tools. Those capabilities require a validated customer use case before they become supported product scope.

## Next steps

1. [Install LeapView Desktop](/docs/desktop/install).
2. [Connect an instance and manage profiles](/docs/desktop/connect-profiles).
3. [Understand authentication and sessions](/docs/desktop/authentication).
4. Keep the [support guide](/docs/desktop/support) available for recovery.
