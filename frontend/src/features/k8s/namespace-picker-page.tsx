import { useMemo, useState } from 'react'
import { Check, Search, SearchX, X } from 'lucide-react'
import type { Cluster, K8sObject } from '@/api/client'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'

type Props = {
  cluster: Cluster
  items: K8sObject[]
  loading: boolean
  error?: string
  onSelect: (namespace: string) => void
  onClose: () => void
}

const phaseOf = (item: K8sObject) => {
  const phase = item.status?.phase
  return typeof phase === 'string' && phase ? phase : 'Active'
}

export function NamespacePickerDialog({
  cluster,
  items,
  loading,
  error,
  onSelect,
  onClose,
}: Props) {
  const [filter, setFilter] = useState('')
  const [showAll, setShowAll] = useState(true)
  const namespaces = useMemo(() => {
    const sorted = [...items]
      .filter(
        (item) => showAll || item.metadata?.name === cluster.default_namespace
      )
      .sort((a, b) =>
        (a.metadata?.name ?? '').localeCompare(b.metadata?.name ?? '')
      )
    if (!filter.trim()) return sorted
    return sorted.filter((item) =>
      (item.metadata?.name ?? '')
        .toLowerCase()
        .includes(filter.trim().toLowerCase())
    )
  }, [cluster.default_namespace, filter, items, showAll])

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent
        showCloseButton={false}
        className='max-h-[min(560px,calc(100vh-4rem))] w-[min(560px,calc(100vw-3rem))] overflow-hidden rounded-xl border-[#d8e2f0] bg-white p-0 shadow-xl'
      >
        <DialogTitle className='sr-only'>选择名称空间</DialogTitle>
        <DialogDescription className='sr-only'>
          为 {cluster.name} 选择要进入的 Kubernetes 名称空间。
        </DialogDescription>
        <div className='flex max-h-[min(560px,calc(100vh-4rem))] flex-col'>
          <div className='flex items-center justify-between gap-4 border-b border-[#e6edf6] px-5 py-4'>
            <div className='flex min-w-0 items-center gap-3'>
              <span className='flex size-8 shrink-0 items-center justify-center rounded-lg bg-[#285391] text-base font-semibold text-white'>
                K
              </span>
              <div className='min-w-0'>
                <h1 className='truncate text-base font-semibold tracking-tight text-[#1d355c]'>
                  {cluster.name}
                </h1>
                <p className='mt-0.5 text-xs text-[#8493aa]'>
                  选择名称空间进入集群控制台
                </p>
              </div>
            </div>
            <Button
              variant='ghost'
              size='icon'
              aria-label='关闭名称空间选择'
              className='size-8 shrink-0 rounded-full text-[#8493aa] hover:bg-[#eef3fa] hover:text-[#334b70]'
              onClick={onClose}
            >
              <X className='size-4' />
            </Button>
          </div>

          <div className='flex items-center gap-3 px-5 pt-4'>
            <label className='relative block flex-1'>
              <span className='sr-only'>按名称筛选</span>
              <Input
                autoFocus
                value={filter}
                onChange={(event) => setFilter(event.target.value)}
                placeholder='按名称筛选'
                className='h-9 w-full rounded-lg border-[#d5dfed] bg-[#fbfdff] pe-9 text-sm text-[#536074] placeholder:text-[#aeb8c8] focus-visible:border-[#7fa2d1] focus-visible:ring-[#dce8f8]'
              />
              <Search className='pointer-events-none absolute end-3 top-1/2 size-4 -translate-y-1/2 text-[#9caabd]' />
            </label>
            <label className='flex shrink-0 cursor-pointer items-center gap-2 text-sm text-[#536074]'>
              <input
                type='checkbox'
                checked={showAll}
                onChange={(event) => setShowAll(event.target.checked)}
                className='peer sr-only'
              />
              <span className='flex size-4 items-center justify-center rounded border border-[#9cb3d3] bg-white text-white transition peer-checked:border-[#285391] peer-checked:bg-[#285391]'>
                {showAll && <Check className='size-3' />}
              </span>
              显示所有
            </label>
          </div>

          <div className='min-h-0 flex-1 overflow-y-auto px-5 py-3'>
            {error ? (
              <div className='px-4 py-10 text-center text-sm text-destructive'>
                {error}
              </div>
            ) : loading ? (
              <div className='space-y-2 py-1'>
                {Array.from({ length: 5 }).map((_, index) => (
                  <div
                    key={index}
                    className='h-9 animate-pulse rounded-lg bg-[#f2f4f8]'
                  />
                ))}
              </div>
            ) : namespaces.length === 0 ? (
              <div className='flex flex-col items-center gap-2 px-4 py-10 text-center'>
                <SearchX className='size-6 text-[#aeb8c8]' />
                <p className='text-sm text-[#7e8796]'>暂无匹配的名称空间</p>
              </div>
            ) : (
              <div className='divide-y divide-[#eef2f8] rounded-lg border border-[#e6edf6]'>
                {namespaces.map((item) => {
                  const name = item.metadata?.name ?? ''
                  const phase = phaseOf(item)
                  const healthy = phase === 'Active'
                  return (
                    <button
                      key={name}
                      type='button'
                      aria-label={`选择名称空间 ${name}`}
                      onClick={() => onSelect(name)}
                      className='flex w-full items-center gap-2.5 px-3 py-2.5 text-left text-sm text-[#33415c] transition hover:bg-[#f7faff] focus-visible:bg-[#f7faff] focus-visible:outline-none'
                    >
                      <span
                        className={`size-2 shrink-0 rounded-full ${
                          healthy ? 'bg-[#12bd91]' : 'bg-[#f0a020]'
                        }`}
                      />
                      <span className='min-w-0 flex-1 truncate font-mono' title={name}>
                        {name}
                      </span>
                      {!healthy && (
                        <span className='shrink-0 rounded border border-[#f5d9a8] bg-[#fdf6ea] px-1.5 py-0.5 text-xs text-[#b57708]'>
                          {phase}
                        </span>
                      )}
                    </button>
                  )
                })}
              </div>
            )}
          </div>

          <div className='flex items-center justify-between gap-4 border-t border-[#eef2f8] px-5 py-3'>
            <span className='text-xs text-[#95a1b2]'>
              共 {namespaces.length} 个名称空间
            </span>
            <Button
              variant='outline'
              className='h-8 rounded-lg border-[#c6d5e9] px-3 text-sm text-[#31598e] hover:bg-[#f5f8fd]'
              onClick={onClose}
            >
              取消
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
