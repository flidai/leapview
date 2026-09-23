import { html, type TemplateResult } from 'lit'
import '../shared/config-viewer'

export function renderAssetConfiguration(configuration: string, language = 'yaml', defaultView: 'outline' | 'raw' = 'outline'): TemplateResult {
  return html`<section class="detail-section configuration-section" aria-label="Configuration">
    <h2>Configuration</h2>
    <lv-config-viewer .configuration=${configuration} .language=${language} .defaultView=${defaultView}></lv-config-viewer>
  </section>`
}
