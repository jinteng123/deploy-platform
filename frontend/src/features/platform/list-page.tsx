import { useQuery } from '@tanstack/react-query'
import { platformApi } from '@/api/client'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ThemeSwitch } from '@/components/theme-switch'

export function PlatformListPage({ kind }: { kind: 'tasks' | 'apps' }) {
  const config = {
    tasks: {
      title: '发布任务',
      columns: ['app_name', 'version', 'status', 'current_step'],
    },
    apps: {
      title: '应用管理',
      columns: ['app_name', 'deployment_name', 'namespace', 'match_keyword'],
    },
  }[kind]
  const query = useQuery({
    queryKey: [kind],
    queryFn: async () => ({
      data: (
        await (kind === 'apps' ? platformApi.apps() : platformApi.logs())
      ).data.map((row) => ({ ...row })),
    }),
  })
  const rows = (query.data?.data ?? []) as Record<string, unknown>[]
  return (
    <>
      <Header fixed>
        <div className='me-auto flex items-center gap-2 text-sm'>
          <span className='text-[#6f88ac]'>首页</span>
          <span className='text-[#b0bfd4]'>/</span>
          <span className='font-semibold text-[#214b8d]'>{config.title}</span>
        </div>
        <ThemeSwitch />
      </Header>
      <Main>
        <div className='mb-6'>
          <h1 className='text-2xl font-bold tracking-tight text-[#1c355e]'>
            {config.title}
          </h1>
          <p className='mt-1 text-sm text-muted-foreground'>
            查看平台中的{kind === 'apps' ? '应用配置' : '发布执行记录'}。
          </p>
        </div>
        <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.06)]'>
          <CardHeader>
            <CardTitle className='text-base text-[#1c355e]'>
              {config.title}列表
            </CardTitle>
          </CardHeader>
          <CardContent className='overflow-x-auto'>
            <table className='w-full text-sm'>
              <thead className='bg-[#f7f9fc]'>
                <tr className='border-b'>
                  {config.columns.map((column) => (
                    <th
                      key={column}
                      className='px-3 py-2 text-left text-xs font-semibold text-[#587092]'
                    >
                      {column}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row, index) => (
                  <tr
                    key={String(row.id ?? row.name ?? index)}
                    className='border-b border-[#edf1f7] last:border-0 hover:bg-[#f7faff]'
                  >
                    {config.columns.map((column) => (
                      <td key={column} className='px-3 py-3'>
                        {String(row[column] ?? '-')}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
            {rows.length === 0 && (
              <p className='py-8 text-center text-muted-foreground'>暂无数据</p>
            )}
          </CardContent>
        </Card>
      </Main>
    </>
  )
}
