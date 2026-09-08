import { expect, test } from 'bun:test'
import { exploreReturnLink } from './explore-return'

test('explorer return context produces a same-origin dashboard path', () => {
  expect(exploreReturnLink('?returnSurface=dashboard&returnDashboard=dashboard%3Asales&returnPage=overview')).toEqual({
    href: '/dashboards/dashboard%3Asales/pages/overview', label: 'Back to dashboard',
  })
})

test('explorer rejects raw or malformed return destinations', () => {
  expect(exploreReturnLink('?returnSurface=dashboard&returnDashboard=https%3A%2F%2Fevil.example&returnPage=overview')).toBeUndefined()
  expect(exploreReturnLink('?returnSurface=explore&returnDashboard=dashboard%3Asales&returnPage=overview')).toBeUndefined()
  expect(exploreReturnLink('?returnSurface=chat&returnConversation=https%3A%2F%2Fevil.example')).toBeUndefined()
  expect(exploreReturnLink('?returnSurface=chat&returnConversation=%2Fadmin')).toBeUndefined()
})

test('explorer return context produces a same-origin chat path', () => {
  expect(exploreReturnLink('?returnSurface=chat&returnConversation=conversation%3Asales')).toEqual({
    href: '/chats/conversation%3Asales', label: 'Back to chat',
  })
})

test('explorer return context produces a same-origin model data path', () => {
  expect(exploreReturnLink('?returnSurface=model&returnAsset=model%3Aorders&returnSection=data')).toEqual({
    href: '/models/model%3Aorders/data', label: 'Back to model',
  })
  expect(exploreReturnLink('?returnSurface=model&returnAsset=model%3Aorders&returnSection=javascript')).toBeUndefined()
})
