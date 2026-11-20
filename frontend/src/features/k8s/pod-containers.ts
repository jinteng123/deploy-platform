import type { K8sObject } from '@/api/client'

// podContainers lists the Pod's own containers — spec.containers plus
// init/ephemeral containers, not the workload template path used by the
// action dialogs.
export function podContainers(obj: K8sObject | undefined): string[] {
  const spec = obj?.spec as Record<string, unknown> | undefined
  const names: string[] = []
  for (const field of ['containers', 'initContainers', 'ephemeralContainers']) {
    const list = spec?.[field]
    if (Array.isArray(list)) {
      for (const c of list) {
        const n = (c as Record<string, unknown>).name
        if (typeof n === 'string' && n !== '') names.push(n)
      }
    }
  }
  return names
}
