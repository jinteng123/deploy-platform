import {
  LayoutDashboard,
  Boxes,
  FileClock,
  KeyRound,
  Network,
  Server,
  ShieldCheck,
  Settings,
  ShipWheel,
  Workflow,
} from 'lucide-react'
import { type SidebarData } from '../types'

export const sidebarData: SidebarData = {
  teams: [
    {
      name: '发布管理平台',
      logo: ShipWheel,
      plan: 'Kubernetes 集群管理',
    },
  ],
  navGroups: [
    {
      title: '发布平台',
      items: [
        {
          title: '平台概览',
          url: '/',
          icon: LayoutDashboard,
        },
        {
          title: '发布中心',
          url: '/tasks',
          icon: FileClock,
        },
        {
          title: '应用与组件',
          url: '/apps',
          icon: Boxes,
        },
        {
          title: '环境与目标',
          url: '/environments',
          icon: Workflow,
        },
        {
          title: 'K8s 控制台',
          url: '/k8s',
          icon: Server,
        },
      ],
    },
    {
      title: '系统',
      items: [
        {
          title: '平台设置',
          icon: Settings,
          items: [
            {
              title: '集群管理',
              url: '/settings/clusters',
              icon: Network,
            },
            {
              title: 'API 密钥',
              url: '/settings/api-keys',
              icon: KeyRound,
            },
            {
              title: '审批范围',
              url: '/settings/approval-scopes',
              icon: ShieldCheck,
            },
          ],
        },
      ],
    },
  ],
}
