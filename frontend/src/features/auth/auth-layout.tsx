import { Rocket, ShieldCheck, Webhook } from 'lucide-react'
import { Logo } from '@/assets/logo'

const FEATURES = [
  {
    icon: Webhook,
    title: '构建接入',
    desc: '构建通知按显式绑定更新指定的非生产目标',
  },
  {
    icon: Rocket,
    title: '生产晋级',
    desc: '固定同一镜像，核验证据并独立审批后执行',
  },
  {
    icon: ShieldCheck,
    title: '权限与审计',
    desc: '遵循 Kubernetes RBAC，关键操作留有审计记录',
  },
]

type AuthLayoutProps = {
  children: React.ReactNode
}

export function AuthLayout({ children }: AuthLayoutProps) {
  return (
    <div className='grid min-h-svh lg:grid-cols-[5fr_7fr]'>
      {/* Left brand panel */}
      <div className='relative hidden overflow-hidden bg-[linear-gradient(180deg,#214e91_0%,#173f7b_52%,#15386e_100%)] p-12 text-white lg:flex lg:flex-col lg:justify-between'>
        <div className='absolute -top-24 -left-24 size-96 rounded-full bg-white/10 blur-3xl' />
        <Logo
          aria-hidden
          className='pointer-events-none absolute -right-24 -bottom-24 size-[28rem] text-white/[0.06]'
        />
        <div className='relative z-10 flex items-center gap-3'>
          <span className='flex size-10 items-center justify-center rounded-xl bg-white text-[#234883] shadow-sm'>
            <Logo className='size-6' />
          </span>
          <div>
            <p className='text-base font-semibold'>发布管理平台</p>
            <p className='text-xs text-white/70'>Kubernetes 集群管理</p>
          </div>
        </div>
        <div className='relative z-10'>
          <h2 className='text-3xl leading-tight font-semibold tracking-tight'>
            从镜像构建到生产发布，
            <br />
            每一步有据可查
          </h2>
          <p className='mt-4 max-w-md text-sm leading-6 text-white/75'>
            云效构建通过接入配置上报不可变镜像，平台按组件与环境绑定定位非生产目标；生产发布经证据核验和独立审批。
          </p>
          <ul className='mt-10 space-y-5'>
            {FEATURES.map((f) => (
              <li key={f.title} className='flex items-center gap-3.5'>
                <span className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-white/10 ring-1 ring-white/15'>
                  <f.icon className='size-4' />
                </span>
                <div>
                  <p className='text-sm font-medium'>{f.title}</p>
                  <p className='text-xs text-white/65'>{f.desc}</p>
                </div>
              </li>
            ))}
          </ul>
        </div>
        <p className='relative z-10 text-xs text-white/50'>
          仅限授权人员访问 · 关键操作留有审计记录
        </p>
      </div>

      {/* Right panel */}
      <div className='flex items-center justify-center bg-[#f4f7fb] px-6 py-12 sm:px-12 dark:bg-background'>
        <div className='w-full max-w-sm duration-300 motion-safe:animate-in motion-safe:fade-in-0 motion-safe:slide-in-from-bottom-2'>
          <div className='mb-10 flex items-center gap-3 lg:hidden'>
            <span className='flex size-10 items-center justify-center rounded-xl bg-[#234883] text-white'>
              <Logo className='size-6' />
            </span>
            <div>
              <p className='text-base font-semibold text-[#1c355e]'>
                发布管理平台
              </p>
              <p className='text-xs text-muted-foreground'>
                Kubernetes 集群管理
              </p>
            </div>
          </div>
          {children}
        </div>
      </div>
    </div>
  )
}
