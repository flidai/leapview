import { mkdir, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'

export type ManufacturingPart = {
  part_name: string
  parent_assembly: string
  stock: number
  unit_cost: number
  photo_url: string
  preview_url: string
  datasheet_url: string
  part_id: string
}

const assetPath = '/static/files/table-manufacturing-demo'

export const manufacturingParts: ManufacturingPart[] = [
  { part_id: 'motor', part_name: 'Electric motor', parent_assembly: 'Conveyor drive', stock: 42, unit_cost: 285.00, photo_url: `${assetPath}/motor.svg`, preview_url: `${assetPath}/motor.svg`, datasheet_url: `${assetPath}/motor.html` },
  { part_id: 'gear', part_name: 'Helical gear', parent_assembly: 'Reduction gearbox', stock: 186, unit_cost: 48.50, photo_url: `${assetPath}/gear.svg`, preview_url: `${assetPath}/gear.svg`, datasheet_url: `${assetPath}/gear.html` },
  { part_id: 'bearing', part_name: 'Ball bearing', parent_assembly: 'Rotor assembly', stock: 320, unit_cost: 18.75, photo_url: `${assetPath}/bearing.svg`, preview_url: `${assetPath}/bearing.svg`, datasheet_url: `${assetPath}/bearing.html` },
  { part_id: 'control-panel', part_name: 'Control panel', parent_assembly: 'Control electronics', stock: 24, unit_cost: 420.00, photo_url: `${assetPath}/control-panel.svg`, preview_url: `${assetPath}/control-panel.svg`, datasheet_url: `${assetPath}/control-panel.html` },
  { part_id: 'drive', part_name: 'Variable frequency drive', parent_assembly: 'Conveyor drive', stock: 18, unit_cost: 680.00, photo_url: `${assetPath}/drive.svg`, preview_url: `${assetPath}/drive.svg`, datasheet_url: `${assetPath}/drive.html` },
]

// Insertion order is the shared catalog's display order.
export const manufacturingColumnLabels = {
  photo_url: 'Image',
  part_name: 'Part name',
  parent_assembly: 'Parent assembly',
  stock: 'Stock',
  unit_cost: 'Unit cost · USD',
  datasheet_url: 'Datasheet',
  preview_url: 'Image preview',
} satisfies Partial<Record<keyof ManufacturingPart, string>>

const specifications: Record<string, [string, string][]> = {
  motor: [['Rated output', '0.75 kW'], ['Nominal speed', '1,400 rpm'], ['Mounting', 'Foot mount'], ['Shaft diameter', '19 mm']],
  gear: [['Tooth count', '32'], ['Module', '2 mm'], ['Helix angle', '20°'], ['Bore diameter', '20 mm']],
  bearing: [['Bore diameter', '25 mm'], ['Outer diameter', '52 mm'], ['Width', '15 mm'], ['Construction', 'Single-row, open']],
  'control-panel': [['Enclosure', '250 × 180 × 90 mm'], ['Control supply', '24 V DC'], ['Operator controls', 'Start, stop and emergency stop'], ['Display', 'Status and setpoint']],
  drive: [['Nominal output', '1.5 kW'], ['Supply', '230 V AC'], ['Frequency range', '0–100 Hz'], ['Mounting', 'Panel mount']],
}

const radial = (count: number, inner: number, outer: number, centerX = 120, centerY = 90): string => Array.from({ length: count * 4 }, (_, index) => {
  const angle = (index / (count * 4)) * Math.PI * 2
  const radius = index % 4 === 0 || index % 4 === 3 ? inner : outer
  return `${(centerX + Math.cos(angle) * radius).toFixed(2)},${(centerY + Math.sin(angle) * radius).toFixed(2)}`
}).join(' ')

const illustrations: Record<string, string> = {
  motor: `
    <ellipse cx="124" cy="146" rx="82" ry="9" fill="#172b42" opacity=".12"/>
    <path d="M57 127h37l-7 16H47zM143 126h28l16 17h-40z" fill="#345977" stroke="#29465e" stroke-width="2"/>
    <path d="M68 55h86l24 15v54l-24 13H68z" fill="url(#blue)" stroke="#284961" stroke-width="2"/>
    <ellipse cx="68" cy="96" rx="24" ry="41" fill="url(#metal)" stroke="#466078" stroke-width="2"/>
    <ellipse cx="65" cy="96" rx="15" ry="29" fill="#314b64" stroke="#a6bbc9" stroke-width="2"/>
    <ellipse cx="65" cy="96" rx="7" ry="13" fill="#9aadb9"/>
    <path d="M37 89h26v14H37z" fill="url(#metal)" stroke="#4a6072" stroke-width="1.5"/>
    <ellipse cx="37" cy="96" rx="4" ry="7" fill="#dbe4e9" stroke="#4a6072"/>
    ${Array.from({ length: 8 }, (_, i) => `<path d="M${83 + i * 10} 59v73" stroke="#244c6b" stroke-width="3"/><path d="M${86 + i * 10} 59v72" stroke="#6390af" stroke-width="1.5"/>`).join('')}
    <path d="M112 45l13-7h37v22h-50z" fill="#5a7890" stroke="#29465e" stroke-width="2"/>
    <path d="M125 38v17h37M112 45h37v15" fill="none" stroke="#8eabbf"/>
    <rect x="111" y="82" width="31" height="17" rx="2" fill="#d3e1ea" stroke="#314e66"/><path d="M116 87h21M116 91h15M116 95h18" stroke="#71889a" stroke-width="1.5"/>
    <circle cx="54" cy="65" r="2.5" fill="#dae3e9"/><circle cx="54" cy="127" r="2.5" fill="#dae3e9"/>`,
  gear: `
    <ellipse cx="121" cy="148" rx="69" ry="8" fill="#172b42" opacity=".12"/>
    <g transform="translate(0 8)"><polygon points="${radial(24, 54, 63)}" fill="#72889a" stroke="#40566c" stroke-width="2"/></g>
    <polygon points="${radial(24, 54, 63)}" fill="url(#metal)" stroke="#456077" stroke-width="2"/>
    ${Array.from({ length: 24 }, (_, i) => `<path d="M174 90l9 0" stroke="#e1eaf0" stroke-width="2" transform="rotate(${i * 15} 120 90)"/>`).join('')}
    <circle cx="120" cy="90" r="45" fill="#99acb9" stroke="#5c7489" stroke-width="2"/>
    <circle cx="120" cy="90" r="38" fill="url(#metal)" stroke="#d7e3eb" stroke-width="2"/>
    ${Array.from({ length: 6 }, (_, i) => `<circle cx="148" cy="90" r="5" fill="#415d73" stroke="#c4d5e0" stroke-width="1.5" transform="rotate(${i * 60} 120 90)"/>`).join('')}
    <circle cx="120" cy="90" r="21" fill="#c7d5de" stroke="#4a6479" stroke-width="2"/>
    <circle cx="120" cy="90" r="13" fill="#334c62" stroke="#e0e9ef" stroke-width="2"/>
    <path d="M116 76v7h8v-7" fill="#334c62"/>
    <path d="M92 66a37 37 0 0 1 49-7" fill="none" stroke="#f7fbff" stroke-width="2" opacity=".7"/>`,
  bearing: `
    <ellipse cx="121" cy="148" rx="67" ry="8" fill="#172b42" opacity=".12"/>
    <circle cx="120" cy="94" r="61" fill="#6d8396" stroke="#415b71" stroke-width="2"/>
    <circle cx="120" cy="88" r="61" fill="url(#metal)" stroke="#49667e" stroke-width="2"/>
    <circle cx="120" cy="88" r="49" fill="#2e485d" stroke="#d8e5ed" stroke-width="2"/>
    <circle cx="120" cy="88" r="42" fill="none" stroke="#8a9caa" stroke-width="2"/>
    ${Array.from({ length: 10 }, (_, i) => `<circle cx="161" cy="88" r="9.5" fill="url(#ball)" stroke="#d4e1e9" stroke-width="1.5" transform="rotate(${i * 36} 120 88)"/>`).join('')}
    <circle cx="120" cy="88" r="30" fill="url(#metal)" stroke="#d7e4ec" stroke-width="2"/>
    <circle cx="120" cy="88" r="21" fill="#eaf1f6" stroke="#425d74" stroke-width="2"/>
    <path d="M68 64a59 59 0 0 1 92-20M96 72a29 29 0 0 1 38-10" fill="none" stroke="#fff" stroke-width="2" opacity=".65"/>`,
  'control-panel': `
    <ellipse cx="123" cy="153" rx="67" ry="7" fill="#172b42" opacity=".12"/>
    <path d="M61 29h105l18 13v103l-18 9H61z" fill="#8196a7" stroke="#3e586e" stroke-width="2"/>
    <rect x="55" y="29" width="111" height="116" rx="5" fill="url(#metal)" stroke="#3e586e" stroke-width="2"/>
    <rect x="65" y="38" width="91" height="40" rx="3" fill="#304c64" stroke="#69849a"/>
    <rect x="71" y="44" width="79" height="27" rx="2" fill="#b8d9dc"/>
    <path d="M78 54h20M78 62h39M125 51v13M129 51h13v13h-13" fill="none" stroke="#3d7079" stroke-width="2"/>
    <circle cx="82" cy="96" r="10" fill="#36836e" stroke="#486174" stroke-width="3"/><circle cx="111" cy="96" r="10" fill="#5d7285" stroke="#486174" stroke-width="3"/>
    <circle cx="139" cy="117" r="17" fill="#e4be4e" stroke="#465d71" stroke-width="2"/><circle cx="139" cy="117" r="10" fill="#be5554" stroke="#8a3c3c" stroke-width="2"/>
    <path d="M72 115h29M72 122h22M72 129h25" stroke="#758b9e" stroke-width="2"/>
    <circle cx="61" cy="35" r="2" fill="#50677a"/><circle cx="160" cy="35" r="2" fill="#50677a"/><circle cx="61" cy="139" r="2" fill="#50677a"/><circle cx="160" cy="139" r="2" fill="#50677a"/>`,
  drive: `
    <ellipse cx="123" cy="151" rx="57" ry="8" fill="#172b42" opacity=".12"/>
    <path d="M82 27h68l21 14v100l-21 13H82z" fill="#58748b" stroke="#324e65" stroke-width="2"/>
    ${Array.from({ length: 6 }, (_, i) => `<path d="M${151 + i * 3} ${31 + i * 2}v${116 - i * 4}" stroke="#304a60" stroke-width="2"/>`).join('')}
    <rect x="74" y="27" width="77" height="117" rx="4" fill="url(#metal)" stroke="#3e5a70" stroke-width="2"/>
    <rect x="82" y="36" width="61" height="46" rx="3" fill="#2c4a64"/>
    <rect x="88" y="42" width="49" height="19" rx="2" fill="#adcfd5"/><path d="M96 48h13v7H96M114 48h13v7h-13" fill="none" stroke="#42727c" stroke-width="2"/>
    <circle cx="98" cy="71" r="4" fill="#6ea4b7"/><circle cx="113" cy="71" r="4" fill="#7e91a2"/><circle cx="129" cy="71" r="4" fill="#7e91a2"/>
    ${Array.from({ length: 7 }, (_, i) => `<path d="M84 ${92 + i * 5}h56" stroke="#697f92" stroke-width="2"/>`).join('')}
    <rect x="87" y="129" width="51" height="9" rx="1" fill="#405e70"/>
    ${Array.from({ length: 6 }, (_, i) => `<circle cx="${92 + i * 8}" cy="133.5" r="2" fill="#b9cda8"/>`).join('')}`,
}

const escapeHTML = (value: string): string => value.replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]!)

function illustration(part: ManufacturingPart): string {
  return `<svg xmlns="http://www.w3.org/2000/svg" width="240" height="180" viewBox="0 0 240 180" role="img" aria-labelledby="title description">
<title id="title">${escapeHTML(part.part_name)} — catalog illustration</title><desc id="description">Illustrative technical drawing for the manufacturing parts demo.</desc>
<defs><linearGradient id="metal" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#e7eef3"/><stop offset=".45" stop-color="#b5c7d4"/><stop offset="1" stop-color="#7f98ad"/></linearGradient><linearGradient id="blue" x1="0" y1="0" x2="0" y2="1"><stop stop-color="#638aa7"/><stop offset="1" stop-color="#3c6584"/></linearGradient><radialGradient id="ball" cx=".32" cy=".25"><stop stop-color="#fff"/><stop offset=".4" stop-color="#c4d4df"/><stop offset="1" stop-color="#627c91"/></radialGradient><pattern id="grid" width="20" height="20" patternUnits="userSpaceOnUse"><path d="M20 0H0V20" fill="none" stroke="#dce6ed" stroke-width=".6"/></pattern></defs>
<rect width="240" height="180" rx="12" fill="#edf3f7"/><rect width="240" height="180" rx="12" fill="url(#grid)"/><path d="M18 29V18h11M211 18h11v11M18 151v11h11M211 162h11v-11" fill="none" stroke="#a8bdcd" stroke-width="1.5"/>${illustrations[part.part_id]}
</svg>\n`
}

function datasheet(part: ManufacturingPart): string {
  const name = escapeHTML(part.part_name)
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${name} · illustrative datasheet</title><style>
*{box-sizing:border-box}body{margin:0;background:#f3f6f8;color:#24374a;font:16px/1.6 system-ui,sans-serif}main{max-width:780px;margin:40px auto;padding:32px;background:white;border:1px solid #d4dee6;border-radius:12px}header{display:flex;gap:28px;align-items:center}img{width:240px;max-width:42%;height:auto}h1{font-size:28px;line-height:1.2;margin:5px 0 12px}.eyebrow{font-size:12px;letter-spacing:1px;text-transform:uppercase;color:#5c7285}p{margin:8px 0}h2{font-size:18px;margin-top:28px}table{border-collapse:collapse;width:100%}th,td{text-align:left;padding:10px 12px;border-bottom:1px solid #dce5ec}th{width:45%;font-weight:500;color:#536b80}.notice{padding:16px;border-left:3px solid #7594ab;background:#f0f5f8;font-size:14px;margin-top:28px}@media(max-width:580px){main{margin:12px;padding:22px}header{display:block}img{max-width:100%}}</style></head><body><main><header><img src="${part.photo_url}" alt="${name} technical illustration"><div><p class="eyebrow">Manufacturing demo · ${escapeHTML(part.part_id)}</p><h1>${name}</h1><p>Parent assembly: ${escapeHTML(part.parent_assembly)}</p><p>Catalog stock: ${part.stock} units<br>Illustrative unit cost: USD ${part.unit_cost.toFixed(2)}</p></div></header><h2>Illustrative nominal specifications</h2><table aria-label="Illustrative specifications"><tbody>${specifications[part.part_id].map(([label, value]) => `<tr><th scope="row">${escapeHTML(label)}</th><td>${escapeHTML(value)}</td></tr>`).join('')}</tbody></table><p class="notice">This is a fictional catalog record for the LeapView table formatting demonstration. The illustration, specifications, inventory and prices are example data, not manufacturer specifications or purchasing guidance.</p></main></body></html>\n`
}

export async function ensureManufacturingDemoAssets(staticRoot: string): Promise<void> {
  const destination = resolve(staticRoot, 'files/table-manufacturing-demo')
  await mkdir(destination, { recursive: true })
  await Promise.all(manufacturingParts.flatMap(part => [
    writeFile(resolve(destination, `${part.part_id}.svg`), illustration(part), 'utf8'),
    writeFile(resolve(destination, `${part.part_id}.html`), datasheet(part), 'utf8'),
  ]))
}
