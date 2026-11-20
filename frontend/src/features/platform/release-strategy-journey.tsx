import {
  ArrowRight,
  GitBranch,
  Info,
  Layers3,
  Route,
  ShieldCheck,
  type LucideIcon,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'

export type ReleaseStrategy = 'rolling' | 'blue_green' | 'gateway_canary'

const strategyContent: Record<
  ReleaseStrategy,
  {
    title: string
    summary: string
    icon: LucideIcon
    steps: string[]
  }
> = {
  rolling: {
    title: '滚动更新',
    summary: '更新现有 Deployment，等待新 Pod 就绪后逐步替换旧 Pod。',
    icon: Layers3,
    steps: ['固定镜像版本', '更新现有 Deployment', '等待 rollout 就绪'],
  },
  blue_green: {
    title: '蓝绿发布',
    summary:
      '先准备隔离的候选 Deployment，通过健康检查后切换 Service selector。',
    icon: GitBranch,
    steps: ['创建隔离候选版', '检查候选版就绪', '切换 Service selector'],
  },
  gateway_canary: {
    title: '网关金丝雀',
    summary: '由既有网关调整稳定版与候选版的流量权重，再按指标逐步推进。',
    icon: Route,
    steps: ['准备独立候选后端', '网关按阶段调整权重', '观察指标并推进或恢复'],
  },
}

function Node({
  eyebrow,
  title,
  detail,
  tone = 'neutral',
}: {
  eyebrow: string
  title: string
  detail: string
  tone?: 'neutral' | 'stable' | 'candidate' | 'gateway'
}) {
  const toneClass = {
    neutral: 'border-border bg-background',
    stable:
      'border-sky-200 bg-sky-50/70 dark:border-sky-900 dark:bg-sky-950/30',
    candidate:
      'border-violet-200 bg-violet-50/70 dark:border-violet-900 dark:bg-violet-950/30',
    gateway:
      'border-amber-200 bg-amber-50/70 dark:border-amber-900 dark:bg-amber-950/30',
  }[tone]
  return (
    <div className={`min-w-0 rounded-lg border px-3 py-2.5 ${toneClass}`}>
      <div className='text-xs font-medium text-muted-foreground'>{eyebrow}</div>
      <div className='mt-0.5 truncate text-sm font-semibold'>{title}</div>
      <div className='mt-0.5 text-xs leading-5 text-muted-foreground'>
        {detail}
      </div>
    </div>
  )
}

function StrategyTopology({ strategy }: { strategy: ReleaseStrategy }) {
  if (strategy === 'blue_green') {
    return (
      <div className='space-y-2'>
        <div className='grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] sm:items-center'>
          <Node
            eyebrow='入口'
            title='现有 Service'
            detail='selector 指向稳定版'
            tone='neutral'
          />
          <ArrowRight
            className='mx-auto size-4 rotate-90 text-muted-foreground sm:rotate-0'
            aria-hidden='true'
          />
          <div className='grid gap-2 sm:grid-cols-2'>
            <Node
              eyebrow='当前承流'
              title='稳定版 Deployment'
              detail='保留供恢复'
              tone='stable'
            />
            <Node
              eyebrow='隔离验证'
              title='候选 Deployment'
              detail='就绪后等待切换'
              tone='candidate'
            />
          </div>
        </div>
        <div className='flex items-start gap-2 rounded-md bg-muted/50 px-3 py-2 text-xs leading-5 text-muted-foreground'>
          <ShieldCheck
            className='mt-0.5 size-3.5 shrink-0 text-emerald-600 dark:text-emerald-400'
            aria-hidden='true'
          />
          <span>
            通过检查后，平台条件更新 Service selector
            指向候选版；旧版保留作为恢复目标。
          </span>
        </div>
      </div>
    )
  }

  if (strategy === 'gateway_canary') {
    return (
      <div className='grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1.5fr)] sm:items-center'>
        <Node
          eyebrow='流量入口'
          title='既有网关路由'
          detail='读取并调整路由权重'
          tone='gateway'
        />
        <ArrowRight
          className='mx-auto size-4 rotate-90 text-muted-foreground sm:rotate-0'
          aria-hidden='true'
        />
        <div className='grid gap-2 sm:grid-cols-2'>
          <Node
            eyebrow='稳定后端'
            title='稳定版 Service'
            detail='保持恢复容量'
            tone='stable'
          />
          <Node
            eyebrow='候选后端'
            title='候选版 Service'
            detail='按阶段接收流量'
            tone='candidate'
          />
        </div>
      </div>
    )
  }

  return (
    <div className='grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] sm:items-center'>
      <Node
        eyebrow='现有目标'
        title='Deployment'
        detail='保留当前工作负载配置'
        tone='stable'
      />
      <ArrowRight
        className='mx-auto size-4 rotate-90 text-muted-foreground sm:rotate-0'
        aria-hidden='true'
      />
      <Node
        eyebrow='更新方式'
        title='逐步替换 Pod'
        detail='新 Pod 就绪后继续推进'
        tone='candidate'
      />
    </div>
  )
}

export function ReleaseStrategyJourney({
  strategy,
  compact = false,
}: {
  strategy: ReleaseStrategy
  compact?: boolean
}) {
  const content = strategyContent[strategy]
  const Icon = content.icon
  return (
    <Card
      role='group'
      aria-label={`${content.title}路径示意`}
      className='overflow-hidden shadow-none'
    >
      <CardContent className={compact ? 'space-y-3 p-3' : 'space-y-4 p-4'}>
        <div className='flex items-start gap-3'>
          <span className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary'>
            <Icon className='size-4' aria-hidden='true' />
          </span>
          <div className='min-w-0 flex-1'>
            <div className='flex flex-wrap items-center gap-2'>
              <h3 className='text-sm font-semibold'>执行方式</h3>
              <Badge variant='outline' className='text-xs'>
                流程示意
              </Badge>
            </div>
            <p className='mt-1 text-xs leading-5 text-muted-foreground'>
              {content.summary}
            </p>
          </div>
        </div>

        <StrategyTopology strategy={strategy} />

        <ol className='grid gap-2 sm:grid-cols-3'>
          {content.steps.map((step, index) => (
            <li
              key={step}
              className='flex min-w-0 items-start gap-2 rounded-md bg-muted/40 px-2.5 py-2'
            >
              <span className='flex size-5 shrink-0 items-center justify-center rounded-full bg-background text-xs font-semibold text-muted-foreground ring-1 ring-border'>
                {index + 1}
              </span>
              <span className='text-xs leading-5'>{step}</span>
            </li>
          ))}
        </ol>
        <p className='flex items-start gap-1.5 text-xs leading-4 text-muted-foreground'>
          <Info className='mt-0.5 size-3 shrink-0' aria-hidden='true' />
          <span>
            示意图描述发布模型，不代表本目标已完成接入或检查；执行前以能力预检和审批计划为准。
          </span>
        </p>
      </CardContent>
    </Card>
  )
}
