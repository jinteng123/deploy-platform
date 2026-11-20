import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, CircleAlert, ShieldCheck, X } from 'lucide-react'
import { toast } from 'sonner'
import { platformApi, type PromotionRequest } from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Textarea } from '@/components/ui/textarea'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ThemeSwitch } from '@/components/theme-switch'
import { PlanPreview } from './delivery-plan-dialog'

const approvalLabels: Record<PromotionRequest['status'], string> = {
  pending: '待审批',
  approved: '已批准',
  rejected: '已拒绝',
  cancelled: '已撤销',
  expired: '已过期',
}

function latestPlanApproval(
  requests: PromotionRequest[],
  planID: number
): PromotionRequest | undefined {
  return requests
    .filter((request) => request.delivery_plan_id === planID)
    .sort((left, right) => right.id - left.id)[0]
}

export function DeliveryPlanDetailPage({
  planID,
  backLink,
}: {
  planID: number
  backLink?: React.ReactNode
}) {
  const role = useAuthStore((state) => state.auth.user?.role)
  const username = useAuthStore((state) => state.auth.user?.username)
  const queryClient = useQueryClient()
  const [rejecting, setRejecting] = useState<PromotionRequest | null>(null)
  const [comment, setComment] = useState('')
  const plan = useQuery({
    queryKey: ['delivery-plan', planID],
    queryFn: () => platformApi.deliveryPlan(planID),
    enabled: Number.isSafeInteger(planID) && planID > 0,
    retry: false,
  })
  const approvals = useQuery({
    queryKey: ['promotion-requests'],
    queryFn: () => platformApi.promotionRequests(),
    refetchInterval: 5000,
    retry: false,
  })
  const request = latestPlanApproval(approvals.data?.data ?? [], planID)
  const invalidate = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ['promotion-requests'] }),
      queryClient.invalidateQueries({ queryKey: ['releases'] }),
      queryClient.invalidateQueries({ queryKey: ['delivery-plan', planID] }),
    ])
  const approve = useMutation({
    mutationFn: (id: number) => platformApi.approvePromotionRequest(id),
    onSuccess: () => {
      toast.success('审批已提交，发布状态正在更新')
      void invalidate()
    },
    onError: (error) => {
      const detail = (error as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail || '审批失败，请重新核对计划状态')
    },
  })
  const reject = useMutation({
    mutationFn: ({ id, note }: { id: number; note: string }) =>
      platformApi.rejectPromotionRequest(id, note),
    onSuccess: () => {
      toast.success('审批请求已关闭')
      void invalidate()
      setRejecting(null)
      setComment('')
    },
    onError: (error) => {
      const detail = (error as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail || '操作失败，请稍后重试')
    },
  })
  const isOperator = role === 'operator' || role === 'admin'
  const isRequester = request?.requester === username
  const canReview = request?.status === 'pending' && isOperator && !isRequester
  const hasApprovalScope = role === 'admin' || request?.can_approve === true
  const canDecide = canReview && hasApprovalScope
  const missingConfigurationSnapshot =
    plan.isSuccess &&
    (!Number.isSafeInteger(plan.data.data.snapshot.schema_version) ||
      plan.data.data.snapshot.schema_version < 7 ||
      !Array.isArray(plan.data.data.snapshot.target.configuration_references) ||
      !plan.data.data.snapshot.target.service_account_name?.trim())
  const canApprove =
    canDecide && plan.isSuccess && !missingConfigurationSnapshot

  return (
    <>
      <Header fixed>
        <div className='me-auto flex min-w-0 items-center gap-2 text-sm'>
          {backLink}
          <span aria-hidden='true' className='text-muted-foreground/60'>
            /
          </span>
          <span aria-current='page' className='truncate font-medium'>
            发布计划 #{planID}
          </span>
        </div>
        <ThemeSwitch />
      </Header>
      <Main>
        <div className='mb-6 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
          <div className='space-y-2'>
            <p className='text-xs font-semibold tracking-[0.12em] text-primary uppercase'>
              RELEASE REVIEW
            </p>
            <h1 className='text-2xl font-semibold tracking-tight sm:text-3xl'>
              发布计划详情
            </h1>
            <p className='max-w-3xl text-sm leading-6 text-muted-foreground'>
              审批前核对制品、目标资源、发布策略和恢复方式。此页面展示申请时固定的不可变快照。
            </p>
          </div>
          {request && (
            <Badge
              variant='outline'
              className='min-h-8 self-start sm:self-auto'
            >
              {approvalLabels[request.status]}
            </Badge>
          )}
        </div>

        {!Number.isSafeInteger(planID) || planID <= 0 ? (
          <p
            role='alert'
            className='rounded-xl border p-5 text-sm text-destructive'
          >
            发布计划编号无效。
          </p>
        ) : plan.isLoading ? (
          <Card>
            <CardContent className='p-6 text-sm text-muted-foreground'>
              正在读取固定计划快照…
            </CardContent>
          </Card>
        ) : plan.isError || !plan.data ? (
          <Card>
            <CardContent className='space-y-3 p-6'>
              <p role='alert' className='text-sm text-destructive'>
                无法读取计划 #{planID}。快照可能已清理，或当前账号没有查看权限。
              </p>
              <Button variant='outline' onClick={() => void plan.refetch()}>
                重新读取
              </Button>
            </CardContent>
          </Card>
        ) : (
          <div className='space-y-6'>
            <PlanPreview plan={plan.data.data} allowApproval={false} />

            <Card className='rounded-xl border-border shadow-sm'>
              <CardHeader className='pb-3'>
                <CardTitle className='flex items-center gap-2 text-base'>
                  <ShieldCheck
                    className='size-4 text-primary'
                    aria-hidden='true'
                  />
                  审批记录
                </CardTitle>
              </CardHeader>
              <CardContent>
                {approvals.isError ? (
                  <div
                    role='alert'
                    className='rounded-lg bg-destructive/5 p-4 text-sm text-destructive'
                  >
                    审批状态读取失败。为避免基于过期状态作决定，当前不开放审批操作。
                  </div>
                ) : approvals.isLoading ? (
                  <p className='rounded-lg bg-muted/40 p-4 text-sm text-muted-foreground'>
                    正在读取审批状态；读取完成前不会开放审批操作。
                  </p>
                ) : request ? (
                  <>
                    <dl className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
                      <div>
                        <dt className='text-xs text-muted-foreground'>
                          申请编号
                        </dt>
                        <dd className='mt-1 font-medium'>审批 #{request.id}</dd>
                      </div>
                      <div>
                        <dt className='text-xs text-muted-foreground'>
                          申请人
                        </dt>
                        <dd className='mt-1 font-medium break-all'>
                          {request.requester}
                        </dd>
                      </div>
                      <div>
                        <dt className='text-xs text-muted-foreground'>
                          审批人
                        </dt>
                        <dd className='mt-1 font-medium break-all'>
                          {request.approver || '尚未决定'}
                        </dd>
                      </div>
                      <div>
                        <dt className='text-xs text-muted-foreground'>
                          申请过期时间
                        </dt>
                        <dd className='mt-1 font-medium'>
                          {request.expires_at?.replace('T', ' ').slice(0, 16) ??
                            '未提供'}
                        </dd>
                      </div>
                    </dl>
                    {request.comment && (
                      <p className='mt-4 rounded-lg bg-muted/40 p-3 text-sm leading-6'>
                        处理意见：{request.comment}
                      </p>
                    )}
                    {isRequester && request.status === 'pending' && (
                      <p className='mt-4 text-sm text-muted-foreground'>
                        申请人不能审批自己的请求；请由另一位操作员核对并处理。
                      </p>
                    )}
                    {canReview && !hasApprovalScope && (
                      <p className='mt-4 rounded-lg border border-amber-300/70 bg-amber-50/70 p-3 text-sm leading-5 text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'>
                        你尚未获准审批此业务应用和生产环境。请联系管理员在「设置
                        → 发布审批范围」授权。
                      </p>
                    )}
                    {request.status === 'pending' &&
                      !isRequester &&
                      !isOperator && (
                        <p className='mt-4 text-sm text-muted-foreground'>
                          只有操作员或管理员可以批准或拒绝此请求。
                        </p>
                      )}
                    {canDecide && (
                      <div className='mt-5 space-y-4 border-t pt-4'>
                        {missingConfigurationSnapshot ? (
                          <div
                            role='alert'
                            className='flex min-w-0 items-start gap-3 rounded-lg border border-amber-200 bg-amber-50/80 p-3.5 text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'
                          >
                            <CircleAlert
                              aria-hidden='true'
                              className='mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-300'
                            />
                            <div className='min-w-0 space-y-1'>
                              <p className='text-sm leading-5 font-semibold'>
                                此计划缺少完整配置引用快照，不能批准
                              </p>
                              <p className='text-sm leading-6 text-amber-900/90 dark:text-amber-100/90'>
                                历史版本计划或读取不完整都不会按“没有配置引用”处理。请拒绝当前申请，检查集群读取权限后重新生成计划并提交审批。
                              </p>
                            </div>
                          </div>
                        ) : (
                          <p className='text-sm leading-6 text-muted-foreground'>
                            批准前服务端会重新验证制品、目标资源身份和计划基线。若任一项发生变化，请拒绝并让申请人重新生成计划。
                          </p>
                        )}
                        <div className='flex justify-end gap-2'>
                          <Button
                            variant='outline'
                            className='min-h-11'
                            disabled={reject.isPending || approve.isPending}
                            onClick={() => {
                              setComment('')
                              setRejecting(request)
                            }}
                          >
                            <X aria-hidden='true' />
                            拒绝
                          </Button>
                          {canApprove && (
                            <Button
                              className='min-h-11'
                              disabled={approve.isPending || reject.isPending}
                              onClick={() => approve.mutate(request.id)}
                            >
                              <Check aria-hidden='true' />
                              {approve.isPending ? '正在复核…' : '批准发布'}
                            </Button>
                          )}
                        </div>
                      </div>
                    )}
                  </>
                ) : (
                  <p className='text-sm text-muted-foreground'>
                    此计划尚无审批请求。只有符合服务端策略和预检条件的计划才可提交审批。
                  </p>
                )}
              </CardContent>
            </Card>
          </div>
        )}
      </Main>

      {rejecting && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setRejecting(null)
          }}
          title='拒绝发布审批'
          desc='拒绝后本次申请不会执行。说明原因有助于申请人修正计划并重新提交。'
          destructive
          confirmText='确认拒绝'
          isLoading={reject.isPending}
          handleConfirm={() =>
            reject.mutate({ id: rejecting.id, note: comment.trim() })
          }
        >
          <Textarea
            aria-label='拒绝原因'
            placeholder='拒绝原因（可选）'
            value={comment}
            onChange={(event) => setComment(event.target.value)}
            rows={3}
          />
        </ConfirmDialog>
      )}
    </>
  )
}
