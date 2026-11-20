import { Outlet } from '@tanstack/react-router'
import { Separator } from '@/components/ui/separator'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'

export function Settings() {
  return (
    <>
      <Header>
        <ThemeSwitch />
        <ProfileDropdown />
      </Header>

      <Main>
        <div className='space-y-0.5'>
          <h1 className='text-2xl font-bold tracking-tight'>平台设置</h1>
          <p className='text-muted-foreground'>
            管理平台接入的集群与全局配置。
          </p>
        </div>
        <Separator className='my-4 lg:my-6' />
        <div className='flex flex-1 flex-col overflow-hidden'>
          <Outlet />
        </div>
      </Main>
    </>
  )
}
