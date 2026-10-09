import { LitElement, css, html, nothing, svg } from 'lit'
import { property } from 'lit/decorators.js'
import { Check, LayoutDashboard, Sparkles } from 'lucide'
import type { ChatTranscriptItemSignal } from '../../generated/signals'
import { dashboardGenerationState } from '../chat/dashboard-generation-state'
import { lucideIcon } from '../shared/lucide-icons'

/** Decorative assembly artwork. It contains no values or fabricated results;
 * the dashboard's actual query envelopes take over when they are available. */
class DashboardGeneration extends LitElement {
  @property({ attribute: false }) transcript: ChatTranscriptItemSignal[] = []
  @property({ attribute: false }) runId = ''
  @property({ type: Boolean }) running = true
  @property({ type: String }) prompt = ''
  @property({ type: String }) error = ''
  @property({ type: Boolean }) loading = false
  @property({ type: Boolean, reflect: true }) compact = false

  static styles = css`
    :host { display: block; height: 100%; min-height: 0; color: var(--lv-fg-default); }
    * { box-sizing: border-box; }
    .stage { min-height: 100%; display: flex; flex-direction: column; background: var(--lv-bg-app, #f6f8fa); }
    .toolbar { display: flex; align-items: center; gap: 10px; min-height: 50px; padding: 12px 20px; border-bottom: var(--lv-border-default); background: var(--lv-bg-panel); font: var(--lv-type-body-compact); }
    .toolbar svg { width: 16px; height: 16px; color: var(--lv-accent); }
    .tag { margin-left: auto; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .content { width: min(100%, 1030px); margin: auto; padding: clamp(20px, 4vw, 48px); }
    .intro { display: flex; align-items: flex-start; gap: 14px; margin-bottom: 28px; }
    .spark { display: grid; place-items: center; width: 38px; height: 38px; flex: 0 0 38px; border-radius: 12px; background: color-mix(in srgb, var(--lv-accent, #0969da) 10%, transparent); color: var(--lv-accent); }
    .spark svg { width: 20px; height: 20px; }
    h2 { margin: 0 0 7px; font: var(--lv-type-section-title, 600 20px/1.3 system-ui); }
    p { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-body-compact, 14px/1.5 system-ui); }
    .canvas { position: relative; display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 16px; padding: 22px; border: var(--lv-border-default, 1px solid #d0d7de); border-radius: 12px; background: var(--lv-bg-panel, #fff); box-shadow: 0 12px 40px #00000006; overflow: hidden; }
    .canvas::after { content: ''; position: absolute; inset: 0; pointer-events: none; background: linear-gradient(110deg, transparent 35%, color-mix(in srgb, var(--lv-accent, #0969da) 5%, transparent) 50%, transparent 65%); transform: translateX(-100%); animation: scan 5s ease-in-out infinite; }
    .card { grid-column: span 2; min-width: 0; border: var(--lv-border-muted, 1px solid #d8dee4); border-radius: 8px; padding: 16px; background: var(--lv-bg-panel, #fff); animation: assemble 4.8s ease-in-out infinite; animation-delay: var(--delay, 0s); }
    .card.trend { grid-column: span 4; }
    .card.bars { grid-column: span 2; }
    .skeleton { height: 7px; border-radius: 4px; background: var(--lv-bg-control, #eaeef2); }
    .heading { width: 48%; margin-bottom: 16px; }
    .metric { width: 60%; height: 22px; margin-bottom: 14px; background: color-mix(in srgb, var(--lv-accent, #0969da) 15%, var(--lv-bg-control, #eaeef2)); }
    .foot { width: 35%; height: 5px; }
    .chart { display: block; width: 100%; height: 150px; overflow: visible; }
    .grid { stroke: var(--lv-line-muted, #d8dee4); stroke-width: .6; fill: none; }
    .line { fill: none; stroke: var(--lv-accent, #0969da); stroke-width: 2.6; stroke-linecap: round; stroke-linejoin: round; stroke-dasharray: 540; stroke-dashoffset: 540; animation: trace 4.8s ease-in-out infinite; }
    .area { fill: color-mix(in srgb, var(--lv-accent, #0969da) 8%, transparent); animation: appear 4.8s ease-in-out infinite; }
    .bar { fill: color-mix(in srgb, var(--lv-accent, #0969da) 35%, var(--lv-bg-control, #eaeef2)); transform-box: fill-box; transform-origin: bottom; animation: grow 4.8s ease-in-out infinite; animation-delay: var(--delay, 0s); }
    .bar:nth-of-type(even) { fill: color-mix(in srgb, var(--lv-accent, #0969da) 65%, var(--lv-bg-control, #eaeef2)); }
    .caption { margin-top: 12px; text-align: right; font: var(--lv-type-caption, 12px/1.5 system-ui); }
    .steps { display: flex; gap: 20px; flex-wrap: wrap; margin-top: 25px; color: var(--lv-fg-muted); font: var(--lv-type-caption, 12px/1.5 system-ui); }
    .step { display: inline-flex; align-items: center; gap: 7px; }
    .dot { width: 6px; height: 6px; border: 1px solid currentColor; border-radius: 50%; }
    .step.current { color: var(--lv-accent); }
    .step.current .dot { background: currentColor; box-shadow: 0 0 0 3px color-mix(in srgb, currentColor 12%, transparent); }
    .step svg { width: 12px; height: 12px; }
    .paused *, .paused::after, .paused .canvas::after { animation-play-state: paused; }
    @keyframes assemble { 0%, 10% { opacity: .5; transform: translateY(5px); } 25%, 82% { opacity: 1; transform: translateY(0); } 100% { opacity: .5; transform: translateY(5px); } }
    @keyframes trace { 0%, 12% { stroke-dashoffset: 540; opacity: .45; } 55%, 90% { stroke-dashoffset: 0; opacity: .85; } 100% { stroke-dashoffset: 540; opacity: .45; } }
    @keyframes grow { 0%, 12% { transform: scaleY(.08); opacity: .35; } 40%, 85% { transform: scaleY(1); opacity: .85; } 100% { transform: scaleY(.08); opacity: .35; } }
    @keyframes appear { 0%, 18%, 100% { opacity: 0; } 55%, 85% { opacity: 1; } }
    @keyframes scan { 0%, 15% { transform: translateX(-100%); } 80%, 100% { transform: translateX(100%); } }
    :host([compact]) .toolbar, :host([compact]) .steps, :host([compact]) .caption, :host([compact]) .metric-card, :host([compact]) .bars { display: none; }
    :host([compact]) .stage { background: transparent; justify-content: center; }
    :host([compact]) .content { padding: 12px; }
    :host([compact]) .intro { margin-bottom: 12px; gap: 8px; }
    :host([compact]) .spark { width: 26px; height: 26px; flex-basis: 26px; border-radius: 8px; }
    :host([compact]) .spark svg { width: 14px; height: 14px; }
    :host([compact]) h2 { font: var(--lv-type-body-compact, 14px/1.4 system-ui); }
    :host([compact]) .intro p { display: none; }
    :host([compact]) .canvas { padding: 0; border: 0; border-radius: 0; box-shadow: none; background: transparent; }
    :host([compact]) .card.trend { grid-column: 1 / -1; padding: 0; border: 0; background: transparent; }
    :host([compact]) .heading { display: none; }
    :host([compact]) .chart { height: 86px; }
    @media(max-width: 640px) { .canvas { gap: 10px; padding: 12px; } .card { padding: 12px; } .card.trend, .card.bars { grid-column: 1 / -1; } .card.bars .chart { height: 100px; } .steps { gap: 12px; } }
    @media(prefers-reduced-motion: reduce) { *, .canvas::after { animation: none !important; } .canvas::after { display: none; } .line { stroke-dashoffset: 0; } }
  `

  render() {
    const state = this.loading
      ? { stage: 'discover', label: 'Opening dashboard builder', detail: 'Loading your dashboard canvas.' }
      : dashboardGenerationState(this.transcript, this.runId, this.running, this.error)
    const stages = ['discover', 'assemble', 'query', 'preview']
    const active = stages.indexOf(state.stage)
    return html`<section class=${`stage${this.running ? '' : ' paused'}`} aria-label="Dashboard generation" aria-busy=${String(this.running)}>
      <div class="toolbar">${lucideIcon(LayoutDashboard)}<span>Dashboard builder</span><span class="tag">${this.loading ? 'Loading' : this.running ? 'Generating' : 'Paused'}</span></div>
      <div class="content">
        <div class="intro" role="status" aria-live="polite"><span class="spark" aria-hidden="true">${lucideIcon(Sparkles)}</span><div><h2>${state.label}</h2><p>${state.detail}</p></div></div>
        <div class="canvas" aria-hidden="true">
          ${[0, 1, 2].map(i => html`<div class="card metric-card" style=${`--delay:${i * .15}s`}><div class="skeleton heading"></div><div class="skeleton metric"></div><div class="skeleton foot"></div></div>`)}
          <div class="card trend" style="--delay:.3s"><div class="skeleton heading"></div><svg class="chart" viewBox="0 0 400 150" preserveAspectRatio="none"><path class="grid" d="M0 30H400M0 65H400M0 100H400M0 135H400"></path><path class="area" d="M0 119L43 99L78 110L116 75L153 86L196 49L229 65L270 37L309 54L355 23L400 35V145H0Z"></path><path class="line" d="M0 119L43 99L78 110L116 75L153 86L196 49L229 65L270 37L309 54L355 23L400 35"></path></svg></div>
          <div class="card bars" style="--delay:.45s"><div class="skeleton heading"></div><svg class="chart" viewBox="0 0 180 150" preserveAspectRatio="none"><path class="grid" d="M0 30H180M0 65H180M0 100H180M0 135H180"></path>${[57, 89, 72, 113, 98, 128].map((height, i) => svg`<rect class="bar" style=${`--delay:${i * .12}s`} x=${i * 30 + 5} y=${140 - height} width="18" height=${height} rx="3"></rect>`)}</svg></div>
        </div>
        <p class="caption">Illustrative layout · your charts appear as they’re ready</p>
        ${this.running && !this.loading ? html`<div class="steps" aria-hidden="true">${['Find data', 'Build layout', 'Query visuals', 'Prepare preview'].map((label, i) => html`<span class=${`step${i === active ? ' current' : ''}`}>${i < active ? lucideIcon(Check) : html`<i class="dot"></i>`}${label}</span>`)}</div>` : nothing}
      </div>
    </section>`
  }
}

customElements.define('lv-dashboard-generation', DashboardGeneration)
