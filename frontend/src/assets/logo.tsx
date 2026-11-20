import { type SVGProps } from 'react'
import { cn } from '@/lib/utils'

// 舵轮 —— 呼应 Kubernetes 的航海意象，与侧栏品牌图标（ShipWheel）一致。
export function Logo({ className, ...props }: SVGProps<SVGSVGElement>) {
  return (
    <svg
      viewBox='0 0 24 24'
      xmlns='http://www.w3.org/2000/svg'
      height='24'
      width='24'
      fill='none'
      stroke='currentColor'
      strokeWidth='2'
      strokeLinecap='round'
      strokeLinejoin='round'
      className={cn('size-6', className)}
      {...props}
    >
      <title>发布管理平台</title>
      <circle cx='12' cy='12' r='8' />
      <path d='M12 2v7.5' />
      <path d='m19 5-5.23 5.23' />
      <path d='M22 12h-7.5' />
      <path d='m19 19-5.23-5.23' />
      <path d='M12 14.5V22' />
      <path d='M10.23 13.77 5 19' />
      <path d='M9.5 12H2' />
      <path d='M10.23 10.23 5 5' />
      <circle cx='12' cy='12' r='2.5' />
    </svg>
  )
}
