import { expect, test } from 'bun:test'
import type { PersonalSessionSignal } from '../../generated/signals'
import { browserNameFromClientHints, browserSessionLabel } from './personal-settings-session-rows'

test('current session browser hints prefer a known browser brand over Chromium', () => {
  const chromeUserAgent = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36'

  expect(browserNameFromClientHints(chromeUserAgent, ['Chromium', 'Microsoft Edge'])).toBe('Edge')
  expect(browserNameFromClientHints(chromeUserAgent, ['Opera', 'Chromium'])).toBe('Opera')
  expect(browserNameFromClientHints(chromeUserAgent, ['Brave', 'Chromium'])).toBe('Brave')
  expect(browserNameFromClientHints(chromeUserAgent, ['Chromium'])).toBe('Chrome')
  expect(browserNameFromClientHints(`${chromeUserAgent} Vivaldi/7.5`, ['Chromium'])).toBe('Vivaldi')
  expect(browserNameFromClientHints(`${chromeUserAgent} YaBrowser/25.8`, ['Chromium'])).toBe('Yandex Browser')
  expect(browserNameFromClientHints('Mozilla/5.0 (Linux; Android 15) Chrome/140.0.0.0 SamsungBrowser/28.0', [])).toBe('Samsung Internet')
  expect(browserNameFromClientHints('Mozilla/5.0 (iPhone) Version/18.0 Safari/604.1 DuckDuckGo/7', [])).toBe('DuckDuckGo')
})

test('current session label uses the detected browser and preserves other session labels', () => {
  const currentSession = {
    id: 'current',
    kind: 'browser',
    clientLabel: 'Chrome on Linux',
    current: true,
  } as PersonalSessionSignal
  const olderSession = { ...currentSession, id: 'older', current: false }

  expect(browserSessionLabel(currentSession, 'Edge')).toBe('Edge on Linux')
  expect(browserSessionLabel(currentSession, 'Brave')).toBe('Brave on Linux')
  expect(browserSessionLabel(olderSession, 'Edge')).toBe('Chrome on Linux')
})
