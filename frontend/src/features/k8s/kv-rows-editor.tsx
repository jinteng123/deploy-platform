/* eslint-disable react-refresh/only-export-components */
import { Plus, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

// One editable line of a ConfigMap `data` / Secret `stringData` map.
export type KvRow = { key: string; value: string }

// ConfigMap.data and Secret.data/stringData keys share the same rule:
// alphanumeric characters plus - _ . — validated live per row so a bad
// key never reaches the manifest.
const KEY_RE = /^[-._a-zA-Z0-9]+$/
export const KV_KEY_ERROR = '键只能包含字母、数字、-、_、.'
export const kvKeyValid = (key: string): boolean => KEY_RE.test(key)

// kvRowsToData folds editor rows into the manifest map — last write wins
// on duplicate keys, mirroring YAML map semantics. Rows still carrying an
// empty key are drafts and skipped (kvRowsInvalid gates submit on them
// when a value was typed).
export function kvRowsToData(rows: KvRow[]): Record<string, string> {
  const data: Record<string, string> = {}
  for (const r of rows) if (r.key !== '') data[r.key] = r.value
  return data
}

// kvRowsInvalid reports whether any touched row has a bad key: a
// non-empty key failing KEY_RE, or a value typed against an empty key.
// Untouched all-empty rows are drafts, not errors.
export function kvRowsInvalid(rows: KvRow[]): boolean {
  return rows.some(
    (r) => (r.key !== '' || r.value !== '') && !kvKeyValid(r.key)
  )
}

// KvRowsEditor is the shared key/value data-rows editor of the
// ConfigMap and Secret create dialogs: one rounded group per row (key
// input + multi-line mono value), inline key validation, dashed
// "add row" footer button.
export function KvRowsEditor({
  rows,
  onChange,
  valuePlaceholder = '值',
  valueMono = true,
  disabled,
  existingKeys,
  lockedValuePlaceholder,
}: {
  rows: KvRow[]
  onChange: (rows: KvRow[]) => void
  valuePlaceholder?: string
  // valueMono renders the value textarea in monospace (config snippets,
  // certificates) — on by default.
  valueMono?: boolean
  disabled?: boolean
  // existingKeys marks rows whose key already lives on the object (the
  // edit path): their key input becomes read-only — a rename is expressed
  // as delete + add — and the value field shows lockedValuePlaceholder so
  // an untouched row means "keep the stored value", not "set to empty".
  existingKeys?: ReadonlySet<string>
  lockedValuePlaceholder?: string
}) {
  const update = (index: number, patch: Partial<KvRow>) =>
    onChange(rows.map((r, i) => (i === index ? { ...r, ...patch } : r)))
  const remove = (index: number) => onChange(rows.filter((_, i) => i !== index))

  return (
    <div className='space-y-2'>
      {rows.length === 0 && (
        <p className='py-1 text-center text-xs text-[#8ba3c7]'>
          暂无数据项，点击下方按钮添加
        </p>
      )}
      {rows.map((row, i) => {
        const locked = existingKeys?.has(row.key) === true
        const bad = row.key !== '' && !kvKeyValid(row.key)
        return (
          <div
            key={i}
            className='space-y-2 rounded-xl border border-[#e3eaf4] bg-[#fbfdff] p-3'
          >
            <div className='flex items-start gap-2'>
              <Input
                aria-label={`键 ${i + 1}`}
                placeholder='键'
                disabled={disabled}
                readOnly={locked}
                value={row.key}
                onChange={(e) => update(i, { key: e.target.value })}
                className={cn(
                  'h-9 flex-1 rounded-lg border-[#d6e1f0] bg-white font-mono text-xs focus-visible:ring-[#234883]/20',
                  bad && 'border-[#a52f3d] focus-visible:ring-[#a52f3d]/20',
                  locked && 'border-transparent bg-[#eef3fa] text-[#31598e]'
                )}
              />
              <Button
                type='button'
                variant='ghost'
                size='icon'
                disabled={disabled}
                aria-label='删除此行'
                onClick={() => remove(i)}
                className='size-8 shrink-0 text-[#8ba3c7] hover:bg-[#fdf0f1] hover:text-[#a52f3d]'
              >
                <Trash2 className='size-4' />
              </Button>
            </div>
            {bad && <p className='text-xs text-[#a52f3d]'>{KV_KEY_ERROR}</p>}
            <Textarea
              aria-label={`值 ${i + 1}`}
              placeholder={
                locked && row.value === ''
                  ? (lockedValuePlaceholder ?? valuePlaceholder)
                  : valuePlaceholder
              }
              disabled={disabled}
              value={row.value}
              onChange={(e) => update(i, { value: e.target.value })}
              className={cn(
                'min-h-[72px] resize-y rounded-lg border-[#d6e1f0] bg-white text-xs focus-visible:ring-[#234883]/20',
                valueMono && 'font-mono'
              )}
            />
          </div>
        )
      })}
      <button
        type='button'
        disabled={disabled}
        onClick={() => onChange([...rows, { key: '', value: '' }])}
        className='flex h-9 w-full items-center justify-center gap-1.5 rounded-lg border border-dashed border-[#d6e1f0] text-xs font-medium text-[#31598e] transition hover:border-[#9bb6dd] hover:bg-[#f5f8fd] focus-visible:ring-2 focus-visible:ring-[#234883]/40 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50'
      >
        <Plus className='size-3.5' />
        添加一行
      </button>
    </div>
  )
}
