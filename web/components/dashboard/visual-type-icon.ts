import { html, svg } from 'lit'

/** Compact two-tone chart pictograms for the dashboard builder. The marks are
 * intentionally filled so visual types remain distinguishable at 20–24px;
 * color is supplied by the picker through product design tokens. */
export function renderVisualTypeIcon(type: string) {
  let marks
  switch (type) {
    case 'line':
      marks = svg`
        <path class="visual-icon-axis" d="M3 3h1.5v16.5H21V21H3z"></path>
        <path class="visual-icon-secondary visual-icon-stroke" d="m6 15 3-3 3 2 3-5 4 2"></path>
        <path class="visual-icon-primary visual-icon-stroke" d="m6 17 3-6 3 1 3-6 4-2"></path>
      `
      break
    case 'area':
      marks = svg`
        <path class="visual-icon-axis" d="M3 3h1.5v16.5H21V21H3z"></path>
        <path class="visual-icon-secondary" d="M5.5 18v-4l3-3 3 2 3-5 4 2v8z"></path>
        <path class="visual-icon-primary" d="M5.5 18v-2l3-6 3 1 3-6 4-2v15z"></path>
      `
      break
    case 'bar':
      marks = svg`
        <path class="visual-icon-axis" d="M3 3h1.5v16.5H21V21H3z"></path>
        <rect class="visual-icon-primary" x="6" y="5" width="12" height="3" rx="1"></rect>
        <rect class="visual-icon-secondary" x="6" y="10.5" width="15" height="3" rx="1"></rect>
        <rect class="visual-icon-primary" x="6" y="16" width="8.5" height="3" rx="1"></rect>
      `
      break
    case 'column':
      marks = svg`
        <path class="visual-icon-axis" d="M3 3h1.5v16.5H21V21H3z"></path>
        <rect class="visual-icon-primary" x="6" y="10" width="3.5" height="8" rx="1"></rect>
        <rect class="visual-icon-secondary" x="11" y="5" width="3.5" height="13" rx="1"></rect>
        <rect class="visual-icon-primary" x="16" y="8" width="3.5" height="10" rx="1"></rect>
      `
      break
    case 'candlestick':
      marks = svg`
        <path class="visual-icon-secondary visual-icon-stroke visual-icon-stroke-thin" d="M7 3v18M17 3v18"></path>
        <rect class="visual-icon-primary" x="4.5" y="6" width="5" height="8" rx="1"></rect>
        <rect class="visual-icon-secondary" x="14.5" y="10" width="5" height="7" rx="1"></rect>
      `
      break
    case 'combo':
      marks = svg`
        <path class="visual-icon-axis" d="M3 3h1.5v16.5H21V21H3z"></path>
        <rect class="visual-icon-secondary" x="6" y="11" width="3" height="7" rx=".75"></rect>
        <rect class="visual-icon-secondary" x="11" y="7" width="3" height="11" rx=".75"></rect>
        <rect class="visual-icon-secondary" x="16" y="13" width="3" height="5" rx=".75"></rect>
        <path class="visual-icon-primary visual-icon-stroke" d="m6 9 4-4 4 3 5-5"></path>
      `
      break
    case 'waterfall':
      marks = svg`
        <path class="visual-icon-axis" d="M3 3h1.5v16.5H21V21H3z"></path>
        <path class="visual-icon-secondary visual-icon-stroke visual-icon-stroke-thin" d="M8 8h2M14 12h2"></path>
        <rect class="visual-icon-primary" x="5.5" y="4" width="3" height="4" rx=".6"></rect>
        <rect class="visual-icon-secondary" x="10.5" y="8" width="3" height="4" rx=".6"></rect>
        <rect class="visual-icon-primary" x="15.5" y="12" width="3" height="6" rx=".6"></rect>
      `
      break
    case 'pie':
      marks = svg`
        <path class="visual-icon-secondary" d="M11 2.5A9.5 9.5 0 1 0 21.5 13H11z"></path>
        <path class="visual-icon-primary" d="M13 2.5a9.5 9.5 0 0 1 8.5 8.5H13z"></path>
        <path class="visual-icon-tertiary" d="M13 13h8.5a9.5 9.5 0 0 1-2.8 5.8z"></path>
      `
      break
    case 'donut':
      marks = svg`
        <circle class="visual-icon-secondary visual-icon-ring" cx="12" cy="12" r="7.5"></circle>
        <path class="visual-icon-primary visual-icon-ring" d="M12 4.5A7.5 7.5 0 0 1 19.5 12"></path>
        <path class="visual-icon-tertiary visual-icon-ring" d="M19.5 12a7.5 7.5 0 0 1-3 6"></path>
      `
      break
    case 'funnel':
      marks = svg`
        <path class="visual-icon-primary" d="M2.5 4h19l-2.8 4H5.3z"></path>
        <path class="visual-icon-secondary" d="M6 10h12l-3 4h-6z"></path>
        <path class="visual-icon-primary" d="M9.5 16h5v4h-5z"></path>
      `
      break
    case 'scatter':
      marks = svg`
        <path class="visual-icon-axis" d="M3 3h1.5v16.5H21V21H3z"></path>
        <circle class="visual-icon-secondary" cx="8" cy="15" r="2"></circle>
        <circle class="visual-icon-primary" cx="12" cy="10" r="2.4"></circle>
        <circle class="visual-icon-secondary" cx="17.5" cy="6" r="1.7"></circle>
        <circle class="visual-icon-primary" cx="18" cy="14.5" r="1.5"></circle>
      `
      break
    case 'heatmap':
      marks = svg`
        <path class="visual-icon-tertiary" d="M3 3h5v5H3zm6.5 6.5h5v5h-5zM16 16h5v5h-5z"></path>
        <path class="visual-icon-secondary" d="M9.5 3h5v5h-5zM3 16h5v5H3zM16 9.5h5v5h-5z"></path>
        <path class="visual-icon-primary" d="M16 3h5v5h-5zM3 9.5h5v5H3zm6.5 6.5h5v5h-5z"></path>
      `
      break
    case 'boxplot':
      marks = svg`
        <path class="visual-icon-secondary visual-icon-stroke visual-icon-stroke-thin" d="M3 12h5m8 0h5M5.5 8v8M18.5 8v8"></path>
        <rect class="visual-icon-secondary" x="8" y="6" width="8" height="12" rx="1"></rect>
        <rect class="visual-icon-primary" x="11" y="6" width="2" height="12"></rect>
      `
      break
    case 'histogram':
      marks = svg`
        <path class="visual-icon-axis" d="M2 3h1.5v16.5H22V21H2z"></path>
        <path class="visual-icon-secondary" d="M4.5 15h3v4h-3zm12-3h3v7h-3z"></path>
        <path class="visual-icon-primary" d="M7.5 9h3v10h-3zm3-5h3v15h-3zm3 3h3v12h-3z"></path>
      `
      break
    case 'treemap':
      marks = svg`
        <rect class="visual-icon-primary" x="2.5" y="3" width="10" height="11" rx="1"></rect>
        <rect class="visual-icon-secondary" x="14" y="3" width="7.5" height="6" rx="1"></rect>
        <rect class="visual-icon-tertiary" x="14" y="10.5" width="7.5" height="10.5" rx="1"></rect>
        <rect class="visual-icon-secondary" x="2.5" y="15.5" width="10" height="5.5" rx="1"></rect>
      `
      break
    case 'sankey':
      marks = svg`
        <path class="visual-icon-secondary visual-icon-band" d="M5 7c6 0 8 7 14 7"></path>
        <path class="visual-icon-tertiary visual-icon-band" d="M5 17c6 0 8-8 14-8"></path>
        <rect class="visual-icon-primary" x="2.5" y="3" width="3" height="8" rx="1"></rect>
        <rect class="visual-icon-secondary" x="2.5" y="13" width="3" height="8" rx="1"></rect>
        <rect class="visual-icon-primary" x="18.5" y="5" width="3" height="12" rx="1"></rect>
      `
      break
    case 'graph':
      marks = svg`
        <path class="visual-icon-tertiary visual-icon-stroke visual-icon-stroke-thin" d="m6 6 12 4M7 18l11-8M6 6l1 12"></path>
        <circle class="visual-icon-primary" cx="6" cy="6" r="3"></circle>
        <circle class="visual-icon-secondary" cx="18" cy="10" r="3"></circle>
        <circle class="visual-icon-primary" cx="7" cy="18" r="3"></circle>
      `
      break
    case 'tree':
      marks = svg`
        <path class="visual-icon-tertiary visual-icon-stroke visual-icon-stroke-thin" d="M12 8v4M6 16v-4h12v4"></path>
        <rect class="visual-icon-primary" x="8" y="3" width="8" height="5" rx="1"></rect>
        <rect class="visual-icon-secondary" x="2.5" y="16" width="7" height="5" rx="1"></rect>
        <rect class="visual-icon-primary" x="14.5" y="16" width="7" height="5" rx="1"></rect>
      `
      break
    case 'sunburst':
      marks = svg`
        <circle class="visual-icon-secondary visual-icon-sunburst-outer" cx="12" cy="12" r="8"></circle>
        <path class="visual-icon-primary visual-icon-sunburst-outer" d="M12 4a8 8 0 0 1 8 8"></path>
        <circle class="visual-icon-tertiary" cx="12" cy="12" r="4.5"></circle>
        <path class="visual-icon-primary" d="M12 7.5A4.5 4.5 0 0 1 16.5 12H12z"></path>
      `
      break
    case 'gauge':
      marks = svg`
        <path class="visual-icon-secondary visual-icon-gauge" d="M4 18a8 8 0 0 1 16 0"></path>
        <path class="visual-icon-primary visual-icon-gauge" d="M4 18a8 8 0 0 1 8-8"></path>
        <path class="visual-icon-primary" d="m11 18 6-7-4 9z"></path>
        <circle class="visual-icon-primary" cx="12" cy="18" r="2"></circle>
      `
      break
    case 'map':
      marks = svg`
        <path class="visual-icon-secondary" d="m2.5 5 6-2.5v16L2.5 21z"></path>
        <path class="visual-icon-tertiary" d="m8.5 2.5 7 3v16l-7-3z"></path>
        <path class="visual-icon-secondary" d="m15.5 5.5 6-3V18l-6 3.5z"></path>
        <path class="visual-icon-primary" d="M15 9.5c0 2.5-3 5.5-3 5.5s-3-3-3-5.5a3 3 0 1 1 6 0Z"></path>
        <circle class="visual-icon-cutout" cx="12" cy="9.5" r="1"></circle>
      `
      break
    case 'radar':
      marks = svg`
        <path class="visual-icon-tertiary" d="m12 2 10 7-4 12H6L2 9z"></path>
        <path class="visual-icon-primary" d="m12 6 6 4-2.5 7H8l-2-6z"></path>
        <circle class="visual-icon-secondary" cx="12" cy="6" r="1.5"></circle>
        <circle class="visual-icon-secondary" cx="18" cy="10" r="1.5"></circle>
        <circle class="visual-icon-secondary" cx="8" cy="17" r="1.5"></circle>
      `
      break
    case 'kpi':
      marks = svg`
        <rect class="visual-icon-tertiary" x="3" y="4" width="18" height="16" rx="2"></rect>
        <path class="visual-icon-secondary" d="M6 15h3v2H6zm4-3h3v5h-3zm4-4h3v9h-3z"></path>
        <path class="visual-icon-primary" d="m12 10 5-5h-3V3h7v7h-2V7l-5 5z"></path>
      `
      break
    case 'table':
      marks = svg`
        <rect class="visual-icon-tertiary" x="2.5" y="3" width="19" height="18" rx="1.5"></rect>
        <path class="visual-icon-primary" d="M2.5 3h19v4h-19z"></path>
        <path class="visual-icon-secondary" d="M4.5 9h5v2h-5zm0 4h5v2h-5zm0 4h5v2h-5zm7-8h8v2h-8zm0 4h8v2h-8zm0 4h8v2h-8z"></path>
      `
      break
    case 'matrix':
      marks = svg`
        <rect class="visual-icon-tertiary" x="2.5" y="3" width="19" height="18" rx="1.5"></rect>
        <path class="visual-icon-primary" d="M2.5 3h19v4h-19zM2.5 7h5v14h-5z"></path>
        <path class="visual-icon-secondary" d="M9 9h4v3H9zm5.5 0h5v3h-5zM9 13.5h4V17H9zm5.5 0h5V17h-5z"></path>
      `
      break
    case 'pivot':
      marks = svg`
        <rect class="visual-icon-tertiary" x="2.5" y="3" width="19" height="18" rx="1.5"></rect>
        <path class="visual-icon-secondary" d="M4.5 5h5v3h-5zm0 5h5v3h-5zm0 5h5v3h-5zM12 5h7.5v3H12zm0 5h7.5v3H12z"></path>
        <path class="visual-icon-primary" d="m14 15 2-2v2h3.5v2H16v2l-2-2-2-2v-2z"></path>
      `
      break
    default:
      marks = svg`<path class="visual-icon-primary" d="M3 4h12v4H3zm0 6h18v4H3zm0 6h9v4H3z"></path>`
  }

  return html`<svg class="visual-type-icon" data-icon-type=${type} viewBox="0 0 24 24" aria-hidden="true">${marks}</svg>`
}
