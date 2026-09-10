type DataExplorerTransportLane = 'semantic' | 'suggestion' | 'stop'

type DataExplorerTransportCommand = {
  action?: unknown
  clientId?: unknown
  explore?: { action?: unknown; filterSuggestions?: unknown }
}

type DataExplorerTransportBridge = {
  requestCancellation: (command: DataExplorerTransportCommand) => AbortController
}

declare global {
  interface Window {
    LeapViewDataExplorerTransport?: DataExplorerTransportBridge
  }
}

const controllersByClient = new Map<string, Partial<Record<DataExplorerTransportLane, AbortController>>>()

function laneFor(command: DataExplorerTransportCommand): DataExplorerTransportLane {
  const nestedAction = typeof command.explore?.action === 'string' ? command.explore.action.trim().toLowerCase() : ''
  const action = nestedAction || (typeof command.action === 'string' ? command.action.trim().toLowerCase() : '')
  if (command.explore?.filterSuggestions !== undefined && action === 'configure') return 'suggestion'
  if (action === 'stop') return 'stop'
  return 'semantic'
}

function clientFor(command: DataExplorerTransportCommand): string {
  return typeof command.clientId === 'string' && command.clientId.trim() ? command.clientId.trim() : 'default'
}

function requestCancellation(command: DataExplorerTransportCommand): AbortController {
  const scopes = controllersByClient.get(clientFor(command)) ?? {}
  const lane = laneFor(command)
  scopes[lane]?.abort()
  const controller = new AbortController()
  scopes[lane] = controller
  controllersByClient.set(clientFor(command), scopes)
  return controller
}

export function releaseDataExplorerTransport(clientID?: unknown): void {
  const client = typeof clientID === 'string' && clientID.trim() ? clientID.trim() : 'default'
  const scopes = controllersByClient.get(client)
  if (!scopes) return
  for (const controller of Object.values(scopes)) controller?.abort()
  controllersByClient.delete(client)
}

if (typeof window !== 'undefined' && !window.LeapViewDataExplorerTransport) {
  window.LeapViewDataExplorerTransport = { requestCancellation }
}
