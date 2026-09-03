export const mapLibreChromeCSS = `
.maplibregl-map{position:relative;overflow:hidden;font:var(--lv-type-secondary);-webkit-tap-highlight-color:transparent}
.maplibregl-canvas{position:absolute;left:0;top:0}
.maplibregl-canvas-container.maplibregl-interactive{cursor:grab;user-select:none;-webkit-user-select:none}
.maplibregl-canvas-container.maplibregl-interactive:active{cursor:grabbing}
.maplibregl-ctrl-top-left,.maplibregl-ctrl-top-right,.maplibregl-ctrl-bottom-left,.maplibregl-ctrl-bottom-right{position:absolute;z-index:3;pointer-events:none}
.maplibregl-ctrl-top-left{top:10px;left:10px}
.maplibregl-ctrl-top-right{top:10px;right:10px}
.maplibregl-ctrl-bottom-left{bottom:10px;left:10px}
.maplibregl-ctrl-bottom-right{right:10px;bottom:10px}
.maplibregl-ctrl{clear:both;pointer-events:auto}
.maplibregl-ctrl-top-left .maplibregl-ctrl{float:left}
.maplibregl-ctrl-top-right .maplibregl-ctrl{float:right}
.maplibregl-ctrl-group{--lv-map-navigation-target:max(30px,calc(25px * var(--report-canvas-inverse-scale,1)));overflow:hidden;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:var(--lv-bg-panel,#fff);box-shadow:0 1px 3px rgba(31,35,40,.16)}
.maplibregl-ctrl-group button{position:relative;display:block;width:var(--lv-map-navigation-target);height:var(--lv-map-navigation-target);margin:0;padding:0;border:0;border-bottom:1px solid var(--lv-line-subtle,#d8dee4);outline:none;background:transparent;color:var(--lv-fg-default,#1f2328);font-family:var(--fontStack-system);font-size:var(--text-subtitle-size);font-weight:var(--base-text-weight-medium);line-height:var(--lv-map-navigation-target);cursor:pointer}
.maplibregl-ctrl-group button:last-child{border-bottom:0}
.maplibregl-ctrl-group button:hover{background:color-mix(in srgb,var(--lv-bg-panel,#fff) 88%,var(--lv-fg-default,#1f2328) 12%)}
.maplibregl-ctrl-group button:focus-visible{z-index:1;box-shadow:inset 0 0 0 2px var(--lv-accent-fg,#0969da)}
.maplibregl-ctrl-group button:disabled,.maplibregl-ctrl-group button[aria-disabled=true]{cursor:not-allowed;opacity:.45}
.maplibregl-ctrl-group .maplibregl-ctrl-icon{position:absolute;inset:0;display:grid;place-items:center}
.maplibregl-ctrl-zoom-in::before{content:"+"}
.maplibregl-ctrl-zoom-out::before{content:"−"}
.maplibregl-ctrl-compass .maplibregl-ctrl-icon::before{content:"↑";font-size:var(--text-title-size-small)}
.maplibregl-ctrl-attrib,.maplibregl-ctrl-logo{display:none}
`

export function installMapLibreChromeStyles(frame: HTMLElement): void {
  const style = document.createElement('style')
  style.dataset.mapLibreChrome = ''
  style.textContent = mapLibreChromeCSS
  frame.prepend(style)
}
