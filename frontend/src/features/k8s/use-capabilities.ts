import { useQuery } from '@tanstack/react-query'
import {
  capsKey,
  k8sApi,
  type K8sObjectParams,
  type K8sResourceCaps,
} from '@/api/client'

// One SelfSubjectRulesReview per (cluster, user, namespace) — cached briefly
// so every gated control on the page shares the same verdict. Write controls
// fail closed: a missing/errored verdict hides the button, and the backend
// still enforces RBAC on every call.
export function useCapabilities(clusterId?: number, namespace?: string) {
  return useQuery({
    queryKey: ['k8s', 'capabilities', clusterId, namespace ?? ''],
    queryFn: () => k8sApi.capabilities(clusterId!, namespace).then((r) => r.data),
    enabled: clusterId != null,
    staleTime: 30_000,
  })
}

// Per-resource verdict for the object a detail page is showing.
export function useResourceCaps(params: K8sObjectParams): {
  query: ReturnType<typeof useCapabilities>
  caps: K8sResourceCaps | undefined
} {
  const query = useCapabilities(params.cluster_id, params.namespace)
  return { query, caps: query.data?.resources[capsKey(params)] }
}
