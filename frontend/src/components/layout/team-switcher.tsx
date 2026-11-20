import * as React from 'react'
import { Check, ChevronsUpDown } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'

type TeamSwitcherProps = {
  teams: {
    name: string
    logo: React.ElementType
    plan: string
  }[]
}

export function TeamSwitcher({ teams }: TeamSwitcherProps) {
  const { isMobile } = useSidebar()
  const [activeTeam, setActiveTeam] = React.useState(teams[0])

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size='lg'
              className='rounded-xl bg-transparent text-white hover:bg-white/10 hover:text-white data-[state=open]:bg-white/10 data-[state=open]:text-white'
            >
              <div className='flex aspect-square size-9 items-center justify-center rounded-xl bg-gradient-to-br from-white/25 to-white/5 text-white shadow-[inset_0_1px_0_rgba(255,255,255,0.3)] ring-1 ring-white/25'>
                <activeTeam.logo className='size-5' />
              </div>
              <div className='grid flex-1 text-start text-sm leading-tight'>
                <span className='truncate font-semibold tracking-tight'>
                  {activeTeam.name}
                </span>
                <span className='truncate text-xs text-white/60'>
                  {activeTeam.plan}
                </span>
              </div>
              <ChevronsUpDown className='ms-auto size-4 text-white/50' />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className='w-(--radix-dropdown-menu-trigger-width) min-w-64 rounded-xl p-1.5'
            align='start'
            side={isMobile ? 'bottom' : 'right'}
            sideOffset={8}
          >
            <DropdownMenuLabel className='px-2 py-1.5 text-xs font-normal text-muted-foreground'>
              工作空间
            </DropdownMenuLabel>
            {teams.map((team) => {
              const active = team === activeTeam
              return (
                <DropdownMenuItem
                  key={team.name}
                  onClick={() => setActiveTeam(team)}
                  className={`gap-3 rounded-lg px-2 py-2 ${
                    active ? 'bg-accent' : ''
                  }`}
                >
                  <div className='flex size-8 shrink-0 items-center justify-center rounded-lg bg-[#234883]/10 text-[#234883] ring-1 ring-[#234883]/15'>
                    <team.logo className='size-4' />
                  </div>
                  <div className='grid min-w-0 flex-1 text-start leading-tight'>
                    <span className='truncate text-sm font-medium'>
                      {team.name}
                    </span>
                    <span className='truncate text-xs text-muted-foreground'>
                      {team.plan}
                    </span>
                  </div>
                  {active && (
                    <Check className='size-4 shrink-0 text-[#234883]' />
                  )}
                </DropdownMenuItem>
              )
            })}
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
