import type {
  ComponentBinding,
  ImageReleaseView,
  VerificationEvidence,
} from '@/api/client'

function imageDigest(image?: string | null) {
  return image?.match(/(?:@|^)(sha256:[a-f\d]{64})$/i)?.[1]?.toLowerCase()
}

export function safeReportURL(value: string) {
  try {
    const url = new URL(value)
    if (
      (url.protocol === 'http:' || url.protocol === 'https:') &&
      !url.username &&
      !url.password
    ) {
      return url.href
    }
  } catch {
    // Invalid or non-web report links are not navigable.
  }
  return undefined
}

export function evidenceForCurrentImage(
  releases: ImageReleaseView[] | undefined,
  app: ComponentBinding,
  actualImage: string | null | undefined,
  kind: 'verification' | 'scan'
): VerificationEvidence | undefined {
  const digest = imageDigest(actualImage)
  if (!digest) return undefined

  return releases
    ?.filter((release) => {
      if (
        release.app_name !== app.app_name ||
        release.image_digest?.toLowerCase() !== digest
      ) {
        return false
      }
      const targetsCluster =
        release.test_cluster_id === app.cluster_id ||
        release.prod_cluster_id === app.cluster_id ||
        release.deployments?.some(
          (deployment) => deployment.cluster_id === app.cluster_id
        )
      const evidence = release.metadata?.[kind]
      return targetsCluster && evidence?.digest.toLowerCase() === digest
    })
    .sort((a, b) => {
      const aRecordedAt = a.metadata?.[kind]?.recorded_at ?? a.created_at
      const bRecordedAt = b.metadata?.[kind]?.recorded_at ?? b.created_at
      return bRecordedAt.localeCompare(aRecordedAt)
    })[0]?.metadata?.[kind]
}

export function evidenceStatus(
  evidence: VerificationEvidence | undefined,
  actualImage: string | null | undefined,
  pending: boolean,
  error: boolean
) {
  if (pending) return { label: '读取中', tone: 'muted' as const }
  if (error) return { label: '暂不可查', tone: 'muted' as const }
  if (!imageDigest(actualImage)) {
    return { label: '无法精确关联', tone: 'muted' as const }
  }
  if (!evidence) return { label: '暂无匹配报告', tone: 'muted' as const }
  if (new Date(evidence.valid_until).getTime() <= Date.now()) {
    return { label: '已过期', tone: 'warning' as const }
  }
  return evidence.status === 'passed'
    ? { label: '通过', tone: 'success' as const }
    : { label: '失败', tone: 'danger' as const }
}
