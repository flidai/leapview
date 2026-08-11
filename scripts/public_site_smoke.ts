import { isDeepStrictEqual } from 'node:util'

export interface PublicReleaseArtifact {
  os: string
  architecture: string
  archiveUrl: string
  checksumUrl: string
}

export interface PublicReleaseManifest {
  schemaVersion: number
  version: string
  tag: string
  revision: string
  image: string
  releaseUrl: string
  artifacts: PublicReleaseArtifact[]
}

export interface DesktopReleaseArtifact {
  platform: string
  architecture: string
  format: string
  fileName: string
  bytes: number
  downloadUrl: string
  sha256: string
  checksumUrl: string
  provenanceUrl: string
  sbomUrl: string
  signature: {
    type: string
    identity: string
  }
}

export interface DesktopReleaseManifest {
  schemaVersion: number
  status: 'preparing' | 'published' | 'withdrawn'
  product: {
    name: string
    applicationId: string
  }
  channel: {
    name: string
    updateOrigin: string
    pathVersion: string
  }
  support: Array<{
    platform: string
    architectures: string[]
    minimumVersion: string
  }>
  release: null | {
    version: string
    publishedAt: string
    notesUrl: string
    sourceCommit: string
    artifacts: DesktopReleaseArtifact[]
  }
}

export interface PublicSiteSmokeOptions {
  baseURL: string
  expectedRelease: PublicReleaseManifest
  expectedDesktopRelease: DesktopReleaseManifest
  aliases?: string[]
  allowHTTP?: boolean
  verifyArtifacts?: boolean
  fetch?: typeof fetch
  requestTimeoutMs?: number
}

function normalizedOrigin(raw: string): string {
  const url = new URL(raw)
  if (url.pathname !== '/' || url.search !== '' || url.hash !== '') {
    throw new Error(`public site origin must not contain a path, query, or fragment: ${raw}`)
  }
  return url.origin
}

async function successfulResponse(
  fetcher: typeof fetch,
  url: string,
  requestTimeoutMs: number,
  init?: RequestInit,
): Promise<Response> {
  let response: Response
  try {
    response = await fetcher(url, {
      redirect: 'follow',
      ...init,
      signal: init?.signal ?? AbortSignal.timeout(requestTimeoutMs),
    })
  } catch (error) {
    throw new Error(`request failed for ${url}: ${error instanceof Error ? error.message : String(error)}`)
  }
  if (!response.ok) {
    await response.body?.cancel()
    throw new Error(`request failed for ${url}: HTTP ${response.status}`)
  }
  return response
}

export async function verifyPublicSite(options: PublicSiteSmokeOptions): Promise<void> {
  const fetcher = options.fetch ?? fetch
  const requestTimeoutMs = options.requestTimeoutMs ?? 15_000
  const request = (url: string, init?: RequestInit) => successfulResponse(fetcher, url, requestTimeoutMs, init)
  const baseURL = normalizedOrigin(options.baseURL)
  if (!options.allowHTTP && new URL(baseURL).protocol !== 'https:') {
    throw new Error(`public site must use HTTPS: ${baseURL}`)
  }

  for (const alias of options.aliases ?? []) {
    const response = await request(alias)
    const finalURL = new URL(response.url)
    await response.body?.cancel()
    if (finalURL.origin !== baseURL || finalURL.pathname !== '/') {
      throw new Error(`public alias ${alias} resolved to ${response.url}, want ${baseURL}/`)
    }
  }

  for (const path of ['/healthz', '/readyz']) {
    const response = await request(baseURL + path)
    const body = (await response.text()).trim()
    if (body !== 'ok') {
      throw new Error(`${path} returned ${JSON.stringify(body)}, want "ok"`)
    }
  }

  const manifestResponse = await request(baseURL + '/release.json')
  const deployedRelease = (await manifestResponse.json()) as PublicReleaseManifest
  if (!isDeepStrictEqual(deployedRelease, options.expectedRelease)) {
    throw new Error('deployed /release.json does not match docs/public-release.json')
  }

  const installationResponse = await request(baseURL + '/docs/installation')
  const installation = await installationResponse.text()
  const requiredValues = [
    options.expectedRelease.version,
    options.expectedRelease.tag,
    options.expectedRelease.revision,
    options.expectedRelease.image,
    options.expectedRelease.releaseUrl,
    ...options.expectedRelease.artifacts.flatMap((artifact) => [artifact.archiveUrl, artifact.checksumUrl]),
  ]
  for (const value of requiredValues) {
    if (!installation.includes(value)) {
      throw new Error(`installation page does not contain ${value}`)
    }
  }

  const desktopManifestResponse = await request(baseURL + '/desktop-release.json')
  const deployedDesktopRelease = (await desktopManifestResponse.json()) as DesktopReleaseManifest
  if (!isDeepStrictEqual(deployedDesktopRelease, options.expectedDesktopRelease)) {
    throw new Error('deployed /desktop-release.json does not match docs/desktop-release.json')
  }
  const desktopPageResponse = await request(baseURL + '/download')
  const desktopPage = await desktopPageResponse.text()
  if (options.expectedDesktopRelease.status === 'preparing') {
    if (
      !desktopPage.includes('Production downloads are not published yet.') ||
      /<a\b[^>]*\bdownload(?:\s|=|>)/iu.test(desktopPage) ||
      /href=["']https:\/\/releases\.leapview\.dev\//iu.test(desktopPage)
    ) {
      throw new Error('unpublished desktop page exposes an artifact')
    }
  } else if (options.expectedDesktopRelease.status === 'withdrawn') {
    if (
      !desktopPage.includes('Desktop downloads are temporarily withdrawn.') ||
      /<a\b[^>]*\bdownload(?:\s|=|>)/iu.test(desktopPage)
    ) {
      throw new Error('withdrawn desktop page exposes an artifact')
    }
  } else {
    if (options.expectedDesktopRelease.release === null) {
      throw new Error('published desktop channel has no release')
    }
    for (const artifact of options.expectedDesktopRelease.release.artifacts) {
      for (const value of [
        artifact.downloadUrl,
        artifact.architecture,
      ]) {
        if (!desktopPage.includes(value)) {
          throw new Error(`desktop download page does not contain ${value}`)
        }
      }
    }
  }

  if (options.verifyArtifacts !== false) {
    for (const artifact of options.expectedRelease.artifacts) {
      for (const url of [artifact.archiveUrl, artifact.checksumUrl]) {
        const response = await request(url, { headers: { Range: 'bytes=0-0' } })
        await response.body?.cancel()
      }
    }
    if (options.expectedDesktopRelease.status === 'published') {
      for (const artifact of options.expectedDesktopRelease.release?.artifacts ?? []) {
        for (const url of [
          artifact.downloadUrl,
          artifact.checksumUrl,
          artifact.provenanceUrl,
          artifact.sbomUrl,
        ]) {
          const response = await request(url, { headers: { Range: 'bytes=0-0' } })
          await response.body?.cancel()
        }
      }
    }
  }
}

async function main(): Promise<void> {
  const manifestPath = process.env.LEAPVIEW_PUBLIC_RELEASE_MANIFEST ?? 'docs/public-release.json'
  const desktopManifestPath = process.env.LEAPVIEW_DESKTOP_RELEASE_MANIFEST ?? 'docs/desktop-release.json'
  const expectedRelease = (await Bun.file(manifestPath).json()) as PublicReleaseManifest
  const expectedDesktopRelease = (await Bun.file(desktopManifestPath).json()) as DesktopReleaseManifest
  const baseURL = process.env.LEAPVIEW_PUBLIC_SITE_URL ?? 'https://leapview.dev'
  const aliases = (process.env.LEAPVIEW_PUBLIC_SITE_ALIASES ?? 'http://leapview.dev,https://www.leapview.dev')
    .split(',')
    .map((value) => value.trim())
    .filter(Boolean)
  await verifyPublicSite({ baseURL, expectedRelease, expectedDesktopRelease, aliases })
  console.log(`public adoption smoke passed for ${baseURL} and ${expectedRelease.image}`)
}

if (import.meta.main) {
  await main()
}
