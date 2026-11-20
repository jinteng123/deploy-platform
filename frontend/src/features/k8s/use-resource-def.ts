import { useQuery } from '@tanstack/react-query'
import { k8sApi, type K8sObjectParams, type K8sResourceType } from '@/api/client'

// useResourceDef resolves the allowlist entry (with write capability
// flags) for the current GVR — shared by the action bar and the YAML
// editor so both gate on the same server-side truth.
export function useResourceDef(params: K8sObjectParams): K8sResourceType | undefined {
  const types = useQuery({
    queryKey: ['k8s', 'resource-types'],
    queryFn: () => k8sApi.resourceTypes(),
    staleTime: 60_000,
  })
  return types.data?.data?.find(
    (t) =>
      t.group === (params.group ?? '') &&
      t.version === params.version &&
      t.resource === params.resource
  )
}
