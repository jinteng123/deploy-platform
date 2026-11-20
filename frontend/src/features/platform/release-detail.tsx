import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  ArrowUpRight,
  CheckCircle2,
  CircleHelp,
  Loader2,
  Undo2,
  XCircle,
} from 'lucide-react'
import { toast } from 'sonner'
import {
  platformApi,
  type DeployLog,
  type ImageReleaseView,
  type RollbackPayload,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import {
  componentSourceLabel,
  sourceRepositoryIdentity,
} from './component-binding'
import { safeReportURL } from './environment-evidence'
import {
  deployLogRunning,
  deployStepName,
  parseStepDetail,
  releaseBuildIdentity,
} from './release-status'

export function StatusBadge({ status }: { status?: string | null }) {
  if (!status) return <span className='text-muted-foreground'>-</span>
  const label: Record<string, string> = {
    pending: '等待中',
    running: '进行中',
    success: '成功',
    failed: '失败',
    interrupted: '需核对',
    paused: '已暂停',
    cancelled: '已取消',
  }
  return (
    <Badge
      variant='outline'
      className={
        status === 'success'
          ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-200'
          : status === 'failed'
            ? 'border-destructive/30 bg-destructive/10 text-destructive'
            : status === 'running'
              ? 'border-blue-300 bg-blue-50 text-blue-800 dark:border-blue-800 dark:bg-blue-950/50 dark:text-blue-200'
              : status === 'paused'
                ? 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-100'
                : 'border-border bg-muted text-muted-foreground'
      }
    >
      {label[status] ?? status}
    </Badge>
  )
}

export function DeployStepTimeline({
  log,
  fallbackStep,
}: {
  log: DeployLog
  fallbackStep?: string | null
}) {
  const steps = parseStepDetail(log.step_detail)
  const running = deployLogRunning(log.status)
  // While the deploy is in flight, current_step is the authoritative
  // position — step_detail only records completed steps.
  const activeStep = running ? (log.current_step ?? fallbackStep) : null
  if (steps.length === 0 && !activeStep) {
    return (
      <p className='text-xs text-muted-foreground'>
        {running ? '等待执行…' : '暂无步骤详情'}
      </p>
    )
  }
  return (
    <ol className='space-y-1.5'>
      {steps.map((s, i) => (
        <li key={`${s.step}-${i}`}>
          <div className='flex items-center gap-2'>
            {s.observation_status === 'unknown' ? (
              <CircleHelp className='size-4 shrink-0 text-amber-600 dark:text-amber-400' />
            ) : s.success ? (
              <CheckCircle2 className='size-4 shrink-0 text-emerald-600' />
            ) : (
              <XCircle className='size-4 shrink-0 text-destructive' />
            )}
            <span className='text-sm'>{deployStepName(s.step)}</span>
            {s.observation_status === 'unknown' && (
              <Badge
                variant='outline'
                className='text-amber-800 dark:text-amber-200'
              >
                无法核实
              </Badge>
            )}
            {s.observation_status === 'verified' && (
              <Badge
                variant='outline'
                className='border-emerald-300 text-emerald-800 dark:border-emerald-800 dark:text-emerald-200'
              >
                digest 一致
              </Badge>
            )}
          </div>
          {(s.stdout || s.stderr) && (
            <details className='ms-6 mt-1'>
              <summary className='cursor-pointer text-xs text-muted-foreground select-none'>
                查看输出
              </summary>
              {s.stdout ? (
                <pre className='mt-1 max-h-48 overflow-auto rounded bg-muted p-2 font-mono text-xs break-all whitespace-pre-wrap text-foreground'>
                  {s.stdout}
                </pre>
              ) : null}
              {s.stderr ? (
                <pre className='mt-1 max-h-48 overflow-auto rounded border border-destructive/25 bg-destructive/5 p-2 font-mono text-xs break-all whitespace-pre-wrap text-destructive'>
                  {s.stderr}
                </pre>
              ) : null}
            </details>
          )}
        </li>
      ))}
      {activeStep && (
        <li className='flex items-center gap-2'>
          <Loader2 className='size-4 shrink-0 animate-spin text-blue-600' />
          <span className='text-sm'>{deployStepName(activeStep)}</span>
        </li>
      )}
    </ol>
  )
}

function RollbackDialog({
  appName,
  log,
  onClose,
}: {
  appName: string
  log: DeployLog
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  // Mounted with open=true — the empty selection resolves to the first
  // history record once the query lands.
  const [selected, setSelected] = useState('')
  const [error, setError] = useState('')
  const history = useQuery({
    queryKey: ['rollback-history', appName, log.cluster_id],
    queryFn: () => platformApi.rollbackHistory(appName, log.cluster_id),
    retry: false,
  })
  const rollback = useMutation({
    mutationFn: (payload: RollbackPayload) => platformApi.rollback(payload),
    onSuccess: (res) => {
      toast.success(res.data?.message || '回滚任务已提交')
      queryClient.invalidateQueries({ queryKey: ['releases'] })
      queryClient.invalidateQueries({ queryKey: ['deploy-log', log.id] })
      onClose()
    },
    onError: (err) => {
      const detail = (err as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      setError(detail ?? '回滚提交失败')
    },
  })
  const records = history.data?.data ?? []
  const selectedId =
    selected || (records.length > 0 ? String(records[0].id) : '')
  const record = records.find((r) => String(r.id) === selectedId)
  const submit = () => {
    if (!record?.image_full) return
    rollback.mutate({
      cluster_id: log.cluster_id,
      target_log_id: record.id,
    })
  }
  return (
    <Dialog
      open={true}
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
    >
      <DialogContent className='sm:max-w-md'>
        <DialogHeader>
          <DialogTitle>回滚到历史版本</DialogTitle>
          <DialogDescription>
            选择 {appName}{' '}
            在该集群上部署成功的历史版本，确认后提交新的镜像回退任务。配置、清单和数据库不会随镜像恢复，请先确认兼容性。
          </DialogDescription>
        </DialogHeader>
        {history.isLoading ? (
          <p className='text-sm text-muted-foreground'>加载历史版本…</p>
        ) : history.isError ? (
          <p className='text-sm text-muted-foreground'>无法获取历史版本</p>
        ) : records.length === 0 ? (
          <p className='text-sm text-muted-foreground'>暂无可回滚的历史版本</p>
        ) : (
          <Select value={selectedId} onValueChange={setSelected}>
            <SelectTrigger className='w-full'>
              <SelectValue placeholder='选择历史版本' />
            </SelectTrigger>
            <SelectContent>
              {records.map((r) => (
                <SelectItem
                  key={r.id}
                  value={String(r.id)}
                  disabled={!r.image_full}
                >
                  {r.version} · {r.deploy_type} ·{' '}
                  {r.finished_at?.replace('T', ' ') ?? '-'}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {error && <p className='text-sm text-destructive'>{error}</p>}
        <DialogFooter>
          <Button
            variant='outline'
            onClick={onClose}
            disabled={rollback.isPending}
          >
            取消
          </Button>
          <Button
            onClick={submit}
            disabled={!record?.image_full || rollback.isPending}
          >
            确认回滚
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function EnvDeploySection({
  title,
  appName,
  logId,
  status,
  error,
  currentStep,
  open,
}: {
  title: string
  appName: string
  logId?: number | null
  status?: string | null
  error?: string | null
  currentStep?: string | null
  open: boolean
}) {
  const role = useAuthStore((s) => s.auth.user?.role)
  const [rollbackOpen, setRollbackOpen] = useState(false)
  const hasLog = logId != null && logId > 0
  const logQuery = useQuery({
    queryKey: ['deploy-log', logId],
    queryFn: () => platformApi.deployLog(logId!),
    enabled: open && hasLog,
    retry: false,
    // Keep polling while the deploy is in flight; stop once it settles.
    refetchInterval: (query) =>
      deployLogRunning(query.state.data?.data?.status) ? 2000 : false,
  })
  const log = logQuery.data?.data
  const shownStatus = log?.status ?? status
  const shownError = log?.error_message ?? error
  return (
    <section className='space-y-3 rounded-xl border border-border p-4'>
      <div className='flex flex-wrap items-center gap-2'>
        <h3 className='text-sm font-semibold text-foreground'>{title}</h3>
        <StatusBadge status={shownStatus} />
        {log && (
          <span className='font-mono text-xs text-muted-foreground'>
            {log.deployment_name}/{log.container_name}@{log.namespace}
          </span>
        )}
        {log?.started_at && (
          <span className='text-xs text-muted-foreground'>
            {log.started_at.replace('T', ' ')}
            {log.finished_at ? ` → ${log.finished_at.replace('T', ' ')}` : ''}
          </span>
        )}
        {hasLog && (
          <div className='ms-auto flex flex-wrap items-center gap-1'>
            <Button asChild variant='ghost' size='sm' className='text-primary'>
              <Link
                to='/tasks/logs/$logId'
                params={{ logId: String(logId) }}
                aria-label={`查看完整任务 #${logId}`}
              >
                <ArrowUpRight aria-hidden='true' />
                查看完整任务
              </Link>
            </Button>
            {log && role !== 'viewer' && (
              <Button
                variant='ghost'
                size='sm'
                onClick={() => setRollbackOpen(true)}
              >
                <Undo2 />
                回滚到历史版本
              </Button>
            )}
          </div>
        )}
      </div>
      {!hasLog ? (
        <p className='text-xs text-muted-foreground'>暂无部署记录</p>
      ) : logQuery.isLoading ? (
        <Skeleton className='h-16 w-full' />
      ) : logQuery.isError || !log ? (
        <p className='text-xs text-muted-foreground'>无法加载部署详情</p>
      ) : (
        <DeployStepTimeline log={log} fallbackStep={currentStep} />
      )}
      {shownError && (
        <div className='rounded-lg border border-destructive/25 bg-destructive/5 px-3 py-2 text-xs break-all text-destructive'>
          {shownError}
        </div>
      )}
      {rollbackOpen && log && (
        <RollbackDialog
          appName={appName}
          log={log}
          onClose={() => setRollbackOpen(false)}
        />
      )}
    </section>
  )
}

export function ReleaseDetailDialog({
  release,
  open,
  onClose,
}: {
  release: ImageReleaseView
  open: boolean
  onClose: () => void
}) {
  const meta: { label: string; value: React.ReactNode }[] = []
  const binding = release.metadata?.binding
  const sourceRepositories = release.metadata?.source_repositories ?? []
  const reportedSourcePath = release.metadata?.source_path
  const configurationSource = release.metadata?.configuration_source
  if (binding) {
    meta.push({
      label: '接入时的代码仓库',
      value: componentSourceLabel(binding.source_kind, binding.source_repo_url),
    })
    if (binding.source_repo_url)
      meta.push({ label: '构建目录', value: binding.source_path || '.' })
    if (binding.deployment_config_repo_url) {
      meta.push({
        label: '接入时的部署配置仓库',
        value: (
          <div className='space-y-1'>
            <code className='block font-mono text-xs break-all'>
              {binding.deployment_config_repo_url}
            </code>
            <span className='text-xs text-muted-foreground'>
              目录 · {binding.deployment_config_path || '.'}
            </span>
          </div>
        ),
      })
    }
    meta.push({
      label: '接入时的镜像仓库',
      value: binding.image_repository || '未限定',
    })
    meta.push({
      label: '测试部署位置',
      value: `${binding.namespace} / ${binding.deployment_name} / ${binding.container_name}`,
    })
  }
  if (sourceRepositories.length > 0) {
    const expectedSourceRepository =
      binding?.source_kind === 'repository'
        ? sourceRepositoryIdentity(binding.source_repo_url ?? '')
        : ''
    const sourceRepositoryMatched =
      Boolean(expectedSourceRepository) &&
      sourceRepositories.some(
        (repository) =>
          sourceRepositoryIdentity(repository) === expectedSourceRepository
      )
    const expectedSourcePath = binding?.source_path || '.'
    const sourcePathMatched =
      sourceRepositoryMatched &&
      (reportedSourcePath
        ? expectedSourcePath === reportedSourcePath
        : expectedSourcePath === '.')
    meta.push({
      label: '本次构建代码源',
      value: (
        <div className='space-y-1'>
          {sourceRepositories.map((repository) => (
            <code
              key={repository}
              className='block font-mono text-xs leading-5 break-all'
            >
              {repository}
            </code>
          ))}
          {expectedSourceRepository && (
            <Badge
              variant='outline'
              className={
                sourcePathMatched
                  ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-200'
                  : 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-100'
              }
            >
              {!sourceRepositoryMatched
                ? '代码仓库与组件绑定不一致'
                : sourcePathMatched
                  ? '仓库与目录已匹配'
                  : '仓库已匹配，构建目录不一致'}
            </Badge>
          )}
        </div>
      ),
    })
  } else if (binding?.source_kind === 'repository' && binding.source_repo_url) {
    meta.push({
      label: '本次构建代码源',
      value: (
        <Badge
          variant='outline'
          className='border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-100'
        >
          本次构建未报告代码仓库
        </Badge>
      ),
    })
  }
  if (reportedSourcePath) {
    meta.push({
      label: '本次构建目录',
      value: (
        <code
          data-testid='release-source-path'
          className='font-mono text-xs break-all'
        >
          {reportedSourcePath}
        </code>
      ),
    })
  }
  if (configurationSource) {
    const configurationSourceMatched =
      Boolean(binding?.deployment_config_repo_url) &&
      sourceRepositoryIdentity(configurationSource.repository_url) ===
        sourceRepositoryIdentity(binding?.deployment_config_repo_url ?? '') &&
      configurationSource.path === (binding?.deployment_config_path || '.')
    meta.push({
      label: '本次部署配置来源',
      value: (
        <div className='space-y-1'>
          <code className='block font-mono text-xs break-all'>
            {configurationSource.repository_url}
          </code>
          <span className='block text-xs text-muted-foreground'>
            目录 · {configurationSource.path || '.'}
          </span>
          <code
            data-testid='release-config-revision'
            className='block font-mono text-xs break-all'
          >
            {configurationSource.revision}
          </code>
          {binding?.deployment_config_repo_url && (
            <Badge
              variant='outline'
              className={
                configurationSourceMatched
                  ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-200'
                  : 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-100'
              }
            >
              {configurationSourceMatched
                ? '配置仓库与目录已匹配'
                : '配置来源与组件绑定不一致'}
            </Badge>
          )}
        </div>
      ),
    })
  } else if (binding?.deployment_config_repo_url) {
    meta.push({
      label: '本次部署配置来源',
      value: (
        <Badge
          variant='outline'
          className='border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-100'
        >
          尚未固定 Git commit
        </Badge>
      ),
    })
  }
  if (release.pipeline_name || release.pipeline_id || release.build_number)
    meta.push({ label: '流水线 / 构建', value: releaseBuildIdentity(release) })
  if (release.branch) meta.push({ label: '分支', value: release.branch })
  if (release.commit_id)
    meta.push({
      label: 'Commit',
      value: (
        <code className='font-mono text-xs'>
          {release.commit_id.slice(0, 12)}
        </code>
      ),
    })
  if (release.metadata?.commit_timestamp)
    meta.push({
      label: 'Git 提交时间',
      value: new Intl.DateTimeFormat('zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        timeZoneName: 'short',
      }).format(new Date(release.metadata.commit_timestamp)),
    })
  if (release.version) meta.push({ label: '版本', value: release.version })
  if (release.image_full)
    meta.push({
      label: '镜像',
      value: (
        <code className='font-mono text-xs break-all'>
          {release.image_full}
        </code>
      ),
    })
  if (release.created_at)
    meta.push({
      label: '创建时间',
      value: release.created_at.replace('T', ' '),
    })
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
    >
      <DialogContent className='max-h-[90vh] overflow-y-auto sm:max-w-3xl'>
        <DialogHeader>
          <DialogTitle>
            发布单 #{release.id} · {release.app_name}
          </DialogTitle>
          <DialogDescription>
            发布单元数据与各环境部署执行详情。
          </DialogDescription>
        </DialogHeader>
        {meta.length > 0 && (
          <div className='grid gap-3 sm:grid-cols-2 lg:grid-cols-3'>
            {meta.map((m) => (
              <div key={m.label} className='min-w-0'>
                <div className='text-xs text-muted-foreground'>{m.label}</div>
                <div className='mt-0.5 text-sm'>{m.value}</div>
              </div>
            ))}
          </div>
        )}
        <EvidenceHistory release={release} />
        <EnvDeploySection
          title='测试环境'
          appName={release.app_name}
          logId={release.test_deploy_log_id}
          status={release.test_status}
          error={release.test_error}
          currentStep={release.test_current_step}
          open={open}
        />
        <EnvDeploySection
          title='生产环境'
          appName={release.app_name}
          logId={release.prod_deploy_log_id}
          status={release.prod_status}
          error={release.prod_error}
          currentStep={release.prod_current_step}
          open={open}
        />
        <DialogFooter>
          <Button variant='outline' onClick={onClose}>
            关闭
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function EvidenceHistory({ release }: { release: ImageReleaseView }) {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(timer)
  }, [])
  const verification = release.metadata?.verification_history?.length
    ? release.metadata.verification_history
    : release.metadata?.verification
      ? [release.metadata.verification]
      : []
  const scans = release.metadata?.scan_history?.length
    ? release.metadata.scan_history
    : release.metadata?.scan
      ? [release.metadata.scan]
      : []
  const entries = [
    ...verification.map((evidence) => ({ evidence, scan: false })),
    ...scans.map((evidence) => ({ evidence, scan: true })),
  ].sort(
    (a, b) =>
      new Date(b.evidence.recorded_at ?? 0).getTime() -
      new Date(a.evidence.recorded_at ?? 0).getTime()
  )
  if (!entries.length) return null
  return (
    <details className='rounded-xl border border-border px-4 py-3'>
      <summary className='cursor-pointer text-sm font-medium'>
        验证与扫描记录（{entries.length}）
      </summary>
      <ol className='mt-3 space-y-3'>
        {entries.map(({ evidence, scan }, index) => {
          const expired = new Date(evidence.valid_until).getTime() <= now
          const reportURL = evidence.report_url
            ? safeReportURL(evidence.report_url)
            : undefined
          return (
            <li
              key={`${scan ? 'scan' : 'verification'}-${evidence.recorded_at ?? index}-${index}`}
              className='min-w-0 rounded-lg bg-muted/50 p-3'
            >
              <div className='flex flex-wrap items-center gap-2'>
                <span className='text-sm font-medium'>
                  {scan
                    ? '安全扫描'
                    : evidence.kind === 'ci'
                      ? 'CI 验证'
                      : evidence.kind === 'smoke'
                        ? 'Smoke 验证'
                        : '人工验收'}
                </span>
                <Badge
                  variant='outline'
                  className={
                    expired
                      ? 'text-muted-foreground'
                      : evidence.status === 'passed'
                        ? 'border-emerald-300 text-emerald-800 dark:border-emerald-800 dark:text-emerald-200'
                        : 'border-destructive/30 text-destructive'
                  }
                >
                  {expired
                    ? '已过期'
                    : evidence.status === 'passed'
                      ? '通过'
                      : '失败'}
                </Badge>
              </div>
              <p className='mt-1 text-xs text-muted-foreground'>
                {evidence.recorded_by || '来源未知'} · 记录于{' '}
                {evidence.recorded_at
                  ? new Date(evidence.recorded_at).toLocaleString()
                  : '时间未知'}
                {' · '}有效至 {new Date(evidence.valid_until).toLocaleString()}
              </p>
              {evidence.summary && (
                <p className='mt-2 text-sm break-words'>{evidence.summary}</p>
              )}
              {scan &&
                (evidence.tool ||
                  evidence.tool_version ||
                  evidence.rules_version) && (
                  <p className='mt-2 text-xs text-muted-foreground'>
                    扫描工具：{evidence.tool || '未知'}{' '}
                    {evidence.tool_version || ''}
                    {' · '}规则版本：{evidence.rules_version || '未知'}
                  </p>
                )}
              {reportURL && (
                <a
                  className='mt-2 inline-block text-sm text-primary underline underline-offset-4'
                  href={reportURL}
                  target='_blank'
                  rel='noreferrer'
                >
                  查看报告
                </a>
              )}
            </li>
          )
        })}
      </ol>
    </details>
  )
}
