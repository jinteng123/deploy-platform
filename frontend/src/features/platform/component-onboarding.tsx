import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  ArrowDown,
  ArrowRight,
  Check,
  Copy,
  GitBranch,
  Layers,
  Loader2,
  Server,
} from 'lucide-react'
import { toast } from 'sonner'
import {
  k8sApi,
  platformApi,
  type BindingPreview,
  type Cluster,
  type ComponentBinding,
  type ComponentBindingPayload,
} from '@/api/client'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  ciEventExample,
  matchCIEventToBinding,
  repositoryOf,
  workloadContainers,
} from './component-binding'
import { StrategyCapabilityPreview } from './strategy-capability-preview'

const selectClass =
  'h-11 w-full min-w-0 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50'
const detail = (err: unknown) =>
  (err as { response?: { data?: { detail?: string } } })?.response?.data
    ?.detail ?? '检查失败，请确认集群连接和资源读取权限后重试。'

function Field({
  id,
  label,
  hint,
  hintStatus = false,
  children,
}: {
  id: string
  label: string
  hint?: string
  hintStatus?: boolean
  children: React.ReactNode
}) {
  return (
    <div className='min-w-0 space-y-2'>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && (
        <p
          role={hintStatus ? 'status' : undefined}
          aria-live={hintStatus ? 'polite' : undefined}
          className='flex items-center gap-2 text-xs text-muted-foreground'
        >
          {hintStatus && (
            <Loader2
              aria-hidden='true'
              className='size-3.5 shrink-0 motion-safe:animate-spin'
            />
          )}
          <span>{hint}</span>
        </p>
      )}
    </div>
  )
}

export function ComponentOnboarding({
  editing,
  copyFrom,
  clusters,
  existing,
  onClose,
  onNextStep,
  onSaved,
}: {
  editing?: ComponentBinding
  copyFrom?: ComponentBinding
  clusters: Cluster[]
  existing: ComponentBinding[]
  onClose: () => void
  onNextStep: (appID: number) => void
  onSaved: () => void
}) {
  const seed = editing ?? copyFrom
  const [step, setStep] = useState(0)
  const [form, setForm] = useState<ComponentBindingPayload>(() => ({
    app_name: seed?.app_name ?? '',
    source_kind:
      seed?.source_kind ?? (seed?.source_repo_url ? 'repository' : 'unknown'),
    source_repo_url: seed?.source_repo_url ?? '',
    source_path: seed?.source_path || '.',
    deployment_config_repo_url: seed?.deployment_config_repo_url ?? '',
    deployment_config_path: seed?.deployment_config_path ?? '',
    image_repository: seed?.image_repository ?? '',
    cluster_id: editing?.cluster_id ?? 0,
    namespace: editing?.namespace ?? '',
    deployment_name: editing?.deployment_name ?? '',
    container_name: editing?.container_name ?? '',
    match_keyword: seed?.match_keyword ?? '',
    harbor_project: seed?.harbor_project ?? 'library',
    description: seed?.description ?? '',
  }))
  const [error, setError] = useState('')
  const [preview, setPreview] = useState<BindingPreview | null>(null)
  const [sampleAppName, setSampleAppName] = useState('')
  const [sampleTargetCluster, setSampleTargetCluster] = useState('')
  const [sampleImage, setSampleImage] = useState('')
  const [sampleSourceRepository, setSampleSourceRepository] = useState('')
  const [sampleSourcePath, setSampleSourcePath] = useState('')
  const [confirmed, setConfirmed] = useState(false)
  const contentRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    contentRef.current?.scrollTo({ top: 0 })
  }, [step, preview])
  const cluster = clusters.find((c) => c.id === form.cluster_id)
  const namespaces = useQuery({
    queryKey: ['binding-namespaces', form.cluster_id],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: form.cluster_id,
        version: 'v1',
        resource: 'namespaces',
      }),
    enabled: step === 1 && !!cluster?.enabled,
    retry: false,
  })
  const workloads = useQuery({
    queryKey: ['binding-deployments', form.cluster_id, form.namespace],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: form.cluster_id,
        group: 'apps',
        version: 'v1',
        resource: 'deployments',
        namespace: form.namespace,
      }),
    enabled: step === 1 && !!cluster?.enabled && !!form.namespace,
    retry: false,
  })
  const objects = workloads.data?.data.items ?? []
  const containers = workloadContainers(
    objects.find((o) => o.metadata?.name === form.deployment_name)
  )
  const selectedImage = containers.find(
    (c) => c.name === form.container_name
  )?.image
  const workloadHint = !cluster?.enabled
    ? '先选择一个已启用的集群。'
    : !form.namespace
      ? '先选择命名空间。'
      : workloads.isPending
        ? '正在读取此命名空间的 Deployment…'
        : workloads.isSuccess && !objects.length
          ? '此命名空间暂无可选择的 Deployment。请先部署服务，或选择其他命名空间。'
          : undefined
  const workloadLoading =
    workloads.isPending && !!cluster?.enabled && !!form.namespace
  const targetResourcesLoading =
    step === 1 &&
    !!cluster?.enabled &&
    (namespaces.isFetching || workloads.isFetching)
  const payload = (): ComponentBindingPayload => ({
    ...form,
    app_name: form.app_name.trim(),
    source_repo_url: form.source_repo_url.trim(),
    source_path:
      form.source_kind === 'repository' ? form.source_path.trim() || '.' : '',
    deployment_config_repo_url: form.deployment_config_repo_url?.trim() ?? '',
    deployment_config_path: form.deployment_config_repo_url?.trim()
      ? form.deployment_config_path?.trim() || '.'
      : '',
    image_repository: form.image_repository.trim(),
    match_keyword: form.match_keyword.trim() || form.app_name.trim(),
  })
  const check = useMutation({
    mutationFn: () => platformApi.previewApp(payload()),
    onSuccess: ({ data }) => {
      setPreview(data)
      setStep(2)
      setSampleAppName(form.app_name.trim())
      setSampleTargetCluster(cluster?.env_code ?? '')
      setSampleImage(`${form.image_repository.trim()}:next`)
      setSampleSourceRepository(
        form.source_kind === 'repository' ? form.source_repo_url.trim() : ''
      )
      setSampleSourcePath(
        form.source_kind === 'repository' ? form.source_path.trim() || '.' : ''
      )
      setConfirmed(false)
      setError('')
    },
    onError: (err) => setError(detail(err)),
  })
  const save = useMutation({
    mutationFn: () =>
      editing
        ? platformApi.updateApp(editing.id, payload())
        : platformApi.createApp(payload()),
    onSuccess: ({ data }) => {
      toast.success('组件绑定已保存，未更新集群镜像', {
        description: '下一步：关联业务应用、逻辑组件和环境，再选择发布策略。',
        action: {
          label: '关联业务应用与环境',
          onClick: () => onNextStep(data.id),
        },
        duration: 10000,
      })
      onSaved()
      onClose()
    },
    onError: (err) => setError(detail(err)),
  })
  const update = (patch: Partial<ComponentBindingPayload>) => {
    setForm((f) => ({ ...f, ...patch }))
    setError('')
    setPreview(null)
    setConfirmed(false)
  }
  const updateSampleValue = (
    setter: (value: string) => void,
    value: string
  ) => {
    setter(value)
    setConfirmed(false)
  }
  const next = () => {
    setError('')
    if (step === 0) {
      if (!form.app_name.trim()) {
        setError('请填写组件标识，CI 将用它定位组件。')
        return
      }
      if (form.source_kind === 'repository' && !form.source_repo_url.trim()) {
        setError(
          '已选择代码仓库来源，请填写仓库地址；没有仓库时请选择第三方镜像或待登记。'
        )
        return
      }
      if (form.source_repo_url.trim()) {
        try {
          const u = new URL(form.source_repo_url.trim())
          if (
            u.protocol !== 'https:' ||
            u.username ||
            u.password ||
            u.search ||
            u.hash ||
            u.pathname === '/'
          )
            throw new Error()
        } catch {
          setError(
            '请填写不含凭据的 HTTPS 代码仓库地址，例如 https://git.example.com/team/order.git。'
          )
          return
        }
      }
      if (form.deployment_config_repo_url?.trim()) {
        try {
          const u = new URL(form.deployment_config_repo_url.trim())
          const configPath = form.deployment_config_path?.trim() || '.'
          if (
            u.protocol !== 'https:' ||
            u.username ||
            u.password ||
            u.search ||
            u.hash ||
            u.pathname === '/' ||
            configPath.startsWith('/') ||
            configPath.includes('\\') ||
            (configPath
              .split('/')
              .some((part) => ['', '.', '..'].includes(part)) &&
              configPath !== '.')
          )
            throw new Error()
        } catch {
          setError(
            '部署配置仓库须为无凭据的 HTTPS 地址，目录须为仓库内相对路径。'
          )
          return
        }
      } else if (form.deployment_config_path?.trim()) {
        setError('填写部署配置目录前，请先填写配置仓库地址。')
        return
      }
      setStep(1)
    } else {
      if (
        !cluster?.enabled ||
        !form.namespace ||
        !form.deployment_name ||
        !form.container_name
      ) {
        setError('请依次选择集群、命名空间、Deployment 和容器。')
        return
      }
      if (
        !form.image_repository.trim() ||
        repositoryOf(`${form.image_repository.trim()}:check`) !==
          form.image_repository.trim()
      ) {
        setError(
          '请填写完整镜像仓库，例如 registry.example.com/team/order-api，不含 tag 或 digest。'
        )
        return
      }
      if (
        existing.some(
          (a) =>
            a.id !== editing?.id &&
            a.cluster_id === form.cluster_id &&
            a.app_name === form.app_name.trim()
        )
      ) {
        setError('该组件已在此集群绑定，请编辑已有绑定或选择另一个集群。')
        return
      }
      check.mutate()
    }
  }
  const production = cluster?.environment_tier === 'production'
  const autoTest = !production && cluster?.deploy_policy === 'automatic'
  const sampleMatch = matchCIEventToBinding(
    {
      appName: sampleAppName,
      targetCluster: sampleTargetCluster,
      imageFull: sampleImage,
      sourceRepository: sampleSourceRepository,
      sourcePath: sampleSourcePath,
    },
    {
      appName: form.app_name,
      targetCluster: cluster?.env_code ?? '',
      imageRepository: form.image_repository,
      sourceKind: form.source_kind,
      sourceRepository: form.source_repo_url,
      sourcePath: form.source_path,
    }
  )
  const steps = [
    { name: '组件与代码来源', shortName: '组件来源', icon: GitBranch },
    { name: '镜像与部署目标', shortName: '部署目标', icon: Server },
    { name: '核对与接入 CI', shortName: '核对接入', icon: Check },
  ]
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !save.isPending) onClose()
      }}
    >
      <DialogContent className='max-h-[94dvh] w-[calc(100%-2rem)] max-w-[calc(100%-2rem)] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-3xl'>
        <DialogHeader className='space-y-2 border-b px-5 py-4 text-start sm:px-6'>
          <DialogTitle>
            {editing
              ? '编辑组件绑定'
              : copyFrom
                ? '为组件添加环境'
                : '接入一个组件'}
          </DialogTitle>
          <DialogDescription>
            把代码来源、镜像仓库和集群容器连起来。完成设置后，每次镜像更新都有明确的部署位置。
          </DialogDescription>
          <ol aria-label='接入步骤' className='grid grid-cols-3 gap-2'>
            {steps.map(({ name, shortName, icon: StepIcon }, i) => {
              const Icon = i < step ? Check : StepIcon
              return (
                <li
                  key={name}
                  aria-current={i === step ? 'step' : undefined}
                  className={`min-w-0 rounded-lg p-2 text-xs sm:p-3 sm:text-sm ${i === step ? 'bg-primary/10 text-primary' : 'bg-muted text-muted-foreground'}`}
                >
                  <div className='flex flex-col items-center gap-1 text-center sm:flex-row sm:gap-2 sm:text-start'>
                    <Icon className='size-4 shrink-0' />
                    <span className='sm:hidden'>
                      {i + 1}. {shortName}
                    </span>
                    <span className='hidden sm:inline'>
                      {i + 1}. {name}
                    </span>
                  </div>
                </li>
              )
            })}
          </ol>
        </DialogHeader>
        <div
          ref={contentRef}
          data-testid='component-onboarding-scroll-content'
          className='min-h-0 overflow-y-auto px-5 py-4 sm:px-6'
        >
          <fieldset
            disabled={check.isPending || save.isPending}
            className='min-w-0 space-y-5'
          >
            {step === 0 && (
              <div className='space-y-5'>
                <Field
                  id='component-name'
                  label='组件标识（CI 的 APP_NAME）'
                  hint='一个可独立更新的服务对应一个标识；同一组件在测试、生产环境使用相同标识。'
                >
                  <Input
                    id='component-name'
                    disabled={!!editing || !!copyFrom}
                    value={form.app_name}
                    maxLength={128}
                    placeholder='例如 order-api'
                    onChange={(e) => update({ app_name: e.target.value })}
                  />
                </Field>
                <Field
                  id='source-kind'
                  label='镜像由哪里构建'
                  hint='来源关系用于接入核对和追溯；镜像构建仍由云效或其他 CI 执行。'
                >
                  <select
                    id='source-kind'
                    className={selectClass}
                    value={form.source_kind}
                    onChange={(event) => {
                      const sourceKind = event.target
                        .value as ComponentBindingPayload['source_kind']
                      update({
                        source_kind: sourceKind,
                        ...(sourceKind === 'repository'
                          ? {}
                          : { source_repo_url: '', source_path: '' }),
                      })
                    }}
                  >
                    <option value='unknown'>来源待登记</option>
                    <option value='repository'>从代码仓库构建</option>
                    <option value='third_party'>第三方镜像</option>
                  </select>
                </Field>
                {form.source_kind === 'repository' && (
                  <Field
                    id='source-repo'
                    label='代码仓库地址'
                    hint='填写仓库主页的 HTTPS 地址。平台记录来源，构建仍由你的 CI 执行。'
                  >
                    <Input
                      id='source-repo'
                      value={form.source_repo_url}
                      maxLength={1024}
                      placeholder='https://git.example.com/team/order.git'
                      onChange={(e) =>
                        update({ source_repo_url: e.target.value })
                      }
                    />
                  </Field>
                )}
                {form.source_kind === 'repository' && form.source_repo_url && (
                  <Field
                    id='source-path'
                    label='仓库内构建目录'
                    hint='整个仓库填 .；一个仓库有多个服务时，填写这个组件所在的目录。'
                  >
                    <Input
                      id='source-path'
                      value={form.source_path}
                      maxLength={512}
                      placeholder='例如 services/order-api'
                      onChange={(e) => update({ source_path: e.target.value })}
                    />
                  </Field>
                )}
                <Field
                  id='deployment-config-repo'
                  label='Helm / Git 部署配置仓库（可选）'
                  hint='这是部署 values、清单或配置所在的仓库，可以与代码仓库不同。登记后，Runner 固定该仓库和目录；每次构建只需上报本次 commit。'
                >
                  <Input
                    id='deployment-config-repo'
                    value={form.deployment_config_repo_url ?? ''}
                    maxLength={1024}
                    placeholder='https://git.example.com/team/deploy-config.git'
                    onChange={(e) => {
                      const repository = e.target.value
                      update({
                        deployment_config_repo_url: repository,
                        deployment_config_path: repository.trim()
                          ? form.deployment_config_path?.trim() || '.'
                          : '',
                      })
                    }}
                  />
                </Field>
                {!!form.deployment_config_repo_url?.trim() && (
                  <Field
                    id='deployment-config-path'
                    label='配置仓库内目录'
                    hint='填写 Helm chart 或部署清单所在目录；仓库根目录填 .。该目录会和每次构建的 commit 一起固定。'
                  >
                    <Input
                      id='deployment-config-path'
                      value={form.deployment_config_path ?? '.'}
                      maxLength={512}
                      placeholder='例如 charts/order-api'
                      onChange={(e) =>
                        update({ deployment_config_path: e.target.value })
                      }
                    />
                  </Field>
                )}
                <div className='rounded-lg bg-muted p-4 text-sm text-muted-foreground'>
                  {form.source_kind === 'third_party'
                    ? '第三方镜像会明确标记为无代码仓库来源；仍需填写精确镜像仓库并核对部署目标。'
                    : form.source_kind === 'unknown'
                      ? '当前只登记了部署目标，来源尚未确认。后续可补充代码仓库或明确选择第三方镜像。'
                      : '例如：订单系统仓库中的 services/order-api → order-api 组件。同一个仓库可以按目录拆成多个组件。'}
                </div>
              </div>
            )}
            {step === 1 && (
              <div className='space-y-5'>
                <div className='grid gap-4 sm:grid-cols-2'>
                  <Field id='binding-cluster' label='部署到哪个集群'>
                    <select
                      id='binding-cluster'
                      className={selectClass}
                      value={form.cluster_id || ''}
                      onChange={(e) => {
                        const c = clusters.find(
                          (v) => v.id === Number(e.target.value)
                        )
                        update({
                          cluster_id: c?.id ?? 0,
                          namespace: c?.default_namespace || 'default',
                          deployment_name: '',
                          container_name: '',
                        })
                      }}
                    >
                      <option value=''>选择已接入的集群</option>
                      {clusters.map((c) => (
                        <option key={c.id} value={c.id} disabled={!c.enabled}>
                          {c.name} ·{' '}
                          {c.environment_tier === 'production'
                            ? '生产'
                            : '非生产'}
                          {!c.enabled && '（已禁用）'}
                        </option>
                      ))}
                    </select>
                    {!clusters.length && (
                      <a href='/clusters' className='text-sm underline'>
                        先接入集群
                      </a>
                    )}
                  </Field>
                  <Field
                    id='binding-namespace'
                    label='命名空间'
                    hint={
                      namespaces.isPending && cluster?.enabled
                        ? `正在读取${cluster.name}的命名空间…`
                        : undefined
                    }
                    hintStatus={namespaces.isPending && !!cluster?.enabled}
                  >
                    <select
                      id='binding-namespace'
                      className={selectClass}
                      value={form.namespace}
                      disabled={
                        !cluster?.enabled ||
                        namespaces.isPending ||
                        namespaces.isError
                      }
                      onChange={(e) =>
                        update({
                          namespace: e.target.value,
                          deployment_name: '',
                          container_name: '',
                        })
                      }
                    >
                      <option value=''>选择命名空间</option>
                      {[
                        ...new Set([
                          form.namespace,
                          ...(namespaces.data?.data.items ?? []).map(
                            (n) => n.metadata?.name ?? ''
                          ),
                        ]),
                      ]
                        .filter(Boolean)
                        .map((ns) => (
                          <option key={ns} value={ns}>
                            {ns}
                          </option>
                        ))}
                    </select>
                  </Field>
                  <Field
                    id='binding-workload'
                    label='要更新哪个 Deployment'
                    hint={workloadHint}
                    hintStatus={workloadLoading}
                  >
                    <select
                      id='binding-workload'
                      className={selectClass}
                      value={form.deployment_name}
                      disabled={
                        !form.namespace ||
                        workloads.isPending ||
                        workloads.isError ||
                        (workloads.isSuccess && !objects.length)
                      }
                      onChange={(e) => {
                        const cs = workloadContainers(
                          objects.find(
                            (o) => o.metadata?.name === e.target.value
                          )
                        )
                        update({
                          deployment_name: e.target.value,
                          container_name: cs.length === 1 ? cs[0].name : '',
                          ...(!form.image_repository && cs.length === 1
                            ? { image_repository: repositoryOf(cs[0].image) }
                            : {}),
                        })
                      }}
                    >
                      <option value=''>
                        {!cluster?.enabled
                          ? '先选择一个已启用的集群'
                          : !form.namespace
                            ? '先选择命名空间'
                            : workloads.isPending
                              ? '正在读取 Deployment…'
                              : workloads.isError
                                ? '读取失败，请查看提示'
                                : workloads.isSuccess && !objects.length
                                  ? '当前命名空间没有 Deployment'
                                  : '选择一个 Deployment'}
                      </option>
                      {objects.map((o) => (
                        <option key={o.metadata?.name} value={o.metadata?.name}>
                          {o.metadata?.name}
                        </option>
                      ))}
                    </select>
                  </Field>
                  <Field
                    id='binding-container'
                    label='要替换哪个容器的镜像'
                    hint={
                      form.deployment_name && !containers.length
                        ? '此 Deployment 未返回容器定义，因此无法绑定镜像。'
                        : '一个 Deployment 可有多个容器；请选择 CI 实际更新镜像的那个。'
                    }
                  >
                    <select
                      id='binding-container'
                      className={selectClass}
                      value={form.container_name}
                      disabled={!containers.length}
                      onChange={(e) => {
                        const c = containers.find(
                          (v) => v.name === e.target.value
                        )
                        update({
                          container_name: e.target.value,
                          ...(!form.image_repository && c
                            ? { image_repository: repositoryOf(c.image) }
                            : {}),
                        })
                      }}
                    >
                      <option value=''>
                        {!form.deployment_name
                          ? '先选择 Deployment'
                          : containers.length
                            ? '选择一个容器'
                            : '所选 Deployment 没有容器'}
                      </option>
                      {containers.map((c) => (
                        <option key={c.name} value={c.name}>
                          {c.name}
                        </option>
                      ))}
                    </select>
                  </Field>
                </div>
                {(namespaces.isError || workloads.isError) && (
                  <div
                    role='alert'
                    className='space-y-2 text-sm text-destructive'
                  >
                    <p>无法读取集群资源，请确认连接和读取权限。</p>
                    {namespaces.isError && (
                      <p>命名空间：{detail(namespaces.error)}</p>
                    )}
                    {workloads.isError && (
                      <p>Deployment：{detail(workloads.error)}</p>
                    )}
                    <Button
                      variant='outline'
                      size='sm'
                      onClick={() => {
                        void namespaces.refetch()
                        if (form.namespace) void workloads.refetch()
                      }}
                    >
                      重新读取
                    </Button>
                  </div>
                )}
                {selectedImage && (
                  <p className='rounded-lg bg-muted p-3 text-sm break-all'>
                    所选容器当前镜像：<code>{selectedImage}</code>
                  </p>
                )}
                {!!form.cluster_id &&
                  form.namespace &&
                  form.deployment_name && (
                    <StrategyCapabilityPreview
                      clusterId={form.cluster_id}
                      namespace={form.namespace}
                      deploymentName={form.deployment_name}
                    />
                  )}
                <Field
                  id='image-repo'
                  label='这个组件使用的镜像仓库'
                  hint='只填 registry/项目/镜像名称，版本由每次构建提供。可从所选容器自动带入，也可填写 CI 实际推送的位置。'
                >
                  <Input
                    id='image-repo'
                    value={form.image_repository}
                    maxLength={512}
                    placeholder='registry.example.com/team/order-api'
                    onChange={(e) =>
                      update({ image_repository: e.target.value })
                    }
                  />
                  {selectedImage && repositoryOf(selectedImage) && (
                    <Button
                      variant='outline'
                      size='sm'
                      onClick={() =>
                        update({
                          image_repository: repositoryOf(selectedImage),
                        })
                      }
                    >
                      使用当前容器的镜像仓库
                    </Button>
                  )}
                </Field>
                <section
                  aria-label='构建会更新这里'
                  className='space-y-2 rounded-xl border bg-card p-3 sm:space-y-3 sm:p-4'
                >
                  <div>
                    <h3 className='text-sm font-semibold'>构建会更新这里</h3>
                    <p className='mt-1 text-xs text-muted-foreground'>
                      仅展示匹配关系，不会保存配置或改动集群。
                    </p>
                  </div>
                  <div className='flex flex-col gap-1 sm:flex-row sm:items-stretch sm:gap-2'>
                    <div className='min-w-0 flex-1 rounded-lg bg-muted/60 p-2 sm:p-3'>
                      <p className='text-xs text-muted-foreground'>
                        CI 组件 + 环境
                      </p>
                      <p className='mt-1 font-mono text-sm break-all'>
                        {form.app_name.trim() || '组件标识'}
                      </p>
                      <p className='font-mono text-xs break-all text-muted-foreground'>
                        {cluster?.env_code || 'TARGET_CLUSTER'}
                      </p>
                    </div>
                    <ArrowDown
                      aria-hidden='true'
                      className='mx-auto size-3 shrink-0 text-muted-foreground sm:hidden'
                    />
                    <ArrowRight
                      aria-hidden='true'
                      className='hidden size-4 shrink-0 self-center text-muted-foreground sm:block'
                    />
                    <div className='min-w-0 flex-1 rounded-lg bg-muted/60 p-2 sm:p-3'>
                      <p className='text-xs text-muted-foreground'>
                        精确镜像仓库
                      </p>
                      <p className='mt-1 font-mono text-sm break-all'>
                        {form.image_repository.trim()
                          ? `${form.image_repository.trim()}:<tag>`
                          : '填写 CI 推送的镜像仓库'}
                      </p>
                      <p className='text-xs text-muted-foreground'>
                        每次构建提供 tag 与 digest
                      </p>
                    </div>
                    <ArrowDown
                      aria-hidden='true'
                      className='mx-auto size-3 shrink-0 text-muted-foreground sm:hidden'
                    />
                    <ArrowRight
                      aria-hidden='true'
                      className='hidden size-4 shrink-0 self-center text-muted-foreground sm:block'
                    />
                    <div className='min-w-0 flex-1 rounded-lg bg-primary/5 p-2 ring-1 ring-primary/15 sm:p-3'>
                      <p className='text-xs text-muted-foreground'>
                        更新目标容器
                      </p>
                      <p className='mt-1 text-sm font-medium break-all'>
                        {cluster?.name || '选择集群'}
                      </p>
                      <p className='font-mono text-xs break-all text-muted-foreground'>
                        {form.namespace || 'namespace'} /{' '}
                        {form.deployment_name || 'Deployment'} /{' '}
                        {form.container_name || '容器'}
                      </p>
                    </div>
                  </div>
                </section>
                {production && (
                  <p className='text-sm text-muted-foreground'>
                    这是生产目标。同一组件需要先完成非生产部署与验证，再经另一名操作员批准发布。
                  </p>
                )}
              </div>
            )}
            {step === 2 && preview && (
              <div className='space-y-5'>
                <section
                  aria-label='组件对应关系'
                  className='space-y-3 rounded-lg bg-muted p-4 text-sm'
                >
                  <h3 className='flex items-center gap-2 font-semibold'>
                    <Layers className='size-4' />
                    这次设置建立的对应关系
                  </h3>
                  <div className='grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto_minmax(0,1fr)] sm:items-stretch'>
                    <div className='min-w-0 rounded-md border bg-background p-2 sm:p-3'>
                      <p className='text-xs text-muted-foreground'>代码来源</p>
                      <p className='mt-0.5 font-medium break-all'>
                        {form.source_kind === 'repository'
                          ? form.source_repo_url || '仓库地址未填写'
                          : form.source_kind === 'third_party'
                            ? '已确认：第三方镜像'
                            : '来源待登记'}
                      </p>
                      <p className='mt-0.5 text-xs break-all text-muted-foreground'>
                        {form.source_kind === 'repository'
                          ? `构建目录 · ${form.source_path || '整个仓库'}`
                          : '镜像构建由现有 CI 完成'}
                      </p>
                    </div>
                    <ArrowDown
                      aria-hidden='true'
                      className='mx-auto size-4 text-muted-foreground sm:hidden'
                    />
                    <ArrowRight
                      aria-hidden='true'
                      className='hidden size-4 shrink-0 self-center text-muted-foreground sm:block'
                    />
                    <div className='min-w-0 rounded-md border bg-background p-2 sm:p-3'>
                      <p className='text-xs text-muted-foreground'>
                        APP_NAME → 组件
                      </p>
                      <p className='mt-0.5 font-mono font-semibold break-all'>
                        {form.app_name || '组件标识未填写'}
                      </p>
                      <p className='mt-1 text-xs text-muted-foreground'>
                        IMAGE_FULL 仓库
                      </p>
                      <p className='font-mono text-xs leading-4 break-all'>
                        {form.image_repository || '镜像仓库未填写'}
                      </p>
                    </div>
                    <ArrowDown
                      aria-hidden='true'
                      className='mx-auto size-4 text-muted-foreground sm:hidden'
                    />
                    <ArrowRight
                      aria-hidden='true'
                      className='hidden size-4 shrink-0 self-center text-muted-foreground sm:block'
                    />
                    <div className='min-w-0 rounded-md border bg-background p-2 sm:p-3'>
                      <p className='text-xs text-muted-foreground'>
                        TARGET_CLUSTER → 部署位置
                      </p>
                      <p className='mt-0.5 font-medium break-all'>
                        {cluster?.env_code || '环境代码未配置'}
                        {cluster ? ` · ${cluster.name}` : ''}
                      </p>
                      <p className='mt-0.5 text-xs break-all text-muted-foreground'>
                        {form.namespace} / Deployment {form.deployment_name} /
                        容器 {form.container_name}
                      </p>
                    </div>
                  </div>
                  <div className='rounded-md border bg-background p-3'>
                    <p className='text-xs text-muted-foreground'>
                      部署配置来源
                    </p>
                    {form.deployment_config_repo_url?.trim() ? (
                      <>
                        <p className='mt-0.5 font-mono text-xs break-all'>
                          {form.deployment_config_repo_url.trim()}
                        </p>
                        <p className='mt-0.5 text-xs text-muted-foreground'>
                          目录 · {form.deployment_config_path?.trim() || '.'}
                        </p>
                      </>
                    ) : (
                      <p className='mt-0.5 text-xs text-muted-foreground'>
                        未固定配置仓库；若每次镜像构建都会使用 Helm/Git
                        配置，建议在此登记，以便对齐同次发布的 commit。
                      </p>
                    )}
                  </div>
                  <p className='text-xs break-all text-muted-foreground'>
                    当前检查到的镜像：<code>{preview.current_image}</code>
                  </p>
                  {preview.warnings.map((w) => (
                    <p key={w} className='text-amber-700 dark:text-amber-300'>
                      {w}
                    </p>
                  ))}
                </section>
                <section
                  aria-labelledby='ci-event-simulation-title'
                  className='space-y-3'
                >
                  <div>
                    <h3
                      id='ci-event-simulation-title'
                      className='text-sm font-semibold'
                    >
                      模拟云效通知值
                    </h3>
                    <p className='mt-1 text-xs text-muted-foreground'>
                      核对组件、目标、镜像，以及代码仓库和构建目录。修改这里只做预览，不会发送通知或发布。
                    </p>
                  </div>
                  <div className='grid gap-4 sm:grid-cols-2'>
                    <Field
                      id='sample-app-name'
                      label='CI 的 APP_NAME'
                      hint='必须与组件标识完全一致。'
                    >
                      <Input
                        id='sample-app-name'
                        value={sampleAppName}
                        onChange={(e) =>
                          updateSampleValue(setSampleAppName, e.target.value)
                        }
                      />
                    </Field>
                    <Field
                      id='sample-target-cluster'
                      label='CI 的 TARGET_CLUSTER'
                      hint='填写此集群的环境代码。'
                    >
                      <Input
                        id='sample-target-cluster'
                        value={sampleTargetCluster}
                        onChange={(e) =>
                          updateSampleValue(
                            setSampleTargetCluster,
                            e.target.value
                          )
                        }
                      />
                    </Field>
                    {form.source_kind === 'repository' && (
                      <>
                        <Field
                          id='sample-source-repository'
                          label='云效 sources[].repo'
                          hint='通知报告的仓库必须与组件登记仓库一致。'
                        >
                          <Textarea
                            id='sample-source-repository'
                            value={sampleSourceRepository}
                            onChange={(e) =>
                              updateSampleValue(
                                setSampleSourceRepository,
                                e.target.value
                              )
                            }
                            rows={2}
                            spellCheck={false}
                            className='min-h-[4.25rem] resize-y font-mono text-xs break-all'
                          />
                        </Field>
                        <Field
                          id='sample-source-path'
                          label='CI 的 SOURCE_PATH'
                          hint={
                            form.source_path.trim() && form.source_path !== '.'
                              ? '子目录组件必填，必须与登记目录完全一致。'
                              : '仓库根目录填 .；非根目录组件必填。'
                          }
                        >
                          <Input
                            id='sample-source-path'
                            value={sampleSourcePath}
                            onChange={(e) =>
                              updateSampleValue(
                                setSampleSourcePath,
                                e.target.value
                              )
                            }
                            className='font-mono text-xs'
                          />
                        </Field>
                      </>
                    )}
                    <div className='sm:col-span-2'>
                      <Field
                        id='sample-image'
                        label='CI 的 IMAGE_FULL'
                        hint='填写构建推送后的完整镜像引用，带 tag 或 digest。'
                      >
                        <Textarea
                          id='sample-image'
                          value={sampleImage}
                          rows={2}
                          spellCheck={false}
                          className='min-h-[4.25rem] resize-y font-mono text-sm break-all'
                          onChange={(e) =>
                            updateSampleValue(setSampleImage, e.target.value)
                          }
                        />
                      </Field>
                    </div>
                  </div>
                </section>
                <p
                  role='status'
                  className={`rounded-lg border p-3 text-sm break-words ${sampleMatch.matches ? 'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200' : 'border-destructive/25 bg-destructive/5 text-destructive'}`}
                >
                  {sampleMatch.matches
                    ? `命中绑定：${sampleMatch.message} → ${cluster?.name} / ${form.namespace} / ${form.deployment_name} / ${form.container_name}`
                    : `平台将拒绝该更新：${sampleMatch.message}`}
                </p>
                <details className='rounded-lg border p-4 text-sm'>
                  <summary className='cursor-pointer font-medium'>
                    CI 如何通知平台
                  </summary>
                  {autoTest ? (
                    <div className='mt-3 space-y-3'>
                      <p>
                        构建并推送镜像后，向{' '}
                        <code>/api/v1/webhooks/yunxiao</code> 发送以下
                        JSON；替换所有占位值。每次更新只替换此组件的所选容器，其他容器保持原样。
                      </p>
                      <pre className='overflow-x-auto rounded bg-muted p-3 text-xs'>
                        {ciEventExample(payload(), cluster?.env_code ?? '')}
                      </pre>
                      <Button
                        variant='outline'
                        size='sm'
                        onClick={async () => {
                          try {
                            await navigator.clipboard.writeText(
                              ciEventExample(payload(), cluster?.env_code ?? '')
                            )
                            toast.success('已复制 CI 请求示例')
                          } catch {
                            toast.error('复制失败，请手动选择示例内容')
                          }
                        }}
                      >
                        <Copy />
                        复制请求示例
                      </Button>
                      <p className='text-xs text-muted-foreground'>
                        管理员通过 YUNXIAO_WEBHOOK_TOKEN 配置接入密钥，通过
                        X-Webhook-Token 请求头发送；在 CI 的 Secret
                        中保存。云效标准事件使用
                        APP_NAME、IMAGE_FULL、IMAGE_DIGEST、TARGET_CLUSTER
                        全局参数，代码仓库由 sources[].repo
                        报告。登记了代码子目录时，还需设置同值的 SOURCE_PATH
                        全局参数。
                      </p>
                    </div>
                  ) : (
                    <p className='mt-3 text-muted-foreground'>
                      {production
                        ? '生产绑定通过发布中心选择目标并审批；请在非生产自动部署环境的绑定中复制 CI 接入示例。同一组件的 APP_NAME 必须一致。'
                        : '此集群当前为手动策略。镜像事件的首次测试部署需要非生产自动策略，请先在集群设置中调整，或为组件添加一个符合条件的环境。'}
                    </p>
                  )}
                </details>
              </div>
            )}
          </fieldset>
          {error && (
            <p role='alert' className='mt-4 text-sm text-destructive'>
              {error}
            </p>
          )}
        </div>
        <DialogFooter className='grid grid-cols-2 gap-x-2 gap-y-3 border-t px-5 py-4 sm:flex sm:items-center sm:gap-3 sm:px-6'>
          {step === 2 && (
            <Label className='order-0 col-span-2 flex min-w-0 cursor-pointer items-start gap-3 rounded-lg border bg-muted/40 p-3 text-sm leading-5 sm:me-auto sm:max-w-[55%] sm:border-0 sm:bg-transparent sm:p-0'>
              <input
                type='checkbox'
                checked={confirmed}
                disabled={
                  check.isPending || save.isPending || !sampleMatch.matches
                }
                onChange={(e) => setConfirmed(e.target.checked)}
                className='mt-0.5 size-4 shrink-0 accent-primary'
              />
              <span>
                我已核对组件、镜像仓库和部署位置。保存只登记绑定，不会发布镜像。
              </span>
            </Label>
          )}
          <Button
            variant='outline'
            className={`min-h-11 w-full sm:order-none sm:min-h-9 sm:w-auto ${step === 0 ? 'order-2 col-span-2' : 'order-2'}`}
            onClick={onClose}
            disabled={save.isPending}
          >
            取消
          </Button>
          {step > 0 && (
            <Button
              className='order-3 min-h-11 w-full sm:order-none sm:min-h-9 sm:w-auto'
              variant='outline'
              disabled={check.isPending || save.isPending}
              onClick={() => {
                setStep(step - 1)
                setError('')
                setPreview(null)
                setConfirmed(false)
              }}
            >
              上一步
            </Button>
          )}
          {step < 2 ? (
            <Button
              className='order-1 col-span-2 min-h-11 w-full sm:order-none sm:min-h-9 sm:w-auto'
              onClick={next}
              disabled={check.isPending || targetResourcesLoading}
            >
              {targetResourcesLoading
                ? '正在读取集群资源…'
                : check.isPending
                  ? '检查目标中…'
                  : step === 0
                    ? '下一步：选择部署目标'
                    : '检查绑定并继续'}
            </Button>
          ) : (
            <Button
              className='order-1 col-span-2 min-h-11 w-full sm:order-none sm:min-h-9 sm:w-auto'
              onClick={() => save.mutate()}
              disabled={
                !confirmed || !sampleMatch.matches || !preview || save.isPending
              }
            >
              {save.isPending ? '保存中…' : '保存组件绑定'}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
