import { LitElement, html } from 'lit'
import { property } from 'lit/decorators.js'
import { displayRunDuration, hasLiveRunDuration, LiveRunDurationClock } from './live-run-duration'

/** Updates its own text without rerendering the table or investigation graph. */
class LiveRunDuration extends LitElement {
  @property({ type: String }) status = ''
  @property({ type: String }) startedAt = ''
  @property({ type: String }) finishedAt = ''
  @property({ type: String }) recordedDuration = ''
  @property({ type: Boolean }) live = true
  private readonly clock = new LiveRunDurationClock(this, () => this.live && hasLiveRunDuration(this.status, this.startedAt, this.finishedAt))

  createRenderRoot(): HTMLElement { return this }

  render() {
    return html`${displayRunDuration(this.status, this.startedAt, this.finishedAt, this.recordedDuration, this.clock.nowMs)}`
  }
}

if (!customElements.get('lv-live-run-duration')) customElements.define('lv-live-run-duration', LiveRunDuration)
