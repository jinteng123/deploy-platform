import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  Boxes,
  ChevronRight,
  Database,
  FolderTree,
  HardDrive,
  Layers,
  LayoutDashboard,
  ListTree,
  Network,
  Server,
  Settings2,
} from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from '@/components/ui/sidebar'
import type { NavLink } from './types'

type Props = {
  item: NavLink
  href: string
}

type K8sMenuItem = {
  title: string
  url: string
  icon: React.ElementType
  active: boolean
}

const parseLocation = (href: string) => {
  const url = new URL(href, window.location.origin)
  const params = url.searchParams
  const cluster = params.get('cluster')
  const namespace = params.get('ns')
  const resource = params.get('res') ?? params.get('r')
  return {
    pathname: url.pathname,
    cluster,
    namespace,
    resource,
    section: params.get('section'),
  }
}

const matchesResource = (
  current: ReturnType<typeof parseLocation>,
  resource: string,
  section: string
) =>
  current.resource === resource ||
  (current.resource == null && current.section === section)

function SubGroupLabel({ children }: { children: React.ReactNode }) {
  return (
    <li className='px-3 pt-3 pb-1 text-[11px] font-medium tracking-wide text-white/40 first:pt-1'>
      {children}
    </li>
  )
}

export function K8sConsoleNav({ item, href }: Props) {
  const { isMobile, setOpenMobile } = useSidebar()
  const current = useMemo(() => parseLocation(href), [href])
  const isK8sRoute =
    current.pathname === '/k8s' || current.pathname.startsWith('/k8s/')
  const hasCluster = Boolean(current.cluster)
  const hasNamespace = hasCluster && Boolean(current.namespace)

  const clusterUrl = current.cluster
    ? `cluster=${encodeURIComponent(current.cluster)}`
    : ''
  const namespaceUrl = current.namespace
    ? `&ns=${encodeURIComponent(current.namespace)}`
    : ''
  const contextualUrl = (section: string, resource: string) =>
    `/k8s?${clusterUrl}${namespaceUrl}&section=${section}&res=${resource}`

  // Cluster-scoped views stay available even when the URL carries no
  // namespace (e.g. a nodes/storageclasses detail page) — the menu must
  // not collapse to just "集群列表" there.
  const clusterItems: K8sMenuItem[] = hasCluster
    ? [
        {
          title: '集群概览',
          url: contextualUrl('overview', 'dashboard'),
          icon: LayoutDashboard,
          active:
            current.resource === 'dashboard' ||
            (current.resource == null && current.section === 'overview'),
        },
        {
          title: '节点',
          url: contextualUrl('overview', 'nodes'),
          icon: Server,
          active: current.resource === 'nodes',
        },
        {
          title: '名称空间',
          url: contextualUrl('overview', 'namespaces'),
          icon: FolderTree,
          active: current.resource === 'namespaces',
        },
        {
          title: '持久卷',
          url: contextualUrl('storage', 'persistentvolumes'),
          icon: Database,
          active: current.resource === 'persistentvolumes',
        },
        {
          title: '存储类',
          url: contextualUrl('storage', 'storageclasses'),
          icon: Layers,
          active: current.resource === 'storageclasses',
        },
      ]
    : []

  const namespaceItems: K8sMenuItem[] = hasNamespace
    ? [
        {
          title: '名称空间首页',
          url: contextualUrl('overview', 'namespace-home'),
          icon: LayoutDashboard,
          active: current.resource === 'namespace-home',
        },
        {
          title: '工作负载',
          url: contextualUrl('workloads', 'deployments'),
          icon: Boxes,
          active:
            current.section === 'workloads' ||
            [
              'deployments',
              'statefulsets',
              'daemonsets',
              'jobs',
              'cronjobs',
            ].includes(current.resource ?? ''),
        },
        {
          title: '容器组',
          url: contextualUrl('pods', 'pods'),
          icon: ListTree,
          active: matchesResource(current, 'pods', 'pods'),
        },
        {
          title: '服务',
          url: contextualUrl('network', 'services'),
          icon: Network,
          active:
            current.section === 'network' ||
            ['services', 'ingresses'].includes(current.resource ?? ''),
        },
        {
          title: '配置',
          url: contextualUrl('config', 'configmaps'),
          icon: Settings2,
          active:
            current.section === 'config' ||
            ['configmaps', 'secrets'].includes(current.resource ?? ''),
        },
        {
          title: '存储声明',
          url: contextualUrl('storage', 'persistentvolumeclaims'),
          icon: HardDrive,
          active:
            current.resource === 'persistentvolumeclaims' ||
            (current.section === 'storage' &&
              !['persistentvolumes', 'storageclasses'].includes(
                current.resource ?? ''
              )),
        },
        {
          title: '事件',
          url: contextualUrl('overview', 'events'),
          icon: Activity,
          active: current.resource === 'events',
        },
      ]
    : []

  const topItems: K8sMenuItem[] = [
    {
      title: '集群列表',
      url: '/k8s',
      icon: Server,
      active: !hasCluster && current.pathname === '/k8s',
    },
  ]
  if (hasCluster && !hasNamespace) {
    topItems.push({
      title: '选择名称空间',
      url: `/k8s?${clusterUrl}`,
      icon: FolderTree,
      active: current.pathname === '/k8s' && current.namespace == null,
    })
  }

  const closeMobile = () => {
    if (isMobile) setOpenMobile(false)
  }

  const renderItem = (menuItem: K8sMenuItem) => (
    <SidebarMenuSubItem key={menuItem.title}>
      <SidebarMenuSubButton
        asChild
        isActive={menuItem.active}
        className='text-white/75 data-[active=true]:text-white'
      >
        <Link to={menuItem.url} onClick={closeMobile}>
          <menuItem.icon />
          <span>{menuItem.title}</span>
        </Link>
      </SidebarMenuSubButton>
    </SidebarMenuSubItem>
  )

  return (
    <Collapsible
      asChild
      key={isK8sRoute ? 'active' : 'inactive'}
      defaultOpen={isK8sRoute}
      className='group/collapsible'
    >
      <SidebarMenuItem>
        <CollapsibleTrigger asChild>
          <SidebarMenuButton
            tooltip={item.title}
            isActive={isK8sRoute}
            className='h-10 rounded-xl px-3 text-[13px] text-white/80'
          >
            {item.icon && <item.icon />}
            <span>{item.title}</span>
            <ChevronRight className='ms-auto transition-transform duration-200 group-data-[state=open]/collapsible:rotate-90 rtl:rotate-180' />
          </SidebarMenuButton>
        </CollapsibleTrigger>
        <CollapsibleContent className='CollapsibleContent'>
          <SidebarMenuSub>
            {topItems.map(renderItem)}
            {clusterItems.length > 0 && (
              <>
                <SubGroupLabel>集群</SubGroupLabel>
                {clusterItems.map(renderItem)}
              </>
            )}
            {namespaceItems.length > 0 && (
              <>
                <SubGroupLabel>命名空间</SubGroupLabel>
                {namespaceItems.map(renderItem)}
              </>
            )}
          </SidebarMenuSub>
        </CollapsibleContent>
      </SidebarMenuItem>
    </Collapsible>
  )
}
