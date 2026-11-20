import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { CircleAlert, RefreshCw } from 'lucide-react'
import { toast } from 'sonner'
import {
  platformApi,
  type ImageReleaseView,
  type PromotionRequest,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ThemeSwitch } from '@/components/theme-switch'
import { DeliveryOverview } from './delivery-overview'
import { DeliveryPlanDialog, DeliveryPlanHistory } from './delivery-plan-dialog'
import { ReleaseDetailDialog, StatusBadge } from './release-detail'
import { promoteAction, releaseBuildIdentity } from './release-status'

function ErrorLine({ error }: { error?: string | null }) {
  if (!error) return null
  return (
    <div
      role='status'
      className='mt-1 max-w-52 truncate text-xs text-destructive'
      title={error}
    >
      {error}
    </div>
  )
}

function ApprovalScopeNotice({ compact = false }: { compact?: boolean }) {
  const message = '请联系管理员为此业务应用和生产环境配置审批范围。'
  if (compact) {
    return (
      <span
        role='status'
        title={message}
        className='inline-flex items-center gap-1.5 rounded-md border border-amber-300/70 bg-amber-50/70 px-2 py-1.5 text-xs font-medium text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'
      >
        <CircleAlert aria-hidden='true' className='size-3.5 shrink-0' />
        无审批权限
      </span>
    )
  }
  return (
    <div
      role='status'
      className='flex items-start gap-2.5 rounded-lg border border-amber-300/70 bg-amber-50/70 p-3 text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'
    >
      <CircleAlert aria-hidden='true' className='mt-0.5 size-4 shrink-0' />
      <div className='space-y-0.5'>
        <p className='text-sm leading-5 font-semibold'>缺少审批范围</p>
        <p className='text-sm leading-5 text-amber-900/90 dark:text-amber-100/90'>
          {message}
        </p>
      </div>
    </div>
  )
}

const promoStatusStyle: Record<string, string> = {
  pending:
    'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-200',
  approved:
    'border-emerald-300 bg-emerald-50 text-emerald-900 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-200',
  rejected:
    'border-destructive/30 bg-destructive/10 text-destructive dark:border-destructive/50 dark:bg-destructive/15',
  cancelled:
    'border-border bg-muted text-muted-foreground dark:border-border dark:bg-muted/60',
  expired:
    'border-border bg-muted text-muted-foreground dark:border-border dark:bg-muted/60',
}
const promoStatusLabel: Record<string, string> = {
  pending: '待审批',
  approved: '已批准',
  rejected: '已拒绝',
  cancelled: '已撤销',
  expired: '已过期',
}

export function PromotionRequestsCard({
  releases,
}: {
  releases: ImageReleaseView[]
}) {
  const role = useAuthStore((s) => s.auth.user?.role)
  const username = useAuthStore((s) => s.auth.user?.username)
  const queryClient = useQueryClient()
  const [rejecting, setRejecting] = useState<PromotionRequest | null>(null)
  const [comment, setComment] = useState('')
  const requests = useQuery({
    queryKey: ['promotion-requests'],
    queryFn: () => platformApi.promotionRequests(),
    refetchInterval: 3000,
  })
  const invalidate = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['promotion-requests'] }),
      queryClient.invalidateQueries({ queryKey: ['releases'] }),
    ])
  const errDetail = (err: unknown, fallback: string) =>
    (err as { response?: { data?: { detail?: string } } }).response?.data
      ?.detail ?? fallback
  const approve = useMutation({
    mutationFn: (id: number) => platformApi.approvePromotionRequest(id),
    onSuccess: () => {
      toast.success('已批准，生产发布已提交')
      void invalidate()
    },
    onError: (err) => toast.error(errDetail(err, '批准失败')),
  })
  const reject = useMutation({
    mutationFn: ({ id, note }: { id: number; note: string }) =>
      platformApi.rejectPromotionRequest(id, note),
    onSuccess: () => {
      toast.success('已关闭该审批请求')
      void invalidate()
      setRejecting(null)
      setComment('')
    },
    onError: (err) => toast.error(errDetail(err, '操作失败')),
  })
  const rows = requests.data?.data ?? []
  const releaseOf = (id: number) => releases.find((r) => r.id === id)
  const canOperate = role === 'operator' || role === 'admin'
  return (
    <Card className='mt-6 rounded-xl border-border shadow-sm'>
      <CardHeader>
        <CardTitle className='text-base'>晋级审批</CardTitle>
      </CardHeader>
      <CardContent>
        <div className='hidden overflow-x-auto xl:block'>
          <table className='w-full min-w-[1080px] text-sm'>
            <thead className='bg-muted/50'>
              <tr className='border-b'>
                {[
                  '发布单',
                  '目标集群',
                  '发起人',
                  '状态',
                  '审批人',
                  '过期时间',
                  '操作',
                ].map((c) => (
                  <th
                    key={c}
                    className='px-3 py-3 text-left text-xs font-semibold whitespace-nowrap text-muted-foreground'
                  >
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => {
                const rel = releaseOf(p.release_id)
                const own = username != null && p.requester === username
                return (
                  <tr
                    key={p.id}
                    className='border-b border-border/70 last:border-0'
                  >
                    <td className='px-3 py-3'>
                      <div className='font-medium'>
                        {rel?.app_name ?? `#${p.release_id}`}
                      </div>
                      <div className='max-w-52 truncate font-mono text-xs text-muted-foreground'>
                        {rel?.image_full ?? ''}
                      </div>
                      {p.delivery_plan_id != null && (
                        <Button
                          asChild
                          variant='link'
                          size='sm'
                          className='mt-1 h-10 px-0 font-medium'
                        >
                          <Link
                            to='/tasks/plans/$planId'
                            params={{ planId: String(p.delivery_plan_id) }}
                          >
                            查看计划 #{p.delivery_plan_id}
                          </Link>
                        </Button>
                      )}
                    </td>
                    <td className='px-3 py-3'>
                      <ApprovalEvidence
                        releaseID={p.release_id}
                        clusterID={p.prod_cluster_id}
                        expectedImage={p.expected_image}
                        templateHash={p.template_hash}
                      />
                    </td>
                    <td className='px-3 py-3'>{p.requester}</td>
                    <td className='px-3 py-3'>
                      <Badge
                        variant='outline'
                        className={promoStatusStyle[p.status] ?? ''}
                      >
                        {promoStatusLabel[p.status] ?? p.status}
                      </Badge>
                      {p.comment && (
                        <div
                          className='mt-1 max-w-52 truncate text-xs text-muted-foreground'
                          title={p.comment}
                        >
                          {p.comment}
                        </div>
                      )}
                    </td>
                    <td className='px-3 py-3'>{p.approver ?? '-'}</td>
                    <td className='px-3 py-3 text-xs whitespace-nowrap'>
                      {p.expires_at?.replace('T', ' ').slice(0, 16) ?? '-'}
                    </td>
                    <td className='px-3 py-3'>
                      {p.status === 'pending' && canOperate ? (
                        p.delivery_plan_id != null && !own ? (
                          p.can_approve ? (
                            <Button asChild size='sm' className='min-h-10'>
                              <Link
                                to='/tasks/plans/$planId'
                                params={{ planId: String(p.delivery_plan_id) }}
                              >
                                核对计划并审批
                              </Link>
                            </Button>
                          ) : (
                            <ApprovalScopeNotice compact />
                          )
                        ) : (
                          <div className='flex gap-2'>
                            {p.delivery_plan_id == null &&
                              !own &&
                              p.can_approve && (
                                <Button
                                  size='sm'
                                  disabled={
                                    approve.isPending || reject.isPending
                                  }
                                  onClick={() => approve.mutate(p.id)}
                                >
                                  批准
                                </Button>
                              )}
                            {(own || p.can_approve) && (
                              <Button
                                size='sm'
                                variant='outline'
                                disabled={reject.isPending || approve.isPending}
                                onClick={() => {
                                  setComment('')
                                  setRejecting(p)
                                }}
                              >
                                {own ? '撤销' : '拒绝'}
                              </Button>
                            )}
                            {!own && !p.can_approve && (
                              <ApprovalScopeNotice compact />
                            )}
                          </div>
                        )
                      ) : (
                        <span className='text-muted-foreground'>-</span>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        <div className='space-y-3 xl:hidden'>
          {rows.map((p) => {
            const rel = releaseOf(p.release_id)
            const own = username != null && p.requester === username
            return (
              <article
                key={p.id}
                className='space-y-3 rounded-lg border border-border p-4'
              >
                <div className='flex flex-wrap items-start justify-between gap-2'>
                  <div className='min-w-0'>
                    <h3 className='font-medium'>
                      {rel?.app_name ?? `发布单 #${p.release_id}`}
                    </h3>
                    {rel?.image_full && (
                      <p className='mt-1 font-mono text-xs break-all text-muted-foreground'>
                        {rel.image_full}
                      </p>
                    )}
                  </div>
                  <Badge
                    variant='outline'
                    className={promoStatusStyle[p.status] ?? ''}
                  >
                    {promoStatusLabel[p.status] ?? p.status}
                  </Badge>
                </div>
                {p.delivery_plan_id != null && (
                  <Button
                    asChild
                    variant='link'
                    size='sm'
                    className='h-10 px-0 font-medium'
                  >
                    <Link
                      to='/tasks/plans/$planId'
                      params={{ planId: String(p.delivery_plan_id) }}
                    >
                      查看发布计划 #{p.delivery_plan_id}
                    </Link>
                  </Button>
                )}
                <div className='rounded-md bg-muted/40 p-3'>
                  <ApprovalEvidence
                    releaseID={p.release_id}
                    clusterID={p.prod_cluster_id}
                    expectedImage={p.expected_image}
                    templateHash={p.template_hash}
                  />
                </div>
                <dl className='grid grid-cols-2 gap-x-4 gap-y-2 text-xs'>
                  <div>
                    <dt className='text-muted-foreground'>发起人</dt>
                    <dd className='mt-0.5 break-all'>{p.requester}</dd>
                  </div>
                  <div>
                    <dt className='text-muted-foreground'>审批人</dt>
                    <dd className='mt-0.5 break-all'>{p.approver ?? '-'}</dd>
                  </div>
                  <div className='col-span-2'>
                    <dt className='text-muted-foreground'>过期时间</dt>
                    <dd className='mt-0.5'>
                      {p.expires_at?.replace('T', ' ').slice(0, 16) ?? '-'}
                    </dd>
                  </div>
                </dl>
                {p.comment && (
                  <p className='text-xs text-muted-foreground'>{p.comment}</p>
                )}
                {p.status === 'pending' && canOperate && (
                  <div className='flex gap-2 border-t border-border pt-3'>
                    {p.delivery_plan_id != null && !own ? (
                      p.can_approve ? (
                        <Button asChild className='min-h-11 w-full'>
                          <Link
                            to='/tasks/plans/$planId'
                            params={{ planId: String(p.delivery_plan_id) }}
                          >
                            核对计划并审批
                          </Link>
                        </Button>
                      ) : (
                        <ApprovalScopeNotice />
                      )
                    ) : (
                      <>
                        {p.delivery_plan_id == null &&
                          !own &&
                          p.can_approve && (
                            <Button
                              className='min-h-11 flex-1'
                              disabled={approve.isPending || reject.isPending}
                              onClick={() => approve.mutate(p.id)}
                            >
                              批准
                            </Button>
                          )}
                        {(own || p.can_approve) && (
                          <Button
                            className='min-h-11 flex-1'
                            variant='outline'
                            disabled={reject.isPending || approve.isPending}
                            onClick={() => {
                              setComment('')
                              setRejecting(p)
                            }}
                          >
                            {own ? '撤销' : '拒绝'}
                          </Button>
                        )}
                        {!own && !p.can_approve && <ApprovalScopeNotice />}
                      </>
                    )}
                  </div>
                )}
              </article>
            )
          })}
        </div>
        {rows.length === 0 && (
          <p className='py-6 text-center text-muted-foreground'>暂无审批请求</p>
        )}
      </CardContent>
      {rejecting && (
        <ConfirmDialog
          open={true}
          onOpenChange={(o) => {
            if (!o) setRejecting(null)
          }}
          title={
            rejecting.requester === username ? '撤销审批请求' : '拒绝审批请求'
          }
          desc={
            rejecting.requester === username
              ? '确认撤销这条发布到正式环境的申请？'
              : '确认拒绝这条发布到正式环境的申请？'
          }
          destructive={rejecting.requester !== username}
          confirmText='确认'
          isLoading={reject.isPending}
          handleConfirm={() =>
            reject.mutate({ id: rejecting.id, note: comment.trim() })
          }
        >
          <Textarea
            placeholder='备注（可选）'
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            rows={2}
          />
        </ConfirmDialog>
      )}
    </Card>
  )
}

export function ReleasePage({
  linkedReleaseID,
  onLinkedReleaseClose,
}: {
  linkedReleaseID?: number
  onLinkedReleaseClose?: () => void
} = {}) {
  const role = useAuthStore((s) => s.auth.user?.role)
  const [planTarget, setPlanTarget] = useState<ImageReleaseView | null>(null)
  const [detail, setDetail] = useState<ImageReleaseView | null>(null)
  const [verifying, setVerifying] = useState<ImageReleaseView | null>(null)
  const linkedReleaseIDIsValid =
    Number.isSafeInteger(linkedReleaseID) && (linkedReleaseID ?? 0) > 0
  const linkedRelease = useQuery({
    queryKey: ['release', linkedReleaseID],
    queryFn: () => platformApi.release(linkedReleaseID!),
    enabled: linkedReleaseIDIsValid,
    retry: false,
  })
  const releases = useQuery({
    queryKey: ['releases'],
    queryFn: () => platformApi.releases(),
    refetchInterval: 3000,
  })
  const rows = releases.data?.data ?? []
  const selectedDetail = linkedReleaseIDIsValid
    ? (linkedRelease.data?.data ?? null)
    : detail

  const closeReleaseDetail = () => {
    if (linkedReleaseIDIsValid) {
      onLinkedReleaseClose?.()
    } else {
      setDetail(null)
    }
  }

  return (
    <>
      <Header fixed>
        <div className='me-auto flex items-center gap-2 text-sm'>
          <span className='text-muted-foreground'>首页</span>
          <span aria-hidden='true' className='text-muted-foreground/60'>
            /
          </span>
          <span aria-current='page' className='font-medium text-foreground'>
            发布中心
          </span>
        </div>
        <ThemeSwitch />
      </Header>
      <Main>
        {linkedReleaseIDIsValid && linkedRelease.isPending && (
          <p role='status' className='mb-4 text-sm text-muted-foreground'>
            正在打开发布单 #{linkedReleaseID}…
          </p>
        )}
        {linkedReleaseIDIsValid && linkedRelease.isError && (
          <div
            role='alert'
            className='mb-4 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/25 bg-destructive/5 p-3 text-sm text-destructive'
          >
            <span>
              无法读取关联发布单 #{linkedReleaseID}，请检查连接后重试。
            </span>
            <Button
              variant='outline'
              size='sm'
              className='min-h-10'
              onClick={() => void linkedRelease.refetch()}
            >
              <RefreshCw aria-hidden='true' />
              重试
            </Button>
          </div>
        )}
        <div className='mb-6 max-w-4xl space-y-2'>
          <p className='text-xs font-semibold tracking-[0.12em] text-primary uppercase'>
            DELIVERY CENTER
          </p>
          <h1 className='text-2xl font-semibold tracking-tight text-foreground sm:text-3xl'>
            镜像发布
          </h1>
          <p className='max-w-3xl text-sm leading-6 text-muted-foreground'>
            云效构建成功自动部署测试环境；部署与业务验证通过后，申请将同一制品发布到生产环境
          </p>
          <p className='max-w-3xl text-xs leading-5 text-muted-foreground'>
            列表按平台接收顺序排列；流水线构建号只在同一流水线内比较。跨分支或流水线判断版本先后时，请核对
            Commit 与镜像 digest。
          </p>
        </div>
        <Card className='rounded-xl border-border shadow-sm'>
          <CardHeader>
            <CardTitle className='text-base'>发布单</CardTitle>
          </CardHeader>
          <CardContent>
            <div className='hidden overflow-x-auto xl:block'>
              <table className='w-full min-w-[1000px] table-fixed text-sm'>
                <thead className='bg-muted/50'>
                  <tr className='border-b'>
                    {[
                      '发布单',
                      '制品 / Digest',
                      '测试环境 / 验证',
                      '正式环境',
                      '创建时间',
                      '操作',
                    ].map((c) => (
                      <th
                        key={c}
                        className='px-3 py-3 text-left text-xs font-semibold whitespace-nowrap text-muted-foreground'
                      >
                        {c}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r) => {
                    const action = promoteAction(r)
                    return (
                      <tr
                        key={r.id}
                        className='border-b border-border/70 transition-colors last:border-0 hover:bg-muted/40'
                      >
                        <td className='px-3 py-3'>
                          <Button
                            variant='link'
                            size='sm'
                            className='h-auto min-h-11 justify-start px-0 font-medium text-foreground focus-visible:underline'
                            onClick={() => setDetail(r)}
                          >
                            {r.app_name}
                            <span className='sr-only'>查看发布单 #{r.id}</span>
                          </Button>
                          <div className='mt-1 text-xs text-muted-foreground'>
                            {r.branch ?? '-'}
                            {r.commit_id ? ` / ${r.commit_id.slice(0, 8)}` : ''}
                          </div>
                          <div
                            className='text-xs text-muted-foreground'
                            title={releaseBuildIdentity(r)}
                          >
                            {releaseBuildIdentity(r)}
                          </div>
                        </td>
                        <td className='px-3 py-3 font-mono text-xs break-all'>
                          {r.image_full}
                          <div
                            className='mt-1 max-w-64 truncate text-muted-foreground'
                            title={r.image_digest ?? undefined}
                          >
                            {r.image_digest ?? '未固化 digest'}
                          </div>
                          <div className='text-muted-foreground'>
                            来源：
                            {r.metadata?.digest_source === 'ci'
                              ? 'CI 提供'
                              : r.metadata?.digest_source === 'registry'
                                ? '入站解析'
                                : r.metadata?.digest_source === 'mock'
                                  ? '模拟'
                                  : '未知'}
                          </div>
                        </td>
                        <td className='px-3 py-3'>
                          <StatusBadge status={r.test_status} />
                          <div className='mt-1 text-xs text-muted-foreground'>
                            {r.test_cluster_name ??
                              `集群 #${r.test_cluster_id}`}
                          </div>
                          <ErrorLine error={r.test_error} />
                          <EvidenceSummary release={r} />
                        </td>
                        <td className='px-3 py-3'>
                          {r.deployments?.some(
                            (d) => d.cluster_id !== r.test_cluster_id
                          ) ? (
                            r.deployments
                              .filter((d) => d.cluster_id !== r.test_cluster_id)
                              .map((d) => (
                                <div key={d.id} className='mb-1'>
                                  集群 #{d.cluster_id}{' '}
                                  <StatusBadge status={d.status} />
                                </div>
                              ))
                          ) : (
                            <StatusBadge status={r.prod_status} />
                          )}
                          <ErrorLine error={r.prod_error} />
                        </td>
                        <td className='px-3 py-3 text-xs whitespace-nowrap'>
                          {r.created_at?.replace('T', ' ')}
                        </td>
                        <td
                          className='px-3 py-3'
                          onClick={(e) => e.stopPropagation()}
                        >
                          {role !== 'viewer' &&
                            r.image_digest &&
                            r.test_status === 'success' && (
                              <Button
                                size='sm'
                                variant='outline'
                                className='mr-2'
                                onClick={() => setVerifying(r)}
                              >
                                记录验收
                              </Button>
                            )}
                          {action.actionable && role !== 'viewer' ? (
                            <Button size='sm' onClick={() => setPlanTarget(r)}>
                              {action.label}
                            </Button>
                          ) : (
                            <span className='text-muted-foreground'>
                              {action.actionable && role === 'viewer'
                                ? '等待操作员创建计划'
                                : action.label}
                            </span>
                          )}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
            <div className='space-y-3 xl:hidden'>
              {rows.map((r) => {
                const action = promoteAction(r)
                return (
                  <article
                    key={r.id}
                    className='space-y-3 rounded-lg border border-border p-4'
                  >
                    <div className='flex items-start justify-between gap-3'>
                      <div className='min-w-0'>
                        <Button
                          variant='link'
                          size='sm'
                          className='min-h-11 justify-start px-0 font-semibold text-foreground focus-visible:underline'
                          onClick={() => setDetail(r)}
                        >
                          {r.app_name}
                          <span className='sr-only'>查看发布单 #{r.id}</span>
                        </Button>
                        <p className='text-xs text-muted-foreground'>
                          发布单 #{r.id}
                          {r.created_at &&
                            ` · ${r.created_at.replace('T', ' ')}`}
                        </p>
                      </div>
                      <StatusBadge status={r.test_status} />
                    </div>
                    <div className='rounded-md bg-muted/40 p-3'>
                      <p className='font-mono text-xs leading-5 break-all'>
                        {r.image_full}
                      </p>
                      <p className='mt-1 font-mono text-xs break-all text-muted-foreground'>
                        {r.image_digest ?? '未固化 digest'}
                      </p>
                      <p className='mt-1 text-xs text-muted-foreground'>
                        Digest 来源：
                        {r.metadata?.digest_source === 'ci'
                          ? 'CI 提供'
                          : r.metadata?.digest_source === 'registry'
                            ? '入站解析'
                            : r.metadata?.digest_source === 'mock'
                              ? '模拟'
                              : '未知'}
                      </p>
                    </div>
                    <dl className='grid grid-cols-2 gap-x-4 gap-y-3 text-xs'>
                      <div>
                        <dt className='text-muted-foreground'>分支 / Commit</dt>
                        <dd className='mt-0.5 break-all'>
                          {r.branch ?? '-'}
                          {r.commit_id ? ` / ${r.commit_id.slice(0, 8)}` : ''}
                        </dd>
                      </div>
                      <div className='min-w-0'>
                        <dt className='text-muted-foreground'>流水线 / 构建</dt>
                        <dd
                          className='mt-0.5 break-all'
                          title={releaseBuildIdentity(r)}
                        >
                          {releaseBuildIdentity(r)}
                        </dd>
                      </div>
                      <div>
                        <dt className='text-muted-foreground'>测试环境</dt>
                        <dd className='mt-0.5 break-words'>
                          {r.test_cluster_name ?? `集群 #${r.test_cluster_id}`}
                        </dd>
                      </div>
                      <div>
                        <dt className='text-muted-foreground'>正式环境</dt>
                        <dd className='mt-0.5'>
                          {r.deployments?.some(
                            (d) => d.cluster_id !== r.test_cluster_id
                          ) ? (
                            r.deployments
                              .filter((d) => d.cluster_id !== r.test_cluster_id)
                              .map((d) => (
                                <span key={d.id} className='mb-1 block'>
                                  集群 #{d.cluster_id} ·{' '}
                                  <StatusBadge status={d.status} />
                                </span>
                              ))
                          ) : (
                            <StatusBadge status={r.prod_status} />
                          )}
                        </dd>
                      </div>
                    </dl>
                    <div className='rounded-md border border-border/70 p-3'>
                      <EvidenceSummary release={r} />
                    </div>
                    {r.test_error && <ErrorLine error={r.test_error} />}
                    {r.prod_error && <ErrorLine error={r.prod_error} />}
                    <div className='flex flex-col gap-2 border-t border-border pt-3'>
                      {role !== 'viewer' &&
                        r.image_digest &&
                        r.test_status === 'success' && (
                          <Button
                            variant='outline'
                            className='min-h-11 w-full'
                            onClick={() => setVerifying(r)}
                          >
                            记录验收
                          </Button>
                        )}
                      {action.actionable && role !== 'viewer' ? (
                        <Button
                          className='min-h-11 w-full'
                          onClick={() => setPlanTarget(r)}
                        >
                          {action.label}
                        </Button>
                      ) : (
                        <p className='py-2 text-center text-xs text-muted-foreground'>
                          {action.actionable && role === 'viewer'
                            ? '等待操作员创建计划'
                            : action.label}
                        </p>
                      )}
                    </div>
                  </article>
                )
              })}
            </div>
            {releases.isError && (
              <p role='alert' className='py-8 text-center text-destructive'>
                发布单加载失败，请检查连接后重试
              </p>
            )}
            {!releases.isError && rows.length === 0 && (
              <p className='py-8 text-center text-muted-foreground'>
                {releases.isLoading
                  ? '加载发布单…'
                  : '暂无发布单，等待云效 Webhook 触发'}
              </p>
            )}
          </CardContent>
        </Card>
        <PromotionRequestsCard releases={rows} />
        <DeliveryPlanHistory />
        <DeliveryOverview />
      </Main>
      {selectedDetail && (
        <ReleaseDetailDialog
          release={selectedDetail}
          open={true}
          onClose={closeReleaseDetail}
        />
      )}
      {verifying && (
        <VerificationDialog
          release={verifying}
          onClose={() => setVerifying(null)}
        />
      )}
      {planTarget && (
        <DeliveryPlanDialog
          release={planTarget}
          onClose={() => setPlanTarget(null)}
        />
      )}
    </>
  )
}

export function EvidenceSummary({ release }: { release: ImageReleaseView }) {
  const e = release.metadata?.verification
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])
  return (
    <div className='mt-2 space-y-1 text-xs'>
      <div>
        业务验证：
        {!e
          ? '未记录'
          : new Date(e.valid_until).getTime() <= now
            ? '已过期'
            : e.status === 'passed'
              ? '通过'
              : '失败'}
      </div>
      {e && (
        <div className='text-muted-foreground'>
          来源：{e.recorded_by ?? e.kind} · 有效至{' '}
          {new Date(e.valid_until).toLocaleString()}
        </div>
      )}
      {e?.summary && <p className='max-w-64 whitespace-normal'>{e.summary}</p>}
      {e?.report_url && (
        <a
          className='underline'
          href={e.report_url}
          target='_blank'
          rel='noreferrer'
        >
          查看验证报告
        </a>
      )}
      {release.metadata?.scan && (
        <div>
          扫描报告：
          {new Date(release.metadata.scan.valid_until).getTime() <= now
            ? '已过期'
            : release.metadata.scan.status === 'passed'
              ? '通过'
              : '失败'}
          （仅报告）
        </div>
      )}
      {release.promotion_blockers?.map((reason) => (
        <div key={reason} className='text-amber-700'>
          {reason}
        </div>
      ))}
    </div>
  )
}

function ApprovalEvidence({
  releaseID,
  clusterID,
  expectedImage,
  templateHash,
}: {
  releaseID: number
  clusterID: number
  expectedImage?: string
  templateHash?: string
}) {
  const release = useQuery({
    queryKey: ['release', releaseID],
    queryFn: () => platformApi.release(releaseID),
    refetchInterval: 3000,
  })
  const current = useQuery({
    queryKey: ['release-current-image', releaseID, clusterID],
    queryFn: () => platformApi.currentImage(releaseID, clusterID),
    refetchInterval: 3000,
    retry: false,
  })
  const clusters = useQuery({
    queryKey: ['clusters'],
    queryFn: platformApi.clusters,
  })
  const cluster = clusters.data?.data.find((c) => c.id === clusterID)
  const r = release.data?.data
  return (
    <div className='max-w-80 space-y-1 text-xs whitespace-normal'>
      <div>
        {cluster?.name ?? `生产集群 #${clusterID}`} ·{' '}
        {cluster?.environment ?? '生产环境'}
      </div>
      {expectedImage && (
        <div className='break-all'>申请基线：{expectedImage}</div>
      )}
      <div className='break-all'>
        当前：{current.data?.data.image || '无法获取当前镜像'}
      </div>
      <div className='break-all'>
        候选：{r?.image_digest || '未固化 digest'}
      </div>
      {templateHash && (
        <div title={templateHash}>
          工作负载配置摘要：{templateHash.slice(0, 12)}
        </div>
      )}
      {r?.commit_id && <div>提交：{r.commit_id}</div>}
      {r && <div>来源：{releaseBuildIdentity(r)}</div>}
      {r && <EvidenceSummary release={r} />}
      <div>恢复范围：仅历史镜像回退；配置和数据库需单独确认</div>
    </div>
  )
}

export function VerificationDialog({
  release,
  onClose,
}: {
  release: ImageReleaseView
  onClose: () => void
}) {
  const client = useQueryClient()
  const [summary, setSummary] = useState('')
  const [report, setReport] = useState('')
  const [status, setStatus] = useState<'passed' | 'failed'>('passed')
  const record = useMutation({
    mutationFn: () =>
      platformApi.recordVerification(release.id, {
        digest: release.image_digest!,
        kind: 'manual',
        status,
        summary,
        report_url: report,
        valid_until: new Date(Date.now() + 86400000).toISOString(),
      }),
    onSuccess: () => {
      toast.success('验收结果已记录，有效期 24 小时')
      void client.invalidateQueries({ queryKey: ['releases'] })
      void client.invalidateQueries({ queryKey: ['release', release.id] })
      onClose()
    },
    onError: (err) =>
      toast.error(
        (err as { response?: { data?: { detail?: string } } }).response?.data
          ?.detail ?? '保存验收失败'
      ),
  })
  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title='记录业务验收'
      desc={`${release.app_name} · ${release.image_digest}`}
      confirmText='保存验收'
      disabled={!summary.trim() || (!!report && !/^https?:\/\//.test(report))}
      isLoading={record.isPending}
      handleConfirm={() => record.mutate()}
    >
      <div className='space-y-3'>
        <Label htmlFor='verification-status'>验收结论</Label>
        <Select
          value={status}
          onValueChange={(value) => setStatus(value as 'passed' | 'failed')}
        >
          <SelectTrigger id='verification-status'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='passed'>通过</SelectItem>
            <SelectItem value='failed'>失败</SelectItem>
          </SelectContent>
        </Select>
        <Label htmlFor='verification-summary'>验证内容与结论依据（必填）</Label>
        <Textarea
          id='verification-summary'
          value={summary}
          maxLength={2000}
          onChange={(e) => setSummary(e.target.value)}
          placeholder='检查了哪些业务流程，结果是什么'
        />
        <Label htmlFor='verification-report'>
          报告链接（可选，HTTP/HTTPS）
        </Label>
        <Input
          id='verification-report'
          value={report}
          onChange={(e) => setReport(e.target.value)}
          placeholder='https://…'
        />
        <p className='text-sm text-muted-foreground'>
          这条记录是人工验收声明。部署成功不会自动生成验证通过结果。
        </p>
      </div>
    </ConfirmDialog>
  )
}
