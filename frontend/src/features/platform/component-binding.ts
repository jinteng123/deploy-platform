import type { ComponentBindingPayload, K8sObject } from '@/api/client'

export function componentSourceLabel(
  sourceKind?: ComponentBindingPayload['source_kind'],
  sourceRepoURL?: string
): string {
  if (sourceKind === 'third_party') return '已确认：第三方镜像'
  if (sourceKind === 'repository' || sourceRepoURL)
    return sourceRepoURL || '代码仓库待登记'
  return '代码来源待确认'
}

function objectRecord(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined
}

export function servicesSelectingWorkload(
  deployment: K8sObject | undefined,
  services: K8sObject[] | undefined
): string[] | undefined {
  const template = objectRecord(deployment?.spec?.template)
  const templateMetadata = objectRecord(template?.metadata)
  const labels = objectRecord(templateMetadata?.labels)
  if (!labels || Object.keys(labels).length === 0 || !services) return undefined

  return services.flatMap((service) => {
    const selector = objectRecord(objectRecord(service.spec)?.selector)
    if (!selector || Object.keys(selector).length === 0) return []
    const matches = Object.entries(selector).every(
      ([key, value]) => typeof value === 'string' && labels[key] === value
    )
    return matches && service.metadata?.name ? [service.metadata.name] : []
  })
}

export function autoscalersForWorkload(
  deploymentName: string,
  autoscalers: K8sObject[] | undefined
): K8sObject[] | undefined {
  if (!autoscalers) return undefined
  return autoscalers.filter((autoscaler) => {
    const target = objectRecord(objectRecord(autoscaler.spec)?.scaleTargetRef)
    return target?.kind === 'Deployment' && target.name === deploymentName
  })
}

export function repositoryOf(image: string): string {
  if (!image || /\s|:\/\/|[?#]/.test(image) || image.split('@').length > 2)
    return ''
  const [reference, digest] = image.split('@')
  if (digest !== undefined && !/^sha256:[a-f0-9]{64}$/i.test(digest)) return ''
  const lastColon = reference.lastIndexOf(':')
  if (
    digest === undefined &&
    (lastColon <= reference.lastIndexOf('/') || !reference.slice(lastColon + 1))
  )
    return ''
  const repo =
    lastColon > reference.lastIndexOf('/')
      ? reference.slice(0, lastColon)
      : reference
  const [host, ...parts] = repo.split('/')
  if (!host || !parts.length || (!/[.:]/.test(host) && host !== 'localhost'))
    return ''
  if (!/^(localhost|[a-z0-9][a-z0-9.-]*)(:[0-9]{1,5})?$/.test(host)) return ''
  return parts.every((p) => /^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$/.test(p))
    ? repo
    : ''
}

export function sourceRepositoryIdentity(value: string): string {
  const raw = value.trim()
  if (!raw || /\s|\\/.test(raw)) return ''
  let host: string
  let repositoryPath: string
  if (raw.startsWith('git@') && !raw.includes('://')) {
    const remote = raw.slice(4)
    const separator = remote.indexOf(':')
    if (separator <= 0) return ''
    host = remote.slice(0, separator)
    repositoryPath = remote.slice(separator + 1)
  } else if (!raw.includes('://') && raw.includes('/')) {
    // Build event metadata is stored in this canonical host/path form by the
    // backend; accept it alongside the URLs entered in component bindings.
    const separator = raw.indexOf('/')
    host = raw.slice(0, separator).toLowerCase()
    repositoryPath = raw.slice(separator + 1)
    if (!/^[a-z0-9][a-z0-9.-]*(?::[0-9]{1,5})?$/.test(host)) return ''
  } else {
    try {
      const url = new URL(raw)
      if (!['https:', 'http:', 'ssh:', 'git:'].includes(url.protocol)) return ''
      if (
        (url.username &&
          !(url.protocol === 'ssh:' && url.username === 'git')) ||
        url.password ||
        url.search ||
        url.hash
      )
        return ''
      host = url.hostname.toLowerCase()
      const defaultPort =
        (url.protocol === 'https:' && url.port === '443') ||
        (url.protocol === 'http:' && url.port === '80') ||
        (url.protocol === 'ssh:' && url.port === '22')
      if (url.port && !defaultPort) host += `:${url.port}`
      repositoryPath = decodeURIComponent(url.pathname)
    } catch {
      return ''
    }
  }
  repositoryPath = repositoryPath.replace(/^\/+|\/+$/g, '')
  if (!host || !repositoryPath || /[?#\s\\]/.test(repositoryPath)) return ''
  if (
    repositoryPath
      .split('/')
      .some((part) => !part || part === '.' || part === '..')
  )
    return ''
  if (repositoryPath.endsWith('.git'))
    repositoryPath = repositoryPath.slice(0, -4)
  return repositoryPath && !repositoryPath.endsWith('/')
    ? `${host.toLowerCase()}/${repositoryPath}`
    : ''
}

function validRepositorySubpath(value: string): boolean {
  if (!value) return true
  if (value === '.') return true
  if (value.length > 512 || value.startsWith('/') || value.includes('\\'))
    return false
  const parts = value.split('/')
  return parts.every((part) => part && part !== '.' && part !== '..')
}

export function matchCIEventToBinding(
  sample: {
    appName: string
    targetCluster: string
    imageFull: string
    sourceRepository?: string
    sourcePath?: string
  },
  expected: {
    appName: string
    targetCluster: string
    imageRepository: string
    sourceKind?: ComponentBindingPayload['source_kind']
    sourceRepository?: string
    sourcePath?: string
  }
): { matches: boolean; message: string } {
  const errors: string[] = []
  const appName = sample.appName.trim()
  const targetCluster = sample.targetCluster.trim()
  const imageRepository = repositoryOf(sample.imageFull.trim())

  if (!appName || appName !== expected.appName.trim()) {
    errors.push(`APP_NAME 应为 ${expected.appName.trim() || '组件标识'}`)
  }
  if (!targetCluster || targetCluster !== expected.targetCluster.trim()) {
    errors.push(
      `TARGET_CLUSTER 应为 ${expected.targetCluster.trim() || '目标环境代码'}`
    )
  }
  if (!imageRepository || imageRepository !== expected.imageRepository.trim()) {
    errors.push(
      `IMAGE_FULL 必须属于 ${expected.imageRepository.trim() || '已绑定的镜像仓库'}`
    )
  }

  if (expected.sourceKind === 'repository') {
    const expectedRepository = sourceRepositoryIdentity(
      expected.sourceRepository ?? ''
    )
    const reportedRepository = sourceRepositoryIdentity(
      sample.sourceRepository ?? ''
    )
    if (!reportedRepository || reportedRepository !== expectedRepository) {
      errors.push('sources.repo 应与组件登记的代码仓库一致')
    }
    const expectedPath = expected.sourcePath?.trim() || '.'
    const reportedPath = sample.sourcePath?.trim() ?? ''
    if (!validRepositorySubpath(reportedPath)) {
      errors.push('SOURCE_PATH 必须是仓库内相对目录')
    } else if (expectedPath !== '.' && !reportedPath) {
      errors.push(`SOURCE_PATH 应为 ${expectedPath}`)
    } else if (reportedPath && reportedPath !== expectedPath) {
      errors.push(`SOURCE_PATH 应为 ${expectedPath}`)
    }
  }

  if (errors.length) {
    return { matches: false, message: errors.join('；') }
  }

  return {
    matches: true,
    message:
      expected.sourceKind === 'repository'
        ? `${appName} + ${targetCluster} → ${sourceRepositoryIdentity(expected.sourceRepository ?? '')} · ${expected.sourcePath?.trim() || '.'} → ${imageRepository}`
        : `${appName} + ${targetCluster} → ${imageRepository}`,
  }
}

export function workloadContainers(
  object?: K8sObject
): { name: string; image: string }[] {
  const template = object?.spec?.template as
    | { spec?: { containers?: unknown[] } }
    | undefined
  return (template?.spec?.containers ?? []).flatMap((raw) => {
    const c = raw as { name?: unknown; image?: unknown } | null
    return c && typeof c.name === 'string' && typeof c.image === 'string'
      ? [{ name: c.name, image: c.image }]
      : []
  })
}

export function ciEventExample(
  binding: ComponentBindingPayload,
  envCode: string
) {
  const event: Record<string, string> = {
    event_id: '<每次构建唯一的 ID>',
    target_cluster: envCode,
    app_name: binding.app_name,
    image_full: `${binding.image_repository}:<构建版本>`,
    image_digest: '<CI 推送后获得的 sha256 digest>',
    commit_id: '<代码提交 SHA>',
  }
  if (binding.source_kind === 'repository' && binding.source_repo_url) {
    event.source_repository = binding.source_repo_url
    event.source_path = binding.source_path.trim() || '.'
  }
  return JSON.stringify(event, null, 2)
}
