import { type K8sClusterOverview } from '@/api/client'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/components/empty-state'
import { age } from './age'
import { formatBytes, formatCPU, formatPercent } from './format'
import { StatusText } from './status-text'

// ClusterOverview renders the 集群概览 dashboard: version + capacity
// cards, node table with usage, Warning events and top pods. When
// metrics-server is absent the backend reports metrics.available=false
// and the usage areas degrade to 暂无数据 while everything else stays.
export function ClusterOverview({ data }: { data: K8sClusterOverview }) {
  const w = data.workloads
  const stats: [string, string][] = [
    ['集群版本', data.server_version || '-'],
    ['节点', `${data.nodes.ready} / ${data.nodes.total} 就绪`],
    ['命名空间', String(data.namespaces)],
    ['Pods', `${w.pods_running} / ${w.pods} 运行中`],
    ['Deployments', String(w.deployments)],
    ['StatefulSets', String(w.stateful_sets)],
    ['DaemonSets', String(w.daemon_sets)],
    ['Jobs / CronJobs', `${w.jobs} / ${w.cron_jobs}`],
  ]
  return (
    <div className='mt-4 space-y-4'>
      <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
        {stats.map(([label, value]) => (
          <Card
            key={label}
            className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'
          >
            <CardContent className='px-4 py-3'>
              <div className='text-xs text-muted-foreground'>{label}</div>
              <div
                className='mt-1 truncate text-lg font-semibold'
                title={value}
              >
                {value}
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
        <CardHeader>
          <CardTitle className='text-base text-[#1c355e]'>节点</CardTitle>
        </CardHeader>
        <CardContent className='overflow-x-auto'>
          <table className='w-full text-sm'>
            <thead>
              <tr className='border-b bg-[#f7f9fc]'>
                {['名称', '状态', '角色', '版本', '内部 IP', 'CPU', '内存'].map(
                  (h) => (
                    <th
                      key={h}
                      className='px-3 py-2 text-left text-xs font-semibold whitespace-nowrap text-[#587092]'
                    >
                      {h}
                    </th>
                  )
                )}
              </tr>
            </thead>
            <tbody>
              {data.nodes.items.map((n) => (
                <tr
                  key={n.name}
                  className='border-b border-[#edf1f7] last:border-0'
                >
                  <td className='px-3 py-3 font-medium whitespace-nowrap'>
                    {n.name}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>
                    <StatusText value={n.ready ? 'Ready' : 'NotReady'} />
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>
                    {n.roles.join(', ') || '-'}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>
                    {n.kubelet_version || '-'}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>
                    {n.internal_ip || '-'}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>
                    {n.usage
                      ? `${formatCPU(n.usage.cpu_millis)} / ${formatCPU(n.cpu_capacity_millis)} (${formatPercent(n.usage.cpu_millis, n.cpu_capacity_millis)})`
                      : `${formatCPU(n.cpu_capacity_millis)} 容量`}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>
                    {n.usage
                      ? `${formatBytes(n.usage.mem_bytes)} / ${formatBytes(n.mem_capacity_bytes)} (${formatPercent(n.usage.mem_bytes, n.mem_capacity_bytes)})`
                      : `${formatBytes(n.mem_capacity_bytes)} 容量`}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {data.nodes.items.length === 0 && <EmptyState />}
        </CardContent>
      </Card>

      <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
        <CardHeader>
          <CardTitle className='text-base text-[#1c355e]'>资源用量</CardTitle>
        </CardHeader>
        <CardContent className='overflow-x-auto'>
          {data.metrics.available ? (
            <table className='w-full text-sm'>
              <thead>
                <tr className='border-b bg-[#f7f9fc]'>
                  {['命名空间', 'Pod', 'CPU', '内存'].map((h) => (
                    <th
                      key={h}
                      className='px-3 py-2 text-left text-xs font-semibold whitespace-nowrap text-[#587092]'
                    >
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {data.top_pods.map((p) => (
                  <tr
                    key={`${p.namespace}/${p.name}`}
                    className='border-b border-[#edf1f7] last:border-0'
                  >
                    <td className='px-3 py-3 whitespace-nowrap'>
                      {p.namespace}
                    </td>
                    <td className='px-3 py-3 font-medium whitespace-nowrap'>
                      {p.name}
                    </td>
                    <td className='px-3 py-3 whitespace-nowrap'>
                      {formatCPU(p.usage.cpu_millis)}
                    </td>
                    <td className='px-3 py-3 whitespace-nowrap'>
                      {formatBytes(p.usage.mem_bytes)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <EmptyState />
          )}
          {data.metrics.available && data.top_pods.length === 0 && (
            <EmptyState />
          )}
        </CardContent>
      </Card>

      <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
        <CardHeader>
          <CardTitle className='text-base text-[#1c355e]'>
            Warning 事件
          </CardTitle>
        </CardHeader>
        <CardContent className='overflow-x-auto'>
          <table className='w-full text-sm'>
            <thead>
              <tr className='border-b bg-[#f7f9fc]'>
                {['最后时间', '命名空间', '对象', '原因', '消息', '次数'].map(
                  (h) => (
                    <th
                      key={h}
                      className='px-3 py-2 text-left text-xs font-semibold whitespace-nowrap text-[#587092]'
                    >
                      {h}
                    </th>
                  )
                )}
              </tr>
            </thead>
            <tbody>
              {data.warning_events.map((e, i) => (
                <tr
                  key={`${e.namespace}/${e.object}/${e.reason}/${i}`}
                  className='border-b border-[#edf1f7] last:border-0'
                >
                  <td className='px-3 py-3 whitespace-nowrap'>
                    {age(e.last_timestamp)}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>
                    {e.namespace || '-'}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>
                    {e.object || '-'}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>{e.reason}</td>
                  <td className='max-w-md truncate px-3 py-3' title={e.message}>
                    {e.message}
                  </td>
                  <td className='px-3 py-3 whitespace-nowrap'>{e.count}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {data.warning_events.length === 0 && <EmptyState />}
        </CardContent>
      </Card>
    </div>
  )
}
