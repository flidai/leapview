import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { AdminAgentSignal } from '../../generated/signals'
import './agent-prompt-editor'
import './agent-tools'

type AgentSettingsTab = 'instructions' | 'tools'

/**
 * The complete admin Agent surface. The page owns the signal and prompt
 * values; this component owns the compact overview and the workspace tabs.
 */
export class AgentSettings extends LitElement {
  @property({ attribute: false }) agent: AdminAgentSignal | null = null
  @property({ attribute: false }) prompt = ''
  @state() private tab: AgentSettingsTab = 'instructions'

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    h2,
    h3,
    p {
      margin: 0;
    }

    .surface {
      display: grid;
      min-width: 0;
      gap: var(--base-size-20);
    }

    .overview {
      display: grid;
      min-width: 0;
      padding: var(--base-size-4) 0 var(--base-size-20);
      border-bottom: var(--lv-border-muted);
    }

    .visually-hidden {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0 0 0 0);
      clip-path: inset(50%);
      white-space: nowrap;
    }

    .status-value {
      display: inline-flex;
      width: fit-content;
      align-items: center;
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel-muted);
      padding: var(--base-size-2) var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      white-space: nowrap;
    }

    .status-value.is-enabled {
      background: var(--lv-bg-success-muted, var(--lv-bg-panel-muted));
      color: var(--lv-fg-success);
    }

    .status-value.is-disabled {
      background: var(--lv-bg-danger-muted, var(--lv-bg-panel-muted));
      color: var(--lv-fg-danger);
    }

    .overview-grid {
      display: grid;
      grid-template-columns: repeat(5, minmax(0, 1fr));
      gap: var(--base-size-8);
    }

    .overview-stat {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
      border-left: var(--lv-border-muted);
      padding: 0 var(--base-size-12);
    }

    .overview-stat:first-child {
      border-left: 0;
      padding-left: 0;
    }

    .overview-stat:last-child {
      padding-right: 0;
    }

    .overview-label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
      letter-spacing: .03em;
    }

    .overview-value {
      overflow: hidden;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .notice {
      display: flex;
      align-items: start;
      gap: var(--base-size-8);
      border-left: 2px solid var(--lv-line-muted);
      padding: var(--base-size-4) 0 var(--base-size-4) var(--base-size-12);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
      line-height: var(--base-text-lineHeight-snug);
    }

    .notice strong {
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
    }

    .workspace {
      min-width: 0;
    }

    .tab-bar {
      display: flex;
      align-items: center;
      gap: var(--base-size-4);
      border-bottom: var(--lv-border-muted);
      padding: 0;
    }

    .tab-bar button {
      position: relative;
      border: 0;
      border-radius: var(--lv-radius-small) var(--lv-radius-small) 0 0;
      background: transparent;
      padding: var(--base-size-8) var(--base-size-12);
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
    }

    .tab-bar button:hover {
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-default);
    }

    .tab-bar button.is-active {
      color: var(--lv-fg-accent);
    }

    .tab-bar button.is-active::after {
      position: absolute;
      right: var(--base-size-8);
      bottom: -1px;
      left: var(--base-size-8);
      height: 2px;
      background: var(--lv-fg-accent);
      content: '';
    }

    .tab-bar button:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: -2px;
    }

    .workspace-body {
      min-width: 0;
      padding-top: var(--base-size-16);
    }

    .empty {
      padding: var(--base-size-16) 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    @media (max-width: 760px) {
      .overview-grid {
        grid-template-columns: repeat(2, minmax(0, 1fr));
        row-gap: var(--base-size-16);
      }

      .overview-stat {
        border-left: 0;
        padding: 0;
      }
    }

    @media (max-width: 480px) {
      .overview-grid {
        grid-template-columns: minmax(0, 1fr);
      }

      .workspace-body {
        padding-top: var(--base-size-12);
      }
    }
  `

  render() {
    const agent = this.agent
    if (!agent) return html`<div class="surface"><p class="empty">Agent settings are unavailable.</p></div>`

    const prompt = this.prompt || agent.systemPrompt || ''
    const enabledLabel = agent.enabled ? 'Enabled' : 'Disabled'
    return html`
      <div class="surface">
        <section class="overview" aria-label="Agent overview">
          <h2 class="visually-hidden">Agent overview</h2>
          <div class="overview-grid">
            ${this.stat('Status', enabledLabel, agent.enabled ? 'enabled' : 'disabled')}
            ${this.stat('Model', agent.model || 'Not configured')}
            ${this.stat('Tools', String(agent.tools.length))}
            ${this.stat('Access', agent.canWrite ? 'Editable' : 'Read-only')}
            ${this.stat('Instructions', prompt.trim() ? 'Configured' : 'Not configured')}
          </div>
        </section>

        ${!agent.canWrite ? html`
          <div class="notice" role="note">
            <strong>Deployment managed.</strong>
            <span>Agent instructions are controlled by deployment configuration and cannot be changed here.</span>
          </div>
        ` : ''}

        <section class="workspace" aria-label="Agent workspace">
          <div class="tab-bar" role="tablist" aria-label="Agent settings sections">
            ${this.renderTab('instructions', 'Instructions')}
            ${this.renderTab('tools', 'Tools')}
          </div>
          <div class="workspace-body">
            ${this.tab === 'instructions'
              ? html`<div role="tabpanel" aria-label="Instructions"><lv-agent-prompt-editor .value=${prompt} value=${prompt} ?disabled=${!agent.canWrite}></lv-agent-prompt-editor></div>`
              : html`<div role="tabpanel" aria-label="Tools"><lv-agent-tools .tools=${agent.tools}></lv-agent-tools></div>`}
          </div>
        </section>
      </div>
    `
  }

  private stat(label: string, value: string, status: 'enabled' | 'disabled' | '' = '') {
    return html`
      <div class="overview-stat">
        <span class="overview-label">${label}</span>
        <span class=${status ? `status-value is-${status}` : 'overview-value'} title=${value}>${value}</span>
      </div>
    `
  }

  private renderTab(tab: AgentSettingsTab, label: string) {
    const active = this.tab === tab
    return html`
      <button
        class=${active ? 'is-active' : ''}
        type="button"
        role="tab"
        aria-selected=${String(active)}
        @click=${() => { this.tab = tab }}
      >${label}</button>
    `
  }
}

if (!customElements.get('lv-agent-settings')) customElements.define('lv-agent-settings', AgentSettings)
