import { createFileRoute, redirect } from '@tanstack/react-router'

// 集群管理已移至平台设置下；保留旧地址跳转以兼容既有入口与书签
export const Route = createFileRoute('/_authenticated/clusters/')({
  beforeLoad: () => {
    throw redirect({ to: '/settings/clusters' })
  },
})
