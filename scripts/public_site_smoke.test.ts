import { afterEach, describe, expect, test } from 'bun:test'
import type { DesktopReleaseManifest, PublicReleaseManifest } from './public_site_smoke'
import { verifyPublicSite } from './public_site_smoke'

const release: PublicReleaseManifest = {
  schemaVersion: 1,
  version: '0.2.0-rc.1',
  tag: 'v0.2.0-rc.1',
  revision: 'dfb3086d59284c6597180e99a7d07f41e36a7f7e',
  image: 'ghcr.io/yacobolo/leapview@sha256:8b32fc291c86005c69c2ca1fa673dcaa4cb84d39cfc951e065a2775b122f81d9',
  releaseUrl: 'https://github.com/flidai/leapview/releases/tag/v0.2.0-rc.1',
  artifacts: [
    {
      os: 'linux',
      architecture: 'amd64',
      archiveUrl: 'https://example.test/leapview-linux-amd64.tar.gz',
      checksumUrl: 'https://example.test/leapview-linux-amd64.tar.gz.sha256',
    },
  ],
}

const desktopRelease: DesktopReleaseManifest = {
  schemaVersion: 1,
  status: 'preparing',
  product: {
    name: 'LeapView',
    applicationId: 'dev.leapview.desktop',
  },
  channel: {
    name: 'stable',
    updateOrigin: 'https://releases.leapview.dev',
    pathVersion: 'v1',
  },
  support: [
    { platform: 'darwin', architectures: ['arm64', 'x64'], minimumVersion: 'macOS 13 Ventura' },
    { platform: 'linux', architectures: ['x64'], minimumVersion: 'Ubuntu 22.04 LTS' },
    { platform: 'win32', architectures: ['x64'], minimumVersion: 'Windows 10' },
  ],
  release: null,
}

const servers: ReturnType<typeof Bun.serve>[] = []

afterEach(() => {
  for (const server of servers.splice(0)) {
    server.stop(true)
  }
})

function installation(values: string[]): string {
  return `<html><body>${values.map((value) => `<p>${value}</p>`).join('')}</body></html>`
}

function startSite(
  installationBody: string,
  desktopDownloadBody = '<html><body>Production downloads are not published yet.</body></html>',
): string {
  const server = Bun.serve({
    port: 0,
    fetch(request) {
      const path = new URL(request.url).pathname
      switch (path) {
        case '/healthz':
        case '/readyz':
          return new Response('ok')
        case '/release.json':
          return Response.json(release)
        case '/desktop-release.json':
          return Response.json(desktopRelease)
        case '/download':
          return new Response(desktopDownloadBody, { headers: { 'Content-Type': 'text/html' } })
        case '/docs/installation':
          return new Response(installationBody, { headers: { 'Content-Type': 'text/html' } })
        default:
          return new Response('not found', { status: 404 })
      }
    },
  })
  servers.push(server)
  return `http://127.0.0.1:${server.port}`
}

describe('public site adoption smoke', () => {
  test('bounds stalled requests so CI cannot hang indefinitely', async () => {
    const stalledFetch = ((_input: string | URL | Request, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(init.signal?.reason), { once: true })
      })) as typeof fetch

    await expect(
      verifyPublicSite({
        baseURL: 'http://127.0.0.1',
        expectedRelease: release,
        expectedDesktopRelease: desktopRelease,
        allowHTTP: true,
        verifyArtifacts: false,
        fetch: stalledFetch,
        requestTimeoutMs: 20,
      }),
    ).rejects.toThrow('request failed')
  })

  test('accepts a healthy site whose manifest, installation page, and immutable links agree', async () => {
    const required = [
      release.version,
      release.tag,
      release.revision,
      release.image,
      release.releaseUrl,
      ...release.artifacts.flatMap((artifact) => [artifact.archiveUrl, artifact.checksumUrl]),
    ]
    const baseURL = startSite(installation(required))

    await expect(
      verifyPublicSite({
        baseURL,
        expectedRelease: release,
        expectedDesktopRelease: desktopRelease,
        allowHTTP: true,
        verifyArtifacts: false,
      }),
    ).resolves.toBeUndefined()
  })

  test('rejects documentation that drifts from the deployed release manifest', async () => {
    const baseURL = startSite(installation([release.version]))

    await expect(
      verifyPublicSite({
        baseURL,
        expectedRelease: release,
        expectedDesktopRelease: desktopRelease,
        allowHTTP: true,
        verifyArtifacts: false,
      }),
    ).rejects.toThrow('installation page does not contain')
  })

  test('rejects non-HTTPS production origins before making a request', async () => {
    await expect(
      verifyPublicSite({
        baseURL: 'http://leapview.dev',
        expectedRelease: release,
        expectedDesktopRelease: desktopRelease,
        verifyArtifacts: false,
      }),
    ).rejects.toThrow('public site must use HTTPS')
  })

  test('rejects an unpublished desktop page that leaks a release-host download', async () => {
    const required = [
      release.version,
      release.tag,
      release.revision,
      release.image,
      release.releaseUrl,
      ...release.artifacts.flatMap((artifact) => [artifact.archiveUrl, artifact.checksumUrl]),
    ]
    const baseURL = startSite(
      installation(required),
      '<a href="https://releases.leapview.dev/unsigned.dmg" download>Download</a>',
    )

    await expect(
      verifyPublicSite({
        baseURL,
        expectedRelease: release,
        expectedDesktopRelease: desktopRelease,
        allowHTTP: true,
        verifyArtifacts: false,
      }),
    ).rejects.toThrow('unpublished desktop page exposes an artifact')
  })
})
