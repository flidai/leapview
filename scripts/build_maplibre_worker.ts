import { version } from 'maplibre-gl/package.json'

export async function buildMapLibreWorker(outdir: string): Promise<void> {
  // MapLibre 6 loads this module relative to its bundled main-thread chunk.
  // Bun does not emit assets referenced through new URL(..., import.meta.url).
  const result = await Bun.build({
    entrypoints: ['node_modules/maplibre-gl/dist/maplibre-gl-worker.mjs'],
    target: 'browser',
    format: 'esm',
    minify: true,
    outdir: `${outdir}/chunks`,
    naming: `maplibre-gl-worker-${version}.mjs`,
  })
  for (const log of result.logs) console.error(log)
  if (!result.success) throw new Error(`failed to build MapLibre worker for ${outdir}`)
}
