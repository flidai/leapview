import type { PersonalCapabilityOptionSignal, PersonalPermissionPairSignal } from '../../generated/signals'

export type TokenPermissionOption = {
  id: string
  label: string
  permissions: PersonalPermissionPairSignal[]
}

export type TokenPermissionPolicy = {
  id: string
  label: string
  description: string
  category: string
  searchText: string
  targetScope: string
  options: TokenPermissionOption[]
  future?: TokenPermissionOption
}

export function tokenPermissionPolicies(capabilities: PersonalCapabilityOptionSignal[]): TokenPermissionPolicy[] {
  const policies = new Map<string, TokenPermissionPolicy>()
  capabilities.forEach((capability) => {
    const permissions = uniquePermissionPairs(capability.permissions ?? [])
    if (!permissions.length) return
    const option: TokenPermissionOption = { id: permissionOptionID(permissions), label: capability.label, permissions }
    const pair = permissions[0]
    if (permissions.length !== 1 || !pair) {
      const id = `bundle:${option.id}`
      policies.set(id, {
        id, label: capability.label, description: capability.description, category: capability.category,
        searchText: `${capability.value} ${capability.actionLabel} ${capability.label} ${capability.description}`, targetScope: 'fixed', options: [option],
      })
      return
    }
    const target = pair.target
    const resourcePolicy = target.scope === 'resource' || Boolean(target.includeFuture)
    const targetScope = resourcePolicy ? 'resource' : target.scope
    const resourceKind = target.resourceKind ?? ''
    const key = JSON.stringify([pair.profile, pair.action, targetScope, target.projectId ?? '', target.instanceId ?? '', resourceKind])
    const actionLabel = capability.actionLabel?.trim() || capability.label
    const description = resourcePolicy ? capability.category : capability.description
    const policy = policies.get(key) ?? {
      id: key,
      label: actionLabel,
      description,
      category: capability.category,
      searchText: '',
      targetScope,
      options: [],
    }
    policy.searchText += ` ${capability.value} ${capability.label} ${capability.description} ${pair.action}`
    if (target.includeFuture) policy.future = option
    else policy.options.push(option)
    policies.set(key, policy)
  })
  return [...policies.values()].sort((left, right) => left.category.localeCompare(right.category) || left.label.localeCompare(right.label))
}

export function groupTokenPermissionPolicies(policies: TokenPermissionPolicy[]): Array<[string, TokenPermissionPolicy[]]> {
  const groups = new Map<string, TokenPermissionPolicy[]>()
  for (const policy of policies) {
    const values = groups.get(policy.category) ?? []
    values.push(policy)
    groups.set(policy.category, values)
  }
  return [...groups.entries()]
}

export function uniquePermissionPairs(permissions: PersonalPermissionPairSignal[]): PersonalPermissionPairSignal[] {
  const seen = new Set<string>()
  return permissions.filter((permission) => {
    const key = permissionPairKey(permission)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

export function permissionPairKey(permission: PersonalPermissionPairSignal): string {
  const target = permission.target
  return JSON.stringify([
    permission.profile,
    permission.action,
    target.scope,
    target.instanceId ?? '',
    target.projectId ?? '',
    target.resourceKind ?? '',
    target.resourceId ?? '',
    target.includeFuture ?? false,
  ])
}

function permissionOptionID(permissions: PersonalPermissionPairSignal[]): string {
  const identities = permissions.map(permissionPairKey).sort()
  return `permission:${JSON.stringify(identities)}`
}

export function permissionLabelsByPair(capabilities: PersonalCapabilityOptionSignal[]): Map<string, string> {
  const labels = new Map<string, string>()
  for (const capability of capabilities) {
    for (const permission of capability.permissions ?? []) {
      const key = permissionPairKey(permission)
      if (!labels.has(key)) labels.set(key, `${capability.label} · ${capability.description}`)
    }
  }
  return labels
}

export function formatTechnicalPermissionTarget(permission: PersonalPermissionPairSignal): string {
  const target = permission.target
  if (target.resourceId) return `${target.resourceKind ?? 'resource'} ${target.resourceId} · project ${target.projectId ?? 'unknown'}`
  if (target.includeFuture) return `${target.resourceKind ?? 'resource'} · current and future resources · project ${target.projectId ?? 'unknown'}`
  if (target.projectId) return `project ${target.projectId}`
  if (target.instanceId) return `instance ${target.instanceId}`
  return target.scope
}

export function formatPermissionPair(permission: PersonalPermissionPairSignal): string {
  const target = permission.target.resourceId
    ? `${permission.target.resourceKind ?? 'resource'} ${permission.target.resourceId}`
    : permission.target.projectId
      ? `Project ${permission.target.projectId}`
      : permission.target.instanceId
        ? `Instance ${permission.target.instanceId}`
        : permission.target.scope
  return `${permission.action} · ${target}`
}
