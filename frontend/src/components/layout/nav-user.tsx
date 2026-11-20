import { Link } from '@tanstack/react-router'
import { ChevronsUpDown, LogOut, Settings } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import useDialogState from '@/hooks/use-dialog-state'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { SignOutDialog } from '@/components/sign-out-dialog'

const ROLE_LABELS = {
  admin: '管理员',
  operator: '操作者',
  viewer: '查看者',
} as const

// 中文名取首字（单字即可辨识），ASCII 名取两字母缩写
const initials = (name: string) =>
  /^[\x20-\x7e]+$/.test(name) ? name.slice(0, 2).toUpperCase() : name.slice(0, 1)

export function NavUser() {
  const { isMobile } = useSidebar()
  const [open, setOpen] = useDialogState()
  const { auth } = useAuthStore()
  const name = auth.user?.display_name || auth.user?.username || '未登录'
  const role = auth.user ? (ROLE_LABELS[auth.user.role] ?? auth.user.role) : ''

  return (
    <>
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarMenuButton
                size='lg'
                className='rounded-xl border border-white/10 bg-white/10 px-3 py-2 text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.08)] transition hover:bg-white/15 hover:text-white data-[state=open]:bg-white/15 data-[state=open]:text-white'
              >
                <Avatar className='h-9 w-9 rounded-lg'>
                  <AvatarFallback className='rounded-lg bg-white text-[13px] font-semibold text-[#214b8d]'>
                    {initials(name)}
                  </AvatarFallback>
                </Avatar>
                <div className='grid flex-1 text-start text-sm leading-tight'>
                  <span className='truncate font-semibold'>{name}</span>
                  <span className='mt-0.5 truncate text-xs text-white/60'>
                    {auth.user?.username === name ? role : `${auth.user?.username} · ${role}`}
                  </span>
                </div>
                <ChevronsUpDown className='ms-auto size-4 text-white/50' />
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              className='w-(--radix-dropdown-menu-trigger-width) min-w-60 rounded-xl border-[#e0e8f4] p-1.5 shadow-[0_12px_32px_rgba(16,40,80,0.14)]'
              side={isMobile ? 'bottom' : 'right'}
              align='end'
              sideOffset={8}
            >
              <DropdownMenuLabel className='p-0 font-normal'>
                <div className='flex items-center gap-3 px-2 py-2.5'>
                  <Avatar className='h-10 w-10 rounded-lg'>
                    <AvatarFallback className='rounded-lg bg-[#234883] text-sm font-semibold text-white'>
                      {initials(name)}
                    </AvatarFallback>
                  </Avatar>
                  <div className='grid flex-1 text-start leading-tight'>
                    <span className='truncate text-sm font-semibold text-[#1c355e]'>
                      {name}
                    </span>
                    <span className='mt-0.5 truncate font-mono text-xs text-[#7e9ac0]'>
                      {auth.user?.username === name
                        ? role
                        : auth.user?.username}
                    </span>
                  </div>
                </div>
              </DropdownMenuLabel>
              <DropdownMenuSeparator className='bg-[#edf1f7]' />
              <DropdownMenuGroup>
                <DropdownMenuItem
                  asChild
                  className='rounded-lg px-2.5 py-2 text-[13px] text-[#31598e] focus:bg-[#f4f7fb] focus:text-[#234883]'
                >
                  <Link to='/settings/clusters'>
                    <Settings className='size-4' />
                    平台设置
                  </Link>
                </DropdownMenuItem>
              </DropdownMenuGroup>
              <DropdownMenuSeparator className='bg-[#edf1f7]' />
              <DropdownMenuItem
                className='rounded-lg px-2.5 py-2 text-[13px] text-[#5a6b85] focus:bg-[#fdf0f1] focus:text-[#a52f3d]'
                onClick={() => setOpen(true)}
              >
                <LogOut className='size-4' />
                退出登录
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      </SidebarMenu>

      <SignOutDialog open={!!open} onOpenChange={setOpen} />
    </>
  )
}
