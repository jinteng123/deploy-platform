import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { BadgeCheck, LoaderCircle, ShieldCheck, Users } from 'lucide-react'
import { toast } from 'sonner'
import { platformApi } from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'

const scopeKey = (applicationId: number, environmentId: number) =>
  `${applicationId}:${environmentId}`

export function ApprovalScopesPage() {
  const queryClient = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const [selectedUserId, setSelectedUserId] = useState<number | undefined>()
  const [scopeOverrides, setScopeOverrides] = useState<
    Record<number, Set<string>>
  >({})
  const users = useQuery({
    queryKey: ['admin-users'],
    queryFn: () => platformApi.users().then((response) => response.data),
  })
  const model = useQuery({
    queryKey: ['delivery-model'],
    queryFn: () =>
      platformApi.deliveryModel().then((response) => response.data),
  })
  const grants = useQuery({
    queryKey: ['delivery-approval-grants'],
    queryFn: () =>
      platformApi.deliveryApprovalGrants().then((response) => response.data),
  })
  const operators = useMemo(
    () =>
      (users.data ?? []).filter(
        (candidate) => candidate.role === 'operator' && candidate.enabled
      ),
    [users.data]
  )
  const eligibleScopes = useMemo(() => {
    if (!model.data) return []
    const result: Array<{
      applicationId: number
      applicationName: string
      environmentId: number
      environmentName: string
      componentNames: string[]
    }> = []
    for (const application of model.data.applications) {
      for (const environment of model.data.environments) {
        if (
          environment.environment_tier !== 'production' ||
          environment.requires_review
        )
          continue
        const bindings = model.data.bindings.filter(
          (binding) =>
            binding.application_id === application.id &&
            binding.environment_id === environment.id &&
            !binding.requires_review
        )
        const componentNames = bindings
          .map(
            (binding) =>
              model.data?.components.find(
                (component) =>
                  component.id === binding.component_id &&
                  component.application_id === application.id &&
                  !component.requires_review
              )?.name
          )
          .filter((name): name is string => !!name)
        if (componentNames.length === 0) continue
        result.push({
          applicationId: application.id,
          applicationName: application.name,
          environmentId: environment.id,
          environmentName: environment.name,
          componentNames: Array.from(new Set(componentNames)).sort(),
        })
      }
    }
    return result.sort((a, b) =>
      `${a.applicationName}/${a.environmentName}`.localeCompare(
        `${b.applicationName}/${b.environmentName}`,
        'zh-CN'
      )
    )
  }, [model.data])

  const activeUserId = selectedUserId ?? operators[0]?.id
  const checkedScopes = useMemo(() => {
    if (activeUserId === undefined) return new Set<string>()
    return (
      scopeOverrides[activeUserId] ??
      new Set(
        (grants.data ?? [])
          .filter((grant) => grant.user_id === activeUserId)
          .map((grant) => scopeKey(grant.application_id, grant.environment_id))
      )
    )
  }, [activeUserId, grants.data, scopeOverrides])

  const save = useMutation({
    mutationFn: () => {
      if (activeUserId === undefined) throw new Error('请选择 operator')
      return platformApi.replaceDeliveryApprovalGrants(
        activeUserId,
        eligibleScopes
          .filter((scope) =>
            checkedScopes.has(
              scopeKey(scope.applicationId, scope.environmentId)
            )
          )
          .map((scope) => ({
            application_id: scope.applicationId,
            environment_id: scope.environmentId,
          }))
      )
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ['delivery-approval-grants'],
      })
      toast.success('审批范围已保存')
    },
    onError: () => toast.error('保存审批范围失败，请检查目标状态后重试'),
  })

  const selectedUser = operators.find(
    (candidate) => candidate.id === activeUserId
  )
  const hasError = users.isError || model.isError || grants.isError
  const isLoading = users.isPending || model.isPending || grants.isPending

  return (
    <>
      <Header>
        <ThemeSwitch />
        <ProfileDropdown />
      </Header>
      <Main className='space-y-6'>
        <div className='flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between'>
          <div className='space-y-1'>
            <p className='text-xs font-semibold tracking-[0.16em] text-primary uppercase'>
              访问控制
            </p>
            <h1 className='text-2xl font-semibold tracking-tight'>
              发布审批范围
            </h1>
            <p className='max-w-2xl text-sm text-muted-foreground'>
              Operator
              只能批准已授权的业务应用与生产环境。申请人仍不能审批自己的发布。
            </p>
          </div>
          <div className='flex items-center gap-2 self-start rounded-xl border bg-card px-3 py-2 text-xs text-muted-foreground sm:self-auto'>
            <ShieldCheck className='size-4 text-primary' aria-hidden='true' />
            <span>管理员保留全局审批权限</span>
          </div>
        </div>

        {user?.role !== 'admin' ? (
          <section className='rounded-2xl border border-amber-500/30 bg-amber-500/5 p-5 text-sm'>
            此页面仅供管理员管理审批范围。
          </section>
        ) : hasError ? (
          <section className='rounded-2xl border border-destructive/30 bg-destructive/5 p-5'>
            <p className='font-medium'>审批范围信息读取失败</p>
            <p className='mt-1 text-sm text-muted-foreground'>
              请检查用户和发布模型接口后重试。
            </p>
            <Button
              className='mt-4'
              variant='outline'
              onClick={() => {
                void users.refetch()
                void model.refetch()
                void grants.refetch()
              }}
            >
              重试读取
            </Button>
          </section>
        ) : isLoading ? (
          <section className='flex min-h-48 items-center justify-center gap-2 rounded-2xl border bg-card text-sm text-muted-foreground'>
            <LoaderCircle className='size-4 animate-spin' aria-hidden='true' />
            正在读取用户与已核对的生产目标
          </section>
        ) : (
          <div className='grid gap-5 lg:grid-cols-[minmax(220px,0.35fr)_minmax(0,1fr)]'>
            <section className='h-fit rounded-2xl border bg-card p-4 sm:p-5'>
              <div className='flex items-center gap-2'>
                <Users className='size-4 text-primary' aria-hidden='true' />
                <h2 className='font-semibold'>选择审批人</h2>
              </div>
              <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                仅显示启用中的 operator。授权按业务应用和逻辑环境生效。
              </p>
              {operators.length === 0 ? (
                <p className='mt-5 rounded-xl bg-muted/50 p-3 text-sm text-muted-foreground'>
                  还没有可配置的 operator 账号。
                </p>
              ) : (
                <div className='mt-4 space-y-2'>
                  {operators.map((candidate) => {
                    const count = (grants.data ?? []).filter(
                      (grant) => grant.user_id === candidate.id
                    ).length
                    return (
                      <button
                        key={candidate.id}
                        type='button'
                        onClick={() => setSelectedUserId(candidate.id)}
                        className={`flex min-h-12 w-full items-center justify-between gap-3 rounded-xl border px-3 text-left transition-colors focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none ${
                          activeUserId === candidate.id
                            ? 'border-primary/40 bg-primary/5'
                            : 'bg-background hover:bg-muted/50'
                        }`}
                      >
                        <span className='min-w-0'>
                          <span className='block truncate text-sm font-medium'>
                            {candidate.display_name || candidate.username}
                          </span>
                          <span className='block truncate text-xs text-muted-foreground'>
                            @{candidate.username}
                          </span>
                        </span>
                        <span className='shrink-0 text-xs text-muted-foreground'>
                          {count} 个范围
                        </span>
                      </button>
                    )
                  })}
                </div>
              )}
            </section>

            <section className='rounded-2xl border bg-card p-4 sm:p-6'>
              <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
                <div>
                  <div className='flex items-center gap-2'>
                    <BadgeCheck
                      className='size-4 text-primary'
                      aria-hidden='true'
                    />
                    <h2 className='font-semibold'>可批准的生产范围</h2>
                  </div>
                  <p className='mt-1 text-sm text-muted-foreground'>
                    {selectedUser
                      ? `为 ${selectedUser.display_name || selectedUser.username} 配置范围；未勾选的发布会被服务端拒绝。`
                      : '先选择一个 operator。'}
                  </p>
                </div>
                <Button
                  onClick={() => save.mutate()}
                  disabled={
                    !selectedUser ||
                    eligibleScopes.length === 0 ||
                    save.isPending
                  }
                  className='min-h-11 w-full sm:w-auto'
                >
                  {save.isPending ? '正在保存…' : '保存审批范围'}
                </Button>
              </div>

              {eligibleScopes.length === 0 ? (
                <div className='mt-5 rounded-xl border border-dashed p-5 text-sm'>
                  <p className='font-medium'>没有可授权的生产目标</p>
                  <p className='mt-1 text-muted-foreground'>
                    请先在“应用与组件”中完成业务应用、组件和生产环境的绑定核对。
                  </p>
                </div>
              ) : (
                <div className='mt-5 grid gap-3 sm:grid-cols-2'>
                  {eligibleScopes.map((scope) => {
                    const key = scopeKey(
                      scope.applicationId,
                      scope.environmentId
                    )
                    const checked = checkedScopes.has(key)
                    return (
                      <label
                        key={key}
                        className={`flex min-h-24 cursor-pointer items-start gap-3 rounded-xl border p-4 transition-colors focus-within:ring-2 focus-within:ring-ring/60 ${
                          checked
                            ? 'border-primary/50'
                            : 'border-border hover:border-primary/40'
                        }`}
                        style={{
                          backgroundColor: checked
                            ? 'color-mix(in oklab, var(--primary) 8%, var(--background))'
                            : 'var(--background)',
                          color: 'var(--foreground)',
                        }}
                      >
                        <Checkbox
                          checked={checked}
                          onCheckedChange={(next) => {
                            if (activeUserId === undefined) return
                            setScopeOverrides((current) => {
                              const persisted = new Set(
                                (grants.data ?? [])
                                  .filter(
                                    (grant) => grant.user_id === activeUserId
                                  )
                                  .map((grant) =>
                                    scopeKey(
                                      grant.application_id,
                                      grant.environment_id
                                    )
                                  )
                              )
                              const changed = new Set(
                                current[activeUserId] ?? persisted
                              )
                              if (next === true) changed.add(key)
                              else changed.delete(key)
                              return { ...current, [activeUserId]: changed }
                            })
                          }}
                          aria-label={`${scope.applicationName}，${scope.environmentName}`}
                        />
                        <span className='min-w-0 flex-1'>
                          <span className='flex flex-wrap items-center gap-2'>
                            <span className='font-medium'>
                              {scope.applicationName}
                            </span>
                            <span
                              className='text-muted-foreground'
                              style={{ color: 'var(--muted-foreground)' }}
                              aria-hidden='true'
                            >
                              /
                            </span>
                            <span
                              className='text-sm text-muted-foreground'
                              style={{ color: 'var(--muted-foreground)' }}
                            >
                              {scope.environmentName}
                            </span>
                          </span>
                          <span
                            className='mt-1 block text-xs leading-5 text-muted-foreground'
                            style={{ color: 'var(--muted-foreground)' }}
                          >
                            适用组件：{scope.componentNames.join('、')}
                          </span>
                        </span>
                      </label>
                    )
                  })}
                </div>
              )}
              <p className='mt-5 border-t pt-4 text-xs leading-5 text-muted-foreground'>
                只有已确认的生产绑定会显示在此处。保存会替换该 operator
                的全部范围；空选后保存将撤销其所有审批授权。
              </p>
            </section>
          </div>
        )}
      </Main>
    </>
  )
}
