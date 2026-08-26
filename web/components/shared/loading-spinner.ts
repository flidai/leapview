import { LitElement, css, html } from 'lit'
import { property } from 'lit/decorators.js'

type SpinnerSize = 'small' | 'medium' | 'large'

const animationDurationMilliseconds = 1000

class LeapViewLoadingSpinner extends LitElement {
  @property({ reflect: true }) size: SpinnerSize = 'medium'

  static styles = css`
    :host {
      display: inline-flex;
      width: var(--spinner-size-medium);
      height: var(--spinner-size-medium);
      flex: 0 0 auto;
      color: inherit;
    }

    :host([size='small']) {
      width: var(--spinner-size-small);
      height: var(--spinner-size-small);
    }

    :host([size='large']) {
      width: var(--spinner-size-large);
      height: var(--spinner-size-large);
    }

    svg {
      width: 100%;
      height: 100%;
      transform-origin: center;
      animation: primer-spinner-rotate var(--base-duration-1000) var(--base-easing-linear) infinite;
    }

    @keyframes primer-spinner-rotate {
      100% {
        transform: rotate(360deg);
      }
    }

    @media (prefers-reduced-motion: reduce) {
      svg {
        animation: none;
      }
    }
  `

  connectedCallback() {
    super.connectedCallback()
    if (this.getAttribute('aria-hidden') === 'true') return
    if (!this.hasAttribute('role')) this.setAttribute('role', 'status')
    if (!this.hasAttribute('aria-label')) this.setAttribute('aria-label', 'Loading')
  }

  render() {
    const now = typeof performance === 'undefined' ? 0 : performance.now()
    const animationDelay = -(now % animationDurationMilliseconds)
    return html`
      <svg
        viewBox="0 0 16 16"
        fill="none"
        aria-hidden="true"
        style=${`animation-delay: ${animationDelay}ms`}
      >
        <circle
          cx="8"
          cy="8"
          r="7"
          stroke="currentColor"
          stroke-opacity="0.25"
          stroke-width="2"
          vector-effect="non-scaling-stroke"
        ></circle>
        <path
          d="M15 8a7.002 7.002 0 00-7-7"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          vector-effect="non-scaling-stroke"
        ></path>
      </svg>
    `
  }
}

if (!customElements.get('lv-loading-spinner')) customElements.define('lv-loading-spinner', LeapViewLoadingSpinner)

declare global {
  interface HTMLElementTagNameMap {
    'lv-loading-spinner': LeapViewLoadingSpinner
  }
}
