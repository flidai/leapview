declare const __PLAYGROUND_BUILD_ID__: string
export {}

type BuildEvent = { type: 'rebuilding' | 'rebuilt' | 'error'; buildID: string; message?: string }

// The non-watching test server returns HTTP 204, which stops EventSource retries.
const events = new EventSource('/__playground/events')
let connected = false
let reloading = false
let indicator: HTMLDivElement | undefined

function showStatus(message: string, error = '') {
  if (!indicator) {
    indicator = document.createElement('div')
    indicator.id = 'playground-build-status'
    // Keep build failures visible even above an expanded native visual dialog.
    indicator.popover = 'manual'
    const shadow = indicator.attachShadow({ mode: 'open' })
    shadow.innerHTML = `<style>
      :host { position:fixed; inset:auto 12px 12px auto; margin:0; padding:0; border:0; background:transparent; z-index:2147483647; max-width:min(38rem,calc(100vw - 24px)); color:var(--lv-fg-default); font:var(--lv-type-body-compact,14px system-ui); }
      section { border:var(--lv-border-default,1px solid currentColor); border-radius:var(--lv-radius-default,6px); background:var(--lv-bg-overlay,white); padding:10px 14px; box-shadow:var(--shadow-floating-small); }
      p { margin:0; } pre { margin:8px 0 0; max-height:40vh; overflow:auto; white-space:pre-wrap; overflow-wrap:anywhere; font:12px monospace; }
      [hidden] { display:none; }
    </style><section><p role="status"></p><pre role="alert" tabindex="0" hidden></pre></section>`
    document.body.append(indicator)
  }
  indicator.hidden = false
  indicator.shadowRoot!.querySelector('p')!.textContent = message
  const detail = indicator.shadowRoot!.querySelector('pre')!
  detail.textContent = error
  detail.hidden = !error
  if (!indicator.matches(':popover-open')) indicator.showPopover()
}

function onBuild(event: MessageEvent<string>) {
  const state = JSON.parse(event.data) as BuildEvent
  connected = true
  if (state.type === 'rebuilding') showStatus('Rebuilding…')
  else if (state.type === 'error') showStatus('Build failed · save a correction to retry', state.message)
  else if (state.buildID !== __PLAYGROUND_BUILD_ID__ && !reloading) {
    reloading = true
    showStatus('Reloading…')
    window.dispatchEvent(new Event('playground-before-reload'))
    events.close()
    location.reload()
  } else if (indicator) { indicator.hidePopover(); indicator.hidden = true }
}

events.addEventListener('rebuilding', onBuild as EventListener)
events.addEventListener('rebuilt', onBuild as EventListener)
events.addEventListener('error', event => {
  if (event instanceof MessageEvent) onBuild(event)
  else if (connected && !reloading) showStatus('Rebuild connection lost · reconnecting…')
})
window.addEventListener('pagehide', () => events.close(), { once: true })
