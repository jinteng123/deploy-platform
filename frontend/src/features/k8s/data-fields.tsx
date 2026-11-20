/* eslint-disable react-refresh/only-export-components */
import { useId, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { kvKeyValid, type KvRow } from './kv-rows-editor'

export const mapRows = (data: Record<string, string> = {}): KvRow[] =>
  Object.entries(data).map(([key, value]) => ({ key, value }))

export function rowsError(rows: KvRow[], metadata = false): string {
  const keys = new Set<string>()
  for (const { key } of rows) {
    if (!key) return '请先为新的条目设置键名'
    if (keys.has(key)) return `已经存在同名的键：${key}`
    keys.add(key)
    if (metadata) {
      const parts = key.split('/')
      const name = parts[parts.length - 1] ?? ''
      if (
        parts.length > 2 ||
        !/^[a-zA-Z0-9]([-._a-zA-Z0-9]*[a-zA-Z0-9])?$/.test(name) ||
        name.length > 63 ||
        (parts.length === 2 &&
          (!/^[a-z0-9]([-.a-z0-9]*[a-z0-9])?$/.test(parts[0]) ||
            parts[0].length > 253))
      ) {
        return `非法元数据键：${key}`
      }
    } else if (!kvKeyValid(key) || key.length > 253) {
      return '键只能包含字母、数字、-、_、.，长度不能超过 253'
    }
  }
  return ''
}

export function DataCode({
  value,
  onChange,
  label,
  disabled,
  tall = false,
}: {
  value: string
  onChange?: (value: string) => void
  label: string
  disabled?: boolean
  tall?: boolean
}) {
  return (
    <div
      className={cn(
        'flex min-w-0 overflow-hidden border border-[#dcdfe6] bg-[#fafafa] font-mono text-[13px] leading-6',
        tall && 'min-h-[calc(90vh-350px)]'
      )}
    >
      <div
        aria-hidden
        className='shrink-0 border-r border-[#ebeef5] bg-[#f5f5f5] px-3 py-2 text-right text-[#aaa] tabular-nums select-none'
      >
        {value.split('\n').map((_, i) => (
          <div key={i}>{i + 1}</div>
        ))}
      </div>
      {onChange ? (
        <Textarea
          aria-label={label}
          value={value}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          className={cn(
            'min-h-32 flex-1 resize-y rounded-none border-0 bg-transparent px-3 py-2 font-mono text-[13px] leading-6 shadow-none focus-visible:ring-1',
            tall && 'min-h-[calc(90vh-350px)]'
          )}
          spellCheck={false}
        />
      ) : (
        <pre
          aria-label={label}
          className='min-h-8 min-w-0 flex-1 overflow-auto px-3 py-2 break-all whitespace-pre-wrap'
        >
          {value}
        </pre>
      )}
    </div>
  )
}

// Kuboard defaults to a vertical list of data keys and a large code pane.
// Both layouts operate on the same draft so switching never loses edits.
export function DataFields({
  rows,
  onChange,
  editing,
  disabled,
  metadata = false,
  addLabel = '添加条目',
  empty = '暂无数据条目',
  fieldName = '元数据',
}: {
  rows: KvRow[]
  onChange: (rows: KvRow[]) => void
  editing: boolean
  disabled?: boolean
  metadata?: boolean
  addLabel?: string
  empty?: string
  // fieldName prefixes the row aria-labels (键/值) so the 标签 and 注解
  // field groups stay distinguishable to assistive tech — and to tests.
  fieldName?: string
}) {
  const id = useId()
  const [layout, setLayout] = useState<'entries' | 'tabs'>('tabs')
  const [selected, setSelected] = useState(0)
  const [error, setError] = useState('')
  const current = Math.min(selected, Math.max(0, rows.length - 1))
  const patch = (index: number, field: Partial<KvRow>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...field } : row)))
  const add = () => {
    const err = rowsError(rows, metadata)
    if (err) {
      setError(err)
      return
    }
    setError('')
    onChange([...rows, { key: '', value: '' }])
    setSelected(rows.length)
  }
  const select = (index: number) => {
    if (editing && index !== current && !rows[current]?.key) {
      setError('请先为新的条目设置键名')
      return
    }
    setError('')
    setSelected(index)
  }
  const remove = (index: number) => {
    onChange(rows.filter((_, i) => i !== index))
    if (index <= current) setSelected(Math.max(0, current - 1))
    setError('')
  }
  const item = (index: number, tall = false) => {
    const row = rows[index]
    return (
      <div className='min-w-0 flex-1'>
        {editing ? (
          <div className='mb-3 flex items-center gap-2'>
            <Input
              aria-label={`${metadata ? fieldName : ''}键 ${index + 1}`}
              placeholder='key'
              value={row.key}
              disabled={disabled}
              onChange={(e) => patch(index, { key: e.target.value })}
              className='h-8 max-w-md rounded font-mono text-xs'
            />
            <Button
              variant='ghost'
              size='icon'
              aria-label='删除此行'
              disabled={disabled}
              onClick={() => remove(index)}
              className='size-8 text-[#f44656]'
            >
              <Trash2 className='size-4' />
            </Button>
          </div>
        ) : (
          !tall && (
            <div className='mb-2 font-mono text-sm text-[#234883]'>
              {row.key}
            </div>
          )
        )}
        {metadata ? (
          // View mode never reaches item(): the metadata table returns early.
          <Textarea
            aria-label={`${fieldName}值 ${index + 1}`}
            value={row.value}
            disabled={disabled}
            onChange={(e) => patch(index, { value: e.target.value })}
            className='rounded text-sm'
          />
        ) : (
          <DataCode
            value={row.value}
            label={`值 ${index + 1}`}
            disabled={disabled}
            tall={tall}
            onChange={editing ? (value) => patch(index, { value }) : undefined}
          />
        )}
      </div>
    )
  }
  const tabs = !metadata && layout === 'tabs'
  // Metadata in view mode is a compact kv table — key column pinned and
  // shaded, values wrap; the row layout below only serves editing.
  if (metadata && !editing) {
    return (
      <section className='min-w-0 overflow-hidden rounded border border-[#ebeef5] bg-white'>
        {rows.length === 0 ? (
          <p className='px-5 py-4 text-center text-xs text-[#909399]'>
            {empty}
          </p>
        ) : (
          <table className='w-full text-xs'>
            <tbody>
              {rows.map((row, i) => (
                <tr
                  key={i}
                  className='border-b border-[#ebeef5] last:border-0'
                >
                  <td className='w-44 bg-[#fafbfc] px-4 py-2.5 align-top font-mono break-all text-[#31598e]'>
                    {row.key}
                  </td>
                  <td className='px-4 py-2.5 break-words text-[#303133]'>
                    {row.value || '—'}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    )
  }
  return (
    <section className='min-w-0 border border-[#ebeef5] bg-white'>
      {!metadata && (
        <div className='flex flex-wrap items-center justify-between gap-2 border-b border-[#ebeef5] bg-[#e9edf3] px-5 py-3'>
          <div className='inline-flex overflow-hidden rounded border border-[#dcdfe6]'>
            {(['entries', 'tabs'] as const).map((mode) => (
              <button
                type='button'
                key={mode}
                aria-pressed={layout === mode}
                onClick={() => setLayout(mode)}
                className={cn(
                  'px-4 py-2 text-xs transition-colors',
                  layout === mode
                    ? 'bg-[#234883] text-white'
                    : 'bg-white text-[#606266] hover:text-[#234883]'
                )}
              >
                {mode === 'entries' ? '条目模式' : '标签页模式'}
              </button>
            ))}
          </div>
        </div>
      )}
      {error && (
        <p role='alert' className='px-5 pt-3 text-xs text-[#f44656]'>
          {error}
        </p>
      )}
      <div className={cn('p-5', tabs && 'flex min-h-72 gap-5 max-sm:flex-col')}>
        {tabs && (
          <div
            role='tablist'
            aria-label='数据条目'
            aria-orientation='vertical'
            className='flex w-40 shrink-0 flex-col border-r-2 border-[#e4e7ed] max-sm:w-full max-sm:flex-row max-sm:overflow-x-auto max-sm:border-r-0 max-sm:border-b'
          >
            {rows.map((row, i) => (
              <button
                key={i}
                type='button'
                role='tab'
                id={`${id}-tab-${i}`}
                aria-controls={`${id}-panel`}
                aria-selected={i === current}
                onClick={() => select(i)}
                className={cn(
                  '-mr-0.5 border-r-2 px-3 py-2 text-left font-mono text-xs break-all max-sm:shrink-0 max-sm:whitespace-nowrap',
                  i === current
                    ? 'border-[#234883] font-semibold text-[#234883]'
                    : 'border-transparent text-[#606266] hover:text-[#234883]'
                )}
              >
                {row.key || '新条目'}
              </button>
            ))}
            {editing && (
              <Button
                variant='ghost'
                disabled={disabled}
                onClick={add}
                className='mt-2 shrink-0 justify-start px-2 text-xs whitespace-nowrap text-[#234883] max-sm:mt-0'
              >
                <Plus className='size-3.5' />
                {addLabel}
              </Button>
            )}
          </div>
        )}
        {rows.length === 0 ? (
          <p className='flex-1 py-10 text-center text-sm text-[#909399]'>
            {empty}
          </p>
        ) : tabs ? (
          <div
            role='tabpanel'
            id={`${id}-panel`}
            aria-labelledby={`${id}-tab-${current}`}
            className='min-w-0 flex-1'
          >
            {item(current, true)}
          </div>
        ) : (
          <div className='w-full space-y-4'>
            {rows.map((_, i) => (
              <div
                key={i}
                className='border-b border-[#ebeef5] pb-4 last:border-0'
              >
                {item(i)}
              </div>
            ))}
          </div>
        )}
      </div>
      {editing && !tabs && (
        <Button
          variant='ghost'
          disabled={disabled}
          onClick={add}
          className='mx-5 mb-4 text-xs text-[#234883]'
        >
          <Plus className='size-3.5' />
          {addLabel}
        </Button>
      )}
    </section>
  )
}
